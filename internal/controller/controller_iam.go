package controller

import (
	"context"
	"reflect"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) ensureIAM(seaweedCR *seaweedv1.Seaweed) (done bool, result ctrl.Result, err error) {
	if seaweedCR.Spec.IAM == nil {
		return false, ctrl.Result{}, nil
	}

	if done, result, err = r.ensureIAMService(seaweedCR); done {
		return
	}

	if done, result, err = r.ensureIAMStatefulSet(seaweedCR); done {
		return
	}

	return false, ctrl.Result{}, nil
}

func (r *SeaweedReconciler) ensureIAMStatefulSet(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	statefulSetName := seaweedCR.Name + "-iam"
	statefulSet := &appsv1.StatefulSet{}
	err := r.Get(context.TODO(), types.NamespacedName{Name: statefulSetName, Namespace: seaweedCR.Namespace}, statefulSet)
	if errors.IsNotFound(err) {
		statefulSet = r.createIAMStatefulSet(seaweedCR)
		if err := controllerutil.SetControllerReference(seaweedCR, statefulSet, r.Scheme); err != nil {
			return ReconcileResult(err)
		}
		if err = r.Create(context.TODO(), statefulSet); err != nil {
			return ReconcileResult(err)
		}
		return true, ctrl.Result{RequeueAfter: time.Second * 5}, nil
	} else if err != nil {
		return ReconcileResult(err)
	}

	// Update the statefulset
	if !reflect.DeepEqual(statefulSet.Spec, r.createIAMStatefulSet(seaweedCR).Spec) {
		newStatefulSet := r.createIAMStatefulSet(seaweedCR)
		newStatefulSet.ResourceVersion = statefulSet.ResourceVersion
		newStatefulSet.Spec.Replicas = statefulSet.Spec.Replicas
		if err = r.Update(context.TODO(), newStatefulSet); err != nil {
			return ReconcileResult(err)
		}
		return true, ctrl.Result{RequeueAfter: time.Second * 5}, nil
	}

	return ReconcileResult(nil)
}

func (r *SeaweedReconciler) ensureIAMService(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	serviceName := seaweedCR.Name + "-iam"
	service := &corev1.Service{}
	err := r.Get(context.TODO(), types.NamespacedName{Name: serviceName, Namespace: seaweedCR.Namespace}, service)
	if errors.IsNotFound(err) {
		service = r.createIAMService(seaweedCR)
		if err := controllerutil.SetControllerReference(seaweedCR, service, r.Scheme); err != nil {
			return ReconcileResult(err)
		}
		if err = r.Create(context.TODO(), service); err != nil {
			return ReconcileResult(err)
		}
		return true, ctrl.Result{RequeueAfter: time.Second * 5}, nil
	} else if err != nil {
		return ReconcileResult(err)
	}

	// Update the service
	if !reflect.DeepEqual(service.Spec, r.createIAMService(seaweedCR).Spec) {
		newService := r.createIAMService(seaweedCR)
		newService.ResourceVersion = service.ResourceVersion
		newService.Spec.ClusterIP = service.Spec.ClusterIP
		if err = r.Update(context.TODO(), newService); err != nil {
			return ReconcileResult(err)
		}
		return true, ctrl.Result{RequeueAfter: time.Second * 5}, nil
	}

	return ReconcileResult(nil)
}

func labelsForIAM(name string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":      "seaweedfs",
		"app.kubernetes.io/instance":  name,
		"app.kubernetes.io/component": "iam",
	}
}
