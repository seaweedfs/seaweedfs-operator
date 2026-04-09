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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// Constants
const (
	GRPCPortDelta = 10000

	MasterHTTPPort    = 9333
	VolumeHTTPPort    = 8444
	FilerHTTPPort     = 8888
	FilerS3Port       = 8333 // S3 port (IAM API is also available on this port when S3 is enabled)
	FilerIcebergPort  = 8181 // Default Iceberg catalog REST API port
	AdminHTTPPort     = 23646
	WorkerMetricsPort = 9101 // Default worker metrics port (only used when metricsPort is configured)

	MasterGRPCPort = MasterHTTPPort + GRPCPortDelta
	VolumeGRPCPort = VolumeHTTPPort + GRPCPortDelta
	FilerGRPCPort  = FilerHTTPPort + GRPCPortDelta
	AdminGRPCPort  = AdminHTTPPort + GRPCPortDelta
)

// TLSSpec controls mTLS between SeaweedFS components via cert-manager.
// When Enabled, the operator provisions a cert-manager Certificate covering
// every component's headless service and renders a security.toml ConfigMap
// that wires mTLS into every gRPC endpoint. cert-manager must be installed
// in the cluster — the operator will emit a condition on the Seaweed CR and
// refuse to mount TLS if the cert-manager CRDs are missing.
type TLSSpec struct {
	// Enabled turns on mTLS. Defaults to false.
	Enabled bool `json:"enabled,omitempty"`

	// IssuerRef optionally references an existing cert-manager Issuer or
	// ClusterIssuer to sign the server certificate. When empty the operator
	// provisions a self-signed Issuer + CA Certificate + CA Issuer chain
	// owned by the Seaweed CR, matching the default Helm chart behavior.
	// +optional
	IssuerRef *TLSIssuerRef `json:"issuerRef,omitempty"`
}

// TLSIssuerRef is a thin mirror of cert-manager's ObjectReference so the
// operator's CRD does not take a hard import dependency on cert-manager types
// for its own schema.
type TLSIssuerRef struct {
	Name string `json:"name"`
	// +kubebuilder:default:=Issuer
	// +kubebuilder:validation:Enum=Issuer;ClusterIssuer
	Kind string `json:"kind,omitempty"`
	// +kubebuilder:default:=cert-manager.io
	Group string `json:"group,omitempty"`
}

// SeaweedSpec defines the desired state of Seaweed
type SeaweedSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// TLS configures mTLS between SeaweedFS components. See TLSSpec.
	// +optional
	TLS *TLSSpec `json:"tls,omitempty"`

	// MetricsAddress is Prometheus gateway address
	MetricsAddress string `json:"metricsAddress,omitempty"`

	// Image
	Image string `json:"image,omitempty"`

	// Version
	Version string `json:"version,omitempty"`

	// Master
	Master *MasterSpec `json:"master,omitempty"`

	// Volume
	Volume *VolumeSpec `json:"volume,omitempty"`

	// VolumeTopology defines multiple volume server groups with topology-aware placement
	// This allows defining volume servers across different datacenters and racks in a tree structure
	// +kubebuilder:validation:Optional
	VolumeTopology map[string]*VolumeTopologySpec `json:"volumeTopology,omitempty"`

	// Filer
	Filer *FilerSpec `json:"filer,omitempty"`

	// Admin server for cluster management UI and worker coordination
	Admin *AdminSpec `json:"admin,omitempty"`

	// Worker processes that connect to admin server and execute background jobs
	Worker *WorkerSpec `json:"worker,omitempty"`

	// Note: Standalone IAM has been removed. IAM is now embedded in S3 by default.
	// When filer.s3.enabled=true, IAM API is available on the same S3 port.
	// Use filer.iam=false to disable embedded IAM if needed.

	// SchedulerName of pods
	SchedulerName string `json:"schedulerName,omitempty"`

	// Persistent volume reclaim policy
	PVReclaimPolicy *corev1.PersistentVolumeReclaimPolicy `json:"pvReclaimPolicy,omitempty"`

	// ImagePullPolicy of pods
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// ImagePullSecrets is an optional list of references to secrets in the same namespace to use for pulling any of the images.
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// Whether enable PVC reclaim for orphan PVC left by statefulset scale-in
	EnablePVReclaim *bool `json:"enablePVReclaim,omitempty"`

	// Whether Hostnetwork is enabled for pods
	HostNetwork *bool `json:"hostNetwork,omitempty"`

	// Affinity of pods
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// Base node selectors of Pods, components may add or override selectors upon this respectively
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Base annotations of Pods, components may add or override selectors upon this respectively
	Annotations map[string]string `json:"annotations,omitempty"`

	// Base tolerations of Pods, components may add more tolerations upon this respectively
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// StatefulSetUpdateStrategy indicates the StatefulSetUpdateStrategy that will be
	// employed to update Pods in the StatefulSet when a revision is made to
	// Template.
	StatefulSetUpdateStrategy appsv1.StatefulSetUpdateStrategyType `json:"statefulSetUpdateStrategy,omitempty"`

	// +kubebuilder:validation:Type=integer
	VolumeServerDiskCount *int32 `json:"volumeServerDiskCount,omitempty"`

	// Ingresses
	HostSuffix *string `json:"hostSuffix,omitempty"`
}

// SeaweedStatus defines the observed state of Seaweed
type SeaweedStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// ObservedGeneration is the most recent generation observed for this Seaweed cluster.
	// It corresponds to the cluster's generation, which is updated on mutation of the cluster's spec.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the Seaweed cluster's state
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Master component status
	// +optional
	Master ComponentStatus `json:"master,omitempty"`

	// Volume component status
	// +optional
	Volume ComponentStatus `json:"volume,omitempty"`

	// Filer component status
	// +optional
	Filer ComponentStatus `json:"filer,omitempty"`

	// Admin component status
	// +optional
	Admin ComponentStatus `json:"admin,omitempty"`

	// Worker component status
	// +optional
	Worker ComponentStatus `json:"worker,omitempty"`
}

// ComponentStatus represents the status of a seaweedfs component
type ComponentStatus struct {
	// Total number of desired replicas
	// +kubebuilder:validation:Minimum=0
	Replicas int32 `json:"replicas,omitempty"`

	// Total number of ready replicas
	// +kubebuilder:validation:Minimum=0
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`
}

// MasterSpec is the spec for masters
type MasterSpec struct {
	ComponentSpec               `json:",inline"`
	corev1.ResourceRequirements `json:",inline"`

	// The desired ready replicas
	// +kubebuilder:validation:Minimum=1
	Replicas int32        `json:"replicas"`
	Service  *ServiceSpec `json:"service,omitempty"`

	// Config in raw toml string
	Config *string `json:"config,omitempty"`

	// MetricsPort is the port that the prometheus metrics export listens on
	MetricsPort *int32 `json:"metricsPort,omitempty"`

	// Master-specific settings

	VolumePreallocate  *bool   `json:"volumePreallocate,omitempty"`
	VolumeSizeLimitMB  *int32  `json:"volumeSizeLimitMB,omitempty"`
	GarbageThreshold   *string `json:"garbageThreshold,omitempty"`
	PulseSeconds       *int32  `json:"pulseSeconds,omitempty"`
	DefaultReplication *string `json:"defaultReplication,omitempty"`
	// only for testing
	ConcurrentStart *bool `json:"concurrentStart,omitempty"`
}

// VolumeServerConfig contains common configuration for volume servers
type VolumeServerConfig struct {
	ComponentSpec               `json:",inline"`
	corev1.ResourceRequirements `json:",inline"`

	Service          *ServiceSpec `json:"service,omitempty"`
	StorageClassName *string      `json:"storageClassName,omitempty"`

	// MetricsPort is the port that the prometheus metrics export listens on
	MetricsPort *int32 `json:"metricsPort,omitempty"`

	// Volume-specific settings
	CompactionMBps      *int32 `json:"compactionMBps,omitempty"`
	FileSizeLimitMB     *int32 `json:"fileSizeLimitMB,omitempty"`
	FixJpgOrientation   *bool  `json:"fixJpgOrientation,omitempty"`
	IdleTimeout         *int32 `json:"idleTimeout,omitempty"`
	MaxVolumeCounts     *int32 `json:"maxVolumeCounts,omitempty"`
	MinFreeSpacePercent *int32 `json:"minFreeSpacePercent,omitempty"`
}

// VolumeSpec is the spec for volume servers
type VolumeSpec struct {
	VolumeServerConfig `json:",inline"`

	// The desired ready replicas
	// +kubebuilder:validation:Minimum=0
	Replicas int32 `json:"replicas"`

	// Topology configuration for rack/datacenter-aware placement
	// +kubebuilder:validation:Optional
	Rack *string `json:"rack,omitempty"`
	// +kubebuilder:validation:Optional
	DataCenter *string `json:"dataCenter,omitempty"`
}

// VolumeTopologySpec defines a volume server group with specific topology placement
// It inherits all fields from VolumeServerConfig but allows overriding them for topology-specific configuration
type VolumeTopologySpec struct {
	VolumeServerConfig `json:",inline"`

	// The desired ready replicas for this topology group
	// +kubebuilder:validation:Minimum=0
	Replicas int32 `json:"replicas"`

	// Topology configuration for this volume group (required for topology groups)
	// +kubebuilder:validation:Required
	Rack string `json:"rack"`
	// +kubebuilder:validation:Required
	DataCenter string `json:"dataCenter"`
}

// S3Config defines the S3 configuration with identities
type S3Config struct {
	// +kubebuilder:default:=true
	Enabled      bool                      `json:"enabled,omitempty"`
	ConfigSecret *corev1.SecretKeySelector `json:"configSecret,omitempty"`
}

// IcebergConfig defines the Iceberg catalog REST API configuration
type IcebergConfig struct {
	// +kubebuilder:default:=true
	Enabled bool `json:"enabled,omitempty"`
	// Port for the Iceberg catalog REST API. Defaults to 8181 if not specified.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port *int32 `json:"port,omitempty"`
}

// FilerSpec is the spec for filers
type FilerSpec struct {
	ComponentSpec               `json:",inline"`
	corev1.ResourceRequirements `json:",inline"`

	// The desired ready replicas
	// +kubebuilder:validation:Minimum=1
	Replicas int32        `json:"replicas"`
	Service  *ServiceSpec `json:"service,omitempty"`

	// Config in raw toml string
	Config *string `json:"config,omitempty"`

	// MetricsPort is the port that the prometheus metrics export listens on
	MetricsPort *int32 `json:"metricsPort,omitempty"`

	// Persistence mounts a volume for local filer data
	Persistence *PersistenceSpec `json:"persistence,omitempty"`

	// Filer-specific settings

	MaxMB *int32 `json:"maxMB,omitempty"`
	// S3 configuration for the filer
	S3 *S3Config `json:"s3,omitempty"`
	// IAM enables/disables IAM API embedded in S3 server.
	// When S3 is enabled, IAM is enabled by default (on the same S3 port: 8333).
	// Set to false to explicitly disable embedded IAM.
	// +kubebuilder:default:=true
	IAM bool `json:"iam,omitempty"`

	// Iceberg configuration for the Iceberg catalog REST API
	Iceberg *IcebergConfig `json:"iceberg,omitempty"`
}

// IcebergEffectivePort returns the port to use for the Iceberg catalog REST API.
// Returns FilerIcebergPort (8181) if no port is explicitly configured.
func (c *IcebergConfig) IcebergEffectivePort() int32 {
	if c.Port != nil {
		return *c.Port
	}
	return FilerIcebergPort
}

// AdminSpec is the spec for the admin server (single instance, stateless)
type AdminSpec struct {
	ComponentSpec               `json:",inline"`
	corev1.ResourceRequirements `json:",inline"`

	Service *ServiceSpec `json:"service,omitempty"`

	// MetricsPort is the port that the prometheus metrics export listens on
	MetricsPort *int32 `json:"metricsPort,omitempty"`

	// CredentialsSecret is a reference to a Secret containing admin credentials.
	// The secret should have keys: adminUser, adminPassword, and optionally readOnlyUser, readOnlyPassword
	CredentialsSecret *corev1.LocalObjectReference `json:"credentialsSecret,omitempty"`
}

// WorkerSpec is the spec for worker processes
type WorkerSpec struct {
	ComponentSpec               `json:",inline"`
	corev1.ResourceRequirements `json:",inline"`

	// The desired ready replicas
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default:=1
	Replicas int32 `json:"replicas"`

	// MetricsPort is the port that the prometheus metrics export listens on
	MetricsPort *int32 `json:"metricsPort,omitempty"`

	// Persistence mounts a volume for worker working directory
	Persistence *PersistenceSpec `json:"persistence,omitempty"`

	// JobType specifies which job types or categories the worker should serve.
	// Categories: "all", "default", "heavy". Can also specify explicit job type names.
	// +kubebuilder:default:="all"
	JobType *string `json:"jobType,omitempty"`

	// MaxDetect is the max number of concurrent detection requests
	// +kubebuilder:validation:Minimum=1
	MaxDetect *int32 `json:"maxDetect,omitempty"`

	// MaxExecute is the max number of concurrent execute requests
	// +kubebuilder:validation:Minimum=1
	MaxExecute *int32 `json:"maxExecute,omitempty"`
}

// ComponentSpec is the base spec of each component, the fields should always accessed by the Basic<Component>Spec() method to respect the cluster-level properties
type ComponentSpec struct {
	// Version of the component. Override the cluster-level version if non-empty
	Version *string `json:"version,omitempty"`

	// ImagePullPolicy of the component. Override the cluster-level imagePullPolicy if present
	ImagePullPolicy *corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// ImagePullSecrets is an optional list of references to secrets in the same namespace to use for pulling any of the images.
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// Whether Hostnetwork of the component is enabled. Override the cluster-level setting if present
	HostNetwork *bool `json:"hostNetwork,omitempty"`

	// Affinity of the component. Override the cluster-level one if present
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// PriorityClassName of the component. Override the cluster-level one if present
	PriorityClassName *string `json:"priorityClassName,omitempty"`

	// SchedulerName of the component. Override the cluster-level one if present
	SchedulerName *string `json:"schedulerName,omitempty"`

	// NodeSelector of the component. Merged into the cluster-level nodeSelector if non-empty
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Annotations of the component. Merged into the cluster-level annotations if non-empty
	Annotations map[string]string `json:"annotations,omitempty"`

	// Tolerations of the component. Override the cluster-level tolerations if non-empty
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// List of environment variables to set in the container, like
	// v1.Container.Env.
	// Note that following env names cannot be used and may be overrided by operators
	// - NAMESPACE
	// - POD_IP
	// - POD_NAME
	Env []corev1.EnvVar `json:"env,omitempty"`

	// Optional duration in seconds the pod needs to terminate gracefully. May be decreased in delete request.
	// Value must be non-negative integer. The value zero indicates delete immediately.
	// If this value is nil, the default grace period will be used instead.
	// The grace period is the duration in seconds after the processes running in the pod are sent
	// a termination signal and the time when the processes are forcibly halted with a kill signal.
	// Set this value longer than the expected cleanup time for your process.
	// Defaults to 30 seconds.
	TerminationGracePeriodSeconds *int64 `json:"terminationGracePeriodSeconds,omitempty"`

	// StatefulSetUpdateStrategy indicates the StatefulSetUpdateStrategy that will be
	// employed to update Pods in the StatefulSet when a revision is made to
	// Template.
	StatefulSetUpdateStrategy appsv1.StatefulSetUpdateStrategyType `json:"statefulSetUpdateStrategy,omitempty"`

	// Volumes of the component. Merged into the volumes created by the operator if non-empty
	Volumes []corev1.Volume `json:"volumes,omitempty"`

	// VolumeMounts of the component. Merged into the volumeMounts created by the operator if non-empty
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`

	// ExtraArgs are additional command line arguments passed to the component container
	// +listType=atomic
	ExtraArgs []string `json:"extraArgs,omitempty"`
}

// ServiceSpec is a subset of the original k8s spec
type ServiceSpec struct {
	// Type of the real kubernetes service
	Type corev1.ServiceType `json:"type,omitempty"`

	// Additional annotations of the kubernetes service object
	Annotations map[string]string `json:"annotations,omitempty"`

	// LoadBalancerIP is the loadBalancerIP of service
	LoadBalancerIP *string `json:"loadBalancerIP,omitempty"`

	// ClusterIP is the clusterIP of service
	ClusterIP *string `json:"clusterIP,omitempty"`
}

type PersistenceSpec struct {
	// +kubebuilder:default:=false
	Enabled bool `json:"enabled,omitempty"`

	// ExistingClaim is the name of an existing pvc to use
	ExistingClaim *string `json:"existingClaim,omitempty"`

	// The path the volume will be mounted at
	// +kubebuilder:default:="/data"
	MountPath *string `json:"mountPath,omitempty"`

	// The subdirectory of the volume to mount to
	// +kubebuilder:default:=""
	SubPath *string `json:"subPath,omitempty"`

	// accessModes contains the desired access modes the volume should have.
	// More info: https://kubernetes.io/docs/concepts/storage/persistent-volumes#access-modes-1
	// +kubebuilder:default:={"ReadWriteOnce"}
	AccessModes []corev1.PersistentVolumeAccessMode `json:"accessModes,omitempty"`

	// selector is a label query over volumes to consider for binding.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`

	// resources represents the minimum resources the volume should have.
	// If RecoverVolumeExpansionFailure feature is enabled users are allowed to specify resource requirements
	// that are lower than previous value but must still be higher than capacity recorded in the
	// status field of the claim.
	// More info: https://kubernetes.io/docs/concepts/storage/persistent-volumes#resources
	// +kubebuilder:default:={requests:{storage:"4Gi"}}
	Resources corev1.VolumeResourceRequirements `json:"resources,omitempty"`

	// volumeName is the binding reference to the PersistentVolume backing this claim.
	// +optional
	VolumeName string `json:"volumeName,omitempty"`

	// storageClassName is the name of the StorageClass required by the claim.
	// More info: https://kubernetes.io/docs/concepts/storage/persistent-volumes#class-1
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`

	// volumeMode defines what type of volume is required by the claim.
	// Value of Filesystem is implied when not included in claim spec.
	// +optional
	VolumeMode *corev1.PersistentVolumeMode `json:"volumeMode,omitempty"`

	// dataSource field can be used to specify either:
	// * An existing VolumeSnapshot object (snapshot.storage.k8s.io/VolumeSnapshot)
	// * An existing PVC (PersistentVolumeClaim)
	// If the provisioner or an external controller can support the specified data source,
	// it will create a new volume based on the contents of the specified data source.
	// If the AnyVolumeDataSource feature gate is enabled, this field will always have
	// the same contents as the DataSourceRef field.
	// +optional
	DataSource *corev1.TypedLocalObjectReference `json:"dataSource,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// Seaweed is the Schema for the seaweeds API
type Seaweed struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SeaweedSpec   `json:"spec,omitempty"`
	Status SeaweedStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SeaweedList contains a list of Seaweed
type SeaweedList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Seaweed `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Seaweed{}, &SeaweedList{})
}
