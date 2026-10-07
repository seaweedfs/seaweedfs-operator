package controller

import (
	"context"

	"github.com/seaweedfs/seaweedfs-operator/internal/controller/label"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) ensureSeaweedIngress(ctx context.Context, seaweedCR *seaweedv1.Seaweed) (done bool, result ctrl.Result, err error) {

	if seaweedCR.Spec.HostSuffix != nil && len(*seaweedCR.Spec.HostSuffix) != 0 {
		if done, result, err = r.ensureAllIngress(ctx, seaweedCR); done {
			return
		}
	}

	return
}

func (r *SeaweedReconciler) ensureAllIngress(ctx context.Context, seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.WithValues("sw-ingress", seaweedCR.Name)

	ingressService, err := r.createAllIngress(ctx, seaweedCR)
	if err != nil {
		return ReconcileResult(err)
	}
	if err := controllerutil.SetControllerReference(seaweedCR, ingressService, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err = r.CreateOrUpdateIngress(ingressService)

	log.Info("ensure ingress " + ingressService.Name)
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
