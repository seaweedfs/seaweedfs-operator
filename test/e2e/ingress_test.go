/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Per-component Ingress rendering test. Fast: only creates the Seaweed CR
// and asserts the operator renders the expected Ingress resources — does
// not wait for cluster Ready or require an Ingress controller to be
// installed. Lives in the non-integration lane.
package e2e

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/test/utils"
)

var _ = Describe("Per-component Ingress", Ordered, func() {
	var (
		ctx           context.Context
		k8sClient     client.Client
		restCfg       *rest.Config
		testNamespace = "test-component-ingress"
		seaweedName   = "test-sw-ingress"
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

	It("renders Ingress resources for each opted-in component", func() {
		className := "nginx"
		concurrentStart := true
		sw := &seaweedv1.Seaweed{
			ObjectMeta: metav1.ObjectMeta{Name: seaweedName, Namespace: testNamespace},
			Spec: seaweedv1.SeaweedSpec{
				Image: "chrislusf/seaweedfs:latest",
				Master: &seaweedv1.MasterSpec{
					Replicas: 1,
					// Skip the synchronous wait-for-master step so
					// reconcile progresses to ingress rendering without
					// needing the master pod to actually become Ready.
					ConcurrentStart: &concurrentStart,
					Ingress: &seaweedv1.IngressSpec{
						Enabled:   true,
						ClassName: &className,
						Host:      "master.seaweed.local",
						Path:      "/",
						Annotations: map[string]string{
							"nginx.ingress.kubernetes.io/backend-protocol": "HTTP",
						},
					},
				},
				Volume: &seaweedv1.VolumeSpec{
					Replicas: 1,
					Ingress: &seaweedv1.IngressSpec{
						Enabled: true,
						Host:    "volume.seaweed.local",
					},
				},
				Filer: &seaweedv1.FilerSpec{
					Replicas: 1,
					Ingress: &seaweedv1.IngressSpec{
						Enabled: true,
						Host:    "filer.seaweed.local",
						TLS: []seaweedv1.IngressTLS{{
							Hosts:      []string{"filer.seaweed.local"},
							SecretName: "filer-tls",
						}},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, sw)).To(Succeed())

		By("looking up each rendered Ingress")
		for _, tc := range []struct {
			component string
			host      string
			service   string
			checkTLS  bool
		}{
			{"master", "master.seaweed.local", seaweedName + "-master", false},
			{"volume", "volume.seaweed.local", seaweedName + "-volume", false},
			{"filer", "filer.seaweed.local", seaweedName + "-filer", true},
		} {
			name := seaweedName + "-" + tc.component + "-ingress"
			ing := &networkingv1.Ingress{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: testNamespace}, ing)
			}, 30*time.Second, time.Second).Should(Succeed(), "Ingress %s not created", name)

			Expect(ing.Spec.Rules).To(HaveLen(1))
			Expect(ing.Spec.Rules[0].Host).To(Equal(tc.host))
			Expect(ing.Spec.Rules[0].HTTP.Paths).To(HaveLen(1))
			Expect(ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name).To(Equal(tc.service))

			if tc.checkTLS {
				Expect(ing.Spec.TLS).To(HaveLen(1))
				Expect(ing.Spec.TLS[0].SecretName).To(Equal("filer-tls"))
				Expect(ing.Spec.TLS[0].Hosts).To(ConsistOf("filer.seaweed.local"))
			}
		}

		By("asserting the master Ingress picked up IngressClassName + annotations")
		master := &networkingv1.Ingress{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      seaweedName + "-master-ingress",
			Namespace: testNamespace,
		}, master)).To(Succeed())
		Expect(master.Spec.IngressClassName).NotTo(BeNil())
		Expect(*master.Spec.IngressClassName).To(Equal("nginx"))
		Expect(master.Annotations).To(HaveKeyWithValue(
			"nginx.ingress.kubernetes.io/backend-protocol", "HTTP"))

		By("pruning per-component Ingresses when they are removed from spec")
		// Re-fetch so we edit the latest resourceVersion.
		live := &seaweedv1.Seaweed{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: seaweedName, Namespace: testNamespace}, live)).To(Succeed())
		live.Spec.Master.Ingress = nil
		live.Spec.Volume.Ingress = nil
		// Keep filer.ingress so we can verify the prune is selective.
		Expect(k8sClient.Update(ctx, live)).To(Succeed())

		Eventually(func() bool {
			masterGone := k8sClient.Get(ctx, types.NamespacedName{
				Name:      seaweedName + "-master-ingress",
				Namespace: testNamespace,
			}, &networkingv1.Ingress{})
			volumeGone := k8sClient.Get(ctx, types.NamespacedName{
				Name:      seaweedName + "-volume-ingress",
				Namespace: testNamespace,
			}, &networkingv1.Ingress{})
			return apierrors.IsNotFound(masterGone) && apierrors.IsNotFound(volumeGone)
		}, 60*time.Second, time.Second).Should(BeTrue(),
			"master and volume Ingresses should be pruned after opt-out")

		By("confirming the filer Ingress survives the prune")
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      seaweedName + "-filer-ingress",
			Namespace: testNamespace,
		}, &networkingv1.Ingress{})).To(Succeed())
	})
})
