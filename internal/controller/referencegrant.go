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
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// Group/kind identifiers used when matching ResourceReferenceGrants. These are
// the API group + kind strings as they appear in a ResourceReferenceGrant's
// spec.from / spec.to entries.
const (
	// groupSeaweed is this operator's API group (Seaweed, Bucket, S3* CRDs).
	groupSeaweed = "seaweed.seaweedfs.com"
	// groupCore is the core Kubernetes API group, where Secret lives.
	groupCore = ""

	kindSeaweed         = "Seaweed"
	kindSecret          = "Secret"
	kindS3Identity      = "S3Identity"
	kindS3Credentials   = "S3Credentials"
	kindS3Policy        = "S3Policy"
	kindS3PolicyBinding = "S3PolicyBinding"
	kindBucket          = "Bucket"
)

// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=resourcereferencegrants,verbs=get;list;watch

// referent identifies one side of a cross-namespace reference when matching it
// against a ResourceReferenceGrant. Name is only consulted on the "to" side.
type referent struct {
	Group     string
	Kind      string
	Namespace string
	Name      string
}

// referenceGrantPermits reports whether a reference from `from` to `to` is
// allowed. A same-namespace reference is always allowed and short-circuits
// without an API call. A cross-namespace reference is allowed only when some
// ResourceReferenceGrant in the referent's namespace (to.Namespace) has a from
// entry matching the source's group/kind/namespace and a to entry matching the
// referent's group/kind — and, when that to entry pins a name, its name. This
// mirrors the Gateway API ReferenceGrant semantics: deny by default, the
// referent's namespace owner opts specific sources in.
//
// Enforcement is reconcile-time and therefore eventually consistent, like every
// cross-resource dependency in this operator (and like Gateway API itself):
// the check runs at the start of a reconcile, so revoking a grant does not
// retroactively undo objects already provisioned under it, nor is it guaranteed
// to interrupt an in-flight reconcile. A revoked grant takes effect on the next
// reconcile, which then refuses to (re)provision and reports the reference as
// not granted. Deletion never consults a grant, so revocation cannot strand a
// finalizer.
func referenceGrantPermits(ctx context.Context, c client.Client, from, to referent) (bool, error) {
	if from.Namespace == to.Namespace {
		return true, nil
	}
	var grants seaweedv1.ResourceReferenceGrantList
	if err := c.List(ctx, &grants, client.InNamespace(to.Namespace)); err != nil {
		return false, err
	}
	for i := range grants.Items {
		if grantAllows(&grants.Items[i].Spec, from, to) {
			return true, nil
		}
	}
	return false, nil
}

// grantAllows reports whether a single grant spec permits the from->to
// reference: it must match both a from entry and a to entry.
func grantAllows(spec *seaweedv1.ResourceReferenceGrantSpec, from, to referent) bool {
	fromMatched := false
	for _, f := range spec.From {
		if f.Group == from.Group && f.Kind == from.Kind && f.Namespace == from.Namespace {
			fromMatched = true
			break
		}
	}
	if !fromMatched {
		return false
	}
	for _, t := range spec.To {
		if t.Group != to.Group || t.Kind != to.Kind {
			continue
		}
		// An empty Name permits every resource of this group/kind in the
		// namespace; a set Name pins a single referent.
		if t.Name == "" || t.Name == to.Name {
			return true
		}
	}
	return false
}

// seaweedRefPermitted reports whether a resource of fromKind in fromNamespace
// may resolve the given seaweedRef. Cross-namespace refs require a
// ResourceReferenceGrant in the target Seaweed's namespace; same-namespace refs
// are always permitted.
func seaweedRefPermitted(ctx context.Context, c client.Client, ref seaweedv1.SeaweedReference, fromKind, fromNamespace string) (bool, error) {
	toNamespace := ref.Namespace
	if toNamespace == "" {
		toNamespace = fromNamespace
	}
	return referenceGrantPermits(ctx, c,
		referent{Group: groupSeaweed, Kind: fromKind, Namespace: fromNamespace},
		referent{Group: groupSeaweed, Kind: kindSeaweed, Namespace: toNamespace, Name: ref.Name},
	)
}

// secretRefPermitted reports whether an S3Credentials in fromNamespace may
// reference the Secret named secretName in secretNamespace. Same-namespace
// references are always permitted.
func secretRefPermitted(ctx context.Context, c client.Client, secretNamespace, secretName, fromNamespace string) (bool, error) {
	return referenceGrantPermits(ctx, c,
		referent{Group: groupSeaweed, Kind: kindS3Credentials, Namespace: fromNamespace},
		referent{Group: groupCore, Kind: kindSecret, Namespace: secretNamespace, Name: secretName},
	)
}

// seaweedRefDeniedMessage explains why a cross-namespace seaweedRef was denied
// and exactly which ResourceReferenceGrant would permit it.
func seaweedRefDeniedMessage(ref seaweedv1.SeaweedReference, fromKind, fromNamespace string) string {
	toNamespace := ref.Namespace
	if toNamespace == "" {
		toNamespace = fromNamespace
	}
	return fmt.Sprintf(
		"cross-namespace seaweedRef to Seaweed %q in namespace %q is not permitted; create a ResourceReferenceGrant in namespace %q with from {group: %q, kind: %q, namespace: %q} to {group: %q, kind: %q}",
		ref.Name, toNamespace, toNamespace,
		groupSeaweed, fromKind, fromNamespace, groupSeaweed, kindSeaweed)
}

// secretRefDeniedMessage explains why a cross-namespace secretRef was denied and
// exactly which ResourceReferenceGrant would permit it.
func secretRefDeniedMessage(secretNamespace, secretName, fromNamespace string) string {
	return fmt.Sprintf(
		"cross-namespace secretRef to Secret %q in namespace %q is not permitted; create a ResourceReferenceGrant in namespace %q with from {group: %q, kind: %q, namespace: %q} to {group: %q, kind: %q}",
		secretName, secretNamespace, secretNamespace,
		groupSeaweed, kindS3Credentials, fromNamespace, groupCore, kindSecret)
}
