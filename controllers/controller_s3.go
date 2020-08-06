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

func (r *SeaweedReconciler) ensureS3Servers(seaweedCR *seaweedv1.Seaweed) (done bool, result ctrl.Result, err error) {
	_ = context.Background()
	_ = r.Log.WithValues("seaweed", seaweedCR.Name)

	if done, result, err = r.ensureS3Deployment(seaweedCR); done {
		return done, result, err
	}

	if done, result, err = r.ensureS3Service(seaweedCR); done {
		return done, result, err
	}

	return false, ctrl.Result{}, nil
}

func (r *SeaweedReconciler) ensureS3Deployment(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("sw-s3-statefulset", seaweedCR.Name)

	s3Deployment := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Name: seaweedCR.Name + "-s3", Namespace: seaweedCR.Namespace}, s3Deployment)
	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		dep := r.createS3Deployment(seaweedCR)
		log.Info("Creating a new s3 deployment", "Namespace", dep.Namespace, "Name", dep.Name)
		err = r.Create(ctx, dep)
		if err != nil {
			log.Error(err, "Failed to create new s3 statefulset", "Namespace", dep.Namespace, "Name", dep.Name)
			return true, ctrl.Result{}, err
		}
		// Deployment created successfully - return and requeue
		return false, ctrl.Result{}, nil
	} else if err != nil {
		log.Error(err, "Failed to get s3 statefulset")
		return true, ctrl.Result{}, err
	}
	log.Info("Get s3 stateful set " + s3Deployment.Name)
	return false, ctrl.Result{}, nil
}

func (r *SeaweedReconciler) ensureS3Service(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("sw-filer-service", seaweedCR.Name)

	s3Service := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: seaweedCR.Name + "-s3", Namespace: seaweedCR.Namespace}, s3Service)
	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		dep := r.createS3Service(seaweedCR)
		log.Info("Creating a new s3 service", "Namespace", dep.Namespace, "Name", dep.Name)
		err = r.Create(ctx, dep)
		if err != nil {
			log.Error(err, "Failed to create new s3 service", "Namespace", dep.Namespace, "Name", dep.Name)
			return true, ctrl.Result{}, err
		}
		// Deployment created successfully - return and requeue
		return false, ctrl.Result{}, nil
	} else if err != nil {
		log.Error(err, "Failed to get s3 server service")
		return true, ctrl.Result{}, err
	}
	log.Info("Get s3 service " + s3Service.Name)
	return false, ctrl.Result{}, nil
}

func labelsForS3(name string) map[string]string {
	return map[string]string{"app": "seaweedfs", "role": "s3", "name": name}
}
