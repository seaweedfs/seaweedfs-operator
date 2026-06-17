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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/test/utils"
)

// Verifies the DaemonSet + hostPath volume-server mode end-to-end: the operator
// renders a DaemonSet (not a StatefulSet) backed by node-local hostPath disks,
// skips the per-replica Services, reflects DaemonSet readiness into status, and
// replaces the workload when spec.volume.kind is switched.
//
// This is a render-time spec — the volume pods are never required to become
// ready (they would need real disks and a reachable master), so it stays in the
// light (non-"integration") e2e group that runs on every PR.
var _ = Describe("Volume server DaemonSet with hostPath", Ordered, func() {
	var (
		ctx           context.Context
		k8sClient     client.Client
		restCfg       *rest.Config
		testNamespace = "test-volume-daemonset"
		seaweedName   = "test-seaweed-ds"
	)

	seaweedKey := func() types.NamespacedName {
		return types.NamespacedName{Name: seaweedName, Namespace: testNamespace}
	}
	volumeKey := func() types.NamespacedName {
		return types.NamespacedName{Name: seaweedName + "-volume", Namespace: testNamespace}
	}

	BeforeAll(func() {
		ctx = context.Background()
		k8sClient, restCfg = utils.NewE2EClient()
		utils.EnsureNamespace(ctx, k8sClient, testNamespace)

		concurrentStart := true
		seaweed := &seaweedv1.Seaweed{
			ObjectMeta: metav1.ObjectMeta{Name: seaweedName, Namespace: testNamespace},
			Spec: seaweedv1.SeaweedSpec{
				Image: "chrislusf/seaweedfs:latest",
				Master: &seaweedv1.MasterSpec{
					Replicas:        1,
					ConcurrentStart: &concurrentStart,
				},
				Volume: &seaweedv1.VolumeSpec{
					Kind: seaweedv1.VolumeServerDaemonSet,
					HostPath: []seaweedv1.VolumeServerHostPath{
						{Path: "/mnt/seaweedfs-e2e/disk0"},
						{Path: "/mnt/seaweedfs-e2e/disk1"},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, seaweed)).To(Succeed())
	})

	BeforeEach(func() {
		DeferCleanup(func() {
			utils.CollectDiagnostics(ctx, k8sClient, restCfg, testNamespace)
		})
	})

	AfterAll(func() {
		utils.DeleteNamespace(ctx, k8sClient, testNamespace)
	})

	It("renders a DaemonSet backed by node-local hostPath disks", func() {
		ds := &appsv1.DaemonSet{}
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, volumeKey(), ds)).To(Succeed())

			// Owned by the Seaweed CR so it is garbage-collected on delete.
			g.Expect(ds.OwnerReferences).NotTo(BeEmpty())
			g.Expect(ds.OwnerReferences[0].Kind).To(Equal("Seaweed"))

			spec := ds.Spec.Template.Spec

			// One hostPath volume per entry, and no PVC sources.
			var hostPaths []string
			for _, v := range spec.Volumes {
				if v.HostPath != nil {
					hostPaths = append(hostPaths, v.HostPath.Path)
				}
				g.Expect(v.PersistentVolumeClaim).To(BeNil(),
					"DaemonSet pods must not reference PVC volume sources")
			}
			g.Expect(hostPaths).To(ConsistOf("/mnt/seaweedfs-e2e/disk0", "/mnt/seaweedfs-e2e/disk1"))

			// The volume server advertises the pod IP (no stable ordinal DNS)
			// and uses both hostPath directories.
			var cmd string
			for _, c := range spec.Containers {
				if c.Name == "volume" {
					g.Expect(c.Command).To(HaveLen(3))
					cmd = c.Command[2]
				}
			}
			g.Expect(cmd).To(ContainSubstring("-ip=$(POD_IP)"))
			g.Expect(cmd).To(ContainSubstring("-dir=/data0,/data1"))
		}, time.Minute*2, time.Second*5).Should(Succeed())

		By("not creating a StatefulSet in DaemonSet mode")
		Consistently(func() bool {
			sts := &appsv1.StatefulSet{}
			return apierrors.IsNotFound(k8sClient.Get(ctx, volumeKey(), sts))
		}, time.Second*10, time.Second*2).Should(BeTrue())

		By("provisioning the headless peer Service but no per-replica Services")
		peer := &corev1.Service{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: seaweedName + "-volume-peer", Namespace: testNamespace}, peer)).To(Succeed())
		perReplica := &corev1.Service{}
		Expect(apierrors.IsNotFound(
			k8sClient.Get(ctx, types.NamespacedName{Name: seaweedName + "-volume-0", Namespace: testNamespace}, perReplica),
		)).To(BeTrue(), "DaemonSet mode must not create per-replica volume Services")
	})

	It("reports volume status from the DaemonSet", func() {
		// The operator mirrors the DaemonSet's desired count into status —
		// compare against the live DaemonSet rather than assuming a node count,
		// so the assertion holds regardless of cluster taints/eligibility.
		Eventually(func(g Gomega) {
			sw := &seaweedv1.Seaweed{}
			ds := &appsv1.DaemonSet{}
			g.Expect(k8sClient.Get(ctx, seaweedKey(), sw)).To(Succeed())
			g.Expect(k8sClient.Get(ctx, volumeKey(), ds)).To(Succeed())
			g.Expect(sw.Status.Volume.Replicas).To(Equal(ds.Status.DesiredNumberScheduled))
		}, time.Minute*2, time.Second*5).Should(Succeed())
	})

	It("replaces the DaemonSet with a StatefulSet when kind switches", func() {
		Eventually(func(g Gomega) {
			sw := &seaweedv1.Seaweed{}
			g.Expect(k8sClient.Get(ctx, seaweedKey(), sw)).To(Succeed())
			sw.Spec.Volume.Kind = seaweedv1.VolumeServerStatefulSet
			sw.Spec.Volume.HostPath = nil
			sw.Spec.Volume.Replicas = 1
			sw.Spec.Volume.Requests = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}
			g.Expect(k8sClient.Update(ctx, sw)).To(Succeed())
		}, time.Minute, time.Second*5).Should(Succeed())

		By("deleting the old DaemonSet")
		Eventually(func() bool {
			ds := &appsv1.DaemonSet{}
			return apierrors.IsNotFound(k8sClient.Get(ctx, volumeKey(), ds))
		}, time.Minute*2, time.Second*5).Should(BeTrue())

		By("creating the StatefulSet")
		Eventually(func() error {
			sts := &appsv1.StatefulSet{}
			return k8sClient.Get(ctx, volumeKey(), sts)
		}, time.Minute*2, time.Second*5).Should(Succeed())
	})
})
