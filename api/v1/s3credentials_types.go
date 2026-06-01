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

// S3IdentityRef references the IAM identity (user) that owns a credential.
// The Name is the IAM user name, which equals the target S3Identity's
// resolved name (its spec.name, or metadata.name when spec.name is unset).
type S3IdentityRef struct {
	// Name is the IAM user name the credential belongs to.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Name string `json:"name"`
}

// S3SecretRef points at the Kubernetes Secret that stores the access key and
// secret key. The Secret defaults to the same namespace as the S3Credentials
// resource; set Namespace to reference a Secret in another namespace.
//
// On first reconcile the controller reads the Secret: if both keys are
// already present it adopts them (registering the access key on the IAM user
// if needed); otherwise it generates a fresh key pair and writes it back.
// A Secret created by the controller is labelled as operator-managed and is
// removed together with the IAM access key when the CR is deleted with
// reclaimPolicy: Delete. A pre-existing (user-managed) Secret is never
// deleted by the controller.
//
// A cross-namespace reference is denied unless a ResourceReferenceGrant in the
// Secret's namespace permits it (the CR stays Pending until then). A
// cross-namespace Secret must already exist; the controller never creates
// Secrets in foreign namespaces.
type S3SecretRef struct {
	// Name of the Secret. Defaults to .metadata.name of the S3Credentials.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name,omitempty"`

	// Namespace of the Secret. Defaults to the namespace of the S3Credentials
	// resource. When set to a different namespace the Secret must already
	// exist; the controller will not create Secrets in foreign namespaces.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// AccessKeyField is the key under which the access key id is stored in
	// the Secret. Defaults to "accessKey".
	// +optional
	// +kubebuilder:default:=accessKey
	// +kubebuilder:validation:MinLength=1
	AccessKeyField string `json:"accessKeyField,omitempty"`

	// SecretKeyField is the key under which the secret access key is stored
	// in the Secret. Defaults to "secretKey".
	// +optional
	// +kubebuilder:default:=secretKey
	// +kubebuilder:validation:MinLength=1
	SecretKeyField string `json:"secretKeyField,omitempty"`
}

// S3CredentialsSpec defines the desired state of an S3 IAM access key pair
// bound to an identity and mirrored into a Kubernetes Secret.
type S3CredentialsSpec struct {
	// SeaweedRef points at the Seaweed cluster whose IAM service owns the
	// identity.
	// +kubebuilder:validation:Required
	SeaweedRef SeaweedReference `json:"seaweedRef"`

	// IdentityRef names the IAM identity (user) the credential belongs to.
	// The identity must already exist (typically managed by an S3Identity
	// resource); the controller waits for it otherwise. Immutable.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="identityRef is immutable"
	IdentityRef S3IdentityRef `json:"identityRef"`

	// SecretRef selects the Kubernetes Secret that stores the key pair.
	// +optional
	SecretRef S3SecretRef `json:"secretRef,omitempty"`

	// ReclaimPolicy controls whether the underlying IAM access key (and any
	// operator-created Secret) is deleted when this CR is removed. Defaults
	// to Delete.
	// +optional
	// +kubebuilder:default:=Delete
	ReclaimPolicy S3ReclaimPolicy `json:"reclaimPolicy,omitempty"`
}

// S3CredentialsStatus reflects the observed state of the credential.
type S3CredentialsStatus struct {
	// ObservedGeneration is the .metadata.generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is a coarse summary of the credential's lifecycle.
	// +optional
	Phase S3Phase `json:"phase,omitempty"`

	// Conditions are the structured per-aspect state signals.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// AccessKey is the public access key id provisioned for the identity.
	// The secret key is never written to status — it lives only in the
	// referenced Secret.
	// +optional
	AccessKey string `json:"accessKey,omitempty"`

	// SecretName is the resolved name of the Secret holding the key pair.
	// +optional
	SecretName string `json:"secretName,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=s3cred,categories=seaweedfs
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.seaweedRef.name`
// +kubebuilder:printcolumn:name="Identity",type=string,JSONPath=`.spec.identityRef.name`
// +kubebuilder:printcolumn:name="AccessKey",type=string,JSONPath=`.status.accessKey`
// +kubebuilder:printcolumn:name="Secret",type=string,JSONPath=`.status.secretName`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// S3Credentials is the Schema for declaratively provisioning an S3 access key
// pair for an identity and mirroring it into a Kubernetes Secret.
type S3Credentials struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   S3CredentialsSpec   `json:"spec,omitempty"`
	Status S3CredentialsStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// S3CredentialsList contains a list of S3Credentials.
type S3CredentialsList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []S3Credentials `json:"items"`
}

func init() {
	SchemeBuilder.Register(&S3Credentials{}, &S3CredentialsList{})
}
