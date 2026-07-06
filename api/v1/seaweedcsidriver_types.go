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
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// DefaultCSIDriverName is the CSI driver name registered with kubelet and
	// referenced by StorageClasses when spec.driverName is unset. A SeaweedFS
	// filer mounted through this name shows up to the kubelet as a single
	// node-global driver, so two SeaweedCSIDriver objects must not claim the
	// same name on the same cluster (see CSIConditionDriverNameConflict).
	DefaultCSIDriverName = "seaweedfs-csi-driver"

	// DefaultCSIDriverImage is the seaweedfs-csi-driver image, pinned to the
	// version this controller renders the sidecars and arguments for.
	DefaultCSIDriverImage = "chrislusf/seaweedfs-csi-driver:v1.4.20"

	// DefaultCSIMountImage is the seaweedfs-mount image used by the mount
	// service DaemonSet.
	DefaultCSIMountImage = "chrislusf/seaweedfs-mount:v1.4.20"

	// DefaultKubeletPath is the host kubelet root directory. The node plugin's
	// host paths (plugins, plugins_registry, pods) hang off this.
	DefaultKubeletPath = "/var/lib/kubelet"

	// DefaultMountSocketDir is the host directory shared between the mount
	// service and the node plugin for the mount gRPC socket.
	DefaultMountSocketDir = "/var/lib/seaweedfs-mount"
)

// SeaweedCSIDriverSpec defines the desired state of a SeaweedFS CSI driver
// deployment.
//
// A CSI driver is a node-global concern: the kubelet registers exactly one
// driver per driverName per node regardless of how many SeaweedFS clusters
// run on it. The operator therefore deploys the driver from a dedicated,
// opt-in object rather than as a sub-field of the Seaweed CR. The driver can
// mount either an operator-managed Seaweed cluster (SeaweedRef, grant-gated
// across namespaces) or any external filer (FilerAddress).
//
// +kubebuilder:validation:XValidation:rule="(has(self.seaweedRef) ? 1 : 0) + (has(self.filerAddress) ? 1 : 0) == 1",message="exactly one of seaweedRef or filerAddress must be set"
type SeaweedCSIDriverSpec struct {
	// SeaweedRef points at an operator-managed Seaweed CR whose filer the
	// driver mounts. A cross-namespace reference is denied unless a
	// ResourceReferenceGrant in the target Seaweed's namespace permits it,
	// matching the Bucket and S3 IAM CRDs. Mutually exclusive with
	// FilerAddress.
	// +optional
	SeaweedRef *SeaweedReference `json:"seaweedRef,omitempty"`

	// FilerAddress is an explicit filer HTTP host:port (e.g.
	// "my-filer.storage:8888") for mounting a filer not managed by this
	// operator. SeaweedFS derives the gRPC port internally. Mutually
	// exclusive with SeaweedRef.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	FilerAddress string `json:"filerAddress,omitempty"`

	// DriverName is the CSI driver name registered with kubelet and
	// referenced by StorageClasses. Node-global and immutable once set:
	// changing it would orphan existing PersistentVolumes that record the
	// old name. Defaults to "seaweedfs-csi-driver".
	// +optional
	// +kubebuilder:default:=seaweedfs-csi-driver
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="driverName is immutable"
	DriverName string `json:"driverName,omitempty"`

	// Image is the seaweedfs-csi-driver image used by both the controller
	// Deployment and the node DaemonSet.
	// +optional
	// +kubebuilder:default:="chrislusf/seaweedfs-csi-driver:v1.4.20"
	Image string `json:"image,omitempty"`

	// ImagePullPolicy applies to every container the operator renders for
	// this driver (the driver image and the sig-storage sidecars).
	// +optional
	ImagePullPolicy *corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// ImagePullSecrets is an optional list of secrets in the deployment
	// namespace used to pull the driver and sidecar images.
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// LogVerbosity sets the driver glog verbosity (-v) on the plugin
	// containers. Omit to use the image default.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=10
	LogVerbosity *int32 `json:"logVerbosity,omitempty"`

	// CacheCapacityMB is the per-node local chunk cache size in MiB
	// (--cacheCapacityMB). 0 disables the cache. Omit to use the image
	// default.
	// +optional
	// +kubebuilder:validation:Minimum=0
	CacheCapacityMB *int32 `json:"cacheCapacityMB,omitempty"`

	// ConcurrentWriters caps concurrent writer goroutines per mount
	// (--concurrentWriters). Omit to use the image default.
	// +optional
	// +kubebuilder:validation:Minimum=0
	ConcurrentWriters *int32 `json:"concurrentWriters,omitempty"`

	// ConcurrentReaders caps concurrent chunk fetches per read
	// (--concurrentReaders). Omit to use the image default.
	// +optional
	// +kubebuilder:validation:Minimum=0
	ConcurrentReaders *int32 `json:"concurrentReaders,omitempty"`

	// Sidecars overrides the upstream sig-storage sidecar images. Leave a
	// field empty to use the operator's pinned default.
	// +optional
	Sidecars CSISidecarImages `json:"sidecars,omitempty"`

	// Controller configures the controller-plane Deployment that runs the
	// external-provisioner, external-resizer and (optionally)
	// external-attacher next to the driver's controller service.
	// +optional
	Controller CSIControllerSpec `json:"controller,omitempty"`

	// Node configures the per-node DaemonSet that runs the driver's node
	// service and the node-driver-registrar.
	// +optional
	Node CSINodeSpec `json:"node,omitempty"`

	// MountService configures the privileged mount DaemonSet that performs
	// the FUSE mounts on behalf of the node plugin. It is enabled by default
	// and required for the node component to mount volumes.
	// +optional
	MountService CSIMountServiceSpec `json:"mountService,omitempty"`

	// StorageClass optionally manages a StorageClass bound to this driver.
	// Omit the block to leave StorageClass management to the cluster admin.
	// +optional
	StorageClass *CSIStorageClassSpec `json:"storageClass,omitempty"`
}

// CSISidecarImages overrides the upstream Kubernetes CSI sidecar images. All
// fields are optional; an empty value falls back to the operator's pinned
// default for that sidecar.
type CSISidecarImages struct {
	// Provisioner overrides the external-provisioner image.
	// +optional
	Provisioner string `json:"provisioner,omitempty"`

	// Attacher overrides the external-attacher image.
	// +optional
	Attacher string `json:"attacher,omitempty"`

	// Resizer overrides the external-resizer image.
	// +optional
	Resizer string `json:"resizer,omitempty"`

	// NodeDriverRegistrar overrides the node-driver-registrar image.
	// +optional
	NodeDriverRegistrar string `json:"nodeDriverRegistrar,omitempty"`

	// LivenessProbe overrides the livenessprobe sidecar image.
	// +optional
	LivenessProbe string `json:"livenessProbe,omitempty"`
}

// CSIControllerSpec configures the controller-plane Deployment.
type CSIControllerSpec struct {
	// Replicas is the controller Deployment replica count. Run 2+ with
	// leader election for HA. Defaults to 1.
	// +optional
	// +kubebuilder:default:=1
	// +kubebuilder:validation:Minimum=0
	Replicas *int32 `json:"replicas,omitempty"`

	// AttacherEnabled runs the external-attacher sidecar. SeaweedFS does not
	// truly attach volumes to nodes, but the attacher is kept for backward
	// compatibility and is enabled by default. Disabling it requires
	// deleting the CSIDriver object and any leftover VolumeAttachments by
	// hand. Defaults to true.
	// +optional
	// +kubebuilder:default:=true
	AttacherEnabled *bool `json:"attacherEnabled,omitempty"`

	// Resources are the compute resources for the driver container in the
	// controller pod.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// NodeSelector constrains the controller pod to matching nodes.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations applied to the controller pod.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// Affinity applied to the controller pod. When unset the operator
	// applies a soft pod anti-affinity so replicas spread across nodes.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`
}

// CSINodeSpec configures the per-node DaemonSet.
type CSINodeSpec struct {
	// KubeletPath is the host kubelet root directory the node plugin mounts
	// to expose volumes and register its socket. Override on distributions
	// that relocate it (MicroK8s: /var/snap/microk8s/common/var/lib/kubelet,
	// k0s: /var/lib/k0s/kubelet). Modern k3s keeps the default (older releases
	// nested it under the data dir but moved back because CSI plugins expect
	// the standard path). Defaults to /var/lib/kubelet.
	// +optional
	// +kubebuilder:default:="/var/lib/kubelet"
	KubeletPath string `json:"kubeletPath,omitempty"`

	// HostPID lets the node plugin enter container mount namespaces to repair
	// stale FUSE mounts after a restart. Defaults to true.
	// +optional
	// +kubebuilder:default:=true
	HostPID *bool `json:"hostPID,omitempty"`

	// Resources are the compute resources for the driver container in the
	// node pod.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// NodeSelector constrains which nodes run the node plugin. Empty means
	// every schedulable node.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations applied to the node pod. The operator adds a blanket
	// toleration so the plugin can run on tainted nodes (a node without the
	// plugin cannot mount SeaweedFS volumes); entries here are appended.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// UpdateStrategy is the DaemonSet update strategy type. Defaults to
	// RollingUpdate. Set to OnDelete for clusters where rolling the node
	// plugin would disrupt pods with live mounts.
	// +optional
	UpdateStrategy appsv1.DaemonSetUpdateStrategyType `json:"updateStrategy,omitempty"`
}

// CSIMountServiceSpec configures the mount DaemonSet that runs the FUSE
// mounts the node plugin delegates to over a shared host socket.
type CSIMountServiceSpec struct {
	// Enabled toggles the mount DaemonSet. It must be enabled for the node
	// component to mount volumes. Defaults to true.
	// +optional
	// +kubebuilder:default:=true
	Enabled *bool `json:"enabled,omitempty"`

	// Image is the seaweedfs-mount image.
	// +optional
	// +kubebuilder:default:="chrislusf/seaweedfs-mount:v1.4.20"
	Image string `json:"image,omitempty"`

	// SocketDir is the host directory shared with the node plugin for the
	// mount socket. Defaults to /var/lib/seaweedfs-mount.
	// +optional
	// +kubebuilder:default:="/var/lib/seaweedfs-mount"
	SocketDir string `json:"socketDir,omitempty"`

	// Resources are the compute resources for the mount container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// NodeSelector constrains which nodes run the mount service.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations applied to the mount pod. As with the node plugin a
	// blanket toleration is added by the operator and these are appended.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// UpdateStrategy is the mount DaemonSet update strategy type. Defaults to
	// OnDelete because the mount service is not yet resilient to restarting
	// underneath live mounts.
	// +optional
	UpdateStrategy appsv1.DaemonSetUpdateStrategyType `json:"updateStrategy,omitempty"`
}

// CSIStorageClassSpec describes a StorageClass the operator manages for this
// driver. The StorageClass is cluster-scoped, so the operator cannot set an
// owner reference on it; it is instead tracked and cleaned up via the
// SeaweedCSIDriver finalizer.
type CSIStorageClassSpec struct {
	// Name of the managed StorageClass. Defaults to the driver name.
	// +optional
	Name string `json:"name,omitempty"`

	// IsDefaultClass marks the StorageClass as the cluster default via the
	// storageclass.kubernetes.io/is-default-class annotation. At most one
	// default should exist cluster-wide; the operator does not police other
	// classes.
	// +optional
	// +kubebuilder:default:=false
	IsDefaultClass bool `json:"isDefaultClass,omitempty"`

	// ReclaimPolicy for dynamically provisioned volumes. Defaults to Delete.
	// +optional
	// +kubebuilder:default:=Delete
	// +kubebuilder:validation:Enum=Delete;Retain
	ReclaimPolicy *corev1.PersistentVolumeReclaimPolicy `json:"reclaimPolicy,omitempty"`

	// VolumeBindingMode controls when volume binding and provisioning
	// occur. Defaults to Immediate.
	// +optional
	// +kubebuilder:default:=Immediate
	// +kubebuilder:validation:Enum=Immediate;WaitForFirstConsumer
	VolumeBindingMode *storagev1.VolumeBindingMode `json:"volumeBindingMode,omitempty"`

	// AllowVolumeExpansion permits online expansion of volumes provisioned
	// by this class. Defaults to true.
	// +optional
	// +kubebuilder:default:=true
	AllowVolumeExpansion *bool `json:"allowVolumeExpansion,omitempty"`

	// Parameters are passed verbatim to the driver at provision time
	// (e.g. collection, replication, diskType, path). The operator does not
	// validate keys — see the seaweedfs-csi-driver docs for the accepted set.
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`

	// MountOptions are added to every PersistentVolume provisioned by this
	// class.
	// +optional
	// +listType=atomic
	MountOptions []string `json:"mountOptions,omitempty"`
}

// CSIDriverPhase is a coarse summary of the driver deployment's lifecycle.
// +kubebuilder:validation:Enum=Pending;Ready;Degraded;Failed;Terminating
type CSIDriverPhase string

const (
	// CSIDriverPhasePending is set before the controller and node workloads
	// have reported any readiness, or while a precondition (filer
	// reachability, reference grant) is unmet.
	CSIDriverPhasePending CSIDriverPhase = "Pending"
	// CSIDriverPhaseReady is set when the controller is available and the
	// node plugin is ready on every node it targets.
	CSIDriverPhaseReady CSIDriverPhase = "Ready"
	// CSIDriverPhaseDegraded is set when the deployment is partially
	// available — e.g. the controller is up but the node plugin is missing
	// on some nodes.
	CSIDriverPhaseDegraded CSIDriverPhase = "Degraded"
	// CSIDriverPhaseFailed is set on a non-retryable configuration error.
	CSIDriverPhaseFailed CSIDriverPhase = "Failed"
	// CSIDriverPhaseTerminating is set while finalizers run during deletion.
	CSIDriverPhaseTerminating CSIDriverPhase = "Terminating"
)

// Condition types emitted by the SeaweedCSIDriver controller. Reasons and
// messages are controller-defined; these type names are the API contract
// surfaced in `kubectl get seaweedcsidriver -o yaml`.
const (
	// CSIConditionReady summarises whether the driver matches spec and is
	// usable cluster-wide.
	CSIConditionReady = "Ready"
	// CSIConditionClusterReachable reports filer connectivity (only when a
	// SeaweedRef is used).
	CSIConditionClusterReachable = "ClusterReachable"
	// CSIConditionReferenceGranted is set False (reason ReferenceGrantMissing)
	// while a required cross-namespace ResourceReferenceGrant is absent.
	CSIConditionReferenceGranted = "ReferenceGranted"
	// CSIConditionControllerAvailable reports the controller Deployment's
	// availability.
	CSIConditionControllerAvailable = "ControllerAvailable"
	// CSIConditionNodeAvailable reports whether the node DaemonSet is ready
	// on every node it targets.
	CSIConditionNodeAvailable = "NodeAvailable"
	// CSIConditionDriverNameConflict is set True when another
	// SeaweedCSIDriver already manages the same driverName. The conflicting
	// object reconciles no workloads to avoid fighting over the kubelet's
	// node-global driver registration.
	CSIConditionDriverNameConflict = "DriverNameConflict"
)

// CSIComponentStatus reports observed replica readiness for one managed
// workload (the controller Deployment or the node DaemonSet).
type CSIComponentStatus struct {
	// Desired is the number of pods the workload wants scheduled (Deployment
	// replicas, or DaemonSet desiredNumberScheduled).
	// +optional
	Desired int32 `json:"desired,omitempty"`

	// Ready is the number of pods currently ready.
	// +optional
	Ready int32 `json:"ready,omitempty"`
}

// SeaweedCSIDriverStatus reflects the observed state of the driver deployment.
type SeaweedCSIDriverStatus struct {
	// ObservedGeneration is the .metadata.generation the controller last
	// reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is a coarse summary of the deployment's lifecycle.
	// +optional
	Phase CSIDriverPhase `json:"phase,omitempty"`

	// Conditions are the structured per-aspect state signals.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// DriverName echoes the registered CSI driver name actually used.
	// +optional
	DriverName string `json:"driverName,omitempty"`

	// ResolvedFilerAddress is the filer endpoint the driver was configured
	// with (the resolved SeaweedRef service address, or the explicit
	// FilerAddress).
	// +optional
	ResolvedFilerAddress string `json:"resolvedFilerAddress,omitempty"`

	// Controller reports the controller Deployment's replica readiness.
	// +optional
	Controller CSIComponentStatus `json:"controller,omitempty"`

	// Node reports the node DaemonSet's pod readiness across targeted nodes.
	// +optional
	Node CSIComponentStatus `json:"node,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=swcsi,categories=seaweedfs
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Driver",type=string,JSONPath=`.status.driverName`
// +kubebuilder:printcolumn:name="Filer",type=string,JSONPath=`.status.resolvedFilerAddress`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="CtrlReady",type=integer,JSONPath=`.status.controller.ready`
// +kubebuilder:printcolumn:name="NodesReady",type=integer,JSONPath=`.status.node.ready`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// SeaweedCSIDriver is the Schema for declaratively deploying the SeaweedFS
// CSI driver so workloads can mount a filer as PersistentVolumes. It manages
// the controller Deployment, the per-node and mount DaemonSets, the
// cluster-scoped CSIDriver object and RBAC, and an optional StorageClass.
type SeaweedCSIDriver struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SeaweedCSIDriverSpec   `json:"spec,omitempty"`
	Status SeaweedCSIDriverStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SeaweedCSIDriverList contains a list of SeaweedCSIDriver.
type SeaweedCSIDriverList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SeaweedCSIDriver `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SeaweedCSIDriver{}, &SeaweedCSIDriverList{})
}
