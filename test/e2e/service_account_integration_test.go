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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/test/utils"
)

// Covers issue #244: without spec.<component>.serviceAccountName the
// operator-managed StatefulSets render an empty SA and pods fall back to
// the namespace's default SA, which forces OpenShift / PSA-restricted
// clusters to bind elevated SCCs to that default SA. Verifies the field
// flows through to the rendered StatefulSet pod template for each
// component. The referenced ServiceAccounts are intentionally not
// created — this test asserts manifest rendering, not pod scheduling.
var _ = Describe("ServiceAccountName Integration", Ordered, func() {
	var (
		ctx           context.Context
		k8sClient     client.Client
		restCfg       *rest.Config
		testNamespace = "test-service-accounts"
		seaweedName   = "test-seaweed-sa"
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

	Context("When deploying Seaweed with per-component serviceAccountName set", func() {
		var seaweed *seaweedv1.Seaweed
		masterSA := "seaweedfs-master"
		volumeSA := "seaweedfs-volume"
		filerSA := "seaweedfs-filer"

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
						ConcurrentStart: &concurrentStart,
						ComponentSpec: seaweedv1.ComponentSpec{
							ServiceAccountName: &masterSA,
						},
					},
					Volume: &seaweedv1.VolumeSpec{
						Replicas: 1,
						VolumeServerConfig: seaweedv1.VolumeServerConfig{
							ComponentSpec: seaweedv1.ComponentSpec{
								ServiceAccountName: &volumeSA,
							},
						},
					},
					Filer: &seaweedv1.FilerSpec{
						Replicas: 1,
						ComponentSpec: seaweedv1.ComponentSpec{
							ServiceAccountName: &filerSA,
						},
					},
					VolumeServerDiskCount: func() *int32 { v := int32(1); return &v }(),
				},
			}
		})

		AfterEach(func() {
			if seaweed != nil {
				_ = k8sClient.Delete(ctx, seaweed)
				Eventually(func() error {
					return k8sClient.Get(ctx, types.NamespacedName{
						Name:      seaweedName,
						Namespace: testNamespace,
					}, seaweed)
				}, time.Minute*2, time.Second*5).ShouldNot(Succeed())
			}
		})

		It("should set serviceAccountName on master, volume, and filer StatefulSets", func() {
			Expect(k8sClient.Create(ctx, seaweed)).To(Succeed())

			clientset, err := utils.GetClientset(restCfg)
			Expect(err).NotTo(HaveOccurred())

			expected := map[string]string{
				seaweedName + "-master": masterSA,
				seaweedName + "-volume": volumeSA,
				seaweedName + "-filer":  filerSA,
			}

			for stsName, wantSA := range expected {
				stsName, wantSA := stsName, wantSA
				Eventually(func() (string, error) {
					sts, err := clientset.AppsV1().StatefulSets(testNamespace).Get(ctx, stsName, metav1.GetOptions{})
					if err != nil {
						return "", err
					}
					return sts.Spec.Template.Spec.ServiceAccountName, nil
				}, time.Minute*2, time.Second*10).Should(Equal(wantSA), "StatefulSet %s should pin pods to %s, not the namespace default SA", stsName, wantSA)
			}
		})
	})
})
