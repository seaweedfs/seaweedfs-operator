package controller

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	label "github.com/seaweedfs/seaweedfs-operator/internal/controller/label"
)

func (r *SeaweedReconciler) ensureAdminServers(seaweedCR *seaweedv1.Seaweed) (done bool, result ctrl.Result, err error) {
	_ = context.Background()
	_ = r.Log.With("seaweed", seaweedCR.Name)

	if done, result, err = r.ensureAdminService(seaweedCR); done {
		return
	}

	if done, result, err = r.ensureAdminStatefulSet(seaweedCR); done {
		return
	}

	return
}

func (r *SeaweedReconciler) ensureAdminService(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.With("sw-admin-service", seaweedCR.Name)

	adminService := r.createAdminService(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, adminService, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateService(adminService)

	log.Debug("ensure admin service " + adminService.Name)

	return ReconcileResult(err)
}

func labelsForAdmin(name string) map[string]string {
	return map[string]string{
		label.ManagedByLabelKey: "seaweedfs-operator",
		label.NameLabelKey:      "seaweedfs",
		label.ComponentLabelKey: "admin",
		label.InstanceLabelKey:  name,
	}
}

func (r *SeaweedReconciler) ensureAdminStatefulSet(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.With("sw-admin-statefulset", seaweedCR.Name)

	adminStatefulSet := r.createAdminStatefulSet(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, adminStatefulSet, r.Scheme); err != nil {
		return ReconcileResult(err)
	}

	_, err := r.CreateOrUpdate(adminStatefulSet, func(existing, desired runtime.Object) error {
		existingStatefulSet := existing.(*appsv1.StatefulSet)
		desiredStatefulSet := desired.(*appsv1.StatefulSet)

		existingStatefulSet.Spec.Replicas = desiredStatefulSet.Spec.Replicas
		existingStatefulSet.Spec.Template.ObjectMeta = desiredStatefulSet.Spec.Template.ObjectMeta
		existingStatefulSet.Spec.Template.Spec = desiredStatefulSet.Spec.Template.Spec
		return nil
	})

	log.Debug("ensure admin stateful set " + adminStatefulSet.Name)
	return ReconcileResult(err)
}
