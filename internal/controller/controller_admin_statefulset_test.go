package controller

import "testing"

func TestAdminHealthPath(t *testing.T) {
	cases := []struct {
		name      string
		extraArgs []string
		want      string
	}{
		{"no prefix", nil, "/health"},
		{"empty args", []string{}, "/health"},
		{"unrelated flag", []string{"-master=m:9333"}, "/health"},
		{"single dash equals", []string{"-urlPrefix=/seaweedfs"}, "/seaweedfs/health"},
		{"double dash equals", []string{"--urlPrefix=/seaweedfs"}, "/seaweedfs/health"},
		{"space separated", []string{"-urlPrefix", "/seaweedfs"}, "/seaweedfs/health"},
		{"missing leading slash", []string{"-urlPrefix=seaweedfs"}, "/seaweedfs/health"},
		{"trailing slash", []string{"-urlPrefix=/seaweedfs/"}, "/seaweedfs/health"},
		{"nested prefix", []string{"-urlPrefix=/ops/admin"}, "/ops/admin/health"},
		{"empty value", []string{"-urlPrefix="}, "/health"},
		{"last occurrence wins", []string{"-urlPrefix=/a", "-urlPrefix=/b"}, "/b/health"},
		{"prefix among other args", []string{"-foo=bar", "-urlPrefix=/x", "-baz"}, "/x/health"},
		{"positional urlPrefix token ignored", []string{"urlPrefix=/nope"}, "/health"},
		{"next token is a flag", []string{"-urlPrefix", "-port=8080"}, "/health"},
		{"trailing bare flag", []string{"-urlPrefix"}, "/health"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := adminHealthPath(tc.extraArgs); got != tc.want {
				t.Errorf("adminHealthPath(%v) = %q, want %q", tc.extraArgs, got, tc.want)
			}
		})
	}
}
