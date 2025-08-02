package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// Mock reconciler for integration tests
func createMockReconciler() *SeaweedReconciler {
	// Add our custom resource to the scheme
	s := scheme.Scheme
	err := seaweedv1.AddToScheme(s)
	if err != nil {
		panic(err)
	}

	// Create a fake client
	fakeClient := fake.NewClientBuilder().WithScheme(s).Build()

	return &SeaweedReconciler{
		Client: fakeClient,
		Scheme: s,
	}
}

func TestEnsureIAMIntegration(t *testing.T) {
	tests := []struct {
		name         string
		seaweed      *seaweedv1.Seaweed
		expectError  bool
		validateFunc func(*testing.T, client.Client, *seaweedv1.Seaweed)
	}{
		{
			name: "Create standalone IAM resources",
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
					Volume: &seaweedv1.VolumeSpec{
						Replicas: 1,
					},
					Filer: &seaweedv1.FilerSpec{
						Replicas: 1,
					},
					IAM: &seaweedv1.IAMSpec{
						Replicas: 1,
						ResourceRequirements: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("128Mi"),
							},
						},
					},
				},
			},
			expectError: false,
			validateFunc: func(t *testing.T, c client.Client, seaweed *seaweedv1.Seaweed) {
				ctx := context.Background()

				// Check StatefulSet was created
				sts := &appsv1.StatefulSet{}
				stsErr := c.Get(ctx, types.NamespacedName{
					Name:      "test-seaweed-iam",
					Namespace: "default",
				}, sts)
				if stsErr != nil {
					t.Errorf("Expected IAM StatefulSet to be created, got error: %v", stsErr)
				}

				// Check Service was created
				svc := &corev1.Service{}
				svcErr := c.Get(ctx, types.NamespacedName{
					Name:      "test-seaweed-iam",
					Namespace: "default",
				}, svc)
				if svcErr != nil {
					t.Errorf("Expected IAM Service to be created, got error: %v", svcErr)
				}

				// Validate StatefulSet properties (only if no error)
				if stsErr == nil && *sts.Spec.Replicas != 1 {
					t.Errorf("Expected 1 replica, got %d", *sts.Spec.Replicas)
				}

				// Validate Service properties (only if no error)
				if svcErr == nil && svc.Spec.Type != corev1.ServiceTypeClusterIP {
					t.Errorf("Expected ClusterIP service, got %s", svc.Spec.Type)
				}

				// Check IAM port (only if service was created successfully)
				if svcErr == nil {
					found := false
					for _, port := range svc.Spec.Ports {
						if port.Name == "iam-http" && port.Port == seaweedv1.FilerIAMPort {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("Expected IAM port %d not found in service", seaweedv1.FilerIAMPort)
					}
				}
			},
		},
		{
			name: "Handle IAM not configured",
			seaweed: &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-seaweed-no-iam",
					Namespace: "default",
				},
				Spec: seaweedv1.SeaweedSpec{
					Image: "chrislusf/seaweedfs:latest",
					Master: &seaweedv1.MasterSpec{
						Replicas: 1,
					},
					// No IAM specified
				},
			},
			expectError: false,
			validateFunc: func(t *testing.T, c client.Client, seaweed *seaweedv1.Seaweed) {
				ctx := context.Background()

				// Check StatefulSet was NOT created
				sts := &appsv1.StatefulSet{}
				err := c.Get(ctx, types.NamespacedName{
					Name:      "test-seaweed-no-iam-iam",
					Namespace: "default",
				}, sts)
				if !errors.IsNotFound(err) {
					t.Errorf("Expected IAM StatefulSet not to be created, but found: %v", sts)
				}

				// Check Service was NOT created
				svc := &corev1.Service{}
				err = c.Get(ctx, types.NamespacedName{
					Name:      "test-seaweed-no-iam-iam",
					Namespace: "default",
				}, svc)
				if !errors.IsNotFound(err) {
					t.Errorf("Expected IAM Service not to be created, but found: %v", svc)
				}
			},
		},
		{
			name: "Create IAM with custom configuration",
			seaweed: &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-seaweed-custom",
					Namespace: "default",
				},
				Spec: seaweedv1.SeaweedSpec{
					Image: "chrislusf/seaweedfs:latest",
					Master: &seaweedv1.MasterSpec{
						Replicas: 1,
					},
					IAM: &seaweedv1.IAMSpec{
						Replicas:    2,
						Port:        int32Ptr(9111),
						MetricsPort: int32Ptr(9090),
						Service: &seaweedv1.ServiceSpec{
							Type: corev1.ServiceTypeLoadBalancer,
						},
					},
				},
			},
			expectError: false,
			validateFunc: func(t *testing.T, c client.Client, seaweed *seaweedv1.Seaweed) {
				ctx := context.Background()

				// Check StatefulSet
				sts := &appsv1.StatefulSet{}
				err := c.Get(ctx, types.NamespacedName{
					Name:      "test-seaweed-custom-iam",
					Namespace: "default",
				}, sts)
				if err != nil {
					t.Errorf("Expected IAM StatefulSet to be created, got error: %v", err)
					return
				}

				// Check replicas
				if *sts.Spec.Replicas != 2 {
					t.Errorf("Expected 2 replicas, got %d", *sts.Spec.Replicas)
				}

				// Check Service
				svc := &corev1.Service{}
				err = c.Get(ctx, types.NamespacedName{
					Name:      "test-seaweed-custom-iam",
					Namespace: "default",
				}, svc)
				if err != nil {
					t.Errorf("Expected IAM Service to be created, got error: %v", err)
					return
				}

				// Check service type
				if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
					t.Errorf("Expected LoadBalancer service, got %s", svc.Spec.Type)
				}

				// Check custom ports
				iamPortFound := false
				metricsPortFound := false
				for _, port := range svc.Spec.Ports {
					if port.Name == "iam-http" && port.Port == 9111 {
						iamPortFound = true
					}
					if port.Name == "iam-metrics" && port.Port == 9090 {
						metricsPortFound = true
					}
				}
				if !iamPortFound {
					t.Error("Expected custom IAM port 9111 not found")
				}
				if !metricsPortFound {
					t.Error("Expected metrics port 9090 not found")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := createMockReconciler()

			// Create the Seaweed resource
			ctx := context.Background()
			err := reconciler.Create(ctx, tt.seaweed)
			if err != nil {
				t.Fatalf("Failed to create Seaweed resource: %v", err)
			}

			// Run the IAM reconciliation
			done, result, err := reconciler.ensureIAM(tt.seaweed)

			// Check error expectation
			if tt.expectError && err == nil {
				t.Error("Expected an error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			// For successful operations, check results
			if !tt.expectError {
				// Should always be done=false on success (to continue to next components)
				// Only done=true when there's an error or requeue needed
				expectedDone := result.RequeueAfter > 0 || err != nil
				if done != expectedDone {
					t.Errorf("Expected done=%v, got done=%v, result=%v", expectedDone, done, result)
				}
			}

			// Run validation if provided
			if tt.validateFunc != nil {
				tt.validateFunc(t, reconciler.Client, tt.seaweed)
			}
		})
	}
}

func TestFullSeaweedReconcileWithIAM(t *testing.T) {
	reconciler := createMockReconciler()
	ctx := context.Background()

	// Create a comprehensive Seaweed resource with both standalone and embedded IAM
	seaweed := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "full-test-seaweed",
			Namespace: "default",
		},
		Spec: seaweedv1.SeaweedSpec{
			Image: "chrislusf/seaweedfs:latest",
			Master: &seaweedv1.MasterSpec{
				Replicas: 1,
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("256Mi"),
					},
				},
			},
			Volume: &seaweedv1.VolumeSpec{
				Replicas: 1,
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("200m"),
						corev1.ResourceMemory: resource.MustParse("512Mi"),
					},
				},
			},
			Filer: &seaweedv1.FilerSpec{
				Replicas: 1,
				S3:       &seaweedv1.S3Config{Enabled: true},
				IAM:      true, // Embedded IAM
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("150m"),
						corev1.ResourceMemory: resource.MustParse("384Mi"),
					},
				},
			},
			IAM: &seaweedv1.IAMSpec{
				Replicas: 1, // Standalone IAM
				Port:     int32Ptr(8111),
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
				},
			},
		},
	}

	// Create the resource
	err := reconciler.Create(ctx, seaweed)
	if err != nil {
		t.Fatalf("Failed to create Seaweed resource: %v", err)
	}

	// Simulate the full reconciliation (simplified version)
	done, result, err := reconciler.ensureIAM(seaweed)
	if err != nil {
		t.Fatalf("IAM reconciliation failed: %v", err)
	}

	t.Logf("IAM reconciliation: done=%v, result=%v", done, result)

	// Validate that both standalone and embedded IAM configurations work

	// Check standalone IAM StatefulSet
	iamSts := &appsv1.StatefulSet{}
	err = reconciler.Get(ctx, types.NamespacedName{
		Name:      "full-test-seaweed-iam",
		Namespace: "default",
	}, iamSts)
	if err != nil {
		t.Errorf("Expected standalone IAM StatefulSet to be created: %v", err)
	} else {
		t.Logf("Standalone IAM StatefulSet created successfully")
	}

	// Check standalone IAM Service
	iamSvc := &corev1.Service{}
	err = reconciler.Get(ctx, types.NamespacedName{
		Name:      "full-test-seaweed-iam",
		Namespace: "default",
	}, iamSvc)
	if err != nil {
		t.Errorf("Expected standalone IAM Service to be created: %v", err)
	} else {
		t.Logf("Standalone IAM Service created successfully")
	}

	// Note: For embedded IAM, we would need to check the filer StatefulSet and Service,
	// but that requires running the filer reconciliation as well, which is beyond
	// the scope of this IAM-focused test.
}

// Benchmark tests for performance
func BenchmarkCreateIAMStatefulSet(b *testing.B) {
	reconciler := &SeaweedReconciler{}
	seaweed := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bench-seaweed",
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
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = reconciler.createIAMStatefulSet(seaweed)
	}
}

func BenchmarkCreateIAMService(b *testing.B) {
	reconciler := &SeaweedReconciler{}
	seaweed := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bench-seaweed",
			Namespace: "default",
		},
		Spec: seaweedv1.SeaweedSpec{
			IAM: &seaweedv1.IAMSpec{
				Replicas: 1,
			},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = reconciler.createIAMService(seaweed)
	}
}

func BenchmarkBuildIAMStartupScript(b *testing.B) {
	seaweed := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bench-seaweed",
			Namespace: "default",
		},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 3},
			IAM: &seaweedv1.IAMSpec{
				Replicas:    2,
				Port:        int32Ptr(8111),
				MetricsPort: int32Ptr(9090),
			},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildIAMStartupScript(seaweed)
	}
}

// Test error conditions
func TestIAMErrorConditions(t *testing.T) {
	tests := []struct {
		name        string
		seaweed     *seaweedv1.Seaweed
		description string
	}{
		{
			name: "Zero replicas",
			seaweed: &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "zero-replicas",
					Namespace: "default",
				},
				Spec: seaweedv1.SeaweedSpec{
					Master: &seaweedv1.MasterSpec{
						Replicas: 1,
					},
					IAM: &seaweedv1.IAMSpec{
						Replicas: 0, // Should handle gracefully
					},
				},
			},
			description: "Should handle zero replicas gracefully",
		},
		{
			name: "Invalid port range",
			seaweed: &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "invalid-port",
					Namespace: "default",
				},
				Spec: seaweedv1.SeaweedSpec{
					Master: &seaweedv1.MasterSpec{
						Replicas: 1,
					},
					IAM: &seaweedv1.IAMSpec{
						Replicas: 1,
						Port:     int32Ptr(99999), // Invalid port
					},
				},
			},
			description: "Should handle invalid port numbers",
		},
		{
			name: "Very large resources",
			seaweed: &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "large-resources",
					Namespace: "default",
				},
				Spec: seaweedv1.SeaweedSpec{
					Master: &seaweedv1.MasterSpec{
						Replicas: 1,
					},
					IAM: &seaweedv1.IAMSpec{
						Replicas: 100, // Large number of replicas
						ResourceRequirements: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100"),
								corev1.ResourceMemory: resource.MustParse("1000Gi"),
							},
						},
					},
				},
			},
			description: "Should handle large resource requests",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &SeaweedReconciler{}

			// These should not panic or crash
			t.Log(tt.description)

			// Test StatefulSet creation
			sts := reconciler.createIAMStatefulSet(tt.seaweed)
			if sts == nil {
				t.Error("Expected StatefulSet to be created even with edge case inputs")
			}

			// Test Service creation
			svc := reconciler.createIAMService(tt.seaweed)
			if svc == nil {
				t.Error("Expected Service to be created even with edge case inputs")
			}

			// Test startup script
			script := buildIAMStartupScript(tt.seaweed)
			if script == "" {
				t.Error("Expected startup script to be generated even with edge case inputs")
			}
		})
	}
}
