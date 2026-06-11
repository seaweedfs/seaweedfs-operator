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

// S3PolicyEffect is the effect of a policy statement.
// +kubebuilder:validation:Enum=Allow;Deny
type S3PolicyEffect string

const (
	S3PolicyEffectAllow S3PolicyEffect = "Allow"
	S3PolicyEffectDeny  S3PolicyEffect = "Deny"
)

// S3PolicyStatement is one statement of an AWS-style S3 policy document. The
// controller assembles the spec statements into a policy document and stores
// it via the IAM PutPolicy API.
type S3PolicyStatement struct {
	// Sid is an optional statement identifier.
	// +optional
	Sid string `json:"sid,omitempty"`

	// Effect is whether the statement allows or denies the listed actions.
	// +kubebuilder:validation:Required
	Effect S3PolicyEffect `json:"effect"`

	// Actions is the list of S3 actions the statement applies to, in AWS
	// form (e.g. "s3:GetObject", "s3:PutObject", "s3:ListBucket"). The
	// wildcards "*" and "s3:*" are accepted. At least one action is required.
	// +kubebuilder:validation:MinItems=1
	// +listType=set
	Actions []string `json:"actions"`

	// Resources is the list of resources the statement applies to. Entries
	// may be full ARNs (e.g. "arn:aws:s3:::my-bucket/*") or the convenient
	// bucket-relative shorthand ("my-bucket", "my-bucket/*"), which the
	// controller expands to "arn:aws:s3:::my-bucket" /
	// "arn:aws:s3:::my-bucket/*". The bare wildcard "*" is passed through.
	// At least one resource is required.
	// +kubebuilder:validation:MinItems=1
	// +listType=set
	Resources []string `json:"resources"`
}

// S3PolicySpec defines the desired state of an S3 IAM policy.
//
// Provide exactly one of statements (the friendly, structured form) or
// policyDocument (a raw AWS-style JSON document, for full control / features
// not modelled by statements).
//
// +kubebuilder:validation:XValidation:rule="(has(self.statements) && size(self.statements) > 0) != (has(self.policyDocument) && size(self.policyDocument) > 0)",message="set exactly one of spec.statements or spec.policyDocument"
type S3PolicySpec struct {
	// SeaweedRef points at the Seaweed cluster whose IAM service owns this
	// policy.
	// +kubebuilder:validation:Required
	SeaweedRef SeaweedReference `json:"seaweedRef"`

	// Name is the IAM policy name. Defaults to .metadata.name. Immutable
	// once set. IAM policy names are global to the cluster: the oldest CR
	// claiming a name owns it, and later claimants are marked Failed with
	// a Conflict condition.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:XValidation:rule="oldSelf == '' || self == oldSelf",message="policy name is immutable once set"
	Name string `json:"name,omitempty"`

	// ReclaimPolicy controls whether the underlying IAM policy is deleted
	// when this CR is removed. Defaults to Delete.
	// +optional
	// +kubebuilder:default:=Delete
	ReclaimPolicy S3ReclaimPolicy `json:"reclaimPolicy,omitempty"`

	// Statements is the structured form of the policy. Mutually exclusive
	// with policyDocument.
	// +optional
	// +listType=atomic
	Statements []S3PolicyStatement `json:"statements,omitempty"`

	// PolicyDocument is a raw AWS-style policy document JSON. Mutually
	// exclusive with statements. Use this for advanced features (conditions,
	// principals, NotResource) not modelled by statements.
	// +optional
	PolicyDocument string `json:"policyDocument,omitempty"`
}

// S3PolicyStatus reflects the observed state of the policy.
type S3PolicyStatus struct {
	// ObservedGeneration is the .metadata.generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is a coarse summary of the policy's lifecycle.
	// +optional
	Phase S3Phase `json:"phase,omitempty"`

	// Conditions are the structured per-aspect state signals.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// PolicyName echoes the resolved IAM policy name actually used (Spec.Name
	// or the metadata.name fallback).
	// +optional
	PolicyName string `json:"policyName,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=s3pol,categories=seaweedfs
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.seaweedRef.name`
// +kubebuilder:printcolumn:name="Policy",type=string,JSONPath=`.status.policyName`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// S3Policy is the Schema for declaratively provisioning an S3 IAM policy
// inside a Seaweed cluster's embedded IAM service. Attach it to identities
// with S3PolicyBinding.
type S3Policy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   S3PolicySpec   `json:"spec,omitempty"`
	Status S3PolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// S3PolicyList contains a list of S3Policy.
type S3PolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []S3Policy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&S3Policy{}, &S3PolicyList{})
}
