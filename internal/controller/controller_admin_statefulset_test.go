package controller

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func TestAdminRoutePath(t *testing.T) {
	cases := []struct {
		name      string
		extraArgs []string
		route     string
		want      string
	}{
		{"no prefix health", nil, "/health", "/health"},
		{"no prefix metrics", nil, "/metrics", "/metrics"},
		{"empty args", []string{}, "/health", "/health"},
		{"unrelated flag", []string{"-master=m:9333"}, "/health", "/health"},
		{"single dash equals", []string{"-urlPrefix=/seaweedfs"}, "/health", "/seaweedfs/health"},
		{"double dash equals", []string{"--urlPrefix=/seaweedfs"}, "/metrics", "/seaweedfs/metrics"},
		{"space separated", []string{"-urlPrefix", "/seaweedfs"}, "/health", "/seaweedfs/health"},
		{"missing leading slash", []string{"-urlPrefix=seaweedfs"}, "/health", "/seaweedfs/health"},
		{"trailing slash", []string{"-urlPrefix=/seaweedfs/"}, "/health", "/seaweedfs/health"},
		{"nested prefix", []string{"-urlPrefix=/ops/admin"}, "/metrics", "/ops/admin/metrics"},
		{"empty value", []string{"-urlPrefix="}, "/health", "/health"},
		{"last occurrence wins", []string{"-urlPrefix=/a", "-urlPrefix=/b"}, "/health", "/b/health"},
		{"prefix among other args", []string{"-foo=bar", "-urlPrefix=/x", "-baz"}, "/health", "/x/health"},
		{"positional urlPrefix token ignored", []string{"urlPrefix=/nope"}, "/health", "/health"},
		// Upstream's fla9 parser (like Go's flag pkg) always consumes the next
		// arg as the value for a string flag, so the probe must mirror that.
		{"next token is a flag is consumed", []string{"-urlPrefix", "-port=8080"}, "/health", "/-port=8080/health"},
		{"trailing bare flag has no value", []string{"-urlPrefix"}, "/health", "/health"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := adminRoutePath(tc.extraArgs, tc.route); got != tc.want {
				t.Errorf("adminRoutePath(%v, %q) = %q, want %q", tc.extraArgs, tc.route, got, tc.want)
			}
		})
	}
}

func TestBuildAdminStartupScript(t *testing.T) {
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			Admin:  &seaweedv1.AdminSpec{},
		},
	}
	got := buildAdminStartupScript(m, "-urlPrefix=/admin")
	// `exec` must prefix weed so SIGTERM from the kubelet reaches weed
	// instead of the /bin/sh wrapper.
	if !strings.HasPrefix(got, "exec weed -logtostderr=true admin ") {
		t.Fatalf("expected exec'd weed command, got %q", got)
	}
	if !strings.Contains(got, "-urlPrefix=/admin") {
		t.Fatalf("expected extra args to be included, got %q", got)
	}
}

func TestAdminCredentialEnvVars(t *testing.T) {
	baseSpec := func() *seaweedv1.Seaweed {
		return &seaweedv1.Seaweed{
			ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
			Spec: seaweedv1.SeaweedSpec{
				Master: &seaweedv1.MasterSpec{Replicas: 1},
				Admin:  &seaweedv1.AdminSpec{},
			},
		}
	}

	t.Run("no credentials secret yields no env vars", func(t *testing.T) {
		if got := adminCredentialEnvVars(baseSpec()); got != nil {
			t.Fatalf("expected nil env vars, got %+v", got)
		}
	})

	t.Run("empty secret name yields no env vars", func(t *testing.T) {
		m := baseSpec()
		m.Spec.Admin.CredentialsSecret = &corev1.LocalObjectReference{Name: ""}
		if got := adminCredentialEnvVars(m); got != nil {
			t.Fatalf("expected nil env vars, got %+v", got)
		}
	})

	t.Run("credentials secret projects every key as optional secretKeyRef", func(t *testing.T) {
		m := baseSpec()
		m.Spec.Admin.CredentialsSecret = &corev1.LocalObjectReference{Name: "admin-creds"}

		got := adminCredentialEnvVars(m)
		if len(got) != len(adminCredentialEnvMappings) {
			t.Fatalf("expected %d env vars, got %d: %+v", len(adminCredentialEnvMappings), len(got), got)
		}
		for i, mapping := range adminCredentialEnvMappings {
			ev := got[i]
			if ev.Name != mapping.envName {
				t.Errorf("env[%d]: got name %q, want %q", i, ev.Name, mapping.envName)
			}
			if ev.ValueFrom == nil || ev.ValueFrom.SecretKeyRef == nil {
				t.Fatalf("env[%d] (%s): missing SecretKeyRef", i, mapping.envName)
			}
			ref := ev.ValueFrom.SecretKeyRef
			if ref.Name != "admin-creds" {
				t.Errorf("env[%d]: secret name %q, want %q", i, ref.Name, "admin-creds")
			}
			if ref.Key != mapping.secretKey {
				t.Errorf("env[%d]: secret key %q, want %q", i, ref.Key, mapping.secretKey)
			}
			// Every mapping is optional so users can supply only
			// adminUser/adminPassword without pod startup failing on the
			// missing read-only keys.
			if ref.Optional == nil || !*ref.Optional {
				t.Errorf("env[%d] (%s): SecretKeyRef.Optional must be true", i, mapping.envName)
			}
		}
	})
}
