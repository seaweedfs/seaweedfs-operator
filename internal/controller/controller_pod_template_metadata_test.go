package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestMergePodTemplateMetadataKeepsExternalKeys covers the restart signal that
// kubectl rollout restart, Reloader and the Vault Secrets Operator all write to
// the pod template. Replacing ObjectMeta wholesale discarded it mid-roll, which
// left the StatefulSet half rolled: the highest ordinal had already been
// recreated, and ordinal 0 matched the reverted template and was never rolled.
func TestMergePodTemplateMetadataKeepsExternalKeys(t *testing.T) {
	existing := &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"kubectl.kubernetes.io/restartedAt": "2026-09-03T12:23:43Z",
				"vso.hashicorp.com/restartedAt":     "rotation-7",
			},
			Labels: map[string]string{
				"app":                     "seaweedfs",
				"example.com/team":        "storage",
				"pod-template-generation": "stale",
			},
		},
	}
	desired := &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{"seaweed.seaweedfs.com/checksum": "abc123"},
			Labels: map[string]string{
				"app":                     "seaweedfs",
				"pod-template-generation": "fresh",
			},
		},
	}

	mergePodTemplateMetadata(existing, desired)

	for k, want := range map[string]string{
		"kubectl.kubernetes.io/restartedAt": "2026-09-03T12:23:43Z",
		"vso.hashicorp.com/restartedAt":     "rotation-7",
		"seaweed.seaweedfs.com/checksum":    "abc123",
	} {
		if got := existing.Annotations[k]; got != want {
			t.Errorf("annotation %q = %q, want %q", k, got, want)
		}
	}
	if got := existing.Labels["example.com/team"]; got != "storage" {
		t.Errorf("label set by another controller was dropped, got %q", got)
	}
	// The operator stays authoritative over the keys it does set.
	if got := existing.Labels["pod-template-generation"]; got != "fresh" {
		t.Errorf("operator-managed label = %q, want the desired value", got)
	}
}

// The rest of the pod template must still be replaced outright, so a spec
// change the operator makes is not silently merged away.
func TestMergePodTemplateMetadataReplacesTheRestOfObjectMeta(t *testing.T) {
	existing := &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "left-over",
			Annotations: map[string]string{"external": "keep"},
		},
	}
	desired := &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Name: "seaweed-filer"},
	}

	mergePodTemplateMetadata(existing, desired)

	if existing.Name != "seaweed-filer" {
		t.Errorf("Name = %q, want the desired value", existing.Name)
	}
	if got := existing.Annotations["external"]; got != "keep" {
		t.Errorf("external annotation = %q, want it preserved", got)
	}
}

// Both sides start empty on a freshly created workload.
func TestMergePodTemplateMetadataHandlesNilMaps(t *testing.T) {
	existing := &corev1.PodTemplateSpec{}
	desired := &corev1.PodTemplateSpec{}

	mergePodTemplateMetadata(existing, desired)

	if existing.Annotations == nil || len(existing.Annotations) != 0 {
		t.Errorf("Annotations = %v, want empty and non-nil", existing.Annotations)
	}
	if existing.Labels == nil || len(existing.Labels) != 0 {
		t.Errorf("Labels = %v, want empty and non-nil", existing.Labels)
	}
}
