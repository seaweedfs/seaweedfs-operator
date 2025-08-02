package controller

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func TestCreateIAMStatefulSet(t *testing.T) {
	tests := []struct {
		name     string
		seaweed  *seaweedv1.Seaweed
		validate func(*testing.T, *appsv1.StatefulSet)
	}{
		{
			name: "Basic IAM StatefulSet",
			seaweed: &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-seaweed",
					Namespace: "default",
				},
				Spec: seaweedv1.SeaweedSpec{
					Image: "chrislusf/seaweedfs:latest",
					Master: &seaweedv1.MasterSpec{
						Replicas: 1,
					},
					IAM: &seaweedv1.IAMSpec{
						Replicas: 1,
					},
				},
			},
			validate: func(t *testing.T, sts *appsv1.StatefulSet) {
				if sts.Name != "test-seaweed-iam" {
					t.Errorf("Expected name 'test-seaweed-iam', got %s", sts.Name)
				}
				if *sts.Spec.Replicas != 1 {
					t.Errorf("Expected 1 replica, got %d", *sts.Spec.Replicas)
				}
				if len(sts.Spec.Template.Spec.Containers) != 1 {
					t.Errorf("Expected 1 container, got %d", len(sts.Spec.Template.Spec.Containers))
				}
				container := sts.Spec.Template.Spec.Containers[0]
				if container.Name != "iam" {
					t.Errorf("Expected container name 'iam', got %s", container.Name)
				}
				if container.Image != "chrislusf/seaweedfs:latest" {
					t.Errorf("Expected image 'chrislusf/seaweedfs:latest', got %s", container.Image)
				}
				// Check default IAM port
				found := false
				for _, port := range container.Ports {
					if port.Name == "iam-http" && port.ContainerPort == seaweedv1.FilerIAMPort {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected IAM HTTP port %d not found", seaweedv1.FilerIAMPort)
				}
			},
		},
		{
			name: "IAM StatefulSet with custom port",
			seaweed: &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-seaweed",
					Namespace: "default",
				},
				Spec: seaweedv1.SeaweedSpec{
					Image: "chrislusf/seaweedfs:latest",
					Master: &seaweedv1.MasterSpec{
						Replicas: 1,
					},
					IAM: &seaweedv1.IAMSpec{
						Replicas: 2,
						Port:     int32Ptr(9111),
					},
				},
			},
			validate: func(t *testing.T, sts *appsv1.StatefulSet) {
				if *sts.Spec.Replicas != 2 {
					t.Errorf("Expected 2 replicas, got %d", *sts.Spec.Replicas)
				}
				container := sts.Spec.Template.Spec.Containers[0]
				// Check custom IAM port
				found := false
				for _, port := range container.Ports {
					if port.Name == "iam-http" && port.ContainerPort == 9111 {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected custom IAM HTTP port 9111 not found")
				}
			},
		},
		{
			name: "IAM StatefulSet with metrics port",
			seaweed: &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-seaweed",
					Namespace: "default",
				},
				Spec: seaweedv1.SeaweedSpec{
					Image: "chrislusf/seaweedfs:latest",
					Master: &seaweedv1.MasterSpec{
						Replicas: 1,
					},
					IAM: &seaweedv1.IAMSpec{
						Replicas:    1,
						MetricsPort: int32Ptr(9090),
					},
				},
			},
			validate: func(t *testing.T, sts *appsv1.StatefulSet) {
				container := sts.Spec.Template.Spec.Containers[0]
				// Check metrics port
				found := false
				for _, port := range container.Ports {
					if port.Name == "iam-metrics" && port.ContainerPort == 9090 {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected IAM metrics port 9090 not found")
				}
			},
		},
		{
			name: "IAM StatefulSet with resources",
			seaweed: &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-seaweed",
					Namespace: "default",
				},
				Spec: seaweedv1.SeaweedSpec{
					Image: "chrislusf/seaweedfs:latest",
					Master: &seaweedv1.MasterSpec{
						Replicas: 1,
					},
					IAM: &seaweedv1.IAMSpec{
						Replicas: 1,
						ResourceRequirements: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("128Mi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("200m"),
								corev1.ResourceMemory: resource.MustParse("256Mi"),
							},
						},
					},
				},
			},
			validate: func(t *testing.T, sts *appsv1.StatefulSet) {
				container := sts.Spec.Template.Spec.Containers[0]

				// Check CPU request
				cpuRequest := container.Resources.Requests[corev1.ResourceCPU]
				if cpuRequest.String() != "100m" {
					t.Errorf("Expected CPU request '100m', got %s", cpuRequest.String())
				}

				// Check memory limit
				memLimit := container.Resources.Limits[corev1.ResourceMemory]
				if memLimit.String() != "256Mi" {
					t.Errorf("Expected memory limit '256Mi', got %s", memLimit.String())
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &SeaweedReconciler{}
			sts := reconciler.createIAMStatefulSet(tt.seaweed)
			tt.validate(t, sts)
		})
	}
}

func TestCreateIAMService(t *testing.T) {
	tests := []struct {
		name     string
		seaweed  *seaweedv1.Seaweed
		validate func(*testing.T, *corev1.Service)
	}{
		{
			name: "Basic IAM Service",
			seaweed: &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-seaweed",
					Namespace: "default",
				},
				Spec: seaweedv1.SeaweedSpec{
					Master: &seaweedv1.MasterSpec{
						Replicas: 1,
					},
					IAM: &seaweedv1.IAMSpec{
						Replicas: 1,
					},
				},
			},
			validate: func(t *testing.T, svc *corev1.Service) {
				if svc.Name != "test-seaweed-iam" {
					t.Errorf("Expected name 'test-seaweed-iam', got %s", svc.Name)
				}
				if svc.Spec.Type != corev1.ServiceTypeClusterIP {
					t.Errorf("Expected ClusterIP service type, got %s", svc.Spec.Type)
				}

				// Check default IAM port
				found := false
				for _, port := range svc.Spec.Ports {
					if port.Name == "iam-http" && port.Port == seaweedv1.FilerIAMPort {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected IAM HTTP port %d not found", seaweedv1.FilerIAMPort)
				}
			},
		},
		{
			name: "IAM Service with custom port",
			seaweed: &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-seaweed",
					Namespace: "default",
				},
				Spec: seaweedv1.SeaweedSpec{
					Master: &seaweedv1.MasterSpec{
						Replicas: 1,
					},
					IAM: &seaweedv1.IAMSpec{
						Replicas: 1,
						Port:     int32Ptr(9111),
					},
				},
			},
			validate: func(t *testing.T, svc *corev1.Service) {
				// Check custom IAM port
				found := false
				for _, port := range svc.Spec.Ports {
					if port.Name == "iam-http" && port.Port == 9111 {
						found = true
						if port.TargetPort != intstr.FromInt(9111) {
							t.Errorf("Expected target port 9111, got %v", port.TargetPort)
						}
						break
					}
				}
				if !found {
					t.Errorf("Expected custom IAM HTTP port 9111 not found")
				}
			},
		},
		{
			name: "IAM Service with metrics port",
			seaweed: &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-seaweed",
					Namespace: "default",
				},
				Spec: seaweedv1.SeaweedSpec{
					Master: &seaweedv1.MasterSpec{
						Replicas: 1,
					},
					IAM: &seaweedv1.IAMSpec{
						Replicas:    1,
						MetricsPort: int32Ptr(9090),
					},
				},
			},
			validate: func(t *testing.T, svc *corev1.Service) {
				// Check metrics port
				found := false
				for _, port := range svc.Spec.Ports {
					if port.Name == "iam-metrics" && port.Port == 9090 {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected IAM metrics port 9090 not found")
				}
			},
		},
		{
			name: "IAM Service with custom service spec",
			seaweed: &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-seaweed",
					Namespace: "default",
				},
				Spec: seaweedv1.SeaweedSpec{
					Master: &seaweedv1.MasterSpec{
						Replicas: 1,
					},
					IAM: &seaweedv1.IAMSpec{
						Replicas: 1,
						Service: &seaweedv1.ServiceSpec{
							Type:           corev1.ServiceTypeLoadBalancer,
							LoadBalancerIP: stringPtr("10.0.0.100"),
						},
					},
				},
			},
			validate: func(t *testing.T, svc *corev1.Service) {
				if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
					t.Errorf("Expected LoadBalancer service type, got %s", svc.Spec.Type)
				}
				if svc.Spec.LoadBalancerIP != "10.0.0.100" {
					t.Errorf("Expected LoadBalancer IP '10.0.0.100', got %s", svc.Spec.LoadBalancerIP)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &SeaweedReconciler{}
			svc := reconciler.createIAMService(tt.seaweed)
			tt.validate(t, svc)
		})
	}
}

func TestBuildIAMStartupScript(t *testing.T) {
	tests := []struct {
		name     string
		seaweed  *seaweedv1.Seaweed
		expected []string // Expected command parts
	}{
		{
			name: "Basic IAM startup script",
			seaweed: &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-seaweed",
					Namespace: "default",
				},
				Spec: seaweedv1.SeaweedSpec{
					Master: &seaweedv1.MasterSpec{
						Replicas: 1,
					},
					IAM: &seaweedv1.IAMSpec{
						Replicas: 1,
					},
				},
			},
			expected: []string{
				"weed",
				"-logtostderr=true",
				"iam",
				"-master=test-seaweed-master-0.test-seaweed-master-peer.default:9333",
				"-filer=test-seaweed-filer:8888",
				"-port=8111",
			},
		},
		{
			name: "IAM startup script with custom port",
			seaweed: &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-seaweed",
					Namespace: "default",
				},
				Spec: seaweedv1.SeaweedSpec{
					Master: &seaweedv1.MasterSpec{
						Replicas: 1,
					},
					IAM: &seaweedv1.IAMSpec{
						Replicas: 1,
						Port:     int32Ptr(9111),
					},
				},
			},
			expected: []string{
				"weed",
				"-logtostderr=true",
				"iam",
				"-master=test-seaweed-master-0.test-seaweed-master-peer.default:9333",
				"-filer=test-seaweed-filer:8888",
				"-port=9111",
			},
		},
		{
			name: "IAM startup script with metrics port",
			seaweed: &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-seaweed",
					Namespace: "default",
				},
				Spec: seaweedv1.SeaweedSpec{
					Master: &seaweedv1.MasterSpec{
						Replicas: 1,
					},
					IAM: &seaweedv1.IAMSpec{
						Replicas:    1,
						MetricsPort: int32Ptr(9090),
					},
				},
			},
			expected: []string{
				"weed",
				"-logtostderr=true",
				"iam",
				"-master=test-seaweed-master-0.test-seaweed-master-peer.default:9333",
				"-filer=test-seaweed-filer:8888",
				"-port=8111",
				"-metricsPort=9090",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildIAMStartupScript(tt.seaweed)

			// Check if all expected parts are in the result
			for _, expected := range tt.expected {
				if !contains(result, expected) {
					t.Errorf("Expected '%s' to be in startup script, got: %s", expected, result)
				}
			}
		})
	}
}

func TestLabelsForIAM(t *testing.T) {
	labels := labelsForIAM("test-seaweed")

	expectedLabels := map[string]string{
		"app.kubernetes.io/name":      "seaweedfs",
		"app.kubernetes.io/instance":  "test-seaweed",
		"app.kubernetes.io/component": "iam",
	}

	for key, expectedValue := range expectedLabels {
		if actualValue, exists := labels[key]; !exists {
			t.Errorf("Expected label %s not found", key)
		} else if actualValue != expectedValue {
			t.Errorf("Expected label %s to be %s, got %s", key, expectedValue, actualValue)
		}
	}
}

// Helper functions
func int32Ptr(i int32) *int32 {
	return &i
}

func stringPtr(s string) *string {
	return &s
}

func contains(str, substr string) bool {
	return strings.Contains(str, substr)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
