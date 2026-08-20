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

// spec.filer.maxMB was served by the CRD but never reached the filer, so the
// chunk size stayed at weed's built-in default no matter what was set.
func TestBuildFilerStartupScript_MaxMB(t *testing.T) {
	newFiler := func(maxMB *int32) *seaweedv1.Seaweed {
		return &seaweedv1.Seaweed{
			ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
			Spec: seaweedv1.SeaweedSpec{
				Master: &seaweedv1.MasterSpec{Replicas: 1},
				Filer:  &seaweedv1.FilerSpec{Replicas: 1, MaxMB: maxMB},
			},
		}
	}

	t.Run("set value is passed through", func(t *testing.T) {
		maxMB := int32(32)
		if got := buildFilerStartupScript(newFiler(&maxMB)); !strings.Contains(got, "-maxMB=32") {
			t.Errorf("expected -maxMB=32 in startup script, got %q", got)
		}
	})

	// Unset must stay absent rather than emit -maxMB=0, which would tell the
	// filer never to chunk instead of leaving it at its own default.
	t.Run("unset omits the flag", func(t *testing.T) {
		if got := buildFilerStartupScript(newFiler(nil)); strings.Contains(got, "-maxMB") {
			t.Errorf("expected no -maxMB flag when unset, got %q", got)
		}
	})
}

// The pod template has to carry the jwt-signing marker, or turning a section
// on changes only the Secret and the running filer never re-reads it.
func TestFilerStatefulSetCarriesJWTSigningAnnotation(t *testing.T) {
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			Filer:  &seaweedv1.FilerSpec{Replicas: 1},
		},
	}
	r := &SeaweedReconciler{}

	if got := r.createFilerStatefulSet(m).Spec.Template.Annotations; got[jwtSigningAnnotation] != "" {
		t.Errorf("expected no jwt-signing annotation without a security.toml mount, got %v", got)
	}

	m.Spec.SecurityConfig = &seaweedv1.SecurityConfigSpec{
		JWTSigning: &seaweedv1.JWTSigningSpec{FilerWrite: true},
	}
	if got := r.createFilerStatefulSet(m).Spec.Template.Annotations[jwtSigningAnnotation]; got != "filerWrite" {
		t.Errorf("pod template annotation = %q, want %q", got, "filerWrite")
	}
}

// The filer must never be probed with a request the security.toml mounted
// into the same pod can reject. Current seaweedfs exempts GET / from the read
// guard, but seaweedfs builds before that exemption answer it with 401
// ("wrong jwt") once [jwt.filer_signing.read] is set, and the pod lands in
// CrashLoopBackOff — which is how the operator behaved on every fresh install
// back when it rendered that section unconditionally. /healthz is outside the
// guard in both.
func TestFilerProbeNotRejectedByReadJWT(t *testing.T) {
	cases := []struct {
		name      string
		jwt       *seaweedv1.JWTSigningSpec
		wantPath  string
		wantGuard bool
	}{
		{name: "no jwt signing", jwt: nil, wantPath: "/", wantGuard: false},
		{name: "filer write only", jwt: &seaweedv1.JWTSigningSpec{FilerWrite: true}, wantPath: "/", wantGuard: false},
		{name: "filer read", jwt: &seaweedv1.JWTSigningSpec{FilerRead: true}, wantPath: "/healthz", wantGuard: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{Name: "seaweedfs", Namespace: "default"},
				Spec: seaweedv1.SeaweedSpec{
					Image:  "chrislusf/seaweedfs:3.96",
					Master: &seaweedv1.MasterSpec{Replicas: 1},
					Volume: &seaweedv1.VolumeSpec{Replicas: 1},
					Filer:  &seaweedv1.FilerSpec{Replicas: 1},
				},
			}
			if tc.jwt != nil {
				m.Spec.SecurityConfig = &seaweedv1.SecurityConfigSpec{JWTSigning: tc.jwt}
			}
			r := &SeaweedReconciler{}
			sts := r.createFilerStatefulSet(m)

			c := filerContainer(t, &sts.Spec.Template.Spec)

			// The probe the kubelet runs against the filer. It carries no
			// Authorization header.
			for _, probe := range []struct {
				kind string
				p    *corev1.Probe
			}{{"readiness", c.ReadinessProbe}, {"liveness", c.LivenessProbe}} {
				if probe.p == nil || probe.p.HTTPGet == nil {
					t.Fatalf("expected an HTTP %s probe on the filer container", probe.kind)
				}
				if probe.p.HTTPGet.Path != tc.wantPath || probe.p.HTTPGet.Port.IntValue() != seaweedv1.FilerHTTPPort {
					t.Fatalf("expected %s probe GET %s on port %d, got %s:%s", probe.kind, tc.wantPath,
						seaweedv1.FilerHTTPPort, probe.p.HTTPGet.Path, probe.p.HTTPGet.Port.String())
				}
			}

			// The security.toml the operator mounts into this very pod,
			// rendered exactly as ensureSecuritySecret writes it.
			securityTOML := renderSecurityTOML(jwtSigningConfig(m), jwtSigningKeys{filerWrite: "w", filerRead: "r"}, tlsEffective(m))
			readJWTRequired := strings.Contains(securityTOML, "[jwt.filer_signing.read]")
			if readJWTRequired != tc.wantGuard {
				t.Fatalf("[jwt.filer_signing.read] rendered = %v, want %v:\n%s", readJWTRequired, tc.wantGuard, securityTOML)
			}

			// The bug was both holding at once: every probe GET answered 401.
			if readJWTRequired && c.ReadinessProbe.HTTPGet.Path == "/" {
				t.Fatalf("filer is probed with an unauthenticated GET / but the mounted "+
					"security.toml enables [jwt.filer_signing.read], so the probe gets "+
					"401 \"wrong jwt\" and the pod CrashLoopBackOffs.\nsecurity.toml:\n%s", securityTOML)
			}
		})
	}
}
