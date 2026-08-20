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
			name: "no jwt no tls",
			spec: seaweedv1.SeaweedSpec{Master: &seaweedv1.MasterSpec{Replicas: 1}},
			want: false,
		},
		{
			// A filer in spec used to force security.toml into existence, which
			// meant [jwt.filer_signing] was on for every cluster with no way to
			// turn it off.
			name: "filer alone does not trigger config",
			spec: seaweedv1.SeaweedSpec{
				Master: &seaweedv1.MasterSpec{Replicas: 1},
				Filer:  &seaweedv1.FilerSpec{},
			},
			want: false,
		},
		{
			name: "admin alone does not trigger config",
			spec: seaweedv1.SeaweedSpec{
				Master: &seaweedv1.MasterSpec{Replicas: 1},
				Admin:  &seaweedv1.AdminSpec{},
			},
			want: false,
		},
		{
			name: "tls always triggers config",
			spec: seaweedv1.SeaweedSpec{
				Master: &seaweedv1.MasterSpec{Replicas: 1},
				TLS:    &seaweedv1.TLSSpec{Enabled: true},
			},
			want: true,
		},
		{
			name: "filer write signing triggers config",
			spec: seaweedv1.SeaweedSpec{
				Master: &seaweedv1.MasterSpec{Replicas: 1},
				Filer:  &seaweedv1.FilerSpec{},
				SecurityConfig: &seaweedv1.SecurityConfigSpec{
					JWTSigning: &seaweedv1.JWTSigningSpec{FilerWrite: true},
				},
			},
			want: true,
		},
		{
			name: "volume write signing triggers config without a filer",
			spec: seaweedv1.SeaweedSpec{
				Master: &seaweedv1.MasterSpec{Replicas: 1},
				SecurityConfig: &seaweedv1.SecurityConfigSpec{
					JWTSigning: &seaweedv1.JWTSigningSpec{VolumeWrite: true},
				},
			},
			want: true,
		},
		{
			name: "all flags off does not trigger config",
			spec: seaweedv1.SeaweedSpec{
				Master: &seaweedv1.MasterSpec{Replicas: 1},
				Filer:  &seaweedv1.FilerSpec{},
				SecurityConfig: &seaweedv1.SecurityConfigSpec{
					JWTSigning: &seaweedv1.JWTSigningSpec{},
				},
			},
			want: false,
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
	allKeys := jwtSigningKeys{
		volumeWrite: "vw-key",
		volumeRead:  "vr-key",
		filerWrite:  "fw-key",
		filerRead:   "fr-key",
	}

	t.Run("nothing enabled emits no jwt section", func(t *testing.T) {
		got := renderSecurityTOML(seaweedv1.JWTSigningSpec{}, allKeys, false)
		if strings.Contains(got, "[jwt") {
			t.Errorf("expected no [jwt.*] sections, got %q", got)
		}
		for _, key := range []string{"vw-key", "vr-key", "fw-key", "fr-key"} {
			if strings.Contains(got, key) {
				t.Errorf("expected no key material for disabled sections, got %q", got)
			}
		}
	})

	sections := []struct {
		name    string
		cfg     seaweedv1.JWTSigningSpec
		section string
		key     string
	}{
		{"volumeWrite", seaweedv1.JWTSigningSpec{VolumeWrite: true}, "[jwt.signing]", "vw-key"},
		{"volumeRead", seaweedv1.JWTSigningSpec{VolumeRead: true}, "[jwt.signing.read]", "vr-key"},
		{"filerWrite", seaweedv1.JWTSigningSpec{FilerWrite: true}, "[jwt.filer_signing]", "fw-key"},
		{"filerRead", seaweedv1.JWTSigningSpec{FilerRead: true}, "[jwt.filer_signing.read]", "fr-key"},
	}
	for _, tc := range sections {
		t.Run(tc.name+" renders only its own section", func(t *testing.T) {
			got := renderSecurityTOML(tc.cfg, allKeys, false)
			if !strings.Contains(got, tc.section+"\nkey = \""+tc.key+"\"") {
				t.Errorf("expected %s with key %q, got %q", tc.section, tc.key, got)
			}
			for _, other := range sections {
				if other.name == tc.name {
					continue
				}
				if strings.Contains(got, other.key) {
					t.Errorf("expected %s alone, but %s key leaked in: %q", tc.section, other.section, got)
				}
			}
			if strings.Contains(got, "expires_after_seconds") {
				t.Errorf("expected no expiry override without one configured, got %q", got)
			}
		})
	}

	t.Run("sub-section headers do not swallow their parent", func(t *testing.T) {
		// [jwt.signing] and [jwt.signing.read] share a prefix; both must be
		// emitted with their own key when both are on.
		got := renderSecurityTOML(seaweedv1.JWTSigningSpec{VolumeWrite: true, VolumeRead: true}, allKeys, false)
		if !strings.Contains(got, "[jwt.signing]\nkey = \"vw-key\"") || !strings.Contains(got, "[jwt.signing.read]\nkey = \"vr-key\"") {
			t.Errorf("expected both volume sections with their own keys, got %q", got)
		}
	})

	t.Run("only positive expiry overrides are written", func(t *testing.T) {
		cfg := seaweedv1.JWTSigningSpec{
			VolumeWrite: true,
			FilerWrite:  true,
			ExpiresAfterSeconds: &seaweedv1.JWTExpiresAfterSecondsSpec{
				VolumeWrite: 30,
				FilerWrite:  0,
			},
		}
		got := renderSecurityTOML(cfg, allKeys, false)
		if !strings.Contains(got, "[jwt.signing]\nkey = \"vw-key\"\nexpires_after_seconds = 30\n") {
			t.Errorf("expected volume write expiry override, got %q", got)
		}
		if strings.Count(got, "expires_after_seconds") != 1 {
			t.Errorf("expected exactly one expiry override (zero keeps weed defaults), got %q", got)
		}
	})

	t.Run("with TLS emits grpc sections", func(t *testing.T) {
		got := renderSecurityTOML(seaweedv1.JWTSigningSpec{FilerWrite: true}, allKeys, true)
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

	t.Run("TLS alone emits no jwt section", func(t *testing.T) {
		// mTLS between components says nothing about whether the cluster wants
		// JWTs; enabling one must not silently enable the other.
		got := renderSecurityTOML(seaweedv1.JWTSigningSpec{}, allKeys, true)
		if strings.Contains(got, "[jwt") {
			t.Errorf("expected no [jwt.*] sections for a TLS-only cluster, got %q", got)
		}
		if !strings.Contains(got, "[grpc]") {
			t.Errorf("expected [grpc] section, got %q", got)
		}
	})
}

// The rendered file has to survive a round trip through the parser that
// preserves keys across reconciles, including for same-prefix sections.
func TestParseJWTSigningKeysRoundTrip(t *testing.T) {
	want := jwtSigningKeys{
		volumeWrite: "vw-key",
		volumeRead:  "vr-key",
		filerWrite:  "fw-key",
		filerRead:   "fr-key",
	}
	cfg := seaweedv1.JWTSigningSpec{VolumeWrite: true, VolumeRead: true, FilerWrite: true, FilerRead: true}
	if got := parseJWTSigningKeys(renderSecurityTOML(cfg, want, true)); got != want {
		t.Errorf("parseJWTSigningKeys round trip = %+v, want %+v", got, want)
	}
}

// Once nothing needs security.toml any more, the Secret has to go: it holds
// HMAC keys, and a lingering copy still listing [jwt.filer_signing] reads as
// if the cluster enforced it.
func TestEnsureSecurityConfig_PrunesSecretWhenNoLongerNeeded(t *testing.T) {
	m := newSecurityTestSeaweed()
	r := securityTestReconciler(t, m)
	ctx := context.Background()
	key := client.ObjectKey{Name: SecurityConfigSecretName(m), Namespace: m.Namespace}

	if _, _, err := r.ensureSecurityConfig(ctx, m); err != nil {
		t.Fatalf("ensureSecurityConfig with filerWrite on: %v", err)
	}
	if err := r.Get(ctx, key, &corev1.Secret{}); err != nil {
		t.Fatalf("expected the Secret to exist while filerWrite is on: %v", err)
	}

	m.Spec.SecurityConfig = nil
	if _, _, err := r.ensureSecurityConfig(ctx, m); err != nil {
		t.Fatalf("ensureSecurityConfig after turning signing off: %v", err)
	}
	if err := r.Get(ctx, key, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Errorf("expected the security Secret to be deleted, got err=%v", err)
	}
}

// A Secret occupying the generated name that this CR does not control is
// somebody else's; the operator must not delete it.
func TestEnsureSecurityConfig_LeavesUnownedSecretAlone(t *testing.T) {
	m := newSecurityTestSeaweed()
	m.Spec.SecurityConfig = nil
	foreign := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: SecurityConfigSecretName(m), Namespace: m.Namespace},
		Data:       map[string][]byte{"security.toml": []byte("not ours")},
	}
	r := securityTestReconciler(t, m, foreign)

	if _, _, err := r.ensureSecurityConfig(context.Background(), m); err != nil {
		t.Fatalf("ensureSecurityConfig: %v", err)
	}
	if err := r.Get(context.Background(), client.ObjectKey{Name: foreign.Name, Namespace: foreign.Namespace}, &corev1.Secret{}); err != nil {
		t.Errorf("expected an unowned Secret to be left alone, got err=%v", err)
	}
}

// Toggling a JWT section has to reach the running pods. weed reads
// security.toml at startup, and nothing else in the pod template changes when
// a section is added to a cluster that already mounts the file, so the
// annotation is what makes the StatefulSet roll.
func TestJWTSigningRevisionAnnotation(t *testing.T) {
	seaweed := func(cfg *seaweedv1.JWTSigningSpec, tls bool) *seaweedv1.Seaweed {
		m := &seaweedv1.Seaweed{
			ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
			Spec:       seaweedv1.SeaweedSpec{Master: &seaweedv1.MasterSpec{Replicas: 1}},
		}
		if cfg != nil {
			m.Spec.SecurityConfig = &seaweedv1.SecurityConfigSpec{JWTSigning: cfg}
		}
		if tls {
			m.Spec.TLS = &seaweedv1.TLSSpec{Enabled: true}
		}
		return m
	}

	t.Run("absent when no security.toml is mounted", func(t *testing.T) {
		got := withJWTSigningAnnotation(seaweed(nil, false), map[string]string{"user": "kept"})
		if _, ok := got[jwtSigningAnnotation]; ok {
			t.Errorf("expected no annotation without a security.toml mount, got %v", got)
		}
		if got["user"] != "kept" {
			t.Errorf("expected user annotations preserved, got %v", got)
		}
	})

	t.Run("names the enabled sections", func(t *testing.T) {
		m := seaweed(&seaweedv1.JWTSigningSpec{
			VolumeWrite:         true,
			FilerWrite:          true,
			ExpiresAfterSeconds: &seaweedv1.JWTExpiresAfterSecondsSpec{VolumeWrite: 30},
		}, false)
		got := withJWTSigningAnnotation(m, map[string]string{"user": "kept"})
		if want := "volumeWrite=30,filerWrite"; got[jwtSigningAnnotation] != want {
			t.Errorf("annotation = %q, want %q", got[jwtSigningAnnotation], want)
		}
		if got["user"] != "kept" {
			t.Errorf("expected user annotations preserved, got %v", got)
		}
	})

	t.Run("a TLS-only cluster is still marked", func(t *testing.T) {
		// Without this, a TLS cluster upgrading from an operator that always
		// wrote [jwt.filer_signing] would keep the old key in memory: the
		// Secret changes but no pod field does, so nothing restarts.
		got := withJWTSigningAnnotation(seaweed(nil, true), nil)
		if got[jwtSigningAnnotation] != "none" {
			t.Errorf("annotation = %q, want %q", got[jwtSigningAnnotation], "none")
		}
	})

	t.Run("changing a flag changes the value", func(t *testing.T) {
		before := jwtSigningRevision(seaweed(&seaweedv1.JWTSigningSpec{FilerWrite: true}, false))
		after := jwtSigningRevision(seaweed(&seaweedv1.JWTSigningSpec{FilerWrite: true, VolumeWrite: true}, false))
		if before == after {
			t.Errorf("expected the revision to change when a section is added, got %q twice", before)
		}
	})

	t.Run("stable across identical specs", func(t *testing.T) {
		cfg := &seaweedv1.JWTSigningSpec{VolumeRead: true, FilerRead: true}
		if a, b := jwtSigningRevision(seaweed(cfg, false)), jwtSigningRevision(seaweed(cfg, true)); a != b {
			t.Errorf("revision must not depend on anything but the jwt flags: %q vs %q", a, b)
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
		Spec: seaweedv1.SeaweedSpec{
			Filer: &seaweedv1.FilerSpec{Replicas: 1},
			SecurityConfig: &seaweedv1.SecurityConfigSpec{
				JWTSigning: &seaweedv1.JWTSigningSpec{FilerWrite: true},
			},
		},
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

// Every enabled section gets its own key: reusing one key across sections
// would let a token minted for one guard pass another.
func TestEnsureSecuritySecret_DistinctKeyPerSection(t *testing.T) {
	m := newSecurityTestSeaweed()
	m.Spec.SecurityConfig.JWTSigning = &seaweedv1.JWTSigningSpec{
		VolumeWrite: true, VolumeRead: true, FilerWrite: true, FilerRead: true,
	}
	r := securityTestReconciler(t, m)

	if _, _, err := r.ensureSecuritySecret(context.Background(), m); err != nil {
		t.Fatalf("ensureSecuritySecret: %v", err)
	}
	secret := &corev1.Secret{}
	if err := r.Get(context.Background(), client.ObjectKey{Name: SecurityConfigSecretName(m), Namespace: m.Namespace}, secret); err != nil {
		t.Fatalf("get security Secret: %v", err)
	}

	keys := parseJWTSigningKeys(string(secret.Data["security.toml"]))
	seen := map[string]string{}
	for _, s := range jwtSections {
		got := *s.key(&keys)
		if got == "" {
			t.Errorf("section %s: expected a generated key, got none", s.name)
			continue
		}
		if other, dup := seen[got]; dup {
			t.Errorf("section %s reuses the key of %s", s.name, other)
		}
		seen[got] = s.name
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
	if !strings.Contains(got, `key = "legacy-write"`) {
		t.Errorf("expected legacy write key preserved, got %q", got)
	}
	// The legacy read key is dropped along with its section: this CR only asks
	// for filerWrite, and the operator never renders a section nobody enabled.
	if strings.Contains(got, `key = "legacy-read"`) || strings.Contains(got, "[jwt.filer_signing.read]") {
		t.Errorf("expected legacy read key dropped, got %q", got)
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
