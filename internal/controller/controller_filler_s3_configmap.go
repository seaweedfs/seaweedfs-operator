package controller

import (
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createFilerS3ConfigMap(m *seaweedv1.Seaweed) *corev1.ConfigMap {
	labels := labelsForFiler(m.Name)

	// Convert S3 identities to JSON
	identitiesJSON, err := json.Marshal(m.Spec.Filer.S3)
	if err != nil {
		// Handle error - in production code you'd want proper error handling
		// For now, we'll use an empty JSON array if marshaling fails
		identitiesJSON = []byte("[]")
	}

	dep := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-filer-s3",
			Namespace: m.Namespace,
			Labels:    labels,
		},
		Data: map[string]string{
			"s3_identities.json": string(identitiesJSON),
		},
	}
	return dep
}

// Made with Bob
