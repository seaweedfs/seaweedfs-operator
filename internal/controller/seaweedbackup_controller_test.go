/*


Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func backupTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := seaweedv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func clusterWithFilesystemStorage() *seaweedv1.Seaweed {
	return &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "ns1"},
		Spec: seaweedv1.SeaweedSpec{
			Image:  "chrislusf/seaweedfs:test",
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			Backup: &seaweedv1.BackupSpec{
				Storages: map[string]seaweedv1.BackupStorageSpec{
					"pvc": {
						Type:       seaweedv1.BackupStorageFilesystem,
						Filesystem: &seaweedv1.FilesystemBackupStore{ExistingClaim: "backup-pvc", MountPath: "/backup"},
					},
				},
			},
		},
	}
}

func newBackupReconciler(t *testing.T, objs ...client.Object) *SeaweedBackupReconciler {
	t.Helper()
	scheme := backupTestScheme(t)
	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&seaweedv1.SeaweedBackup{}, &batchv1.Job{}).
		Build()
	return &SeaweedBackupReconciler{
		Client:   cli,
		Log:      logf.Log,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(20),
	}
}

func TestBackupReconcileCreatesJobAndCompletes(t *testing.T) {
	backup := &seaweedv1.SeaweedBackup{
		ObjectMeta: metav1.ObjectMeta{Name: "bk1", Namespace: "ns1"},
		Spec:       seaweedv1.SeaweedBackupSpec{ClusterName: "c1", StorageName: "pvc", FilerPath: "/"},
	}
	r := newBackupReconciler(t, clusterWithFilesystemStorage(), backup)
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns1", Name: "bk1"}}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	jobName := boundedName("bk1", "-bkp")
	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Namespace: "ns1", Name: jobName}, &job); err != nil {
		t.Fatalf("expected snapshot job %q: %v", jobName, err)
	}
	if len(job.OwnerReferences) == 0 || job.OwnerReferences[0].Name != "bk1" {
		t.Errorf("job is not owned by the backup: %+v", job.OwnerReferences)
	}

	var got seaweedv1.SeaweedBackup
	if err := r.Get(ctx, req.NamespacedName, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != seaweedv1.BackupPhaseRunning {
		t.Fatalf("phase after create = %q, want Running", got.Status.Phase)
	}
	if got.Status.JobName != jobName {
		t.Errorf("status.jobName = %q", got.Status.JobName)
	}

	// Drive the Job to success and reconcile again.
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	if err := r.Status().Update(ctx, &job); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if err := r.Get(ctx, req.NamespacedName, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != seaweedv1.BackupPhaseCompleted {
		t.Fatalf("phase after job complete = %q, want Completed", got.Status.Phase)
	}
	if got.Status.CompletionTime == nil {
		t.Error("completionTime not set")
	}
}

func TestBackupReconcileJobFailure(t *testing.T) {
	backup := &seaweedv1.SeaweedBackup{
		ObjectMeta: metav1.ObjectMeta{Name: "bk1", Namespace: "ns1"},
		Spec:       seaweedv1.SeaweedBackupSpec{ClusterName: "c1", StorageName: "pvc"},
	}
	r := newBackupReconciler(t, clusterWithFilesystemStorage(), backup)
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns1", Name: "bk1"}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}

	var job batchv1.Job
	jobName := boundedName("bk1", "-bkp")
	if err := r.Get(ctx, types.NamespacedName{Namespace: "ns1", Name: jobName}, &job); err != nil {
		t.Fatal(err)
	}
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}
	if err := r.Status().Update(ctx, &job); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	var got seaweedv1.SeaweedBackup
	if err := r.Get(ctx, req.NamespacedName, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != seaweedv1.BackupPhaseFailed {
		t.Fatalf("phase = %q, want Failed", got.Status.Phase)
	}
}

func TestBackupReconcileMissingCluster(t *testing.T) {
	backup := &seaweedv1.SeaweedBackup{
		ObjectMeta: metav1.ObjectMeta{Name: "bk1", Namespace: "ns1"},
		Spec:       seaweedv1.SeaweedBackupSpec{ClusterName: "ghost", StorageName: "pvc"},
	}
	r := newBackupReconciler(t, backup)
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns1", Name: "bk1"}}
	res, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter == 0 {
		t.Error("expected a requeue while the cluster is missing")
	}
	var got seaweedv1.SeaweedBackup
	if err := r.Get(ctx, req.NamespacedName, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != seaweedv1.BackupPhasePending {
		t.Fatalf("phase = %q, want Pending", got.Status.Phase)
	}
}

func TestBackupReconcileMissingStorage(t *testing.T) {
	backup := &seaweedv1.SeaweedBackup{
		ObjectMeta: metav1.ObjectMeta{Name: "bk1", Namespace: "ns1"},
		Spec:       seaweedv1.SeaweedBackupSpec{ClusterName: "c1", StorageName: "nope"},
	}
	r := newBackupReconciler(t, clusterWithFilesystemStorage(), backup)
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns1", Name: "bk1"}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	var got seaweedv1.SeaweedBackup
	if err := r.Get(ctx, req.NamespacedName, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != seaweedv1.BackupPhasePending {
		t.Fatalf("phase = %q, want Pending", got.Status.Phase)
	}
}

func TestBackupReconcileExistingJobPopulatesStatus(t *testing.T) {
	backup := &seaweedv1.SeaweedBackup{
		ObjectMeta: metav1.ObjectMeta{Name: "bk1", Namespace: "ns1", Generation: 3},
		Spec:       seaweedv1.SeaweedBackupSpec{ClusterName: "c1", StorageName: "pvc", FilerPath: "/"},
	}
	r := newBackupReconciler(t, clusterWithFilesystemStorage(), backup)
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns1", Name: "bk1"}}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	jobName := boundedName("bk1", "-bkp")
	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Namespace: "ns1", Name: jobName}, &job); err != nil {
		t.Fatal(err)
	}
	jobStart := metav1.NewTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	job.Status.StartTime = &jobStart
	if err := r.Status().Update(ctx, &job); err != nil {
		t.Fatal(err)
	}
	wantDest := job.Annotations[seaweedv1.AnnotationBackupDestination]
	if wantDest == "" {
		t.Fatal("snapshot job carries no destination annotation")
	}

	// Simulate a status update lost after Job creation: the Job exists but
	// the backup status was never persisted.
	wipeStatus := func() {
		t.Helper()
		var cur seaweedv1.SeaweedBackup
		if err := r.Get(ctx, req.NamespacedName, &cur); err != nil {
			t.Fatal(err)
		}
		cur.Status = seaweedv1.SeaweedBackupStatus{}
		if err := r.Status().Update(ctx, &cur); err != nil {
			t.Fatal(err)
		}
	}
	checkStatus := func(phase seaweedv1.BackupPhase) {
		t.Helper()
		var got seaweedv1.SeaweedBackup
		if err := r.Get(ctx, req.NamespacedName, &got); err != nil {
			t.Fatal(err)
		}
		if got.Status.Phase != phase {
			t.Errorf("phase = %q, want %q", got.Status.Phase, phase)
		}
		if got.Status.JobName != jobName {
			t.Errorf("jobName = %q, want %q", got.Status.JobName, jobName)
		}
		if got.Status.Destination != wantDest {
			t.Errorf("destination = %q, want %q", got.Status.Destination, wantDest)
		}
		if got.Status.StartTime == nil || !got.Status.StartTime.Equal(&jobStart) {
			t.Errorf("startTime = %v, want %v", got.Status.StartTime, jobStart)
		}
		if got.Status.ObservedGeneration != 3 {
			t.Errorf("observedGeneration = %d, want 3", got.Status.ObservedGeneration)
		}
		if !meta.IsStatusConditionTrue(got.Status.Conditions, seaweedv1.BackupConditionClusterReachable) {
			t.Error("ClusterReachable condition not populated")
		}
	}

	wipeStatus()
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile existing job: %v", err)
	}
	checkStatus(seaweedv1.BackupPhaseRunning)

	if err := r.Get(ctx, types.NamespacedName{Namespace: "ns1", Name: jobName}, &job); err != nil {
		t.Fatal(err)
	}
	jobEnd := metav1.NewTime(jobStart.Add(time.Hour))
	job.Status.CompletionTime = &jobEnd
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	if err := r.Status().Update(ctx, &job); err != nil {
		t.Fatal(err)
	}
	wipeStatus()
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile finished job: %v", err)
	}
	checkStatus(seaweedv1.BackupPhaseCompleted)

	var got seaweedv1.SeaweedBackup
	if err := r.Get(ctx, req.NamespacedName, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.CompletionTime == nil || !got.Status.CompletionTime.Equal(&jobEnd) {
		t.Errorf("completionTime = %v, want %v", got.Status.CompletionTime, jobEnd)
	}
}

func TestBackupReconcileTerminalIncompleteStatus(t *testing.T) {
	backup := &seaweedv1.SeaweedBackup{
		ObjectMeta: metav1.ObjectMeta{Name: "bk1", Namespace: "ns1", Generation: 2},
		Spec:       seaweedv1.SeaweedBackupSpec{ClusterName: "c1", StorageName: "pvc"},
	}
	r := newBackupReconciler(t, clusterWithFilesystemStorage(), backup)
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns1", Name: "bk1"}}
	jobName := boundedName("bk1", "-bkp")

	setPhase := func(phase seaweedv1.BackupPhase) {
		t.Helper()
		var cur seaweedv1.SeaweedBackup
		if err := r.Get(ctx, req.NamespacedName, &cur); err != nil {
			t.Fatal(err)
		}
		cur.Status = seaweedv1.SeaweedBackupStatus{Phase: phase}
		if err := r.Status().Update(ctx, &cur); err != nil {
			t.Fatal(err)
		}
	}
	getBackup := func() seaweedv1.SeaweedBackup {
		t.Helper()
		var got seaweedv1.SeaweedBackup
		if err := r.Get(ctx, req.NamespacedName, &got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Namespace: "ns1", Name: jobName}, &job); err != nil {
		t.Fatal(err)
	}
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	if err := r.Status().Update(ctx, &job); err != nil {
		t.Fatal(err)
	}

	// A Completed backup with wiped fields heals from the finished Job.
	setPhase(seaweedv1.BackupPhaseCompleted)
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	got := getBackup()
	if got.Status.Phase != seaweedv1.BackupPhaseCompleted {
		t.Fatalf("phase = %q, want Completed", got.Status.Phase)
	}
	if got.Status.JobName != jobName || got.Status.Destination == "" ||
		got.Status.StartTime == nil || got.Status.CompletionTime == nil ||
		got.Status.ObservedGeneration != 2 {
		t.Fatalf("status not fully restored: %+v", got.Status)
	}

	// With the Job gone, reconciliation must not run another snapshot.
	setPhase(seaweedv1.BackupPhaseCompleted)
	if err := r.Delete(ctx, &job); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, types.NamespacedName{Namespace: "ns1", Name: jobName}, &job); !apierrors.IsNotFound(err) {
		t.Fatalf("expected no replacement job, got %v", err)
	}
	if got := getBackup(); got.Status.Phase != seaweedv1.BackupPhaseCompleted {
		t.Fatalf("phase = %q, want Completed", got.Status.Phase)
	}

	// With the cluster gone, pending must not demote the terminal phase.
	var cluster seaweedv1.Seaweed
	if err := r.Get(ctx, types.NamespacedName{Namespace: "ns1", Name: "c1"}, &cluster); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, &cluster); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	got = getBackup()
	if got.Status.Phase != seaweedv1.BackupPhaseCompleted {
		t.Fatalf("phase = %q, want Completed", got.Status.Phase)
	}
	if meta.IsStatusConditionTrue(got.Status.Conditions, seaweedv1.BackupConditionClusterReachable) {
		t.Error("ClusterReachable should be false while the cluster is missing")
	}
}

func TestBackupDeletionRetainReleasesFinalizer(t *testing.T) {
	now := metav1.Now()
	backup := &seaweedv1.SeaweedBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name: "bk1", Namespace: "ns1",
			Finalizers:        []string{BackupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: seaweedv1.SeaweedBackupSpec{ClusterName: "c1", StorageName: "pvc"},
		Status: seaweedv1.SeaweedBackupStatus{
			Phase:       seaweedv1.BackupPhaseCompleted,
			JobName:     "bk1-bkp",
			Destination: "/backup/c1/bk1/filer.meta.gz",
		},
	}
	r := newBackupReconciler(t, clusterWithFilesystemStorage(), backup)
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns1", Name: "bk1"}}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	// Once the last finalizer is dropped the CR is gone.
	var got seaweedv1.SeaweedBackup
	if err := r.Get(ctx, req.NamespacedName, &got); !apierrors.IsNotFound(err) {
		t.Fatalf("expected the backup to be deleted, got %v", err)
	}
	var job batchv1.Job
	err := r.Get(ctx, types.NamespacedName{Namespace: "ns1", Name: boundedName("bk1", "-cln")}, &job)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("no cleanup job expected for Retain, got %v", err)
	}
}

func TestBackupDeletionDeleteCleansArchive(t *testing.T) {
	now := metav1.Now()
	backup := &seaweedv1.SeaweedBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name: "bk1", Namespace: "ns1",
			Finalizers:        []string{BackupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: seaweedv1.SeaweedBackupSpec{
			ClusterName: "c1", StorageName: "pvc",
			ReclaimPolicy: seaweedv1.BackupReclaimDelete,
		},
		Status: seaweedv1.SeaweedBackupStatus{
			Phase:       seaweedv1.BackupPhaseCompleted,
			JobName:     "bk1-bkp",
			Destination: "/backup/c1/bk1/filer.meta.gz",
		},
	}
	r := newBackupReconciler(t, clusterWithFilesystemStorage(), backup)
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns1", Name: "bk1"}}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	jobName := boundedName("bk1", "-cln")
	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Namespace: "ns1", Name: jobName}, &job); err != nil {
		t.Fatalf("expected cleanup job %q: %v", jobName, err)
	}
	// The cleanup Job must not be owned by the deleting CR, or a foreground
	// deletion would tear it down mid-run.
	if len(job.OwnerReferences) != 0 {
		t.Errorf("cleanup job must not be owned by the backup: %+v", job.OwnerReferences)
	}
	cmd := job.Spec.Template.Spec.Containers[0].Command
	if len(cmd) < 3 || !strings.Contains(cmd[2], "rm -f '/backup/c1/bk1/filer.meta.gz'") {
		t.Errorf("cleanup job command does not remove the archive: %v", cmd)
	}
	var vol *corev1.Volume
	for i := range job.Spec.Template.Spec.Volumes {
		if job.Spec.Template.Spec.Volumes[i].PersistentVolumeClaim != nil {
			vol = &job.Spec.Template.Spec.Volumes[i]
		}
	}
	if vol == nil || vol.PersistentVolumeClaim.ClaimName != "backup-pvc" {
		t.Errorf("cleanup job does not mount the backup claim")
	}

	// The CR stays until the cleanup Job completes.
	var got seaweedv1.SeaweedBackup
	if err := r.Get(ctx, req.NamespacedName, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Finalizers) == 0 {
		t.Fatal("finalizer released before cleanup finished")
	}

	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	if err := r.Status().Update(ctx, &job); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, req.NamespacedName, &got); !apierrors.IsNotFound(err) {
		t.Fatalf("expected the backup to be deleted after cleanup, got %v", err)
	}
}

func TestBackupDeletionDeleteStopsRunningSnapshot(t *testing.T) {
	now := metav1.Now()
	backup := &seaweedv1.SeaweedBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name: "bk1", Namespace: "ns1",
			Finalizers:        []string{BackupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: seaweedv1.SeaweedBackupSpec{
			ClusterName: "c1", StorageName: "pvc",
			ReclaimPolicy: seaweedv1.BackupReclaimDelete,
		},
		Status: seaweedv1.SeaweedBackupStatus{
			Phase:   seaweedv1.BackupPhaseRunning,
			JobName: "bk1-bkp",
		},
	}
	snap := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "bk1-bkp", Namespace: "ns1"}}
	r := newBackupReconciler(t, clusterWithFilesystemStorage(), backup, snap)
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns1", Name: "bk1"}}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	var got batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Namespace: "ns1", Name: "bk1-bkp"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Suspend == nil || !*got.Spec.Suspend {
		t.Fatal("snapshot job not suspended")
	}
	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Namespace: "ns1", Name: boundedName("bk1", "-cln")}, &job); !apierrors.IsNotFound(err) {
		t.Fatalf("cleanup must wait for the snapshot pods, got %v", err)
	}

	// Cleanup waits while the snapshot still has active pods.
	got.Status.Active = 1
	if err := r.Status().Update(ctx, &got); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, types.NamespacedName{Namespace: "ns1", Name: boundedName("bk1", "-cln")}, &job); !apierrors.IsNotFound(err) {
		t.Fatalf("cleanup must wait for active snapshot pods, got %v", err)
	}

	got.Status.Active = 0
	if err := r.Status().Update(ctx, &got); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, types.NamespacedName{Namespace: "ns1", Name: boundedName("bk1", "-cln")}, &job); err != nil {
		t.Fatalf("expected cleanup job after the snapshot pods stopped: %v", err)
	}
}

func TestBackupDeletionRetainLeavesForeignCleanupJob(t *testing.T) {
	now := metav1.Now()
	backup := &seaweedv1.SeaweedBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name: "bk1", Namespace: "ns1", UID: "uid-bk1",
			Finalizers:        []string{BackupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: seaweedv1.SeaweedBackupSpec{ClusterName: "c1", StorageName: "pvc"},
	}
	foreign := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: cleanupJobName(backup), Namespace: "ns1"}}
	r := newBackupReconciler(t, clusterWithFilesystemStorage(), backup, foreign)
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns1", Name: "bk1"}}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	var got seaweedv1.SeaweedBackup
	if err := r.Get(ctx, req.NamespacedName, &got); !apierrors.IsNotFound(err) {
		t.Fatalf("expected the backup to be deleted, got %v", err)
	}
	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Namespace: "ns1", Name: cleanupJobName(backup)}, &job); err != nil {
		t.Fatalf("unrelated cleanup-named job must not be deleted: %v", err)
	}
}

func TestBackupDeletionDeleteIgnoresForeignCleanupJob(t *testing.T) {
	now := metav1.Now()
	backup := &seaweedv1.SeaweedBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name: "bk1", Namespace: "ns1", UID: "uid-bk1",
			Finalizers:        []string{BackupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: seaweedv1.SeaweedBackupSpec{
			ClusterName: "c1", StorageName: "pvc",
			ReclaimPolicy: seaweedv1.BackupReclaimDelete,
		},
		Status: seaweedv1.SeaweedBackupStatus{
			Phase:       seaweedv1.BackupPhaseCompleted,
			JobName:     "bk1-bkp",
			Destination: "/backup/c1/bk1/filer.meta.gz",
		},
	}
	foreign := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: cleanupJobName(backup), Namespace: "ns1",
			Labels: map[string]string{seaweedv1.LabelBackupUID: "uid-other"},
		},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
		},
	}
	r := newBackupReconciler(t, clusterWithFilesystemStorage(), backup, foreign)
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns1", Name: "bk1"}}

	// A completed foreign Job holding the UID-keyed name is neither adopted
	// as our cleanup nor deleted.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	var got seaweedv1.SeaweedBackup
	if err := r.Get(ctx, req.NamespacedName, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Finalizers) == 0 {
		t.Fatal("finalizer released on a foreign cleanup job's success")
	}
	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Namespace: "ns1", Name: cleanupJobName(backup)}, &job); err != nil {
		t.Fatalf("unrelated cleanup-named job must not be deleted: %v", err)
	}
	if err := r.Delete(ctx, &job); err != nil {
		t.Fatal(err)
	}

	// Once the name frees up, our own cleanup Job is created.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, types.NamespacedName{Namespace: "ns1", Name: cleanupJobName(backup)}, &job); err != nil {
		t.Fatalf("expected own cleanup job: %v", err)
	}
	if got := job.Labels[seaweedv1.LabelBackupUID]; got != "uid-bk1" {
		t.Fatalf("cleanup job uid label = %q, want uid-bk1", got)
	}
}

func TestBackupFinalizerAddedForDeletePolicy(t *testing.T) {
	backup := &seaweedv1.SeaweedBackup{
		ObjectMeta: metav1.ObjectMeta{Name: "bk1", Namespace: "ns1"},
		Spec: seaweedv1.SeaweedBackupSpec{
			ClusterName: "c1", StorageName: "pvc",
			ReclaimPolicy: seaweedv1.BackupReclaimDelete,
		},
	}
	r := newBackupReconciler(t, clusterWithFilesystemStorage(), backup)
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns1", Name: "bk1"}}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	var got seaweedv1.SeaweedBackup
	if err := r.Get(ctx, req.NamespacedName, &got); err != nil {
		t.Fatal(err)
	}
	if !controllerutil.ContainsFinalizer(&got, BackupFinalizer) {
		t.Error("finalizer not added for reclaimPolicy Delete")
	}
}
