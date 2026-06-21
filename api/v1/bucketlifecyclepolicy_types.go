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

// BucketLifecycleRuleStatus toggles whether a single rule is acted on.
// +kubebuilder:validation:Enum=Enabled;Disabled
type BucketLifecycleRuleStatus string

const (
	BucketLifecycleRuleEnabled  BucketLifecycleRuleStatus = "Enabled"
	BucketLifecycleRuleDisabled BucketLifecycleRuleStatus = "Disabled"
)

// BucketLifecycleRef points at the Bucket whose lifecycle this policy manages.
// The Bucket must live in the same namespace; the cluster and the resolved
// bucket name are taken from it.
type BucketLifecycleRef struct {
	// Name of the Bucket CR in the same namespace.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// BucketLifecycleExpiration expires current object versions.
// +kubebuilder:validation:XValidation:rule="has(self.days) || (has(self.expiredObjectDeleteMarker) && self.expiredObjectDeleteMarker)",message="expiration must set days or expiredObjectDeleteMarker"
type BucketLifecycleExpiration struct {
	// Days is the object age in days after which it is expired.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Days int32 `json:"days,omitempty"`

	// ExpiredObjectDeleteMarker removes a version's delete marker once it is
	// the only version left. Applies to versioned buckets only.
	// +optional
	ExpiredObjectDeleteMarker bool `json:"expiredObjectDeleteMarker,omitempty"`
}

// BucketLifecycleNoncurrentVersionExpiration expires non-current versions of
// objects in a versioned bucket.
type BucketLifecycleNoncurrentVersionExpiration struct {
	// NoncurrentDays is the number of days a version stays non-current before
	// it is expired.
	// +kubebuilder:validation:Minimum=1
	NoncurrentDays int32 `json:"noncurrentDays"`

	// NewerNoncurrentVersions retains this many newer non-current versions
	// before expiring the rest. Zero (the default) expires all eligible
	// non-current versions.
	// +optional
	// +kubebuilder:validation:Minimum=0
	NewerNoncurrentVersions int32 `json:"newerNoncurrentVersions,omitempty"`
}

// BucketLifecycleAbortIncompleteMultipartUpload aborts multipart uploads that
// were never completed.
type BucketLifecycleAbortIncompleteMultipartUpload struct {
	// DaysAfterInitiation is the number of days after a multipart upload is
	// started before it is aborted.
	// +kubebuilder:validation:Minimum=1
	DaysAfterInitiation int32 `json:"daysAfterInitiation"`
}

// BucketLifecycleRule is one S3 lifecycle rule. Each rule must carry at least
// one action.
// +kubebuilder:validation:XValidation:rule="has(self.expiration) || has(self.abortIncompleteMultipartUpload) || has(self.noncurrentVersionExpiration)",message="rule must set at least one of expiration, abortIncompleteMultipartUpload, or noncurrentVersionExpiration"
type BucketLifecycleRule struct {
	// ID uniquely identifies the rule within the policy.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	ID string `json:"id"`

	// Prefix limits the rule to object keys under this prefix. Empty matches
	// every object in the bucket.
	// +optional
	Prefix string `json:"prefix,omitempty"`

	// Status enables or disables the rule. Defaults to Enabled.
	// +optional
	// +kubebuilder:default:=Enabled
	Status BucketLifecycleRuleStatus `json:"status,omitempty"`

	// Expiration expires current object versions.
	// +optional
	Expiration *BucketLifecycleExpiration `json:"expiration,omitempty"`

	// NoncurrentVersionExpiration expires non-current versions in a versioned
	// bucket.
	// +optional
	NoncurrentVersionExpiration *BucketLifecycleNoncurrentVersionExpiration `json:"noncurrentVersionExpiration,omitempty"`

	// AbortIncompleteMultipartUpload aborts stale multipart uploads.
	// +optional
	AbortIncompleteMultipartUpload *BucketLifecycleAbortIncompleteMultipartUpload `json:"abortIncompleteMultipartUpload,omitempty"`
}

// BucketLifecyclePolicySpec defines the desired lifecycle configuration of a
// bucket. The controller reconciles the bucket's lifecycle to exactly these
// rules, so a single policy owns a bucket's whole lifecycle configuration.
type BucketLifecyclePolicySpec struct {
	// BucketRef points at the Bucket whose lifecycle is managed. Immutable.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="bucketRef is immutable"
	BucketRef BucketLifecycleRef `json:"bucketRef"`

	// Rules is the set of lifecycle rules, keyed by id so duplicate ids are
	// rejected at admission.
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=id
	Rules []BucketLifecycleRule `json:"rules"`

	// ReclaimPolicy controls whether the lifecycle rules are removed from the
	// bucket when this CR is deleted. Defaults to Delete.
	// +optional
	// +kubebuilder:default:=Delete
	ReclaimPolicy BucketReclaimPolicy `json:"reclaimPolicy,omitempty"`
}

// Condition types emitted by the lifecycle policy controller.
const (
	// BucketLifecyclePolicyConditionReady summarises whether the bucket's
	// lifecycle matches spec.
	BucketLifecyclePolicyConditionReady = "Ready"
	// BucketLifecyclePolicyConditionBucketResolved reports whether bucketRef
	// resolves to a provisioned Bucket.
	BucketLifecyclePolicyConditionBucketResolved = "BucketResolved"
)

// BucketLifecyclePolicyStatus reflects the observed state of the policy.
type BucketLifecyclePolicyStatus struct {
	// ObservedGeneration is the .metadata.generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is a coarse summary of the policy's lifecycle.
	// +optional
	Phase BucketPhase `json:"phase,omitempty"`

	// Conditions are the structured per-aspect state signals.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// BucketName is the resolved bucket name the rules were applied to.
	// +optional
	BucketName string `json:"bucketName,omitempty"`

	// AppliedRules is the number of rules currently applied to the bucket.
	// +optional
	AppliedRules int32 `json:"appliedRules,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=swblp,categories=seaweedfs
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Bucket",type=string,JSONPath=`.spec.bucketRef.name`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Rules",type=integer,JSONPath=`.status.appliedRules`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// BucketLifecyclePolicy is the Schema for declaratively managing the S3
// lifecycle configuration of a SeaweedFS bucket.
type BucketLifecyclePolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BucketLifecyclePolicySpec   `json:"spec,omitempty"`
	Status BucketLifecyclePolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BucketLifecyclePolicyList contains a list of BucketLifecyclePolicy.
type BucketLifecyclePolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BucketLifecyclePolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BucketLifecyclePolicy{}, &BucketLifecyclePolicyList{})
}
