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
	AdminHTTPPort  = 23646

	MasterGRPCPort = MasterHTTPPort + GRPCPortDelta
	VolumeGRPCPort = VolumeHTTPPort + GRPCPortDelta
	FilerGRPCPort  = FilerHTTPPort + GRPCPortDelta
)

// SeaweedSpec defines the desired state of Seaweed
type SeaweedSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Image
	Image string `json:"image,omitempty"`

	// Version
	Version string `json:"version,omitempty"`

	// Metrics configuration for all components
	Metrics *MetricsSpec `json:"metrics,omitempty"`

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

	// FilerBackup
	FilerBackup *FilerBackupSpec `json:"filerBackup,omitempty"`

	// Admin UI
	Admin *AdminSpec `json:"admin,omitempty"`

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

	// Storage configuration for all components
	Storage *StorageSpec `json:"storage,omitempty"`

	// Host suffix (base domain) for ingresses of components
	HostSuffix *string `json:"hostSuffix,omitempty"`
}

type MetricsSpec struct {
	// +kubebuilder:default:=false
	Enabled bool `json:"enabled,omitempty"`

	// MetricsPort is the port that the prometheus metrics export listens on
	// +kubebuilder:default:=5555
	MetricsPort *int32 `json:"metricsPort,omitempty"`
}

type StorageSpec struct {
	// StorageClassName is the name of the StorageClass to use for all components
	StorageClassName *string `json:"storageClassName,omitempty"`

	// VolumeServerDiskCount is the number of disks to use for volume servers
	VolumeServerDiskCount int32 `json:"volumeServerDiskCount,omitempty"`
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

// VolumeSpec is the spec for volumes
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

// S3Credential defines an S3 credential
type S3Credential struct {
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
}

// S3Identity defines an identity with credentials and allowed actions
type S3Identity struct {
	Name        string         `json:"name"`
	Credentials []S3Credential `json:"credentials,omitempty"`
	Actions     []string       `json:"actions"`
}

// S3ConfigSecret defines the S3 configuration secret reference
type S3ConfigSecret struct {
	// Name of the secret containing S3 configuration
	Name string `json:"name,omitempty"`

	// Key in the secret for the configuration
	Key string `json:"key,omitempty"`
}

// S3Config defines the S3 configuration
type S3Config struct {
	// +kubebuilder:default:=false
	Enabled bool `json:"enabled,omitempty"`

	// Identities defines S3 identities
	Identities []S3Identity `json:"identities,omitempty"`

	// ConfigSecret references a secret containing S3 configuration
	ConfigSecret *S3ConfigSecret `json:"configSecret,omitempty"`
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

	// S3 configuration
	// +kubebuilder:default:={enabled:true}
	S3 *S3Config `json:"s3,omitempty"`

	// IAM is whether to enable IAM (embedded in S3 by default)
	// +kubebuilder:default:=true
	IAM bool `json:"iam,omitempty"`
}

// FilerBackupSpec is the spec for filer backups
type FilerBackupSpec struct {
	ComponentSpec               `json:",inline"`
	corev1.ResourceRequirements `json:",inline"`

	// The desired ready replicas
	// +kubebuilder:validation:Minimum=1
	Replicas int32        `json:"replicas"`
	Service  *ServiceSpec `json:"service,omitempty"`

	// Config in raw toml string
	Config *string `json:"config,omitempty"`

	// Persistence mounts a volume for local filer data
	Persistence *PersistenceSpec `json:"persistence,omitempty"`

	// Filer-specific settings

	MaxMB *int32 `json:"maxMB,omitempty"`

	// Backup-specific settings

	// Sink configuration for backup destinations
	Sink *SinkConfig `json:"sink,omitempty"`
}

// AdminSpec is the spec for admin UI
type AdminSpec struct {
	ComponentSpec               `json:",inline"`
	corev1.ResourceRequirements `json:",inline"`

	// The desired ready replicas
	// +kubebuilder:validation:Minimum=1
	Replicas int32        `json:"replicas"`
	Service  *ServiceSpec `json:"service,omitempty"`

	// Admin server port
	// +kubebuilder:default:=23646
	Port *int32 `json:"port,omitempty"`

	// Comma-separated master servers (if empty, will be auto-discovered from cluster)
	Masters string `json:"masters,omitempty"`

	// Directory to store admin configuration and data files
	DataDir string `json:"dataDir,omitempty"`

	// Admin interface username
	// +kubebuilder:default:="admin"
	AdminUser string `json:"adminUser,omitempty"`

	// Admin interface password (if empty, auth is disabled)
	AdminPassword string `json:"adminPassword,omitempty"`

	// Admin password secret reference (alternative to AdminPassword)
	AdminPasswordSecretRef *AdminPasswordSecretRef `json:"adminPasswordSecretRef,omitempty"`

	// Persistence mounts a volume for admin data
	Persistence *PersistenceSpec `json:"persistence,omitempty"`

	// TLS configuration for HTTPS
	TLS *AdminTLSSpec `json:"tls,omitempty"`
}

// AdminPasswordSecretRef defines admin password secret reference
type AdminPasswordSecretRef struct {
	// Name of the secret
	Name string `json:"name,omitempty"`

	// Key in the secret containing the password
	Key string `json:"key,omitempty"`
}

// AdminTLSSpec defines TLS configuration for admin UI
type AdminTLSSpec struct {
	// +kubebuilder:default:=false
	Enabled bool `json:"enabled,omitempty"`

	// Certificate secret reference
	CertificateSecretRef *AdminCertificateSecretRef `json:"certificateSecretRef,omitempty"`

	// Certificate file path (if not using secret)
	CertFile string `json:"certFile,omitempty"`

	// Key file path (if not using secret)
	KeyFile string `json:"keyFile,omitempty"`

	// CA certificate file path (optional, for mutual TLS)
	CAFile string `json:"caFile,omitempty"`
}

// AdminCertificateSecretRef defines certificate secret reference
type AdminCertificateSecretRef struct {
	// Name of the secret
	Name string `json:"name,omitempty"`

	// Mapping of the configuration key to the secret key
	Mapping AdminCertificateSecretRefMapping `json:"mapping,omitempty"`
}

type AdminCertificateSecretRefMapping struct {
	Cert string `json:"cert,omitempty"`
	Key  string `json:"key,omitempty"`
	CA   string `json:"ca,omitempty"`
}

// SinkConfig defines the backup sink configuration
type SinkConfig struct {
	// Local sink configuration
	Local *LocalSinkConfig `json:"local,omitempty"`

	// Filer sink configuration
	Filer *FilerSinkConfig `json:"filer,omitempty"`

	// S3 sink configuration
	S3 *S3SinkConfig `json:"s3,omitempty"`

	// Google Cloud Storage sink configuration
	GoogleCloudStorage *GoogleCloudStorageSinkConfig `json:"googleCloudStorage,omitempty"`

	// Azure sink configuration
	Azure *AzureSinkConfig `json:"azure,omitempty"`

	// Backblaze sink configuration
	Backblaze *BackblazeSinkConfig `json:"backblaze,omitempty"`
}

// LocalSinkConfig defines local file system sink configuration
type LocalSinkConfig struct {
	// +kubebuilder:default:=false
	Enabled bool `json:"enabled,omitempty"`

	// Directory where backups will be stored
	Directory string `json:"directory,omitempty"`

	// Whether to use incremental backup mode
	// +kubebuilder:default:=false
	IsIncremental bool `json:"isIncremental,omitempty"`
}

// FilerSinkConfig defines filer sink configuration
type FilerSinkConfig struct {
	// +kubebuilder:default:=false
	Enabled bool `json:"enabled,omitempty"`

	// gRPC address of the target filer
	GRPCAddress string `json:"grpcAddress,omitempty"`

	// Directory where backups will be stored
	Directory string `json:"directory,omitempty"`

	// Replication setting
	Replication string `json:"replication,omitempty"`

	// Collection setting
	Collection string `json:"collection,omitempty"`

	// TTL in seconds
	TTLSec int32 `json:"ttlSec,omitempty"`

	// Whether to use incremental backup mode
	// +kubebuilder:default:=false
	IsIncremental bool `json:"isIncremental,omitempty"`
}

// S3SinkConfig defines S3 sink configuration
type S3SinkConfig struct {
	// +kubebuilder:default:=false
	Enabled bool `json:"enabled,omitempty"`

	// AWS access key ID (if empty, loads from shared credentials file)
	AWSAccessKeyID string `json:"awsAccessKeyID,omitempty"`

	// AWS secret access key (if empty, loads from shared credentials file)
	AWSSecretAccessKey string `json:"awsSecretAccessKey,omitempty"`

	// AWS credentials secret reference
	AWSCredentialsSecretRef *AWSCredentialsSecretRef `json:"awsCredentialsSecretRef,omitempty"`

	// AWS region
	Region string `json:"region,omitempty"`

	// S3 bucket name
	Bucket string `json:"bucket,omitempty"`

	// Destination directory in the bucket
	Directory string `json:"directory,omitempty"`

	// Custom S3 endpoint
	Endpoint string `json:"endpoint,omitempty"`

	// Whether to use incremental backup mode
	// +kubebuilder:default:=false
	IsIncremental bool `json:"isIncremental,omitempty"`
}

type AWSCredentialsSecretRef struct {
	// Name of the secret
	Name string `json:"name,omitempty"`

	// Mapping of the configuration key to the secret key
	Mapping AWSCredentialsSecretRefMapping `json:"mapping,omitempty"`
}

type AWSCredentialsSecretRefMapping struct {
	AWSAccessKeyID     string `json:"awsAccessKeyID,omitempty"`
	AWSSecretAccessKey string `json:"awsSecretAccessKey,omitempty"`
}

// AzureCredentialsSecretRef defines Azure credentials secret reference
type AzureCredentialsSecretRef struct {
	// Name of the secret
	Name string `json:"name,omitempty"`

	// Mapping of the configuration key to the secret key
	Mapping AzureCredentialsSecretRefMapping `json:"mapping,omitempty"`
}

type AzureCredentialsSecretRefMapping struct {
	AccountName string `json:"accountName,omitempty"`
	AccountKey  string `json:"accountKey,omitempty"`
}

// GoogleCloudStorageCredentialsSecretRef defines Google Cloud Storage credentials secret reference
type GoogleCloudStorageCredentialsSecretRef struct {
	// Name of the secret
	Name string `json:"name,omitempty"`

	// Mapping of the configuration key to the secret key
	Mapping GoogleCloudStorageCredentialsSecretRefMapping `json:"mapping,omitempty"`
}

type GoogleCloudStorageCredentialsSecretRefMapping struct {
	GoogleApplicationCredentials string `json:"googleApplicationCredentials,omitempty"`
}

// BackblazeCredentialsSecretRef defines Backblaze B2 credentials secret reference
type BackblazeCredentialsSecretRef struct {
	// Name of the secret
	Name string `json:"name,omitempty"`

	// Mapping of the configuration key to the secret key
	Mapping BackblazeCredentialsSecretRefMapping `json:"mapping,omitempty"`
}

type BackblazeCredentialsSecretRefMapping struct {
	B2AccountID            string `json:"b2AccountID,omitempty"`
	B2MasterApplicationKey string `json:"b2MasterApplicationKey,omitempty"`
}

// GoogleCloudStorageSinkConfig defines Google Cloud Storage sink configuration
type GoogleCloudStorageSinkConfig struct {
	// +kubebuilder:default:=false
	Enabled bool `json:"enabled,omitempty"`

	// Path to Google application credentials JSON file
	GoogleApplicationCredentials string `json:"googleApplicationCredentials,omitempty"`

	// Google Cloud Storage credentials secret reference
	GoogleCloudStorageCredentialsSecretRef *GoogleCloudStorageCredentialsSecretRef `json:"googleCloudStorageCredentialsSecretRef,omitempty"`

	// GCS bucket name
	Bucket string `json:"bucket,omitempty"`

	// Destination directory in the bucket
	Directory string `json:"directory,omitempty"`

	// Whether to use incremental backup mode
	// +kubebuilder:default:=false
	IsIncremental bool `json:"isIncremental,omitempty"`
}

// AzureSinkConfig defines Azure Blob Storage sink configuration
type AzureSinkConfig struct {
	// +kubebuilder:default:=false
	Enabled bool `json:"enabled,omitempty"`

	// Azure storage account name
	AccountName string `json:"accountName,omitempty"`

	// Azure storage account key
	AccountKey string `json:"accountKey,omitempty"`

	// Azure credentials secret reference
	AzureCredentialsSecretRef *AzureCredentialsSecretRef `json:"azureCredentialsSecretRef,omitempty"`

	// Azure container name
	Container string `json:"container,omitempty"`

	// Destination directory in the container
	Directory string `json:"directory,omitempty"`

	// Whether to use incremental backup mode
	// +kubebuilder:default:=false
	IsIncremental bool `json:"isIncremental,omitempty"`
}

// BackblazeSinkConfig defines Backblaze B2 sink configuration
type BackblazeSinkConfig struct {
	// +kubebuilder:default:=false
	Enabled bool `json:"enabled,omitempty"`

	// B2 account ID
	B2AccountID string `json:"b2AccountID,omitempty"`

	// B2 master application key
	B2MasterApplicationKey string `json:"b2MasterApplicationKey,omitempty"`

	// Backblaze B2 credentials secret reference
	BackblazeCredentialsSecretRef *BackblazeCredentialsSecretRef `json:"backblazeCredentialsSecretRef,omitempty"`

	// B2 region
	B2Region string `json:"b2Region,omitempty"`

	// B2 bucket name
	Bucket string `json:"bucket,omitempty"`

	// Destination directory in the bucket
	Directory string `json:"directory,omitempty"`

	// Whether to use incremental backup mode
	// +kubebuilder:default:=false
	IsIncremental bool `json:"isIncremental,omitempty"`
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

// BucketClaimSpec defines the desired state of BucketClaim
type BucketClaimSpec struct {
	// BucketName is the name of the bucket to be created
	// +kubebuilder:validation:Required
	BucketName string `json:"bucketName"`

	// Region is the region of the bucket to be created
	Region string `json:"region,omitempty"`

	// Quota of the bucket to be created
	Quota BucketQuota `json:"quota,omitempty"`

	// Whether versioning is enabled
	// +kubebuilder:default:=false
	VersioningEnabled bool `json:"versioningEnabled,omitempty"`

	// ObjectLock is the object lock configuration of the bucket to be created
	ObjectLock BucketObjectLock `json:"objectLock,omitempty"`

	// ClusterRef is a reference to the Seaweed cluster where the bucket should be created
	// +kubebuilder:validation:Required
	ClusterRef ClusterReference `json:"clusterRef"`

	// Secret configuration for S3 credentials
	Secret *BucketSecretSpec `json:"secret,omitempty"`
}

type BucketObjectLock struct {
	// Enabled is whether object lock is enabled
	// +kubebuilder:default:=false
	Enabled bool `json:"enabled,omitempty"`

	// Mode is the mode of the object lock
	// +kubebuilder:default:="GOVERNANCE"
	Mode string `json:"mode,omitempty"`

	// Duration is the duration of the object lock
	Duration int32 `json:"duration,omitempty"`
}

// BucketQuota defines a quota for a bucket
type BucketQuota struct {
	// Size is the size of the quota
	Size int64 `json:"size"`

	// Unit is the unit of the quota
	Unit string `json:"unit"`

	// Enabled is whether the quota is enabled
	Enabled bool `json:"enabled"`
}

// BucketSecretSpec defines the configuration for creating a secret with S3 credentials
type BucketSecretSpec struct {
	// Whether to create a secret with S3 credentials
	// +kubebuilder:default:=true
	Enabled bool `json:"enabled,omitempty"`

	// Name of the secret to create (if empty, will use bucket name)
	Name string `json:"name,omitempty"`

	// Secret type to create
	// +kubebuilder:default:="Opaque"
	Type corev1.SecretType `json:"type,omitempty"`

	// Additional labels to add to the secret
	Labels map[string]string `json:"labels,omitempty"`

	// Additional annotations to add to the secret
	Annotations map[string]string `json:"annotations,omitempty"`
}

// ClusterReference defines a reference to a Seaweed cluster
type ClusterReference struct {
	// Name of the Seaweed cluster
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Namespace of the Seaweed cluster (if empty, uses the same namespace as BucketClaim)
	Namespace string `json:"namespace,omitempty"`
}

// BucketClaimStatus defines the observed state of BucketClaim
type BucketClaimStatus struct {
	// Phase represents the current phase of the bucket claim
	// +kubebuilder:validation:Enum=Pending;Creating;Ready;Failed
	Phase BucketClaimPhase `json:"phase,omitempty"`

	// Message provides additional information about the current phase
	Message string `json:"message,omitempty"`

	// Conditions represent the latest available observations of a bucket claim's current state
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// BucketInfo contains information about the created bucket
	BucketInfo *BucketInfo `json:"bucketInfo,omitempty"`

	// SecretInfo contains information about the created secret
	SecretInfo *BucketSecretInfo `json:"secretInfo,omitempty"`

	// LastUpdateTime is the timestamp of the last status update
	LastUpdateTime *metav1.Time `json:"lastUpdateTime,omitempty"`
}

type BucketSecretInfo struct {
	// Name of the created secret
	Name string `json:"name,omitempty"`

	// Namespace of the created secret
	Namespace string `json:"namespace,omitempty"`
}

// BucketClaimPhase represents the phase of a bucket claim
type BucketClaimPhase string

const (
	// BucketClaimPhasePending indicates the bucket claim is pending
	BucketClaimPhasePending BucketClaimPhase = "Pending"
	// BucketClaimPhaseCreating indicates the bucket is being created
	BucketClaimPhaseCreating BucketClaimPhase = "Creating"
	// BucketClaimPhaseReady indicates the bucket is ready
	BucketClaimPhaseReady BucketClaimPhase = "Ready"
	// BucketClaimPhaseFailed indicates the bucket creation failed
	BucketClaimPhaseFailed BucketClaimPhase = "Failed"
)

// BucketInfo contains information about a bucket
type BucketInfo struct {
	// Name of the bucket
	Name string `json:"name,omitempty"`

	// Creation timestamp
	CreatedAt *metav1.Time `json:"createdAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Bucket",type="string",JSONPath=".spec.bucketName"
// +kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".spec.clusterRef.name"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// BucketClaim is the Schema for the bucketclaims API
type BucketClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BucketClaimSpec   `json:"spec,omitempty"`
	Status BucketClaimStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BucketClaimList contains a list of BucketClaim
type BucketClaimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BucketClaim `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Seaweed{}, &SeaweedList{})
	SchemeBuilder.Register(&BucketClaim{}, &BucketClaimList{})
}
