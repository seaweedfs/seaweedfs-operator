package controller

import (
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

	// The script is held in an env var and replayed via printf into weed shell.
	cmd := strings.Join(c.Command, " ")
	if !strings.Contains(cmd, `printf '%s\n' "$WEED_SHELL_SCRIPT" | weed`) {
		t.Errorf("command does not pipe the script into weed shell: %q", cmd)
	}
	if !strings.Contains(cmd, " shell -master=seaweed-sample-master-0.") {
		t.Errorf("command does not run weed shell against the masters: %q", cmd)
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
