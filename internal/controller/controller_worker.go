package controller

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	label "github.com/seaweedfs/seaweedfs-operator/internal/controller/label"
)

func (r *SeaweedReconciler) ensureWorkers(ctx context.Context, seaweedCR *seaweedv1.Seaweed) (done bool, result ctrl.Result, err error) {
	_ = r.Log.WithValues("seaweed", seaweedCR.Name)

	// Clean up resources left over from the StatefulSet era: the old
	// StatefulSet and the headless peer Service. Without this, upgrading
	// from a pre-Deployment operator version would leave duplicate worker
	// pods running (StatefulSet + Deployment both selecting on the same
	// labels), all registering with admin.
	if done, result, err = r.cleanupLegacyWorkerStatefulSet(ctx, seaweedCR); done {
		return
	}

	if done, result, err = r.ensureWorkerDeployment(ctx, seaweedCR); done {
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

func (r *SeaweedReconciler) ensureWorkerDeployment(ctx context.Context, seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.WithValues("sw-worker-deployment", seaweedCR.Name)

	workerDeployment := r.createWorkerDeployment(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, workerDeployment, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateDeployment(workerDeployment)
	log.Info("ensure worker deployment " + workerDeployment.Name)
	return ReconcileResult(err)
}

// cleanupLegacyWorkerStatefulSet removes the StatefulSet + headless peer
// Service that earlier operator versions created for workers. Safe to call
// on every reconcile: both deletes are IsNotFound-tolerant.
func (r *SeaweedReconciler) cleanupLegacyWorkerStatefulSet(ctx context.Context, seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	name := seaweedCR.Name + "-worker"
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: seaweedCR.Namespace}}
	if err := r.Delete(ctx, sts); err != nil && !apierrors.IsNotFound(err) {
		return ReconcileResult(err)
	}
	peer := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name + "-peer", Namespace: seaweedCR.Namespace}}
	if err := r.Delete(ctx, peer); err != nil && !apierrors.IsNotFound(err) {
		return ReconcileResult(err)
	}
	return ReconcileResult(nil)
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
