package controller

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func TestSecurityConfigNeeded(t *testing.T) {
	cases := []struct {
		name string
		spec seaweedv1.SeaweedSpec
		want bool
	}{
		{
			name: "no filer no admin no tls",
			spec: seaweedv1.SeaweedSpec{Master: &seaweedv1.MasterSpec{Replicas: 1}},
			want: false,
		},
		{
			name: "filer only triggers config",
			spec: seaweedv1.SeaweedSpec{
				Master: &seaweedv1.MasterSpec{Replicas: 1},
				Filer:  &seaweedv1.FilerSpec{},
			},
			want: true,
		},
		{
			name: "admin only triggers config",
			spec: seaweedv1.SeaweedSpec{
				Master: &seaweedv1.MasterSpec{Replicas: 1},
				Admin:  &seaweedv1.AdminSpec{},
			},
			want: true,
		},
		{
			name: "tls always triggers config",
			spec: seaweedv1.SeaweedSpec{
				Master: &seaweedv1.MasterSpec{Replicas: 1},
				TLS:    &seaweedv1.TLSSpec{Enabled: true},
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
				Spec:       tc.spec,
			}
			if got := securityConfigNeeded(m); got != tc.want {
				t.Errorf("securityConfigNeeded = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRenderSecurityTOML(t *testing.T) {
	t.Run("without TLS only emits jwt sections", func(t *testing.T) {
		got := renderSecurityTOML("write-key", "read-key", false)
		if !strings.Contains(got, "[jwt.filer_signing]") {
			t.Errorf("expected [jwt.filer_signing] section, got %q", got)
		}
		if !strings.Contains(got, `key = "write-key"`) {
			t.Errorf("expected write-key value, got %q", got)
		}
		if strings.Contains(got, "[grpc") {
			t.Errorf("expected no [grpc.*] sections without TLS, got %q", got)
		}
	})

	t.Run("with TLS emits jwt and grpc sections", func(t *testing.T) {
		got := renderSecurityTOML("write-key", "read-key", true)
		if !strings.Contains(got, "[jwt.filer_signing]") {
			t.Errorf("expected [jwt.filer_signing] section, got %q", got)
		}
		if !strings.Contains(got, "[grpc.filer]") {
			t.Errorf("expected [grpc.filer] section with TLS, got %q", got)
		}
		if !strings.Contains(got, tlsMountPath+"/tls.crt") {
			t.Errorf("expected mount path %q in cert refs, got %q", tlsMountPath, got)
		}
	})

	t.Run("never emits volume jwt.signing", func(t *testing.T) {
		// Shipping [jwt.signing] would force volume servers to enforce
		// signed reads/writes; the operator does not wire that up
		// end-to-end. Catch any future regression that re-introduces it.
		for _, withTLS := range []bool{false, true} {
			got := renderSecurityTOML("write-key", "read-key", withTLS)
			if strings.Contains(got, "[jwt.signing]") {
				t.Errorf("withTLS=%v: unexpected [jwt.signing] section, got %q", withTLS, got)
			}
		}
	})
}
