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

func (r *SeaweedReconciler) ensureVolumeServers(seaweedCR *seaweedv1.Seaweed) (done bool, result ctrl.Result, err error) {
	_ = context.Background()
	_ = r.Log.With("seaweed", seaweedCR.Name)

	if done, result, err = r.ensureVolumeServerPeerService(seaweedCR); done {
		return
	}

	if done, result, err = r.ensureVolumeServerServices(seaweedCR); done {
		return
	}

	if done, result, err = r.ensureVolumeServerStatefulSet(seaweedCR); done {
		return
	}

	metricsPort := resolveMetricsPort(seaweedCR, seaweedCR.Spec.Volume.MetricsPort)

	if metricsPort != nil {
		if done, result, err = r.ensureVolumeServerServiceMonitor(seaweedCR); done {
			return
		}
	}

	return
}

func (r *SeaweedReconciler) ensureVolumeServerStatefulSet(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.With("sw-volume-statefulset", seaweedCR.Name)

	volumeServerStatefulSet := r.createVolumeServerStatefulSet(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, volumeServerStatefulSet, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdate(volumeServerStatefulSet, func(existing, desired runtime.Object) error {
		existingStatefulSet := existing.(*appsv1.StatefulSet)
		desiredStatefulSet := desired.(*appsv1.StatefulSet)

		existingStatefulSet.Spec.Replicas = desiredStatefulSet.Spec.Replicas
		existingStatefulSet.Spec.Template.ObjectMeta = desiredStatefulSet.Spec.Template.ObjectMeta
		existingStatefulSet.Spec.Template.Spec = desiredStatefulSet.Spec.Template.Spec
		return nil
	})

	log.Debug("ensure volume stateful set " + volumeServerStatefulSet.Name)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureVolumeServerPeerService(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {

	log := r.Log.With("sw-volume-peer-service", seaweedCR.Name)

	volumeServerPeerService := r.createVolumeServerPeerService(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, volumeServerPeerService, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateService(volumeServerPeerService)

	log.Debug("ensure volume peer service " + volumeServerPeerService.Name)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureVolumeServerServices(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {

	for i := 0; i < int(seaweedCR.Spec.Volume.Replicas); i++ {
		done, result, err := r.ensureVolumeServerService(seaweedCR, i)
		if done {
			return done, result, err
		}
	}

	return ReconcileResult(nil)
}

func (r *SeaweedReconciler) ensureVolumeServerService(seaweedCR *seaweedv1.Seaweed, i int) (bool, ctrl.Result, error) {

	log := r.Log.With("sw-volume-service", seaweedCR.Name, "index", i)

	volumeServerService := r.createVolumeServerService(seaweedCR, i)
	if err := controllerutil.SetControllerReference(seaweedCR, volumeServerService, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateService(volumeServerService)

	log.Debug("ensure volume service "+volumeServerService.Name, "index", i)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureVolumeServerServiceMonitor(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.With("sw-volume-servicemonitor", seaweedCR.Name)

	volumeServiceMonitor := r.createVolumeServerServiceMonitor(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, volumeServiceMonitor, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateServiceMonitor(volumeServiceMonitor)

	log.Debug("get volume service monitor " + volumeServiceMonitor.Name)
	return ReconcileResult(err)
}

func labelsForVolumeServer(name string) map[string]string {
	return map[string]string{
		label.ManagedByLabelKey: "seaweedfs-operator",
		label.NameLabelKey:      "seaweedfs",
		label.ComponentLabelKey: "volume",
		label.InstanceLabelKey:  name,
	}
}
