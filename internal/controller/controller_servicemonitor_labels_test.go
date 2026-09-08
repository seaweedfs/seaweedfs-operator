package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/internal/controller/label"
)

// metricsPort is a helper to build a *int32.
func metricsPort(p int32) *int32 { return &p }

// selectorMatches reports whether matchLabels matches serviceLabels.
func selectorMatches(matchLabels, serviceLabels map[string]string) bool {
	sel := labels.Set(matchLabels)
	for k, v := range serviceLabels {
		if sel.Has(k) && sel.Get(k) != v {
			return false
		}
	}
	for k, v := range sel {
		if serviceLabels[k] != v {
			return false
		}
	}
	return true
}

// TestServiceMonitorDoesNotDoubleScrape verifies the ServiceMonitor selector
// matches the regular Service but not the peer Service.
func TestServiceMonitorDoesNotDoubleScrape(t *testing.T) {
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "seaweedfs", Namespace: "test"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1, MetricsPort: metricsPort(8080)},
			Volume: &seaweedv1.VolumeSpec{
				Replicas: 1,
				VolumeServerConfig: seaweedv1.VolumeServerConfig{
					MetricsPort: metricsPort(8080),
				},
			},
			Filer: &seaweedv1.FilerSpec{Replicas: 1, MetricsPort: metricsPort(8080)},
			Admin: &seaweedv1.AdminSpec{MetricsPort: metricsPort(9327)},
		},
	}
	r := &SeaweedReconciler{}

	type svcPair struct {
		name     string
		regular  map[string]string
		peer     map[string]string
		selector map[string]string
	}
	var pairs []svcPair

	// master
	{
		sm := r.createMasterServiceMonitor(m)
		pairs = append(pairs, svcPair{
			name:     "master",
			regular:  r.createMasterService(m).Labels,
			peer:     r.createMasterPeerService(m).Labels,
			selector: sm.Spec.Selector.MatchLabels,
		})
	}
	// filer
	{
		sm := r.createFilerServiceMonitor(m)
		pairs = append(pairs, svcPair{
			name:     "filer",
			regular:  r.createFilerService(m).Labels,
			peer:     r.createFilerPeerService(m).Labels,
			selector: sm.Spec.Selector.MatchLabels,
		})
	}
	// volume (per-pod service for index 0)
	{
		sm := r.createVolumeServerServiceMonitor(m)
		pairs = append(pairs, svcPair{
			name:     "volume",
			regular:  r.createVolumeServerService(m, 0).Labels,
			peer:     r.createVolumeServerPeerService(m).Labels,
			selector: sm.Spec.Selector.MatchLabels,
		})
	}
	// admin
	{
		sm := r.createAdminServiceMonitor(m)
		pairs = append(pairs, svcPair{
			name:     "admin",
			regular:  r.createAdminService(m).Labels,
			peer:     r.createAdminPeerService(m).Labels,
			selector: sm.Spec.Selector.MatchLabels,
		})
	}

	for _, p := range pairs {
		t.Run(p.name, func(t *testing.T) {
			if p.regular[label.MetricsServiceLabelKey] != "true" {
				t.Errorf("regular %s Service missing %q marker label; labels=%v",
					p.name, label.MetricsServiceLabelKey, p.regular)
			}
			if p.selector[label.MetricsServiceLabelKey] != "true" {
				t.Errorf("%s ServiceMonitor selector missing %q marker label; selector=%v",
					p.name, label.MetricsServiceLabelKey, p.selector)
			}
			if _, ok := p.peer[label.MetricsServiceLabelKey]; ok {
				t.Errorf("peer %s Service must NOT carry %q marker label; labels=%v",
					p.name, label.MetricsServiceLabelKey, p.peer)
			}

			if !selectorMatches(p.selector, p.regular) {
				t.Errorf("%s ServiceMonitor selector does not match the regular Service: selector=%v service=%v",
					p.name, p.selector, p.regular)
			}
			if selectorMatches(p.selector, p.peer) {
				t.Errorf("%s ServiceMonitor selector matches the peer Service, causing double scrape: selector=%v peer=%v",
					p.name, p.selector, p.peer)
			}
		})
	}
}

// TestVolumeTopologyServiceMonitorDoesNotDoubleScrape covers the volume topology variant.
func TestVolumeTopologyServiceMonitorDoesNotDoubleScrape(t *testing.T) {
	topology := &seaweedv1.VolumeTopologySpec{
		Replicas:   1,
		Rack:       "rack1",
		DataCenter: "dc1",
		VolumeServerConfig: seaweedv1.VolumeServerConfig{
			MetricsPort: metricsPort(8080),
		},
	}
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "seaweedfs", Namespace: "test"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			VolumeTopology: map[string]*seaweedv1.VolumeTopologySpec{
				"rack1": topology,
			},
		},
	}
	r := &SeaweedReconciler{}

	sm := r.createVolumeServerTopologyServiceMonitor(m, "rack1", topology)
	regular := r.createVolumeServerTopologyService(m, "rack1", 0).Labels
	peer := r.createVolumeServerTopologyPeerService(m, "rack1").Labels
	selector := sm.Spec.Selector.MatchLabels

	if regular[label.MetricsServiceLabelKey] != "true" {
		t.Errorf("regular topology volume Service missing %q marker label; labels=%v",
			label.MetricsServiceLabelKey, regular)
	}
	if selector[label.MetricsServiceLabelKey] != "true" {
		t.Errorf("topology volume ServiceMonitor selector missing %q marker label; selector=%v",
			label.MetricsServiceLabelKey, selector)
	}
	if _, ok := peer[label.MetricsServiceLabelKey]; ok {
		t.Errorf("peer topology volume Service must NOT carry %q marker label; labels=%v",
			label.MetricsServiceLabelKey, peer)
	}
	if !selectorMatches(selector, regular) {
		t.Errorf("topology ServiceMonitor selector does not match the regular Service: selector=%v service=%v",
			selector, regular)
	}
	if selectorMatches(selector, peer) {
		t.Errorf("topology ServiceMonitor selector matches the peer Service, causing double scrape: selector=%v peer=%v",
			selector, peer)
	}
}

// TestPeerServicesKeepPodSelecting ensures the marker label does not leak into
// Spec.Selector, which must keep matching pods.
func TestPeerServicesKeepPodSelecting(t *testing.T) {
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "seaweedfs", Namespace: "test"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1, MetricsPort: metricsPort(8080)},
			Volume: &seaweedv1.VolumeSpec{
				Replicas: 1,
				VolumeServerConfig: seaweedv1.VolumeServerConfig{
					MetricsPort: metricsPort(8080),
				},
			},
			Filer: &seaweedv1.FilerSpec{Replicas: 1, MetricsPort: metricsPort(8080)},
			Admin: &seaweedv1.AdminSpec{MetricsPort: metricsPort(9327)},
		},
	}
	r := &SeaweedReconciler{}

	checks := []struct {
		name     string
		selector map[string]string
	}{
		{"master", r.createMasterService(m).Spec.Selector},
		{"filer", r.createFilerService(m).Spec.Selector},
		{"volume", r.createVolumeServerService(m, 0).Spec.Selector},
		{"admin", r.createAdminService(m).Spec.Selector},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			if _, ok := c.selector[label.MetricsServiceLabelKey]; ok {
				t.Errorf("%s Service Spec.Selector must not contain the marker label (pods do not have it): %v",
					c.name, c.selector)
			}
		})
	}
}
