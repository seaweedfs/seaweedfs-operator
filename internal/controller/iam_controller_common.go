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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// Finalizers protecting each IAM CR from deletion until the reconciler has
// honored its reclaimPolicy.
const (
	s3IdentityFinalizer      = "seaweed.seaweedfs.com/s3identity-protection"
	s3CredentialsFinalizer   = "seaweed.seaweedfs.com/s3credentials-protection"
	s3PolicyFinalizer        = "seaweed.seaweedfs.com/s3policy-protection"
	s3PolicyBindingFinalizer = "seaweed.seaweedfs.com/s3policybinding-protection"
)

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

func (p *iamAdminProvider) getIAMAdmin(filer string, adminSigningKey []byte, log logr.Logger) (IAMAdmin, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cache == nil {
		p.cache = make(map[string]IAMAdmin)
	}
	// Cache key folds in a hash of the signing key so that key rotation (a
	// user editing the security.toml ConfigMap) invalidates the cached
	// client instead of leaving a stale Bearer issuer behind.
	cacheKey := filer + "\x00" + signingKeyFingerprint(adminSigningKey)
	if a, ok := p.cache[cacheKey]; ok {
		return a, nil
	}
	if p.AdminFactory == nil {
		return nil, fmt.Errorf("iam admin factory is not configured")
	}
	a, err := p.AdminFactory(filer, adminSigningKey, log)
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

// resolveSeaweedFiler looks up the Seaweed CR named by ref and returns its
// filer address along with the admin signing key the operator rendered into
// the cluster's security.toml ConfigMap (issue #257: the filer's IAM gRPC
// service rejects unauthenticated calls when jwt.filer_signing.key is set, so
// the operator must sign its own Bearer tokens with the same key). found is
// false (with a nil error) when the Seaweed CR does not exist, so callers can
// surface a transient "cluster not found" condition and requeue rather than
// treating it as a hard error.
//
// adminSigningKey is nil when the security ConfigMap has not been reconciled
// yet, when its data is missing/malformed, or when the cluster was not
// provisioned by this operator (no ConfigMap to read). In those cases the IAM
// client falls back to unauthenticated calls, matching the cluster's likely
// configuration.
func resolveSeaweedFiler(ctx context.Context, c client.Client, ref seaweedv1.SeaweedReference, ownNamespace string) (filer string, adminSigningKey []byte, found bool, err error) {
	ns := ref.Namespace
	if ns == "" {
		ns = ownNamespace
	}
	var sw seaweedv1.Seaweed
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, &sw); err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil, false, nil
		}
		return "", nil, false, err
	}
	key, err := loadFilerAdminSigningKey(ctx, c, &sw)
	if err != nil {
		return "", nil, false, err
	}
	return getFilerAddress(&sw), key, true, nil
}

// loadFilerAdminSigningKey reads jwt.filer_signing.key from the
// seaweedfs-security-config ConfigMap the operator renders for sw. Returns
// nil with no error when the ConfigMap does not exist or its security.toml
// has no key (the cluster will be running unauthenticated in that case).
// A non-NotFound API error is propagated so the reconciler can requeue.
func loadFilerAdminSigningKey(ctx context.Context, c client.Client, sw *seaweedv1.Seaweed) ([]byte, error) {
	if !securityConfigNeeded(sw) {
		return nil, nil
	}
	var cm corev1.ConfigMap
	err := c.Get(ctx, types.NamespacedName{Namespace: sw.Namespace, Name: SecurityConfigMapName(sw)}, &cm)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	raw := extractTOMLKey(cm.Data["security.toml"], "jwt.filer_signing", "key")
	if raw == "" {
		return nil, nil
	}
	return []byte(raw), nil
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
