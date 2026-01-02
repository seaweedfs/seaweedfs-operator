package controller

import (
	"github.com/seaweedfs/seaweedfs-operator/internal/controller/label"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) ensureSeaweedIngress(seaweedCR *seaweedv1.Seaweed) (done bool, result ctrl.Result, err error) {

	if seaweedCR.Spec.HostSuffix != nil && len(*seaweedCR.Spec.HostSuffix) != 0 {
		if done, result, err = r.ensureAllIngress(seaweedCR); done {
			return
		}
	}

	return
}

func (r *SeaweedReconciler) ensureAllIngress(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.WithValues("sw-ingress", seaweedCR.Name)

	ingressService := r.createAllIngress(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, ingressService, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateIngress(ingressService)

	log.Debug("ensure ingress " + ingressService.Name)
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
