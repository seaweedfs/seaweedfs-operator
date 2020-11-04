package controllers

import (
	"context"

	"github.com/seaweedfs/seaweedfs-operator/controllers/label"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) ensureSeaweedIngress(seaweedCR *seaweedv1.Seaweed) (done bool, result ctrl.Result, err error) {
	_ = context.Background()
	_ = r.Log.WithValues("seaweed", seaweedCR.Name)

	if seaweedCR.Spec.Filer.HostSuffix != nil && len(*seaweedCR.Spec.Filer.HostSuffix) != 0 {
		if done, result, err = r.ensureFilerIngress(seaweedCR); done {
			return
		}
	}

	return
}

func (r *SeaweedReconciler) ensureFilerIngress(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.WithValues("sw-master-service", seaweedCR.Name)

	ingressService := r.createFilerIngress(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, ingressService, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateIngress(ingressService)

	log.Info("Get master service " + ingressService.Name)
	return ReconcileResult(err)
}

func labelsForIngress(name string) map[string]string {
	return map[string]string{
		label.ManagedByLabelKey: "seaweedfs-operator",
		label.NameLabelKey:      "seaweedfs",
		label.ComponentLabelKey: "ingress",
		label.InstanceLabelKey:  name,
	}
}
