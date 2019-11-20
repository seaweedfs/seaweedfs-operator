package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SeaweedfsClusterSpec defines the desired state of SeaweedfsCluster
// +k8s:openapi-gen=true
type SeaweedfsClusterSpec struct {
	Version string       `json:"version,omitempty"`
	Master  MasterSpec   `json:"master,omitempty"`
	Volumes []VolumeSpec `json:"volumes,omitempty"`
	Filer   FilerSpec    `json:"filer,omitempty"`
}

// MasterSpec defines the desired state of master server in cluster
// +k8s:openapi-gen=true
type MasterSpec struct {
	// Replicas a size of a raft cluster.
	// The master servers are coordinated by Raft protocol, to elect a leader.
	Replicas int32 `json:"replicas,omitempty"`
	// Port set master server http api service port. default is 9333
	// Master servers also use it identify each other.
	Port int32 `json:"port,omitempty"`
	// DisableHTTP if disable http proto, only gRPC operations are allowed.
	// GRPC port is http port + 10000
	DisableHTTP bool `json:"disableHttp,omitempty"`
	// DefaultReplication set the data replication policy in volumes. default "000"
	DefaultReplication string `json:"default_replication,omitempty"`
}

// VolumeSpec defines the desired state of volume servers in cluster
// +k8s:openapi-gen=true
type VolumeSpec struct {
	// Max set the maximum numbers of volumes management by this server.
	// Each volume is a 30G size file in under layer filesystem. Default is 7
	Max int32 `json:"max,omitempty"`
	// CompactionMbps limit background compaction or copying speed in mega bytes per second
	CompactionMbps int32 `json:"compaction_mbps,omitempty"`
	//Rack current volume server's rack name
	Rack string `json:"rack,omitempty"`
	// DataCenter current volume server's data center name
	DataCenter string `json:"dataCenter,omitempty"`
	// Port volume server api http listen port
	Port int32 `json:"port,omitempty"`
}

// FilerSpec defines the desired state of filer server in cluster
// +k8s:openapi-gen=true
type FilerSpec struct {
	// Replicas a size of filer replications
	Replicas int32 `json:"replicas,omitempty"`
	// DirListLimit limit sub dir listing size, default 100000
	DirListLimit int32 `json:"dirListLimit,omitempty"`
	// DisableDirListing turn off directory listing
	DisableDirListing bool `json:"disableDirListing,omitempty"`
	// MaxMB split files larger than the limit, default 32
	MaxMB int32 `json:"max_mb,omitempty"`
	// Port filer server http listen port
	Port int32 `json:"port,omitempty"`
}

// SeaweedfsClusterStatus defines the observed state of SeaweedfsCluster
// +k8s:openapi-gen=true
type SeaweedfsClusterStatus struct {
	ClusterID string
	Master    MasterStatus
	Volumes   []VolumesStatus
	Filer     FilerStatus
}

// MasterStatus defines the observed state of SeaweedfsCluster master server
// +k8s:openapi-gen=true
type MasterStatus struct {
	// +k8s:openapi-gen=false
	ContainerSpec
	Replicas int32 `json:"replicas"`
}

type VolumesStatus struct {
	// +k8s:openapi-gen=false
	ContainerSpec
}

type FilerStatus struct {
	// +k8s:openapi-gen=false
	ContainerSpec
	Replicas int32 `json:"replicas"`
}

// ContainerSpec is the container spec of a pod
// +k8s:openapi-gen=false
type ContainerSpec struct {
	Image           string               `json:"image"`
	ImagePullPolicy corev1.PullPolicy    `json:"imagePullPolicy,omitempty"`
	Requests        *ResourceRequirement `json:"requests,omitempty"`
	Limits          *ResourceRequirement `json:"limits,omitempty"`
}

// ResourceRequirement is resource requirements for a pod
// +k8s:openapi-gen=true
type ResourceRequirement struct {
	// CPU is how many cores a pod requires
	CPU string `json:"cpu,omitempty"`
	// Memory is how much memory a pod requires
	Memory string `json:"memory,omitempty"`
	// Storage is storage size a pod requires
	Storage string `json:"storage,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// SeaweedfsCluster is the Schema for the seaweedfsclusters API
// +k8s:openapi-gen=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=seaweedfsclusters,scope=Namespaced
type SeaweedfsCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SeaweedfsClusterSpec   `json:"spec,omitempty"`
	Status SeaweedfsClusterStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// SeaweedfsClusterList contains a list of SeaweedfsCluster
type SeaweedfsClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SeaweedfsCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SeaweedfsCluster{}, &SeaweedfsClusterList{})
}
