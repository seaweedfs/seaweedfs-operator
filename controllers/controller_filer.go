package controllers

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"

	appsv1 "k8s.io/api/apps/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) ensureFilerServers(seaweedCR *seaweedv1.Seaweed) (done bool, result ctrl.Result, err error) {
	_ = context.Background()
	_ = r.Log.WithValues("seaweed", seaweedCR.Name)

	if done, result, err = r.ensureFilerPeerService(seaweedCR); done {
		return
	}

	if done, result, err = r.ensureFilerService(seaweedCR); done {
		return
	}

	if done, result, err = r.ensureFilerStatefulSet(seaweedCR); done {
		return
	}

	return
}

func (r *SeaweedReconciler) ensureFilerStatefulSet(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.WithValues("sw-filer-statefulset", seaweedCR.Name)

	filerStatefulSet := r.createFilerStatefulSet(seaweedCR)
	_, err := r.CreateOrUpdate(filerStatefulSet, func(existing, desired runtime.Object) error {
		existingStatefulSet := existing.(*appsv1.StatefulSet)
		desiredStatefulSet := desired.(*appsv1.StatefulSet)

		existingStatefulSet.Spec.Replicas = desiredStatefulSet.Spec.Replicas
		existingStatefulSet.Spec.Template.Spec.Containers[0].Image = desiredStatefulSet.Spec.Template.Spec.Containers[0].Image
		return nil
	})
	log.Info("ensure filer stateful set " + filerStatefulSet.Name)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureFilerPeerService(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {

	log := r.Log.WithValues("sw-filer-peer-service", seaweedCR.Name)

	filerPeerService := r.createFilerPeerService(seaweedCR)
	_, err := r.CreateOrUpdateService(filerPeerService)

	log.Info("ensure filer peer service " + filerPeerService.Name)

	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureFilerService(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {

	log := r.Log.WithValues("sw-filer-service", seaweedCR.Name)

	filerService := r.createFilerService(seaweedCR)
	_, err := r.CreateOrUpdateService(filerService)

	log.Info("ensure filer service " + filerService.Name)

	return ReconcileResult(err)
}

func labelsForFiler(name string) map[string]string {
	return map[string]string{"app": "seaweedfs", "role": "filer", "name": name}
}
