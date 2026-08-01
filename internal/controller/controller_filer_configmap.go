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
	if filerConfigSecret(m) != nil {
		return false
	}
	return m.Spec.Filer != nil && m.Spec.Filer.Config != nil && strings.TrimSpace(*m.Spec.Filer.Config) != ""
}

// filerConfigSecret returns the Secret key holding filer.toml, or nil when
// none is usable. The CRD rejects setting both Config and ConfigSecret, but
// the secret still wins here so behaviour stays defined if a CR predates the
// validation rule. A selector without both a name and a key cannot be
// projected, so it is treated as unset rather than mounted empty — see
// hasFilerConfig for why an empty filer.toml is worse than none.
func filerConfigSecret(m *seaweedv1.Seaweed) *corev1.SecretKeySelector {
	if m.Spec.Filer == nil || m.Spec.Filer.ConfigSecret == nil {
		return nil
	}
	sel := m.Spec.Filer.ConfigSecret
	if sel.Name == "" || sel.Key == "" {
		return nil
	}
	return sel
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
