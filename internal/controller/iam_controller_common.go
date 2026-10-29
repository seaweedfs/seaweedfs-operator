/*


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

package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/grpc"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/internal/controller/swadmin"
)

// Finalizers protecting each IAM CR from deletion until the reconciler has
// honored its reclaimPolicy.
const (
	s3IdentityFinalizer      = "seaweed.seaweedfs.com/s3identity-protection"
	s3CredentialsFinalizer   = "seaweed.seaweedfs.com/s3credentials-protection"
	s3PolicyFinalizer        = "seaweed.seaweedfs.com/s3policy-protection"
	s3PolicyBindingFinalizer = "seaweed.seaweedfs.com/s3policybinding-protection"
	s3OIDCProviderFinalizer  = "seaweed.seaweedfs.com/s3oidcprovider-protection"
	s3RoleFinalizer          = "seaweed.seaweedfs.com/s3role-protection"
)

// iamResyncInterval re-runs a Ready IAM resource periodically so state lost when
// the filer's ephemeral store restarts is re-provisioned without a spec change.
// Each IAM reconcile is idempotent; this mirrors the Seaweed reconciler's
// safety-net requeue.
const iamResyncInterval = 5 * time.Minute

// iamAdminProvider supplies (and caches) IAMAdmin instances per target filer.
// Embedded in each IAM reconciler so they share the construction and caching
// logic. The cache is keyed by filer address; swadmin.IAMClient is stateless
// (it dials per call) so cached entries never need eviction.
type iamAdminProvider struct {
	// AdminFactory builds an IAMAdmin for a filer. Tests inject a fake;
	// SetupWithManager defaults it to NewSwadminIAMAdmin.
	AdminFactory IAMAdminFactory

	cache map[string]IAMAdmin
	mu    sync.Mutex
}

func (p *iamAdminProvider) getIAMAdmin(target filerTarget, log logr.Logger) (IAMAdmin, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cache == nil {
		p.cache = make(map[string]IAMAdmin)
	}
	// Cache key folds in hashes of the signing key and TLS material so that
	// rotating either (a user editing the security.toml ConfigMap,
	// cert-manager renewing the server certificate) invalidates the cached
	// client instead of leaving stale credentials behind.
	cacheKey := target.address + "\x00" + signingKeyFingerprint(target.adminSigningKey) + "\x00" + target.tlsFingerprint
	if a, ok := p.cache[cacheKey]; ok {
		return a, nil
	}
	if p.AdminFactory == nil {
		return nil, fmt.Errorf("iam admin factory is not configured")
	}
	a, err := p.AdminFactory(target.address, target.adminSigningKey, target.grpcDialOption, log)
	if err != nil {
		return nil, err
	}
	p.cache[cacheKey] = a
	return a, nil
}

func signingKeyFingerprint(key []byte) string {
	if len(key) == 0 {
		return ""
	}
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:8])
}

// tlsMaterialFingerprint hashes the PEM material behind a TLS dial option so
// admin caches can key on it.
func tlsMaterialFingerprint(ca, cert, key []byte) string {
	sum := sha256.Sum256(bytes.Join([][]byte{ca, cert, key}, []byte{0}))
	return hex.EncodeToString(sum[:8])
}

// peerAuthFingerprint renders the peer authorization a dial was built with
// into the material fingerprint, so editing allowedCommonNames or
// allowedWildcardDomain replaces cached clients built under the old
// constraint instead of keeping removed identities trusted.
func peerAuthFingerprint(tls *seaweedv1.TLSSpec) string {
	if tls == nil {
		return ""
	}
	var names []string
	if c := tls.AllowedCommonNames; c != nil {
		for _, l := range [][]string{c.Master, c.Volume, c.Filer, c.S3, c.Admin, c.Worker} {
			names = append(names, l...)
		}
	}
	sort.Strings(names)
	names = append(names, tls.AllowedWildcardDomain)
	sum := sha256.Sum256([]byte(strings.Join(names, "\x00")))
	return "." + hex.EncodeToString(sum[:8])
}

// filerTarget bundles everything a gRPC client needs to reach a Seaweed
// cluster's filer: the address, the admin Bearer signing key, and transport
// credentials matching the cluster's [grpc] mTLS state.
type filerTarget struct {
	address         string
	adminSigningKey []byte
	// grpcDialOption is non-nil when the cluster's gRPC ports require mTLS;
	// nil means dial without TLS.
	grpcDialOption grpc.DialOption
	// tlsFingerprint identifies the TLS material behind grpcDialOption so
	// admin caches roll over when cert-manager renews the certificate.
	tlsFingerprint string
}

// resolveSeaweedFiler looks up the Seaweed CR named by ref and returns its
// filer address along with the admin signing key the operator rendered into
// the cluster's security.toml Secret (the filer's IAM gRPC service rejects
// unauthenticated calls when jwt.filer_signing.key is set, so the operator
// must sign its own Bearer tokens with the same key) and the
// gRPC transport credentials matching the cluster's mTLS state. found is
// false (with a nil error) when the Seaweed CR does not exist, so callers can
// surface a transient "cluster not found" condition and requeue rather than
// treating it as a hard error.
//
// adminSigningKey is nil when the security Secret has not been reconciled
// yet, when its data is missing/malformed, or when the cluster was not
// provisioned by this operator (no Secret to read). In those cases the IAM
// client falls back to unauthenticated calls, matching the cluster's likely
// configuration.
func resolveSeaweedFiler(ctx context.Context, c client.Client, ref seaweedv1.SeaweedReference, ownNamespace string) (target filerTarget, found bool, err error) {
	ns := ref.Namespace
	if ns == "" {
		ns = ownNamespace
	}
	var sw seaweedv1.Seaweed
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, &sw); err != nil {
		if apierrors.IsNotFound(err) {
			return filerTarget{}, false, nil
		}
		return filerTarget{}, false, err
	}
	key, err := loadFilerAdminSigningKey(ctx, c, &sw)
	if err != nil {
		return filerTarget{}, false, err
	}
	dialOption, tlsFingerprint, err := loadSeaweedGrpcDialOption(ctx, c, &sw)
	if err != nil {
		return filerTarget{}, false, err
	}
	return filerTarget{
		address:         getFilerAddress(&sw),
		adminSigningKey: key,
		grpcDialOption:  dialOption,
		tlsFingerprint:  tlsFingerprint,
	}, true, nil
}

// loadFilerAdminSigningKey reads jwt.filer_signing.key from the
// seaweedfs-security-config Secret the operator renders for sw. A missing
// Secret or key returns nil: that is the normal state for a cluster without
// jwtSigning.filerWrite, whose filer accepts unauthenticated IAM calls.
func loadFilerAdminSigningKey(ctx context.Context, c client.Client, sw *seaweedv1.Seaweed) ([]byte, error) {
	var secret corev1.Secret
	err := c.Get(ctx, types.NamespacedName{Namespace: sw.Namespace, Name: SecurityConfigSecretName(sw)}, &secret)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	raw := extractTOMLKey(string(secret.Data["security.toml"]), "jwt.filer_signing", "key")
	if raw == "" {
		return nil, nil
	}
	return []byte(raw), nil
}

// operatorTLSDirEnv names the directory on the operator pod holding the CA,
// cert and key the operator dials a cluster with when the cluster's gRPC TLS
// material comes from spec.tls.source.volume. The source volume is mounted
// into component pods only, so the operator's credentials arrive separately.
const operatorTLSDirEnv = "SEAWEED_TLS_DIR"

// errOperatorTLSUnavailable marks the case where a cluster requires mTLS from
// a user-managed source volume but the operator holds no material to dial
// with. Reconcilers surface it as ClusterReachable=False instead of failing
// a dial that can never succeed.
var errOperatorTLSUnavailable = errors.New("operator TLS material unavailable")

// loadSeaweedGrpcDialOption returns the transport credentials the operator's
// gRPC clients must use to reach sw's components, plus a fingerprint of the
// TLS material for admin cache keys. Both are zero when the cluster's gRPC
// ports run without TLS: spec.tls disabled, or the server TLS Secret not
// issued (yet) — pods only mount the Secret once cert-manager has produced
// it, so its absence means the cluster is (still) running plaintext gRPC and
// dialing insecure matches. An incomplete Secret is treated the same way:
// when ca.crt is missing the pods' own security.toml points at a missing
// file and seaweedfs falls back to plaintext too.
func loadSeaweedGrpcDialOption(ctx context.Context, c client.Client, sw *seaweedv1.Seaweed) (grpc.DialOption, string, error) {
	if !tlsEnabled(sw) {
		return nil, "", nil
	}
	if tlsFromVolume(sw) {
		return loadOperatorGrpcDialOption(sw)
	}
	var sec corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: sw.Namespace, Name: TLSServerSecretName(sw)}, &sec); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, "", nil
		}
		return nil, "", err
	}
	ca, cert, key := sec.Data["ca.crt"], sec.Data["tls.crt"], sec.Data["tls.key"]
	if len(ca) == 0 || len(cert) == 0 || len(key) == 0 {
		return nil, "", nil
	}
	dialOption, err := swadmin.ClientTLSDialOption(ca, cert, key)
	if err != nil {
		return nil, "", fmt.Errorf("build gRPC TLS credentials from secret %s/%s: %w", sw.Namespace, TLSServerSecretName(sw), err)
	}
	return dialOption, tlsMaterialFingerprint(ca, cert, key), nil
}

// loadOperatorGrpcDialOption builds credentials from PEM files on the
// operator's own pod, under the directory SEAWEED_TLS_DIR points at. File
// names follow spec.tls.source.files so the same CSI driver layout can serve
// the components and the operator; files are re-read on every call so
// rotating short-lived material is picked up like a Secret refresh.
func loadOperatorGrpcDialOption(sw *seaweedv1.Seaweed) (grpc.DialOption, string, error) {
	dir := os.Getenv(operatorTLSDirEnv)
	if dir == "" {
		return nil, "", fmt.Errorf("%w: Seaweed %s/%s uses spec.tls.source.volume; set %s on the operator deployment to a directory holding its client CA, cert and key", errOperatorTLSUnavailable, sw.Namespace, sw.Name, operatorTLSDirEnv)
	}
	caName, certName, keyName := tlsFileNames(sw.Spec.TLS)
	read := func(name string) ([]byte, error) {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("%w: %s/%s: %v", errOperatorTLSUnavailable, dir, name, err)
		}
		return b, nil
	}
	ca, err := read(caName)
	if err != nil {
		return nil, "", err
	}
	cert, err := read(certName)
	if err != nil {
		return nil, "", err
	}
	key, err := read(keyName)
	if err != nil {
		return nil, "", err
	}
	dialOption, err := swadmin.ClientTLSDialOptionVerifyPeerFor(ca, cert, key, expectedServerPeerFor(sw))
	if err != nil {
		return nil, "", fmt.Errorf("%w: build gRPC TLS credentials from %s: %v", errOperatorTLSUnavailable, dir, err)
	}
	return dialOption, tlsMaterialFingerprint(ca, cert, key) + peerAuthFingerprint(sw.Spec.TLS), nil
}

// tlsComponents are the component buckets a peer identity can attribute to —
// the same set allowedCommonNames divides its entries across.
var tlsComponents = []string{"master", "volume", "filer", "s3", "admin", "worker"}

// tlsPeerNames collects every allowedCommonNames entry keyed back to the
// component bucket it was declared under.
func tlsPeerNames(tls *seaweedv1.TLSSpec) map[string]string {
	declared := map[string]string{}
	if c := tls.AllowedCommonNames; c != nil {
		for comp, list := range map[string][]string{
			"master": c.Master, "volume": c.Volume, "filer": c.Filer,
			"s3": c.S3, "admin": c.Admin, "worker": c.Worker,
		} {
			for _, n := range list {
				declared[n] = comp
			}
		}
	}
	return declared
}

// servicePeerDNS reports whether a DNS SAN names component c's service or a
// peer pod under it, namespace-qualified so a same-named service in another
// namespace on the same CA cannot impersonate it.
func servicePeerDNS(sw *seaweedv1.Seaweed, c, dns string) bool {
	svc := sw.Name + "-" + c
	ns := sw.Namespace
	return dns == svc+"."+ns ||
		dns == svc+"."+ns+".svc" ||
		dns == svc+"."+ns+".svc.cluster.local" ||
		strings.HasSuffix(dns, "."+svc+"-peer."+ns+".svc.cluster.local")
}

// expectedServerPeer derives the server identity the operator's dial must
// see. On a shared CA (a SPIRE trust domain, for example) chain verification
// alone accepts any workload the CA issues, so the leaf must also present an
// identity the cluster owns: a name in the declared authorization — the
// wildcard domain matched by CN or DNS SAN suffix or by spiffe:// URI SAN
// trust domain, or an entry in allowedCommonNames — or one of the cluster's
// namespace-qualified component service DNS names, which covers CA-issued
// certs minted for the services rather than the workload. Bare short names
// are not accepted: they carry no namespace, so a same-named service in
// another namespace sharing the CA could impersonate the component. A CR
// with no authorization configured keeps chain-only verification.
func expectedServerPeer(sw *seaweedv1.Seaweed) func(*x509.Certificate) error {
	tls := sw.Spec.TLS
	if tls == nil || (tls.AllowedWildcardDomain == "" && tls.AllowedCommonNames == nil) {
		return nil
	}
	domain := tls.AllowedWildcardDomain
	trustDomain := strings.TrimPrefix(domain, ".")
	var names map[string]bool
	if declared := tlsPeerNames(tls); len(declared) > 0 {
		names = map[string]bool{}
		for n := range declared {
			names[n] = true
		}
	}
	return func(leaf *x509.Certificate) error {
		if domain != "" && strings.HasSuffix(leaf.Subject.CommonName, domain) {
			return nil
		}
		if names[leaf.Subject.CommonName] {
			return nil
		}
		for _, dns := range leaf.DNSNames {
			if domain != "" && strings.HasSuffix(dns, domain) {
				return nil
			}
			for _, c := range tlsComponents {
				if servicePeerDNS(sw, c, dns) {
					return nil
				}
			}
		}
		for _, uri := range leaf.URIs {
			if domain != "" && uri.Scheme == "spiffe" && uri.Host == trustDomain {
				return nil
			}
			if names[uri.String()] {
				return nil
			}
		}
		return fmt.Errorf("server certificate identity %q is not authorized by spec.tls", leaf.Subject.CommonName)
	}
}

// expectedServerPeerFor scopes expectedServerPeer to the dial target. The
// handshake authority names the component the dial intends to reach, so a
// leaf attributable to a different component is rejected even when it sits
// inside the declared trust boundary — a certificate accepted as another
// component's client identity no longer identifies every endpoint.
// Attribution comes from the cluster's service DNS names, per-component
// allowedCommonNames entries, and spiffe:// path segments naming a component;
// a leaf naming no component keeps the union check, so deployments sharing
// one unattributed certificate across components see no behavior change.
func expectedServerPeerFor(sw *seaweedv1.Seaweed) func(authority string, leaf *x509.Certificate) error {
	union := expectedServerPeer(sw)
	if union == nil {
		return nil
	}
	return func(authority string, leaf *x509.Certificate) error {
		comp := authorityComponent(sw, authority)
		attributed := attributedServerComponents(sw, leaf)
		if comp == "" || len(attributed) == 0 || attributed[comp] {
			return union(leaf)
		}
		others := make([]string, 0, len(attributed))
		for c := range attributed {
			others = append(others, c)
		}
		sort.Strings(others)
		return fmt.Errorf("server certificate identifies %s, not the %s endpoint", strings.Join(others, "+"), comp)
	}
}

// authorityComponent resolves the component a dial target belongs to from the
// service or peer-pod name in the authority's host. Targets that cannot be
// mapped — pod IPs, external addresses, unrelated hosts — return "".
func authorityComponent(sw *seaweedv1.Seaweed, authority string) string {
	host, _, err := net.SplitHostPort(authority)
	if err != nil {
		host = authority
	}
	first, rest, _ := strings.Cut(host, ".")
	second, _, _ := strings.Cut(rest, ".")
	for _, c := range tlsComponents {
		svc := sw.Name + "-" + c
		if first == svc || host == svc {
			return c
		}
		// StatefulSet pods (seaweed-master-0) reach the cluster through the
		// peer headless service or as a bare pod name.
		if suffix, ok := strings.CutPrefix(first, svc+"-"); ok && isOrdinal(suffix) {
			if second == "" || second == svc+"-peer" || second == sw.Namespace {
				return c
			}
		}
	}
	return ""
}

func isOrdinal(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// attributedServerComponents reports which components the leaf's identity
// evidence names: its service DNS SANs, allowedCommonNames entries (CN, DNS
// SAN or URI SAN value), domain names prefixed by a component service name,
// and spiffe:// SVID path segments equal to a component or its service name.
func attributedServerComponents(sw *seaweedv1.Seaweed, leaf *x509.Certificate) map[string]bool {
	attributed := map[string]bool{}
	tls := sw.Spec.TLS
	if tls == nil {
		return attributed
	}
	domain := tls.AllowedWildcardDomain
	trustDomain := strings.TrimPrefix(domain, ".")
	declared := tlsPeerNames(tls)
	addName := func(name string) {
		if comp, ok := declared[name]; ok {
			attributed[comp] = true
			return
		}
		if comp := domainPeerComponent(sw, name, domain); comp != "" {
			attributed[comp] = true
		}
	}
	if cn := leaf.Subject.CommonName; cn != "" {
		addName(cn)
	}
	for _, dns := range leaf.DNSNames {
		service := false
		for _, c := range tlsComponents {
			if servicePeerDNS(sw, c, dns) {
				attributed[c] = true
				service = true
			}
		}
		if !service {
			addName(dns)
		}
	}
	for _, uri := range leaf.URIs {
		if comp, ok := declared[uri.String()]; ok {
			attributed[comp] = true
			continue
		}
		if domain != "" && uri.Scheme == "spiffe" && uri.Host == trustDomain {
			if comp := svidPathComponent(sw, uri.Path); comp != "" {
				attributed[comp] = true
			}
		}
	}
	return attributed
}

// domainPeerComponent attributes a wildcard-domain-matched name to a
// component when its leading labels name that component's service
// (seaweed-filer.e2e.test → filer); anything else in the domain stays
// unattributed.
func domainPeerComponent(sw *seaweedv1.Seaweed, name, domain string) string {
	if domain == "" || !strings.HasSuffix(name, domain) {
		return ""
	}
	host := strings.TrimSuffix(strings.TrimSuffix(name, domain), ".")
	for _, c := range tlsComponents {
		svc := sw.Name + "-" + c
		if host == svc || strings.HasPrefix(host, svc+"-") {
			return c
		}
	}
	return ""
}

// svidPathComponent attributes a trust-domain SVID to a component when a path
// segment names it (sa/seaweeds-filer, ns/prod/role/filer); SVIDs naming no
// component stay unattributed.
func svidPathComponent(sw *seaweedv1.Seaweed, path string) string {
	for _, seg := range strings.Split(strings.Trim(path, "/"), "/") {
		for _, c := range tlsComponents {
			if seg == sw.Name+"-"+c || seg == c {
				return c
			}
		}
	}
	return ""
}

// reportClusterUnreachable persists ClusterReachable=False on obj's status
// and requeues on the IAM resync cadence instead of erroring — used when the
// cluster exists but the operator cannot currently reach it.
func reportClusterUnreachable(ctx context.Context, c client.Client, obj client.Object, conds *[]metav1.Condition, generation int64, err error) (ctrl.Result, error) {
	reason := "ClusterUnreachable"
	if errors.Is(err, errOperatorTLSUnavailable) {
		reason = "OperatorTLSNotConfigured"
	}
	setIAMCondition(conds, generation, seaweedv1.S3ConditionClusterReachable, metav1.ConditionFalse, reason, err.Error())
	if uerr := c.Status().Update(ctx, obj); uerr != nil {
		return ctrl.Result{}, uerr
	}
	return ctrl.Result{RequeueAfter: iamResyncInterval}, nil
}

// setIAMCondition upserts a condition on an IAM CR's condition list, stamping
// the observed generation so stale conditions are obvious.
func setIAMCondition(conds *[]metav1.Condition, generation int64, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(conds, metav1.Condition{
		Type:               condType,
		Status:             status,
		ObservedGeneration: generation,
		Reason:             reason,
		Message:            message,
	})
}

// clearIAMCondition removes a condition from an IAM CR's condition list. Used to
// drop a transient condition (e.g. ReferenceGranted=False) once its cause is
// resolved, so it does not linger as stale state next to a Ready condition.
func clearIAMCondition(conds *[]metav1.Condition, condType string) {
	meta.RemoveStatusCondition(conds, condType)
}
