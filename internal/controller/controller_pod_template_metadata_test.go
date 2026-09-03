package controller

import (
	"reflect"
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

	mergePodTemplateMetadata(&metav1.ObjectMeta{}, existing, desired)

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

	mergePodTemplateMetadata(&metav1.ObjectMeta{}, existing, desired)

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

	mergePodTemplateMetadata(&metav1.ObjectMeta{}, existing, desired)

	if existing.Annotations == nil || len(existing.Annotations) != 0 {
		t.Errorf("Annotations = %v, want empty and non-nil", existing.Annotations)
	}
	if existing.Labels == nil || len(existing.Labels) != 0 {
		t.Errorf("Labels = %v, want empty and non-nil", existing.Labels)
	}
}

// A key the operator rendered last time and no longer does is removed; keys it
// never managed stay. The record it leaves behind lists the rendered keys in
// sorted order, so a converged workload is not rewritten every pass.
func TestMergePodTemplateMetadataRemovesKeysItStoppedRendering(t *testing.T) {
	owner := &metav1.ObjectMeta{Annotations: map[string]string{
		LastAppliedPodTemplateKeys: `{"labels":["app","tier"],"annotations":["checksum/config","seaweed.seaweedfs.com/jwt-signing"]}`,
	}}
	existing := &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				"app":              "seaweedfs",
				"tier":             "storage",
				"example.com/team": "storage",
			},
			Annotations: map[string]string{
				"checksum/config":                   "v2",
				"seaweed.seaweedfs.com/jwt-signing": "volumeWrite",
				"kubectl.kubernetes.io/restartedAt": "2026-09-03T12:23:43Z",
			},
		},
	}
	desired := &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				"app":       "seaweedfs",
				"component": "filer",
			},
			Annotations: map[string]string{
				"checksum/config": "v3",
				"alpha/beta":      "1",
			},
		},
	}

	mergePodTemplateMetadata(owner, existing, desired)

	wantLabels := map[string]string{
		"app":              "seaweedfs",
		"component":        "filer",
		"example.com/team": "storage",
	}
	wantAnnotations := map[string]string{
		"checksum/config":                   "v3",
		"alpha/beta":                        "1",
		"kubectl.kubernetes.io/restartedAt": "2026-09-03T12:23:43Z",
	}
	if !reflect.DeepEqual(existing.Labels, wantLabels) {
		t.Errorf("labels = %v, want %v", existing.Labels, wantLabels)
	}
	if !reflect.DeepEqual(existing.Annotations, wantAnnotations) {
		t.Errorf("annotations = %v, want %v", existing.Annotations, wantAnnotations)
	}
	wantRecord := `{"labels":["app","component"],"annotations":["alpha/beta","checksum/config"]}`
	if got := owner.Annotations[LastAppliedPodTemplateKeys]; got != wantRecord {
		t.Errorf("record = %s, want %s", got, wantRecord)
	}
}

// Without a record nothing is removed: on the first pass after an upgrade a
// stale operator key cannot be told apart from one another controller owns.
func TestMergePodTemplateMetadataWithoutRecordOnlyOverlays(t *testing.T) {
	owner := &metav1.ObjectMeta{}
	existing := &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      map[string]string{"app": "seaweedfs", "tier": "storage"},
			Annotations: map[string]string{"kubectl.kubernetes.io/restartedAt": "2026-09-03T12:23:43Z"},
		},
	}
	desired := &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "seaweedfs"}},
	}

	mergePodTemplateMetadata(owner, existing, desired)

	if got := existing.Labels["tier"]; got != "storage" {
		t.Errorf("label with no record was removed, got %q", got)
	}
	if got := existing.Annotations["kubectl.kubernetes.io/restartedAt"]; got == "" {
		t.Error("annotation set by another controller was dropped")
	}
	if got := owner.Annotations[LastAppliedPodTemplateKeys]; got != `{"labels":["app"]}` {
		t.Errorf("record = %s, want the rendered keys", got)
	}
}
