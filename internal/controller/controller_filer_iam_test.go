package controller

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

const (
	filerIAMPortName = "filer-iam"
)

func TestBuildFilerStartupScriptWithIAM(t *testing.T) {
	tests := []struct {
		name        string
		seaweed     *seaweedv1.Seaweed
		expectIAM   bool
		expectedCmd []string
	}{
		{
			name: "Filer without IAM",
			seaweed: &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-seaweed",
					Namespace: "default",
				},
				Spec: seaweedv1.SeaweedSpec{
					Master: &seaweedv1.MasterSpec{Replicas: 1},
					Filer: &seaweedv1.FilerSpec{
						Replicas: 1,
						S3:       &seaweedv1.S3Config{Enabled: true},
					},
				},
			},
			expectIAM: false,
			expectedCmd: []string{
				"weed",
				"-logtostderr=true",
				"filer",
				"-port=8888",
				"-s3",
			},
		},
		{
			name: "Filer with embedded IAM",
			seaweed: &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-seaweed",
					Namespace: "default",
				},
				Spec: seaweedv1.SeaweedSpec{
					Master: &seaweedv1.MasterSpec{Replicas: 1},
					Filer: &seaweedv1.FilerSpec{
						Replicas: 1,
						S3:       &seaweedv1.S3Config{Enabled: true},
						IAM:      true,
					},
				},
			},
			expectIAM: true,
			expectedCmd: []string{
				"weed",
				"-logtostderr=true",
				"filer",
				"-port=8888",
				"-s3",
				"-iam",
				"-iam.port=8111",
			},
		},
		{
			name: "Filer with embedded IAM and custom port",
			seaweed: &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-seaweed",
					Namespace: "default",
				},
				Spec: seaweedv1.SeaweedSpec{
					Master: &seaweedv1.MasterSpec{Replicas: 1},
					Filer: &seaweedv1.FilerSpec{
						Replicas: 1,
						S3:       &seaweedv1.S3Config{Enabled: true},
						IAM:      true,
					},
					IAM: &seaweedv1.IAMSpec{
						Port: int32Ptr(9111),
					},
				},
			},
			expectIAM: true,
			expectedCmd: []string{
				"weed",
				"-logtostderr=true",
				"filer",
				"-port=8888",
				"-s3",
				"-iam",
				"-iam.port=9111",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildFilerStartupScript(tt.seaweed)

			// Check for IAM flag
			hasIAMFlag := strings.Contains(result, "-iam")
			if hasIAMFlag != tt.expectIAM {
				t.Errorf("Expected IAM flag presence: %v, got: %v in command: %s", tt.expectIAM, hasIAMFlag, result)
			}

			// Check all expected command parts
			for _, expected := range tt.expectedCmd {
				if !strings.Contains(result, expected) {
					t.Errorf("Expected '%s' to be in startup script, got: %s", expected, result)
				}
			}
		})
	}
}

func TestCreateFilerStatefulSetWithIAM(t *testing.T) {
	tests := []struct {
		name     string
		seaweed  *seaweedv1.Seaweed
		validate func(*testing.T, *appsv1.StatefulSet)
	}{
		{
			name: "Filer StatefulSet with embedded IAM",
			seaweed: &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-seaweed",
					Namespace: "default",
				},
				Spec: seaweedv1.SeaweedSpec{
					Image:  "chrislusf/seaweedfs:latest",
					Master: &seaweedv1.MasterSpec{Replicas: 1},
					Filer: &seaweedv1.FilerSpec{
						Replicas: 1,
						S3:       &seaweedv1.S3Config{Enabled: true},
						IAM:      true,
					},
				},
			},
			validate: func(t *testing.T, sts *appsv1.StatefulSet) {
				container := sts.Spec.Template.Spec.Containers[0]

				// Check that IAM port is exposed
				iamPortFound := false
				s3PortFound := false

				for _, port := range container.Ports {
					if port.Name == filerIAMPortName && port.ContainerPort == seaweedv1.FilerIAMPort {
						iamPortFound = true
					}
					if port.Name == "filer-s3" && port.ContainerPort == seaweedv1.FilerS3Port {
						s3PortFound = true
					}
				}

				if !iamPortFound {
					t.Errorf("Expected IAM port %d not found in container ports", seaweedv1.FilerIAMPort)
				}
				if !s3PortFound {
					t.Errorf("Expected S3 port %d not found in container ports", seaweedv1.FilerS3Port)
				}

				// Check startup command contains IAM flags
				cmdStr := strings.Join(container.Command, " ")
				if !strings.Contains(cmdStr, "-iam") {
					t.Errorf("Expected filer startup command to contain '-iam' flag")
				}
			},
		},
		{
			name: "Filer StatefulSet with embedded IAM and custom port",
			seaweed: &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-seaweed",
					Namespace: "default",
				},
				Spec: seaweedv1.SeaweedSpec{
					Image:  "chrislusf/seaweedfs:latest",
					Master: &seaweedv1.MasterSpec{Replicas: 1},
					Filer: &seaweedv1.FilerSpec{
						Replicas: 1,
						IAM:      true,
					},
					IAM: &seaweedv1.IAMSpec{
						Port: int32Ptr(9111),
					},
				},
			},
			validate: func(t *testing.T, sts *appsv1.StatefulSet) {
				container := sts.Spec.Template.Spec.Containers[0]

				// Check that custom IAM port is exposed
				iamPortFound := false
				for _, port := range container.Ports {
					if port.Name == "filer-iam" && port.ContainerPort == 9111 {
						iamPortFound = true
						break
					}
				}

				if !iamPortFound {
					t.Errorf("Expected custom IAM port 9111 not found in container ports")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &SeaweedReconciler{}
			sts := reconciler.createFilerStatefulSet(tt.seaweed)
			tt.validate(t, sts)
		})
	}
}

func TestCreateFilerServiceWithIAM(t *testing.T) {
	tests := []struct {
		name     string
		seaweed  *seaweedv1.Seaweed
		validate func(*testing.T, *corev1.Service)
	}{
		{
			name: "Filer Service with embedded IAM",
			seaweed: &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-seaweed",
					Namespace: "default",
				},
				Spec: seaweedv1.SeaweedSpec{
					Master: &seaweedv1.MasterSpec{Replicas: 1},
					Filer: &seaweedv1.FilerSpec{
						Replicas: 1,
						S3:       &seaweedv1.S3Config{Enabled: true},
						IAM:      true,
					},
				},
			},
			validate: func(t *testing.T, svc *corev1.Service) {
				// Check that IAM port is exposed in service
				iamPortFound := false
				s3PortFound := false

				for _, port := range svc.Spec.Ports {
					if port.Name == "filer-iam" && port.Port == seaweedv1.FilerIAMPort {
						iamPortFound = true
						if port.TargetPort != intstr.FromInt(int(seaweedv1.FilerIAMPort)) {
							t.Errorf("Expected IAM target port %d, got %v", seaweedv1.FilerIAMPort, port.TargetPort)
						}
					}
					if port.Name == "filer-s3" && port.Port == seaweedv1.FilerS3Port {
						s3PortFound = true
					}
				}

				if !iamPortFound {
					t.Errorf("Expected IAM port %d not found in service ports", seaweedv1.FilerIAMPort)
				}
				if !s3PortFound {
					t.Errorf("Expected S3 port %d not found in service ports", seaweedv1.FilerS3Port)
				}
			},
		},
		{
			name: "Filer Service with embedded IAM and custom port",
			seaweed: &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-seaweed",
					Namespace: "default",
				},
				Spec: seaweedv1.SeaweedSpec{
					Master: &seaweedv1.MasterSpec{Replicas: 1},
					Filer: &seaweedv1.FilerSpec{
						Replicas: 1,
						IAM:      true,
					},
					IAM: &seaweedv1.IAMSpec{
						Port: int32Ptr(9111),
					},
				},
			},
			validate: func(t *testing.T, svc *corev1.Service) {
				// Check that custom IAM port is exposed
				iamPortFound := false
				for _, port := range svc.Spec.Ports {
					if port.Name == "filer-iam" && port.Port == 9111 {
						iamPortFound = true
						if port.TargetPort != intstr.FromInt(9111) {
							t.Errorf("Expected custom IAM target port 9111, got %v", port.TargetPort)
						}
						break
					}
				}

				if !iamPortFound {
					t.Errorf("Expected custom IAM port 9111 not found in service ports")
				}
			},
		},
		{
			name: "Filer Service without IAM",
			seaweed: &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-seaweed",
					Namespace: "default",
				},
				Spec: seaweedv1.SeaweedSpec{
					Master: &seaweedv1.MasterSpec{Replicas: 1},
					Filer: &seaweedv1.FilerSpec{
						Replicas: 1,
						S3:       &seaweedv1.S3Config{Enabled: true},
						IAM:      false,
					},
				},
			},
			validate: func(t *testing.T, svc *corev1.Service) {
				// Check that IAM port is NOT exposed
				for _, port := range svc.Spec.Ports {
					if port.Name == "filer-iam" {
						t.Errorf("Unexpected IAM port found in service when IAM is disabled")
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &SeaweedReconciler{}
			svc := reconciler.createFilerService(tt.seaweed)
			tt.validate(t, svc)
		})
	}
}

func TestCreateFilerPeerServiceWithIAM(t *testing.T) {
	tests := []struct {
		name     string
		seaweed  *seaweedv1.Seaweed
		validate func(*testing.T, *corev1.Service)
	}{
		{
			name: "Filer Peer Service with embedded IAM",
			seaweed: &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-seaweed",
					Namespace: "default",
				},
				Spec: seaweedv1.SeaweedSpec{
					Master: &seaweedv1.MasterSpec{Replicas: 1},
					Filer: &seaweedv1.FilerSpec{
						Replicas: 1,
						S3:       &seaweedv1.S3Config{Enabled: true},
						IAM:      true,
					},
				},
			},
			validate: func(t *testing.T, svc *corev1.Service) {
				// Check service type (should be headless)
				if svc.Spec.ClusterIP != "None" {
					t.Errorf("Expected headless service (ClusterIP: None), got %s", svc.Spec.ClusterIP)
				}

				// Check that IAM port is exposed in peer service
				iamPortFound := false
				for _, port := range svc.Spec.Ports {
					if port.Name == "filer-iam" && port.Port == seaweedv1.FilerIAMPort {
						iamPortFound = true
						break
					}
				}

				if !iamPortFound {
					t.Errorf("Expected IAM port %d not found in peer service ports", seaweedv1.FilerIAMPort)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &SeaweedReconciler{}
			svc := reconciler.createFilerPeerService(tt.seaweed)
			tt.validate(t, svc)
		})
	}
}

// Test IAM configuration integration
func TestIAMConfiguration(t *testing.T) {
	tests := []struct {
		name        string
		seaweed     *seaweedv1.Seaweed
		description string
		validate    func(*testing.T, *seaweedv1.Seaweed)
	}{
		{
			name: "Standalone IAM only",
			seaweed: &seaweedv1.Seaweed{
				Spec: seaweedv1.SeaweedSpec{
					IAM: &seaweedv1.IAMSpec{
						Replicas: 2,
						Port:     int32Ptr(8111),
					},
					Filer: &seaweedv1.FilerSpec{
						Replicas: 1,
						IAM:      false,
					},
				},
			},
			description: "Should support standalone IAM without embedded IAM",
			validate: func(t *testing.T, seaweed *seaweedv1.Seaweed) {
				if seaweed.Spec.IAM == nil {
					t.Error("Expected standalone IAM to be configured")
				}
				if seaweed.Spec.Filer.IAM {
					t.Error("Expected embedded IAM to be disabled")
				}
			},
		},
		{
			name: "Embedded IAM only",
			seaweed: &seaweedv1.Seaweed{
				Spec: seaweedv1.SeaweedSpec{
					Filer: &seaweedv1.FilerSpec{
						Replicas: 1,
						IAM:      true,
					},
				},
			},
			description: "Should support embedded IAM without standalone IAM",
			validate: func(t *testing.T, seaweed *seaweedv1.Seaweed) {
				if !seaweed.Spec.Filer.IAM {
					t.Error("Expected embedded IAM to be enabled")
				}
				// No standalone IAM should be fine
			},
		},
		{
			name: "Both standalone and embedded IAM",
			seaweed: &seaweedv1.Seaweed{
				Spec: seaweedv1.SeaweedSpec{
					IAM: &seaweedv1.IAMSpec{
						Replicas: 1,
						Port:     int32Ptr(8111),
					},
					Filer: &seaweedv1.FilerSpec{
						Replicas: 1,
						IAM:      true,
					},
				},
			},
			description: "Should support both configurations (though not recommended)",
			validate: func(t *testing.T, seaweed *seaweedv1.Seaweed) {
				if seaweed.Spec.IAM == nil {
					t.Error("Expected standalone IAM to be configured")
				}
				if !seaweed.Spec.Filer.IAM {
					t.Error("Expected embedded IAM to be enabled")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log(tt.description)
			tt.validate(t, tt.seaweed)
		})
	}
}
