package controller

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/grpc"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// testTLSSecret builds a Secret shaped like the cert-manager output the
// operator reads: ca.crt/tls.crt/tls.key under TLSServerSecretName(sw). A
// single self-signed certificate serves as both CA and leaf — enough for
// loadSeaweedGrpcDialOption to parse and build credentials from.
func testTLSSecret(t *testing.T, sw *seaweedv1.Seaweed) *corev1.Secret {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test.seaweedfs"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: TLSServerSecretName(sw), Namespace: sw.Namespace},
		Type:       corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"ca.crt":  certPEM,
			"tls.crt": certPEM,
			"tls.key": keyPEM,
		},
	}
}

func TestLoadSeaweedGrpcDialOption(t *testing.T) {
	scheme := iamTestScheme(t)
	swTLS := newTestSeaweedWithFiler()
	swTLS.Spec.TLS = &seaweedv1.TLSSpec{Enabled: true}
	secret := testTLSSecret(t, swTLS)

	t.Run("tls disabled", func(t *testing.T) {
		sw := newTestSeaweedWithFiler()
		cli := iamTestClient(t, scheme, sw)
		opt, fp, err := loadSeaweedGrpcDialOption(context.Background(), cli, sw)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if opt != nil || fp != "" {
			t.Fatalf("expected no dial option for TLS-disabled cluster, got %v / %q", opt, fp)
		}
	})

	t.Run("tls enabled but secret missing", func(t *testing.T) {
		cli := iamTestClient(t, scheme, swTLS)
		opt, fp, err := loadSeaweedGrpcDialOption(context.Background(), cli, swTLS)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if opt != nil || fp != "" {
			t.Fatalf("expected plaintext fallback while the server TLS Secret is absent, got %v / %q", opt, fp)
		}
	})

	t.Run("tls enabled with incomplete secret", func(t *testing.T) {
		partial := secret.DeepCopy()
		delete(partial.Data, "ca.crt")
		cli := iamTestClient(t, scheme, swTLS, partial)
		opt, fp, err := loadSeaweedGrpcDialOption(context.Background(), cli, swTLS)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if opt != nil || fp != "" {
			t.Fatalf("expected plaintext fallback for incomplete Secret, got %v / %q", opt, fp)
		}
	})

	t.Run("tls enabled with issued secret", func(t *testing.T) {
		cli := iamTestClient(t, scheme, swTLS, secret)
		opt, fp, err := loadSeaweedGrpcDialOption(context.Background(), cli, swTLS)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if opt == nil {
			t.Fatal("expected a TLS dial option")
		}
		if fp == "" {
			t.Fatal("expected a TLS material fingerprint")
		}
	})
}

// TestS3Credentials_PassesTLSDialOptionToFactory pins the fix for IAM
// reconciliation against a cluster with [grpc] mTLS: the reconciler must read
// the cluster's server TLS Secret and hand transport credentials to the
// IAMAdminFactory. Without them every IAM gRPC call dialed insecure and
// failed the moment spec.tls was enabled.
func TestS3Credentials_PassesTLSDialOptionToFactory(t *testing.T) {
	scheme := iamTestScheme(t)
	sw := newTestSeaweedWithFiler()
	sw.Spec.TLS = &seaweedv1.TLSSpec{Enabled: true}
	cred := &seaweedv1.S3Credentials{
		ObjectMeta: metav1.ObjectMeta{Name: "alice-creds", Namespace: "media"},
		Spec: seaweedv1.S3CredentialsSpec{
			SeaweedRef:  iamSeaweedRef(),
			IdentityRef: seaweedv1.S3IdentityRef{Name: "alice"},
			SecretRef:   seaweedv1.S3SecretRef{Name: "alice-secret"},
		},
	}
	cli := iamTestClient(t, scheme, sw, testTLSSecret(t, sw), cred)
	fa := newFakeIAMAdmin()
	fa.seedUser("alice")
	r := &S3CredentialsReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	var gotDialOption grpc.DialOption
	r.AdminFactory = func(_ string, _ []byte, dialOption grpc.DialOption, _ logr.Logger) (IAMAdmin, error) {
		gotDialOption = dialOption
		return fa, nil
	}

	key := types.NamespacedName{Namespace: "media", Name: "alice-creds"}
	reconcileStable(t, r, key, 5)

	if gotDialOption == nil {
		t.Fatal("IAMAdminFactory received no dial option for a TLS-enabled cluster")
	}
}

// TestBucketReconcile_PassesTLSDialOptionToFactory is the bucket-side twin:
// `weed shell` and IAM grant calls must also dial with the cluster's mTLS
// credentials.
func TestBucketReconcile_PassesTLSDialOptionToFactory(t *testing.T) {
	sw := newTestSeaweedWithFiler()
	sw.Spec.TLS = &seaweedv1.TLSSpec{Enabled: true}
	bucket := newTestBucket("photos")
	bucket.Finalizers = []string{BucketFinalizer}

	fa := newFakeAdmin()
	var gotDialOption grpc.DialOption
	r, _ := testReconcilerWithFactory(t, func(_, _ string, _ []byte, dialOption grpc.DialOption, _ logr.Logger) (BucketAdmin, error) {
		gotDialOption = dialOption
		return fa, nil
	}, sw, testTLSSecret(t, sw), bucket)

	key := types.NamespacedName{Namespace: bucket.Namespace, Name: bucket.Name}
	reconcileUntilStable(t, r, key, 5)

	if gotDialOption == nil {
		t.Fatal("BucketAdminFactory received no dial option for a TLS-enabled cluster")
	}
}
