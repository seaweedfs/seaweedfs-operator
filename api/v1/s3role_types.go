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

// S3OIDCProviderRef names an S3OIDCProvider in the referencing CR's namespace.
type S3OIDCProviderRef struct {
	// Name of the S3OIDCProvider.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// S3RoleWebIdentity admits OIDC tokens from one trusted provider whose
// subject is in an explicit list. The controller turns it into the role's
// trust policy, so the SeaweedFS-specific form (the provider named by its
// issuer URL, the subject matched with the "oidc:sub" condition key) never
// has to be written by hand.
type S3RoleWebIdentity struct {
	// ProviderRef names the S3OIDCProvider whose tokens may assume the role.
	// It must target the same Seaweed cluster. The token's audience is
	// checked against that provider's clientIDs.
	// +kubebuilder:validation:Required
	ProviderRef S3OIDCProviderRef `json:"providerRef"`

	// Subjects is the exact set of token "sub" claims allowed to assume the
	// role — for a SPIFFE issuer, SPIFFE IDs such as
	// "spiffe://example.org/ns/app/sa/app". Matched exactly; no wildcards,
	// and no "${", which the IAM policy engine would expand as a variable.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=100
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=1024
	// +kubebuilder:validation:XValidation:rule="self.all(s, !s.contains('${'))",message="a subject must not contain '${': the policy engine expands it as a variable"
	// +listType=set
	Subjects []string `json:"subjects"`
}

// S3RoleSpec defines the desired state of an STS role: who may assume it
// (webIdentity) and what the resulting temporary credentials may do
// (policyRefs).
type S3RoleSpec struct {
	// SeaweedRef points at the Seaweed cluster whose IAM service owns this
	// role. Immutable; roles cannot be moved between clusters.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="seaweedRef is immutable"
	SeaweedRef SeaweedReference `json:"seaweedRef"`

	// Name is the IAM role name; the role ARN is arn:aws:iam::role/<name>.
	// Defaults to .metadata.name. Immutable once set. Role names are global
	// to the cluster: the oldest CR claiming a name owns it, and later
	// claimants are marked Failed with a Conflict condition.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[\w+=,.@-]+$`
	// +kubebuilder:validation:XValidation:rule="oldSelf == '' || self == oldSelf",message="role name is immutable once set"
	Name string `json:"name,omitempty"`

	// WebIdentity is who may assume the role with AssumeRoleWithWebIdentity.
	// +kubebuilder:validation:Required
	WebIdentity S3RoleWebIdentity `json:"webIdentity"`

	// PolicyRefs are the IAM policies granted to sessions of this role. Each
	// name resolves to an S3Policy in the same namespace (its effective IAM
	// policy name), or is taken as an IAM policy name. The controller waits
	// until every policy exists. At most 10, SeaweedFS's per-role quota.
	// +optional
	// +kubebuilder:validation:MaxItems=10
	// +listType=map
	// +listMapKey=name
	PolicyRefs []S3PolicyRef `json:"policyRefs,omitempty"`

	// Description is an optional human-readable description.
	// +optional
	// +kubebuilder:validation:MaxLength=1000
	Description string `json:"description,omitempty"`

	// MaxSessionDuration caps, in seconds, the sessions of this role.
	// Defaults to the cluster's STS setting.
	// +optional
	// +kubebuilder:validation:Minimum=3600
	// +kubebuilder:validation:Maximum=43200
	MaxSessionDuration int64 `json:"maxSessionDuration,omitempty"`

	// ReclaimPolicy controls whether the underlying role is deleted when this
	// CR is removed. Defaults to Delete.
	// +optional
	// +kubebuilder:default:=Delete
	ReclaimPolicy S3ReclaimPolicy `json:"reclaimPolicy,omitempty"`
}

// S3RoleStatus reflects the observed state of the role.
type S3RoleStatus struct {
	// ObservedGeneration is the .metadata.generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is a coarse summary of the role's lifecycle.
	// +optional
	Phase S3Phase `json:"phase,omitempty"`

	// Conditions are the structured per-aspect state signals.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// RoleName echoes the resolved IAM role name actually used.
	// +optional
	RoleName string `json:"roleName,omitempty"`

	// RoleArn is the ARN to pass as RoleArn to AssumeRoleWithWebIdentity.
	// +optional
	RoleArn string `json:"roleArn,omitempty"`

	// AttachedPolicies are the resolved IAM policy names last applied.
	// +optional
	AttachedPolicies []string `json:"attachedPolicies,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=s3role,categories=seaweedfs
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.seaweedRef.name`
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=`.status.roleName`
// +kubebuilder:printcolumn:name="ARN",type=string,JSONPath=`.status.roleArn`,priority=1
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// S3Role is the Schema for declaratively provisioning an STS role in a Seaweed
// cluster's embedded IAM service, so a workload holding an OIDC token (for
// example a SPIFFE JWT-SVID) can exchange it for temporary S3 credentials
// with AssumeRoleWithWebIdentity instead of holding static access keys.
type S3Role struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   S3RoleSpec   `json:"spec,omitempty"`
	Status S3RoleStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// S3RoleList contains a list of S3Role.
type S3RoleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []S3Role `json:"items"`
}

func init() {
	SchemeBuilder.Register(&S3Role{}, &S3RoleList{})
}
