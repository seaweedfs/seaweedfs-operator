package v1

import corev1 "k8s.io/api/core/v1"

type VectorSpec struct {
	ComponentSpec               `json:",inline"`
	corev1.ResourceRequirements `json:",inline"`
	// +kubebuilder:default:=1
	// +kubebuilder:validation:Minimum=0
	Replicas int32 `json:"replicas"`
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port *int32 `json:"port,omitempty"`
	// MetricsPort enables HTTP probes and Prometheus on engines with metrics support.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	MetricsPort *int32 `json:"metricsPort,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	ReadCacheMB *int64 `json:"readCacheMB,omitempty"`
}

func (s *Seaweed) BaseVectorSpec() ComponentAccessor {
	return buildSeaweedComponentAccessor(&s.Spec, &s.Spec.Vector.ComponentSpec)
}
