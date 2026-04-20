package controller

import "testing"

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
