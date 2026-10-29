/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// TLS integration test: verifies the operator provisions the self-signed
// issuer chain, the wildcard server Certificate, the security Secret,
// and wires them into every component pod so the cluster still comes up
// cleanly. Kind test clusters provisioned by `make kind-prepare` already
// install cert-manager, so this spec can run without additional setup.
package e2e

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/test/utils"
)

var _ = Describe("SeaweedFS TLS via cert-manager", Ordered, Label("integration"), func() {
	var (
		ctx           context.Context
		k8sClient     client.Client
		restCfg       *rest.Config
		testNamespace = "test-tls"
		seaweedName   = "test-sw-tls"
	)

	BeforeAll(func() {
		ctx = context.Background()
		k8sClient, restCfg = utils.NewE2EClient()
		utils.EnsureNamespace(ctx, k8sClient, testNamespace)

		// Skip if cert-manager isn't installed — don't fail an unrelated
		// test run just because the environment is missing the dep.
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(schema.GroupVersionKind{
			Group: "cert-manager.io", Version: "v1", Kind: "CertificateList",
		})
		if err := k8sClient.List(ctx, list, &client.ListOptions{Limit: 1}); err != nil {
			Skip("cert-manager not installed, skipping TLS test: " + err.Error())
		}
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

	It("provisions cert chain + security.toml and brings a cluster up", func() {
		concurrentStart := true
		one := int32(1)
		// Volume server requires a storage request so the operator can
		// size its VolumeClaimTemplate — leaving it nil produces an
		// invalid PVC that blocks the StatefulSet forever.
		sw := &seaweedv1.Seaweed{
			ObjectMeta: metav1.ObjectMeta{Name: seaweedName, Namespace: testNamespace},
			Spec: seaweedv1.SeaweedSpec{
				Image:                 "chrislusf/seaweedfs:latest",
				VolumeServerDiskCount: &one,
				TLS:                   &seaweedv1.TLSSpec{Enabled: true},
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
				Filer: &seaweedv1.FilerSpec{Replicas: 1},
			},
		}
		Expect(k8sClient.Create(ctx, sw)).To(Succeed())

		By("waiting for cert-manager resources to become Ready")
		// Probing the Issuer's status.conditions via unstructured keeps us
		// off any compile-time dep on cert-manager.
		Eventually(func() bool {
			return isCertManagerReady(ctx, k8sClient, testNamespace, seaweedName+"-selfsigned", "Issuer")
		}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "self-signed Issuer did not become Ready")

		Eventually(func() bool {
			return isCertManagerReady(ctx, k8sClient, testNamespace, seaweedName+"-ca", "Certificate")
		}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "CA Certificate did not become Ready")

		Eventually(func() bool {
			return isCertManagerReady(ctx, k8sClient, testNamespace, seaweedName+"-ca-issuer", "Issuer")
		}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "CA Issuer did not become Ready")

		Eventually(func() bool {
			return isCertManagerReady(ctx, k8sClient, testNamespace, seaweedName+"-server", "Certificate")
		}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "server Certificate did not become Ready")

		By("confirming the server Secret and security Secret exist")
		secret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      seaweedName + "-server-tls",
			Namespace: testNamespace,
		}, secret)).To(Succeed())
		Expect(secret.Data).To(HaveKey("tls.crt"))
		Expect(secret.Data).To(HaveKey("tls.key"))
		Expect(secret.Data).To(HaveKey("ca.crt"))

		securitySecret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      seaweedName + "-security-config",
			Namespace: testNamespace,
		}, securitySecret)).To(Succeed())
		securityTOML := string(securitySecret.Data["security.toml"])
		Expect(securityTOML).To(ContainSubstring(`ca = "/etc/sw-tls/ca.crt"`))
		Expect(securityTOML).To(ContainSubstring(`[grpc.master]`))
		Expect(securityTOML).To(ContainSubstring(`[grpc.filer]`))

		By("confirming pods mount the TLS Secret and security.toml")
		pods := &corev1.PodList{}
		Expect(k8sClient.List(ctx, pods, client.InNamespace(testNamespace))).To(Succeed())
		Expect(pods.Items).NotTo(BeEmpty(), "no seaweed pods found")
		for _, p := range pods.Items {
			if len(p.Spec.Containers) == 0 {
				continue
			}
			hasTLS := false
			hasSecurity := false
			for _, vm := range p.Spec.Containers[0].VolumeMounts {
				if vm.MountPath == "/etc/sw-tls" {
					hasTLS = true
				}
				if vm.MountPath == "/etc/sw-security" {
					hasSecurity = true
				}
			}
			Expect(hasTLS).To(BeTrue(), "pod %s missing /etc/sw-tls mount", p.Name)
			Expect(hasSecurity).To(BeTrue(), "pod %s missing /etc/sw-security mount", p.Name)
			// And the startup command should have -config_dir before the subcommand.
			cmd := strings.Join(p.Spec.Containers[0].Command, " ")
			Expect(cmd).To(ContainSubstring("-config_dir=/etc/sw-security"),
				"pod %s missing -config_dir flag in command: %s", p.Name, cmd)
		}

		By("waiting for the Seaweed CR to become Ready with TLS enabled")
		utils.WaitForSeaweedReady(ctx, k8sClient,
			types.NamespacedName{Name: seaweedName, Namespace: testNamespace},
			7*time.Minute)
	})
})

// isCertManagerReady checks whether a cert-manager resource has a
// Ready=True condition. Works for both Certificate and Issuer kinds.
func isCertManagerReady(ctx context.Context, c client.Client, ns, name, kind string) bool {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "cert-manager.io", Version: "v1", Kind: kind,
	})
	err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, u)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			GinkgoWriter.Printf("get %s %s: %v\n", kind, name, err)
		}
		return false
	}
	conditions, found, err := unstructured.NestedSlice(u.Object, "status", "conditions")
	if err != nil || !found {
		return false
	}
	for _, raw := range conditions {
		cond, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if cond["type"] == "Ready" && cond["status"] == "True" {
			return true
		}
	}
	return false
}

// TLS integration test for spec.tls.source: TLS material arrives in a volume
// the user provides — here a plain Secret standing in for a CSI driver like
// the SPIFFE CSI driver — so the operator must provision no cert-manager
// objects, must mount the volume into every component, and must render
// security.toml with the configured file names and peer authorization.
var _ = Describe("SeaweedFS TLS via spec.tls.source volume", Ordered, Label("integration"), func() {
	var (
		ctx           context.Context
		k8sClient     client.Client
		restCfg       *rest.Config
		testNamespace = "test-tls-volume"
		seaweedName   = "test-sw-tls-vol"
		secretName    = "e2e-tls-material"
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

	It("mounts the user volume and brings the cluster up without cert-manager objects", func() {
		concurrentStart := true
		one := int32(1)
		refresh := metav1.Duration{Duration: time.Minute}

		By("creating the TLS material Secret standing in for a CSI volume")
		Expect(k8sClient.Create(ctx, e2eTLSSecret(testNamespace, secretName, seaweedName))).To(Succeed())

		sw := &seaweedv1.Seaweed{
			ObjectMeta: metav1.ObjectMeta{Name: seaweedName, Namespace: testNamespace},
			Spec: seaweedv1.SeaweedSpec{
				Image:                 "chrislusf/seaweedfs:latest",
				VolumeServerDiskCount: &one,
				TLS: &seaweedv1.TLSSpec{
					Enabled: true,
					Source: &seaweedv1.TLSSource{
						Volume: &corev1.VolumeSource{
							Secret: &corev1.SecretVolumeSource{SecretName: secretName},
						},
					},
					AllowedWildcardDomain: ".e2e.test",
					RefreshInterval:       &refresh,
				},
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
				Filer: &seaweedv1.FilerSpec{Replicas: 1},
			},
		}
		Expect(k8sClient.Create(ctx, sw)).To(Succeed())

		By("confirming the operator creates no cert-manager objects")
		Consistently(func() int {
			return countUnstructured(ctx, k8sClient, testNamespace, certificateGVKE2E) +
				countUnstructured(ctx, k8sClient, testNamespace, issuerGVKE2E)
		}, 30*time.Second, 5*time.Second).Should(Equal(0), "cert-manager objects must not be created for a volume source")

		By("confirming security.toml renders the wildcard domain and default file names")
		securitySecret := &corev1.Secret{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Name:      seaweedName + "-security-config",
				Namespace: testNamespace,
			}, securitySecret)
		}, time.Minute, 5*time.Second).Should(Succeed())
		securityTOML := string(securitySecret.Data["security.toml"])
		Expect(securityTOML).To(ContainSubstring(`ca = "/etc/sw-tls/ca.crt"`))
		Expect(securityTOML).To(ContainSubstring(`allowed_wildcard_domain = ".e2e.test"`))
		Expect(securityTOML).To(ContainSubstring(`[grpc.master]`))

		By("confirming pods mount the user volume, not an operator Secret")
		Eventually(func() int {
			pods := &corev1.PodList{}
			if err := k8sClient.List(ctx, pods, client.InNamespace(testNamespace)); err != nil {
				return -1
			}
			return len(pods.Items)
		}, 2*time.Minute, 5*time.Second).Should(BeNumerically(">", 0), "no seaweed pods found")
		pods := &corev1.PodList{}
		Expect(k8sClient.List(ctx, pods, client.InNamespace(testNamespace))).To(Succeed())
		for _, p := range pods.Items {
			if len(p.Spec.Containers) == 0 {
				continue
			}
			sourceOK := false
			for _, v := range p.Spec.Volumes {
				if v.Name == "sw-tls" && v.Secret != nil && v.Secret.SecretName == secretName {
					sourceOK = true
				}
			}
			Expect(sourceOK).To(BeTrue(), "pod %s must mount the source volume, got %v", p.Name, p.Spec.Volumes)
			hasTLS, hasRefresh := false, false
			for _, vm := range p.Spec.Containers[0].VolumeMounts {
				if vm.MountPath == "/etc/sw-tls" {
					hasTLS = true
				}
			}
			for _, e := range p.Spec.Containers[0].Env {
				if e.Name == "WEED_TLS_CERT_REFRESH_INTERVAL" && e.Value == "1m0s" {
					hasRefresh = true
				}
			}
			Expect(hasTLS).To(BeTrue(), "pod %s missing /etc/sw-tls mount", p.Name)
			Expect(hasRefresh).To(BeTrue(), "pod %s missing WEED_TLS_CERT_REFRESH_INTERVAL", p.Name)
		}

		By("waiting for the Seaweed CR to become Ready on volume-sourced mTLS")
		utils.WaitForSeaweedReady(ctx, k8sClient,
			types.NamespacedName{Name: seaweedName, Namespace: testNamespace},
			7*time.Minute)
	})
})

var (
	certificateGVKE2E = schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"}
	issuerGVKE2E      = schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Issuer"}
)

func countUnstructured(ctx context.Context, c client.Client, ns string, gvk schema.GroupVersionKind) int {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind + "List"})
	if err := c.List(ctx, list, client.InNamespace(ns)); err != nil {
		return -1
	}
	return len(list.Items)
}

// e2eTLSSecret mints a CA and a leaf certificate the whole cluster shares —
// the same shape cert-manager writes — so the Secret works as a
// spec.tls.source volume. The leaf covers the headless service names the
// operator would otherwise put on the wildcard Certificate.
func e2eTLSSecret(ns, name, seaweedName string) *corev1.Secret {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	Expect(err).NotTo(HaveOccurred())
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	Expect(err).NotTo(HaveOccurred())

	now := time.Now()
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "e2e CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	Expect(err).NotTo(HaveOccurred())
	caCert, err := x509.ParseCertificate(caDER)
	Expect(err).NotTo(HaveOccurred())

	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "seaweed.e2e.test"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames: []string{
			"localhost",
			seaweedName + "-master",
			seaweedName + "-master-peer",
			seaweedName + "-volume",
			seaweedName + "-volume-peer",
			seaweedName + "-filer",
			seaweedName + "-filer-peer",
			seaweedName + "-s3",
			seaweedName + "-admin",
			seaweedName + "-worker",
			"*." + seaweedName + "-master-peer." + ns + ".svc.cluster.local",
			"*." + seaweedName + "-volume-peer." + ns + ".svc.cluster.local",
			"*." + seaweedName + "-filer-peer." + ns + ".svc.cluster.local",
			"*." + ns + ".svc.cluster.local",
		},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	Expect(err).NotTo(HaveOccurred())

	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	Expect(err).NotTo(HaveOccurred())

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"ca.crt":  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
			"tls.crt": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
			"tls.key": pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		},
	}
}
