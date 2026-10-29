package controller

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
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

// With a spec.tls.source volume the cluster never has an operator-managed
// Secret; the operator's own credentials come from SEAWEED_TLS_DIR, re-read
// per call so rotated material is picked up.
func TestLoadSeaweedGrpcDialOption_VolumeSource(t *testing.T) {
	scheme := iamTestScheme(t)
	newSw := func(files *seaweedv1.TLSFileNames) *seaweedv1.Seaweed {
		sw := newTestSeaweedWithFiler()
		sw.Spec.TLS = &seaweedv1.TLSSpec{
			Enabled: true,
			Source: &seaweedv1.TLSSource{
				Volume: &corev1.VolumeSource{CSI: &corev1.CSIVolumeSource{Driver: "csi.spiffe.io"}},
				Files:  files,
			},
		}
		return sw
	}
	writeDir := func(t *testing.T, data map[string][]byte, names [3]string) string {
		dir := t.TempDir()
		for i, name := range names {
			key := []string{"ca.crt", "tls.crt", "tls.key"}[i]
			if err := os.WriteFile(filepath.Join(dir, name), data[key], 0o600); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
		}
		return dir
	}

	t.Run("unset dir reports unavailable material", func(t *testing.T) {
		sw := newSw(nil)
		cli := iamTestClient(t, scheme, sw)
		_, _, err := loadSeaweedGrpcDialOption(context.Background(), cli, sw)
		if !errors.Is(err, errOperatorTLSUnavailable) {
			t.Fatalf("expected errOperatorTLSUnavailable, got %v", err)
		}
	})

	t.Run("missing files report unavailable material", func(t *testing.T) {
		sw := newSw(nil)
		t.Setenv(operatorTLSDirEnv, t.TempDir())
		cli := iamTestClient(t, scheme, sw)
		_, _, err := loadSeaweedGrpcDialOption(context.Background(), cli, sw)
		if !errors.Is(err, errOperatorTLSUnavailable) {
			t.Fatalf("expected errOperatorTLSUnavailable, got %v", err)
		}
	})

	t.Run("dir with default names dials TLS", func(t *testing.T) {
		sw := newSw(nil)
		data := testTLSSecret(t, sw).Data
		t.Setenv(operatorTLSDirEnv, writeDir(t, data, [3]string{"ca.crt", "tls.crt", "tls.key"}))
		cli := iamTestClient(t, scheme, sw)
		opt, fp, err := loadSeaweedGrpcDialOption(context.Background(), cli, sw)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if opt == nil || fp == "" {
			t.Fatalf("expected dial option and fingerprint, got %v / %q", opt, fp)
		}
	})

	t.Run("dir honoring source.files names", func(t *testing.T) {
		sw := newSw(&seaweedv1.TLSFileNames{CA: "bundle.pem", Cert: "svid.pem", Key: "svid_key.pem"})
		data := testTLSSecret(t, sw).Data
		t.Setenv(operatorTLSDirEnv, writeDir(t, data, [3]string{"bundle.pem", "svid.pem", "svid_key.pem"}))
		cli := iamTestClient(t, scheme, sw)
		opt, _, err := loadSeaweedGrpcDialOption(context.Background(), cli, sw)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if opt == nil {
			t.Fatal("expected a TLS dial option")
		}
	})
}

// TestExpectedServerPeer pins the leaf-identity check the volume-sourced dial
// adds on top of chain verification: on a shared CA any workload it issues
// could otherwise impersonate the filer.
func TestExpectedServerPeer(t *testing.T) {
	spiffe := func(s string) *url.URL {
		u, err := url.Parse(s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		return u
	}
	swWith := func(tls *seaweedv1.TLSSpec) *seaweedv1.Seaweed {
		return &seaweedv1.Seaweed{
			ObjectMeta: metav1.ObjectMeta{Name: "seaweed", Namespace: "media"},
			Spec:       seaweedv1.SeaweedSpec{TLS: tls},
		}
	}

	t.Run("nil spec passes no constraint", func(t *testing.T) {
		if expectedServerPeer(swWith(nil)) != nil {
			t.Fatal("expected nil verifier")
		}
	})

	t.Run("wildcard domain matches leaf CN", func(t *testing.T) {
		verify := expectedServerPeer(swWith(&seaweedv1.TLSSpec{AllowedWildcardDomain: ".e2e.test"}))
		if err := verify(&x509.Certificate{Subject: pkix.Name{CommonName: "seaweed.e2e.test"}}); err != nil {
			t.Fatalf("expected pass, got %v", err)
		}
		if err := verify(&x509.Certificate{Subject: pkix.Name{CommonName: "rogue.other.test"}}); err == nil {
			t.Fatal("expected rejection for a CN outside the domain")
		}
	})

	t.Run("wildcard domain matches spiffe trust domain", func(t *testing.T) {
		verify := expectedServerPeer(swWith(&seaweedv1.TLSSpec{AllowedWildcardDomain: "cluster.local"}))
		leaf := &x509.Certificate{URIs: []*url.URL{spiffe("spiffe://cluster.local/ns/media/sa/filer")}}
		if err := verify(leaf); err != nil {
			t.Fatalf("expected pass, got %v", err)
		}
		leaf.URIs = []*url.URL{spiffe("spiffe://other.domain/ns/media/sa/filer")}
		if err := verify(leaf); err == nil {
			t.Fatal("expected rejection for another trust domain")
		}
	})

	t.Run("allowed common names match CN and URI SANs", func(t *testing.T) {
		verify := expectedServerPeer(swWith(&seaweedv1.TLSSpec{AllowedCommonNames: &seaweedv1.TLSAllowedCommonNames{
			Filer: []string{"filer.svid"},
		}}))
		if err := verify(&x509.Certificate{Subject: pkix.Name{CommonName: "filer.svid"}}); err != nil {
			t.Fatalf("expected pass, got %v", err)
		}
		leaf := &x509.Certificate{URIs: []*url.URL{spiffe("spiffe://td/ns/d/sa/filer")}}
		if err := verify(leaf); err == nil {
			t.Fatal("expected rejection for an undeclared identity")
		}
	})

	t.Run("cluster service DNS names are accepted", func(t *testing.T) {
		verify := expectedServerPeer(swWith(&seaweedv1.TLSSpec{AllowedWildcardDomain: ".example.org"}))
		for _, dns := range []string{
			"seaweed-filer.media",
			"seaweed-filer.media.svc",
			"seaweed-filer.media.svc.cluster.local",
			"seaweed-filer-0.seaweed-filer-peer.media.svc.cluster.local",
		} {
			if err := verify(&x509.Certificate{DNSNames: []string{dns}}); err != nil {
				t.Fatalf("expected pass for %q, got %v", dns, err)
			}
		}
		for _, dns := range []string{
			"seaweed-filer",
			"seaweed-filer.other",
			"seaweed-filer.other.svc.cluster.local",
			"other-filer.media",
		} {
			if err := verify(&x509.Certificate{DNSNames: []string{dns}}); err == nil {
				t.Fatalf("expected rejection for %q", dns)
			}
		}
	})

	t.Run("peer authorization changes the fingerprint", func(t *testing.T) {
		base := peerAuthFingerprint(&seaweedv1.TLSSpec{AllowedWildcardDomain: ".a"})
		if peerAuthFingerprint(&seaweedv1.TLSSpec{AllowedWildcardDomain: ".b"}) == base {
			t.Fatal("wildcard domain not covered by the fingerprint")
		}
		if peerAuthFingerprint(&seaweedv1.TLSSpec{AllowedCommonNames: &seaweedv1.TLSAllowedCommonNames{Filer: []string{"x"}}}) == base {
			t.Fatal("common names not covered by the fingerprint")
		}
		if peerAuthFingerprint(nil) != "" {
			t.Fatal("expected empty fingerprint for nil tls")
		}
	})
}

// TestExpectedServerPeerFor pins the per-target hardening: a leaf attributable
// to a different component is rejected on the dialed endpoint even though it
// sits inside the declared trust boundary, while unattributed leaves keep the
// union behavior so shared-certificate deployments are unaffected.
func TestExpectedServerPeerFor(t *testing.T) {
	spiffe := func(s string) *url.URL {
		u, err := url.Parse(s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		return u
	}
	sw := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "seaweed", Namespace: "media"},
		Spec: seaweedv1.SeaweedSpec{TLS: &seaweedv1.TLSSpec{
			AllowedWildcardDomain: ".e2e.test",
			AllowedCommonNames: &seaweedv1.TLSAllowedCommonNames{
				Master: []string{"master-clients.svid"},
				Filer:  []string{"filer-clients.svid"},
			},
		}},
	}
	verify := expectedServerPeerFor(sw)
	filerAuth := "seaweed-filer.media:8888"
	masterAuth := "seaweed-master-0.seaweed-master-peer.media:9333"

	t.Run("nil spec passes no constraint", func(t *testing.T) {
		if expectedServerPeerFor(&seaweedv1.Seaweed{}) != nil {
			t.Fatal("expected nil verifier")
		}
	})

	t.Run("matching service identity passes on its own endpoint", func(t *testing.T) {
		leaf := &x509.Certificate{DNSNames: []string{"seaweed-filer.media.svc.cluster.local"}}
		if err := verify(filerAuth, leaf); err != nil {
			t.Fatalf("filer cert on filer dial: %v", err)
		}
	})

	t.Run("wrong-component service identity is rejected", func(t *testing.T) {
		leaf := &x509.Certificate{DNSNames: []string{"seaweed-master.media.svc.cluster.local"}}
		if err := verify(filerAuth, leaf); err == nil {
			t.Fatal("master cert accepted on the filer dial")
		}
		if err := verify(masterAuth, leaf); err != nil {
			t.Fatalf("master cert on master dial: %v", err)
		}
	})

	t.Run("declared names carry no component attribution", func(t *testing.T) {
		// allowedCommonNames.master lists callers the master accepts; a filer
		// certificate declared there keeps union acceptance on every dial.
		leaf := &x509.Certificate{Subject: pkix.Name{CommonName: "master-clients.svid"}}
		if err := verify(filerAuth, leaf); err != nil {
			t.Fatalf("master-declared CN on filer dial: %v", err)
		}
		if err := verify(masterAuth, leaf); err != nil {
			t.Fatalf("master-declared CN on master dial: %v", err)
		}
	})

	t.Run("domain names attribute by service prefix", func(t *testing.T) {
		if err := verify(filerAuth, &x509.Certificate{Subject: pkix.Name{CommonName: "seaweed-filer.e2e.test"}}); err != nil {
			t.Fatalf("filer domain CN on filer dial: %v", err)
		}
		if err := verify(filerAuth, &x509.Certificate{Subject: pkix.Name{CommonName: "seaweed-master.e2e.test"}}); err == nil {
			t.Fatal("master domain CN accepted on the filer dial")
		}
	})

	t.Run("spiffe svid attributes by path segment", func(t *testing.T) {
		filerSVID := &x509.Certificate{URIs: []*url.URL{spiffe("spiffe://e2e.test/ns/media/sa/seaweed-filer")}}
		if err := verify(filerAuth, filerSVID); err != nil {
			t.Fatalf("filer SVID on filer dial: %v", err)
		}
		if err := verify(masterAuth, filerSVID); err == nil {
			t.Fatal("filer SVID accepted on the master dial")
		}
	})

	t.Run("svid namespace segment does not hide the workload identity", func(t *testing.T) {
		leaf := &x509.Certificate{URIs: []*url.URL{spiffe("spiffe://e2e.test/ns/master/sa/seaweed-filer")}}
		if err := verify(filerAuth, leaf); err != nil {
			t.Fatalf("filer SVID behind a master-named namespace on filer dial: %v", err)
		}
		if err := verify(masterAuth, leaf); err == nil {
			t.Fatal("filer SVID accepted on the master dial")
		}
	})

	t.Run("name declared under several components keeps union acceptance", func(t *testing.T) {
		swShared := &seaweedv1.Seaweed{
			ObjectMeta: metav1.ObjectMeta{Name: "seaweed", Namespace: "media"},
			Spec: seaweedv1.SeaweedSpec{TLS: &seaweedv1.TLSSpec{
				AllowedWildcardDomain: ".e2e.test",
				AllowedCommonNames: &seaweedv1.TLSAllowedCommonNames{
					Master: []string{"shared-clients.svid"},
					Filer:  []string{"shared-clients.svid"},
				},
			}},
		}
		sharedVerify := expectedServerPeerFor(swShared)
		leaf := &x509.Certificate{Subject: pkix.Name{CommonName: "shared-clients.svid"}}
		for _, authority := range []string{filerAuth, masterAuth} {
			if err := sharedVerify(authority, leaf); err != nil {
				t.Fatalf("shared declared CN rejected on %s: %v", authority, err)
			}
		}
	})

	t.Run("unattributed leaf keeps the union check on a known endpoint", func(t *testing.T) {
		leaf := &x509.Certificate{Subject: pkix.Name{CommonName: "shared.e2e.test"}}
		if err := verify(filerAuth, leaf); err != nil {
			t.Fatalf("shared domain cert on filer dial: %v", err)
		}
	})

	t.Run("unresolvable authority keeps the union check", func(t *testing.T) {
		leaf := &x509.Certificate{DNSNames: []string{"seaweed-filer.media.svc.cluster.local"}}
		if err := verify("10.9.8.7:8080", leaf); err != nil {
			t.Fatalf("filer cert on unmapped dial: %v", err)
		}
	})

	t.Run("unauthorized leaf still fails", func(t *testing.T) {
		leaf := &x509.Certificate{Subject: pkix.Name{CommonName: "rogue.other.test"}}
		if err := verify(filerAuth, leaf); err == nil {
			t.Fatal("unauthorized cert accepted")
		}
	})
}
