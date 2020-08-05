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

func (r *SeaweedReconciler) ensureVolumeServers(seaweedCR *seaweedv1.Seaweed) (done bool, result ctrl.Result, err error) {
	_ = context.Background()
	_ = r.Log.WithValues("seaweed", seaweedCR.Name)

	if done, result, err = r.ensureVolumeServerStatefulSet(seaweedCR); done {
		return done, result, err
	}

	if done, result, err = r.ensureVolumeServerService(seaweedCR); done {
		return done, result, err
	}

	return false, ctrl.Result{}, nil
}

func (r *SeaweedReconciler) ensureVolumeServerStatefulSet(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("sw-volume-statefulset", seaweedCR.Name)

	volumeServerStatefulSet := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Name: seaweedCR.Name + "-volume", Namespace: seaweedCR.Namespace}, volumeServerStatefulSet)
	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		dep := r.createVolumeServerStatefulSet(seaweedCR)
		log.Info("Creating a new volume statefulset", "Namespace", dep.Namespace, "Name", dep.Name)
		err = r.Create(ctx, dep)
		if err != nil {
			log.Error(err, "Failed to create new volume statefulset", "Namespace", dep.Namespace, "Name", dep.Name)
			return true, ctrl.Result{}, err
		}
		// Deployment created successfully - return and requeue
		return false, ctrl.Result{}, nil
	} else if err != nil {
		log.Error(err, "Failed to get volume server statefulset")
		return true, ctrl.Result{}, err
	}
	log.Info("Get volume stateful set " + volumeServerStatefulSet.Name)
	return false, ctrl.Result{}, nil
}

func (r *SeaweedReconciler) ensureVolumeServerService(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("sw-volume-service", seaweedCR.Name)

	volumeServerService := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: seaweedCR.Name + "-volume", Namespace: seaweedCR.Namespace}, volumeServerService)
	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		dep := r.createVolumeServerService(seaweedCR)
		log.Info("Creating a new volume service", "Namespace", dep.Namespace, "Name", dep.Name)
		err = r.Create(ctx, dep)
		if err != nil {
			log.Error(err, "Failed to create new volume service", "Namespace", dep.Namespace, "Name", dep.Name)
			return true, ctrl.Result{}, err
		}
		// Deployment created successfully - return and requeue
		return false, ctrl.Result{}, nil
	} else if err != nil {
		log.Error(err, "Failed to get volume server service")
		return true, ctrl.Result{}, err
	}
	log.Info("Get volume service " + volumeServerService.Name)
	return false, ctrl.Result{}, nil
}

func labelsForVolumeServer(name string) map[string]string {
	return map[string]string{"app": "seaweedfs", "role": "volume", "name": name}
}
