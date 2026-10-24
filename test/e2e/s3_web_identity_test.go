/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Web-identity federation end to end: an S3OIDCProvider trusting the cluster's
// own service-account issuer and an S3Role admitting one ServiceAccount. A pod
// of that ServiceAccount exchanges its projected token for S3 credentials with
// AssumeRoleWithWebIdentity and writes to a bucket; a pod of another
// ServiceAccount is refused. The issuer is the Kubernetes API server, so the
// test needs no identity provider of its own.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/test/utils"
)

// seaweedImageForWebIdentity is the SeaweedFS image under test. It must carry
// the filer IAM gRPC OIDC-provider and role RPCs; override with SEAWEEDFS_IMAGE
// to test a build that is not yet released.
func seaweedImageForWebIdentity() string {
	if img := os.Getenv("SEAWEEDFS_IMAGE"); img != "" {
		return img
	}
	return "chrislusf/seaweedfs:latest"
}

const webIdentityAudience = "seaweedfs-s3"

// webIdentityScript exchanges the pod's projected token and checks the outcome
// named by $EXPECT: "allow" must get credentials and write the bucket, "deny"
// must be refused. The Job succeeds only on the expected outcome.
const webIdentityScript = `set -u
TOKEN=$(cat /var/run/secrets/tokens/s3)
if CREDS=$(aws sts assume-role-with-web-identity --endpoint-url "$EP" --role-arn "$ROLE" \
    --role-session-name e2e --web-identity-token "$TOKEN" \
    --query 'Credentials.[AccessKeyId,SecretAccessKey,SessionToken]' --output text 2>/tmp/err); then
  GOT=allow
else
  GOT=deny
fi
echo "outcome=$GOT expected=$EXPECT"
cat /tmp/err
if [ "$EXPECT" = deny ]; then
  [ "$GOT" = deny ] && grep -q AccessDenied /tmp/err
  exit $?
fi
[ "$GOT" = allow ] || exit 1
set -- $CREDS
export AWS_ACCESS_KEY_ID=$1 AWS_SECRET_ACCESS_KEY=$2 AWS_SESSION_TOKEN=$3
echo federated > /tmp/object
aws s3 cp /tmp/object "s3://$BUCKET/web-identity.txt" --endpoint-url "$EP"
`

var _ = Describe("S3 web-identity federation", Ordered, Label("integration"), func() {
	var (
		ctx           context.Context
		k8sClient     client.Client
		restCfg       *rest.Config
		testNamespace = "test-s3-web-identity"
		seaweedName   = "test-sw-wi"
		bucketName    = "wi-bucket"
		discoveryCRB  = "test-s3-web-identity-oidc-discovery"
	)

	named := func(name string) types.NamespacedName {
		return types.NamespacedName{Namespace: testNamespace, Name: name}
	}

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
		_ = k8sClient.Delete(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: discoveryCRB}})
		sw := &seaweedv1.Seaweed{}
		if k8sClient.Get(ctx, named(seaweedName), sw) == nil {
			_ = k8sClient.Delete(ctx, sw)
		}
		utils.DeleteNamespace(ctx, k8sClient, testNamespace)
	})

	It("exchanges a ServiceAccount token for S3 credentials only for the trusted subject", func() {
		By("reading the cluster's service-account issuer")
		clientset, err := utils.GetClientset(restCfg)
		Expect(err).NotTo(HaveOccurred())
		raw, err := clientset.RESTClient().Get().AbsPath("/.well-known/openid-configuration").DoRaw(ctx)
		Expect(err).NotTo(HaveOccurred())
		var discovery struct {
			Issuer string `json:"issuer"`
		}
		Expect(json.Unmarshal(raw, &discovery)).To(Succeed())
		Expect(discovery.Issuer).To(HavePrefix("https://"))
		GinkgoWriter.Printf("service-account issuer: %s\n", discovery.Issuer)

		By("letting SeaweedFS fetch the issuer's discovery document and JWKS without credentials")
		Expect(k8sClient.Create(ctx, &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: discoveryCRB},
			RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "system:service-account-issuer-discovery"},
			Subjects:   []rbacv1.Subject{{Kind: "Group", APIGroup: "rbac.authorization.k8s.io", Name: "system:unauthenticated"}},
		})).To(Succeed())

		By("creating a Seaweed whose filer S3 trusts the cluster CA")
		concurrentStart := true
		sw := &seaweedv1.Seaweed{
			ObjectMeta: metav1.ObjectMeta{Name: seaweedName, Namespace: testNamespace},
			Spec: seaweedv1.SeaweedSpec{
				Image: seaweedImageForWebIdentity(),
				// STS signs its session tokens with jwt.filer_signing.key; without
				// it the filer's embedded S3 starts with no STS at all. It also
				// makes the IAM gRPC service require the operator's admin token.
				SecurityConfig: &seaweedv1.SecurityConfigSpec{
					JWTSigning: &seaweedv1.JWTSigningSpec{FilerWrite: true},
				},
				VolumeServerDiskCount: func() *int32 { v := int32(1); return &v }(),
				Master:                &seaweedv1.MasterSpec{Replicas: 1, ConcurrentStart: &concurrentStart},
				Volume: &seaweedv1.VolumeSpec{
					Replicas: 1,
					VolumeServerConfig: seaweedv1.VolumeServerConfig{
						ResourceRequirements: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("2Gi")},
						},
					},
				},
				Filer: &seaweedv1.FilerSpec{
					Replicas: 1,
					S3:       &seaweedv1.S3Config{Enabled: true},
					ComponentSpec: seaweedv1.ComponentSpec{
						// The issuer serves TLS signed by the cluster CA; add
						// it to the trust Go builds its system pool from.
						Env: []corev1.EnvVar{{Name: "SSL_CERT_DIR", Value: "/etc/ssl/certs:/etc/kube-ca"}},
						Volumes: []corev1.Volume{{
							Name: "kube-ca",
							VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: "kube-root-ca.crt"},
							}},
						}},
						VolumeMounts: []corev1.VolumeMount{{Name: "kube-ca", MountPath: "/etc/kube-ca", ReadOnly: true}},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, sw)).To(Succeed())
		utils.WaitForSeaweedReady(ctx, k8sClient, named(seaweedName), 7*time.Minute)

		ref := seaweedv1.SeaweedReference{Name: seaweedName}

		By("declaring the bucket, its policy, the provider and the role")
		Expect(k8sClient.Create(ctx, &seaweedv1.Bucket{
			ObjectMeta: metav1.ObjectMeta{Name: bucketName, Namespace: testNamespace},
			Spec:       seaweedv1.BucketSpec{ClusterRef: seaweedv1.BucketClusterRef{Name: seaweedName}},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &seaweedv1.S3Policy{
			ObjectMeta: metav1.ObjectMeta{Name: "wi-rw", Namespace: testNamespace},
			Spec: seaweedv1.S3PolicySpec{
				SeaweedRef: ref,
				Statements: []seaweedv1.S3PolicyStatement{{
					Effect:    seaweedv1.S3PolicyEffectAllow,
					Actions:   []string{"s3:*"},
					Resources: []string{bucketName, bucketName + "/*"},
				}},
			},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &seaweedv1.S3OIDCProvider{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster-issuer", Namespace: testNamespace},
			Spec: seaweedv1.S3OIDCProviderSpec{
				SeaweedRef: ref,
				IssuerURL:  discovery.Issuer,
				ClientIDs:  []string{webIdentityAudience},
			},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &seaweedv1.S3Role{
			ObjectMeta: metav1.ObjectMeta{Name: "writer", Namespace: testNamespace},
			Spec: seaweedv1.S3RoleSpec{
				SeaweedRef: ref,
				WebIdentity: seaweedv1.S3RoleWebIdentity{
					ProviderRef: seaweedv1.S3OIDCProviderRef{Name: "cluster-issuer"},
					Subjects:    []string{fmt.Sprintf("system:serviceaccount:%s:writer", testNamespace)},
				},
				PolicyRefs: []seaweedv1.S3PolicyRef{{Name: "wi-rw"}},
			},
		})).To(Succeed())

		By("waiting for the provider and the role to be Ready")
		var role seaweedv1.S3Role
		Eventually(func(g Gomega) {
			var provider seaweedv1.S3OIDCProvider
			g.Expect(k8sClient.Get(ctx, named("cluster-issuer"), &provider)).To(Succeed())
			g.Expect(provider.Status.Phase).To(Equal(seaweedv1.S3PhaseReady), "provider conditions: %v", provider.Status.Conditions)
			g.Expect(k8sClient.Get(ctx, named("writer"), &role)).To(Succeed())
			g.Expect(role.Status.Phase).To(Equal(seaweedv1.S3PhaseReady), "role conditions: %v", role.Status.Conditions)
		}, 3*time.Minute, 5*time.Second).Should(Succeed())
		Expect(role.Status.RoleArn).To(Equal("arn:aws:iam::role/writer"))

		endpoint := fmt.Sprintf("http://%s-filer.%s.svc:%d", seaweedName, testNamespace, seaweedv1.FilerS3Port)
		for _, sa := range []string{"writer", "outsider"} {
			Expect(k8sClient.Create(ctx, &corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{Name: sa, Namespace: testNamespace},
			})).To(Succeed())
		}

		run := func(sa, expect string) {
			expiry := int64(3600)
			backoff := int32(4)
			job := &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{Name: "exchange-" + sa, Namespace: testNamespace},
				Spec: batchv1.JobSpec{
					BackoffLimit: &backoff,
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							ServiceAccountName: sa,
							RestartPolicy:      corev1.RestartPolicyNever,
							Containers: []corev1.Container{{
								Name:    "aws",
								Image:   "amazon/aws-cli:2.27.50",
								Command: []string{"/bin/sh", "-c", webIdentityScript},
								Env: []corev1.EnvVar{
									{Name: "EP", Value: endpoint},
									{Name: "ROLE", Value: role.Status.RoleArn},
									{Name: "BUCKET", Value: bucketName},
									{Name: "EXPECT", Value: expect},
									{Name: "AWS_REGION", Value: "us-east-1"},
								},
								VolumeMounts: []corev1.VolumeMount{{Name: "tokens", MountPath: "/var/run/secrets/tokens"}},
							}},
							Volumes: []corev1.Volume{{
								Name: "tokens",
								VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
									Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
										Audience: webIdentityAudience, ExpirationSeconds: &expiry, Path: "s3",
									}}},
								}},
							}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, job)).To(Succeed())
			DeferCleanup(func() {
				if !CurrentSpecReport().Failed() {
					return
				}
				pods, err := clientset.CoreV1().Pods(testNamespace).List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + job.Name})
				if err != nil {
					return
				}
				for _, pod := range pods.Items {
					logs, _ := clientset.CoreV1().Pods(testNamespace).GetLogs(pod.Name, &corev1.PodLogOptions{}).DoRaw(ctx)
					GinkgoWriter.Printf("--- logs of %s:\n%s\n", pod.Name, logs)
				}
			})
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, named(job.Name), job)).To(Succeed())
				g.Expect(job.Status.Failed).To(BeNumerically("<=", backoff), "job %s exhausted its retries", job.Name)
				g.Expect(job.Status.Succeeded).To(BeNumerically("==", 1))
			}, 5*time.Minute, 5*time.Second).Should(Succeed())
		}

		By("the trusted ServiceAccount gets credentials and writes the bucket")
		run("writer", "allow")

		By("another ServiceAccount of the same issuer is refused")
		run("outsider", "deny")
	})
})
