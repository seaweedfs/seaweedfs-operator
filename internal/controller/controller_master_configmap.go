package controller

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// createMasterConfigMap returns a ConfigMap carrying the user-supplied
// master.toml, or nil when no Master.Config is set. See the filer equivalent
// for rationale.
func (r *SeaweedReconciler) createMasterConfigMap(m *seaweedv1.Seaweed) *corev1.ConfigMap {
	if m.Spec.Master == nil || m.Spec.Master.Config == nil {
		return nil
	}
	labels := labelsForMaster(m.Name)

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-master",
			Namespace: m.Namespace,
			Labels:    labels,
		},
		Data: map[string]string{
			"master.toml": *m.Spec.Master.Config,
		},
	}
}
