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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// Group/kind strings as they appear in a ResourceReferenceGrant's from/to
// entries. groupCore ("") is the core API group, where Secret lives.
const (
	groupSeaweed = "seaweed.seaweedfs.com"
	groupCore    = ""

	kindSeaweed         = "Seaweed"
	kindSecret          = "Secret"
	kindS3Identity      = "S3Identity"
	kindS3Credentials   = "S3Credentials"
	kindS3Policy        = "S3Policy"
	kindS3PolicyBinding = "S3PolicyBinding"
	kindS3OIDCProvider  = "S3OIDCProvider"
	kindBucket          = "Bucket"
)

// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=resourcereferencegrants,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

// referent is one side of a reference; Name is only consulted on the "to" side.
type referent struct {
	Group     string
	Kind      string
	Namespace string
	Name      string
}

// referenceGrantPermits reports whether a from->to reference is allowed. Same
// namespace is always allowed; cross-namespace requires a ResourceReferenceGrant
// in to.Namespace matching both sides (Gateway API ReferenceGrant semantics:
// deny by default). Enforcement is reconcile-time, so revoking a grant blocks the
// next (re)provision but does not undo objects already provisioned under it.
func referenceGrantPermits(ctx context.Context, c client.Client, from, to referent) (bool, error) {
	if from.Namespace == to.Namespace {
		return true, nil
	}
	var grants seaweedv1.ResourceReferenceGrantList
	if err := c.List(ctx, &grants, client.InNamespace(to.Namespace)); err != nil {
		return false, err
	}
	fromLabels, err := sourceNamespaceLabels(ctx, c, grants.Items, from.Namespace)
	if err != nil {
		return false, err
	}
	for i := range grants.Items {
		if grantAllows(&grants.Items[i].Spec, from, to, fromLabels) {
			return true, nil
		}
	}
	return false, nil
}

// sourceNamespaceLabels reads namespace's labels, but only when some grant uses
// a namespaceSelector — otherwise the exact-namespace path needs no extra read.
func sourceNamespaceLabels(ctx context.Context, c client.Client, grants []seaweedv1.ResourceReferenceGrant, namespace string) (map[string]string, error) {
	usesSelector := false
	for i := range grants {
		for _, f := range grants[i].Spec.From {
			if f.NamespaceSelector != nil {
				usesSelector = true
			}
		}
	}
	if !usesSelector {
		return nil, nil
	}
	var ns corev1.Namespace
	if err := c.Get(ctx, client.ObjectKey{Name: namespace}, &ns); err != nil {
		return nil, err
	}
	return ns.Labels, nil
}

// grantAllows reports whether a grant spec matches both a from entry and a to
// entry. An empty to.Name is a wildcard over the group/kind. fromLabels are the
// labels of from.Namespace, used only by namespaceSelector entries.
func grantAllows(spec *seaweedv1.ResourceReferenceGrantSpec, from, to referent, fromLabels map[string]string) bool {
	fromMatched := false
	for _, f := range spec.From {
		if f.Group == from.Group && f.Kind == from.Kind && fromNamespaceMatches(f, from.Namespace, fromLabels) {
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
		if t.Name == "" || t.Name == to.Name {
			return true
		}
	}
	return false
}

// fromNamespaceMatches reports whether a from-entry covers the source namespace,
// by exact name or by namespaceSelector (an empty selector matches every one).
func fromNamespaceMatches(f seaweedv1.ReferenceGrantFrom, namespace string, nsLabels map[string]string) bool {
	if f.NamespaceSelector == nil {
		return f.Namespace == namespace
	}
	sel, err := metav1.LabelSelectorAsSelector(f.NamespaceSelector)
	if err != nil {
		return false
	}
	return sel.Matches(labels.Set(nsLabels))
}

// seaweedRefPermitted reports whether fromKind in fromNamespace may resolve ref.
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
// reference the Secret secretNamespace/secretName.
func secretRefPermitted(ctx context.Context, c client.Client, secretNamespace, secretName, fromNamespace string) (bool, error) {
	return referenceGrantPermits(ctx, c,
		referent{Group: groupSeaweed, Kind: kindS3Credentials, Namespace: fromNamespace},
		referent{Group: groupCore, Kind: kindSecret, Namespace: secretNamespace, Name: secretName},
	)
}

// seaweedRefDeniedMessage names the grant that would permit a denied seaweedRef.
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

// secretRefDeniedMessage names the grant that would permit a denied secretRef.
func secretRefDeniedMessage(secretNamespace, secretName, fromNamespace string) string {
	return fmt.Sprintf(
		"cross-namespace secretRef to Secret %q in namespace %q is not permitted; create a ResourceReferenceGrant in namespace %q with from {group: %q, kind: %q, namespace: %q} to {group: %q, kind: %q}",
		secretName, secretNamespace, secretNamespace,
		groupSeaweed, kindS3Credentials, fromNamespace, groupCore, kindSecret)
}
