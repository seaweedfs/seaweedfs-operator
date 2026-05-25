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

// This file holds the types shared by the S3 IAM CRDs (S3Identity,
// S3Credentials, S3Policy, S3PolicyBinding). They all target a Seaweed
// cluster's embedded IAM service (the IAM gRPC API served by the filer,
// see IAM_SUPPORT.md) and they all manage pure configuration — there is no
// object data behind them — so unlike the Bucket CRD they default their
// reclaim policy to Delete: the CR is the source of truth for the IAM
// object, mirroring the convention in AWS ACK / Crossplane / Config Connector.

// SeaweedReference points at the Seaweed CR whose embedded IAM service hosts
// the identity/policy. Cross-namespace references are allowed and are NOT
// gated by an admission-time SubjectAccessReview — gate access with
// kubernetes RBAC on the IAM CRDs themselves if you need to restrict which
// namespaces may target a particular Seaweed (same model as BucketClusterRef).
type SeaweedReference struct {
	// Name of the Seaweed CR.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace of the Seaweed CR. Defaults to the referencing CR's own
	// namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// S3ReclaimPolicy controls what happens to the underlying SeaweedFS IAM
// object (user, access key, policy, or attachment) when the CR is deleted.
// +kubebuilder:validation:Enum=Delete;Retain
type S3ReclaimPolicy string

const (
	// S3ReclaimDelete removes the underlying IAM object when the CR is
	// deleted. This is the default — the CR owns the object's lifecycle.
	S3ReclaimDelete S3ReclaimPolicy = "Delete"

	// S3ReclaimRetain leaves the underlying IAM object in place when the CR
	// is deleted. Use this to hand an object off to out-of-band management.
	S3ReclaimRetain S3ReclaimPolicy = "Retain"
)

// S3Phase is a coarse summary of an IAM CR's lifecycle, shared by all four
// IAM kinds.
// +kubebuilder:validation:Enum=Pending;Ready;Failed;Terminating
type S3Phase string

const (
	S3PhasePending     S3Phase = "Pending"
	S3PhaseReady       S3Phase = "Ready"
	S3PhaseFailed      S3Phase = "Failed"
	S3PhaseTerminating S3Phase = "Terminating"
)

// Condition types shared by the IAM controllers. The reason and message are
// controller-defined; these type names are the API contract surfaced in
// `kubectl get ... -o yaml`.
const (
	// S3ConditionReady summarises whether the CR matches its spec.
	S3ConditionReady = "Ready"
	// S3ConditionClusterReachable reports filer/IAM connectivity via the
	// referenced Seaweed CR.
	S3ConditionClusterReachable = "ClusterReachable"
)
