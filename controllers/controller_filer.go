package controllers

import (
	"context"
	"k8s.io/apimachinery/pkg/runtime"

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

	if done, result, err = r.ensureFilerHeadlessService(seaweedCR); done {
		return
	}

	if done, result, err = r.ensureFilerNodePortService(seaweedCR); done {
		return
	}

	if done, result, err = r.ensureFilerStatefulSet(seaweedCR); done {
		return
	}

	return
}

func (r *SeaweedReconciler) ensureFilerStatefulSet(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	filerStatefulSet := r.createFilerStatefulSet(seaweedCR)
	_, err := r.CreateOrUpdate(seaweedCR, filerStatefulSet, func(existing, desired runtime.Object) error {
		existingStatefulSet := existing.(*appsv1.StatefulSet)
		desiredStatefulSet := desired.(*appsv1.StatefulSet)

		existingStatefulSet.Spec.Replicas = desiredStatefulSet.Spec.Replicas
		existingStatefulSet.Spec.Template.Spec.Containers[0].Image = desiredStatefulSet.Spec.Template.Spec.Containers[0].Image
		return nil
	})
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureFilerHeadlessService(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	return r.ensureService(seaweedCR, "filer-headless", r.createFilerHeadlessService)
}

func (r *SeaweedReconciler) ensureFilerNodePortService(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	return r.ensureService(seaweedCR, "filer", r.createFilerNodePortService)
}

func labelsForFiler(name string) map[string]string {
	return map[string]string{"app": "seaweedfs", "role": "filer", "name": name}
}

type CreateServiceFunc func(m *seaweedv1.Seaweed) *corev1.Service

func (r *SeaweedReconciler) ensureService(seaweedCR *seaweedv1.Seaweed, nameSuffix string, serviceFunc CreateServiceFunc) (bool, ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("sw", seaweedCR.Name, "service", nameSuffix)

	aService := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: seaweedCR.Name + "-" + nameSuffix, Namespace: seaweedCR.Namespace}, aService)
	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		dep := serviceFunc(seaweedCR)
		log.Info("Creating a new service", "Namespace", dep.Namespace, "Name", dep.Name)
		err = r.Create(ctx, dep)
		if err != nil {
			log.Error(err, "Failed to create service", "Namespace", dep.Namespace, "Name", dep.Name)
			return ReconcileResult(err)
		}
		// Deployment created successfully - return and requeue
		return ReconcileResult(err)
	} else if err != nil {
		log.Error(err, "Failed to get server service")
		return ReconcileResult(err)
	}
	log.Info("Get service " + aService.Name)
	return ReconcileResult(err)
}
