package controller

import (
	"context"

	monitorv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// reconcileServiceMonitor creates or updates sm when the component sets
// metricsPort and spec.serviceMonitor leaves ServiceMonitors enabled, and
// deletes the ServiceMonitor of that name otherwise. Only a ServiceMonitor
// this Seaweed controls is deleted, so one a user created under the same
// name is left alone. Clusters without the Prometheus Operator CRD are
// skipped either way.
func (r *SeaweedReconciler) reconcileServiceMonitor(ctx context.Context, m *seaweedv1.Seaweed, metricsPort *int32, sm *monitorv1.ServiceMonitor) (bool, ctrl.Result, error) {
	if metricsPort != nil && m.Spec.ServiceMonitorsEnabled() {
		if err := controllerutil.SetControllerReference(m, sm, r.Scheme); err != nil {
			return ReconcileResult(err)
		}
		_, err := r.CreateOrUpdateServiceMonitor(sm)
		return ReconcileResult(err)
	}
	return ReconcileResult(r.deleteOwnedServiceMonitor(ctx, m, sm.Name))
}

func (r *SeaweedReconciler) deleteOwnedServiceMonitor(ctx context.Context, m *seaweedv1.Seaweed, name string) error {
	if !r.serviceMonitorCRDAvailable() {
		return nil
	}
	existing := &monitorv1.ServiceMonitor{}
	err := r.Get(ctx, client.ObjectKey{Namespace: m.Namespace, Name: name}, existing)
	if err != nil {
		if meta.IsNoMatchError(err) || runtime.IsNotRegisteredError(err) {
			r.markServiceMonitorUnavailable()
			return nil
		}
		return client.IgnoreNotFound(err)
	}
	if !metav1.IsControlledBy(existing, m) {
		return nil
	}
	return client.IgnoreNotFound(r.Delete(ctx, existing))
}
