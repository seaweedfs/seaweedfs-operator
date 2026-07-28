package controller

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// spec.metricsAddress was served by the CRD but never read, so no component
// ever pushed to the configured Pushgateway.
func TestBuildMasterStartupScript_MetricsAddress(t *testing.T) {
	newMaster := func(addr string) *seaweedv1.Seaweed {
		return &seaweedv1.Seaweed{
			ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
			Spec: seaweedv1.SeaweedSpec{
				MetricsAddress: addr,
				Master:         &seaweedv1.MasterSpec{Replicas: 1},
			},
		}
	}

	t.Run("set value is passed through", func(t *testing.T) {
		got := buildMasterStartupScript(newMaster("pushgateway.monitoring:9091"))
		if !strings.Contains(got, "-metrics.address=pushgateway.monitoring:9091") {
			t.Errorf("expected -metrics.address in startup script, got %q", got)
		}
	})

	// Empty must emit nothing: weed's own default is "" (push disabled), and
	// passing an empty value would be an unnecessary argv change on every
	// existing cluster.
	t.Run("unset omits the flag", func(t *testing.T) {
		if got := buildMasterStartupScript(newMaster("")); strings.Contains(got, "-metrics.address") {
			t.Errorf("expected no -metrics.address flag when unset, got %q", got)
		}
	})
}
