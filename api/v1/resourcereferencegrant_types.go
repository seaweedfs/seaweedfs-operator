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

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ResourceReferenceGrant grants resources in one namespace permission to be
// referenced from another namespace. It mirrors the Gateway API ReferenceGrant
// (sigs.k8s.io/gateway-api): a cross-namespace reference is denied by default
// and only succeeds when a ResourceReferenceGrant in the *referent's* namespace
// (the namespace of the resource being pointed at) explicitly allows it.
//
// The grant lives in the namespace that owns the resources being referenced (the
// "to" side), so that namespace's owner — not the requester — controls who may
// reach in. Every spec.from entry is paired with every spec.to entry: a
// reference is permitted when its source matches any from entry and its target
// matches any to entry.
//
// This gates the cross-namespace references in this API group:
//   - seaweedRef on S3Identity, S3Credentials, S3Policy, S3PolicyBinding, and
//     clusterRef on Bucket (kind "Seaweed", group seaweed.seaweedfs.com)
//   - secretRef on S3Credentials (kind "Secret", core group "")
//
// Same-namespace references never require a grant.

// ReferenceGrantFrom describes a source of cross-namespace references: a kind in
// a namespace that the grant trusts to reach into this namespace.
type ReferenceGrantFrom struct {
	// Group is the API group of the referencing resource. The core API group
	// is the empty string. For this operator's CRDs use
	// "seaweed.seaweedfs.com".
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^$|^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	Group string `json:"group"`

	// Kind is the kind of the referencing resource, e.g. "S3Credentials",
	// "S3Identity", "S3Policy", "S3PolicyBinding", or "Bucket".
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-zA-Z]([-a-zA-Z0-9]*[a-zA-Z0-9])?$`
	Kind string `json:"kind"`

	// Namespace is the namespace of the referencing resource.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Namespace string `json:"namespace"`
}

// ReferenceGrantTo describes a referent: a kind (optionally a single named
// resource) in this grant's namespace that may be referenced from a trusted
// source.
type ReferenceGrantTo struct {
	// Group is the API group of the referent. The core API group (used by
	// Secret) is the empty string. For this operator's Seaweed CR use
	// "seaweed.seaweedfs.com".
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^$|^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	Group string `json:"group"`

	// Kind is the kind of the referent, e.g. "Seaweed" or "Secret".
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-zA-Z]([-a-zA-Z0-9]*[a-zA-Z0-9])?$`
	Kind string `json:"kind"`

	// Name, when set, restricts the grant to a single referent of the given
	// group/kind in this namespace. Leave it empty to permit every resource of
	// that group/kind in this namespace.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name,omitempty"`
}

// ResourceReferenceGrantSpec identifies the cross-namespace references this
// grant permits. A reference is allowed when it matches at least one from entry
// and at least one to entry.
type ResourceReferenceGrantSpec struct {
	// From is the set of trusted sources (kind + namespace) that may reference
	// the resources listed in To.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +listType=atomic
	From []ReferenceGrantFrom `json:"from"`

	// To is the set of referents (kind, optionally a single name) in this
	// grant's own namespace that the sources in From may reference.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +listType=atomic
	To []ReferenceGrantTo `json:"to"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=refgrant,categories=seaweedfs
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ResourceReferenceGrant is the Schema for declaratively permitting
// cross-namespace references into the grant's namespace. Like the Gateway API
// ReferenceGrant it has no status: it is pure authorization data consumed by the
// other controllers, with no lifecycle of its own.
type ResourceReferenceGrant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ResourceReferenceGrantSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// ResourceReferenceGrantList contains a list of ResourceReferenceGrant.
type ResourceReferenceGrantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ResourceReferenceGrant `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ResourceReferenceGrant{}, &ResourceReferenceGrantList{})
}
