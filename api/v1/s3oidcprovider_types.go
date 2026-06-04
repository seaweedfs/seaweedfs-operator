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

// S3OIDCProviderSpec defines the desired state of a trusted OpenID Connect
// identity provider registered with a Seaweed cluster's embedded IAM service.
// Registering a provider lets clients exchange OIDC tokens from that issuer for
// temporary S3 credentials via STS AssumeRoleWithWebIdentity, declaratively and
// without the static -iam.config file (which freezes the dynamic IAM store).
type S3OIDCProviderSpec struct {
	// SeaweedRef points at the Seaweed cluster whose IAM service owns this
	// provider.
	// +kubebuilder:validation:Required
	SeaweedRef SeaweedReference `json:"seaweedRef"`

	// IssuerURL is the OIDC issuer URL (e.g. "https://accounts.google.com" or
	// "https://keycloak.example.com/realms/prod"). It is the provider's stable
	// identity, from which the IAM service derives the provider ARN. Immutable;
	// to point at a different issuer, create a new provider.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="issuerURL is immutable"
	IssuerURL string `json:"issuerURL"`

	// ClientIDs is the list of audiences (OIDC "aud" / client IDs) accepted
	// from this issuer. At least one is required.
	// +kubebuilder:validation:MinItems=1
	// +listType=set
	ClientIDs []string `json:"clientIDs"`

	// Thumbprints is the optional list of SHA-1 thumbprints of the issuer's TLS
	// signing certificates. Leave empty to let the IAM service fetch and pin
	// the issuer's JWKS over its trusted CA set.
	// +optional
	// +listType=set
	Thumbprints []string `json:"thumbprints,omitempty"`

	// ReclaimPolicy controls whether the underlying OIDC provider is removed
	// from the IAM service when this CR is deleted. Defaults to Delete.
	// +optional
	// +kubebuilder:default:=Delete
	ReclaimPolicy S3ReclaimPolicy `json:"reclaimPolicy,omitempty"`
}

// S3OIDCProviderStatus reflects the observed state of the OIDC provider.
type S3OIDCProviderStatus struct {
	// ObservedGeneration is the .metadata.generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is a coarse summary of the provider's lifecycle.
	// +optional
	Phase S3Phase `json:"phase,omitempty"`

	// Conditions are the structured per-aspect state signals.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// ProviderArn echoes the ARN the IAM service assigned to the registered
	// provider, derived from the cluster account ID and issuer URL.
	// +optional
	ProviderArn string `json:"providerArn,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=s3oidc,categories=seaweedfs
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.seaweedRef.name`
// +kubebuilder:printcolumn:name="Issuer",type=string,JSONPath=`.spec.issuerURL`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// S3OIDCProvider is the Schema for declaratively registering a trusted OpenID
// Connect identity provider with a Seaweed cluster's embedded IAM service, so
// OIDC/STS federation can be configured through the operator instead of the
// static -iam.config file.
type S3OIDCProvider struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   S3OIDCProviderSpec   `json:"spec,omitempty"`
	Status S3OIDCProviderStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// S3OIDCProviderList contains a list of S3OIDCProvider.
type S3OIDCProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []S3OIDCProvider `json:"items"`
}

func init() {
	SchemeBuilder.Register(&S3OIDCProvider{}, &S3OIDCProviderList{})
}
