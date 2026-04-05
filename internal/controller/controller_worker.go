package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"

	appsv1 "k8s.io/api/apps/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	label "github.com/seaweedfs/seaweedfs-operator/internal/controller/label"
)

func (r *SeaweedReconciler) ensureWorkers(ctx context.Context, seaweedCR *seaweedv1.Seaweed) (done bool, result ctrl.Result, err error) {
	_ = r.Log.WithValues("seaweed", seaweedCR.Name)

	if done, result, err = r.ensureWorkerPeerService(seaweedCR); done {
		return
	}

	if done, result, err = r.ensureWorkerStatefulSet(ctx, seaweedCR); done {
		return
	}

	if seaweedCR.Spec.Worker.MetricsPort != nil {
		if done, result, err = r.ensureWorkerService(seaweedCR); done {
			return
		}

		if done, result, err = r.ensureWorkerServiceMonitor(seaweedCR); done {
			return
		}
	}

	return
}

func (r *SeaweedReconciler) ensureWorkerStatefulSet(ctx context.Context, seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.WithValues("sw-worker-statefulset", seaweedCR.Name)

	workerStatefulSet := r.createWorkerStatefulSet(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, workerStatefulSet, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdate(workerStatefulSet, func(existing, desired runtime.Object) error {
		existingStatefulSet := existing.(*appsv1.StatefulSet)
		desiredStatefulSet := desired.(*appsv1.StatefulSet)

		existingStatefulSet.Spec.Replicas = desiredStatefulSet.Spec.Replicas
		existingStatefulSet.Spec.Template.ObjectMeta = desiredStatefulSet.Spec.Template.ObjectMeta
		existingStatefulSet.Spec.Template.Spec = desiredStatefulSet.Spec.Template.Spec

		return r.reconcileVolumeClaimTemplates(ctx, seaweedCR, existingStatefulSet, desiredStatefulSet)
	})
	log.Info("ensure worker stateful set " + workerStatefulSet.Name)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureWorkerPeerService(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.WithValues("sw-worker-peer-service", seaweedCR.Name)

	workerPeerService := r.createWorkerPeerService(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, workerPeerService, r.Scheme); err != nil {
		return ReconcileResult(err)
	}

	_, err := r.CreateOrUpdateService(workerPeerService)
	log.Info("ensure worker peer service " + workerPeerService.Name)

	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureWorkerService(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.WithValues("sw-worker-service", seaweedCR.Name)

	workerService := r.createWorkerService(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, workerService, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateService(workerService)

	log.Info("ensure worker service " + workerService.Name)

	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureWorkerServiceMonitor(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.WithValues("sw-worker-servicemonitor", seaweedCR.Name)

	workerServiceMonitor := r.createWorkerServiceMonitor(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, workerServiceMonitor, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateServiceMonitor(workerServiceMonitor)

	log.Info("Get worker service monitor " + workerServiceMonitor.Name)
	return ReconcileResult(err)
}

func labelsForWorker(name string) map[string]string {
	return map[string]string{
		label.ManagedByLabelKey: "seaweedfs-operator",
		label.NameLabelKey:      "seaweedfs",
		label.ComponentLabelKey: "worker",
		label.InstanceLabelKey:  name,
	}
}
