/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/test/utils"
)

var _ = Describe("Resource Requirements Integration", Ordered, func() {
	var (
		ctx           context.Context
		k8sClient     client.Client
		testNamespace = "test-resources"
		seaweedName   = "test-seaweed-resources"
	)

	BeforeAll(func() {
		ctx = context.Background()

		// Set up scheme with Seaweed CRDs
		scheme := runtime.NewScheme()
		utilruntime.Must(clientgoscheme.AddToScheme(scheme))
		utilruntime.Must(seaweedv1.AddToScheme(scheme))

		// Get Kubernetes client with proper scheme
		cfg := config.GetConfigOrDie()
		var err error
		k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
		Expect(err).NotTo(HaveOccurred())

		// Create test namespace
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: testNamespace,
			},
		}
		err = k8sClient.Create(ctx, ns)
		if err != nil {
			// Namespace might already exist, try to get it
			err = k8sClient.Get(ctx, types.NamespacedName{Name: testNamespace}, ns)
			if err != nil {
				Expect(err).NotTo(HaveOccurred(), "Failed to create or get test namespace")
			}
		}

		// Verify CRDs are installed by checking if we can list Seaweed resources
		seaweedList := &seaweedv1.SeaweedList{}
		err = k8sClient.List(ctx, seaweedList, client.InNamespace(testNamespace))
		if err != nil {
			GinkgoWriter.Printf("WARNING: Could not list Seaweed resources: %v\n", err)
			GinkgoWriter.Printf("This might indicate CRDs are not installed in the cluster\n")
			// Try to get CRD info
			GinkgoWriter.Printf("Checking if Seaweed CRD exists in cluster...\n")
		} else {
			GinkgoWriter.Printf("SUCCESS: Can list Seaweed resources (found %d items)\n", len(seaweedList.Items))
		}

		// Wait briefly for controller to be ready to process requests
		time.Sleep(5 * time.Second)
	})

	AfterAll(func() {
		// Clean up test namespace
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, ns)
	})

	Context("When deploying Seaweed with resource requirements", func() {
		var seaweed *seaweedv1.Seaweed

		BeforeEach(func() {
			concurrentStart := true
			seaweed = &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{
					Name:      seaweedName,
					Namespace: testNamespace,
				},
				Spec: seaweedv1.SeaweedSpec{
					Image: "chrislusf/seaweedfs:latest",
					Master: &seaweedv1.MasterSpec{
						Replicas:        1,
						ConcurrentStart: &concurrentStart, // Allow parallel creation of volume/filer StatefulSets
						ResourceRequirements: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("500m"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1000m"),
								corev1.ResourceMemory: resource.MustParse("2Gi"),
							},
						},
					},
					Volume: &seaweedv1.VolumeSpec{
						Replicas: 1,
						VolumeServerConfig: seaweedv1.VolumeServerConfig{
							ResourceRequirements: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:              resource.MustParse("250m"),
									corev1.ResourceMemory:           resource.MustParse("512Mi"),
									corev1.ResourceStorage:          resource.MustParse("10Gi"), // Should NOT appear in container
									corev1.ResourceEphemeralStorage: resource.MustParse("1Gi"),  // Should appear in container
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:     resource.MustParse("500m"),
									corev1.ResourceMemory:  resource.MustParse("1Gi"),
									corev1.ResourceStorage: resource.MustParse("20Gi"), // Should NOT appear in container
								},
							},
						},
					},
					Filer: &seaweedv1.FilerSpec{
						Replicas: 1,
						ResourceRequirements: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("256Mi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("200m"),
								corev1.ResourceMemory: resource.MustParse("512Mi"),
							},
						},
					},
					Storage: &seaweedv1.StorageSpec{
						VolumeServerDiskCount: 1,
					},
				},
			}
		})

		AfterEach(func() {
			// Clean up the Seaweed resource
			if seaweed != nil {
				_ = k8sClient.Delete(ctx, seaweed)
				// Wait for cleanup
				Eventually(func() error {
					return k8sClient.Get(ctx, types.NamespacedName{
						Name:      seaweedName,
						Namespace: testNamespace,
					}, seaweed)
				}, time.Minute*2, time.Second*5).ShouldNot(Succeed())
			}
		})

		It("should apply resource requirements to master containers correctly", func() {
			// Create the Seaweed resource
			GinkgoWriter.Printf("Creating Seaweed resource in namespace %s\n", testNamespace)
			err := k8sClient.Create(ctx, seaweed)
			if err != nil {
				GinkgoWriter.Printf("FAILED to create Seaweed resource: %v\n", err)
				GinkgoWriter.Printf("Error type: %T\n", err)
				Fail(fmt.Sprintf("Could not create Seaweed resource: %v", err))
			}
			GinkgoWriter.Printf("Seaweed resource created successfully\n")

			// Wait for the master statefulset to be created
			GinkgoWriter.Printf("Waiting for StatefulSet %s-master to be created...\n", seaweedName)
			Eventually(func() error {
				clientset, err := utils.GetClientset(config.GetConfigOrDie())
				if err != nil {
					GinkgoWriter.Printf("Failed to get clientset: %v\n", err)
					return err
				}

				sts, err := clientset.AppsV1().StatefulSets(testNamespace).Get(ctx, seaweedName+"-master", metav1.GetOptions{})
				if err != nil {
					GinkgoWriter.Printf("StatefulSet not found yet: %v\n", err)
					return err
				}

				GinkgoWriter.Printf("StatefulSet found! Checking containers...\n")

				// Verify the container has the correct resource requirements
				container := sts.Spec.Template.Spec.Containers[0]
				Expect(container.Name).To(Equal("master"))

				// Check CPU requests and limits
				actualCPURequest := container.Resources.Requests[corev1.ResourceCPU]
				expectedCPURequest := resource.MustParse("500m")
				GinkgoWriter.Printf("CPU Request - Expected: %s, Actual: %s\n", expectedCPURequest.String(), actualCPURequest.String())
				// Compare CPU values numerically to handle format differences
				Expect(actualCPURequest.MilliValue()).To(Equal(expectedCPURequest.MilliValue()))

				actualCPULimit := container.Resources.Limits[corev1.ResourceCPU]
				expectedCPULimit := resource.MustParse("1000m")
				GinkgoWriter.Printf("CPU Limit - Expected: %s, Actual: %s\n", expectedCPULimit.String(), actualCPULimit.String())
				// Compare CPU values numerically (1000m == 1.0 cores)
				Expect(actualCPULimit.MilliValue()).To(Equal(expectedCPULimit.MilliValue()))

				// Check memory requests and limits
				actualMemRequest := container.Resources.Requests[corev1.ResourceMemory]
				expectedMemRequest := resource.MustParse("1Gi")
				GinkgoWriter.Printf("Memory Request - Expected: %s, Actual: %s\n", expectedMemRequest.String(), actualMemRequest.String())
				// Compare memory values numerically to handle format differences
				Expect(actualMemRequest.Value()).To(Equal(expectedMemRequest.Value()))

				actualMemLimit := container.Resources.Limits[corev1.ResourceMemory]
				expectedMemLimit := resource.MustParse("2Gi")
				GinkgoWriter.Printf("Memory Limit - Expected: %s, Actual: %s\n", expectedMemLimit.String(), actualMemLimit.String())
				// Compare memory values numerically to handle format differences
				Expect(actualMemLimit.Value()).To(Equal(expectedMemLimit.Value()))

				return nil
			}, time.Minute*2, time.Second*10).Should(Succeed())
		})

		It("should apply resource requirements to volume containers correctly and filter storage resources", func() {
			// Create the Seaweed resource
			Expect(k8sClient.Create(ctx, seaweed)).To(Succeed())

			// Wait for the volume statefulset to be created
			Eventually(func() error {
				clientset, err := utils.GetClientset(config.GetConfigOrDie())
				if err != nil {
					return err
				}

				sts, err := clientset.AppsV1().StatefulSets(testNamespace).Get(ctx, seaweedName+"-volume", metav1.GetOptions{})
				if err != nil {
					return err
				}

				// Verify the container has the correct resource requirements
				container := sts.Spec.Template.Spec.Containers[0]
				Expect(container.Name).To(Equal("volume"))

				// Check CPU requests and limits
				Expect(container.Resources.Requests[corev1.ResourceCPU]).To(Equal(resource.MustParse("250m")))
				Expect(container.Resources.Limits[corev1.ResourceCPU]).To(Equal(resource.MustParse("500m")))

				// Check memory requests and limits
				Expect(container.Resources.Requests[corev1.ResourceMemory]).To(Equal(resource.MustParse("512Mi")))
				Expect(container.Resources.Limits[corev1.ResourceMemory]).To(Equal(resource.MustParse("1Gi")))

				// Check ephemeral-storage is included (valid for containers)
				Expect(container.Resources.Requests[corev1.ResourceEphemeralStorage]).To(Equal(resource.MustParse("1Gi")))

				// CRITICAL: Verify that storage resources are NOT included in container spec
				_, hasStorageRequest := container.Resources.Requests[corev1.ResourceStorage]
				Expect(hasStorageRequest).To(BeFalse(), "Storage resources should be filtered out of container specs")

				_, hasStorageLimit := container.Resources.Limits[corev1.ResourceStorage]
				Expect(hasStorageLimit).To(BeFalse(), "Storage resources should be filtered out of container specs")

				return nil
			}, time.Minute*2, time.Second*10).Should(Succeed())
		})

		It("should apply resource requirements to filer containers correctly", func() {
			// Create the Seaweed resource
			Expect(k8sClient.Create(ctx, seaweed)).To(Succeed())

			// Wait for the filer statefulset to be created
			Eventually(func() error {
				clientset, err := utils.GetClientset(config.GetConfigOrDie())
				if err != nil {
					return err
				}

				sts, err := clientset.AppsV1().StatefulSets(testNamespace).Get(ctx, seaweedName+"-filer", metav1.GetOptions{})
				if err != nil {
					return err
				}

				// Verify the container has the correct resource requirements
				container := sts.Spec.Template.Spec.Containers[0]
				Expect(container.Name).To(Equal("filer"))

				// Check CPU requests and limits
				Expect(container.Resources.Requests[corev1.ResourceCPU]).To(Equal(resource.MustParse("100m")))
				Expect(container.Resources.Limits[corev1.ResourceCPU]).To(Equal(resource.MustParse("200m")))

				// Check memory requests and limits
				Expect(container.Resources.Requests[corev1.ResourceMemory]).To(Equal(resource.MustParse("256Mi")))
				Expect(container.Resources.Limits[corev1.ResourceMemory]).To(Equal(resource.MustParse("512Mi")))

				return nil
			}, time.Minute*2, time.Second*10).Should(Succeed())
		})

		It("should use storage resources for PVC sizing in volume statefulset", func() {
			// Create the Seaweed resource
			Expect(k8sClient.Create(ctx, seaweed)).To(Succeed())

			// Wait for the volume statefulset to be created and verify PVC template
			Eventually(func() error {
				clientset, err := utils.GetClientset(config.GetConfigOrDie())
				if err != nil {
					return err
				}

				sts, err := clientset.AppsV1().StatefulSets(testNamespace).Get(ctx, seaweedName+"-volume", metav1.GetOptions{})
				if err != nil {
					return err
				}

				// Check that PVC template includes storage request
				if len(sts.Spec.VolumeClaimTemplates) > 0 {
					pvcTemplate := sts.Spec.VolumeClaimTemplates[0]
					storageRequest := pvcTemplate.Spec.Resources.Requests[corev1.ResourceStorage]
					Expect(storageRequest).To(Equal(resource.MustParse("10Gi")), "Storage request should be used for PVC sizing")
				}

				return nil
			}, time.Minute*2, time.Second*10).Should(Succeed())
		})
	})
})
