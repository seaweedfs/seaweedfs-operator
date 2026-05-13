package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func TestFilterContainerResources(t *testing.T) {
	// Test with various resource types
	input := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:              resource.MustParse("500m"),
			corev1.ResourceMemory:           resource.MustParse("1Gi"),
			corev1.ResourceStorage:          resource.MustParse("10Gi"),
			corev1.ResourceEphemeralStorage: resource.MustParse("1Gi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:              resource.MustParse("1000m"),
			corev1.ResourceMemory:           resource.MustParse("2Gi"),
			corev1.ResourceStorage:          resource.MustParse("20Gi"),
			corev1.ResourceEphemeralStorage: resource.MustParse("2Gi"),
		},
	}

	filtered := filterContainerResources(input)

	// Verify storage is removed from requests
	if _, exists := filtered.Requests[corev1.ResourceStorage]; exists {
		t.Errorf("Expected storage to be filtered out from requests")
	}

	// Verify storage is removed from limits
	if _, exists := filtered.Limits[corev1.ResourceStorage]; exists {
		t.Errorf("Expected storage to be filtered out from limits")
	}

	// Verify other resources are preserved
	expectedResources := []corev1.ResourceName{
		corev1.ResourceCPU,
		corev1.ResourceMemory,
		corev1.ResourceEphemeralStorage,
	}

	for _, resource := range expectedResources {
		if _, exists := filtered.Requests[resource]; !exists {
			t.Errorf("Expected %s to be preserved in requests", resource)
		}
		if _, exists := filtered.Limits[resource]; !exists {
			t.Errorf("Expected %s to be preserved in limits", resource)
		}
	}

	// Verify values are correct
	if !filtered.Requests[corev1.ResourceCPU].Equal(resource.MustParse("500m")) {
		t.Errorf("CPU request value mismatch")
	}
	if !filtered.Limits[corev1.ResourceMemory].Equal(resource.MustParse("2Gi")) {
		t.Errorf("Memory limit value mismatch")
	}
}

func TestFilterContainerResourcesEmpty(t *testing.T) {
	// Test with empty ResourceRequirements
	input := corev1.ResourceRequirements{}
	filtered := filterContainerResources(input)

	if filtered.Requests != nil {
		t.Errorf("Expected empty requests to remain nil")
	}
	if filtered.Limits != nil {
		t.Errorf("Expected empty limits to remain nil")
	}
}

// TestBuildVolumeServerStartupScriptWithTopologyNilVolume guards against
// the panic surfaced by issue #244 once VolumeTopology was exercised
// without a flat spec.volume: the builder previously dereferenced
// m.Spec.Volume.<field> unconditionally, crashing the reconciler on
// topology-only deployments.
func TestBuildVolumeServerStartupScriptWithTopologyNilVolume(t *testing.T) {
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1},
		},
	}
	topo := &seaweedv1.VolumeTopologySpec{
		VolumeServerConfig: seaweedv1.VolumeServerConfig{},
		Rack:               "rack1",
		DataCenter:         "dc1",
	}
	// Must not panic.
	got := buildVolumeServerStartupScriptWithTopology(m, []string{"/data0"}, "rack1", topo)
	if got == "" {
		t.Fatal("expected non-empty startup script")
	}
}

// TestBuildTopologyPodSpecServiceAccountName locks in that the
// VolumeTopology pod-spec builder (which constructs PodSpec manually
// rather than via ComponentAccessor.BuildPodSpec) also propagates
// serviceAccountName — see issue #244.
func TestBuildTopologyPodSpecServiceAccountName(t *testing.T) {
	sa := "seaweedfs-volume-rack1"
	cases := []struct {
		name     string
		set      *string
		expected string
	}{
		{"unset preserves default SA fallback", nil, ""},
		{"explicit value is propagated", &sa, sa},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
			}
			topo := &seaweedv1.VolumeTopologySpec{
				VolumeServerConfig: seaweedv1.VolumeServerConfig{
					ComponentSpec: seaweedv1.ComponentSpec{ServiceAccountName: tc.set},
				},
				Rack:       "rack1",
				DataCenter: "dc1",
			}
			got := buildTopologyPodSpec(m, topo).ServiceAccountName
			if got != tc.expected {
				t.Fatalf("ServiceAccountName = %q, want %q", got, tc.expected)
			}
		})
	}
}

// TestGetFilerAddress pins the filer address format; a regression here
// resurfaces issue #237.
func TestGetFilerAddress(t *testing.T) {
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "seaweed", Namespace: "seaweedfs"},
	}
	got := getFilerAddress(m)
	want := "seaweed-filer.seaweedfs:8888"
	if got != want {
		t.Fatalf("getFilerAddress = %q, want %q", got, want)
	}
}

// TestMergePodLabels pins the contract for issue #243: user-supplied labels
// are added to the pod template, but operator-managed selector labels always
// win on key collisions so the StatefulSet/Deployment selector keeps matching.
func TestMergePodLabels(t *testing.T) {
	selector := map[string]string{
		"app.kubernetes.io/name":       "seaweedfs",
		"app.kubernetes.io/instance":   "seaweed",
		"app.kubernetes.io/component":  "filer",
		"app.kubernetes.io/managed-by": "seaweedfs-operator",
	}

	t.Run("nil user labels returns the selector unchanged", func(t *testing.T) {
		got := mergePodLabels(selector, nil)
		if len(got) != len(selector) {
			t.Fatalf("len = %d, want %d", len(got), len(selector))
		}
		for k, v := range selector {
			if got[k] != v {
				t.Errorf("got[%q] = %q, want %q", k, got[k], v)
			}
		}
	})

	t.Run("user labels are added alongside selector labels", func(t *testing.T) {
		user := map[string]string{"backup": "true", "team": "storage"}
		got := mergePodLabels(selector, user)
		for k, v := range selector {
			if got[k] != v {
				t.Errorf("selector label %q lost: got %q, want %q", k, got[k], v)
			}
		}
		for k, v := range user {
			if got[k] != v {
				t.Errorf("user label %q missing: got %q, want %q", k, got[k], v)
			}
		}
	})

	t.Run("user cannot override operator selector keys", func(t *testing.T) {
		user := map[string]string{
			"app.kubernetes.io/component": "hijacked",
			"backup":                      "true",
		}
		got := mergePodLabels(selector, user)
		if got["app.kubernetes.io/component"] != "filer" {
			t.Errorf("operator component label was overridden: got %q, want %q",
				got["app.kubernetes.io/component"], "filer")
		}
		if got["backup"] != "true" {
			t.Errorf("user label backup missing: got %q", got["backup"])
		}
	})
}
