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

	if *filerStatefulSet.Spec.Replicas != seaweedCR.Spec.FilerCount ||
		filerStatefulSet.Spec.Template.Spec.Containers[0].Image != seaweedCR.Spec.Image {
		filerStatefulSet.Spec.Replicas = &seaweedCR.Spec.FilerCount
		filerStatefulSet.Spec.Template.Spec.Containers[0].Image = seaweedCR.Spec.Image
		if err = r.Update(ctx, filerStatefulSet); err != nil {
			log.Error(err, "Failed to update filer statefulset", "Namespace", filerStatefulSet.Namespace, "Name", filerStatefulSet.Name)
			return ReconcileResult(err)
		}
		// Deployment created successfully - return and requeue
		return ReconcileResult(err)
	}

	log.Info("Get filer stateful set " + filerStatefulSet.Name)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureFilerHeadlessService(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	return r.ensureService(seaweedCR, "-filer-headless", r.createFilerHeadlessService)
}

func (r *SeaweedReconciler) ensureFilerNodePortService(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	return r.ensureService(seaweedCR, "-filer", r.createFilerNodePortService)
}

func labelsForFiler(name string) map[string]string {
	return map[string]string{"app": "seaweedfs", "role": "filer", "name": name}
}

type CreateServiceFunc func(m *seaweedv1.Seaweed) *corev1.Service

func (r *SeaweedReconciler) ensureService(seaweedCR *seaweedv1.Seaweed, nameSuffix string, serviceFunc CreateServiceFunc) (bool, ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("sw", seaweedCR.Name, "service", nameSuffix)

	aService := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: seaweedCR.Name + nameSuffix, Namespace: seaweedCR.Namespace}, aService)
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
