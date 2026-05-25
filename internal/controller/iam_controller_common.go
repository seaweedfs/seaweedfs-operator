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
	"sync"

	"github.com/go-logr/logr"
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

func (p *iamAdminProvider) getIAMAdmin(filer string, log logr.Logger) (IAMAdmin, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cache == nil {
		p.cache = make(map[string]IAMAdmin)
	}
	if a, ok := p.cache[filer]; ok {
		return a, nil
	}
	a, err := p.AdminFactory(filer, log)
	if err != nil {
		return nil, err
	}
	p.cache[filer] = a
	return a, nil
}

// resolveSeaweedFiler looks up the Seaweed CR named by ref and returns its
// filer address. found is false (with a nil error) when the Seaweed CR does
// not exist, so callers can surface a transient "cluster not found" condition
// and requeue rather than treating it as a hard error.
func resolveSeaweedFiler(ctx context.Context, c client.Client, ref seaweedv1.SeaweedReference, ownNamespace string) (filer string, found bool, err error) {
	ns := ref.Namespace
	if ns == "" {
		ns = ownNamespace
	}
	var sw seaweedv1.Seaweed
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, &sw); err != nil {
		if apierrors.IsNotFound(err) {
			return "", false, nil
		}
		return "", false, err
	}
	return getFilerAddress(&sw), true, nil
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
