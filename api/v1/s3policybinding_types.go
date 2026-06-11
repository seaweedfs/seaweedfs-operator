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

// S3PolicyRef references the IAM policy to attach. The Name resolves to an
// S3Policy of that name in the same namespace (using its effective IAM policy
// name, so a spec.name override is transparent); when no such S3Policy
// exists, Name is taken as the IAM policy name itself.
type S3PolicyRef struct {
	// Name of the S3Policy in the same namespace, or the IAM policy name
	// for a policy not managed by an S3Policy resource.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	Name string `json:"name"`
}

// S3SubjectKind enumerates the kinds of subject a policy can be bound to.
// Only S3Identity is supported today.
// +kubebuilder:validation:Enum=S3Identity
type S3SubjectKind string

const (
	S3SubjectKindIdentity S3SubjectKind = "S3Identity"
)

// S3Subject is a single principal a policy is attached to.
type S3Subject struct {
	// Kind is the subject kind. Only S3Identity is supported. Defaults to
	// S3Identity.
	// +optional
	// +kubebuilder:default:=S3Identity
	Kind S3SubjectKind `json:"kind,omitempty"`

	// Name of the S3Identity in the same namespace, or the IAM user name
	// for an identity not managed by an S3Identity resource.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Name string `json:"name"`
}

// S3PolicyBindingSpec defines the desired attachment of a policy to a set of
// identities. The controller reconciles to exactly this set — identities
// removed from subjects have the policy detached (the identity itself is left
// intact).
type S3PolicyBindingSpec struct {
	// SeaweedRef points at the Seaweed cluster whose IAM service owns the
	// policy and identities.
	// +kubebuilder:validation:Required
	SeaweedRef SeaweedReference `json:"seaweedRef"`

	// PolicyRef names the IAM policy to attach. The policy must already
	// exist (typically managed by an S3Policy resource); the controller
	// waits for it otherwise. Immutable.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="policyRef is immutable"
	PolicyRef S3PolicyRef `json:"policyRef"`

	// Subjects is the set of identities the policy is attached to. The
	// controller reconciles to exactly this set (and deduplicates it during
	// reconcile); keying the list by name additionally rejects duplicate
	// subjects at admission. At least one subject is required.
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=name
	Subjects []S3Subject `json:"subjects"`

	// ReclaimPolicy controls whether the policy is detached from its
	// subjects when this CR is removed. Defaults to Delete (detach).
	// +optional
	// +kubebuilder:default:=Delete
	ReclaimPolicy S3ReclaimPolicy `json:"reclaimPolicy,omitempty"`
}

// S3PolicyBindingStatus reflects the observed state of the binding.
type S3PolicyBindingStatus struct {
	// ObservedGeneration is the .metadata.generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is a coarse summary of the binding's lifecycle.
	// +optional
	Phase S3Phase `json:"phase,omitempty"`

	// Conditions are the structured per-aspect state signals.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// AttachedSubjects lists the IAM user names the policy is currently
	// attached to.
	// +optional
	// +listType=atomic
	AttachedSubjects []string `json:"attachedSubjects,omitempty"`

	// PolicyName is the IAM policy name the policyRef resolved to.
	// +optional
	PolicyName string `json:"policyName,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=s3pb,categories=seaweedfs
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.seaweedRef.name`
// +kubebuilder:printcolumn:name="Policy",type=string,JSONPath=`.spec.policyRef.name`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// S3PolicyBinding is the Schema for declaratively attaching an S3 IAM policy
// to one or more identities inside a Seaweed cluster's embedded IAM service.
type S3PolicyBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   S3PolicyBindingSpec   `json:"spec,omitempty"`
	Status S3PolicyBindingStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// S3PolicyBindingList contains a list of S3PolicyBinding.
type S3PolicyBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []S3PolicyBinding `json:"items"`
}

func init() {
	SchemeBuilder.Register(&S3PolicyBinding{}, &S3PolicyBindingList{})
}
