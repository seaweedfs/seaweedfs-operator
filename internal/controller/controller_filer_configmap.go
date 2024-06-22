package controller

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createFilerConfigMap(m *seaweedv1.Seaweed) *corev1.ConfigMap {
	labels := labelsForFiler(m.Name)

	toml := ""
	if m.Spec.Filer.Config != nil {
		toml = *m.Spec.Filer.Config
	}

	dep := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-filer",
			Namespace: m.Namespace,
			Labels:    labels,
		},
		Data: map[string]string{
			"filer.toml": toml,
		},
	}
	return dep
}
