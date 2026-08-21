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
	newMaster := func(replicas int32, persistence *seaweedv1.PersistenceSpec) *seaweedv1.Seaweed {
		return &seaweedv1.Seaweed{
			ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
			Spec: seaweedv1.SeaweedSpec{
				Master: &seaweedv1.MasterSpec{Replicas: replicas, Persistence: persistence},
			},
		}
	}
	r := &SeaweedReconciler{}

	t.Run("unset leaves the master on its own default", func(t *testing.T) {
		m := newMaster(3, nil)
		if got := buildMasterStartupScript(m); strings.Contains(got, "-mdir") {
			t.Errorf("expected no -mdir flag when persistence is unset, got %q", got)
		}
		if vcts := r.createMasterStatefulSet(m).Spec.VolumeClaimTemplates; len(vcts) != 0 {
			t.Errorf("expected no volumeClaimTemplates, got %d", len(vcts))
		}
	})

	t.Run("enabled claims a volume and points -mdir at it", func(t *testing.T) {
		m := newMaster(3, &seaweedv1.PersistenceSpec{Enabled: true, MountPath: ptr.To("/var/lib/seaweedfs")})
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

	// An existing claim is one volume shared by the whole StatefulSet, so it is
	// only valid for a single master — the API rejects the rest. Its name stays
	// in ClaimName: PVC names may carry dots and run to 253 characters, and a
	// pod volume name may do neither.
	t.Run("existing claim is referenced by name only", func(t *testing.T) {
		m := newMaster(1, &seaweedv1.PersistenceSpec{Enabled: true, ExistingClaim: ptr.To("my.master.claim")})
		sts := r.createMasterStatefulSet(m)
		if vcts := sts.Spec.VolumeClaimTemplates; len(vcts) != 0 {
			t.Errorf("expected no volumeClaimTemplates for an existing claim, got %d", len(vcts))
		}
		found := false
		for _, vol := range sts.Spec.Template.Spec.Volumes {
			if vol.Name == "sw-master" && vol.PersistentVolumeClaim != nil && vol.PersistentVolumeClaim.ClaimName == "my.master.claim" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected an sw-master volume bound to my.master.claim, got %+v", sts.Spec.Template.Spec.Volumes)
		}
		if !hasMount(sts, "sw-master", "/data") {
			t.Errorf("expected the master container to mount sw-master at /data, got %+v", sts.Spec.Template.Spec.Containers[0].VolumeMounts)
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
