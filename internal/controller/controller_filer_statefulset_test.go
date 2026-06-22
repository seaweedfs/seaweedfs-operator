package controller

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// filerContainer returns the "filer" container from a StatefulSet built by the
// reconciler, failing the test if it is missing.
func filerContainer(t *testing.T, sts *corev1.PodSpec) *corev1.Container {
	t.Helper()
	for i := range sts.Containers {
		if sts.Containers[i].Name == "filer" {
			return &sts.Containers[i]
		}
	}
	t.Fatalf("filer container not found in pod spec")
	return nil
}

// On a fresh, no-TLS install the operator used to mount a security.toml that
// enabled [jwt.filer_signing.read], which made the filer demand a signed JWT
// on every GET. The readiness/liveness probe is an unauthenticated GET / on
// the filer HTTP port, so the filer answered 401 ("wrong jwt"), the probe
// failed, and the pod landed in CrashLoopBackOff.
//
// The invariant: the filer must not be probed with an unauthenticated read
// while the security.toml mounted into the same pod requires JWT-signed reads.
func TestFilerProbeNotRejectedByReadJWT(t *testing.T) {
	// The minimal CR from the issue: filer present, no TLS, no admin.
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "seaweedfs", Namespace: "default"},
		Spec: seaweedv1.SeaweedSpec{
			Image:  "chrislusf/seaweedfs:3.96",
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			Volume: &seaweedv1.VolumeSpec{Replicas: 1},
			Filer:  &seaweedv1.FilerSpec{Replicas: 1},
		},
	}
	r := &SeaweedReconciler{}
	sts := r.createFilerStatefulSet(m)

	c := filerContainer(t, &sts.Spec.Template.Spec)

	// The probe the kubelet runs against the filer: an unauthenticated GET /
	// on the filer HTTP port. It carries no Authorization header.
	probe := c.ReadinessProbe
	if probe == nil || probe.HTTPGet == nil {
		t.Fatalf("expected an HTTP readiness probe on the filer container")
	}
	unauthenticatedReadProbe := probe.HTTPGet.Path == "/" &&
		probe.HTTPGet.Port.IntValue() == seaweedv1.FilerHTTPPort
	if !unauthenticatedReadProbe {
		t.Fatalf("expected probe GET / on port %d, got %s:%s",
			seaweedv1.FilerHTTPPort, probe.HTTPGet.Path, probe.HTTPGet.Port.String())
	}

	// The security.toml the operator mounts into this very pod, rendered for a
	// no-TLS cluster exactly as ensureSecuritySecret writes it.
	if !securityConfigNeeded(m) {
		t.Fatalf("precondition: expected security.toml to be mounted for a filer CR")
	}
	securityTOML := renderSecurityTOML("write-key", tlsEffective(m))
	readJWTRequired := strings.Contains(securityTOML, "[jwt.filer_signing.read]")

	// The bug was both holding at once: every probe GET answered with 401.
	if unauthenticatedReadProbe && readJWTRequired {
		t.Fatalf("filer is probed with an unauthenticated GET / but the mounted "+
			"security.toml enables [jwt.filer_signing.read], so the probe gets "+
			"401 \"wrong jwt\" and the pod CrashLoopBackOffs.\nsecurity.toml:\n%s", securityTOML)
	}
}
