package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// TestS3IngressBackend pins which Service the all-in-one HostSuffix Ingress
// routes the s3.<suffix> host to. The standalone gateway must win over the
// deprecated embedded filer S3, and the rule must be skipped when no S3 path
// is enabled so we never publish a host pointing at a non-existent port.
func TestS3IngressBackend(t *testing.T) {
	cases := []struct {
		name     string
		spec     seaweedv1.SeaweedSpec
		wantSvc  string
		wantPort int32
		wantOK   bool
	}{
		{
			name:     "standalone gateway default port",
			spec:     seaweedv1.SeaweedSpec{S3: &seaweedv1.S3GatewaySpec{Replicas: 1}},
			wantSvc:  "sw-s3",
			wantPort: seaweedv1.FilerS3Port,
			wantOK:   true,
		},
		{
			name:     "standalone gateway custom port",
			spec:     seaweedv1.SeaweedSpec{S3: &seaweedv1.S3GatewaySpec{Replicas: 1, Port: ptrInt32(9000)}},
			wantSvc:  "sw-s3",
			wantPort: 9000,
			wantOK:   true,
		},
		{
			name: "standalone gateway wins over embedded",
			spec: seaweedv1.SeaweedSpec{
				S3:    &seaweedv1.S3GatewaySpec{Replicas: 1},
				Filer: &seaweedv1.FilerSpec{S3: &seaweedv1.S3Config{Enabled: true}},
			},
			wantSvc:  "sw-s3",
			wantPort: seaweedv1.FilerS3Port,
			wantOK:   true,
		},
		{
			name:     "embedded filer s3 enabled",
			spec:     seaweedv1.SeaweedSpec{Filer: &seaweedv1.FilerSpec{S3: &seaweedv1.S3Config{Enabled: true}}},
			wantSvc:  "sw-filer",
			wantPort: seaweedv1.FilerS3Port,
			wantOK:   true,
		},
		{
			name:   "embedded filer s3 disabled",
			spec:   seaweedv1.SeaweedSpec{Filer: &seaweedv1.FilerSpec{S3: &seaweedv1.S3Config{Enabled: false}}},
			wantOK: false,
		},
		{
			name:   "no s3 at all",
			spec:   seaweedv1.SeaweedSpec{Filer: &seaweedv1.FilerSpec{}},
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{Name: "sw"},
				Spec:       tc.spec,
			}
			gotSvc, gotPort, gotOK := s3IngressBackend(m)
			if gotOK != tc.wantOK {
				t.Fatalf("ok = %v, want %v", gotOK, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if gotSvc != tc.wantSvc {
				t.Errorf("svc = %q, want %q", gotSvc, tc.wantSvc)
			}
			if gotPort != tc.wantPort {
				t.Errorf("port = %d, want %d", gotPort, tc.wantPort)
			}
		})
	}
}
