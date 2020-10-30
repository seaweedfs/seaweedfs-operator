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
	Hosts []string `json:"hosts,omitempty"`
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

	// Master-specific settings

	VolumePreallocate  *bool   `json:"volumePreallocate,omitempty"`
	VolumeSizeLimitMB  *int32  `json:"volumeSizeLimitMB,omitempty"`
	GarbageThreshold   *string `json:"garbageThreshold,omitempty"`
	PulseSeconds       *int32  `json:"pulseSeconds,omitempty"`
	DefaultReplication *string `json:"defaultReplication,omitempty"`
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

	// Volume-specific settings

	CompactionMBps      *int32 `json:"compactionMBps,omitempty"`
	FileSizeLimitMB     *int32 `json:"fileSizeLimitMB,omitempty"`
	FixJpgOrientation   *bool  `json:"fixJpgOrientation,omitempty"`
	IdleTimeout         *int32 `json:"idleTimeout,omitempty"`
	MaxVolumeCounts     *int32 `json:"maxVolumeCounts,omitempty"`
	MinFreeSpacePercent *int32 `json:"minFreeSpacePercent,omitempty"`
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

	// Filer-specific settings

	MaxMB *int32 `json:"maxMB,omitempty"`
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

	// Additional containers of the component.
	AdditionalContainers []corev1.Container `json:"additionalContainers,omitempty"`

	// Additional volumes of component pod. Currently this only
	// supports additional volume mounts for sidecar containers.
	AdditionalVolumes []corev1.Volume `json:"additionalVolumes,omitempty"`

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
