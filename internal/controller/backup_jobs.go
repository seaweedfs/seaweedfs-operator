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

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// backupBackoffLimit caps Job retries before the snapshot/restore is marked
// Failed. Snapshots are cheap to re-run, so keep it low.
const backupBackoffLimit int32 = 3

// backupStorageVolumeName is the PVC volume name used by filesystem-storage
// snapshot/restore/mirror pods.
const backupStorageVolumeName = "backup-storage"

// weedCmd joins the standard `weed <flags> <subcommand> <args...>` invocation,
// reusing weedPreamble so TLS clusters get -config_dir wired in.
func weedCmd(m *seaweedv1.Seaweed, subcommand string, args ...string) string {
	parts := weedPreamble(m, nil, subcommand)
	parts = append(parts, args...)
	return strings.Join(parts, " ")
}

// metaLoadStatement builds the `fs.meta.load` shell statement, scoping to a
// subtree via -dirPrefix when filerPath is not the root.
func metaLoadStatement(file, filerPath string) string {
	if fp := filerPathOrDefault(filerPath); fp != "/" {
		return fmt.Sprintf("fs.meta.load -dirPrefix=%s %s", fp, file)
	}
	return "fs.meta.load " + file
}

// filesystemPVCVolume returns the PVC volume + mount for a filesystem storage,
// mounting at the storage's MountPath with its SubPath so all pods see the
// same backup-area root.
func filesystemPVCVolume(fs *seaweedv1.FilesystemBackupStore) (corev1.Volume, corev1.VolumeMount) {
	vol := corev1.Volume{
		Name: backupStorageVolumeName,
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: fs.ExistingClaim,
			},
		},
	}
	mount := corev1.VolumeMount{
		Name:      backupStorageVolumeName,
		MountPath: filesystemMountPath(fs),
		SubPath:   fs.SubPath,
	}
	return vol, mount
}

// snapshotScript returns the shell program a snapshot Job runs. For filesystem
// storages it writes the .meta.gz straight onto the PVC; for object stores it
// stages the snapshot into a reserved filer path that the data mirror carries
// off-cluster. `test -s` guards against `weed shell` swallowing a save error.
func snapshotScript(m *seaweedv1.Seaweed, st seaweedv1.BackupStorageSpec, cluster, backupName, filerPath string) (script, destination string) {
	masters := getMasterPeersString(m)
	filer := getFilerAddress(m)
	fp := filerPathOrDefault(filerPath)
	shell := weedCmd(m, "shell", "-master="+masters, "-filer="+filer)

	if st.Type == seaweedv1.BackupStorageFilesystem {
		out := path.Join(filesystemMountPath(st.Filesystem), metaRelPath(cluster, backupName))
		script = strings.Join([]string{
			"set -euo pipefail",
			fmt.Sprintf("mkdir -p %s", path.Dir(out)),
			fmt.Sprintf("echo 'fs.meta.save -o %s %s' | %s", out, fp, shell),
			fmt.Sprintf("test -s %s", out),
			fmt.Sprintf("echo 'metadata snapshot written to %s'", out),
			"",
		}, "\n")
		return script, out
	}

	scratch := path.Join(backupScratchDir, "filer.meta.gz")
	// http:// is intentional even on TLS clusters: the operator's TLS is gRPC
	// mTLS (security.toml), and filer.copy talks to the filer over gRPC (wired
	// via -config_dir); the filer's HTTP endpoint stays plain HTTP.
	dstURL := fmt.Sprintf("http://%s%s/%s/", filer, reservedBackupFilerDir, backupName)
	copyCmd := weedCmd(m, "filer.copy", scratch, dstURL)
	staged := reservedFilerMetaPath(backupName)
	script = strings.Join([]string{
		"set -euo pipefail",
		fmt.Sprintf("echo 'fs.meta.save -o %s %s' | %s", scratch, fp, shell),
		fmt.Sprintf("test -s %s", scratch),
		copyCmd,
		fmt.Sprintf("echo 'metadata snapshot staged at filer %s; data mirror replicates it to storage %q'", staged, mirrorSinkDirectory("", st, cluster)),
		"",
	}, "\n")
	return script, staged
}

// restoreScript returns the shell program a restore Job runs. localPath is set
// for filesystem sources (read straight off the PVC); filerURL is set for
// object-store sources (read back from the reserved filer path via filer.cat).
func restoreScript(m *seaweedv1.Seaweed, localPath, filerURL, filerPath string) string {
	masters := getMasterPeersString(m)
	filer := getFilerAddress(m)
	shell := weedCmd(m, "shell", "-master="+masters, "-filer="+filer)

	if localPath != "" {
		return strings.Join([]string{
			"set -euo pipefail",
			fmt.Sprintf("test -s %s", localPath),
			fmt.Sprintf("echo '%s' | %s", metaLoadStatement(localPath, filerPath), shell),
			fmt.Sprintf("echo 'metadata restored from %s'", localPath),
			"",
		}, "\n")
	}

	scratch := path.Join(backupScratchDir, "restore.meta.gz")
	catCmd := weedCmd(m, "filer.cat", "-o", scratch, filerURL)
	return strings.Join([]string{
		"set -euo pipefail",
		catCmd,
		fmt.Sprintf("test -s %s", scratch),
		fmt.Sprintf("echo '%s' | %s", metaLoadStatement(scratch, filerPath), shell),
		fmt.Sprintf("echo 'metadata restored from %s'", filerURL),
		"",
	}, "\n")
}

// backupPodSpec assembles the shared one-container pod that runs `command`,
// wiring TLS config, an emptyDir scratch dir, and (for filesystem storages)
// the backup PVC.
func backupPodSpec(m *seaweedv1.Seaweed, name, command string, st seaweedv1.BackupStorageSpec, mountPVC bool) corev1.PodSpec {
	scratchVol := corev1.Volume{
		Name:         "scratch",
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	}
	mounts := []corev1.VolumeMount{{Name: "scratch", MountPath: backupScratchDir}}
	volumes := []corev1.Volume{scratchVol}

	if mountPVC && st.Type == seaweedv1.BackupStorageFilesystem && st.Filesystem != nil {
		vol, mount := filesystemPVCVolume(st.Filesystem)
		volumes = append(volumes, vol)
		mounts = append(mounts, mount)
	}
	if tlsVols, tlsMounts := tlsVolumesAndMounts(m); len(tlsVols) > 0 {
		volumes = append(volumes, tlsVols...)
		mounts = append(mounts, tlsMounts...)
	}

	enableServiceLinks := false
	return corev1.PodSpec{
		RestartPolicy:      corev1.RestartPolicyNever,
		ImagePullSecrets:   m.Spec.ImagePullSecrets,
		EnableServiceLinks: &enableServiceLinks,
		Containers: []corev1.Container{{
			Name:            name,
			Image:           backupImage(m),
			ImagePullPolicy: m.Spec.ImagePullPolicy,
			Command:         []string{"/bin/sh", "-ec", command},
			VolumeMounts:    mounts,
		}},
		Volumes: volumes,
	}
}

// buildSnapshotJob returns the metadata-snapshot Job for a SeaweedBackup.
func buildSnapshotJob(m *seaweedv1.Seaweed, jobName string, backup *seaweedv1.SeaweedBackup, st seaweedv1.BackupStorageSpec) (*batchv1.Job, string) {
	script, dest := snapshotScript(m, st, backup.Spec.ClusterName, backup.Name, backup.Spec.FilerPath)
	pod := backupPodSpec(m, "snapshot", script, st, true)
	labels := map[string]string{
		seaweedv1.LabelBackupCluster: backup.Spec.ClusterName,
	}
	if sched := backup.Labels[seaweedv1.LabelBackupSchedule]; sched != "" {
		labels[seaweedv1.LabelBackupSchedule] = sched
	}
	return newJob(backup.Namespace, jobName, labels, pod), dest
}

// buildRestoreJob returns the restore Job for a SeaweedRestore. Exactly one of
// localPath / filerURL is set by the caller depending on the storage type.
func buildRestoreJob(m *seaweedv1.Seaweed, jobName string, restore *seaweedv1.SeaweedRestore, st seaweedv1.BackupStorageSpec, localPath, filerURL string) *batchv1.Job {
	script := restoreScript(m, localPath, filerURL, restore.Spec.FilerPath)
	pod := backupPodSpec(m, "restore", script, st, localPath != "")
	labels := map[string]string{seaweedv1.LabelBackupCluster: restore.Spec.ClusterName}
	return newJob(restore.Namespace, jobName, labels, pod)
}

// newJob wraps a pod spec in a one-shot Job.
func newJob(namespace, name string, labels map[string]string, pod corev1.PodSpec) *batchv1.Job {
	backoff := backupBackoffLimit
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       pod,
			},
		},
	}
}

// buildMirrorDeployment returns the continuous `weed filer.backup` Deployment
// for a data mirror. The rendered replication.toml (and, on TLS clusters,
// security.toml + the GCS key) are projected into backupConfigDir.
func buildMirrorDeployment(m *seaweedv1.Seaweed, name, replicationSecret string, mirror seaweedv1.BackupMirrorSpec, st seaweedv1.BackupStorageSpec, hasGCSKey bool) *appsv1.Deployment {
	filer := getFilerAddress(m)
	fp := filerPathOrDefault(mirror.FilerPath)

	cmd := []string{"weed"}
	if la := m.Spec.LoggingArgs; len(la) > 0 {
		cmd = append(cmd, la...)
	} else {
		cmd = append(cmd, "-logtostderr=true")
	}
	cmd = append(cmd,
		"-config_dir="+backupConfigDir,
		"filer.backup",
		"-filer="+filer,
		"-filerPath="+fp,
		"-initialSnapshot",
	)

	labels := labelsForBackupMirror(m.Name, mirror.StorageName)

	// Project replication.toml (+ security.toml + gcs.json on demand) into one
	// config dir so `weed -config_dir` finds everything it needs.
	configSources := []corev1.VolumeProjection{{
		Secret: &corev1.SecretProjection{
			LocalObjectReference: corev1.LocalObjectReference{Name: replicationSecret},
			Items: []corev1.KeyToPath{
				{Key: "replication.toml", Path: "replication.toml"},
			},
		},
	}}
	// Only project the GCS key file when it was actually rendered into the
	// Secret (a key was supplied). Projecting a key that isn't in the Secret
	// makes the kubelet fail the volume mount, so ambient-credential GCS
	// mirrors must not reference it.
	if st.Type == seaweedv1.BackupStorageGCS && hasGCSKey {
		configSources[0].Secret.Items = append(configSources[0].Secret.Items, corev1.KeyToPath{Key: gcsKeyFileName, Path: gcsKeyFileName})
	}

	volumes := []corev1.Volume{}
	var mounts []corev1.VolumeMount
	if securityConfigNeeded(m) {
		configSources = append(configSources, corev1.VolumeProjection{
			Secret: &corev1.SecretProjection{
				LocalObjectReference: corev1.LocalObjectReference{Name: SecurityConfigSecretName(m)},
				Items:                []corev1.KeyToPath{{Key: "security.toml", Path: "security.toml"}},
			},
		})
	}
	volumes = append(volumes, corev1.Volume{
		Name:         "weed-config",
		VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: configSources}},
	})
	mounts = append(mounts, corev1.VolumeMount{Name: "weed-config", ReadOnly: true, MountPath: backupConfigDir})

	// The TLS certs that security.toml references still mount at their canonical
	// path; reuse the shared helper and drop its security-config mount (we
	// project security.toml above instead).
	if tlsEffective(m) {
		volumes = append(volumes, corev1.Volume{
			Name: tlsVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: TLSServerSecretName(m)},
			},
		})
		mounts = append(mounts, corev1.VolumeMount{Name: tlsVolumeName, ReadOnly: true, MountPath: tlsMountPath})
	}

	if st.Type == seaweedv1.BackupStorageFilesystem && st.Filesystem != nil {
		vol, mount := filesystemPVCVolume(st.Filesystem)
		volumes = append(volumes, vol)
		mounts = append(mounts, mount)
	}

	replicas := int32(1)
	enableServiceLinks := false
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: m.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			// filer.backup keeps a local checkpoint; never run two at once.
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					ImagePullSecrets:   m.Spec.ImagePullSecrets,
					EnableServiceLinks: &enableServiceLinks,
					Containers: []corev1.Container{{
						Name:            "filer-backup",
						Image:           backupImage(m),
						ImagePullPolicy: m.Spec.ImagePullPolicy,
						Command:         cmd,
						VolumeMounts:    mounts,
					}},
					Volumes: volumes,
				},
			},
		},
	}
}

// mirrorDeploymentName is the deterministic name of a mirror Deployment.
func mirrorDeploymentName(cluster, storage string) string {
	return fmt.Sprintf("%s-backup-mirror-%s", cluster, storage)
}

// mirrorReplicationSecretName is the deterministic name of the rendered
// replication.toml Secret backing a mirror Deployment.
func mirrorReplicationSecretName(cluster, storage string) string {
	return fmt.Sprintf("%s-backup-mirror-%s-config", cluster, storage)
}

// labelsForBackupMirror are the selector labels for a mirror Deployment.
func labelsForBackupMirror(cluster, storage string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":               "seaweedfs",
		"app.kubernetes.io/component":          "backup-mirror",
		"app.kubernetes.io/instance":           cluster,
		"app.kubernetes.io/managed-by":         "seaweedfs-operator",
		seaweedv1.LabelBackupCluster:           cluster,
		"seaweed.seaweedfs.com/backup-storage": storage,
	}
}
