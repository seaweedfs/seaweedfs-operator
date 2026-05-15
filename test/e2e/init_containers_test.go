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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/test/utils"
)

// Covers issue #249: ComponentSpec.InitContainers must flow through to the
// rendered pod template for every component, so users can gate the
// SeaweedFS process on external dependencies (wait for Cassandra, fix PVC
// ownership, run a schema migration). A sidecar can't substitute — sidecars
// start in parallel with the main container, so the SeaweedFS process has
// already exited before the wait loop completes.
//
// This is a render-time assertion (the init container images are dummy and
// never run); the operator's job is to put the user's spec into the pod
// template, and that's what we check.
var _ = Describe("InitContainers Integration", Ordered, func() {
	var (
		ctx           context.Context
		k8sClient     client.Client
		restCfg       *rest.Config
		testNamespace = "test-init-containers"
		seaweedName   = "test-seaweed-init"
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

	// initContainer builds a small init container that exits 0. The image
	// is intentionally cheap and well-known so the test doesn't depend on
	// custom images being available.
	initContainer := func(name, message string) corev1.Container {
		return corev1.Container{
			Name:    name,
			Image:   "busybox:1.36",
			Command: []string{"sh", "-c", "echo " + message},
		}
	}

	// assertInitContainers polls the named StatefulSet until its pod template
	// matches the expected init container list (by name + image).
	assertSTSInitContainers := func(stsName string, expected []corev1.Container) {
		clientset, err := utils.GetClientset(restCfg)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func(g Gomega) {
			sts, err := clientset.AppsV1().StatefulSets(testNamespace).Get(ctx, stsName, metav1.GetOptions{})
			g.Expect(err).NotTo(HaveOccurred())
			got := sts.Spec.Template.Spec.InitContainers
			g.Expect(got).To(HaveLen(len(expected)),
				"StatefulSet %s should have %d initContainers, got %d", stsName, len(expected), len(got))
			for i, want := range expected {
				g.Expect(got[i].Name).To(Equal(want.Name),
					"StatefulSet %s initContainers[%d].Name", stsName, i)
				g.Expect(got[i].Image).To(Equal(want.Image),
					"StatefulSet %s initContainers[%d].Image", stsName, i)
				g.Expect(got[i].Command).To(Equal(want.Command),
					"StatefulSet %s initContainers[%d].Command", stsName, i)
			}
		}, time.Minute*2, time.Second*5).Should(Succeed())
	}

	assertDeploymentInitContainers := func(depName string, expected []corev1.Container) {
		clientset, err := utils.GetClientset(restCfg)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func(g Gomega) {
			dep, err := clientset.AppsV1().Deployments(testNamespace).Get(ctx, depName, metav1.GetOptions{})
			g.Expect(err).NotTo(HaveOccurred())
			got := dep.Spec.Template.Spec.InitContainers
			g.Expect(got).To(HaveLen(len(expected)),
				"Deployment %s should have %d initContainers, got %d", depName, len(expected), len(got))
			for i, want := range expected {
				g.Expect(got[i].Name).To(Equal(want.Name),
					"Deployment %s initContainers[%d].Name", depName, i)
				g.Expect(got[i].Image).To(Equal(want.Image),
					"Deployment %s initContainers[%d].Image", depName, i)
			}
		}, time.Minute*2, time.Second*5).Should(Succeed())
	}

	Context("When ComponentSpec.InitContainers is set on flat components", func() {
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

		It("renders init containers on master, volume, and filer StatefulSets", func() {
			concurrentStart := true
			masterInit := initContainer("wait-for-deps", "master-ready")
			volumeInit := initContainer("fix-perms", "volume-ready")
			// Two init containers on filer to verify ordering is preserved —
			// this is the canonical use case in #249 (wait for TCP, then
			// wait for a schema/keyspace before the filer starts).
			filerInit1 := initContainer("wait-cassandra", "tcp-ready")
			filerInit2 := initContainer("wait-keyspace", "schema-ready")

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
							InitContainers: []corev1.Container{masterInit},
						},
					},
					Volume: &seaweedv1.VolumeSpec{
						Replicas: 1,
						VolumeServerConfig: seaweedv1.VolumeServerConfig{
							ComponentSpec: seaweedv1.ComponentSpec{
								InitContainers: []corev1.Container{volumeInit},
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
							InitContainers: []corev1.Container{filerInit1, filerInit2},
						},
					},
					VolumeServerDiskCount: func() *int32 { v := int32(1); return &v }(),
				},
			}
			Expect(k8sClient.Create(ctx, seaweed)).To(Succeed())

			assertSTSInitContainers(seaweedName+"-master", []corev1.Container{masterInit})
			assertSTSInitContainers(seaweedName+"-volume", []corev1.Container{volumeInit})
			assertSTSInitContainers(seaweedName+"-filer", []corev1.Container{filerInit1, filerInit2})
		})

		// VolumeTopology renders pods through buildTopologyPodSpec, not
		// ComponentAccessor.BuildPodSpec — InitContainers needs to flow
		// through both code paths. Setting VolumeTopology causes the
		// operator to skip the flat -volume StatefulSet, so this spec
		// verifies only the topology-suffixed StatefulSet.
		It("renders init containers on topology-aware volume StatefulSets", func() {
			concurrentStart := true
			topologyInit := initContainer("fix-perms", "topology-ready")
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
									InitContainers: []corev1.Container{topologyInit},
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

			assertSTSInitContainers(seaweedName+"-volume-rack1", []corev1.Container{topologyInit})
		})

		// The standalone S3 gateway renders a Deployment via controller_s3.go,
		// which is a separate pod-spec builder from the StatefulSet path —
		// verify the InitContainers wiring on that branch too.
		It("renders init containers on the standalone S3 Deployment", func() {
			concurrentStart := true
			s3Init := initContainer("wait-filer", "s3-ready")
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
							InitContainers: []corev1.Container{s3Init},
						},
					},
					VolumeServerDiskCount: func() *int32 { v := int32(1); return &v }(),
				},
			}
			Expect(k8sClient.Create(ctx, seaweed)).To(Succeed())

			// Confirm the Deployment exists and its pod template carries
			// the init container.
			dep := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      seaweedName + "-s3",
					Namespace: testNamespace,
				}, dep)
			}, 60*time.Second, time.Second).Should(Succeed())
			assertDeploymentInitContainers(seaweedName+"-s3", []corev1.Container{s3Init})
		})
	})
})
