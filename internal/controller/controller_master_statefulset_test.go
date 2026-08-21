package controller

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// spec.metricsAddress was served by the CRD but never read, so no component
// ever pushed to the configured Pushgateway.
func TestBuildMasterStartupScript_MetricsAddress(t *testing.T) {
	newMaster := func(addr string) *seaweedv1.Seaweed {
		return &seaweedv1.Seaweed{
			ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
			Spec: seaweedv1.SeaweedSpec{
				MetricsAddress: addr,
				Master:         &seaweedv1.MasterSpec{Replicas: 1},
			},
		}
	}

	t.Run("set value is passed through", func(t *testing.T) {
		got := buildMasterStartupScript(newMaster("pushgateway.monitoring:9091"))
		if !strings.Contains(got, "-metrics.address=pushgateway.monitoring:9091") {
			t.Errorf("expected -metrics.address in startup script, got %q", got)
		}
	})

	// Empty must emit nothing: weed's own default is "" (push disabled), and
	// passing an empty value would be an unnecessary argv change on every
	// existing cluster.
	t.Run("unset omits the flag", func(t *testing.T) {
		if got := buildMasterStartupScript(newMaster("")); strings.Contains(got, "-metrics.address") {
			t.Errorf("expected no -metrics.address flag when unset, got %q", got)
		}
	})
}

// The master's raft log and snapshots live in -mdir, and with them the
// cluster's TopologyId. With no volume behind it the master ran on the
// container's writable layer and lost that on every pod recreation.
func TestMasterPersistence(t *testing.T) {
	newMaster := func(persistence *seaweedv1.PersistenceSpec) *seaweedv1.Seaweed {
		return &seaweedv1.Seaweed{
			ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
			Spec: seaweedv1.SeaweedSpec{
				Master: &seaweedv1.MasterSpec{Replicas: 3, Persistence: persistence},
			},
		}
	}
	r := &SeaweedReconciler{}

	t.Run("unset leaves the master on its own default", func(t *testing.T) {
		m := newMaster(nil)
		if got := buildMasterStartupScript(m); strings.Contains(got, "-mdir") {
			t.Errorf("expected no -mdir flag when persistence is unset, got %q", got)
		}
		if vcts := r.createMasterStatefulSet(m).Spec.VolumeClaimTemplates; len(vcts) != 0 {
			t.Errorf("expected no volumeClaimTemplates, got %d", len(vcts))
		}
	})

	t.Run("enabled claims a volume and points -mdir at it", func(t *testing.T) {
		m := newMaster(&seaweedv1.PersistenceSpec{Enabled: true, MountPath: ptr.To("/var/lib/seaweedfs")})
		if got := buildMasterStartupScript(m); !strings.Contains(got, "-mdir=/var/lib/seaweedfs") {
			t.Errorf("expected -mdir at the mount path, got %q", got)
		}

		sts := r.createMasterStatefulSet(m)
		vcts := sts.Spec.VolumeClaimTemplates
		if len(vcts) != 1 || vcts[0].Name != "sw-master" {
			t.Fatalf("expected one sw-master volumeClaimTemplate, got %+v", vcts)
		}
		if !hasMount(sts, "sw-master", "/var/lib/seaweedfs") {
			t.Errorf("expected the master container to mount sw-master at /var/lib/seaweedfs, got %+v", sts.Spec.Template.Spec.Containers[0].VolumeMounts)
		}
	})

	// An existing claim is shared, so it is a pod volume rather than a
	// per-replica template.
	t.Run("existing claim is mounted as is", func(t *testing.T) {
		m := newMaster(&seaweedv1.PersistenceSpec{Enabled: true, ExistingClaim: ptr.To("my-pvc")})
		sts := r.createMasterStatefulSet(m)
		if vcts := sts.Spec.VolumeClaimTemplates; len(vcts) != 0 {
			t.Errorf("expected no volumeClaimTemplates for an existing claim, got %d", len(vcts))
		}
		found := false
		for _, vol := range sts.Spec.Template.Spec.Volumes {
			if vol.Name == "my-pvc" && vol.PersistentVolumeClaim != nil && vol.PersistentVolumeClaim.ClaimName == "my-pvc" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected a my-pvc volume, got %+v", sts.Spec.Template.Spec.Volumes)
		}
		if !hasMount(sts, "my-pvc", "/data") {
			t.Errorf("expected the master container to mount my-pvc at /data, got %+v", sts.Spec.Template.Spec.Containers[0].VolumeMounts)
		}
	})
}

func hasMount(sts *appsv1.StatefulSet, name, mountPath string) bool {
	for _, mount := range sts.Spec.Template.Spec.Containers[0].VolumeMounts {
		if mount.Name == name && mount.MountPath == mountPath {
			return true
		}
	}
	return false
}
