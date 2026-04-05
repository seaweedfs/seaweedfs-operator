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
	corev1 "k8s.io/api/core/v1"
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

var _ = Describe("SeaweedFS Cluster Startup", Ordered, func() {
	var (
		ctx           context.Context
		k8sClient     client.Client
		testNamespace = "test-cluster-startup"
		seaweedName   = "test-seaweed"
	)

	BeforeAll(func() {
		ctx = context.Background()

		scheme := runtime.NewScheme()
		utilruntime.Must(clientgoscheme.AddToScheme(scheme))
		utilruntime.Must(seaweedv1.AddToScheme(scheme))

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
			err = k8sClient.Get(ctx, types.NamespacedName{Name: testNamespace}, ns)
			Expect(err).NotTo(HaveOccurred(), "Failed to create or get test namespace")
		}
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

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, ns)
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
				},
				Filer: &seaweedv1.FilerSpec{
					Replicas: 1,
				},
			},
		}

		By("creating the Seaweed resource")
		Expect(k8sClient.Create(ctx, seaweed)).To(Succeed())
		GinkgoWriter.Printf("Seaweed resource created in namespace %s\n", testNamespace)

		By("waiting for master StatefulSet to have ready replicas")
		Eventually(func() int32 {
			sts := &appsv1.StatefulSet{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      seaweedName + "-master",
				Namespace: testNamespace,
			}, sts)
			if err != nil {
				GinkgoWriter.Printf("Waiting for master StatefulSet: %v\n", err)
				return 0
			}
			GinkgoWriter.Printf("Master StatefulSet: %d/%d ready\n", sts.Status.ReadyReplicas, *sts.Spec.Replicas)
			return sts.Status.ReadyReplicas
		}, time.Minute*5, time.Second*10).Should(Equal(int32(1)))

		By("waiting for volume StatefulSet to have ready replicas")
		Eventually(func() int32 {
			sts := &appsv1.StatefulSet{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      seaweedName + "-volume",
				Namespace: testNamespace,
			}, sts)
			if err != nil {
				GinkgoWriter.Printf("Waiting for volume StatefulSet: %v\n", err)
				return 0
			}
			GinkgoWriter.Printf("Volume StatefulSet: %d/%d ready\n", sts.Status.ReadyReplicas, *sts.Spec.Replicas)
			return sts.Status.ReadyReplicas
		}, time.Minute*5, time.Second*10).Should(Equal(int32(1)))

		By("waiting for filer StatefulSet to have ready replicas")
		Eventually(func() int32 {
			sts := &appsv1.StatefulSet{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      seaweedName + "-filer",
				Namespace: testNamespace,
			}, sts)
			if err != nil {
				GinkgoWriter.Printf("Waiting for filer StatefulSet: %v\n", err)
				return 0
			}
			GinkgoWriter.Printf("Filer StatefulSet: %d/%d ready\n", sts.Status.ReadyReplicas, *sts.Spec.Replicas)
			return sts.Status.ReadyReplicas
		}, time.Minute*5, time.Second*10).Should(Equal(int32(1)))

		By("verifying the Seaweed CR status shows Ready")
		Eventually(func() bool {
			sw := &seaweedv1.Seaweed{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      seaweedName,
				Namespace: testNamespace,
			}, sw)
			if err != nil {
				return false
			}
			for _, cond := range sw.Status.Conditions {
				if cond.Type == "Ready" && cond.Status == metav1.ConditionTrue {
					GinkgoWriter.Printf("Seaweed cluster is Ready\n")
					return true
				}
			}
			GinkgoWriter.Printf("Seaweed status: master=%d/%d, volume=%d/%d, filer=%d/%d\n",
				sw.Status.Master.ReadyReplicas, sw.Status.Master.Replicas,
				sw.Status.Volume.ReadyReplicas, sw.Status.Volume.Replicas,
				sw.Status.Filer.ReadyReplicas, sw.Status.Filer.Replicas)
			return false
		}, time.Minute*2, time.Second*10).Should(BeTrue())

		By("verifying master API is reachable via port-forward")
		kubeconfig := config.GetConfigOrDie()
		clientset, err := utils.GetClientset(kubeconfig)
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

		err = utils.RunPortForward(kubeconfig, testNamespace, pods.Items[0].Name,
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
