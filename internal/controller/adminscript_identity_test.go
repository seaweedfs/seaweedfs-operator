package controller

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func identityReconciler(t *testing.T, objs ...client.Object) *AdminScriptReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{seaweedv1.AddToScheme, batchv1.AddToScheme, corev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatalf("add scheme: %v", err)
		}
	}
	return &AdminScriptReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).
			WithStatusSubresource(&seaweedv1.AdminScript{}, &batchv1.CronJob{}).WithObjects(objs...).Build(),
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(20),
	}
}

func reconcileScript(t *testing.T, r *AdminScriptReconciler) (*seaweedv1.AdminScript, *batchv1.CronJob) {
	t.Helper()
	ctx := context.Background()
	key := types.NamespacedName{Namespace: "default", Name: "nightly-balance"}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	var script seaweedv1.AdminScript
	if err := r.Get(ctx, key, &script); err != nil {
		t.Fatalf("get script: %v", err)
	}
	var cronJob batchv1.CronJob
	if err := r.Get(ctx, key, &cronJob); err != nil {
		return &script, nil
	}
	return &script, &cronJob
}

func scriptReadyReason(script *seaweedv1.AdminScript) string {
	if c := meta.FindStatusCondition(script.Status.Conditions, seaweedv1.AdminScriptConditionReady); c != nil {
		return c.Reason
	}
	return ""
}

func serviceAccount(name string, allowed bool) *corev1.ServiceAccount {
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name}}
	if allowed {
		sa.Annotations = map[string]string{seaweedv1.ServiceAccountAllowedAnnotation: "true"}
	}
	return sa
}

func TestReconcileChecksWorkloadIdentity(t *testing.T) {
	cases := []struct {
		name       string
		image      string
		sa         string
		objs       []client.Object
		wantReason string
	}{
		{name: "defaults", wantReason: "Scheduled"},
		{name: "same repository, other tag", image: "docker.io/chrislusf/seaweedfs:4.01", wantReason: "Scheduled"},
		{name: "foreign image", image: "evil.example/shell:1", wantReason: "ImageDenied"},
		{name: "same name, other registry", image: "evil.example/chrislusf/seaweedfs:test", wantReason: "ImageDenied"},
		{name: "invalid name", sa: "Not_Valid", wantReason: "ServiceAccountDenied"},
		{name: "missing account", sa: "maintenance", wantReason: "ServiceAccountDenied"},
		{name: "unannotated account", sa: "maintenance", objs: []client.Object{serviceAccount("maintenance", false)}, wantReason: "ServiceAccountDenied"},
		{name: "annotated account", sa: "maintenance", objs: []client.Object{serviceAccount("maintenance", true)}, wantReason: "Scheduled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			script := testAdminScript()
			if tc.image != "" {
				script.Spec.Image = &tc.image
			}
			script.Spec.ServiceAccountName = tc.sa
			r := identityReconciler(t, append(tc.objs, testCluster(), script)...)
			got, cronJob := reconcileScript(t, r)
			if reason := scriptReadyReason(got); reason != tc.wantReason {
				t.Fatalf("Ready reason = %q, want %q", reason, tc.wantReason)
			}
			if scheduled := tc.wantReason == "Scheduled"; scheduled != (cronJob != nil) {
				t.Fatalf("CronJob exists = %v, want %v", cronJob != nil, scheduled)
			}
		})
	}
}

func TestRefusedScriptSuspendsExistingCronJob(t *testing.T) {
	ctx := context.Background()
	script := testAdminScript()
	script.UID = "script-uid"
	script.Spec.ServiceAccountName = "maintenance"
	sa := serviceAccount("maintenance", true)
	r := identityReconciler(t, testCluster(), script, sa)

	if _, cronJob := reconcileScript(t, r); cronJob == nil || (cronJob.Spec.Suspend != nil && *cronJob.Spec.Suspend) {
		t.Fatalf("accepted script should schedule an unsuspended CronJob, got %+v", cronJob)
	}

	sa.Annotations = nil
	if err := r.Update(ctx, sa); err != nil {
		t.Fatalf("drop annotation: %v", err)
	}
	got, cronJob := reconcileScript(t, r)
	if scriptReadyReason(got) != "ServiceAccountDenied" {
		t.Fatalf("Ready reason = %q, want ServiceAccountDenied", scriptReadyReason(got))
	}
	if cronJob == nil || cronJob.Spec.Suspend == nil || !*cronJob.Spec.Suspend {
		t.Fatalf("refused script must suspend its existing CronJob, got %+v", cronJob)
	}

	sa.Annotations = map[string]string{seaweedv1.ServiceAccountAllowedAnnotation: "true"}
	if err := r.Update(ctx, sa); err != nil {
		t.Fatalf("restore annotation: %v", err)
	}
	got, cronJob = reconcileScript(t, r)
	if scriptReadyReason(got) != "Scheduled" {
		t.Fatalf("Ready reason = %q, want Scheduled", scriptReadyReason(got))
	}
	if cronJob.Spec.Suspend != nil && *cronJob.Spec.Suspend {
		t.Fatal("accepted script must resume the CronJob it suspended")
	}
}

func TestRefusedScriptRestoresClusterPodLabels(t *testing.T) {
	ctx := context.Background()
	script := testAdminScript()
	script.UID = "script-uid"
	script.Spec.ServiceAccountName = "maintenance"
	script.Spec.ClusterPodLabels = map[string]string{"reboot-block": "adminscript"}
	sa := serviceAccount("maintenance", true)
	pod := clusterPod("master-0", "seaweed-sample", nil)
	r := identityReconciler(t, testCluster(), script, sa, pod)

	got, _ := reconcileScript(t, r)
	if err := r.syncClusterPodLabels(ctx, got, "seaweed-sample", true); err != nil {
		t.Fatalf("apply labels: %v", err)
	}
	podKey := client.ObjectKeyFromObject(pod)
	var labeled corev1.Pod
	if err := r.Get(ctx, podKey, &labeled); err != nil || labeled.Labels["reboot-block"] != "adminscript" {
		t.Fatalf("labels not applied: %v %v", err, labeled.Labels)
	}

	sa.Annotations = nil
	if err := r.Update(ctx, sa); err != nil {
		t.Fatalf("drop annotation: %v", err)
	}
	if got, _ = reconcileScript(t, r); scriptReadyReason(got) != "ServiceAccountDenied" {
		t.Fatalf("Ready reason = %q, want ServiceAccountDenied", scriptReadyReason(got))
	}
	var restored corev1.Pod
	if err := r.Get(ctx, podKey, &restored); err != nil {
		t.Fatalf("get pod: %v", err)
	}
	if _, ok := restored.Labels["reboot-block"]; ok {
		t.Fatalf("refused script with no active run must restore pod labels, got %v", restored.Labels)
	}
}

func TestRefusedScriptMirrorsCronJobStatus(t *testing.T) {
	ctx := context.Background()
	script := testAdminScript()
	script.UID = "script-uid"
	script.Spec.ServiceAccountName = "maintenance"
	r := identityReconciler(t, testCluster(), script, serviceAccount("maintenance", true))

	_, cronJob := reconcileScript(t, r)
	cronJob.Status.Active = []corev1.ObjectReference{{Name: "nightly-balance-1"}}
	if err := r.Status().Update(ctx, cronJob); err != nil {
		t.Fatalf("mark run active: %v", err)
	}
	if got, _ := reconcileScript(t, r); got.Status.Active != 1 {
		t.Fatalf("status.active = %d, want 1", got.Status.Active)
	}

	script.Spec.ServiceAccountName = "unapproved"
	var stored seaweedv1.AdminScript
	if err := r.Get(ctx, client.ObjectKeyFromObject(script), &stored); err != nil {
		t.Fatalf("get script: %v", err)
	}
	stored.Spec.ServiceAccountName = "unapproved"
	if err := r.Update(ctx, &stored); err != nil {
		t.Fatalf("refuse script: %v", err)
	}
	_, cronJob = reconcileScript(t, r)
	cronJob.Status.Active = nil
	if err := r.Status().Update(ctx, cronJob); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	got, _ := reconcileScript(t, r)
	if scriptReadyReason(got) != "ServiceAccountDenied" || got.Status.Active != 0 {
		t.Fatalf("refused script status = %q active=%d, want ServiceAccountDenied active=0", scriptReadyReason(got), got.Status.Active)
	}
}

func TestMissingClusterSuspendsCronJob(t *testing.T) {
	ctx := context.Background()
	script := testAdminScript()
	script.UID = "script-uid"
	cluster := testCluster()
	r := identityReconciler(t, cluster, script)

	if _, cronJob := reconcileScript(t, r); cronJob == nil {
		t.Fatal("accepted script should schedule a CronJob")
	}
	if err := r.Delete(ctx, cluster); err != nil {
		t.Fatalf("delete cluster: %v", err)
	}
	got, cronJob := reconcileScript(t, r)
	if scriptReadyReason(got) != "ClusterNotFound" || cronJob.Spec.Suspend == nil || !*cronJob.Spec.Suspend {
		t.Fatalf("missing cluster: reason %q suspend %v, want ClusterNotFound and a suspended CronJob", scriptReadyReason(got), cronJob.Spec.Suspend)
	}
}

func TestNamedServiceAccountIsRechecked(t *testing.T) {
	for _, sa := range []string{"", "maintenance"} {
		script := testAdminScript()
		script.Spec.ServiceAccountName = sa
		r := identityReconciler(t, testCluster(), script, serviceAccount("maintenance", true))
		res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(script)})
		if err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		if want := sa != ""; (res.RequeueAfter > 0) != want {
			t.Errorf("serviceAccountName %q: RequeueAfter = %v, want requeue %v", sa, res.RequeueAfter, want)
		}
	}
}
