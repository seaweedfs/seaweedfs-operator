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
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/grpc"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
// the cluster's security.toml Secret (issue #257: the filer's IAM gRPC
// service rejects unauthenticated calls when jwt.filer_signing.key is set, so
// the operator must sign its own Bearer tokens with the same key) and the
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
// seaweedfs-security-config Secret the operator renders for sw. Returns
// nil with no error when the Secret does not exist or its security.toml
// has no key (the cluster will be running unauthenticated in that case).
// A non-NotFound API error is propagated so the reconciler can requeue.
func loadFilerAdminSigningKey(ctx context.Context, c client.Client, sw *seaweedv1.Seaweed) ([]byte, error) {
	if !securityConfigNeeded(sw) {
		return nil, nil
	}
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
