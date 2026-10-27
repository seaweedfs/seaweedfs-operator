package controller

import (
	"context"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/internal/controller/label"
)

func testCluster() *seaweedv1.Seaweed {
	return &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "seaweed-sample", Namespace: "default"},
		Spec: seaweedv1.SeaweedSpec{
			Image:  "chrislusf/seaweedfs:test",
			Master: &seaweedv1.MasterSpec{Replicas: 3},
			Admin:  &seaweedv1.AdminSpec{},
		},
	}
}

func testAdminScript() *seaweedv1.AdminScript {
	return &seaweedv1.AdminScript{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly-balance", Namespace: "default"},
		Spec: seaweedv1.AdminScriptSpec{
			ClusterRef: seaweedv1.AdminScriptClusterRef{Name: "seaweed-sample"},
			Schedule:   "0 2 * * *",
			Script:     "lock\nvolume.balance -force\nunlock",
		},
	}
}

func cronContainer(t *testing.T, cron *batchv1.CronJob) corev1.Container {
	t.Helper()
	containers := cron.Spec.JobTemplate.Spec.Template.Spec.Containers
	if len(containers) != 1 {
		t.Fatalf("expected exactly 1 container, got %d", len(containers))
	}
	return containers[0]
}

func TestBuildCronJob(t *testing.T) {
	r := &AdminScriptReconciler{}
	cron := r.buildCronJob(testAdminScript(), testCluster())

	if cron.Name != "nightly-balance" || cron.Namespace != "default" {
		t.Fatalf("unexpected CronJob meta: %s/%s", cron.Namespace, cron.Name)
	}
	if cron.Spec.Schedule != "0 2 * * *" {
		t.Errorf("schedule = %q, want %q", cron.Spec.Schedule, "0 2 * * *")
	}
	// Default concurrency must be Forbid so admin scripts never overlap.
	if cron.Spec.ConcurrencyPolicy != batchv1.ForbidConcurrent {
		t.Errorf("concurrencyPolicy = %q, want Forbid", cron.Spec.ConcurrencyPolicy)
	}
	if got := cron.Labels[label.ComponentLabelKey]; got != "admin-script" {
		t.Errorf("component label = %q, want admin-script", got)
	}

	c := cronContainer(t, cron)
	if c.Image != "chrislusf/seaweedfs:test" {
		t.Errorf("image = %q, want the cluster image", c.Image)
	}
	if cron.Spec.JobTemplate.Spec.Template.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("restartPolicy = %q, want Never", cron.Spec.JobTemplate.Spec.Template.Spec.RestartPolicy)
	}

	// The script is held in an env var and replayed via printf into the weed
	// invocation, which is passed as positional parameters and run via "$@".
	if len(c.Command) < 6 || c.Command[0] != "/bin/sh" || c.Command[1] != "-ec" {
		t.Fatalf("unexpected command prefix: %v", c.Command)
	}
	if c.Command[2] != `printf '%s\n' "$WEED_SHELL_SCRIPT" | "$@"` {
		t.Errorf("inline script does not pipe via \"$@\": %q", c.Command[2])
	}
	if c.Command[3] != "--" || c.Command[4] != "weed" {
		t.Errorf("weed argv is not passed as positional params: %v", c.Command[4:])
	}
	argv := strings.Join(c.Command[4:], " ")
	if !strings.Contains(argv, "shell -master=seaweed-sample-master-0.") {
		t.Errorf("command does not run weed shell against the masters: %q", argv)
	}

	var scriptEnv string
	for _, e := range c.Env {
		if e.Name == "WEED_SHELL_SCRIPT" {
			scriptEnv = e.Value
		}
	}
	if scriptEnv != "lock\nvolume.balance -force\nunlock" {
		t.Errorf("WEED_SHELL_SCRIPT env = %q, want the spec script verbatim", scriptEnv)
	}
}

func TestBuildCronJobNoAdminComponent(t *testing.T) {
	// weed shell targets the masters, not the admin server, so a cluster
	// without spec.admin must still render a CronJob (and must not panic
	// dereferencing the absent admin component).
	cluster := testCluster()
	cluster.Spec.Admin = nil

	cron := (&AdminScriptReconciler{}).buildCronJob(testAdminScript(), cluster)
	c := cronContainer(t, cron)
	if c.Image != "chrislusf/seaweedfs:test" {
		t.Errorf("image = %q, want the cluster image", c.Image)
	}
	argv := strings.Join(c.Command[4:], " ")
	if !strings.Contains(argv, "shell -master=seaweed-sample-master-0.") {
		t.Errorf("command does not run weed shell against the masters: %q", argv)
	}
}

func TestBuildCronJobFilerFlag(t *testing.T) {
	r := &AdminScriptReconciler{}

	t.Run("no filer omits the flag", func(t *testing.T) {
		cron := r.buildCronJob(testAdminScript(), testCluster())
		cmd := strings.Join(cronContainer(t, cron).Command, " ")
		if strings.Contains(cmd, "-filer=") {
			t.Errorf("expected no -filer flag when cluster has no filer: %q", cmd)
		}
	})

	t.Run("with filer adds the flag", func(t *testing.T) {
		cluster := testCluster()
		cluster.Spec.Filer = &seaweedv1.FilerSpec{Replicas: 1}
		cron := r.buildCronJob(testAdminScript(), cluster)
		cmd := strings.Join(cronContainer(t, cron).Command, " ")
		if !strings.Contains(cmd, "-filer=seaweed-sample-filer.default:8888") {
			t.Errorf("expected -filer flag pointing at the cluster filer: %q", cmd)
		}
	})
}

func TestBuildCronJobImageOverride(t *testing.T) {
	script := testAdminScript()
	override := "custom/weed:v1"
	script.Spec.Image = &override

	cron := (&AdminScriptReconciler{}).buildCronJob(script, testCluster())
	if got := cronContainer(t, cron).Image; got != override {
		t.Errorf("image = %q, want the override %q", got, override)
	}
}

func TestBuildCronJobCredentialsSecret(t *testing.T) {
	r := &AdminScriptReconciler{}

	secretRef := func(c corev1.Container) string {
		for _, ef := range c.EnvFrom {
			if ef.SecretRef != nil {
				return ef.SecretRef.Name
			}
		}
		return ""
	}

	t.Run("none configured exposes no secret env", func(t *testing.T) {
		cron := r.buildCronJob(testAdminScript(), testCluster())
		if name := secretRef(cronContainer(t, cron)); name != "" {
			t.Errorf("expected no envFrom secret, got %q", name)
		}
	})

	t.Run("falls back to cluster admin credentials", func(t *testing.T) {
		cluster := testCluster()
		cluster.Spec.Admin.CredentialsSecret = &corev1.LocalObjectReference{Name: "cluster-admin-creds"}
		cron := r.buildCronJob(testAdminScript(), cluster)
		if name := secretRef(cronContainer(t, cron)); name != "cluster-admin-creds" {
			t.Errorf("envFrom secret = %q, want cluster-admin-creds", name)
		}
	})

	t.Run("own reference wins over cluster", func(t *testing.T) {
		cluster := testCluster()
		cluster.Spec.Admin.CredentialsSecret = &corev1.LocalObjectReference{Name: "cluster-admin-creds"}
		script := testAdminScript()
		script.Spec.CredentialsSecret = &corev1.LocalObjectReference{Name: "script-creds"}
		cron := r.buildCronJob(script, cluster)
		if name := secretRef(cronContainer(t, cron)); name != "script-creds" {
			t.Errorf("envFrom secret = %q, want script-creds", name)
		}
	})
}

// TestCreateOrUpdateCronJobUpdatesSecurityContexts verifies that changing an
// AdminScript updates the stored CronJob template used by future runs.
func TestCreateOrUpdateCronJobUpdatesSecurityContexts(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := seaweedv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add Seaweed scheme: %v", err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add batch scheme: %v", err)
	}

	r := &AdminScriptReconciler{
		Client:   fake.NewClientBuilder().WithScheme(scheme).Build(),
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(2),
	}
	ctx := context.Background()
	script := testAdminScript()
	cluster := testCluster()
	initial := r.buildCronJob(script, cluster)
	if _, err := r.createOrUpdateCronJob(ctx, script, initial); err != nil {
		t.Fatalf("create initial CronJob: %v", err)
	}

	script.Spec.PodSecurityContext = samplePodSecurityContext()
	script.Spec.ContainerSecurityContext = sampleContainerSecurityContext()
	updated := r.buildCronJob(script, cluster)
	if _, err := r.createOrUpdateCronJob(ctx, script, updated); err != nil {
		t.Fatalf("update CronJob security contexts: %v", err)
	}

	stored := &batchv1.CronJob{}
	key := types.NamespacedName{Name: updated.Name, Namespace: updated.Namespace}
	if err := r.Get(ctx, key, stored); err != nil {
		t.Fatalf("get updated CronJob: %v", err)
	}
	assertSecurityContexts(t, stored.Spec.JobTemplate.Spec.Template.Spec, "weed-shell")
}

func clusterPod(name, clusterName string, labels map[string]string) *corev1.Pod {
	base := labelsForMaster(clusterName)
	for k, v := range labels {
		base[k] = v
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels:    base,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "StatefulSet",
				Name:       clusterName + "-" + strings.SplitN(name, "-", 2)[0],
			}},
		},
	}
}

func TestSyncClusterPodLabelsLifecycle(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	podA := clusterPod("master-0", "seaweed-sample", nil)
	podB := clusterPod("volume-0", "seaweed-sample", map[string]string{"reboot-block": "other-owner"})
	foreign := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "other-pod", Namespace: "default",
			Labels: map[string]string{label.NameLabelKey: "seaweedfs", label.InstanceLabelKey: "other-cluster"}},
	}
	spoofed := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "lookalike-0", Namespace: "default", Labels: labelsForMaster("seaweed-sample"),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "StatefulSet",
				Name:       "lookalike",
			}},
		},
	}

	r := &AdminScriptReconciler{
		Client:   fake.NewClientBuilder().WithScheme(scheme).WithObjects(podA, podB, foreign, spoofed).Build(),
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(2),
	}
	ctx := context.Background()

	script := testAdminScript()
	script.Spec.ClusterPodLabels = map[string]string{"reboot-block": "adminscript"}
	cluster := testCluster()

	// Active run: labels applied, previous values recorded.
	if err := r.syncClusterPodLabels(ctx, script, cluster.Name, true); err != nil {
		t.Fatalf("apply labels: %v", err)
	}
	got := &corev1.Pod{}
	if err := r.Get(ctx, types.NamespacedName{Name: "master-0", Namespace: "default"}, got); err != nil {
		t.Fatalf("get pod: %v", err)
	}
	if got.Labels["reboot-block"] != "adminscript" {
		t.Fatalf("expected label applied, got %v", got.Labels)
	}
	if err := r.Get(ctx, types.NamespacedName{Name: "volume-0", Namespace: "default"}, got); err != nil {
		t.Fatalf("get pod: %v", err)
	}
	if got.Labels["reboot-block"] != "adminscript" {
		t.Fatalf("expected label overwritten, got %v", got.Labels)
	}
	if !strings.Contains(got.Annotations[adminScriptLabelBackupAnnotation], "other-owner") {
		t.Fatalf("expected backup of previous value, got %v", got.Annotations)
	}
	if err := r.Get(ctx, types.NamespacedName{Name: "other-pod", Namespace: "default"}, got); err != nil {
		t.Fatalf("get foreign pod: %v", err)
	}
	if _, ok := got.Labels["reboot-block"]; ok {
		t.Fatalf("foreign cluster pod must not be labeled")
	}
	if err := r.Get(ctx, types.NamespacedName{Name: "lookalike-0", Namespace: "default"}, got); err != nil {
		t.Fatalf("get lookalike pod: %v", err)
	}
	if _, ok := got.Labels["reboot-block"]; ok {
		t.Fatalf("pod not owned by the cluster must not be labeled")
	}

	// Run finished: labels removed, prior values restored, annotation gone.
	if err := r.syncClusterPodLabels(ctx, script, cluster.Name, false); err != nil {
		t.Fatalf("restore labels: %v", err)
	}
	if err := r.Get(ctx, types.NamespacedName{Name: "master-0", Namespace: "default"}, got); err != nil {
		t.Fatalf("get pod: %v", err)
	}
	if _, ok := got.Labels["reboot-block"]; ok {
		t.Fatalf("expected label removed, got %v", got.Labels)
	}
	if _, ok := got.Annotations[adminScriptLabelBackupAnnotation]; ok {
		t.Fatalf("expected backup annotation removed, got %v", got.Annotations)
	}
	if err := r.Get(ctx, types.NamespacedName{Name: "volume-0", Namespace: "default"}, got); err != nil {
		t.Fatalf("get pod: %v", err)
	}
	if got.Labels["reboot-block"] != "other-owner" {
		t.Fatalf("expected previous value restored, got %v", got.Labels)
	}
}

func TestSyncClusterPodLabelsReservedKeys(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	pod := clusterPod("master-0", "seaweed-sample", nil)
	r := &AdminScriptReconciler{
		Client:   fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build(),
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(2),
	}
	ctx := context.Background()

	script := testAdminScript()
	script.Spec.ClusterPodLabels = map[string]string{
		label.InstanceLabelKey: "other-instance",
		"reboot-block":         "adminscript",
	}
	cluster := testCluster()

	if err := r.syncClusterPodLabels(ctx, script, cluster.Name, true); err != nil {
		t.Fatalf("apply labels: %v", err)
	}
	got := &corev1.Pod{}
	if err := r.Get(ctx, types.NamespacedName{Name: "master-0", Namespace: "default"}, got); err != nil {
		t.Fatalf("get pod: %v", err)
	}
	if got.Labels[label.InstanceLabelKey] != "seaweed-sample" {
		t.Fatalf("identity label must not be overwritten, got %v", got.Labels)
	}
	if got.Labels["reboot-block"] != "adminscript" {
		t.Fatalf("expected label applied, got %v", got.Labels)
	}
	if strings.Contains(got.Annotations[adminScriptLabelBackupAnnotation], label.InstanceLabelKey) {
		t.Fatalf("identity label must not be backed up, got %v", got.Annotations)
	}

	cron := r.buildCronJob(script, cluster)
	if got := cron.Spec.JobTemplate.Spec.Template.Labels[label.InstanceLabelKey]; got != script.Name {
		t.Fatalf("job pod instance label = %q, want %q", got, script.Name)
	}
}

func TestSyncClusterPodLabelsOverlap(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	pod := clusterPod("master-0", "seaweed-sample", map[string]string{"reboot-block": "original"})
	r := &AdminScriptReconciler{
		Client:   fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build(),
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(2),
	}
	ctx := context.Background()
	cluster := testCluster()

	scriptA := testAdminScript()
	scriptA.Spec.ClusterPodLabels = map[string]string{"reboot-block": "a"}
	scriptB := testAdminScript()
	scriptB.Name = "other-script"
	scriptB.Spec.ClusterPodLabels = map[string]string{"reboot-block": "b"}

	get := func() *corev1.Pod {
		got := &corev1.Pod{}
		if err := r.Get(ctx, types.NamespacedName{Name: "master-0", Namespace: "default"}, got); err != nil {
			t.Fatalf("get pod: %v", err)
		}
		return got
	}

	if err := r.syncClusterPodLabels(ctx, scriptA, cluster.Name, true); err != nil {
		t.Fatalf("apply A: %v", err)
	}
	if err := r.syncClusterPodLabels(ctx, scriptB, cluster.Name, true); err != nil {
		t.Fatalf("apply B: %v", err)
	}
	if got := get().Labels["reboot-block"]; got != "b" {
		t.Fatalf("expected b label, got %q", got)
	}

	if err := r.syncClusterPodLabels(ctx, scriptA, cluster.Name, false); err != nil {
		t.Fatalf("release A: %v", err)
	}
	got := get()
	if got.Labels["reboot-block"] != "b" {
		t.Fatalf("B label must survive A ending, got %v", got.Labels)
	}
	if !strings.Contains(got.Annotations[adminScriptLabelBackupAnnotation], "original") {
		t.Fatalf("B backup must be spliced to original, got %v", got.Annotations)
	}

	if err := r.syncClusterPodLabels(ctx, scriptB, cluster.Name, false); err != nil {
		t.Fatalf("release B: %v", err)
	}
	got = get()
	if got.Labels["reboot-block"] != "original" {
		t.Fatalf("expected original restored, got %v", got.Labels)
	}
	if _, ok := got.Annotations[adminScriptLabelBackupAnnotation]; ok {
		t.Fatalf("expected backup annotation removed, got %v", got.Annotations)
	}
}

// An earlier writer's periodic reconcile re-applies its value while a later
// writer is on top; that write must re-stack the writer so releasing still
// unwinds the chain to the original value.
func TestSyncClusterPodLabelsReapplyRestacksWriter(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	pod := clusterPod("master-0", "seaweed-sample", map[string]string{"reboot-block": "original"})
	r := &AdminScriptReconciler{
		Client:   fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build(),
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(2),
	}
	ctx := context.Background()
	cluster := testCluster()

	scriptA := testAdminScript()
	scriptA.Spec.ClusterPodLabels = map[string]string{"reboot-block": "a"}
	scriptB := testAdminScript()
	scriptB.Name = "other-script"
	scriptB.Spec.ClusterPodLabels = map[string]string{"reboot-block": "b"}

	get := func() *corev1.Pod {
		got := &corev1.Pod{}
		if err := r.Get(ctx, types.NamespacedName{Name: "master-0", Namespace: "default"}, got); err != nil {
			t.Fatalf("get pod: %v", err)
		}
		return got
	}

	if err := r.syncClusterPodLabels(ctx, scriptA, cluster.Name, true); err != nil {
		t.Fatalf("apply A: %v", err)
	}
	if err := r.syncClusterPodLabels(ctx, scriptB, cluster.Name, true); err != nil {
		t.Fatalf("apply B: %v", err)
	}
	// A's run is still active, so its reconcile re-applies its own value.
	if err := r.syncClusterPodLabels(ctx, scriptA, cluster.Name, true); err != nil {
		t.Fatalf("re-apply A: %v", err)
	}
	if got := get().Labels["reboot-block"]; got != "a" {
		t.Fatalf("expected a label after re-apply, got %q", got)
	}

	if err := r.syncClusterPodLabels(ctx, scriptA, cluster.Name, false); err != nil {
		t.Fatalf("release A: %v", err)
	}
	if got := get().Labels["reboot-block"]; got != "b" {
		t.Fatalf("B label must resurface when A ends, got %q", got)
	}

	if err := r.syncClusterPodLabels(ctx, scriptB, cluster.Name, false); err != nil {
		t.Fatalf("release B: %v", err)
	}
	got := get()
	if got.Labels["reboot-block"] != "original" {
		t.Fatalf("expected original restored, got %v", got.Labels)
	}
	if _, ok := got.Annotations[adminScriptLabelBackupAnnotation]; ok {
		t.Fatalf("expected backup annotation removed, got %v", got.Annotations)
	}
}

// Cluster pods may be owned by any cluster workload kind: StatefulSets,
// the volume DaemonSet, or a Deployment's ReplicaSet.
func TestSyncClusterPodLabelsWorkloadKinds(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	owned := func(name, kind, owner string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
				Labels:    labelsForMaster("seaweed-sample"),
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "apps/v1",
					Kind:       kind,
					Name:       owner,
				}},
			},
		}
	}
	pods := []client.Object{
		owned("master-0", "StatefulSet", "seaweed-sample-master"),
		owned("volume-x", "DaemonSet", "seaweed-sample-volume"),
		owned("s3-7c9f4d8b5-abcde", "ReplicaSet", "seaweed-sample-s3-7c9f4d8b5"),
		owned("stray", "ReplicaSet", "unrelated-rs"),
	}
	r := &AdminScriptReconciler{
		Client:   fake.NewClientBuilder().WithScheme(scheme).WithObjects(pods...).Build(),
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(2),
	}
	ctx := context.Background()

	script := testAdminScript()
	script.Spec.ClusterPodLabels = map[string]string{"reboot-block": "adminscript"}
	cluster := testCluster()

	if err := r.syncClusterPodLabels(ctx, script, cluster.Name, true); err != nil {
		t.Fatalf("apply labels: %v", err)
	}
	for _, name := range []string{"master-0", "volume-x", "s3-7c9f4d8b5-abcde"} {
		got := &corev1.Pod{}
		if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, got); err != nil {
			t.Fatalf("get pod %s: %v", name, err)
		}
		if got.Labels["reboot-block"] != "adminscript" {
			t.Errorf("pod %s labels = %v, want reboot-block applied", name, got.Labels)
		}
	}
	stray := &corev1.Pod{}
	if err := r.Get(ctx, types.NamespacedName{Name: "stray", Namespace: "default"}, stray); err != nil {
		t.Fatalf("get stray pod: %v", err)
	}
	if _, ok := stray.Labels["reboot-block"]; ok {
		t.Error("pod owned by an unrelated ReplicaSet must not be labeled")
	}
}

func TestSyncClusterPodLabelsDropsRemovedSpecKeys(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	pod := clusterPod("master-0", "seaweed-sample", map[string]string{"keep-me": "original"})
	r := &AdminScriptReconciler{
		Client:   fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build(),
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(2),
	}
	ctx := context.Background()
	cluster := testCluster()

	script := testAdminScript()
	script.Spec.ClusterPodLabels = map[string]string{"reboot-block": "a", "keep-me": "a"}

	get := func() *corev1.Pod {
		got := &corev1.Pod{}
		if err := r.Get(ctx, types.NamespacedName{Name: "master-0", Namespace: "default"}, got); err != nil {
			t.Fatalf("get pod: %v", err)
		}
		return got
	}

	if err := r.syncClusterPodLabels(ctx, script, cluster.Name, true); err != nil {
		t.Fatalf("apply labels: %v", err)
	}

	script.Spec.ClusterPodLabels = map[string]string{"reboot-block": "a"}
	if err := r.syncClusterPodLabels(ctx, script, cluster.Name, true); err != nil {
		t.Fatalf("re-apply labels: %v", err)
	}
	got := get()
	if got.Labels["keep-me"] != "original" {
		t.Fatalf("dropped key must be restored, got %v", got.Labels)
	}
	if got.Labels["reboot-block"] != "a" {
		t.Fatalf("kept key must stay applied, got %v", got.Labels)
	}
	if strings.Contains(got.Annotations[adminScriptLabelBackupAnnotation], "keep-me") {
		t.Fatalf("dropped key must leave the backup, got %v", got.Annotations)
	}
}

func TestSyncClusterPodLabelsClusterRefMoved(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := seaweedv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add seaweed scheme: %v", err)
	}

	pod := clusterPod("master-0", "old-cluster", nil)
	script := testAdminScript()
	script.Spec.ClusterPodLabels = map[string]string{"reboot-block": "a"}

	r := &AdminScriptReconciler{
		Client:   fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod, script).Build(),
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(2),
	}
	ctx := context.Background()

	// Labels were applied to the cluster the script used to reference.
	if err := r.syncClusterPodLabels(ctx, script, "old-cluster", true); err != nil {
		t.Fatalf("apply labels: %v", err)
	}
	if err := r.setScriptLabelCluster(ctx, script, "old-cluster"); err != nil {
		t.Fatalf("record cluster: %v", err)
	}
	if got := scriptLabelCluster(script); got != "old-cluster" {
		t.Fatalf("label cluster = %q, want old-cluster", got)
	}

	// clusterRef now points at seaweed-sample, but the restore still finds
	// the pods on the recorded cluster.
	if err := r.syncClusterPodLabels(ctx, script, scriptLabelCluster(script), false); err != nil {
		t.Fatalf("restore labels: %v", err)
	}
	got := &corev1.Pod{}
	if err := r.Get(ctx, types.NamespacedName{Name: "master-0", Namespace: "default"}, got); err != nil {
		t.Fatalf("get pod: %v", err)
	}
	if _, ok := got.Labels["reboot-block"]; ok {
		t.Fatalf("expected label removed on old cluster, got %v", got.Labels)
	}
	if _, ok := got.Annotations[adminScriptLabelBackupAnnotation]; ok {
		t.Fatalf("expected backup annotation removed, got %v", got.Annotations)
	}

	script.Spec.ClusterPodLabels = nil
	if err := r.setScriptLabelCluster(ctx, script, "seaweed-sample"); err != nil {
		t.Fatalf("clear record: %v", err)
	}
	if got := script.Annotations[adminScriptLabelsClusterAnnotation]; got != "" {
		t.Fatalf("expected record cleared, got %q", got)
	}
}
