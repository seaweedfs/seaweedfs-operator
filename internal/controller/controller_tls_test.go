package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func TestSecurityConfigNeeded(t *testing.T) {
	cases := []struct {
		name string
		spec seaweedv1.SeaweedSpec
		want bool
	}{
		{
			name: "no filer no admin no tls",
			spec: seaweedv1.SeaweedSpec{Master: &seaweedv1.MasterSpec{Replicas: 1}},
			want: false,
		},
		{
			name: "filer only triggers config",
			spec: seaweedv1.SeaweedSpec{
				Master: &seaweedv1.MasterSpec{Replicas: 1},
				Filer:  &seaweedv1.FilerSpec{},
			},
			want: true,
		},
		{
			name: "admin only triggers config",
			spec: seaweedv1.SeaweedSpec{
				Master: &seaweedv1.MasterSpec{Replicas: 1},
				Admin:  &seaweedv1.AdminSpec{},
			},
			want: true,
		},
		{
			name: "tls always triggers config",
			spec: seaweedv1.SeaweedSpec{
				Master: &seaweedv1.MasterSpec{Replicas: 1},
				TLS:    &seaweedv1.TLSSpec{Enabled: true},
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
				Spec:       tc.spec,
			}
			if got := securityConfigNeeded(m); got != tc.want {
				t.Errorf("securityConfigNeeded = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRenderSecurityTOML(t *testing.T) {
	t.Run("without TLS only emits jwt sections", func(t *testing.T) {
		got := renderSecurityTOML("write-key", "read-key", false)
		if !strings.Contains(got, "[jwt.filer_signing]") {
			t.Errorf("expected [jwt.filer_signing] section, got %q", got)
		}
		if !strings.Contains(got, `key = "write-key"`) {
			t.Errorf("expected write-key value, got %q", got)
		}
		if strings.Contains(got, "[grpc") {
			t.Errorf("expected no [grpc.*] sections without TLS, got %q", got)
		}
	})

	t.Run("with TLS emits jwt and grpc sections", func(t *testing.T) {
		got := renderSecurityTOML("write-key", "read-key", true)
		if !strings.Contains(got, "[jwt.filer_signing]") {
			t.Errorf("expected [jwt.filer_signing] section, got %q", got)
		}
		if !strings.Contains(got, "[grpc.filer]") {
			t.Errorf("expected [grpc.filer] section with TLS, got %q", got)
		}
		if !strings.Contains(got, tlsMountPath+"/tls.crt") {
			t.Errorf("expected mount path %q in cert refs, got %q", tlsMountPath, got)
		}
	})

	t.Run("never emits volume jwt.signing", func(t *testing.T) {
		// Shipping [jwt.signing] would force volume servers to enforce
		// signed reads/writes; the operator does not wire that up
		// end-to-end. Catch any future regression that re-introduces it.
		for _, withTLS := range []bool{false, true} {
			got := renderSecurityTOML("write-key", "read-key", withTLS)
			if strings.Contains(got, "[jwt.signing]") {
				t.Errorf("withTLS=%v: unexpected [jwt.signing] section, got %q", withTLS, got)
			}
		}
	})
}

func securityTestReconciler(t *testing.T, objs ...client.Object) *SeaweedReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("clientgoscheme: %v", err)
	}
	if err := seaweedv1.AddToScheme(scheme); err != nil {
		t.Fatalf("seaweedv1: %v", err)
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &SeaweedReconciler{Client: cli, Scheme: scheme}
}

func newSecurityTestSeaweed() *seaweedv1.Seaweed {
	return &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec:       seaweedv1.SeaweedSpec{Filer: &seaweedv1.FilerSpec{Replicas: 1}},
	}
}

// ensureSecuritySecret must store security.toml in a Secret — JWT signing keys
// are HMAC credentials and must not land in a ConfigMap.
func TestEnsureSecuritySecret_CreatesSecret(t *testing.T) {
	m := newSecurityTestSeaweed()
	r := securityTestReconciler(t, m)

	if _, _, err := r.ensureSecuritySecret(context.Background(), m); err != nil {
		t.Fatalf("ensureSecuritySecret: %v", err)
	}

	secret := &corev1.Secret{}
	if err := r.Get(context.Background(), client.ObjectKey{Name: SecurityConfigSecretName(m), Namespace: m.Namespace}, secret); err != nil {
		t.Fatalf("expected security Secret to exist: %v", err)
	}
	if !strings.Contains(string(secret.Data["security.toml"]), "[jwt.filer_signing]") {
		t.Errorf("expected jwt.filer_signing section in Secret, got %q", secret.Data["security.toml"])
	}
}

// A second reconcile must reuse the keys already in the Secret rather than
// rotating them, which would invalidate live JWTs.
func TestEnsureSecuritySecret_PreservesKeysAcrossReconcile(t *testing.T) {
	m := newSecurityTestSeaweed()
	r := securityTestReconciler(t, m)

	if _, _, err := r.ensureSecuritySecret(context.Background(), m); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	first := &corev1.Secret{}
	if err := r.Get(context.Background(), client.ObjectKey{Name: SecurityConfigSecretName(m), Namespace: m.Namespace}, first); err != nil {
		t.Fatalf("get after first reconcile: %v", err)
	}

	if _, _, err := r.ensureSecuritySecret(context.Background(), m); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	second := &corev1.Secret{}
	if err := r.Get(context.Background(), client.ObjectKey{Name: SecurityConfigSecretName(m), Namespace: m.Namespace}, second); err != nil {
		t.Fatalf("get after second reconcile: %v", err)
	}

	if string(first.Data["security.toml"]) != string(second.Data["security.toml"]) {
		t.Errorf("security.toml changed across reconciles:\nfirst:  %q\nsecond: %q", first.Data["security.toml"], second.Data["security.toml"])
	}
}

// Upgrading from an operator version that stored security.toml in a ConfigMap
// must migrate the keys into the Secret and delete the legacy ConfigMap.
func TestEnsureSecuritySecret_MigratesLegacyConfigMap(t *testing.T) {
	m := newSecurityTestSeaweed()
	legacyTOML := "[jwt.filer_signing]\nkey = \"legacy-write\"\n\n[jwt.filer_signing.read]\nkey = \"legacy-read\"\n"
	legacy := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: SecurityConfigSecretName(m), Namespace: m.Namespace},
		Data:       map[string]string{"security.toml": legacyTOML},
	}
	r := securityTestReconciler(t, m, legacy)

	if _, _, err := r.ensureSecuritySecret(context.Background(), m); err != nil {
		t.Fatalf("ensureSecuritySecret: %v", err)
	}

	secret := &corev1.Secret{}
	if err := r.Get(context.Background(), client.ObjectKey{Name: SecurityConfigSecretName(m), Namespace: m.Namespace}, secret); err != nil {
		t.Fatalf("expected migrated Secret: %v", err)
	}
	got := string(secret.Data["security.toml"])
	if !strings.Contains(got, `key = "legacy-write"`) || !strings.Contains(got, `key = "legacy-read"`) {
		t.Errorf("expected legacy JWT keys preserved, got %q", got)
	}

	cm := &corev1.ConfigMap{}
	err := r.Get(context.Background(), client.ObjectKey{Name: SecurityConfigSecretName(m), Namespace: m.Namespace}, cm)
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected legacy ConfigMap deleted, got err=%v", err)
	}
}

// Once the Secret holds the keys, a reconcile reads from it and must not issue
// a Delete for the legacy ConfigMap — the migration is a one-time event, not a
// per-reconcile cleanup.
func TestEnsureSecuritySecret_SkipsLegacyDeleteWhenSecretPresent(t *testing.T) {
	m := newSecurityTestSeaweed()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: SecurityConfigSecretName(m), Namespace: m.Namespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"security.toml": []byte("[jwt.filer_signing]\nkey = \"existing\"\n")},
	}
	stray := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: SecurityConfigSecretName(m), Namespace: m.Namespace},
		Data:       map[string]string{"security.toml": "stray"},
	}
	r := securityTestReconciler(t, m, secret, stray)

	if _, _, err := r.ensureSecuritySecret(context.Background(), m); err != nil {
		t.Fatalf("ensureSecuritySecret: %v", err)
	}

	cm := &corev1.ConfigMap{}
	if err := r.Get(context.Background(), client.ObjectKey{Name: SecurityConfigSecretName(m), Namespace: m.Namespace}, cm); err != nil {
		t.Errorf("legacy ConfigMap should be left untouched when the Secret already holds keys, got err=%v", err)
	}
}
