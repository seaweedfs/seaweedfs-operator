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
	SFTPPort          = 2222 // Default SFTP listen port

	MasterGRPCPort = MasterHTTPPort + GRPCPortDelta
	VolumeGRPCPort = VolumeHTTPPort + GRPCPortDelta
	FilerGRPCPort  = FilerHTTPPort + GRPCPortDelta
	AdminGRPCPort  = AdminHTTPPort + GRPCPortDelta
)

// IngressSpec is per-component Ingress configuration. When Enabled, the
// operator creates a networking.k8s.io/v1 Ingress pointing at the
// component's Service. This is independent of the legacy HostSuffix
// all-in-one Ingress, which remains supported for backward compatibility.
type IngressSpec struct {
	// Enabled turns on Ingress generation for this component.
	Enabled bool `json:"enabled,omitempty"`

	// ClassName is the name of the IngressClass to use.
	// +optional
	ClassName *string `json:"className,omitempty"`

	// Host is the hostname the Ingress listens on. Required when enabled.
	// +optional
	Host string `json:"host,omitempty"`

	// Path under which the component is served. Defaults to "/".
	// +kubebuilder:default:="/"
	Path string `json:"path,omitempty"`

	// Annotations to apply to the generated Ingress resource. Useful for
	// setting controller-specific annotations (nginx.ingress.kubernetes.io/...
	// etc.) without the operator needing to know about them.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// TLS is a pass-through of Ingress TLS config (hostnames + secret).
	// +optional
	TLS []IngressTLS `json:"tls,omitempty"`
}

// IngressTLS mirrors the networking.k8s.io/v1 IngressTLS fields we care
// about, avoiding a direct schema dependency on k8s.io/api from the CRD.
type IngressTLS struct {
	Hosts      []string `json:"hosts,omitempty"`
	SecretName string   `json:"secretName,omitempty"`
}

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

	// S3 is a top-level, standalone S3 gateway running as a Deployment.
	// Prefer this over FilerSpec.S3 for new clusters — embedded filer S3
	// is deprecated and cannot be scaled independently of the filer.
	// +optional
	S3 *S3GatewaySpec `json:"s3,omitempty"`

	// SFTP is a top-level, standalone SFTP gateway running as a Deployment.
	// Requires spec.filer — the gateway dials the filer for its backing
	// store and serves SSH clients on its configured port.
	// +optional
	SFTP *SFTPSpec `json:"sftp,omitempty"`

	// Backup configures cluster backup: named destinations (storages),
	// cron-driven metadata snapshot schedules, and continuous data mirrors.
	// See BackupSpec and BACKUP_SUPPORT.md.
	// +optional
	Backup *BackupSpec `json:"backup,omitempty"`

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

	// Base labels of Pods, components may add or override labels upon this respectively.
	// Note that user-supplied keys cannot replace the operator-managed selector labels
	// (app.kubernetes.io/name, /instance, /component, /managed-by) — those are required
	// for the StatefulSet/Deployment selector and will be preserved.
	Labels map[string]string `json:"labels,omitempty"`

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

	// LoggingArgs are command line flags placed before the weed subcommand,
	// e.g. ["-logJson", "-v=2"]. seaweedfs's logging flags (defined by the
	// embedded fla9 parser) must precede the subcommand — they are rejected
	// after "master", "filer", etc. — so they cannot be expressed through
	// ExtraArgs. When this slice is non-empty the operator drops its default
	// `-logtostderr=true` and emits the user-supplied flags verbatim, giving
	// full control over log destination, format, and verbosity. Components
	// may override via ComponentSpec.LoggingArgs.
	// +optional
	// +listType=atomic
	LoggingArgs []string `json:"loggingArgs,omitempty"`
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

	// S3 standalone gateway status (SeaweedSpec.S3)
	// +optional
	S3 ComponentStatus `json:"s3,omitempty"`

	// SFTP standalone gateway status (SeaweedSpec.SFTP)
	// +optional
	SFTP ComponentStatus `json:"sftp,omitempty"`

	// BackupMirrors reports the state of the continuous data-mirror
	// Deployments managed for spec.backup.dataMirror.
	// +optional
	// +listType=map
	// +listMapKey=storageName
	BackupMirrors []BackupMirrorStatus `json:"backupMirrors,omitempty"`
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

	// Ingress configuration for the master HTTP UI.
	// +optional
	Ingress *IngressSpec `json:"ingress,omitempty"`
}

// VolumeServerConfig contains common configuration for volume servers
type VolumeServerConfig struct {
	ComponentSpec               `json:",inline"`
	corev1.ResourceRequirements `json:",inline"`

	Service          *ServiceSpec `json:"service,omitempty"`
	StorageClassName *string      `json:"storageClassName,omitempty"`

	// StorageSelector is applied to each PVC template's spec.selector to
	// bind volume-server PVCs to pre-provisioned PVs with matching labels.
	// +optional
	StorageSelector *metav1.LabelSelector `json:"storageSelector,omitempty"`

	// StorageAnnotations is applied to each volume-server PVC template's
	// metadata.annotations — for CSI provisioners that read PVC annotations at
	// provision time (e.g. NetApp Trident snapshot policy). Set this at cluster
	// creation: volumeClaimTemplates are immutable, so editing it on a running
	// cluster is not auto-applied (the operator emits a VolumeClaimTemplatesMismatch
	// warning and the StatefulSet must be recreated for new PVCs to pick it up);
	// already-provisioned PVCs keep the metadata they were created with.
	// +optional
	StorageAnnotations map[string]string `json:"storageAnnotations,omitempty"`

	// StorageLabels is applied to each volume-server PVC template's
	// metadata.labels. Same immutability caveat as StorageAnnotations.
	// +optional
	StorageLabels map[string]string `json:"storageLabels,omitempty"`

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

// VolumeServerKind selects the workload used to run volume servers.
type VolumeServerKind string

const (
	VolumeServerStatefulSet VolumeServerKind = "StatefulSet"
	VolumeServerDaemonSet   VolumeServerKind = "DaemonSet"
)

// VolumeServerHostPath maps a node-local directory into a volume server as a
// data directory.
type VolumeServerHostPath struct {
	// Path is the absolute host directory, mounted at /data<index> and passed
	// to `weed volume -dir`.
	// +kubebuilder:validation:MinLength=1
	Path string `json:"path"`

	// MaxVolumeCount caps volumes in this directory (0 = until the disk fills).
	// When any entry sets it, the operator emits a per-directory -max list;
	// otherwise the single MaxVolumeCounts value applies.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxVolumeCount *int32 `json:"maxVolumeCount,omitempty"`

	// Type is the hostPath type checked by the kubelet before mounting.
	// +kubebuilder:validation:Enum="";DirectoryOrCreate;Directory;FileOrCreate;File;Socket;CharDevice;BlockDevice
	// +kubebuilder:default:="DirectoryOrCreate"
	// +optional
	Type *corev1.HostPathType `json:"type,omitempty"`
}

// VolumeSpec is the spec for volume servers
type VolumeSpec struct {
	VolumeServerConfig `json:",inline"`

	// The desired ready replicas
	// +kubebuilder:validation:Minimum=0
	Replicas int32 `json:"replicas"`

	// Kind selects how volume servers are deployed. "StatefulSet" (the default)
	// scales PVC-backed replicas by Replicas; "DaemonSet" runs one volume
	// server per selected node, ignores Replicas, and requires HostPath.
	// Changing Kind makes the operator delete the previous workload.
	// +kubebuilder:validation:Enum=StatefulSet;DaemonSet
	// +kubebuilder:default:=StatefulSet
	// +optional
	Kind VolumeServerKind `json:"kind,omitempty"`

	// HostPath uses node-local directories as the volume server's data dirs
	// instead of PVCs: each is mounted at /data<index> and passed to
	// `weed volume -dir`. Required for DaemonSet mode (no volumeClaimTemplates);
	// with a StatefulSet, pair it with node anti-affinity.
	// +optional
	// +listType=atomic
	HostPath []VolumeServerHostPath `json:"hostPath,omitempty"`

	// Topology configuration for rack/datacenter-aware placement
	// +kubebuilder:validation:Optional
	Rack *string `json:"rack,omitempty"`
	// +kubebuilder:validation:Optional
	DataCenter *string `json:"dataCenter,omitempty"`

	// Ingress configuration for the volume server HTTP port.
	// +optional
	Ingress *IngressSpec `json:"ingress,omitempty"`
}

// IsDaemonSet reports whether volume servers run as a DaemonSet (unset Kind
// means StatefulSet).
func (v *VolumeSpec) IsDaemonSet() bool {
	return v != nil && v.Kind == VolumeServerDaemonSet
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
//
// Deprecated: S3 embedded in the filer cannot be scaled independently of the
// filer and will be removed in a future release. Prefer SeaweedSpec.S3 for
// new clusters.
type S3Config struct {
	// +kubebuilder:default:=true
	Enabled      bool                      `json:"enabled,omitempty"`
	ConfigSecret *corev1.SecretKeySelector `json:"configSecret,omitempty"`
}

// S3GatewaySpec defines a standalone S3 gateway Deployment that runs
// independently of the filer StatefulSet. This is the preferred way to
// expose S3 — it can scale separately, use its own resources, and live
// behind its own Ingress.
type S3GatewaySpec struct {
	ComponentSpec               `json:",inline"`
	corev1.ResourceRequirements `json:",inline"`

	// The desired number of replicas. S3 is stateless — scale freely.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default:=1
	Replicas int32 `json:"replicas"`

	// Service is the k8s Service in front of the gateway pods.
	// +optional
	Service *ServiceSpec `json:"service,omitempty"`

	// ConfigSecret references a Secret containing the S3 identities config
	// (the equivalent of -s3.config on the weed binary). The Secret key is
	// mounted at /etc/sw/<key>.
	// +optional
	ConfigSecret *corev1.SecretKeySelector `json:"configSecret,omitempty"`

	// MetricsPort, if set, enables the Prometheus metrics listener on this
	// port and causes the operator to provision a matching ServiceMonitor
	// (when the Prometheus Operator CRD is available).
	// +optional
	MetricsPort *int32 `json:"metricsPort,omitempty"`

	// Port overrides the default S3 HTTP port (8333).
	// +optional
	Port *int32 `json:"port,omitempty"`

	// DomainName is the suffix used for virtual-hosted-style buckets
	// (`{bucket}.{domainName}`). Passed through to the weed s3 -domainName
	// flag. See https://github.com/seaweedfs/seaweedfs/wiki/Amazon-S3-API.
	// +optional
	DomainName *string `json:"domainName,omitempty"`

	// IAM enables/disables the embedded IAM API on the same port.
	// Defaults to true, matching the filer-embedded S3 behavior.
	// +kubebuilder:default:=true
	IAM bool `json:"iam,omitempty"`

	// Ingress configuration for the standalone S3 gateway.
	// +optional
	Ingress *IngressSpec `json:"ingress,omitempty"`
}

// SFTPSpec defines a standalone SFTP gateway Deployment. The SFTP server
// dials the filer via the filer's in-cluster Service and serves SSH
// clients on its configured port.
type SFTPSpec struct {
	ComponentSpec               `json:",inline"`
	corev1.ResourceRequirements `json:",inline"`

	// The desired number of replicas. SFTP is stateless — scale freely.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default:=1
	Replicas int32 `json:"replicas"`

	// Service is the k8s Service in front of the gateway pods.
	// +optional
	Service *ServiceSpec `json:"service,omitempty"`

	// Port overrides the default SFTP port (2222).
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	Port *int32 `json:"port,omitempty"`

	// MetricsPort, if set, enables the Prometheus metrics listener on
	// this port and causes the operator to provision a matching
	// ServiceMonitor (when the Prometheus Operator CRD is available).
	// +optional
	MetricsPort *int32 `json:"metricsPort,omitempty"`

	// UserStoreSecret references a key inside a Secret that holds the
	// user database file. Only the referenced key is projected into the
	// pod (at /etc/sw/<key>), and that path is passed to weed via
	// -userStoreFile. Omit to run the gateway without user auth
	// (public mode).
	// +optional
	UserStoreSecret *corev1.SecretKeySelector `json:"userStoreSecret,omitempty"`

	// HostKeysSecret references a Secret containing the SSH host keys
	// the SFTP server presents to clients. The Secret is mounted
	// read-only at /etc/sw/ssh. Omit to let the server generate an
	// ephemeral host key on startup (clients will see a changed
	// fingerprint on every pod restart — fine for dev, bad for prod).
	// +optional
	HostKeysSecret *corev1.LocalObjectReference `json:"hostKeysSecret,omitempty"`

	// AuthMethods is passed to weed sftp -authMethods (e.g. "password",
	// "publickey", or a comma-separated list). Omit to accept the
	// server's defaults.
	// +optional
	AuthMethods *string `json:"authMethods,omitempty"`

	// MaxAuthTries is passed to weed sftp -maxAuthTries.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxAuthTries *int32 `json:"maxAuthTries,omitempty"`

	// Ingress configuration for the SFTP gateway. Note that plain SFTP
	// (SSH over TCP) typically does not route through HTTP Ingress —
	// prefer a LoadBalancer/NodePort Service for most clusters. This
	// field is included for ingress controllers that support TCP
	// passthrough (e.g. nginx with ssl-passthrough, Traefik TCP routers).
	// +optional
	Ingress *IngressSpec `json:"ingress,omitempty"`
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

	// Ingress configuration for the filer HTTP port.
	// +optional
	Ingress *IngressSpec `json:"ingress,omitempty"`

	// S3Ingress configuration for the filer's embedded S3 gateway port.
	// Only used when Filer.S3.Enabled is true. Separate from Ingress so
	// S3 can live on a different hostname than the filer HTTP UI.
	// +optional
	S3Ingress *IngressSpec `json:"s3Ingress,omitempty"`

	// GRPCIngress configuration for the filer gRPC port (FilerHTTPPort +
	// 10000), used by clients such as `weed mount` and the HDFS connector.
	// HTTP and gRPC live on separate ports and an Ingress backend carries
	// one protocol, so gRPC needs its own Ingress on its own hostname. Set
	// the controller's gRPC backend-protocol annotation (for ingress-nginx,
	// nginx.ingress.kubernetes.io/backend-protocol: "GRPC") via Annotations.
	// +optional
	GRPCIngress *IngressSpec `json:"grpcIngress,omitempty"`
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
	// The secret should have keys: adminUser, adminPassword, and optionally
	// readOnlyUser, readOnlyPassword. Each key is projected into the admin
	// pod as the env var weed admin reads for it — WEED_ADMIN_USER,
	// WEED_ADMIN_PASSWORD, WEED_ADMIN_READONLY_USER, WEED_ADMIN_READONLY_PASSWORD
	// respectively — using optional secretKeyRefs, so missing optional keys
	// do not block pod startup. Secret updates require a pod restart to take
	// effect.
	CredentialsSecret *corev1.LocalObjectReference `json:"credentialsSecret,omitempty"`

	// Ingress configuration for the admin UI HTTP port.
	// +optional
	Ingress *IngressSpec `json:"ingress,omitempty"`
}

// WorkerSpec is the spec for worker processes
type WorkerSpec struct {
	ComponentSpec               `json:",inline"`
	corev1.ResourceRequirements `json:",inline"`

	// The desired ready replicas
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default:=1
	Replicas int32 `json:"replicas"`

	// MetricsPort is the port that the prometheus metrics export listens on.
	// The worker's readiness and liveness probes hit /ready and /health on this
	// port, so the operator only creates them when metricsPort is set — and a
	// readinessProbe/livenessProbe override therefore has no effect unless
	// metricsPort is also configured.
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

// ProbeOverride tunes the timing fields of an operator-managed readiness probe.
// Each nil field falls back to the operator's default for that probe; the
// probe handler (HTTP path, port, and scheme) is always set by the operator
// and is not overridable. Mirrors the tunable scalar fields of corev1.Probe.
// Liveness probes use LivenessProbeOverride instead (no SuccessThreshold).
type ProbeOverride struct {
	// Number of seconds after the container has started before the probe is
	// initiated.
	// +kubebuilder:validation:Minimum=0
	// +optional
	InitialDelaySeconds *int32 `json:"initialDelaySeconds,omitempty"`

	// Number of seconds after which the probe times out.
	// +kubebuilder:validation:Minimum=1
	// +optional
	TimeoutSeconds *int32 `json:"timeoutSeconds,omitempty"`

	// How often (in seconds) to perform the probe.
	// +kubebuilder:validation:Minimum=1
	// +optional
	PeriodSeconds *int32 `json:"periodSeconds,omitempty"`

	// Minimum consecutive successes for the probe to be considered successful
	// after having failed.
	// +kubebuilder:validation:Minimum=1
	// +optional
	SuccessThreshold *int32 `json:"successThreshold,omitempty"`

	// Minimum consecutive failures for the probe to be considered failed after
	// having succeeded.
	// +kubebuilder:validation:Minimum=1
	// +optional
	FailureThreshold *int32 `json:"failureThreshold,omitempty"`
}

// LivenessProbeOverride is ProbeOverride without SuccessThreshold: Kubernetes
// requires a liveness probe's successThreshold to be exactly 1, so it is not
// exposed here (the operator always leaves it at 1).
type LivenessProbeOverride struct {
	// Number of seconds after the container has started before the probe is
	// initiated.
	// +kubebuilder:validation:Minimum=0
	// +optional
	InitialDelaySeconds *int32 `json:"initialDelaySeconds,omitempty"`

	// Number of seconds after which the probe times out.
	// +kubebuilder:validation:Minimum=1
	// +optional
	TimeoutSeconds *int32 `json:"timeoutSeconds,omitempty"`

	// How often (in seconds) to perform the probe.
	// +kubebuilder:validation:Minimum=1
	// +optional
	PeriodSeconds *int32 `json:"periodSeconds,omitempty"`

	// Minimum consecutive failures for the probe to be considered failed after
	// having succeeded.
	// +kubebuilder:validation:Minimum=1
	// +optional
	FailureThreshold *int32 `json:"failureThreshold,omitempty"`
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

	// ServiceAccountName of the component's pods. When set, the operator
	// renders pod.spec.serviceAccountName so the component runs under a
	// dedicated ServiceAccount instead of the namespace's default SA. The
	// operator does not create the ServiceAccount — it must already exist
	// in the same namespace as the Seaweed CR. Required on clusters that
	// bind SCCs or PSA-restricted privileges per-SA (e.g. OpenShift) and
	// want to avoid granting elevated permissions to the default SA.
	// +optional
	ServiceAccountName *string `json:"serviceAccountName,omitempty"`

	// SchedulerName of the component. Override the cluster-level one if present
	SchedulerName *string `json:"schedulerName,omitempty"`

	// NodeSelector of the component. Merged into the cluster-level nodeSelector if non-empty
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Annotations of the component. Merged into the cluster-level annotations if non-empty
	Annotations map[string]string `json:"annotations,omitempty"`

	// Labels of the component. Merged into the cluster-level labels and onto the pod
	// template labels. Operator-managed selector labels (app.kubernetes.io/name,
	// /instance, /component, /managed-by) cannot be overridden and will always win,
	// so the StatefulSet/Deployment selector keeps matching its pods.
	Labels map[string]string `json:"labels,omitempty"`

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

	// ReadinessProbe tunes the timing fields of this component's
	// operator-managed readiness probe. Each field left unset keeps the
	// operator's default; the probe handler (HTTP path, port, and scheme) is
	// always managed by the operator and cannot be overridden here. Useful for
	// speeding up startup on test clusters — e.g. lowering the volume server's
	// 90s default periodSeconds.
	// +optional
	ReadinessProbe *ProbeOverride `json:"readinessProbe,omitempty"`

	// LivenessProbe tunes the timing fields of this component's
	// operator-managed liveness probe. See ReadinessProbe. (No successThreshold:
	// Kubernetes requires a liveness probe's successThreshold to be 1.)
	// +optional
	LivenessProbe *LivenessProbeOverride `json:"livenessProbe,omitempty"`

	// LoggingArgs are command line flags placed before the weed subcommand
	// (e.g. ["-logJson", "-v=2"]). When non-empty, this slice fully replaces
	// the cluster-level SeaweedSpec.LoggingArgs for this component — there
	// is no per-flag merge, matching how ExtraArgs and Tolerations work.
	// Leave unset to inherit the cluster-level value. See
	// SeaweedSpec.LoggingArgs for the rationale (logging flags must precede
	// the subcommand and therefore cannot be expressed via ExtraArgs).
	// +optional
	// +listType=atomic
	LoggingArgs []string `json:"loggingArgs,omitempty"`

	// Sidecars are additional containers run alongside the operator-managed
	// container in each pod of this component. Use this to attach helpers
	// like `weed filer.sync` next to a filer, or a log shipper next to any
	// component. Sidecars share the pod's network and lifecycle but are
	// otherwise unmanaged — the operator does not inject env vars, volumes,
	// or probes into them. Reference any extra volumes through
	// ComponentSpec.Volumes.
	//
	// The schema is preserved as opaque to keep the CRD small enough to
	// install via `kubectl apply` (the inlined v1.Container schema would
	// blow past the 256KB last-applied-configuration annotation limit
	// when repeated across components). Validation happens at pod-create
	// time instead of at CR admission.
	// +optional
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Sidecars []corev1.Container `json:"sidecars,omitempty"`

	// InitContainers run to completion, in order, before the operator-managed
	// container of this component starts. Use this to gate the SeaweedFS
	// process on external dependencies — wait for a metadata store (Cassandra,
	// MySQL, Postgres) to be reachable, wait for a schema/keyspace, fix file
	// ownership on a mounted PVC, seed certs into a shared volume, or run a
	// migration. Sidecars cannot substitute: a sidecar starts in parallel
	// with the main container, so the SeaweedFS process has already
	// attempted (and exited) before the sidecar's wait loop completes.
	//
	// The list is appended to the operator-managed initContainers (if any)
	// and runs after them, so user containers can rely on operator-managed
	// init steps. Init containers share the pod's volumes — reference any
	// extra volumes through ComponentSpec.Volumes / ComponentSpec.VolumeMounts.
	// The operator does not inject env vars, probes, or volume mounts into
	// user-supplied init containers.
	//
	// Schema is preserved as opaque for the same CRD-size reason as Sidecars
	// above; validation happens at pod-create time.
	// +optional
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	InitContainers []corev1.Container `json:"initContainers,omitempty"`

	// PodSecurityContext sets pod-level security attributes on this
	// component's pod template, rendered into pod.spec.securityContext. Use
	// it to run the SeaweedFS process as a non-root user (runAsUser /
	// runAsNonRoot), make mounted PVCs group-writable (fsGroup), or pin a
	// seccomp profile pod-wide. The SeaweedFS image runs as root by default,
	// so this is the field to set on PodSecurityStandards-restricted or
	// OpenShift clusters. Leaving it unset preserves the prior behavior.
	// +optional
	PodSecurityContext *corev1.PodSecurityContext `json:"podSecurityContext,omitempty"`

	// ContainerSecurityContext sets container-level security attributes on
	// the operator-managed container of this component, rendered into the
	// container's securityContext. Use it to drop Linux capabilities, set
	// runAsNonRoot / runAsUser, enable readOnlyRootFilesystem, or forbid
	// privilege escalation. Where they overlap, settings here take
	// precedence over PodSecurityContext for this container. It is applied
	// only to the operator-managed container — set securityContext directly
	// on any user-supplied Sidecars or InitContainers.
	// +optional
	ContainerSecurityContext *corev1.SecurityContext `json:"containerSecurityContext,omitempty"`
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

	// Annotations is applied to the generated PVC template's
	// metadata.annotations — for CSI provisioners that read PVC annotations at
	// provision time (e.g. NetApp Trident snapshot policy). Ignored when
	// ExistingClaim is set, since the operator does not own that PVC. Set at
	// creation: like the rest of this template it is immutable afterwards, so a
	// later edit is not auto-applied and existing PVCs keep their metadata.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// Labels is applied to the generated PVC template's metadata.labels. Same
	// ExistingClaim and immutability caveats as Annotations.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
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
