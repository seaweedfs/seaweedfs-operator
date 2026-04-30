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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// VersioningState is the desired versioning configuration for a bucket.
// Once a bucket has been Enabled or Suspended it cannot be returned to Off
// — Suspended is the correct state for "stop adding versions but keep the
// existing version history".
// +kubebuilder:validation:Enum=Off;Enabled;Suspended
type VersioningState string

const (
	VersioningOff       VersioningState = "Off"
	VersioningEnabled   VersioningState = "Enabled"
	VersioningSuspended VersioningState = "Suspended"
)

// BucketReclaimPolicy controls what happens to the underlying SeaweedFS
// bucket when the Bucket CR is removed.
// +kubebuilder:validation:Enum=Retain;Delete
type BucketReclaimPolicy string

const (
	// BucketReclaimRetain leaves the bucket and its contents untouched in
	// the filer when the Bucket CR is deleted. This is the default and the
	// safe choice for production data.
	BucketReclaimRetain BucketReclaimPolicy = "Retain"

	// BucketReclaimDelete deletes the bucket via s3.bucket.delete after the
	// CR is removed. The delete is refused (with a DeleteBlockedByRetention
	// condition) if Object Lock retention still applies to any object in
	// the bucket; flip the policy back to Retain and clear retention
	// manually if you really need to override.
	BucketReclaimDelete BucketReclaimPolicy = "Delete"
)

// BucketAccessAction is one of the actions accepted by `s3.bucket.access`.
// +kubebuilder:validation:Enum=Read;Write;List;Tagging;Admin
type BucketAccessAction string

const (
	BucketAccessRead    BucketAccessAction = "Read"
	BucketAccessWrite   BucketAccessAction = "Write"
	BucketAccessList    BucketAccessAction = "List"
	BucketAccessTagging BucketAccessAction = "Tagging"
	BucketAccessAdmin   BucketAccessAction = "Admin"
)

// BucketClusterRef identifies the Seaweed cluster that hosts the bucket.
// Cross-namespace references are allowed; the controller performs a
// SubjectAccessReview against the requester before reconciling so that
// RBAC on the referenced Seaweed CR is respected.
type BucketClusterRef struct {
	// Name of the Seaweed CR.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace of the Seaweed CR. Defaults to the Bucket's own namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// BucketQuota caps the bucket's total stored size.
type BucketQuota struct {
	// Size is the maximum total stored size for the bucket (e.g., "100Gi").
	// The controller converts this to MiB for the underlying s3.bucket.quota
	// command. Must be non-negative.
	// +kubebuilder:validation:Required
	Size resource.Quantity `json:"size"`

	// Enforce toggles quota enforcement. When false, the limit is recorded
	// but writes are not blocked. Defaults to true.
	// +optional
	// +kubebuilder:default:=true
	Enforce bool `json:"enforce,omitempty"`
}

// BucketAccessGrant grants a set of actions on the bucket to a single IAM
// identity. The identity must already exist in the SeaweedFS IAM service —
// the controller does not create users on your behalf.
type BucketAccessGrant struct {
	// User is the IAM identity name.
	// +kubebuilder:validation:MinLength=1
	User string `json:"user"`

	// Actions is the set of bucket-scoped actions granted to the user.
	// An empty list strips all bucket grants for the user (without deleting
	// the IAM identity itself).
	// +kubebuilder:validation:MinItems=0
	Actions []BucketAccessAction `json:"actions"`
}

// BucketPlacement mirrors `weed shell fs.configure -locationPrefix=/buckets/<name>/`.
// `collection` is intentionally absent — it is always equal to the bucket
// name and is not configurable.
type BucketPlacement struct {
	// Replication string in SeaweedFS three-digit form, e.g. "001" or "010".
	// See https://github.com/seaweedfs/seaweedfs/wiki/Replication.
	// +optional
	// +kubebuilder:validation:Pattern=`^[0-9]{3}$`
	Replication string `json:"replication,omitempty"`

	// DiskType selects the storage tier. Common values: "hdd", "ssd", or a
	// custom tag matching what volumes advertise.
	// +optional
	DiskType string `json:"diskType,omitempty"`

	// TTL is the default object TTL applied under this bucket prefix
	// (e.g., "30d", "1h"). Format follows SeaweedFS:
	// <integer><m|h|d|w|M|y>. Omit to leave TTL unset.
	// +optional
	// +kubebuilder:validation:Pattern=`^[1-9][0-9]*[mhdwMy]$`
	TTL string `json:"ttl,omitempty"`

	// Fsync forces an fsync after every write under this prefix.
	// +optional
	Fsync bool `json:"fsync,omitempty"`

	// WORM (write-once-read-many) makes written files read-only.
	// +optional
	WORM bool `json:"worm,omitempty"`

	// ReadOnly disables further writes under this prefix.
	// +optional
	ReadOnly bool `json:"readOnly,omitempty"`

	// DataCenter pins writes for this bucket to a specific data center.
	// +optional
	DataCenter string `json:"dataCenter,omitempty"`

	// Rack pins writes for this bucket to a specific rack.
	// +optional
	Rack string `json:"rack,omitempty"`

	// DataNode pins writes for this bucket to a specific data node.
	// Rarely useful outside of debugging.
	// +optional
	DataNode string `json:"dataNode,omitempty"`

	// VolumeGrowthCount is the number of physical volumes to add when no
	// writable volumes are available for this bucket's collection.
	// +optional
	// +kubebuilder:validation:Minimum=0
	VolumeGrowthCount int32 `json:"volumeGrowthCount,omitempty"`
}

// BucketSpec defines the desired state of a SeaweedFS bucket.
//
// Cross-field invariants are enforced via CEL `x-kubernetes-validations`
// so they fail fast at admission. Object Lock immutability and the
// "cannot return versioning to Off" rule are implemented as transition
// rules on the individual fields.
//
// +kubebuilder:validation:XValidation:rule="!self.objectLock || self.versioning == 'Enabled'",message="objectLock requires versioning: Enabled"
type BucketSpec struct {
	// Name is the S3 bucket name. Defaults to .metadata.name. Must satisfy
	// the S3 bucket naming rules: 3-63 characters, lowercase alphanumeric,
	// hyphen and dot, starting and ending with an alphanumeric character.
	// Once the bucket has been provisioned this field is immutable; the
	// controller refuses to reconcile a rename.
	// +optional
	// +kubebuilder:validation:Pattern=`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`
	Name string `json:"name,omitempty"`

	// ClusterRef points at the Seaweed CR that owns this bucket.
	// +kubebuilder:validation:Required
	ClusterRef BucketClusterRef `json:"clusterRef"`

	// ReclaimPolicy controls what happens to the underlying bucket when
	// this CR is deleted. Defaults to Retain.
	// +optional
	// +kubebuilder:default:=Retain
	ReclaimPolicy BucketReclaimPolicy `json:"reclaimPolicy,omitempty"`

	// Versioning is the desired versioning state. Defaults to Off. Once
	// Enabled or Suspended, a bucket cannot be returned to Off — use
	// Suspended to halt new versions while keeping the version history.
	// +optional
	// +kubebuilder:default:=Off
	// +kubebuilder:validation:XValidation:rule="self != 'Off' || oldSelf == 'Off'",message="cannot disable versioning once it has been Enabled or Suspended; use Suspended instead"
	Versioning VersioningState `json:"versioning,omitempty"`

	// ObjectLock enables S3 Object Lock on the bucket. Requires
	// versioning: Enabled. Once enabled it cannot be disabled, mirroring
	// the underlying SeaweedFS and S3 semantics.
	// +optional
	// +kubebuilder:default:=false
	// +kubebuilder:validation:XValidation:rule="!oldSelf || self",message="objectLock cannot be disabled once enabled"
	ObjectLock bool `json:"objectLock,omitempty"`

	// Quota optionally caps the bucket's total stored size. Omit the
	// block entirely to leave quota unmanaged.
	// +optional
	Quota *BucketQuota `json:"quota,omitempty"`

	// Owner is the IAM identity that owns the bucket. The identity must
	// already exist in the SeaweedFS IAM service. When omitted the
	// bucket is admin-only.
	// +optional
	Owner string `json:"owner,omitempty"`

	// Access is a declarative list of per-identity bucket grants. The
	// controller reconciles to exactly this list — grants for users not
	// present are removed (the IAM identity itself is left intact).
	// +optional
	Access []BucketAccessGrant `json:"access,omitempty"`

	// Placement carries the `fs.configure` options applied under
	// /buckets/<name>/. All fields are optional; the controller only
	// emits a fs.configure call when at least one is set.
	// +optional
	Placement *BucketPlacement `json:"placement,omitempty"`

	// AnonymousRead exposes the bucket for unauthenticated read via a
	// public-read bucket policy. Defaults to false.
	// +optional
	AnonymousRead bool `json:"anonymousRead,omitempty"`
}

// BucketUsage captures coarse usage stats refreshed periodically by the
// controller. Populated only when usage stats are enabled on the operator.
type BucketUsage struct {
	// ObjectCount is the number of objects in the bucket as of LastUpdated.
	// +optional
	ObjectCount int64 `json:"objectCount,omitempty"`

	// SizeBytes is the total stored size in bytes as of LastUpdated.
	// +optional
	SizeBytes int64 `json:"sizeBytes,omitempty"`

	// LastUpdated is the time the usage stats were last refreshed.
	// +optional
	LastUpdated *metav1.Time `json:"lastUpdated,omitempty"`
}

// BucketStatusQuota mirrors the resolved quota observed on the underlying
// bucket.
type BucketStatusQuota struct {
	// SizeBytes is the spec quota size resolved to bytes.
	// +optional
	SizeBytes int64 `json:"sizeBytes,omitempty"`

	// Enforced reports whether quota enforcement is currently on.
	// +optional
	Enforced bool `json:"enforced,omitempty"`
}

// BucketPhase summarises the bucket's lifecycle.
// +kubebuilder:validation:Enum=Pending;Ready;Failed;Terminating
type BucketPhase string

const (
	BucketPhasePending     BucketPhase = "Pending"
	BucketPhaseReady       BucketPhase = "Ready"
	BucketPhaseFailed      BucketPhase = "Failed"
	BucketPhaseTerminating BucketPhase = "Terminating"
)

// Standard condition types emitted by the bucket controller. Reasons and
// messages are controller-defined; the type names here are the API
// contract surfaced to users in `kubectl get bucket -o yaml`.
const (
	// BucketConditionReady summarises whether the bucket matches spec.
	BucketConditionReady = "Ready"
	// BucketConditionClusterReachable reports filer/master connectivity
	// via the referenced Seaweed CR.
	BucketConditionClusterReachable = "ClusterReachable"
	// BucketConditionQuotaEnforced reports the live quota enforcement state.
	BucketConditionQuotaEnforced = "QuotaEnforced"
	// BucketConditionObjectLockEnabled reports the live Object Lock state.
	BucketConditionObjectLockEnabled = "ObjectLockEnabled"
	// BucketConditionDeleteBlockedByRetention is set on the CR when
	// ReclaimPolicy=Delete cannot proceed because Object Lock retention
	// still applies to one or more objects.
	BucketConditionDeleteBlockedByRetention = "DeleteBlockedByRetention"
	// BucketConditionBucketAlreadyExists is set when a bucket with the
	// requested name already exists in the cluster and was not created by
	// this operator. Adoption is deliberately not supported.
	BucketConditionBucketAlreadyExists = "BucketAlreadyExists"
	// BucketConditionOwnerMissing is set when the spec.owner identity
	// does not exist in the IAM service. The controller retries.
	BucketConditionOwnerMissing = "OwnerMissing"
	// BucketConditionClusterRefForbidden is set when the requester lacks
	// permission to reference the target Seaweed CR (cross-namespace).
	BucketConditionClusterRefForbidden = "ClusterRefForbidden"
)

// BucketStatus reflects the observed state of the bucket.
type BucketStatus struct {
	// ObservedGeneration is the .metadata.generation that the controller
	// last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is a coarse summary of the bucket's lifecycle.
	// +optional
	Phase BucketPhase `json:"phase,omitempty"`

	// Conditions are the structured per-aspect state signals.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// BucketName echoes the resolved bucket name actually used (Spec.Name
	// or the metadata.name fallback). Recorded on first successful
	// reconcile and used to detect rename attempts.
	// +optional
	BucketName string `json:"bucketName,omitempty"`

	// Versioning is the observed versioning state on the filer.
	// +optional
	Versioning VersioningState `json:"versioning,omitempty"`

	// ObjectLockEnabled is the observed Object Lock configuration.
	// +optional
	ObjectLockEnabled bool `json:"objectLockEnabled,omitempty"`

	// Quota is the observed quota on the filer.
	// +optional
	Quota *BucketStatusQuota `json:"quota,omitempty"`

	// OwnerIdentity is the observed bucket owner.
	// +optional
	OwnerIdentity string `json:"ownerIdentity,omitempty"`

	// Usage is the latest usage snapshot. Refreshed on a separate cadence
	// from spec reconciliation; may be unset when usage stats are disabled.
	// +optional
	Usage *BucketUsage `json:"usage,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=swb,categories=seaweedfs
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.clusterRef.name`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Versioning",type=string,JSONPath=`.status.versioning`
// +kubebuilder:printcolumn:name="ObjectLock",type=boolean,JSONPath=`.status.objectLockEnabled`
// +kubebuilder:printcolumn:name="QuotaBytes",type=integer,JSONPath=`.status.quota.sizeBytes`
// +kubebuilder:printcolumn:name="UsedBytes",type=integer,JSONPath=`.status.usage.sizeBytes`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Bucket is the Schema for declaratively provisioning a SeaweedFS S3
// bucket inside a Seaweed cluster.
type Bucket struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BucketSpec   `json:"spec,omitempty"`
	Status BucketStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BucketList contains a list of Bucket.
type BucketList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Bucket `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Bucket{}, &BucketList{})
}
