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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BucketNotificationEventType classifies a filer metadata event, matching
// the create/update/delete/rename taxonomy the filer's notification queues
// use (see detectEventType in weed/notification/webhook).
// +kubebuilder:validation:Enum=create;update;delete;rename
type BucketNotificationEventType string

const (
	BucketNotificationEventCreate BucketNotificationEventType = "create"
	BucketNotificationEventUpdate BucketNotificationEventType = "update"
	BucketNotificationEventDelete BucketNotificationEventType = "delete"
	BucketNotificationEventRename BucketNotificationEventType = "rename"
)

// BucketNotificationBucketRef points at the Bucket whose object events this
// notification set subscribes to. The Bucket must live in the same namespace;
// the cluster and the resolved bucket name are taken from it.
type BucketNotificationBucketRef struct {
	// Name of the Bucket CR in the same namespace.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// BucketNotificationKafkaDestination publishes matched events to a Kafka
// topic. Each rule may target a different topic — and, once the
// notification bridge supports it, a different broker list — which is what
// distinguishes this from the single global [notification.kafka] block in
// notification.toml.
type BucketNotificationKafkaDestination struct {
	// Brokers is the list of host:port bootstrap servers.
	// +kubebuilder:validation:MinItems=1
	Brokers []string `json:"brokers"`

	// Topic is the Kafka topic events are published to.
	// +kubebuilder:validation:MinLength=1
	Topic string `json:"topic"`

	// CredentialsSecretRef references a Secret in the BucketNotification's
	// namespace carrying SASL/TLS material for the broker connection.
	// Recognized keys: sasl_username, sasl_password, sasl_mechanism,
	// tls_ca_cert, tls_client_cert, tls_client_key. Omit for a plaintext,
	// unauthenticated broker.
	// +optional
	CredentialsSecretRef *corev1.LocalObjectReference `json:"credentialsSecretRef,omitempty"`
}

// BucketNotificationWebhookDestination posts matched events to an HTTP
// endpoint, mirroring the filer's [notification.webhook] queue semantics.
type BucketNotificationWebhookDestination struct {
	// Endpoint is the HTTP(S) URL events are POSTed to.
	// +kubebuilder:validation:MinLength=1
	Endpoint string `json:"endpoint"`

	// BearerTokenSecretRef references a Secret key holding the bearer token
	// sent in the Authorization header. Omit for an unauthenticated endpoint.
	// +optional
	BearerTokenSecretRef *corev1.SecretKeySelector `json:"bearerTokenSecretRef,omitempty"`
}

// BucketNotificationDestination selects where a rule's matched events are
// published. Exactly one destination type must be set.
// +kubebuilder:validation:XValidation:rule="(has(self.kafka) ? 1 : 0) + (has(self.webhook) ? 1 : 0) == 1",message="exactly one of kafka or webhook must be set"
type BucketNotificationDestination struct {
	// Kafka publishes to a Kafka topic.
	// +optional
	Kafka *BucketNotificationKafkaDestination `json:"kafka,omitempty"`

	// Webhook posts to an HTTP endpoint.
	// +optional
	Webhook *BucketNotificationWebhookDestination `json:"webhook,omitempty"`
}

// BucketNotificationRule is one event-notification rule: events on objects
// in the bucket matching the prefix and event-type filters are published to
// the destination.
type BucketNotificationRule struct {
	// Name uniquely identifies the rule within this notification set. Rules
	// on the same bucket must have unique names across all BucketNotification
	// resources targeting it — the owning CR aggregates by name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// Prefix limits the rule to object keys under this bucket-relative
	// prefix (e.g. "incoming/"). Empty matches every object in the bucket.
	// +optional
	Prefix string `json:"prefix,omitempty"`

	// EventTypes limits the rule to these event types. Empty matches all
	// types. Declared as a set so duplicates are rejected at admission.
	// +optional
	// +listType=set
	EventTypes []BucketNotificationEventType `json:"eventTypes,omitempty"`

	// Destination is where matched events are published.
	// +kubebuilder:validation:Required
	Destination BucketNotificationDestination `json:"destination"`
}

// BucketNotificationSpec defines the desired notification rules for a bucket.
// Multiple BucketNotification resources may reference the same bucket; their
// rule sets are aggregated by name.
type BucketNotificationSpec struct {
	// BucketRef points at the Bucket whose events are managed. Immutable.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="bucketRef is immutable"
	BucketRef BucketNotificationBucketRef `json:"bucketRef"`

	// Rules is the set of notification rules, keyed by name so duplicate
	// names are rejected at admission.
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=name
	Rules []BucketNotificationRule `json:"rules"`
}

// Condition types emitted by the bucket notification controller.
const (
	// BucketNotificationConditionReady summarises whether the rules are
	// active for the bucket.
	BucketNotificationConditionReady = "Ready"
	// BucketNotificationConditionBucketResolved reports whether bucketRef
	// resolves to a provisioned Bucket.
	BucketNotificationConditionBucketResolved = "BucketResolved"
	// BucketNotificationConditionRuleConflict reports that a rule name on
	// this resource collides with a rule of the same name on another
	// BucketNotification targeting the same bucket.
	BucketNotificationConditionRuleConflict = "RuleConflict"
)

// BucketNotificationStatus reflects the observed state of the notification set.
type BucketNotificationStatus struct {
	// ObservedGeneration is the .metadata.generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is a coarse summary of the resource's lifecycle.
	// +optional
	Phase BucketPhase `json:"phase,omitempty"`

	// Conditions are the structured per-aspect state signals.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// BucketName is the resolved bucket name the rules apply to, recorded on
	// first successful reconcile.
	// +optional
	BucketName string `json:"bucketName,omitempty"`

	// ClusterName and ClusterNamespace record the Seaweed cluster the rules
	// target, so cleanup stays possible after the referenced Bucket is gone.
	// +optional
	ClusterName string `json:"clusterName,omitempty"`
	// +optional
	ClusterNamespace string `json:"clusterNamespace,omitempty"`

	// AppliedRules is the number of rules currently active on the bucket.
	// +optional
	AppliedRules int32 `json:"appliedRules,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=swbn,categories=seaweedfs
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Bucket",type=string,JSONPath=`.spec.bucketRef.name`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Rules",type=integer,JSONPath=`.status.appliedRules`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// BucketNotification is the Schema for declaratively managing event
// notification rules on a SeaweedFS bucket — for example, publishing objects
// created under one prefix to a Kafka topic while updates go to another.
//
// The controller resolves the referenced Bucket and validates the rules; the
// events themselves are delivered by the operator-managed notification
// bridge, which subscribes to the filer's metadata stream and fans out
// per rule.
type BucketNotification struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BucketNotificationSpec   `json:"spec,omitempty"`
	Status BucketNotificationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BucketNotificationList contains a list of BucketNotification.
type BucketNotificationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BucketNotification `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BucketNotification{}, &BucketNotificationList{})
}
