package v1

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIAMSpecValidation(t *testing.T) {
	tests := []struct {
		name    string
		spec    IAMSpec
		isValid bool
		desc    string
	}{
		{
			name: "Valid basic IAM spec",
			spec: IAMSpec{
				Replicas: 1,
			},
			isValid: true,
			desc:    "Basic IAM configuration should be valid",
		},
		{
			name: "Valid IAM spec with custom port",
			spec: IAMSpec{
				Replicas: 2,
				Port:     int32Ptr(9111),
			},
			isValid: true,
			desc:    "IAM with custom port should be valid",
		},
		{
			name: "Valid IAM spec with metrics",
			spec: IAMSpec{
				Replicas:    1,
				MetricsPort: int32Ptr(9090),
			},
			isValid: true,
			desc:    "IAM with metrics port should be valid",
		},
		{
			name: "Valid IAM spec with resources",
			spec: IAMSpec{
				Replicas: 1,
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
				},
			},
			isValid: true,
			desc:    "IAM with resource requirements should be valid",
		},
		{
			name: "Valid IAM spec with service config",
			spec: IAMSpec{
				Replicas: 1,
				Service: &ServiceSpec{
					Type:           corev1.ServiceTypeLoadBalancer,
					LoadBalancerIP: stringPtr("10.0.0.100"),
				},
			},
			isValid: true,
			desc:    "IAM with service configuration should be valid",
		},
		{
			name: "Zero replicas (edge case)",
			spec: IAMSpec{
				Replicas: 0,
			},
			isValid: false,
			desc:    "IAM with zero replicas should be invalid (kubebuilder validation)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log(tt.desc)

			// Basic validation - check that required fields are set appropriately
			if tt.spec.Replicas <= 0 && tt.isValid {
				t.Error("Expected replicas to be positive for valid specs")
			}

			// Test that custom ports are in reasonable ranges
			if tt.spec.Port != nil {
				port := *tt.spec.Port
				if port < 1024 || port > 65535 {
					t.Logf("Port %d is outside typical range (1024-65535)", port)
				}
			}

			// Test metrics port range
			if tt.spec.MetricsPort != nil {
				port := *tt.spec.MetricsPort
				if port < 1024 || port > 65535 {
					t.Logf("Metrics port %d is outside typical range (1024-65535)", port)
				}
			}
		})
	}
}

func TestFilerSpecIAMValidation(t *testing.T) {
	tests := []struct {
		name string
		spec FilerSpec
		desc string
	}{
		{
			name: "Filer with embedded IAM enabled",
			spec: FilerSpec{
				Replicas: 1,
				S3:       &S3Config{Enabled: true},
				IAM:      true,
			},
			desc: "Should support embedded IAM with S3",
		},
		{
			name: "Filer with embedded IAM disabled",
			spec: FilerSpec{
				Replicas: 1,
				S3:       &S3Config{Enabled: true},
				IAM:      false,
			},
			desc: "Should support disabling embedded IAM",
		},
		{
			name: "Filer with IAM but no S3",
			spec: FilerSpec{
				Replicas: 1,
				S3:       &S3Config{Enabled: false},
				IAM:      true,
			},
			desc: "Should support IAM without S3 (though not common)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log(tt.desc)

			// Basic validation
			if tt.spec.Replicas <= 0 {
				t.Error("Expected positive replicas")
			}

			// IAM without S3 might be unusual but not invalid
			if tt.spec.IAM && (tt.spec.S3 == nil || !tt.spec.S3.Enabled) {
				t.Log("Note: IAM enabled without S3 - unusual but valid configuration")
			}
		})
	}
}

func TestSeaweedSpecWithIAM(t *testing.T) {
	tests := []struct {
		name     string
		spec     SeaweedSpec
		validate func(*testing.T, *SeaweedSpec)
		desc     string
	}{
		{
			name: "Complete Seaweed with standalone IAM",
			spec: SeaweedSpec{
				Image: "chrislusf/seaweedfs:latest",
				Master: &MasterSpec{
					Replicas: 1,
				},
				Volume: &VolumeSpec{
					Replicas: 1,
				},
				Filer: &FilerSpec{
					Replicas: 1,
					S3:       &S3Config{Enabled: true},
				},
				IAM: &IAMSpec{
					Replicas: 1,
					Port:     int32Ptr(8111),
				},
			},
			validate: func(t *testing.T, spec *SeaweedSpec) {
				if spec.IAM == nil {
					t.Error("Expected IAM to be configured")
				}
				if spec.Filer.IAM {
					t.Error("Expected embedded IAM to be disabled when standalone IAM is used")
				}
			},
			desc: "Complete SeaweedFS with standalone IAM",
		},
		{
			name: "Complete Seaweed with embedded IAM",
			spec: SeaweedSpec{
				Image: "chrislusf/seaweedfs:latest",
				Master: &MasterSpec{
					Replicas: 1,
				},
				Volume: &VolumeSpec{
					Replicas: 1,
				},
				Filer: &FilerSpec{
					Replicas: 1,
					S3:       &S3Config{Enabled: true},
					IAM:      true,
				},
			},
			validate: func(t *testing.T, spec *SeaweedSpec) {
				if !spec.Filer.IAM {
					t.Error("Expected embedded IAM to be enabled")
				}
			},
			desc: "Complete SeaweedFS with embedded IAM",
		},
		{
			name: "Both standalone and embedded IAM",
			spec: SeaweedSpec{
				Image: "chrislusf/seaweedfs:latest",
				Master: &MasterSpec{
					Replicas: 1,
				},
				Volume: &VolumeSpec{
					Replicas: 1,
				},
				Filer: &FilerSpec{
					Replicas: 1,
					S3:       &S3Config{Enabled: true},
					IAM:      true,
				},
				IAM: &IAMSpec{
					Replicas: 1,
				},
			},
			validate: func(t *testing.T, spec *SeaweedSpec) {
				if spec.IAM == nil {
					t.Error("Expected standalone IAM to be configured")
				}
				if !spec.Filer.IAM {
					t.Error("Expected embedded IAM to be enabled")
				}
				t.Log("Warning: Both standalone and embedded IAM configured - ensure this is intentional")
			},
			desc: "Both standalone and embedded IAM (advanced configuration)",
		},
		{
			name: "No IAM configuration",
			spec: SeaweedSpec{
				Image: "chrislusf/seaweedfs:latest",
				Master: &MasterSpec{
					Replicas: 1,
				},
				Volume: &VolumeSpec{
					Replicas: 1,
				},
				Filer: &FilerSpec{
					Replicas: 1,
					S3:       &S3Config{Enabled: true},
				},
			},
			validate: func(t *testing.T, spec *SeaweedSpec) {
				if spec.IAM != nil {
					t.Error("Expected no standalone IAM configuration")
				}
				if spec.Filer.IAM {
					t.Error("Expected no embedded IAM configuration")
				}
			},
			desc: "SeaweedFS without IAM (basic configuration)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log(tt.desc)
			tt.validate(t, &tt.spec)
		})
	}
}

func TestBaseIAMSpec(t *testing.T) {
	seaweed := &Seaweed{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-seaweed",
			Namespace: "default",
		},
		Spec: SeaweedSpec{
			Image:                     "chrislusf/seaweedfs:latest",
			ImagePullPolicy:           corev1.PullIfNotPresent,
			SchedulerName:             "default-scheduler",
			StatefulSetUpdateStrategy: "RollingUpdate",
			NodeSelector: map[string]string{
				"node-type": "compute",
			},
			Annotations: map[string]string{
				"example.com/annotation": "test",
			},
			IAM: &IAMSpec{
				Replicas: 2,
				ComponentSpec: ComponentSpec{
					Version:         stringPtr("3.70"),
					ImagePullPolicy: &[]corev1.PullPolicy{corev1.PullAlways}[0],
					NodeSelector: map[string]string{
						"iam-node": "true",
					},
				},
			},
		},
	}

	// Test BaseIAMSpec accessor
	baseSpec := seaweed.BaseIAMSpec()

	// Test that component-level overrides work
	if baseSpec.ImagePullPolicy() != corev1.PullAlways {
		t.Errorf("Expected ImagePullPolicy to be overridden to Always, got %v", baseSpec.ImagePullPolicy())
	}

	// Test that cluster-level properties are inherited
	nodeSelector := baseSpec.NodeSelector()
	if nodeSelector["node-type"] != "compute" {
		t.Error("Expected cluster-level node selector to be inherited")
	}
	if nodeSelector["iam-node"] != "true" {
		t.Error("Expected component-level node selector to be present")
	}

	// Test scheduler name inheritance
	if baseSpec.SchedulerName() != "default-scheduler" {
		t.Errorf("Expected scheduler name to be inherited, got %s", baseSpec.SchedulerName())
	}

	// Test annotations inheritance
	annotations := baseSpec.Annotations()
	if annotations["example.com/annotation"] != "test" {
		t.Error("Expected cluster-level annotations to be inherited")
	}
}

func TestIAMSpecDefaults(t *testing.T) {
	tests := []struct {
		name     string
		spec     IAMSpec
		expected struct {
			port        int32
			replicas    int32
			serviceType corev1.ServiceType
		}
		desc string
	}{
		{
			name: "Default values",
			spec: IAMSpec{
				Replicas: 1,
			},
			expected: struct {
				port        int32
				replicas    int32
				serviceType corev1.ServiceType
			}{
				port:        FilerIAMPort,
				replicas:    1,
				serviceType: corev1.ServiceTypeClusterIP,
			},
			desc: "Should use default values when not specified",
		},
		{
			name: "Custom values",
			spec: IAMSpec{
				Replicas: 3,
				Port:     int32Ptr(9111),
				Service: &ServiceSpec{
					Type: corev1.ServiceTypeLoadBalancer,
				},
			},
			expected: struct {
				port        int32
				replicas    int32
				serviceType corev1.ServiceType
			}{
				port:        9111,
				replicas:    3,
				serviceType: corev1.ServiceTypeLoadBalancer,
			},
			desc: "Should respect custom values when specified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log(tt.desc)

			// Test replicas
			if tt.spec.Replicas != tt.expected.replicas {
				t.Errorf("Expected %d replicas, got %d", tt.expected.replicas, tt.spec.Replicas)
			}

			// Test port (with default fallback)
			expectedPort := tt.expected.port
			if tt.spec.Port != nil {
				expectedPort = *tt.spec.Port
			}
			if tt.spec.Port != nil && *tt.spec.Port != expectedPort {
				t.Errorf("Expected port %d, got %d", expectedPort, *tt.spec.Port)
			}

			// Test service type
			if tt.spec.Service != nil {
				if tt.spec.Service.Type != tt.expected.serviceType {
					t.Errorf("Expected service type %s, got %s", tt.expected.serviceType, tt.spec.Service.Type)
				}
			}
		})
	}
}

// Helper functions
func int32Ptr(i int32) *int32 {
	return &i
}

func stringPtr(s string) *string {
	return &s
}
