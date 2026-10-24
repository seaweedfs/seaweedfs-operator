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
