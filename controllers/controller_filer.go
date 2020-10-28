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

	if done, result, err = r.ensureFilerHeadlessService(seaweedCR); done {
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

func (r *SeaweedReconciler) ensureFilerHeadlessService(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {

	log := r.Log.WithValues("sw-filer-headless-service", seaweedCR.Name)

	filerHeadlessService := r.createFilerHeadlessService(seaweedCR)
	_, err := r.CreateOrUpdateService(filerHeadlessService)

	log.Info("ensure filer headless service " + filerHeadlessService.Name)

	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureFilerService(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {

	log := r.Log.WithValues("sw-filer-service", seaweedCR.Name)

	filerService := r.createFilerService(seaweedCR)
	_, err := r.CreateOrUpdateService(filerService)

	log.Info("ensure filer service " + filerService.Name)

	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureFilerConfigMap(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.WithValues("sw-filer-configmap", seaweedCR.Name)

	filerConfigMap := r.createFilerConfigMap(seaweedCR)
	_, err := r.CreateOrUpdateConfigMap(filerConfigMap)

	log.Info("Get filer ConfigMap " + filerConfigMap.Name)
	return ReconcileResult(err)
}

func labelsForFiler(name string) map[string]string {
	return map[string]string{"app": "seaweedfs", "role": "filer", "name": name}
}
