package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func int32p(v int32) *int32 { return &v }

// applyProbeOverride should replace exactly the fields the override supplies,
// leave the rest untouched, and never touch the probe handler.
func TestApplyProbeOverride(t *testing.T) {
	probe := volumeReadinessProbe() // a copy of the operator's default volume probe

	applyProbeOverride(probe, &seaweedv1.ProbeOverride{
		PeriodSeconds:    int32p(5),
		FailureThreshold: int32p(10),
	})

	if probe.PeriodSeconds != 5 {
		t.Fatalf("PeriodSeconds: want 5, got %d", probe.PeriodSeconds)
	}
	if probe.FailureThreshold != 10 {
		t.Fatalf("FailureThreshold: want 10, got %d", probe.FailureThreshold)
	}
	// Untouched defaults.
	if probe.InitialDelaySeconds != 15 {
		t.Fatalf("InitialDelaySeconds should be unchanged at 15, got %d", probe.InitialDelaySeconds)
	}
	// The handler is never overridable.
	if probe.HTTPGet == nil || probe.HTTPGet.Path != "/healthz" {
		t.Fatalf("probe handler must be left as the operator's /healthz")
	}

	// A nil override is a no-op.
	applyProbeOverride(probe, nil)
	if probe.PeriodSeconds != 5 {
		t.Fatalf("nil override must not change the probe")
	}
}

// volumeReadinessProbe returns the operator's default volume readiness probe by
// building a volume StatefulSet with no overrides, so the test stays in sync
// with the controller's defaults.
func volumeReadinessProbe() *corev1.Probe {
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "seaweedfs", Namespace: "default"},
		Spec: seaweedv1.SeaweedSpec{
			Image:  "chrislusf/seaweedfs:4.33",
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			Volume: &seaweedv1.VolumeSpec{Replicas: 1},
		},
	}
	r := &SeaweedReconciler{}
	sts := r.createVolumeServerStatefulSet(m)
	return sts.Spec.Template.Spec.Containers[0].ReadinessProbe
}

// The volume StatefulSet must honor spec.volume.readinessProbe while keeping the
// operator-managed /healthz handler.
func TestVolumeReadinessProbeOverride(t *testing.T) {
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "seaweedfs", Namespace: "default"},
		Spec: seaweedv1.SeaweedSpec{
			Image:  "chrislusf/seaweedfs:4.33",
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			Volume: &seaweedv1.VolumeSpec{
				Replicas: 1,
				VolumeServerConfig: seaweedv1.VolumeServerConfig{
					ComponentSpec: seaweedv1.ComponentSpec{
						ReadinessProbe: &seaweedv1.ProbeOverride{PeriodSeconds: int32p(5)},
					},
				},
			},
		},
	}
	r := &SeaweedReconciler{}
	sts := r.createVolumeServerStatefulSet(m)
	probe := sts.Spec.Template.Spec.Containers[0].ReadinessProbe

	if probe.PeriodSeconds != 5 {
		t.Fatalf("volume readiness PeriodSeconds: want overridden 5, got %d", probe.PeriodSeconds)
	}
	if probe.HTTPGet == nil || probe.HTTPGet.Path != "/healthz" {
		t.Fatalf("override must not change the operator's /healthz handler")
	}
}
