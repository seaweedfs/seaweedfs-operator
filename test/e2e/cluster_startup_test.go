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
	"net/http"
	"strconv"
	"time"

	"github.com/go-resty/resty/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/test/utils"
)

var _ = Describe("SeaweedFS Cluster Startup", Ordered, Label("integration"), func() {
	var (
		ctx           context.Context
		k8sClient     client.Client
		restCfg       *rest.Config
		testNamespace = "test-cluster-startup"
		seaweedName   = "test-seaweed"
	)

	BeforeAll(func() {
		ctx = context.Background()
		k8sClient, restCfg = utils.NewE2EClient()
		utils.EnsureNamespace(ctx, k8sClient, testNamespace)
	})

	BeforeEach(func() {
		// Registered per-spec so a mid-suite failure still dumps diagnostics
		// for just this namespace. Silent on success.
		DeferCleanup(func() {
			utils.CollectDiagnostics(ctx, k8sClient, restCfg, testNamespace)
		})
	})

	AfterAll(func() {
		// Clean up
		seaweed := &seaweedv1.Seaweed{}
		_ = k8sClient.Get(ctx, types.NamespacedName{Name: seaweedName, Namespace: testNamespace}, seaweed)
		_ = k8sClient.Delete(ctx, seaweed)

		// Wait for cleanup before deleting namespace
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: seaweedName, Namespace: testNamespace}, seaweed)
		}, time.Minute*2, time.Second*5).ShouldNot(Succeed())

		utils.DeleteNamespace(ctx, k8sClient, testNamespace)
	})

	It("should start a SeaweedFS cluster with master, volume, and filer", func() {
		concurrentStart := true
		seaweed := &seaweedv1.Seaweed{
			ObjectMeta: metav1.ObjectMeta{
				Name:      seaweedName,
				Namespace: testNamespace,
			},
			Spec: seaweedv1.SeaweedSpec{
				Image:                 "chrislusf/seaweedfs:latest",
				VolumeServerDiskCount: func() *int32 { v := int32(1); return &v }(),
				Master: &seaweedv1.MasterSpec{
					Replicas:        1,
					ConcurrentStart: &concurrentStart,
				},
				Volume: &seaweedv1.VolumeSpec{
					Replicas: 1,
					VolumeServerConfig: seaweedv1.VolumeServerConfig{
						ResourceRequirements: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse("2Gi"),
							},
						},
					},
				},
				Filer: &seaweedv1.FilerSpec{
					Replicas: 1,
				},
			},
		}

		By("creating the Seaweed resource")
		err := k8sClient.Create(ctx, seaweed)
		if err != nil {
			GinkgoWriter.Printf("FAILED to create Seaweed resource: %v\n", err)
		}
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Printf("Seaweed resource created in namespace %s\n", testNamespace)

		By("verifying the Seaweed resource exists")
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Name:      seaweedName,
				Namespace: testNamespace,
			}, seaweed)
		}, time.Minute, time.Second*5).Should(Succeed())

		By("waiting for the Seaweed CR to become Ready")
		utils.WaitForSeaweedReady(ctx,
			k8sClient,
			types.NamespacedName{Name: seaweedName, Namespace: testNamespace},
			7*time.Minute)

		By("sanity-checking the master StatefulSet is reported ready")
		sts := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      seaweedName + "-master",
			Namespace: testNamespace,
		}, sts)).To(Succeed())
		Expect(sts.Status.ReadyReplicas).To(Equal(int32(1)))

		By("verifying master API is reachable via port-forward")
		clientset, err := utils.GetClientset(restCfg)
		Expect(err).NotTo(HaveOccurred())

		// Find the master pod
		pods, err := clientset.CoreV1().Pods(testNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("app.kubernetes.io/component=master,app.kubernetes.io/instance=%s", seaweedName),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pods.Items).NotTo(BeEmpty(), "No master pods found")

		stopCh := make(chan struct{}, 1)
		readyCh := make(chan struct{})
		localPort, err := utils.GetFreePort()
		Expect(err).NotTo(HaveOccurred())

		err = utils.RunPortForward(restCfg, testNamespace, pods.Items[0].Name,
			[]string{fmt.Sprintf("%d:%d", localPort, seaweedv1.MasterHTTPPort)}, stopCh, readyCh)
		Expect(err).NotTo(HaveOccurred())
		<-readyCh

		httpClient := resty.New().SetTimeout(10 * time.Second)
		resp, err := httpClient.R().Get(fmt.Sprintf("http://localhost:%s/cluster/status", strconv.Itoa(localPort)))
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode()).To(Equal(http.StatusOK))
		GinkgoWriter.Printf("Master /cluster/status response: %s\n", resp.String())

		close(stopCh)
	})
})
