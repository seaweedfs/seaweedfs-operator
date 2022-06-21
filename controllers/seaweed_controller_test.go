package controllers

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

var (
	TrueValue   = true
	FalseVallue = false
)

var _ = Describe("Seaweed Controller", func() {
	Context("Basic Functionality", func() {
		It("Should create StatefulSets", func() {
			By("By creating a new Seaweed", func() {
				const (
					namespace = "default"
					name      = "test-seaweed"

					timeout  = time.Second * 30
					interval = time.Millisecond * 250
				)

				ctx := context.Background()
				seaweed := &seaweedv1.Seaweed{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
						Name:      name,
					},
					Spec: seaweedv1.SeaweedSpec{
						Image:                 "chrislusf/seaweedfs:3.12",
						VolumeServerDiskCount: 1,
						Master: &seaweedv1.MasterSpec{
							Replicas:        3,
							ConcurrentStart: &TrueValue,
						},
						Volume: &seaweedv1.VolumeSpec{
							Replicas: 1,
							ResourceRequirements: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceStorage: resource.MustParse("1Gi"),
								},
							},
						},
						Filer: &seaweedv1.FilerSpec{
							Replicas: 2,
						},
					},
				}
				Expect(k8sClient.Create(ctx, seaweed)).Should(Succeed())

				masterKey := types.NamespacedName{Name: name + "-master", Namespace: namespace}
				volumeKey := types.NamespacedName{Name: name + "-volume", Namespace: namespace}
				filerKey := types.NamespacedName{Name: name + "-filer", Namespace: namespace}

				masterSts := &appsv1.StatefulSet{}
				volumeSts := &appsv1.StatefulSet{}
				filerSts := &appsv1.StatefulSet{}

				Eventually(func() bool {
					err := k8sClient.Get(ctx, masterKey, masterSts)
					return err == nil
				}, timeout, interval).Should(BeTrue())
				Expect(masterSts.Spec.Replicas).ShouldNot(BeNil())
				Expect(*masterSts.Spec.Replicas).Should(Equal(seaweed.Spec.Master.Replicas))

				Eventually(func() bool {
					err := k8sClient.Get(ctx, volumeKey, volumeSts)
					return err == nil
				}, timeout, interval).Should(BeTrue())
				Expect(volumeSts.Spec.Replicas).ShouldNot(BeNil())
				Expect(*volumeSts.Spec.Replicas).Should(Equal(seaweed.Spec.Volume.Replicas))

				Eventually(func() bool {
					err := k8sClient.Get(ctx, filerKey, filerSts)
					return err == nil
				}, timeout, interval).Should(BeTrue())
				Expect(filerSts.Spec.Replicas).ShouldNot(BeNil())
				Expect(*filerSts.Spec.Replicas).Should(Equal(seaweed.Spec.Filer.Replicas))
			})
		})
	})
})
