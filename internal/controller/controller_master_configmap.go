package controller

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createMasterConfigMap(m *seaweedv1.Seaweed) *corev1.ConfigMap {
	labels := labelsForMaster(m.Name)

	toml := ""
	if m.Spec.Master.Config != nil {
		toml = *m.Spec.Master.Config
	}

	dep := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-master",
			Namespace: m.Namespace,
			Labels:    labels,
		},
		Data: map[string]string{
			"master.toml": toml,
		},
	}
	// Set master instance as the owner and controller
	// ctrl.SetControllerReference(m, dep, r.Scheme)
	return dep
}
