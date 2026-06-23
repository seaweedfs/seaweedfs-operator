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

// A liveness override may retune timings but can never change SuccessThreshold,
// which Kubernetes requires to be 1 for liveness probes (the field isn't even on
// LivenessProbeOverride). Guards against producing a StatefulSet the API rejects.
func TestVolumeLivenessProbeOverride(t *testing.T) {
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "seaweedfs", Namespace: "default"},
		Spec: seaweedv1.SeaweedSpec{
			Image:  "chrislusf/seaweedfs:4.33",
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			Volume: &seaweedv1.VolumeSpec{
				Replicas: 1,
				VolumeServerConfig: seaweedv1.VolumeServerConfig{
					ComponentSpec: seaweedv1.ComponentSpec{
						LivenessProbe: &seaweedv1.LivenessProbeOverride{PeriodSeconds: int32p(5)},
					},
				},
			},
		},
	}
	r := &SeaweedReconciler{}
	sts := r.createVolumeServerStatefulSet(m)
	probe := sts.Spec.Template.Spec.Containers[0].LivenessProbe

	if probe.PeriodSeconds != 5 {
		t.Fatalf("volume liveness PeriodSeconds: want overridden 5, got %d", probe.PeriodSeconds)
	}
	if probe.SuccessThreshold != 1 {
		t.Fatalf("volume liveness SuccessThreshold must stay 1, got %d", probe.SuccessThreshold)
	}
}

// The S3 gateway defines a liveness probe, so spec.s3.livenessProbe must be
// honored while the operator keeps its /status handler.
func TestS3LivenessProbeOverride(t *testing.T) {
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "seaweedfs", Namespace: "default"},
		Spec: seaweedv1.SeaweedSpec{
			Image:  "chrislusf/seaweedfs:4.33",
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			S3: &seaweedv1.S3GatewaySpec{
				Replicas: 1,
				ComponentSpec: seaweedv1.ComponentSpec{
					LivenessProbe: &seaweedv1.LivenessProbeOverride{PeriodSeconds: int32p(5)},
				},
			},
		},
	}
	r := &SeaweedReconciler{}
	probe := r.buildS3Deployment(m).Spec.Template.Spec.Containers[0].LivenessProbe

	if probe == nil {
		t.Fatal("S3 deployment must define a liveness probe for the override to apply to")
	}
	if probe.PeriodSeconds != 5 {
		t.Fatalf("s3 liveness PeriodSeconds: want overridden 5, got %d", probe.PeriodSeconds)
	}
	if probe.SuccessThreshold != 1 {
		t.Fatalf("s3 liveness SuccessThreshold must stay 1, got %d", probe.SuccessThreshold)
	}
	if probe.HTTPGet == nil || probe.HTTPGet.Path != "/status" {
		t.Fatalf("override must not change the operator's /status handler")
	}
}

// The SFTP gateway defines a TCP liveness probe, so spec.sftp.livenessProbe
// must be honored while the operator keeps its TCP-socket handler.
func TestSFTPLivenessProbeOverride(t *testing.T) {
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "seaweedfs", Namespace: "default"},
		Spec: seaweedv1.SeaweedSpec{
			Image:  "chrislusf/seaweedfs:4.33",
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			SFTP: &seaweedv1.SFTPSpec{
				Replicas: 1,
				ComponentSpec: seaweedv1.ComponentSpec{
					LivenessProbe: &seaweedv1.LivenessProbeOverride{PeriodSeconds: int32p(5)},
				},
			},
		},
	}
	r := &SeaweedReconciler{}
	probe := r.buildSFTPDeployment(m).Spec.Template.Spec.Containers[0].LivenessProbe

	if probe == nil {
		t.Fatal("SFTP deployment must define a liveness probe for the override to apply to")
	}
	if probe.PeriodSeconds != 5 {
		t.Fatalf("sftp liveness PeriodSeconds: want overridden 5, got %d", probe.PeriodSeconds)
	}
	if probe.SuccessThreshold != 1 {
		t.Fatalf("sftp liveness SuccessThreshold must stay 1, got %d", probe.SuccessThreshold)
	}
	if probe.TCPSocket == nil {
		t.Fatalf("override must not change the operator's TCP-socket handler")
	}
}
