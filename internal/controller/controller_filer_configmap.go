package controller

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// createFilerConfigMap returns a ConfigMap carrying the user-supplied
// filer.toml, or nil when no Filer.Config is set. Callers must skip mounting
// when this returns nil — mounting an empty filer.toml causes SeaweedFS to
// treat the config as loaded and skip its default leveldb2 store
// initialization, which crashloops the filer.
func (r *SeaweedReconciler) createFilerConfigMap(m *seaweedv1.Seaweed) *corev1.ConfigMap {
	if m.Spec.Filer == nil || m.Spec.Filer.Config == nil {
		return nil
	}
	labels := labelsForFiler(m.Name)

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-filer",
			Namespace: m.Namespace,
			Labels:    labels,
		},
		Data: map[string]string{
			"filer.toml": *m.Spec.Filer.Config,
		},
	}
}
