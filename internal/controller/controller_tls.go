/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// TLS reconciliation via cert-manager.
//
// Simpler-than-Helm layout: a single wildcard server Certificate covers
// every component's headless service. All components mount the same
// Secret, and the rendered security.toml points every [grpc.<component>]
// stanza at the shared mount. This reduces 8 per-component Certificates
// in the Helm chart down to one server cert (+ CA chain when using the
// default self-signed issuer).
//
// We talk to cert-manager via unstructured.Unstructured instead of taking
// a hard import dependency on the cert-manager Go module. Pulling in
// cert-manager transitively pulls the AWS SDK, gateway-api, and dozens of
// other modules we do not otherwise need. Using unstructured keeps go.mod
// lean and lets the operator's CRD stay self-contained — the only cost is
// a small amount of map-building boilerplate.
//
// Layout when TLSSpec.IssuerRef is empty (the default):
//
//	Issuer <name>-selfsigned (self-signed)
//	Certificate <name>-ca            → Secret <name>-ca
//	Issuer <name>-ca-issuer          (CA from <name>-ca)
//	Certificate <name>-server        → Secret <name>-server-tls
//	ConfigMap <name>-security-config (security.toml)
//
// When TLSSpec.IssuerRef is set the operator skips the self-signed +
// CA pair and issues <name>-server directly from the user's issuer.
package controller

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

const (
	// tlsMountPath is where every component mounts the shared TLS Secret.
	// security.toml references this exact path for [grpc.<component>]
	// cert/key values.
	tlsMountPath = "/etc/sw-tls"

	// securityConfigMountPath holds the security.toml ConfigMap. Separate
	// from the per-component filer.toml/master.toml path so TLS can be
	// enabled independently of user-supplied component configs.
	securityConfigMountPath = "/etc/sw-security"

	tlsVolumeName      = "sw-tls"
	securityVolumeName = "sw-security"

	tlsSecretSuffix         = "-server-tls"
	caSecretSuffix          = "-ca"
	selfSignedIssuerSuffix  = "-selfsigned"
	caIssuerSuffix          = "-ca-issuer"
	securityConfigMapSuffix = "-security-config"
	serverCertSuffix        = "-server"

	certManagerGroup   = "cert-manager.io"
	certManagerVersion = "v1"
)

var (
	certificateGVK = schema.GroupVersionKind{Group: certManagerGroup, Version: certManagerVersion, Kind: "Certificate"}
	issuerGVK      = schema.GroupVersionKind{Group: certManagerGroup, Version: certManagerVersion, Kind: "Issuer"}
)

// tlsEnabled returns true when TLS is requested on the Seaweed CR.
func tlsEnabled(m *seaweedv1.Seaweed) bool {
	return m.Spec.TLS != nil && m.Spec.TLS.Enabled
}

// TLSServerSecretName is the Secret a component should mount at tlsMountPath.
// Exported so component builders can reference it without re-deriving.
func TLSServerSecretName(m *seaweedv1.Seaweed) string {
	return m.Name + tlsSecretSuffix
}

// SecurityConfigMapName is the ConfigMap holding security.toml.
func SecurityConfigMapName(m *seaweedv1.Seaweed) string {
	return m.Name + securityConfigMapSuffix
}

// ensureTLS is called from the top-level Reconcile before any component is
// reconciled. When TLS.Enabled is false this is a no-op. When true and the
// cert-manager CRDs are missing it logs a warning once and returns nil so
// the rest of reconciliation can still progress.
func (r *SeaweedReconciler) ensureTLS(ctx context.Context, m *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	if !tlsEnabled(m) {
		return ReconcileResult(nil)
	}
	if !r.certManagerCRDAvailable(ctx) {
		return ReconcileResult(nil)
	}

	// Self-signed + CA chain is only needed when the user did not provide
	// an IssuerRef.
	if m.Spec.TLS.IssuerRef == nil {
		if done, res, err := r.ensureSelfSignedIssuer(ctx, m); done {
			return done, res, err
		}
		if done, res, err := r.ensureCACertificate(ctx, m); done {
			return done, res, err
		}
		if done, res, err := r.ensureCAIssuer(ctx, m); done {
			return done, res, err
		}
	}
	if done, res, err := r.ensureServerCertificate(ctx, m); done {
		return done, res, err
	}
	return ReconcileResult(nil)
}

// ensureSecurityConfig provisions the security.toml ConfigMap whenever the
// filer or admin server is in spec, regardless of TLS state. The filer needs
// jwt.filer_signing.key to register the IAM gRPC service the Admin UI Users
// tab calls; the admin needs the same key to sign Bearer tokens. Bundling
// those JWT keys with cert-manager mTLS would force every operator user that
// enables Admin to also pull in cert-manager just to make the Users tab work.
func (r *SeaweedReconciler) ensureSecurityConfig(ctx context.Context, m *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	if !securityConfigNeeded(m) {
		return ReconcileResult(nil)
	}
	return r.ensureSecurityConfigMap(ctx, m)
}

// securityConfigNeeded reports whether the security.toml ConfigMap should
// exist for this CR. True when filer or admin is in spec, or whenever TLS
// is enabled (the [grpc.*] sections live in the same file).
func securityConfigNeeded(m *seaweedv1.Seaweed) bool {
	if tlsEnabled(m) {
		return true
	}
	return m.Spec.Filer != nil || m.Spec.Admin != nil
}

// issuerRefForServerCert picks either the user-supplied issuer or the
// self-signed CA issuer the operator provisioned.
func issuerRefForServerCert(m *seaweedv1.Seaweed) map[string]interface{} {
	if ref := m.Spec.TLS.IssuerRef; ref != nil {
		return map[string]interface{}{
			"name":  ref.Name,
			"kind":  defaultIfEmpty(ref.Kind, "Issuer"),
			"group": defaultIfEmpty(ref.Group, certManagerGroup),
		}
	}
	return map[string]interface{}{
		"name":  m.Name + caIssuerSuffix,
		"kind":  "Issuer",
		"group": certManagerGroup,
	}
}

func defaultIfEmpty(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func newIssuer(name, namespace string, spec map[string]interface{}, labels map[string]string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(issuerGVK)
	u.SetName(name)
	u.SetNamespace(namespace)
	u.SetLabels(labels)
	_ = unstructured.SetNestedMap(u.Object, spec, "spec")
	return u
}

func newCertificate(name, namespace string, spec map[string]interface{}, labels map[string]string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(certificateGVK)
	u.SetName(name)
	u.SetNamespace(namespace)
	u.SetLabels(labels)
	_ = unstructured.SetNestedMap(u.Object, spec, "spec")
	return u
}

func (r *SeaweedReconciler) ensureSelfSignedIssuer(ctx context.Context, m *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	u := newIssuer(m.Name+selfSignedIssuerSuffix, m.Namespace, map[string]interface{}{
		"selfSigned": map[string]interface{}{},
	}, labelsForCR(m))
	return ReconcileResult(r.applyUnstructured(ctx, m, u))
}

func (r *SeaweedReconciler) ensureCACertificate(ctx context.Context, m *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	u := newCertificate(m.Name+caSecretSuffix, m.Namespace, map[string]interface{}{
		"secretName": m.Name + caSecretSuffix,
		"commonName": m.Name + " SeaweedFS CA",
		"isCA":       true,
		"issuerRef": map[string]interface{}{
			"name":  m.Name + selfSignedIssuerSuffix,
			"kind":  "Issuer",
			"group": certManagerGroup,
		},
		"privateKey": map[string]interface{}{
			"algorithm": "RSA",
			"size":      int64(2048),
		},
	}, labelsForCR(m))
	return ReconcileResult(r.applyUnstructured(ctx, m, u))
}

func (r *SeaweedReconciler) ensureCAIssuer(ctx context.Context, m *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	u := newIssuer(m.Name+caIssuerSuffix, m.Namespace, map[string]interface{}{
		"ca": map[string]interface{}{
			"secretName": m.Name + caSecretSuffix,
		},
	}, labelsForCR(m))
	return ReconcileResult(r.applyUnstructured(ctx, m, u))
}

// ensureServerCertificate provisions the single wildcard cert shared by
// every component. DNS names cover every headless service and localhost for
// in-pod probes.
func (r *SeaweedReconciler) ensureServerCertificate(ctx context.Context, m *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	ns := m.Namespace
	name := m.Name
	dnsNames := []interface{}{
		"localhost",
		name + "-master",
		name + "-master-peer",
		name + "-volume",
		name + "-volume-peer",
		name + "-filer",
		name + "-filer-peer",
		name + "-admin",
		name + "-worker",
		fmt.Sprintf("*.%s-master-peer.%s.svc.cluster.local", name, ns),
		fmt.Sprintf("*.%s-volume-peer.%s.svc.cluster.local", name, ns),
		fmt.Sprintf("*.%s-filer-peer.%s.svc.cluster.local", name, ns),
		fmt.Sprintf("*.%s.svc.cluster.local", ns),
	}
	u := newCertificate(name+serverCertSuffix, ns, map[string]interface{}{
		"secretName": TLSServerSecretName(m),
		"commonName": name + ".seaweedfs",
		"dnsNames":   dnsNames,
		"usages": []interface{}{
			"digital signature",
			"key encipherment",
			"server auth",
			"client auth",
		},
		"issuerRef": issuerRefForServerCert(m),
		"privateKey": map[string]interface{}{
			"algorithm": "RSA",
			"size":      int64(2048),
		},
	}, labelsForCR(m))
	return ReconcileResult(r.applyUnstructured(ctx, m, u))
}

// ensureSecurityConfigMap writes the security.toml that every component
// reads via -config_dir. All [grpc.<component>] stanzas point at the single
// shared cert/key pair.
//
// JWT keys are generated once and preserved across reconciliations by
// reading the existing ConfigMap before rewriting it. This avoids silently
// rotating JWT signing keys on every reconcile.
func (r *SeaweedReconciler) ensureSecurityConfigMap(ctx context.Context, m *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	name := SecurityConfigMapName(m)
	existing := &corev1.ConfigMap{}
	err := r.Get(ctx, client.ObjectKey{Name: name, Namespace: m.Namespace}, existing)
	var jwtFilerWrite, jwtFilerRead string
	if err == nil && existing.Data != nil {
		jwtFilerWrite = extractTOMLKey(existing.Data["security.toml"], "jwt.filer_signing", "key")
		jwtFilerRead = extractTOMLKey(existing.Data["security.toml"], "jwt.filer_signing.read", "key")
	} else if err != nil && !apierrors.IsNotFound(err) {
		return ReconcileResult(err)
	}
	if jwtFilerWrite == "" {
		jwtFilerWrite = randKey()
	}
	if jwtFilerRead == "" {
		jwtFilerRead = randKey()
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: m.Namespace,
			Labels:    labelsForCR(m),
		},
		Data: map[string]string{
			"security.toml": renderSecurityTOML(jwtFilerWrite, jwtFilerRead, tlsEffective(m)),
		},
	}
	if err := controllerutil.SetControllerReference(m, cm, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err = r.CreateOrUpdateConfigMap(cm)
	return ReconcileResult(err)
}

// renderSecurityTOML emits the [jwt.filer_signing*] sections always (filer
// needs them to register the IAM gRPC service, admin needs them to sign
// Bearer tokens) and the [grpc.*] mTLS sections only when withTLS is true.
//
// The volume-side [jwt.signing*] sections are intentionally omitted: shipping
// them would make every volume server reject unsigned reads/writes, which
// the operator does not currently wire up end-to-end.
func renderSecurityTOML(jwtFilerWrite, jwtFilerRead string, withTLS bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, `# generated by seaweedfs-operator — do not edit
# this file is read by master, volume server, filer, and admin

[jwt.filer_signing]
key = %q

[jwt.filer_signing.read]
key = %q
`, jwtFilerWrite, jwtFilerRead)

	if !withTLS {
		return b.String()
	}

	fmt.Fprintf(&b, `
# all grpc tls authentications are mutual
[grpc]
ca = "%[1]s/ca.crt"

[grpc.volume]
cert = "%[1]s/tls.crt"
key  = "%[1]s/tls.key"

[grpc.master]
cert = "%[1]s/tls.crt"
key  = "%[1]s/tls.key"

[grpc.filer]
cert = "%[1]s/tls.crt"
key  = "%[1]s/tls.key"

[grpc.admin]
cert = "%[1]s/tls.crt"
key  = "%[1]s/tls.key"

[grpc.worker]
cert = "%[1]s/tls.crt"
key  = "%[1]s/tls.key"

[grpc.client]
cert = "%[1]s/tls.crt"
key  = "%[1]s/tls.key"
`, tlsMountPath)
	return b.String()
}

// extractTOMLKey scans a trivial TOML snippet for a `key = "..."` line under
// a specific [section]. Intentionally dumb: avoids pulling in a full TOML
// parser for a file we wrote ourselves. Returns "" on any parse difficulty.
func extractTOMLKey(toml, section, key string) string {
	if toml == "" {
		return ""
	}
	lines := splitLines(toml)
	inSection := false
	want := "[" + section + "]"
	for _, line := range lines {
		trimmed := trimSpace(line)
		if trimmed == want {
			inSection = true
			continue
		}
		if len(trimmed) > 0 && trimmed[0] == '[' {
			inSection = false
			continue
		}
		if !inSection {
			continue
		}
		if len(trimmed) < len(key)+4 || trimmed[:len(key)] != key {
			continue
		}
		rest := trimSpace(trimmed[len(key):])
		if len(rest) < 3 || rest[0] != '=' {
			continue
		}
		rest = trimSpace(rest[1:])
		if len(rest) >= 2 && rest[0] == '"' && rest[len(rest)-1] == '"' {
			return rest[1 : len(rest)-1]
		}
	}
	return ""
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func trimSpace(s string) string {
	i, j := 0, len(s)
	for i < j && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r') {
		i++
	}
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t' || s[j-1] == '\r') {
		j--
	}
	return s[i:j]
}

// randKey returns a base64 string suitable for a JWT signing secret.
func randKey() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "seaweed-operator-fallback-key"
	}
	return base64.StdEncoding.EncodeToString(b[:])
}

// ------------- cert-manager CRD soft probe -------------

var (
	certManagerCRDOnce      sync.Once
	certManagerCRDAvailable bool
	certManagerCRDMu        sync.RWMutex
)

// certManagerAvailableCached returns the cached probe result without
// running the probe itself. When the probe has not yet run (for example
// a reconcile for a TLS-disabled CR fires before any TLS-enabled CR has
// been seen) this returns false, which is the safe default for pod
// builders: they must NOT reference a Secret or ConfigMap the operator
// has not reconciled yet.
//
// Component builders (helper.go:tlsVolumesAndMounts, tlsConfigDirArg)
// use this instead of taking a reconciler handle so they stay pure
// functions of the CR. ensureTLS is guaranteed to have run probeOnce
// before any builder is called in the same reconcile, so by the time
// the builders look at this, the cached value reflects reality.
func certManagerAvailableCached() bool {
	certManagerCRDMu.RLock()
	defer certManagerCRDMu.RUnlock()
	return certManagerCRDAvailable
}

// certManagerCRDAvailable probes once for the cert-manager Certificate kind.
// Same pattern as serviceMonitorCRDAvailable: missing CRD is non-fatal.
func (r *SeaweedReconciler) certManagerCRDAvailable(ctx context.Context) bool {
	certManagerCRDOnce.Do(func() {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(schema.GroupVersionKind{
			Group: certManagerGroup, Version: certManagerVersion, Kind: "CertificateList",
		})
		err := r.List(ctx, list, &client.ListOptions{Limit: 1})
		available := true
		if err != nil {
			if meta.IsNoMatchError(err) || runtime.IsNotRegisteredError(err) {
				available = false
			} else {
				klog.Warningf("cert-manager CRD probe returned unexpected error (assuming available): %v", err)
			}
		}
		certManagerCRDMu.Lock()
		certManagerCRDAvailable = available
		certManagerCRDMu.Unlock()
		if !available {
			klog.Warningf("cert-manager.io Certificate CRD not found; TLS.Enabled=true on Seaweed CRs will be a no-op. Install cert-manager and restart the seaweed controller.")
		}
	})
	certManagerCRDMu.RLock()
	defer certManagerCRDMu.RUnlock()
	return certManagerCRDAvailable
}

// ------------- upsert plumbing -------------

// applyUnstructured creates or updates an unstructured object, replacing
// only the spec. Labels are kept in sync; metadata generation and status
// are left to cert-manager.
func (r *SeaweedReconciler) applyUnstructured(ctx context.Context, owner *seaweedv1.Seaweed, desired *unstructured.Unstructured) error {
	if err := controllerutil.SetControllerReference(owner, desired, r.Scheme); err != nil {
		return err
	}
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(desired.GroupVersionKind())
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	// Mutate the existing object to avoid stomping on resourceVersion / status.
	desiredSpec, found, err := unstructured.NestedMap(desired.Object, "spec")
	if err != nil || !found {
		return fmt.Errorf("desired object %s/%s has no spec", desired.GetKind(), desired.GetName())
	}
	if err := unstructured.SetNestedMap(existing.Object, desiredSpec, "spec"); err != nil {
		return err
	}
	existing.SetLabels(mergeStringMaps(existing.GetLabels(), desired.GetLabels()))
	// Preserve existing owner references but make sure ours is present.
	if err := controllerutil.SetControllerReference(owner, existing, r.Scheme); err != nil {
		return err
	}
	return r.Update(ctx, existing)
}

func mergeStringMaps(dst, src map[string]string) map[string]string {
	if dst == nil {
		dst = map[string]string{}
	}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// labelsForCR returns the base label set used for every TLS-related resource
// the operator reconciles so `kubectl get ... -l` can find them all.
func labelsForCR(m *seaweedv1.Seaweed) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": "seaweedfs-operator",
		"app.kubernetes.io/name":       "seaweedfs",
		"app.kubernetes.io/instance":   m.Name,
	}
}
