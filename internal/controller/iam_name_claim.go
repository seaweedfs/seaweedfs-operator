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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// IAM user and policy names are global per Seaweed cluster while the CRs
// managing them are namespaced, so two CRs can claim the same IAM name. The
// helpers here serialize such claims: the oldest CR owns the name and later
// claimants are surfaced as conflicts instead of silently fighting over one
// IAM object.

func identityIAMName(id *seaweedv1.S3Identity) string {
	if id.Spec.Name != "" {
		return id.Spec.Name
	}
	return id.Name
}

func policyIAMName(p *seaweedv1.S3Policy) string {
	if p.Spec.Name != "" {
		return p.Spec.Name
	}
	return p.Name
}

// seaweedRefKey identifies the referenced cluster with the namespace default
// applied.
func seaweedRefKey(ref seaweedv1.SeaweedReference, ownNamespace string) string {
	ns := ref.Namespace
	if ns == "" {
		ns = ownNamespace
	}
	return ns + "/" + ref.Name
}

// claimPrecedes reports whether a outranks b for a contested IAM name: older
// creationTimestamp first, namespace/name as the deterministic tiebreaker.
func claimPrecedes(a, b metav1.Object) bool {
	at, bt := a.GetCreationTimestamp(), b.GetCreationTimestamp()
	if !at.Equal(&bt) {
		return at.Before(&bt)
	}
	if a.GetNamespace() != b.GetNamespace() {
		return a.GetNamespace() < b.GetNamespace()
	}
	return a.GetName() < b.GetName()
}

// identityClaimants returns the other live S3Identities claiming the same IAM
// user name on the same cluster as self.
func identityClaimants(ctx context.Context, c client.Client, self *seaweedv1.S3Identity, name string) ([]*seaweedv1.S3Identity, error) {
	var list seaweedv1.S3IdentityList
	if err := c.List(ctx, &list); err != nil {
		return nil, err
	}
	cluster := seaweedRefKey(self.Spec.SeaweedRef, self.Namespace)
	var out []*seaweedv1.S3Identity
	for i := range list.Items {
		peer := &list.Items[i]
		if peer.Namespace == self.Namespace && peer.Name == self.Name {
			continue
		}
		if !peer.DeletionTimestamp.IsZero() {
			continue
		}
		if identityIAMName(peer) != name || seaweedRefKey(peer.Spec.SeaweedRef, peer.Namespace) != cluster {
			continue
		}
		out = append(out, peer)
	}
	return out, nil
}

// identityConflict returns the claimant that outranks self for the contested
// IAM user name, or nil when self owns it.
func identityConflict(ctx context.Context, c client.Client, self *seaweedv1.S3Identity, name string) (*seaweedv1.S3Identity, error) {
	peers, err := identityClaimants(ctx, c, self, name)
	if err != nil {
		return nil, err
	}
	var winner *seaweedv1.S3Identity
	for _, peer := range peers {
		if winner == nil || claimPrecedes(peer, winner) {
			winner = peer
		}
	}
	if winner == nil || claimPrecedes(self, winner) {
		return nil, nil
	}
	return winner, nil
}

// policyClaimants returns the other live S3Policies claiming the same IAM
// policy name on the same cluster as self.
func policyClaimants(ctx context.Context, c client.Client, self *seaweedv1.S3Policy, name string) ([]*seaweedv1.S3Policy, error) {
	var list seaweedv1.S3PolicyList
	if err := c.List(ctx, &list); err != nil {
		return nil, err
	}
	cluster := seaweedRefKey(self.Spec.SeaweedRef, self.Namespace)
	var out []*seaweedv1.S3Policy
	for i := range list.Items {
		peer := &list.Items[i]
		if peer.Namespace == self.Namespace && peer.Name == self.Name {
			continue
		}
		if !peer.DeletionTimestamp.IsZero() {
			continue
		}
		if policyIAMName(peer) != name || seaweedRefKey(peer.Spec.SeaweedRef, peer.Namespace) != cluster {
			continue
		}
		out = append(out, peer)
	}
	return out, nil
}

// policyConflict returns the claimant that outranks self for the contested
// IAM policy name, or nil when self owns it.
func policyConflict(ctx context.Context, c client.Client, self *seaweedv1.S3Policy, name string) (*seaweedv1.S3Policy, error) {
	peers, err := policyClaimants(ctx, c, self, name)
	if err != nil {
		return nil, err
	}
	var winner *seaweedv1.S3Policy
	for _, peer := range peers {
		if winner == nil || claimPrecedes(peer, winner) {
			winner = peer
		}
	}
	if winner == nil || claimPrecedes(self, winner) {
		return nil, nil
	}
	return winner, nil
}

// resolveIdentityIAMName maps an identityRef/subject name to an IAM user
// name. A same-namespace S3Identity of that resource name targeting the same
// cluster resolves to its effective IAM name; otherwise the reference is the
// IAM name itself (an identity not managed by a CR).
func resolveIdentityIAMName(ctx context.Context, c client.Client, namespace, ref, clusterKey string) (string, error) {
	var id seaweedv1.S3Identity
	err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ref}, &id)
	switch {
	case err == nil:
		if seaweedRefKey(id.Spec.SeaweedRef, id.Namespace) == clusterKey {
			return identityIAMName(&id), nil
		}
		return ref, nil
	case apierrors.IsNotFound(err):
		return ref, nil
	default:
		return "", err
	}
}

// resolvePolicyIAMName is resolveIdentityIAMName for policyRef.
func resolvePolicyIAMName(ctx context.Context, c client.Client, namespace, ref, clusterKey string) (string, error) {
	var p seaweedv1.S3Policy
	err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ref}, &p)
	switch {
	case err == nil:
		if seaweedRefKey(p.Spec.SeaweedRef, p.Namespace) == clusterKey {
			return policyIAMName(&p), nil
		}
		return ref, nil
	case apierrors.IsNotFound(err):
		return ref, nil
	default:
		return "", err
	}
}
