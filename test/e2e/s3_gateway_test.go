/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Standalone S3 gateway render test. Creates a Seaweed CR with spec.s3
// set and asserts the operator renders the Deployment, Service, and
// (when requested) Ingress pointing at the right backends. Fast: lives
// in the non-integration lane since it does not need the cluster to
// reach Ready.
package e2e

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/test/utils"
)

var _ = Describe("Standalone S3 gateway", Ordered, func() {
	var (
		ctx           context.Context
		k8sClient     client.Client
		restCfg       *rest.Config
		testNamespace = "test-s3-gateway"
		seaweedName   = "test-sw-s3"
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
		sw := &seaweedv1.Seaweed{}
		_ = k8sClient.Get(ctx, types.NamespacedName{Name: seaweedName, Namespace: testNamespace}, sw)
		_ = k8sClient.Delete(ctx, sw)
		utils.DeleteNamespace(ctx, k8sClient, testNamespace)
	})

	It("renders Deployment + Service + Ingress for the gateway", func() {
		concurrentStart := true
		replicas := int32(2)
		className := "nginx"
		sw := &seaweedv1.Seaweed{
			ObjectMeta: metav1.ObjectMeta{Name: seaweedName, Namespace: testNamespace},
			Spec: seaweedv1.SeaweedSpec{
				Image: "chrislusf/seaweedfs:latest",
				Master: &seaweedv1.MasterSpec{
					Replicas:        1,
					ConcurrentStart: &concurrentStart,
				},
				Volume: &seaweedv1.VolumeSpec{Replicas: 1},
				Filer:  &seaweedv1.FilerSpec{Replicas: 1},
				S3: &seaweedv1.S3GatewaySpec{
					Replicas: replicas,
					Ingress: &seaweedv1.IngressSpec{
						Enabled:   true,
						ClassName: &className,
						Host:      "s3.seaweed.local",
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, sw)).To(Succeed())

		By("waiting for the S3 Deployment to be created")
		dep := &appsv1.Deployment{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Name:      seaweedName + "-s3",
				Namespace: testNamespace,
			}, dep)
		}, 60*time.Second, time.Second).Should(Succeed())

		Expect(dep.Spec.Replicas).NotTo(BeNil())
		Expect(*dep.Spec.Replicas).To(Equal(replicas))
		Expect(dep.Spec.Template.Spec.Containers).To(HaveLen(1))
		container := dep.Spec.Template.Spec.Containers[0]
		Expect(container.Name).To(Equal("s3"))
		// The startup command must dial the filer Service on 8888.
		joined := ""
		for _, c := range container.Command {
			joined += c + " "
		}
		Expect(joined).To(ContainSubstring("-filer=" + seaweedName + "-filer:8888"))
		Expect(joined).To(ContainSubstring("s3"))
		Expect(joined).To(ContainSubstring("-port=8333"))

		By("confirming the gateway Service exists and targets the Deployment")
		svc := &corev1.Service{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      seaweedName + "-s3",
			Namespace: testNamespace,
		}, svc)).To(Succeed())
		Expect(svc.Spec.Selector).To(HaveKeyWithValue("app.kubernetes.io/component", "s3"))
		Expect(svc.Spec.Ports).NotTo(BeEmpty())
		Expect(svc.Spec.Ports[0].Port).To(Equal(int32(8333)))

		By("confirming the S3 Ingress was rendered")
		ing := &networkingv1.Ingress{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      seaweedName + "-s3-ingress",
			Namespace: testNamespace,
		}, ing)).To(Succeed())
		Expect(ing.Spec.Rules).To(HaveLen(1))
		Expect(ing.Spec.Rules[0].Host).To(Equal("s3.seaweed.local"))
		Expect(ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name).To(Equal(seaweedName + "-s3"))
		Expect(ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port.Number).To(Equal(int32(8333)))
	})

	// The spec.s3 + spec.filer.s3 mutual exclusion is enforced by the
	// validating webhook. Since the dev deploy runs with
	// ENABLE_WEBHOOKS=false, the assertion lives in a Go unit test
	// (api/v1/seaweed_webhook_test.go) rather than here.
})
