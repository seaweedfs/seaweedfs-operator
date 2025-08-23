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

	MasterHTTPPort = 9333
	VolumeHTTPPort = 8444
	FilerHTTPPort  = 8888
	FilerS3Port    = 8333
	FilerIAMPort   = 8111

	MasterGRPCPort = MasterHTTPPort + GRPCPortDelta
	VolumeGRPCPort = VolumeHTTPPort + GRPCPortDelta
	FilerGRPCPort  = FilerHTTPPort + GRPCPortDelta
)

// SeaweedSpec defines the desired state of Seaweed
type SeaweedSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

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

	// Filer
	Filer *FilerSpec `json:"filer,omitempty"`

	// IAM
	IAM *IAMSpec `json:"iam,omitempty"`

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

	VolumeServerDiskCount int32 `json:"volumeServerDiskCount,omitempty"`

	// Ingresses
	HostSuffix *string `json:"hostSuffix,omitempty"`
}

// SeaweedStatus defines the observed state of Seaweed
type SeaweedStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
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

// VolumeSpec is the spec for volume servers
type VolumeSpec struct {
	ComponentSpec               `json:",inline"`
	corev1.ResourceRequirements `json:",inline"`

	// The desired ready replicas
	// +kubebuilder:validation:Minimum=1
	Replicas int32        `json:"replicas"`
	Service  *ServiceSpec `json:"service,omitempty"`

	StorageClassName *string `json:"storageClassName,omitempty"`

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

// S3Config defines the S3 configuration with identities
type S3Config struct {
	// +kubebuilder:default:=true
	Enabled      bool                      `json:"enabled,omitempty"`
	ConfigSecret *corev1.SecretKeySelector `json:"configSecret,omitempty"`
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
	// Enable IAM service embedded with filer (alternative to standalone IAM)
	IAM bool `json:"iam,omitempty"`
}

// IAMSpec is the spec for IAM servers
type IAMSpec struct {
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

	// IAM-specific settings

	// Port for IAM service (default: 8111)
	Port *int32 `json:"port,omitempty"`
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
