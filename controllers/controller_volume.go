package controllers

import (
	"context"
	"k8s.io/apimachinery/pkg/runtime"

	appsv1 "k8s.io/api/apps/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) ensureVolumeServers(seaweedCR *seaweedv1.Seaweed) (done bool, result ctrl.Result, err error) {
	_ = context.Background()
	_ = r.Log.WithValues("seaweed", seaweedCR.Name)

	if done, result, err = r.ensureVolumeServerStatefulSet(seaweedCR); done {
		return
	}

	if done, result, err = r.ensureVolumeServerService(seaweedCR); done {
		return
	}

	return
}

func (r *SeaweedReconciler) ensureVolumeServerStatefulSet(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.WithValues("sw-volume-statefulset", seaweedCR.Name)

	volumeServerStatefulSet := r.createVolumeServerStatefulSet(seaweedCR)
	_, err := r.CreateOrUpdate(volumeServerStatefulSet, func(existing, desired runtime.Object) error {
		existingStatefulSet := existing.(*appsv1.StatefulSet)
		desiredStatefulSet := desired.(*appsv1.StatefulSet)

		existingStatefulSet.Spec.Replicas = desiredStatefulSet.Spec.Replicas
		existingStatefulSet.Spec.Template.Spec.Containers[0].Image = desiredStatefulSet.Spec.Template.Spec.Containers[0].Image
		return nil
	})

	log.Info("ensure volume stateful set " + volumeServerStatefulSet.Name)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureVolumeServerService(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {

	log := r.Log.WithValues("sw-volume-service", seaweedCR.Name)

	volumeServerService := r.createVolumeServerService(seaweedCR)
	_, err := r.CreateOrUpdateService(volumeServerService)

	log.Info("ensure volume service " + volumeServerService.Name)
	return ReconcileResult(err)
}

func labelsForVolumeServer(name string) map[string]string {
	return map[string]string{"app": "seaweedfs", "role": "volume", "name": name}
}
