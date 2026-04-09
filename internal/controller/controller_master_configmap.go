package controller

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// hasMasterConfig mirrors hasFilerConfig for the master component. Blank
// and whitespace-only Config strings are treated as "no override" because
// the viper-based loader considers any on-disk file a success and would
// suppress the master's own defaults.
func hasMasterConfig(m *seaweedv1.Seaweed) bool {
	return m.Spec.Master != nil && m.Spec.Master.Config != nil && strings.TrimSpace(*m.Spec.Master.Config) != ""
}

// createMasterConfigMap returns a ConfigMap carrying the user-supplied
// master.toml, or nil when no Master.Config is set. See the filer equivalent
// for rationale.
func (r *SeaweedReconciler) createMasterConfigMap(m *seaweedv1.Seaweed) *corev1.ConfigMap {
	if !hasMasterConfig(m) {
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
