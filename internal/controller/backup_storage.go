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

package controller

import (
	"fmt"
	"path"
	"strings"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

const (
	// backupConfigDir is where the data-mirror Deployment finds its rendered
	// replication.toml (and, on TLS clusters, security.toml + the GCS key).
	backupConfigDir = "/etc/seaweedfs"

	// backupScratchDir is the emptyDir snapshot/restore Jobs stage files in.
	backupScratchDir = "/scratch"

	// gcsKeyFileName is the basename of the GCS service-account key, dropped
	// next to replication.toml so the sink's google_application_credentials
	// path resolves inside the pod.
	gcsKeyFileName = "gcs.json"

	// reservedBackupFilerDir is the hidden filer subtree a snapshot Job stages
	// its .meta.gz into when the storage is an object store, so the storage's
	// data mirror carries it off-cluster. Restore reads it back from here.
	reservedBackupFilerDir = "/.seaweedfs-operator/backups"

	// defaultFilerPath is the filer subtree backed up when none is given.
	defaultFilerPath = "/"
)

// backupImage returns the weed image backup/restore/mirror pods run, honoring
// spec.backup.image and falling back to the cluster image.
func backupImage(m *seaweedv1.Seaweed) string {
	if m.Spec.Backup != nil && m.Spec.Backup.Image != nil && *m.Spec.Backup.Image != "" {
		return *m.Spec.Backup.Image
	}
	return m.Spec.Image
}

// filesystemMountPath returns the in-pod mount path for a filesystem storage,
// defaulting to /backup.
func filesystemMountPath(fs *seaweedv1.FilesystemBackupStore) string {
	if fs != nil && fs.MountPath != "" {
		return fs.MountPath
	}
	return "/backup"
}

// filerPathOrDefault normalizes an optional filer path.
func filerPathOrDefault(p string) string {
	if p == "" {
		return defaultFilerPath
	}
	return p
}

// metaRelPath is the snapshot's location relative to a storage root:
// <cluster>/<backup>/filer.meta.gz. Used for filesystem reads/writes and as
// the suffix under reservedBackupFilerDir for object stores.
func metaRelPath(cluster, backupName string) string {
	return path.Join(cluster, backupName, "filer.meta.gz")
}

// reservedFilerMetaPath is where a snapshot Job stages its .meta.gz inside the
// source filer for object-store storages.
func reservedFilerMetaPath(backupName string) string {
	return path.Join(reservedBackupFilerDir, backupName, "filer.meta.gz")
}

// mirrorSinkDirectory is the destination prefix the data mirror writes file
// content under, isolating each cluster's data within a shared storage.
func mirrorSinkDirectory(storageName string, st seaweedv1.BackupStorageSpec, cluster string) string {
	base := "/"
	switch st.Type {
	case seaweedv1.BackupStorageS3:
		if st.S3 != nil {
			base = st.S3.Directory
		}
	case seaweedv1.BackupStorageGCS:
		if st.GCS != nil {
			base = st.GCS.Directory
		}
	case seaweedv1.BackupStorageAzure:
		if st.Azure != nil {
			base = st.Azure.Directory
		}
	case seaweedv1.BackupStorageB2:
		if st.B2 != nil {
			base = st.B2.Directory
		}
	case seaweedv1.BackupStorageFilesystem:
		// The PVC is mounted with SubPath, so the in-pod mount path is already
		// the backup-area root — don't fold SubPath in again here.
		base = filesystemMountPath(st.Filesystem)
	}
	if base == "" {
		base = "/"
	}
	return path.Join(base, cluster, "data")
}

// tomlString renders a value as a quoted TOML basic string.
func tomlString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`)
	return `"` + r.Replace(s) + `"`
}

// renderReplicationToml builds the replication.toml a data-mirror Deployment
// feeds to `weed filer.backup`. Exactly one [sink.*] section is emitted for
// the storage's type, with credentials baked in from creds (resolved from the
// storage's CredentialsSecret). For object stores without a secret, the
// credential fields are left empty so weed falls back to its ambient chain.
func renderReplicationToml(storageName string, st seaweedv1.BackupStorageSpec, cluster string, creds map[string][]byte) (string, error) {
	get := func(k string) string {
		if creds == nil {
			return ""
		}
		return string(creds[k])
	}
	dir := mirrorSinkDirectory(storageName, st, cluster)

	var b strings.Builder
	switch st.Type {
	case seaweedv1.BackupStorageS3:
		if st.S3 == nil {
			return "", fmt.Errorf("storage %q: type s3 requires the s3 block", storageName)
		}
		region := st.S3.Region
		if region == "" {
			region = "us-east-2"
		}
		forcePath := true
		if st.S3.ForcePathStyle != nil {
			forcePath = *st.S3.ForcePathStyle
		}
		fmt.Fprintf(&b, "[sink.s3]\nenabled = true\n")
		fmt.Fprintf(&b, "aws_access_key_id = %s\n", tomlString(get(seaweedv1.BackupSecretKeyAWSAccessKeyID)))
		fmt.Fprintf(&b, "aws_secret_access_key = %s\n", tomlString(get(seaweedv1.BackupSecretKeyAWSSecretAccessKey)))
		fmt.Fprintf(&b, "region = %s\n", tomlString(region))
		fmt.Fprintf(&b, "bucket = %s\n", tomlString(st.S3.Bucket))
		fmt.Fprintf(&b, "directory = %s\n", tomlString(dir))
		fmt.Fprintf(&b, "endpoint = %s\n", tomlString(st.S3.Endpoint))
		fmt.Fprintf(&b, "s3_force_path_style = %t\n", forcePath)
		fmt.Fprintf(&b, "is_incremental = false\n")
	case seaweedv1.BackupStorageGCS:
		if st.GCS == nil {
			return "", fmt.Errorf("storage %q: type gcs requires the gcs block", storageName)
		}
		fmt.Fprintf(&b, "[sink.google_cloud_storage]\nenabled = true\n")
		fmt.Fprintf(&b, "google_application_credentials = %s\n", tomlString(path.Join(backupConfigDir, gcsKeyFileName)))
		fmt.Fprintf(&b, "bucket = %s\n", tomlString(st.GCS.Bucket))
		fmt.Fprintf(&b, "directory = %s\n", tomlString(dir))
		fmt.Fprintf(&b, "is_incremental = false\n")
	case seaweedv1.BackupStorageAzure:
		if st.Azure == nil {
			return "", fmt.Errorf("storage %q: type azure requires the azure block", storageName)
		}
		fmt.Fprintf(&b, "[sink.azure]\nenabled = true\n")
		fmt.Fprintf(&b, "account_name = %s\n", tomlString(st.Azure.AccountName))
		fmt.Fprintf(&b, "account_key = %s\n", tomlString(get(seaweedv1.BackupSecretKeyAzureAccountKey)))
		fmt.Fprintf(&b, "container = %s\n", tomlString(st.Azure.Container))
		fmt.Fprintf(&b, "directory = %s\n", tomlString(dir))
		fmt.Fprintf(&b, "is_incremental = false\n")
	case seaweedv1.BackupStorageB2:
		if st.B2 == nil {
			return "", fmt.Errorf("storage %q: type b2 requires the b2 block", storageName)
		}
		fmt.Fprintf(&b, "[sink.backblaze]\nenabled = true\n")
		fmt.Fprintf(&b, "b2_account_id = %s\n", tomlString(get(seaweedv1.BackupSecretKeyB2AccountID)))
		fmt.Fprintf(&b, "b2_master_application_key = %s\n", tomlString(get(seaweedv1.BackupSecretKeyB2AppKey)))
		fmt.Fprintf(&b, "b2_region = %s\n", tomlString(st.B2.Region))
		fmt.Fprintf(&b, "bucket = %s\n", tomlString(st.B2.Bucket))
		fmt.Fprintf(&b, "directory = %s\n", tomlString(dir))
		fmt.Fprintf(&b, "is_incremental = false\n")
	case seaweedv1.BackupStorageFilesystem:
		if st.Filesystem == nil {
			return "", fmt.Errorf("storage %q: type filesystem requires the filesystem block", storageName)
		}
		fmt.Fprintf(&b, "[sink.local]\nenabled = true\n")
		fmt.Fprintf(&b, "directory = %s\n", tomlString(dir))
		fmt.Fprintf(&b, "is_incremental = false\n")
	default:
		return "", fmt.Errorf("storage %q: unsupported type %q", storageName, st.Type)
	}
	return b.String(), nil
}

// resolveStorage returns the named storage from a cluster's backup config, or
// an error the caller can surface as a status condition.
func resolveStorage(m *seaweedv1.Seaweed, name string) (seaweedv1.BackupStorageSpec, error) {
	if m.Spec.Backup == nil || len(m.Spec.Backup.Storages) == 0 {
		return seaweedv1.BackupStorageSpec{}, fmt.Errorf("cluster %q has no spec.backup.storages configured", m.Name)
	}
	st, ok := m.Spec.Backup.Storages[name]
	if !ok {
		return seaweedv1.BackupStorageSpec{}, fmt.Errorf("storage %q not found in cluster %q spec.backup.storages", name, m.Name)
	}
	return st, nil
}
