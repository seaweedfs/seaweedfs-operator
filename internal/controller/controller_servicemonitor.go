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
// metricsPort and ServiceMonitors are enabled, and deletes the owned
// ServiceMonitor of that name otherwise. Clusters without the Prometheus
// Operator CRD are skipped either way.
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

// pruneServiceMonitors removes ServiceMonitors this Seaweed controls that the
// spec no longer calls for: removed components or topology groups, removed
// metrics ports, and every monitor while ServiceMonitors are disabled. Runs
// on every reconcile so monitors left by skipped component reconcilers do not
// linger; a ServiceMonitor the Seaweed does not control is never touched.
func (r *SeaweedReconciler) pruneServiceMonitors(ctx context.Context, m *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	if !r.serviceMonitorCRDAvailable() {
		return ReconcileResult(nil)
	}
	list := &monitorv1.ServiceMonitorList{}
	if err := r.List(ctx, list, client.InNamespace(m.Namespace)); err != nil {
		if meta.IsNoMatchError(err) || runtime.IsNotRegisteredError(err) {
			r.markServiceMonitorUnavailable()
			return ReconcileResult(nil)
		}
		return ReconcileResult(err)
	}
	desired := desiredServiceMonitorNames(m)

	// The flat volume monitor follows the flat workload, not the spec:
	// retireFlatVolumeServers removes it once the workload has drained.
	flatName := m.Name + "-volume"
	if m.Spec.ServiceMonitorsEnabled() && len(m.Spec.VolumeTopology) > 0 && !desired[flatName] {
		exists, err := r.flatVolumeWorkloadOwned(ctx, m)
		if err != nil {
			return ReconcileResult(err)
		}
		if exists {
			desired[flatName] = true
		}
	}

	for i := range list.Items {
		sm := &list.Items[i]
		if desired[sm.Name] || !metav1.IsControlledBy(sm, m) {
			continue
		}
		if err := r.pruneOwned(ctx, m, &monitorv1.ServiceMonitor{}, sm.Name); err != nil {
			return ReconcileResult(err)
		}
	}
	return ReconcileResult(nil)
}

// desiredServiceMonitorNames is the set of ServiceMonitor names the spec asks
// for: every present component that sets metricsPort, mirroring the names the
// component reconcilers create.
func desiredServiceMonitorNames(m *seaweedv1.Seaweed) map[string]bool {
	names := map[string]bool{}
	if !m.Spec.ServiceMonitorsEnabled() {
		return names
	}
	add := func(name string, metricsPort *int32) {
		if metricsPort != nil {
			names[m.Name+name] = true
		}
	}
	if spec := m.Spec.Master; spec != nil {
		add("-master", spec.MetricsPort)
	}
	if len(m.Spec.VolumeTopology) > 0 {
		for name, spec := range m.Spec.VolumeTopology {
			add("-volume-"+name, spec.MetricsPort)
		}
	} else if spec := m.Spec.Volume; spec != nil {
		add("-volume", spec.MetricsPort)
	}
	if spec := m.Spec.Filer; spec != nil {
		add("-filer", spec.MetricsPort)
	}
	if spec := m.Spec.Admin; spec != nil {
		add("-admin", spec.MetricsPort)
	}
	if spec := m.Spec.Worker; spec != nil && m.Spec.Admin != nil {
		add("-worker", spec.MetricsPort)
	}
	if spec := m.Spec.S3; spec != nil && m.Spec.Filer != nil {
		add("-s3", spec.MetricsPort)
	}
	if spec := m.Spec.SFTP; spec != nil && m.Spec.Filer != nil {
		add("-sftp", spec.MetricsPort)
	}
	return names
}

func (r *SeaweedReconciler) deleteOwnedServiceMonitor(ctx context.Context, m *seaweedv1.Seaweed, name string) error {
	if !r.serviceMonitorCRDAvailable() {
		return nil
	}
	err := r.pruneOwned(ctx, m, &monitorv1.ServiceMonitor{}, name)
	if meta.IsNoMatchError(err) || runtime.IsNotRegisteredError(err) {
		r.markServiceMonitorUnavailable()
		return nil
	}
	return err
}
