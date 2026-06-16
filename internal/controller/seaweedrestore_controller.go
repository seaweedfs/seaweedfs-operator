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
	"path"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// SeaweedRestoreReconciler turns a SeaweedRestore into a one-shot
// `fs.meta.load` Job and tracks its outcome on the CR's status.
type SeaweedRestoreReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=seaweedrestores,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=seaweedrestores/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=seaweedrestores/finalizers,verbs=update

// Reconcile implements the SeaweedRestore lifecycle.
func (r *SeaweedRestoreReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("seaweedrestore", req.NamespacedName)

	var restore seaweedv1.SeaweedRestore
	if err := r.Get(ctx, req.NamespacedName, &restore); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if restore.Status.Phase == seaweedv1.RestorePhaseCompleted || restore.Status.Phase == seaweedv1.RestorePhaseFailed {
		return ctrl.Result{}, nil
	}

	var cluster seaweedv1.Seaweed
	if err := r.Get(ctx, types.NamespacedName{Namespace: restore.Namespace, Name: restore.Spec.ClusterName}, &cluster); err != nil {
		if apierrors.IsNotFound(err) {
			return r.pending(ctx, &restore, "ClusterNotFound",
				"Seaweed cluster "+restore.Spec.ClusterName+" not found in namespace "+restore.Namespace)
		}
		return ctrl.Result{}, err
	}

	src, err := r.resolveSource(ctx, &restore)
	if err != nil {
		return r.pending(ctx, &restore, "SourceUnresolved", err.Error())
	}
	st, err := resolveStorage(&cluster, src.storageName)
	if err != nil {
		return r.pending(ctx, &restore, "StorageNotFound", err.Error())
	}

	// Translate the resolved snapshot location into how the Job reads it:
	// filesystem storages read straight off the PVC at a relative path; object
	// stores read back from the reserved filer path via filer.cat.
	var localPath, filerURL string
	if st.Type == seaweedv1.BackupStorageFilesystem {
		rel := src.metaPath
		if rel == "" {
			rel = metaRelPath(src.cluster, src.backupName)
		}
		localPath = path.Join(filesystemMountPath(st.Filesystem), rel)
	} else {
		fp := src.metaPath
		if fp == "" {
			fp = reservedFilerMetaPath(src.backupName)
		}
		if fp == "" || fp[0] != '/' {
			fp = "/" + fp
		}
		filerURL = fmt.Sprintf("http://%s%s", getFilerAddress(&cluster), fp)
	}

	jobName := boundedName(restore.Name, "-rst")
	var job batchv1.Job
	err = r.Get(ctx, types.NamespacedName{Namespace: restore.Namespace, Name: jobName}, &job)
	switch {
	case apierrors.IsNotFound(err):
		built := buildRestoreJob(&cluster, jobName, &restore, st, localPath, filerURL)
		if err := controllerutil.SetControllerReference(&restore, built, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, built); err != nil && !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, err
		}
		log.Info("created restore job", "job", jobName, "storage", src.storageName)
		now := metav1.Now()
		restore.Status.Phase = seaweedv1.RestorePhaseRunning
		restore.Status.JobName = jobName
		restore.Status.StartTime = &now
		restore.Status.ObservedGeneration = restore.Generation
		meta.SetStatusCondition(&restore.Status.Conditions, metav1.Condition{
			Type: seaweedv1.RestoreConditionSourceResolved, Status: metav1.ConditionTrue,
			ObservedGeneration: restore.Generation, Reason: "Resolved", Message: "restore job created",
		})
		return ctrl.Result{}, r.Status().Update(ctx, &restore)
	case err != nil:
		return ctrl.Result{}, err
	}

	done, success := jobFinished(&job)
	if !done {
		if restore.Status.Phase != seaweedv1.RestorePhaseRunning {
			restore.Status.Phase = seaweedv1.RestorePhaseRunning
			if err := r.Status().Update(ctx, &restore); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: backupRequeue}, nil
	}

	now := metav1.Now()
	restore.Status.CompletionTime = &now
	if success {
		restore.Status.Phase = seaweedv1.RestorePhaseCompleted
		meta.SetStatusCondition(&restore.Status.Conditions, metav1.Condition{
			Type: seaweedv1.RestoreConditionComplete, Status: metav1.ConditionTrue,
			ObservedGeneration: restore.Generation, Reason: "RestoreComplete", Message: "metadata restored",
		})
		r.Recorder.Event(&restore, "Normal", "RestoreCompleted", "metadata restore completed")
	} else {
		restore.Status.Phase = seaweedv1.RestorePhaseFailed
		meta.SetStatusCondition(&restore.Status.Conditions, metav1.Condition{
			Type: seaweedv1.RestoreConditionComplete, Status: metav1.ConditionFalse,
			ObservedGeneration: restore.Generation, Reason: "RestoreFailed", Message: "restore job failed; see job " + jobName,
		})
		r.Recorder.Event(&restore, "Warning", "RestoreFailed", "restore job failed")
	}
	return ctrl.Result{}, r.Status().Update(ctx, &restore)
}

// restoreSource is the resolved snapshot location, independent of storage type.
// Exactly one of (backupName) or (metaPath) drives the eventual path: backupName
// reconstructs the canonical layout, metaPath is an explicit override.
type restoreSource struct {
	storageName string
	backupName  string // set when restoring a SeaweedBackup
	metaPath    string // set when restoring an explicit BackupSource
	cluster     string
}

// resolveSource resolves the restore's backup source. For a BackupName it reads
// the referenced (Completed) SeaweedBackup; for a BackupSource it uses the
// explicit fields.
func (r *SeaweedRestoreReconciler) resolveSource(ctx context.Context, restore *seaweedv1.SeaweedRestore) (restoreSource, error) {
	if restore.Spec.BackupSource != nil {
		return restoreSource{
			storageName: restore.Spec.BackupSource.StorageName,
			metaPath:    restore.Spec.BackupSource.MetaPath,
			cluster:     restore.Spec.ClusterName,
		}, nil
	}
	var backup seaweedv1.SeaweedBackup
	if err := r.Get(ctx, types.NamespacedName{Namespace: restore.Namespace, Name: restore.Spec.BackupName}, &backup); err != nil {
		if apierrors.IsNotFound(err) {
			return restoreSource{}, fmt.Errorf("backup %q not found in namespace %q", restore.Spec.BackupName, restore.Namespace)
		}
		return restoreSource{}, err
	}
	if backup.Status.Phase != seaweedv1.BackupPhaseCompleted {
		return restoreSource{}, fmt.Errorf("backup %q is not Completed (phase %q)", backup.Name, backup.Status.Phase)
	}
	return restoreSource{
		storageName: backup.Spec.StorageName,
		backupName:  backup.Name,
		cluster:     backup.Spec.ClusterName,
	}, nil
}

// pending records a transient blocker and requeues.
func (r *SeaweedRestoreReconciler) pending(ctx context.Context, restore *seaweedv1.SeaweedRestore, reason, msg string) (ctrl.Result, error) {
	restore.Status.Phase = seaweedv1.RestorePhasePending
	meta.SetStatusCondition(&restore.Status.Conditions, metav1.Condition{
		Type: seaweedv1.RestoreConditionSourceResolved, Status: metav1.ConditionFalse,
		ObservedGeneration: restore.Generation, Reason: reason, Message: msg,
	})
	if err := r.Status().Update(ctx, restore); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: backupRequeue}, nil
}

// SetupWithManager wires the reconciler into the manager.
func (r *SeaweedRestoreReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&seaweedv1.SeaweedRestore{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}
