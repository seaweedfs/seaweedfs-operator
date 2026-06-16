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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BackupPhase summarises a SeaweedBackup's lifecycle.
// +kubebuilder:validation:Enum=Pending;Running;Completed;Failed
type BackupPhase string

const (
	// BackupPhasePending means the snapshot Job has not been created yet
	// (waiting on the cluster, storage, or filer to become reachable).
	BackupPhasePending BackupPhase = "Pending"
	// BackupPhaseRunning means the snapshot Job is in flight.
	BackupPhaseRunning BackupPhase = "Running"
	// BackupPhaseCompleted means the snapshot Job finished successfully.
	BackupPhaseCompleted BackupPhase = "Completed"
	// BackupPhaseFailed means the snapshot Job failed.
	BackupPhaseFailed BackupPhase = "Failed"
)

// Condition types emitted by the SeaweedBackup controller.
const (
	// BackupConditionComplete is True once the snapshot Job succeeds.
	BackupConditionComplete = "Complete"
	// BackupConditionClusterReachable reports whether the referenced Seaweed
	// cluster and its backup storage configuration were resolvable.
	BackupConditionClusterReachable = "ClusterReachable"
)

// SeaweedBackup labels set on generated Jobs so the scheduler can find the
// SeaweedBackups belonging to a given cluster schedule.
const (
	// LabelBackupCluster names the owning Seaweed cluster.
	LabelBackupCluster = "seaweed.seaweedfs.com/backup-cluster"
	// LabelBackupSchedule names the schedule that created the backup (absent
	// for on-demand backups).
	LabelBackupSchedule = "seaweed.seaweedfs.com/backup-schedule"
)

// SeaweedBackupSpec is a single, on-demand or scheduled, point-in-time filer
// metadata snapshot (`fs.meta.save`) stored on a named backup storage.
type SeaweedBackupSpec struct {
	// ClusterName is the Seaweed CR, in the same namespace, to back up.
	// Immutable once set.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="clusterName is immutable"
	ClusterName string `json:"clusterName"`

	// StorageName references a key in the cluster's spec.backup.storages.
	// Immutable once set.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="storageName is immutable"
	StorageName string `json:"storageName"`

	// FilerPath is the filer subtree to snapshot. Defaults to "/".
	// +optional
	// +kubebuilder:default:="/"
	FilerPath string `json:"filerPath,omitempty"`
}

// SeaweedBackupStatus reflects the observed state of a backup.
type SeaweedBackupStatus struct {
	// ObservedGeneration is the .metadata.generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is a coarse summary of the backup's lifecycle.
	// +optional
	Phase BackupPhase `json:"phase,omitempty"`

	// JobName is the snapshot Job created for this backup.
	// +optional
	JobName string `json:"jobName,omitempty"`

	// Destination is the resolved location of the metadata snapshot.
	// +optional
	Destination string `json:"destination,omitempty"`

	// StartTime is when the snapshot Job started.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the snapshot Job finished.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// Conditions are the structured per-aspect state signals.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=swbk,categories=seaweedfs
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.clusterName`
// +kubebuilder:printcolumn:name="Storage",type=string,JSONPath=`.spec.storageName`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Completed",type=date,JSONPath=`.status.completionTime`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// SeaweedBackup is a point-in-time filer metadata snapshot of a Seaweed
// cluster, stored on one of the cluster's configured backup storages.
type SeaweedBackup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SeaweedBackupSpec   `json:"spec,omitempty"`
	Status SeaweedBackupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SeaweedBackupList contains a list of SeaweedBackup.
type SeaweedBackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SeaweedBackup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SeaweedBackup{}, &SeaweedBackupList{})
}
