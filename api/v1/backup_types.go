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

// This file defines the Percona-style backup configuration carried on the
// Seaweed CR (spec.backup). It is the shared vocabulary used by the
// SeaweedBackup / SeaweedRestore CRDs and by the continuous data-mirror
// reconciler.
//
// Two complementary mechanisms back the cluster up, because that is what the
// underlying `weed` primitives actually support:
//
//   - Metadata: `fs.meta.save` / `fs.meta.load` produce/consume a one-shot
//     `.meta.gz` snapshot of the filer tree. These are point-in-time and
//     schedulable — the unit a SeaweedBackup represents.
//   - Data: `weed filer.backup` continuously replicates file content into a
//     replication sink (s3/gcs/azure/b2/local). It is a long-running daemon,
//     so it is modeled as a Deployment (spec.backup.dataMirror), not a Job.

// BackupStorageType enumerates the replication sink backends a storage maps to.
// +kubebuilder:validation:Enum=s3;gcs;azure;b2;filesystem
type BackupStorageType string

const (
	// BackupStorageS3 is an AWS-S3-compatible object store (AWS, MinIO, or a
	// SeaweedFS S3 gateway).
	BackupStorageS3 BackupStorageType = "s3"
	// BackupStorageGCS is Google Cloud Storage.
	BackupStorageGCS BackupStorageType = "gcs"
	// BackupStorageAzure is Azure Blob Storage.
	BackupStorageAzure BackupStorageType = "azure"
	// BackupStorageB2 is Backblaze B2.
	BackupStorageB2 BackupStorageType = "b2"
	// BackupStorageFilesystem writes to a mounted PersistentVolumeClaim via the
	// local replication sink.
	BackupStorageFilesystem BackupStorageType = "filesystem"
)

// Well-known keys read from a storage's CredentialsSecret. They mirror the
// environment variable names the AWS / GCS / Azure / B2 SDKs expect so that a
// single Secret works regardless of how the value is consumed.
const (
	// S3 / B2-via-S3 credentials.
	BackupSecretKeyAWSAccessKeyID     = "AWS_ACCESS_KEY_ID"
	BackupSecretKeyAWSSecretAccessKey = "AWS_SECRET_ACCESS_KEY"
	// GCS: the contents of the service-account JSON key file.
	BackupSecretKeyGCSCredentials = "GOOGLE_APPLICATION_CREDENTIALS_JSON"
	// Azure Blob storage account key.
	BackupSecretKeyAzureAccountKey = "AZURE_STORAGE_ACCOUNT_KEY"
	// Backblaze B2 native credentials.
	BackupSecretKeyB2AccountID = "B2_ACCOUNT_ID"
	BackupSecretKeyB2AppKey    = "B2_MASTER_APPLICATION_KEY"
)

// S3BackupStore configures an AWS-S3-compatible replication sink. Credentials
// come from the storage's CredentialsSecret (AWS_ACCESS_KEY_ID /
// AWS_SECRET_ACCESS_KEY); when the secret is omitted the weed sink falls back
// to the ambient AWS credential chain (instance profile, IRSA, etc.).
type S3BackupStore struct {
	// Bucket is an existing bucket that receives the backup.
	// +kubebuilder:validation:MinLength=1
	Bucket string `json:"bucket"`

	// Region of the bucket. Defaults to us-east-2 (the weed sink default).
	// +optional
	Region string `json:"region,omitempty"`

	// Endpoint overrides the S3 endpoint for non-AWS providers (e.g. a
	// SeaweedFS S3 gateway or MinIO). Leave empty for AWS.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// Directory is the destination prefix inside the bucket. Defaults to "/".
	// +optional
	// +kubebuilder:default:="/"
	Directory string `json:"directory,omitempty"`

	// ForcePathStyle uses path-style addressing (bucket in the path, not the
	// host). Required by most S3-compatible providers. Defaults to true.
	// +optional
	// +kubebuilder:default:=true
	ForcePathStyle *bool `json:"forcePathStyle,omitempty"`
}

// GCSBackupStore configures a Google Cloud Storage replication sink. The
// service-account JSON is supplied via the storage's CredentialsSecret under
// GOOGLE_APPLICATION_CREDENTIALS_JSON.
type GCSBackupStore struct {
	// Bucket is an existing bucket that receives the backup.
	// +kubebuilder:validation:MinLength=1
	Bucket string `json:"bucket"`

	// Directory is the destination prefix inside the bucket. Defaults to "/".
	// +optional
	// +kubebuilder:default:="/"
	Directory string `json:"directory,omitempty"`
}

// AzureBackupStore configures an Azure Blob Storage replication sink. The
// account key is supplied via the storage's CredentialsSecret under
// AZURE_STORAGE_ACCOUNT_KEY.
type AzureBackupStore struct {
	// AccountName is the Azure storage account name.
	// +kubebuilder:validation:MinLength=1
	AccountName string `json:"accountName"`

	// Container is an existing blob container that receives the backup.
	// +kubebuilder:validation:MinLength=1
	Container string `json:"container"`

	// Directory is the destination prefix inside the container. Defaults to "/".
	// +optional
	// +kubebuilder:default:="/"
	Directory string `json:"directory,omitempty"`
}

// B2BackupStore configures a Backblaze B2 replication sink. The account id and
// master application key are supplied via the storage's CredentialsSecret
// under B2_ACCOUNT_ID / B2_MASTER_APPLICATION_KEY.
type B2BackupStore struct {
	// Bucket is an existing bucket that receives the backup.
	// +kubebuilder:validation:MinLength=1
	Bucket string `json:"bucket"`

	// Region of the bucket.
	// +optional
	Region string `json:"region,omitempty"`

	// Directory is the destination prefix inside the bucket. Defaults to "/".
	// +optional
	// +kubebuilder:default:="/"
	Directory string `json:"directory,omitempty"`
}

// FilesystemBackupStore writes backups to a PersistentVolumeClaim via the weed
// local sink. This is the self-contained path for metadata snapshots: the
// snapshot is written straight to the mounted volume, no object-store upload
// and no data mirror required. Use an RWX claim if more than one backup pod
// may run concurrently.
type FilesystemBackupStore struct {
	// ExistingClaim is the name of a PersistentVolumeClaim, in the cluster's
	// namespace, that holds backups.
	// +kubebuilder:validation:MinLength=1
	ExistingClaim string `json:"existingClaim"`

	// MountPath is where the claim is mounted inside the backup pod. Defaults
	// to /backup.
	// +optional
	// +kubebuilder:default:="/backup"
	MountPath string `json:"mountPath,omitempty"`

	// SubPath within the claim to mount. Optional.
	// +optional
	SubPath string `json:"subPath,omitempty"`
}

// BackupStorageSpec is one named destination. The per-type sub-block matching
// Type must be set — validated by a CEL rule on the element itself (rather than
// iterating the map from the parent) so the rule cost stays a small constant.
//
// +kubebuilder:validation:XValidation:rule="(self.type != 's3' || has(self.s3)) && (self.type != 'gcs' || has(self.gcs)) && (self.type != 'azure' || has(self.azure)) && (self.type != 'b2' || has(self.b2)) && (self.type != 'filesystem' || has(self.filesystem))",message="storage must set the sub-block matching its type"
type BackupStorageSpec struct {
	// Type selects which sub-block below is used.
	// +kubebuilder:validation:Required
	Type BackupStorageType `json:"type"`

	// +optional
	S3 *S3BackupStore `json:"s3,omitempty"`
	// +optional
	GCS *GCSBackupStore `json:"gcs,omitempty"`
	// +optional
	Azure *AzureBackupStore `json:"azure,omitempty"`
	// +optional
	B2 *B2BackupStore `json:"b2,omitempty"`
	// +optional
	Filesystem *FilesystemBackupStore `json:"filesystem,omitempty"`

	// CredentialsSecret names a Secret, in the cluster's namespace, holding the
	// sink credentials (see the BackupSecretKey* keys). Optional for s3/gcs when
	// the pod has ambient credentials; required for azure/b2; unused for
	// filesystem.
	// +optional
	CredentialsSecret *string `json:"credentialsSecret,omitempty"`
}

// BackupScheduleSpec drives recurring metadata snapshots. The operator's
// internal scheduler creates a SeaweedBackup for the named storage each time
// the cron fires, then prunes completed snapshots beyond Keep.
type BackupScheduleSpec struct {
	// Name identifies the schedule and prefixes the SeaweedBackups it creates.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=50
	Name string `json:"name"`

	// Schedule is a standard cron expression, e.g. "0 2 * * *".
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=120
	Schedule string `json:"schedule"`

	// StorageName references a key in spec.backup.storages.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=50
	StorageName string `json:"storageName"`

	// Keep retains at most this many most-recent completed SeaweedBackups for
	// this schedule; older ones are deleted. 0 keeps all.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Keep int32 `json:"keep,omitempty"`

	// FilerPath is the filer subtree to snapshot. Defaults to "/".
	// +optional
	// +kubebuilder:default:="/"
	FilerPath string `json:"filerPath,omitempty"`

	// Suspend pauses this schedule without removing it.
	// +optional
	// +kubebuilder:default:=false
	Suspend bool `json:"suspend,omitempty"`
}

// BackupMirrorSpec declares a continuous `weed filer.backup` mirror Deployment
// that streams file data into a storage sink. This is the data half of a
// backup; for object-store storages it is also what carries metadata snapshots
// (which the snapshot Job stages into a reserved filer path) off-cluster.
type BackupMirrorSpec struct {
	// StorageName references a key in spec.backup.storages.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=50
	StorageName string `json:"storageName"`

	// FilerPath is the filer subtree to mirror. Defaults to "/".
	// +optional
	// +kubebuilder:default:="/"
	FilerPath string `json:"filerPath,omitempty"`
}

// BackupSpec is the cluster-level backup configuration (Seaweed.spec.backup).
//
// The per-storage type/sub-block check lives on BackupStorageSpec itself; here
// CEL only checks that schedules/mirrors reference a defined storage. The
// collections are bounded (MaxProperties/MaxItems) so these rules stay within
// the apiserver's CEL cost budget. Cross-field checks CEL cannot express live
// in the validating webhook (validateBackup).
//
// +kubebuilder:validation:XValidation:rule="!has(self.schedule) || self.schedule.all(s, s.storageName in self.storages)",message="schedule.storageName must reference a defined storage"
// +kubebuilder:validation:XValidation:rule="!has(self.dataMirror) || self.dataMirror.all(m, m.storageName in self.storages)",message="dataMirror.storageName must reference a defined storage"
type BackupSpec struct {
	// Image optionally overrides the weed image used by backup/restore/mirror
	// pods. Defaults to the cluster's spec.image.
	// +optional
	Image *string `json:"image,omitempty"`

	// Storages is the set of named backup destinations.
	// +kubebuilder:validation:MinProperties=1
	// +kubebuilder:validation:MaxProperties=32
	Storages map[string]BackupStorageSpec `json:"storages"`

	// Schedule is a list of cron-driven metadata snapshot schedules.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=32
	Schedule []BackupScheduleSpec `json:"schedule,omitempty"`

	// DataMirror is a list of continuous data-replication mirrors.
	// +optional
	// +listType=map
	// +listMapKey=storageName
	// +kubebuilder:validation:MaxItems=32
	DataMirror []BackupMirrorSpec `json:"dataMirror,omitempty"`
}

// BackupMirrorStatus reports the state of one continuous data mirror.
type BackupMirrorStatus struct {
	// StorageName is the mirror's destination storage.
	StorageName string `json:"storageName"`
	// DeploymentName is the managed `weed filer.backup` Deployment.
	// +optional
	DeploymentName string `json:"deploymentName,omitempty"`
	// Ready reports whether the mirror Deployment has an available replica.
	// +optional
	Ready bool `json:"ready,omitempty"`
}
