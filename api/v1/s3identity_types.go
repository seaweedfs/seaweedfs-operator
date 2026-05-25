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

// S3Account carries the optional human-facing account attributes attached to
// an IAM identity. All fields are optional; SeaweedFS stores them verbatim.
type S3Account struct {
	// DisplayName is a human-readable name for the identity.
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// Email is the contact e-mail recorded on the identity.
	// +optional
	Email string `json:"email,omitempty"`
}

// S3IdentitySpec defines the desired state of an S3 IAM identity (user).
//
// An identity is created with no credentials by default; attach credentials
// with one or more S3Credentials resources and permissions with S3Policy /
// S3PolicyBinding. This separation lets credentials rotate and policies
// change without touching the identity itself.
type S3IdentitySpec struct {
	// SeaweedRef points at the Seaweed cluster whose IAM service owns this
	// identity.
	// +kubebuilder:validation:Required
	SeaweedRef SeaweedReference `json:"seaweedRef"`

	// Name is the IAM user name. Defaults to .metadata.name. Immutable once
	// set — IAM user renames are not supported, recreate the resource to
	// change the name.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:XValidation:rule="oldSelf == '' || self == oldSelf",message="identity name is immutable once set"
	Name string `json:"name,omitempty"`

	// ReclaimPolicy controls whether the underlying IAM user is deleted when
	// this CR is removed. Defaults to Delete.
	// +optional
	// +kubebuilder:default:=Delete
	ReclaimPolicy S3ReclaimPolicy `json:"reclaimPolicy,omitempty"`

	// Disabled disables the identity without deleting it. A disabled
	// identity cannot authenticate with any of its credentials. Defaults to
	// false.
	// +optional
	// +kubebuilder:default:=false
	Disabled bool `json:"disabled,omitempty"`

	// Account carries optional display-name / e-mail attributes for the
	// identity.
	// +optional
	Account *S3Account `json:"account,omitempty"`
}

// S3IdentityStatus reflects the observed state of the identity.
type S3IdentityStatus struct {
	// ObservedGeneration is the .metadata.generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is a coarse summary of the identity's lifecycle.
	// +optional
	Phase S3Phase `json:"phase,omitempty"`

	// Conditions are the structured per-aspect state signals.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// IdentityName echoes the resolved IAM user name actually used (Spec.Name
	// or the metadata.name fallback). Recorded on first successful reconcile
	// and used to detect rename attempts.
	// +optional
	IdentityName string `json:"identityName,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=s3id,categories=seaweedfs
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.seaweedRef.name`
// +kubebuilder:printcolumn:name="User",type=string,JSONPath=`.status.identityName`
// +kubebuilder:printcolumn:name="Disabled",type=boolean,JSONPath=`.spec.disabled`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// S3Identity is the Schema for declaratively provisioning an S3 IAM identity
// (user) inside a Seaweed cluster's embedded IAM service.
type S3Identity struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   S3IdentitySpec   `json:"spec,omitempty"`
	Status S3IdentityStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// S3IdentityList contains a list of S3Identity.
type S3IdentityList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []S3Identity `json:"items"`
}

func init() {
	SchemeBuilder.Register(&S3Identity{}, &S3IdentityList{})
}
