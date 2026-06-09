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

// ReferenceGrantFrom is a trusted source: a kind in one or more namespaces
// allowed to reference into this grant's namespace.
// namespace is a CEL reserved word, so it must be escaped as __namespace__ here.
// +kubebuilder:validation:XValidation:rule="has(self.__namespace__) != has(self.namespaceSelector)",message="exactly one of namespace or namespaceSelector must be set"
type ReferenceGrantFrom struct {
	// Group of the referencing resource; core group is "".
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^$|^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	Group string `json:"group"`

	// Kind of the referencing resource, e.g. "Bucket" or "S3Credentials".
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-zA-Z]([-a-zA-Z0-9]*[a-zA-Z0-9])?$`
	Kind string `json:"kind"`

	// Namespace names a single source namespace exactly.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Namespace string `json:"namespace,omitempty"`

	// NamespaceSelector trusts every source namespace whose labels match, so
	// dynamically created namespaces are covered without editing the grant. An
	// empty selector ({}) matches all namespaces.
	// +optional
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`
}

// ReferenceGrantTo is a referent in this grant's namespace, optionally pinned to
// a single name.
type ReferenceGrantTo struct {
	// Group of the referent; core group "" for Secret.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^$|^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	Group string `json:"group"`

	// Kind of the referent, e.g. "Seaweed" or "Secret".
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-zA-Z]([-a-zA-Z0-9]*[a-zA-Z0-9])?$`
	Kind string `json:"kind"`

	// Name pins a single referent; empty permits every resource of the group/kind.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name,omitempty"`
}

// ResourceReferenceGrantSpec permits a reference that matches at least one From
// and at least one To entry.
type ResourceReferenceGrantSpec struct {
	// From lists trusted sources that may reference the resources in To.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +listType=atomic
	From []ReferenceGrantFrom `json:"from"`

	// To lists the referents in this grant's namespace that From may reference.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +listType=atomic
	To []ReferenceGrantTo `json:"to"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=refgrant,categories=seaweedfs
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ResourceReferenceGrant permits cross-namespace references into its own
// namespace, mirroring the Gateway API ReferenceGrant: a cross-namespace
// reference (seaweedRef/clusterRef -> Seaweed, secretRef -> Secret) is denied
// unless a grant in the referent's namespace allows it. Same-namespace
// references never need one. It has no status — pure authorization data consumed
// by the other controllers.
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
