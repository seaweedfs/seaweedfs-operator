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

const (
	MasterClusterSize = 3
)

func (r *SeaweedReconciler) ensureMaster(seaweedCR *seaweedv1.Seaweed) (done bool, result ctrl.Result, err error) {
	_ = context.Background()
	_ = r.Log.WithValues("seaweed", seaweedCR.Name)

	if done, result, err = r.ensureMasterStatefulSet(seaweedCR); done {
		return done, result, err
	}

	for masterIndex := 0; masterIndex < MasterClusterSize; masterIndex++ {
		if done, result, err = r.ensureMasterService(seaweedCR, masterIndex); done {
			return done, result, err
		}
	}

	return false, ctrl.Result{}, nil
}

func (r *SeaweedReconciler) ensureMasterStatefulSet(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("sw-master-statefulset", seaweedCR.Name)

	masterStatefulSet := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Name: seaweedCR.Name, Namespace: seaweedCR.Namespace}, masterStatefulSet)
	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		dep := r.createMasterStatefulSet(seaweedCR)
		log.Info("Creating a new master statefulset", "Namespace", dep.Namespace, "Name", dep.Name)
		err = r.Create(ctx, dep)
		if err != nil {
			log.Error(err, "Failed to create new statefulset", "Namespace", dep.Namespace, "Name", dep.Name)
			return true, ctrl.Result{}, err
		}
		// Deployment created successfully - return and requeue
		return true, ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		log.Error(err, "Failed to get Deployment")
		return true, ctrl.Result{}, err
	}
	log.Info("Get master cluster " + masterStatefulSet.Name)
	return false, ctrl.Result{}, nil
}

func (r *SeaweedReconciler) ensureMasterService(seaweedCR *seaweedv1.Seaweed, masterIndex int) (bool, ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("sw-master-service", seaweedCR.Name)

	masterService := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: seaweedCR.Name, Namespace: seaweedCR.Namespace}, masterService)
	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		dep := r.createMasterService(seaweedCR, masterIndex)
		log.Info("Creating a new master headless service", "Namespace", dep.Namespace, "Name", dep.Name)
		err = r.Create(ctx, dep)
		if err != nil {
			log.Error(err, "Failed to creater service master", "masterIndex", masterIndex)
			return true, ctrl.Result{}, err
		}
		// Deployment created successfully - return and requeue
		return true, ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		log.Error(err, "Failed to get service master", "masterIndex", masterIndex)
		return true, ctrl.Result{}, err
	}
	log.Info("Get master service " + masterService.Name)
	return false, ctrl.Result{}, nil

}

func labelsForMaster(name string) map[string]string {
	return map[string]string{"app": "seaweedfs", "role": "master", "name": name}
}
