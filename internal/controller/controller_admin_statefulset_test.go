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
	t.Run("no credentials secret execs weed directly", func(t *testing.T) {
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
		if !strings.HasPrefix(got, "exec weed -logtostderr=true -config_dir=/etc/sw-security admin ") {
			t.Fatalf("expected exec'd weed command, got %q", got)
		}
		if !strings.Contains(got, "-urlPrefix=/admin") {
			t.Fatalf("expected extra args to be included, got %q", got)
		}
		if strings.Contains(got, adminCredentialsMountPath) {
			t.Fatalf("expected no credentials preamble, got %q", got)
		}
	})

	t.Run("credentials secret emits flag-forwarding preamble", func(t *testing.T) {
		m := &seaweedv1.Seaweed{
			ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
			Spec: seaweedv1.SeaweedSpec{
				Master: &seaweedv1.MasterSpec{Replicas: 1},
				Admin: &seaweedv1.AdminSpec{
					CredentialsSecret: &corev1.LocalObjectReference{Name: "admin-creds"},
				},
			},
		}
		got := buildAdminStartupScript(m)

		// The preamble must read each well-known key from the mount path and
		// forward it as `-<key>=<value>` via `"$@"` so values with spaces or
		// special characters survive shell expansion.
		for _, key := range adminCredentialKeys {
			if !strings.Contains(got, key) {
				t.Errorf("expected preamble to reference key %q, got %q", key, got)
			}
		}
		if !strings.Contains(got, adminCredentialsMountPath) {
			t.Errorf("expected preamble to reference mount path %q, got %q", adminCredentialsMountPath, got)
		}
		if !strings.Contains(got, `set -- "$@" "-$key=$(cat "$f")"`) {
			t.Errorf("expected preamble to append flags via positional parameters, got %q", got)
		}
		if !strings.Contains(got, `exec weed -logtostderr=true -config_dir=/etc/sw-security admin`) {
			t.Errorf("expected weed to be exec'd after preamble, got %q", got)
		}
		if !strings.HasSuffix(got, ` "$@"`) {
			t.Errorf("expected weed command to expand positional parameters at the end, got %q", got)
		}
	})

	t.Run("empty credentials secret name skips preamble", func(t *testing.T) {
		m := &seaweedv1.Seaweed{
			ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
			Spec: seaweedv1.SeaweedSpec{
				Master: &seaweedv1.MasterSpec{Replicas: 1},
				Admin: &seaweedv1.AdminSpec{
					CredentialsSecret: &corev1.LocalObjectReference{Name: ""},
				},
			},
		}
		got := buildAdminStartupScript(m)
		if strings.Contains(got, adminCredentialsMountPath) {
			t.Fatalf("expected no credentials preamble for empty secret name, got %q", got)
		}
	})
}
