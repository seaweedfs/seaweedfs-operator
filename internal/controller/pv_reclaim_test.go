package controller

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func newReclaimCluster(enable *bool) *seaweedv1.Seaweed {
	mountPath, subPath := "/data", ""
	return &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Image:           "chrislusf/seaweedfs:3.96",
			EnablePVReclaim: enable,
			Master:          &seaweedv1.MasterSpec{Replicas: 1},
			Volume:          &seaweedv1.VolumeSpec{Replicas: 1},
			Filer: &seaweedv1.FilerSpec{
				Replicas: 1,
				Persistence: &seaweedv1.PersistenceSpec{
					Enabled:   true,
					MountPath: &mountPath,
					SubPath:   &subPath,
				},
			},
		},
	}
}

// spec.enablePVReclaim was served by the CRD but never read, so PVCs orphaned
// by a scale-in were kept regardless of what it was set to.
func TestPVCRetentionPolicy(t *testing.T) {
	enabled, disabled := true, false

	cases := []struct {
		name           string
		enable         *bool
		wantWhenScaled appsv1.PersistentVolumeClaimRetentionPolicyType
	}{
		{"enabled deletes orphans", &enabled, appsv1.DeletePersistentVolumeClaimRetentionPolicyType},
		{"disabled retains", &disabled, appsv1.RetainPersistentVolumeClaimRetentionPolicyType},
		{"unset retains", nil, appsv1.RetainPersistentVolumeClaimRetentionPolicyType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pvcRetentionPolicy(newReclaimCluster(tc.enable))
			if got.WhenScaled != tc.wantWhenScaled {
				t.Errorf("whenScaled = %q want %q", got.WhenScaled, tc.wantWhenScaled)
			}
			// Deleting the cluster must never take the data with it, whatever
			// the scale-in setting is.
			if got.WhenDeleted != appsv1.RetainPersistentVolumeClaimRetentionPolicyType {
				t.Errorf("whenDeleted = %q want Retain", got.WhenDeleted)
			}
		})
	}
}

// The policy has to reach the StatefulSets that actually own PVCs, or the
// setting is inert no matter how it resolves.
func TestStatefulSetsCarryRetentionPolicy(t *testing.T) {
	enabled := true
	m := newReclaimCluster(&enabled)
	r := &SeaweedReconciler{}

	sets := map[string]*appsv1.StatefulSet{
		"filer":  r.createFilerStatefulSet(m),
		"volume": r.createVolumeServerStatefulSet(m),
	}
	for name, sts := range sets {
		if len(sts.Spec.VolumeClaimTemplates) == 0 {
			t.Fatalf("precondition: %s StatefulSet has no VolumeClaimTemplates", name)
		}
		policy := sts.Spec.PersistentVolumeClaimRetentionPolicy
		if policy == nil {
			t.Errorf("%s StatefulSet has no PersistentVolumeClaimRetentionPolicy", name)
			continue
		}
		if policy.WhenScaled != appsv1.DeletePersistentVolumeClaimRetentionPolicyType {
			t.Errorf("%s whenScaled = %q want Delete", name, policy.WhenScaled)
		}
	}
}
