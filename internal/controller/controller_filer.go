package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"

	appsv1 "k8s.io/api/apps/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	label "github.com/seaweedfs/seaweedfs-operator/internal/controller/label"
)

func (r *SeaweedReconciler) ensureFilerServers(seaweedCR *seaweedv1.Seaweed) (done bool, result ctrl.Result, err error) {
	_ = context.Background()
	_ = r.Log.With("seaweed", seaweedCR.Name)

	if done, result, err = r.ensureFilerPeerService(seaweedCR); done {
		return
	}

	if done, result, err = r.ensureFilerService(seaweedCR); done {
		return
	}

	if done, result, err = r.ensureFilerConfigMap(seaweedCR); done {
		return
	}

	if done, result, err = r.ensureFilerStatefulSet(seaweedCR); done {
		return
	}

	metricsPort := resolveMetricsPort(seaweedCR, seaweedCR.Spec.Filer.MetricsPort)

	if metricsPort != nil {
		if done, result, err = r.ensureFilerServiceMonitor(seaweedCR); done {
			return
		}
	}

	return
}

func (r *SeaweedReconciler) ensureFilerStatefulSet(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.With("sw-filer-statefulset", seaweedCR.Name)

	filerStatefulSet := r.createFilerStatefulSet(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, filerStatefulSet, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdate(filerStatefulSet, func(existing, desired runtime.Object) error {
		existingStatefulSet := existing.(*appsv1.StatefulSet)
		desiredStatefulSet := desired.(*appsv1.StatefulSet)

		existingStatefulSet.Spec.Replicas = desiredStatefulSet.Spec.Replicas
		existingStatefulSet.Spec.Template.ObjectMeta = desiredStatefulSet.Spec.Template.ObjectMeta
		existingStatefulSet.Spec.Template.Spec = desiredStatefulSet.Spec.Template.Spec
		return nil
	})

	log.Debug("ensure filer stateful set " + filerStatefulSet.Name)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureFilerPeerService(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {

	log := r.Log.With("sw-filer-peer-service", seaweedCR.Name)

	filerPeerService := r.createFilerPeerService(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, filerPeerService, r.Scheme); err != nil {
		return ReconcileResult(err)
	}

	_, err := r.CreateOrUpdateService(filerPeerService)
	log.Debug("ensure filer peer service " + filerPeerService.Name)

	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureFilerService(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {

	log := r.Log.With("sw-filer-service", seaweedCR.Name)

	filerService := r.createFilerService(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, filerService, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateService(filerService)

	log.Debug("ensure filer service " + filerService.Name)

	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureFilerConfigMap(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.With("sw-filer-configmap", seaweedCR.Name)

	filerConfigMap := r.createFilerConfigMap(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, filerConfigMap, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateConfigMap(filerConfigMap)

	log.Debug("get filer ConfigMap " + filerConfigMap.Name)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureFilerServiceMonitor(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.With("sw-filer-servicemonitor", seaweedCR.Name)

	filerServiceMonitor := r.createFilerServiceMonitor(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, filerServiceMonitor, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateServiceMonitor(filerServiceMonitor)

	log.Debug("get filer service monitor " + filerServiceMonitor.Name)
	return ReconcileResult(err)
}

func labelsForFiler(name string) map[string]string {
	return map[string]string{
		label.ManagedByLabelKey: "seaweedfs-operator",
		label.NameLabelKey:      "seaweedfs",
		label.ComponentLabelKey: "filer",
		label.InstanceLabelKey:  name,
	}
}
