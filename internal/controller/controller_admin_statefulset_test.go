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
	baseSpec := func() *seaweedv1.Seaweed {
		return &seaweedv1.Seaweed{
			ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
			Spec: seaweedv1.SeaweedSpec{
				Master: &seaweedv1.MasterSpec{Replicas: 1},
				Admin:  &seaweedv1.AdminSpec{},
			},
		}
	}

	t.Run("no credentials secret execs weed directly", func(t *testing.T) {
		got := buildAdminStartupScript(baseSpec())
		if strings.Contains(got, "/etc/sw/admin") {
			t.Fatalf("unexpected credential wiring in script: %q", got)
		}
		// `exec` is required so SIGTERM from the kubelet reaches weed
		// instead of the /bin/sh wrapper.
		if !strings.HasPrefix(got, "exec weed -logtostderr=true admin ") {
			t.Fatalf("expected exec'd weed command, got %q", got)
		}
	})

	t.Run("credentials secret injects credential flags via mount", func(t *testing.T) {
		m := baseSpec()
		m.Spec.Admin.CredentialsSecret = &corev1.LocalObjectReference{Name: "admin-creds"}

		got := buildAdminStartupScript(m, "-urlPrefix=/admin")

		// Preamble loops over every well-known key and appends `-<key>=<value>`
		// only when the projected file exists.
		for _, key := range adminCredentialKeys {
			if !strings.Contains(got, key) {
				t.Errorf("script missing key %q: %q", key, got)
			}
		}
		if !strings.Contains(got, `f="/etc/sw/admin/$key"`) {
			t.Errorf("script missing mount-path file lookup: %q", got)
		}
		if !strings.Contains(got, `[ -f "$f" ] && set -- "$@" "-$key=$(cat "$f")"`) {
			t.Errorf("script missing conditional flag append: %q", got)
		}
		// exec replaces the shell with weed so signals propagate, and the
		// positional params populated by the loop must be expanded last.
		if !strings.Contains(got, `exec weed -logtostderr=true admin `) {
			t.Errorf("script missing exec'd weed command: %q", got)
		}
		if !strings.HasSuffix(got, ` "$@"`) {
			t.Errorf("script must end by expanding credential flags via \"$@\": %q", got)
		}
		// Operator-supplied extra args must still appear before "$@" so the
		// dynamic credential flags remain the final tokens.
		if !strings.Contains(got, "-urlPrefix=/admin ") {
			t.Errorf("script should include extra args before \"$@\": %q", got)
		}
	})

	t.Run("empty secret name is treated as unset", func(t *testing.T) {
		m := baseSpec()
		m.Spec.Admin.CredentialsSecret = &corev1.LocalObjectReference{Name: ""}
		got := buildAdminStartupScript(m)
		if strings.Contains(got, "/etc/sw/admin") {
			t.Fatalf("expected no credential wiring for empty secret name: %q", got)
		}
	})
}
