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

// RestorePhase summarises a SeaweedRestore's lifecycle.
// +kubebuilder:validation:Enum=Pending;Running;Completed;Failed
type RestorePhase string

const (
	RestorePhasePending   RestorePhase = "Pending"
	RestorePhaseRunning   RestorePhase = "Running"
	RestorePhaseCompleted RestorePhase = "Completed"
	RestorePhaseFailed    RestorePhase = "Failed"
)

// Condition types emitted by the SeaweedRestore controller.
const (
	// RestoreConditionComplete is True once the restore Job succeeds.
	RestoreConditionComplete = "Complete"
	// RestoreConditionSourceResolved reports whether the backup source
	// (a SeaweedBackup or an explicit BackupSource) was resolvable.
	RestoreConditionSourceResolved = "SourceResolved"
)

// BackupSource locates a metadata snapshot directly, for restoring from a
// backup whose SeaweedBackup CR no longer exists (or was taken by another
// cluster).
type BackupSource struct {
	// StorageName references a key in the target cluster's spec.backup.storages.
	// +kubebuilder:validation:MinLength=1
	StorageName string `json:"storageName"`

	// MetaPath is the snapshot location within the storage, relative to the
	// storage's directory/mount root (e.g. "<cluster>/<backup>/filer.meta.gz").
	// +kubebuilder:validation:MinLength=1
	MetaPath string `json:"metaPath"`
}

// SeaweedRestoreSpec restores a filer metadata snapshot into a Seaweed cluster
// via `fs.meta.load`. Exactly one of BackupName / BackupSource must be set.
//
// +kubebuilder:validation:XValidation:rule="has(self.backupName) != has(self.backupSource)",message="exactly one of backupName or backupSource must be set"
type SeaweedRestoreSpec struct {
	// ClusterName is the Seaweed CR, in the same namespace, to restore into.
	// Immutable once set.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="clusterName is immutable"
	ClusterName string `json:"clusterName"`

	// BackupName references a SeaweedBackup in the same namespace to restore.
	// +optional
	BackupName string `json:"backupName,omitempty"`

	// BackupSource locates a snapshot explicitly when no SeaweedBackup exists.
	// +optional
	BackupSource *BackupSource `json:"backupSource,omitempty"`

	// FilerPath is the filer subtree the snapshot is loaded under. Defaults to
	// "/".
	// +optional
	// +kubebuilder:default:="/"
	FilerPath string `json:"filerPath,omitempty"`
}

// SeaweedRestoreStatus reflects the observed state of a restore.
type SeaweedRestoreStatus struct {
	// ObservedGeneration is the .metadata.generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is a coarse summary of the restore's lifecycle.
	// +optional
	Phase RestorePhase `json:"phase,omitempty"`

	// JobName is the restore Job created for this resource.
	// +optional
	JobName string `json:"jobName,omitempty"`

	// StartTime is when the restore Job started.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the restore Job finished.
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
// +kubebuilder:resource:shortName=swr,categories=seaweedfs
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.clusterName`
// +kubebuilder:printcolumn:name="Backup",type=string,JSONPath=`.spec.backupName`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Completed",type=date,JSONPath=`.status.completionTime`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// SeaweedRestore restores a filer metadata snapshot into a Seaweed cluster.
type SeaweedRestore struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SeaweedRestoreSpec   `json:"spec,omitempty"`
	Status SeaweedRestoreStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SeaweedRestoreList contains a list of SeaweedRestore.
type SeaweedRestoreList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SeaweedRestore `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SeaweedRestore{}, &SeaweedRestoreList{})
}
