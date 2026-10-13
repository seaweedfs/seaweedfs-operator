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
// reach Ready. The native HTTPS spec below does, so it is labeled
// integration.
package e2e

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
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
				Volume: &seaweedv1.VolumeSpec{
					Replicas: 1,
					// k8s 1.34+ rejects VolumeClaimTemplates without a
					// storage request; keep the test green across all
					// matrix versions.
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
		// The reconciler creates the Deployment, Service, and Ingress in
		// sequence within one pass, but on slower k8s matrix versions the
		// Deployment Eventually can return before the Service/Ingress are
		// created. Poll both, same cadence as the Deployment wait above.
		svc := &corev1.Service{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Name:      seaweedName + "-s3",
				Namespace: testNamespace,
			}, svc)
		}, 60*time.Second, time.Second).Should(Succeed())
		Expect(svc.Spec.Selector).To(HaveKeyWithValue("app.kubernetes.io/component", "s3"))
		Expect(svc.Spec.Ports).NotTo(BeEmpty())
		Expect(svc.Spec.Ports[0].Port).To(Equal(int32(8333)))

		By("confirming the S3 Ingress was rendered")
		ing := &networkingv1.Ingress{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Name:      seaweedName + "-s3-ingress",
				Namespace: testNamespace,
			}, ing)
		}, 60*time.Second, time.Second).Should(Succeed())
		Expect(ing.Spec.Rules).To(HaveLen(1))
		Expect(ing.Spec.Rules[0].Host).To(Equal("s3.seaweed.local"))
		Expect(ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name).To(Equal(seaweedName + "-s3"))
		Expect(ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port.Number).To(Equal(int32(8333)))

		By("pruning the gateway when Spec.S3 is cleared")
		live := &seaweedv1.Seaweed{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: seaweedName, Namespace: testNamespace}, live)).To(Succeed())
		live.Spec.S3 = nil
		Expect(k8sClient.Update(ctx, live)).To(Succeed())

		Eventually(func() bool {
			depGone := apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{
				Name:      seaweedName + "-s3",
				Namespace: testNamespace,
			}, &appsv1.Deployment{}))
			svcGone := apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{
				Name:      seaweedName + "-s3",
				Namespace: testNamespace,
			}, &corev1.Service{}))
			ingGone := apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{
				Name:      seaweedName + "-s3-ingress",
				Namespace: testNamespace,
			}, &networkingv1.Ingress{}))
			return depGone && svcGone && ingGone
		}, 60*time.Second, time.Second).Should(BeTrue(),
			"Deployment, Service, and Ingress should all be pruned after Spec.S3 is cleared")
	})

	// The spec.s3 + spec.filer.s3 mutual exclusion is enforced by the
	// validating webhook. Since the dev deploy runs with
	// ENABLE_WEBHOOKS=false, the assertion lives in a Go unit test
	// (api/v1/seaweed_webhook_test.go) rather than here.
})

// Runs a real `weed s3` with native HTTPS, first alongside HTTP and then
// with httpsOnly, and checks what actually answers on each port.
var _ = Describe("Standalone S3 gateway native HTTPS", Ordered, Label("integration"), func() {
	var (
		ctx           context.Context
		k8sClient     client.Client
		restCfg       *rest.Config
		testNamespace = "test-s3-https"
		seaweedName   = "test-sw-s3-https"
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

	// readyS3Pod waits for a single Ready S3 pod whose startup script
	// contains want. That filters out the previous replica after a
	// rollout, instead of racing the still-Ready old pod.
	readyS3Pod := func(want string) string {
		var name string
		Eventually(func(g Gomega) {
			pods := &corev1.PodList{}
			g.Expect(k8sClient.List(ctx, pods, client.InNamespace(testNamespace),
				client.MatchingLabels{"app.kubernetes.io/component": "s3"})).To(Succeed())
			var live []corev1.Pod
			for _, p := range pods.Items {
				if p.DeletionTimestamp == nil {
					live = append(live, p)
				}
			}
			g.Expect(live).To(HaveLen(1))
			p := live[0]
			g.Expect(strings.Join(p.Spec.Containers[0].Command, " ")).To(ContainSubstring(want))
			ready := false
			for _, c := range p.Status.Conditions {
				if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
					ready = true
				}
			}
			g.Expect(ready).To(BeTrue(), "pod %s is not Ready", p.Name)
			name = p.Name
		}, 5*time.Minute, 5*time.Second).Should(Succeed())
		return name
	}

	// get calls /status on a pod port through a port-forward.
	get := func(httpClient *http.Client, scheme, pod string, port int) (int, error) {
		local, err := utils.GetFreePort()
		Expect(err).NotTo(HaveOccurred())
		stopCh, readyCh := make(chan struct{}), make(chan struct{})
		defer close(stopCh)
		Expect(utils.RunPortForward(restCfg, testNamespace, pod,
			[]string{fmt.Sprintf("%d:%d", local, port)}, stopCh, readyCh)).To(Succeed())
		<-readyCh
		resp, err := httpClient.Get(fmt.Sprintf("%s://127.0.0.1:%d/status", scheme, local))
		if err != nil {
			return 0, err
		}
		_ = resp.Body.Close()
		return resp.StatusCode, nil
	}

	It("serves HTTPS with and without HTTP and becomes ready", func() {
		serverName := fmt.Sprintf("%s-s3.%s.svc", seaweedName, testNamespace)
		certPEM, keyPEM, roots := selfSignedCert(serverName)
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "s3-tls", Namespace: testNamespace},
			Type:       corev1.SecretTypeTLS,
			Data:       map[string][]byte{corev1.TLSCertKey: certPEM, corev1.TLSPrivateKeyKey: keyPEM},
		})).To(Succeed())

		concurrentStart := true
		diskCount := int32(1)
		httpsPort := int32(8334)
		Expect(k8sClient.Create(ctx, &seaweedv1.Seaweed{
			ObjectMeta: metav1.ObjectMeta{Name: seaweedName, Namespace: testNamespace},
			Spec: seaweedv1.SeaweedSpec{
				// Pinned: this checks weed's own flag handling, so a new
				// release should be a deliberate bump.
				Image:                 "chrislusf/seaweedfs:4.48",
				VolumeServerDiskCount: &diskCount,
				Master:                &seaweedv1.MasterSpec{Replicas: 1, ConcurrentStart: &concurrentStart},
				Volume: &seaweedv1.VolumeSpec{
					Replicas: 1,
					VolumeServerConfig: seaweedv1.VolumeServerConfig{
						ResourceRequirements: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("2Gi")},
						},
					},
				},
				Filer: &seaweedv1.FilerSpec{Replicas: 1},
				S3: &seaweedv1.S3GatewaySpec{
					Replicas:  1,
					HTTPSPort: &httpsPort,
					TLSSecret: &corev1.LocalObjectReference{Name: "s3-tls"},
				},
			},
		})).To(Succeed())

		// Verifies the chain against the test certificate and the hostname
		// against the Service DNS name.
		httpsClient := &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: serverName},
			},
		}
		httpClient := &http.Client{Timeout: 10 * time.Second}

		By("serving HTTPS alongside HTTP")
		utils.WaitForSeaweedReady(ctx, k8sClient,
			types.NamespacedName{Name: seaweedName, Namespace: testNamespace}, 7*time.Minute)
		pod := readyS3Pod("-port.https=8334")
		Expect(get(httpsClient, "https", pod, 8334)).To(Equal(http.StatusOK))
		Expect(get(httpClient, "http", pod, 8333)).To(Equal(http.StatusOK))

		By("switching to httpsOnly")
		Eventually(func() error {
			sw := &seaweedv1.Seaweed{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: seaweedName, Namespace: testNamespace}, sw); err != nil {
				return err
			}
			sw.Spec.S3.HTTPSOnly = true
			return k8sClient.Update(ctx, sw)
		}, time.Minute, 2*time.Second).Should(Succeed())

		// A Ready pod with -port.grpc means kubelet's HTTPS probe passed
		// against the httpsOnly listener.
		pod = readyS3Pod("-port.grpc=")
		Expect(get(httpsClient, "https", pod, 8334)).To(Equal(http.StatusOK))
		_, err := get(httpClient, "http", pod, 8333)
		Expect(err).To(HaveOccurred(), "plain HTTP should be off with httpsOnly")
	})
})

// selfSignedCert returns a PEM certificate and key for host, plus a pool
// that trusts it.
func selfSignedCert(host string) (certPEM, keyPEM []byte, roots *x509.CertPool) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	Expect(err).NotTo(HaveOccurred())
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: host},
		DNSNames:              []string{host},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	Expect(err).NotTo(HaveOccurred())
	cert, err := x509.ParseCertificate(der)
	Expect(err).NotTo(HaveOccurred())
	keyDER, err := x509.MarshalECPrivateKey(key)
	Expect(err).NotTo(HaveOccurred())

	roots = x509.NewCertPool()
	roots.AddCert(cert)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		roots
}
