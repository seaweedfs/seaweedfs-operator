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

func (r *SeaweedReconciler) ensureFilerBackupServers(ctx context.Context, seaweedCR *seaweedv1.Seaweed) (done bool, result ctrl.Result, err error) {
	_ = r.Log.With("seaweed", seaweedCR.Name)

	if done, result, err = r.ensureFilerBackupConfigMap(ctx, seaweedCR); done {
		return
	}

	if done, result, err = r.ensureFilerBackupStatefulSet(seaweedCR); done {
		return
	}

	return
}

func (r *SeaweedReconciler) ensureFilerBackupStatefulSet(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.With("sw-filer-backup-statefulset", seaweedCR.Name)

	filerBackupStatefulSet := r.createFilerBackupStatefulSet(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, filerBackupStatefulSet, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdate(filerBackupStatefulSet, func(existing, desired runtime.Object) error {
		existingStatefulSet := existing.(*appsv1.StatefulSet)
		desiredStatefulSet := desired.(*appsv1.StatefulSet)

		existingStatefulSet.Spec.Replicas = desiredStatefulSet.Spec.Replicas
		existingStatefulSet.Spec.Template.ObjectMeta = desiredStatefulSet.Spec.Template.ObjectMeta
		existingStatefulSet.Spec.Template.Spec = desiredStatefulSet.Spec.Template.Spec
		return nil
	})
	log.Info("ensure filer backup stateful set " + filerBackupStatefulSet.Name)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureFilerBackupConfigMap(ctx context.Context, seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.With("sw-filer-backup-configmap", seaweedCR.Name)

	filerBackupConfigMap := r.createFilerBackupConfigMap(ctx, seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, filerBackupConfigMap, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateConfigMap(filerBackupConfigMap)

	log.Info("get filer backup ConfigMap " + filerBackupConfigMap.Name)
	return ReconcileResult(err)
}

func labelsForFilerBackup(name string) map[string]string {
	return map[string]string{
		label.ManagedByLabelKey: "seaweedfs-operator",
		label.NameLabelKey:      "seaweedfs",
		label.ComponentLabelKey: "filer-backup",
		label.InstanceLabelKey:  name,
	}
}
