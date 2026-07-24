package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// The admin metrics listener started by `-metricsPort` runs on its own port and
// on the default net/http mux, so it always serves `/metrics` at the root. A
// `-urlPrefix` only affects the admin UI router on the admin HTTP port, and
// applying it to the scrape path makes Prometheus 404.
func TestCreateAdminServiceMonitorMetricsPathIgnoresURLPrefix(t *testing.T) {
	metricsPort := int32(9327)
	cases := []struct {
		name      string
		extraArgs []string
	}{
		{"no prefix", nil},
		{"with prefix", []string{"-urlPrefix=/seaweedfs"}},
		{"nested prefix", []string{"--urlPrefix", "/ops/admin"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
				Spec: seaweedv1.SeaweedSpec{
					Master: &seaweedv1.MasterSpec{Replicas: 1},
					Admin: &seaweedv1.AdminSpec{
						MetricsPort:   &metricsPort,
						ComponentSpec: seaweedv1.ComponentSpec{ExtraArgs: tc.extraArgs},
					},
				},
			}

			r := &SeaweedReconciler{}
			sm := r.createAdminServiceMonitor(m)

			if len(sm.Spec.Endpoints) != 1 {
				t.Fatalf("expected 1 endpoint, got %d", len(sm.Spec.Endpoints))
			}
			if got := sm.Spec.Endpoints[0].Path; got != "/metrics" {
				t.Errorf("endpoint path = %q, want %q", got, "/metrics")
			}
			if got := sm.Spec.Endpoints[0].Port; got != "admin-metrics" {
				t.Errorf("endpoint port = %q, want %q", got, "admin-metrics")
			}
		})
	}
}
