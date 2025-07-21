package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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
