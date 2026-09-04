package controller

import (
	"fmt"
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

// The -s3.port.lance flag, the container port and both Service ports must
// agree: an absent spec.filer.lance advertises nothing, since spec.image may
// predate Lance and a port nobody listens on would still roll the filer.
func TestFilerLanceExposureFollowsFlag(t *testing.T) {
	customPort := int32(19101)
	cases := []struct {
		name     string
		lance    *seaweedv1.LanceConfig
		wantPort int32 // 0 means off everywhere
	}{
		{name: "unset", lance: nil},
		{name: "disabled", lance: &seaweedv1.LanceConfig{Enabled: false}},
		{name: "enabled", lance: &seaweedv1.LanceConfig{Enabled: true}, wantPort: seaweedv1.FilerLancePort},
		{name: "custom port", lance: &seaweedv1.LanceConfig{Enabled: true, Port: &customPort}, wantPort: customPort},
	}
	namedPort := func(ports []corev1.ServicePort) int32 {
		for _, p := range ports {
			if p.Name == "filer-lance" {
				return p.Port
			}
		}
		return 0
	}
	r := &SeaweedReconciler{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
				Spec: seaweedv1.SeaweedSpec{
					Master: &seaweedv1.MasterSpec{Replicas: 1},
					Filer:  &seaweedv1.FilerSpec{Replicas: 1, Lance: tc.lance},
				},
			}

			script := buildFilerStartupScript(m)
			if hasFlag := strings.Contains(script, "-s3.port.lance="); hasFlag != (tc.wantPort != 0) {
				t.Errorf("-s3.port.lance rendered = %v, want %v: %q", hasFlag, tc.wantPort != 0, script)
			} else if hasFlag && !strings.Contains(script, fmt.Sprintf("-s3.port.lance=%d", tc.wantPort)) {
				t.Errorf("expected -s3.port.lance=%d in %q", tc.wantPort, script)
			}

			var got int32
			for _, p := range filerContainer(t, &r.createFilerStatefulSet(m).Spec.Template.Spec).Ports {
				if p.Name == "filer-lance" {
					got = p.ContainerPort
				}
			}
			if got != tc.wantPort {
				t.Errorf("filer-lance container port = %d, want %d", got, tc.wantPort)
			}

			for _, svc := range []*corev1.Service{r.createFilerService(m), r.createFilerPeerService(m)} {
				if got := namedPort(svc.Spec.Ports); got != tc.wantPort {
					t.Errorf("%s filer-lance port = %d, want %d", svc.Name, got, tc.wantPort)
				}
			}
		})
	}
}

// Without the marker on the pod template, turning a section on changes only
// the Secret and the running filer never re-reads it.
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
// into the same pod can reject. Builds before seaweedfs exempted GET / from
// the read guard answer it 401 once [jwt.filer_signing.read] is set, which
// CrashLoopBackOffs the pod; /healthz is outside the guard on every build.
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

			// The kubelet's probe carries no Authorization header.
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

			// The security.toml mounted into this very pod.
			securityTOML := renderSecurityTOML(jwtSigningConfig(m), jwtSigningKeys{filerWrite: "w", filerRead: "r"}, tlsEffective(m))
			readJWTRequired := strings.Contains(securityTOML, "[jwt.filer_signing.read]")
			if readJWTRequired != tc.wantGuard {
				t.Fatalf("[jwt.filer_signing.read] rendered = %v, want %v:\n%s", readJWTRequired, tc.wantGuard, securityTOML)
			}

			// The bug was both holding at once.
			if readJWTRequired && c.ReadinessProbe.HTTPGet.Path == "/" {
				t.Fatalf("filer is probed with an unauthenticated GET / but the mounted "+
					"security.toml enables [jwt.filer_signing.read], so the probe gets "+
					"401 \"wrong jwt\" and the pod CrashLoopBackOffs.\nsecurity.toml:\n%s", securityTOML)
			}
		})
	}
}
