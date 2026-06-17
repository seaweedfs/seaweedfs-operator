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
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// ensureBackupMirrors reconciles the continuous `weed filer.backup` mirror
// Deployments declared in spec.backup.dataMirror, plus their rendered
// replication.toml Secrets, and prunes any that are no longer declared. It
// records per-mirror status on the CR in memory; updateStatus persists it.
func (r *SeaweedReconciler) ensureBackupMirrors(ctx context.Context, m *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	var mirrors []seaweedv1.BackupMirrorSpec
	if m.Spec.Backup != nil {
		mirrors = m.Spec.Backup.DataMirror
	}

	desired := map[string]bool{}
	var statuses []seaweedv1.BackupMirrorStatus

	for _, mirror := range mirrors {
		st, err := resolveStorage(m, mirror.StorageName)
		if err != nil {
			r.Log.Error(err, "backup mirror references unknown storage", "storage", mirror.StorageName)
			if r.Recorder != nil {
				r.Recorder.Eventf(m, corev1.EventTypeWarning, "BackupMirrorInvalid", "%v", err)
			}
			continue
		}

		creds, err := r.resolveBackupCredentials(ctx, m, st)
		if err != nil {
			return ReconcileResult(err)
		}

		toml, err := renderReplicationToml(mirror.StorageName, st, m.Name, creds)
		if err != nil {
			r.Log.Error(err, "render replication.toml", "storage", mirror.StorageName)
			continue
		}

		secretName := mirrorReplicationSecretName(m.Name, mirror.StorageName)
		if done, res, err := r.ensureMirrorSecret(ctx, m, secretName, mirror.StorageName, st, toml, creds); done {
			return done, res, err
		}

		depName := mirrorDeploymentName(m.Name, mirror.StorageName)
		hasGCSKey := len(creds[seaweedv1.BackupSecretKeyGCSCredentials]) > 0
		dep := buildMirrorDeployment(m, depName, secretName, mirror, st, hasGCSKey)
		if err := controllerutil.SetControllerReference(m, dep, r.Scheme); err != nil {
			return ReconcileResult(err)
		}
		if _, err := r.CreateOrUpdateDeployment(dep); err != nil {
			return ReconcileResult(err)
		}

		desired[depName] = true
		statuses = append(statuses, seaweedv1.BackupMirrorStatus{
			StorageName:    mirror.StorageName,
			DeploymentName: depName,
			Ready:          r.mirrorReady(ctx, m.Namespace, depName),
		})
	}

	if err := r.pruneBackupMirrors(ctx, m, desired); err != nil {
		return ReconcileResult(err)
	}

	m.Status.BackupMirrors = statuses
	return ReconcileResult(nil)
}

// ensureMirrorSecret renders the replication config Secret backing a mirror.
func (r *SeaweedReconciler) ensureMirrorSecret(ctx context.Context, m *seaweedv1.Seaweed, name, storage string, st seaweedv1.BackupStorageSpec, toml string, creds map[string][]byte) (bool, ctrl.Result, error) {
	data := map[string][]byte{"replication.toml": []byte(toml)}
	if st.Type == seaweedv1.BackupStorageGCS {
		if json := creds[seaweedv1.BackupSecretKeyGCSCredentials]; len(json) > 0 {
			data[gcsKeyFileName] = json
		}
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: m.Namespace,
			Labels:    labelsForBackupMirror(m.Name, storage),
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
	if err := controllerutil.SetControllerReference(m, secret, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateSecret(secret)
	return ReconcileResult(err)
}

// resolveBackupCredentials reads a storage's CredentialsSecret. A missing
// secret is tolerated for s3/gcs (ambient credentials) but is an error for
// azure/b2, whose keys must be baked into replication.toml.
func (r *SeaweedReconciler) resolveBackupCredentials(ctx context.Context, m *seaweedv1.Seaweed, st seaweedv1.BackupStorageSpec) (map[string][]byte, error) {
	if st.CredentialsSecret == nil || *st.CredentialsSecret == "" {
		return map[string][]byte{}, nil
	}
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: m.Namespace, Name: *st.CredentialsSecret}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("backup credentials secret %q not found in namespace %q", *st.CredentialsSecret, m.Namespace)
		}
		return nil, err
	}
	return secret.Data, nil
}

// mirrorReady reports whether a mirror Deployment has an available replica.
func (r *SeaweedReconciler) mirrorReady(ctx context.Context, namespace, name string) bool {
	var dep appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &dep); err != nil {
		return false
	}
	return dep.Status.AvailableReplicas > 0
}

// pruneBackupMirrors deletes mirror Deployments and their config Secrets that
// are no longer declared in spec.backup.dataMirror.
func (r *SeaweedReconciler) pruneBackupMirrors(ctx context.Context, m *seaweedv1.Seaweed, keep map[string]bool) error {
	selector := client.MatchingLabels{
		"app.kubernetes.io/component": "backup-mirror",
		"app.kubernetes.io/instance":  m.Name,
	}

	var deps appsv1.DeploymentList
	if err := r.List(ctx, &deps, client.InNamespace(m.Namespace), selector); err != nil {
		return err
	}
	for i := range deps.Items {
		d := &deps.Items[i]
		if keep[d.Name] {
			continue
		}
		if err := r.Delete(ctx, d); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}

	var secrets corev1.SecretList
	if err := r.List(ctx, &secrets, client.InNamespace(m.Namespace), selector); err != nil {
		return err
	}
	for i := range secrets.Items {
		s := &secrets.Items[i]
		// The mirror Deployment and its config Secret share the storage label;
		// keep a Secret only if its sibling Deployment is still desired.
		storage := s.Labels["seaweed.seaweedfs.com/backup-storage"]
		if storage != "" && keep[mirrorDeploymentName(m.Name, storage)] {
			continue
		}
		if err := r.Delete(ctx, s); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}
