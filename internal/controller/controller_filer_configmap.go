package controller

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// hasFilerConfig reports whether the user actually supplied non-blank
// filer.toml content. Must match the mount guard in
// controller_filer_statefulset.go — if these drift, the ConfigMap and the
// mount fall out of sync and we re-introduce the empty-file crashloop.
//
// A nil Config means "no override, let the filer use its baked-in
// default". A whitespace-only string is the same in practice: mounting it
// creates /etc/seaweedfs/filer.toml, which SeaweedFS then treats as a
// loaded config and skips its default leveldb2 store initialization —
// exactly the bug this PR is fixing.
func hasFilerConfig(m *seaweedv1.Seaweed) bool {
	return m.Spec.Filer != nil && m.Spec.Filer.Config != nil && strings.TrimSpace(*m.Spec.Filer.Config) != ""
}

// createFilerConfigMap returns a ConfigMap carrying the user-supplied
// filer.toml, or nil when no Filer.Config is set. Callers must skip mounting
// when this returns nil — mounting an empty filer.toml causes SeaweedFS to
// treat the config as loaded and skip its default leveldb2 store
// initialization, which crashloops the filer.
func (r *SeaweedReconciler) createFilerConfigMap(m *seaweedv1.Seaweed) *corev1.ConfigMap {
	if !hasFilerConfig(m) {
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
