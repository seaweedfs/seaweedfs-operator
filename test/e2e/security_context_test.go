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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/test/utils"
)

// Covers issue #251: ComponentSpec.PodSecurityContext and
// ComponentSpec.ContainerSecurityContext must flow through to the rendered pod
// template / operator-managed container for every component, so users can run
// SeaweedFS as non-root, drop capabilities, and satisfy PodSecurityStandards.
//
// Unlike Sidecars/InitContainers (opaque schema), these fields are inlined in
// the CRD, so this suite doubles as a round-trip check that the generated
// OpenAPI schema accepts a realistic securityContext without pruning fields.
var _ = Describe("SecurityContext Integration", Ordered, func() {
	var (
		ctx           context.Context
		k8sClient     client.Client
		restCfg       *rest.Config
		testNamespace = "test-security-context"
		seaweedName   = "test-seaweed-sc"
	)

	BeforeAll(func() {
		ctx = context.Background()
		k8sClient, restCfg = utils.NewE2EClient()
		utils.EnsureNamespace(ctx, k8sClient, testNamespace)
	})

	BeforeEach(func() {
		DeferCleanup(func() {
			utils.CollectDiagnostics(ctx, k8sClient, restCfg, testNamespace)
		})
	})

	AfterAll(func() {
		utils.DeleteNamespace(ctx, k8sClient, testNamespace)
	})

	podSecurityContext := func(uid int64) *corev1.PodSecurityContext {
		runAsNonRoot := true
		fsGroup := uid
		return &corev1.PodSecurityContext{
			RunAsUser:    &uid,
			RunAsNonRoot: &runAsNonRoot,
			FSGroup:      &fsGroup,
		}
	}

	containerSecurityContext := func() *corev1.SecurityContext {
		allowPrivilegeEscalation := false
		readOnlyRootFilesystem := true
		return &corev1.SecurityContext{
			AllowPrivilegeEscalation: &allowPrivilegeEscalation,
			ReadOnlyRootFilesystem:   &readOnlyRootFilesystem,
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		}
	}

	// assertSTSSecurityContext polls the named StatefulSet until its pod
	// template carries the expected pod-level securityContext and its
	// named container carries the expected container-level securityContext.
	assertSTSSecurityContext := func(stsName, containerName string, uid int64) {
		clientset, err := utils.GetClientset(restCfg)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func(g Gomega) {
			sts, err := clientset.AppsV1().StatefulSets(testNamespace).Get(ctx, stsName, metav1.GetOptions{})
			g.Expect(err).NotTo(HaveOccurred())
			podSC := sts.Spec.Template.Spec.SecurityContext
			g.Expect(podSC).NotTo(BeNil(), "StatefulSet %s pod securityContext", stsName)
			g.Expect(podSC.RunAsUser).NotTo(BeNil())
			g.Expect(*podSC.RunAsUser).To(Equal(uid))
			g.Expect(podSC.FSGroup).NotTo(BeNil())
			var c *corev1.Container
			for i := range sts.Spec.Template.Spec.Containers {
				if sts.Spec.Template.Spec.Containers[i].Name == containerName {
					c = &sts.Spec.Template.Spec.Containers[i]
					break
				}
			}
			g.Expect(c).NotTo(BeNil(), "StatefulSet %s container %s", stsName, containerName)
			g.Expect(c.SecurityContext).NotTo(BeNil())
			g.Expect(c.SecurityContext.ReadOnlyRootFilesystem).NotTo(BeNil())
			g.Expect(*c.SecurityContext.ReadOnlyRootFilesystem).To(BeTrue())
			g.Expect(c.SecurityContext.Capabilities).NotTo(BeNil())
			g.Expect(c.SecurityContext.Capabilities.Drop).To(ContainElement(corev1.Capability("ALL")))
		}, time.Minute*2, time.Second*5).Should(Succeed())
	}

	assertDeploymentSecurityContext := func(depName, containerName string, uid int64) {
		clientset, err := utils.GetClientset(restCfg)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func(g Gomega) {
			dep, err := clientset.AppsV1().Deployments(testNamespace).Get(ctx, depName, metav1.GetOptions{})
			g.Expect(err).NotTo(HaveOccurred())
			podSC := dep.Spec.Template.Spec.SecurityContext
			g.Expect(podSC).NotTo(BeNil(), "Deployment %s pod securityContext", depName)
			g.Expect(podSC.RunAsUser).NotTo(BeNil())
			g.Expect(*podSC.RunAsUser).To(Equal(uid))
			var c *corev1.Container
			for i := range dep.Spec.Template.Spec.Containers {
				if dep.Spec.Template.Spec.Containers[i].Name == containerName {
					c = &dep.Spec.Template.Spec.Containers[i]
					break
				}
			}
			g.Expect(c).NotTo(BeNil(), "Deployment %s container %s", depName, containerName)
			g.Expect(c.SecurityContext).NotTo(BeNil())
			g.Expect(c.SecurityContext.ReadOnlyRootFilesystem).NotTo(BeNil())
			g.Expect(*c.SecurityContext.ReadOnlyRootFilesystem).To(BeTrue())
		}, time.Minute*2, time.Second*5).Should(Succeed())
	}

	Context("When securityContext is set on flat components", func() {
		var seaweed *seaweedv1.Seaweed

		AfterEach(func() {
			if seaweed != nil {
				_ = k8sClient.Delete(ctx, seaweed)
				Eventually(func() error {
					return k8sClient.Get(ctx, types.NamespacedName{
						Name:      seaweedName,
						Namespace: testNamespace,
					}, seaweed)
				}, time.Minute*2, time.Second*5).ShouldNot(Succeed())
				seaweed = nil
			}
		})

		It("renders securityContext on master, volume, and filer StatefulSets", func() {
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
						ConcurrentStart: &concurrentStart,
						ComponentSpec: seaweedv1.ComponentSpec{
							PodSecurityContext:       podSecurityContext(1001),
							ContainerSecurityContext: containerSecurityContext(),
						},
					},
					Volume: &seaweedv1.VolumeSpec{
						Replicas: 1,
						VolumeServerConfig: seaweedv1.VolumeServerConfig{
							ComponentSpec: seaweedv1.ComponentSpec{
								PodSecurityContext:       podSecurityContext(1002),
								ContainerSecurityContext: containerSecurityContext(),
							},
							ResourceRequirements: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceStorage: resource.MustParse("1Gi"),
								},
							},
						},
					},
					Filer: &seaweedv1.FilerSpec{
						Replicas: 1,
						ComponentSpec: seaweedv1.ComponentSpec{
							PodSecurityContext:       podSecurityContext(1003),
							ContainerSecurityContext: containerSecurityContext(),
						},
					},
					VolumeServerDiskCount: func() *int32 { v := int32(1); return &v }(),
				},
			}
			Expect(k8sClient.Create(ctx, seaweed)).To(Succeed())

			assertSTSSecurityContext(seaweedName+"-master", "master", 1001)
			assertSTSSecurityContext(seaweedName+"-volume", "volume", 1002)
			assertSTSSecurityContext(seaweedName+"-filer", "filer", 1003)
		})

		// VolumeTopology renders pods through buildTopologyPodSpec, not
		// ComponentAccessor.BuildPodSpec — securityContext needs to flow
		// through both code paths.
		It("renders securityContext on topology-aware volume StatefulSets", func() {
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
						ConcurrentStart: &concurrentStart,
					},
					Filer: &seaweedv1.FilerSpec{Replicas: 1},
					VolumeTopology: map[string]*seaweedv1.VolumeTopologySpec{
						"rack1": {
							VolumeServerConfig: seaweedv1.VolumeServerConfig{
								ComponentSpec: seaweedv1.ComponentSpec{
									PodSecurityContext:       podSecurityContext(1004),
									ContainerSecurityContext: containerSecurityContext(),
								},
								ResourceRequirements: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceStorage: resource.MustParse("1Gi"),
									},
								},
							},
							Replicas:   1,
							Rack:       "rack1",
							DataCenter: "dc1",
						},
					},
					VolumeServerDiskCount: func() *int32 { v := int32(1); return &v }(),
				},
			}
			Expect(k8sClient.Create(ctx, seaweed)).To(Succeed())

			assertSTSSecurityContext(seaweedName+"-volume-rack1", "volume", 1004)
		})

		// The standalone S3 gateway renders a Deployment via controller_s3.go,
		// a separate pod-spec builder from the StatefulSet path.
		It("renders securityContext on the standalone S3 Deployment", func() {
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
						ConcurrentStart: &concurrentStart,
					},
					Volume: &seaweedv1.VolumeSpec{
						Replicas: 1,
						VolumeServerConfig: seaweedv1.VolumeServerConfig{
							ResourceRequirements: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceStorage: resource.MustParse("1Gi"),
								},
							},
						},
					},
					Filer: &seaweedv1.FilerSpec{Replicas: 1},
					S3: &seaweedv1.S3GatewaySpec{
						Replicas: 1,
						ComponentSpec: seaweedv1.ComponentSpec{
							PodSecurityContext:       podSecurityContext(1005),
							ContainerSecurityContext: containerSecurityContext(),
						},
					},
					VolumeServerDiskCount: func() *int32 { v := int32(1); return &v }(),
				},
			}
			Expect(k8sClient.Create(ctx, seaweed)).To(Succeed())

			assertDeploymentSecurityContext(seaweedName+"-s3", "s3", 1005)
		})
	})
})
