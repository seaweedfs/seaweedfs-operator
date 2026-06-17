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

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func newRestoreReconciler(t *testing.T, objs ...client.Object) *SeaweedRestoreReconciler {
	t.Helper()
	scheme := backupTestScheme(t)
	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&seaweedv1.SeaweedRestore{}, &batchv1.Job{}).
		Build()
	return &SeaweedRestoreReconciler{
		Client:   cli,
		Log:      logf.Log,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(20),
	}
}

func completedBackup(name string) *seaweedv1.SeaweedBackup {
	return &seaweedv1.SeaweedBackup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns1"},
		Spec:       seaweedv1.SeaweedBackupSpec{ClusterName: "c1", StorageName: "pvc"},
		Status:     seaweedv1.SeaweedBackupStatus{Phase: seaweedv1.BackupPhaseCompleted},
	}
}

func TestRestoreFromBackupCreatesJobAndCompletes(t *testing.T) {
	restore := &seaweedv1.SeaweedRestore{
		ObjectMeta: metav1.ObjectMeta{Name: "rs1", Namespace: "ns1"},
		Spec:       seaweedv1.SeaweedRestoreSpec{ClusterName: "c1", BackupName: "bk1", FilerPath: "/"},
	}
	r := newRestoreReconciler(t, clusterWithFilesystemStorage(), completedBackup("bk1"), restore)
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns1", Name: "rs1"}}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	jobName := boundedName("rs1", "-rst")
	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Namespace: "ns1", Name: jobName}, &job); err != nil {
		t.Fatalf("expected restore job %q: %v", jobName, err)
	}
	cmd := job.Spec.Template.Spec.Containers[0].Command
	joined := cmd[len(cmd)-1]
	if !containsAll(joined, "fs.meta.load", "/backup/c1/bk1/filer.meta.gz") {
		t.Errorf("restore script does not load the expected snapshot:\n%s", joined)
	}

	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	if err := r.Status().Update(ctx, &job); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	var got seaweedv1.SeaweedRestore
	if err := r.Get(ctx, req.NamespacedName, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != seaweedv1.RestorePhaseCompleted {
		t.Fatalf("phase = %q, want Completed", got.Status.Phase)
	}
}

func TestRestoreWaitsForCompletedBackup(t *testing.T) {
	pending := &seaweedv1.SeaweedBackup{
		ObjectMeta: metav1.ObjectMeta{Name: "bk1", Namespace: "ns1"},
		Spec:       seaweedv1.SeaweedBackupSpec{ClusterName: "c1", StorageName: "pvc"},
		Status:     seaweedv1.SeaweedBackupStatus{Phase: seaweedv1.BackupPhaseRunning},
	}
	restore := &seaweedv1.SeaweedRestore{
		ObjectMeta: metav1.ObjectMeta{Name: "rs1", Namespace: "ns1"},
		Spec:       seaweedv1.SeaweedRestoreSpec{ClusterName: "c1", BackupName: "bk1"},
	}
	r := newRestoreReconciler(t, clusterWithFilesystemStorage(), pending, restore)
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns1", Name: "rs1"}}
	res, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter == 0 {
		t.Error("expected requeue while the source backup is not yet Completed")
	}
	var got seaweedv1.SeaweedRestore
	if err := r.Get(ctx, req.NamespacedName, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != seaweedv1.RestorePhasePending {
		t.Fatalf("phase = %q, want Pending", got.Status.Phase)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
