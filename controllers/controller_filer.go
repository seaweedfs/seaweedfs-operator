package controllers

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) ensureFilerServers(seaweedCR *seaweedv1.Seaweed) (done bool, result ctrl.Result, err error) {
	_ = context.Background()
	_ = r.Log.WithValues("seaweed", seaweedCR.Name)

	if done, result, err = r.ensureFilerStatefulSet(seaweedCR); done {
		return
	}

	if done, result, err = r.ensureFilerService(seaweedCR); done {
		return
	}

	return
}

func (r *SeaweedReconciler) ensureFilerStatefulSet(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("sw-filer-statefulset", seaweedCR.Name)

	filerStatefulSet := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Name: seaweedCR.Name + "-filer", Namespace: seaweedCR.Namespace}, filerStatefulSet)
	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		dep := r.createFilerStatefulSet(seaweedCR)
		log.Info("Creating a new filer statefulset", "Namespace", dep.Namespace, "Name", dep.Name)
		err = r.Create(ctx, dep)
		if err != nil {
			log.Error(err, "Failed to create new filer statefulset", "Namespace", dep.Namespace, "Name", dep.Name)
			return ReconcileResult(err)
		}
		// Deployment created successfully - return and requeue
		return ReconcileResult(err)
	} else if err != nil {
		log.Error(err, "Failed to get filer statefulset")
		return ReconcileResult(err)
	}
	log.Info("Get filer stateful set " + filerStatefulSet.Name)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureFilerService(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("sw-filer-service", seaweedCR.Name)

	volumeServerService := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: seaweedCR.Name + "-filer", Namespace: seaweedCR.Namespace}, volumeServerService)
	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		dep := r.createFilerService(seaweedCR)
		log.Info("Creating a new filer service", "Namespace", dep.Namespace, "Name", dep.Name)
		err = r.Create(ctx, dep)
		if err != nil {
			log.Error(err, "Failed to create new filer service", "Namespace", dep.Namespace, "Name", dep.Name)
			return ReconcileResult(err)
		}
		// Deployment created successfully - return and requeue
		return ReconcileResult(err)
	} else if err != nil {
		log.Error(err, "Failed to get filer server service")
		return ReconcileResult(err)
	}
	log.Info("Get filer service " + volumeServerService.Name)
	return ReconcileResult(err)
}

func labelsForFiler(name string) map[string]string {
	return map[string]string{"app": "seaweedfs", "role": "filer", "name": name}
}
