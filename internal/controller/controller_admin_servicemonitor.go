package controller

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	monitorv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createAdminServiceMonitor(m *seaweedv1.Seaweed) *monitorv1.ServiceMonitor {
	labels := labelsForAdmin(m.Name)

	dep := &monitorv1.ServiceMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-admin",
			Namespace: m.Namespace,
			Labels:    labels,
		},
		Spec: monitorv1.ServiceMonitorSpec{
			Endpoints: []monitorv1.Endpoint{
				{
					// `-metricsPort` starts its own listener on the default
					// net/http mux, so metrics stay at `/metrics` regardless of
					// any `-urlPrefix`, which only wraps the admin UI router on
					// the admin HTTP port.
					Path: "/metrics",
					Port: "admin-metrics",
				},
			},
			Selector: metav1.LabelSelector{
				MatchLabels: labels,
			},
		},
	}

	return dep
}
