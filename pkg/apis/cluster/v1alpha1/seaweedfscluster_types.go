package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SeaweedfsClusterSpec defines the desired state of SeaweedfsCluster
// +k8s:openapi-gen=true
type SeaweedfsClusterSpec struct {
	Version string                `json:"version,omitempty"`
	Master  SeaweedfsMasterSpec   `json:"master,omitempty"`
	Volumes []SeaweedfsVolumeSpec `json:"volumes,omitempty"`
	Filer   SeaweedfsFilerSpec    `json:"filer,omitempty"`
}

// SeaweedfsMasterSpec defines the desired state of master server in cluster
// +k8s:openapi-gen=true
type SeaweedfsMasterSpec struct {
	// Replicas a size of a raft cluster.
	// The master servers are coordinated by Raft protocol, to elect a leader.
	Replicas int32 `json:"replication_size,omitempty"`
	// Port set master server http api service port. default is 9333
	// Master servers also use it identify each other.
	Port int32 `json:"port,omitempty"`
	// DisableHTTP if disable http proto, only gRPC operations are allowed.
	// GRPC port is http port + 10000
	DisableHTTP bool `json:"disable_http,omitempty"`
	// DefaultReplication set the data replication policy in volumes. default "000"
	DefaultReplication string `json:"default_replication,omitempty"`
}

// SeaweedfsVolumeSpec defines the desired state of volume servers in cluster
// +k8s:openapi-gen=true
type SeaweedfsVolumeSpec struct {
	// Max set the maximum numbers of volumes management by this server.
	// Each volume is a 30G size file in under layer filesystem. Default is 7
	Max int32 `json:"max,omitempty"`
	// CompactionMbps limit background compaction or copying speed in mega bytes per second
	CompactionMbps int32 `json:"compaction_mbps,omitempty"`
	//Rack current volume server's rack name
	Rack string `json:"rack,omitempty"`
	// DataCenter current volume server's data center name
	DataCenter string `json:"data_center,omitempty"`
	// Port volume server api http listen port
	Port int32 `json:"port,omitempty"`
}

// SeaweedfsFilerSpec defines the desired state of filer server in cluster
// +k8s:openapi-gen=true
type SeaweedfsFilerSpec struct {
	// Replicas a size of filer replications
	Replicas int32 `json:"replicas,omitempty"`
	// DirListLimit limit sub dir listing size, default 100000
	DirListLimit int32 `json:"dir_list_limit,omitempty"`
	// DisableDirListing turn off directory listing
	DisableDirListing bool `json:"disable_dir_listing,omitempty"`
	// MaxMB split files larger than the limit, default 32
	MaxMB int32 `json:"max_mb,omitempty"`
	// Port filer server http listen port
	Port int32 `json:"port,omitempty"`
}

// SeaweedfsClusterStatus defines the observed state of SeaweedfsCluster
// +k8s:openapi-gen=true
type SeaweedfsClusterStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "operator-sdk generate k8s" to regenerate code after modifying this file
	// Add custom validation using kubebuilder tags: https://book-v1.book.kubebuilder.io/beyond_basics/generating_crd.html
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
