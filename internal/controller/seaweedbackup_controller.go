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

// BackupFinalizer holds a deleting SeaweedBackup until its archive cleanup
// Job has run, when spec.reclaimPolicy is Delete.
const BackupFinalizer = "seaweed.seaweedfs.com/backup-protection"

// SeaweedBackupReconciler turns a SeaweedBackup into a one-shot `fs.meta.save`
// snapshot Job and tracks the Job's outcome on the CR's status.
type SeaweedBackupReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=seaweedbackups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=seaweedbackups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=seaweedbackups/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete

// Reconcile implements the SeaweedBackup snapshot lifecycle.
func (r *SeaweedBackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("seaweedbackup", req.NamespacedName)

	var backup seaweedv1.SeaweedBackup
	if err := r.Get(ctx, req.NamespacedName, &backup); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !backup.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &backup)
	}

	// The finalizer is only needed to delay deletion for archive cleanup.
	if backup.Spec.ReclaimPolicy == seaweedv1.BackupReclaimDelete &&
		!controllerutil.ContainsFinalizer(&backup, BackupFinalizer) {
		controllerutil.AddFinalizer(&backup, BackupFinalizer)
		if err := r.Update(ctx, &backup); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Terminal: nothing more to do once the status is complete. The owned Job
	// is retained for log access and GC'd with the CR.
	terminal := backup.Status.Phase == seaweedv1.BackupPhaseCompleted || backup.Status.Phase == seaweedv1.BackupPhaseFailed
	complete := backup.Status.JobName != "" &&
		backup.Status.Destination != "" &&
		backup.Status.StartTime != nil &&
		backup.Status.CompletionTime != nil &&
		backup.Status.ObservedGeneration == backup.Generation
	if terminal && complete {
		return ctrl.Result{}, nil
	}

	// Resolve the cluster and the named storage.
	var cluster seaweedv1.Seaweed
	if err := r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: backup.Spec.ClusterName}, &cluster); err != nil {
		if apierrors.IsNotFound(err) {
			return r.pending(ctx, &backup, "ClusterNotFound",
				"Seaweed cluster "+backup.Spec.ClusterName+" not found in namespace "+backup.Namespace)
		}
		return ctrl.Result{}, err
	}
	st, err := resolveStorage(&cluster, backup.Spec.StorageName)
	if err != nil {
		return r.pending(ctx, &backup, "StorageNotFound", err.Error())
	}

	jobName := boundedName(backup.Name, "-bkp")
	var job batchv1.Job
	err = r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: jobName}, &job)
	switch {
	case apierrors.IsNotFound(err):
		if terminal {
			// The Job is gone, so there is nothing left to recover into the
			// status; do not run another snapshot for a finished backup.
			return ctrl.Result{}, nil
		}
		built, dest := buildSnapshotJob(&cluster, jobName, &backup, st)
		if err := controllerutil.SetControllerReference(&backup, built, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, built); err != nil && !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, err
		}
		log.Info("created snapshot job", "job", jobName, "storage", backup.Spec.StorageName)
		markRunning(&backup, st, jobName, dest, metav1.Now())
		return ctrl.Result{}, r.Status().Update(ctx, &backup)
	case err != nil:
		return ctrl.Result{}, err
	}

	// Job exists: restore status lost to a failed create-time update. The
	// Job's own record wins over the live storage config, which may have
	// changed since the Job was created.
	dest := backup.Status.Destination
	if d := job.Annotations[seaweedv1.AnnotationBackupDestination]; d != "" {
		dest = d
	}
	if dest == "" {
		_, dest = snapshotScript(&cluster, st, backup.Spec.ClusterName, backup.Name, backup.Spec.FilerPath)
	}
	startTime := job.CreationTimestamp
	if job.Status.StartTime != nil {
		startTime = *job.Status.StartTime
	}
	changed := markRunning(&backup, st, jobName, dest, startTime)

	done, success := jobFinished(&job)
	if !done {
		if changed {
			if err := r.Status().Update(ctx, &backup); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: backupRequeue}, nil
	}

	backup.Status.CompletionTime = job.Status.CompletionTime
	if backup.Status.CompletionTime == nil {
		now := metav1.Now()
		backup.Status.CompletionTime = &now
	}
	if success {
		backup.Status.Phase = seaweedv1.BackupPhaseCompleted
		meta.SetStatusCondition(&backup.Status.Conditions, metav1.Condition{
			Type: seaweedv1.BackupConditionComplete, Status: metav1.ConditionTrue,
			ObservedGeneration: backup.Generation, Reason: "SnapshotComplete", Message: "metadata snapshot completed",
		})
		r.Recorder.Event(&backup, "Normal", "BackupCompleted", "metadata snapshot completed")
	} else {
		backup.Status.Phase = seaweedv1.BackupPhaseFailed
		meta.SetStatusCondition(&backup.Status.Conditions, metav1.Condition{
			Type: seaweedv1.BackupConditionComplete, Status: metav1.ConditionFalse,
			ObservedGeneration: backup.Generation, Reason: "SnapshotFailed", Message: "snapshot job failed; see job " + jobName,
		})
		r.Recorder.Event(&backup, "Warning", "BackupFailed", "snapshot job failed")
	}
	return ctrl.Result{}, r.Status().Update(ctx, &backup)
}

// handleDeletion drives the reclaim-policy path. Retain just drops the
// finalizer; Delete stops any in-flight snapshot, then runs a cleanup Job
// that removes the archive before the finalizer is released.
func (r *SeaweedBackupReconciler) handleDeletion(ctx context.Context, backup *seaweedv1.SeaweedBackup) (ctrl.Result, error) {
	log := r.Log.WithValues("seaweedbackup", types.NamespacedName{Namespace: backup.Namespace, Name: backup.Name})

	if !controllerutil.ContainsFinalizer(backup, BackupFinalizer) {
		return ctrl.Result{}, nil
	}
	if backup.Spec.ReclaimPolicy != seaweedv1.BackupReclaimDelete {
		return r.releaseFinalizer(ctx, backup)
	}

	// Suspend the snapshot Job before cleaning — including one whose status
	// was never persisted, which is the only evidence the backup ever ran.
	// Suspending makes the Job controller tear down its pods; waiting on
	// Status.Active covers them, so a still-terminating pod cannot land the
	// archive after the cleanup ran. The suspended Job is kept (it carries
	// the recorded storage annotations and is GC'd with the CR).
	var snap batchv1.Job
	snapErr := r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: boundedName(backup.Name, "-bkp")}, &snap)
	snapFound := snapErr == nil
	if !snapFound && !apierrors.IsNotFound(snapErr) {
		return ctrl.Result{}, snapErr
	}
	if snapFound && backup.Status.Phase != seaweedv1.BackupPhaseCompleted && backup.Status.Phase != seaweedv1.BackupPhaseFailed {
		if snap.Spec.Suspend == nil || !*snap.Spec.Suspend {
			suspend := true
			snap.Spec.Suspend = &suspend
			if err := r.Update(ctx, &snap); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: backupRequeue}, nil
		}
		if snap.Status.Active > 0 {
			return ctrl.Result{RequeueAfter: backupRequeue}, nil
		}
	}

	var cluster seaweedv1.Seaweed
	if err := r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: backup.Spec.ClusterName}, &cluster); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		log.Info("cluster gone during delete; releasing finalizer without archive cleanup", "cluster", backup.Spec.ClusterName)
		return r.releaseFinalizer(ctx, backup)
	}
	st, err := resolveStorage(&cluster, backup.Spec.StorageName)
	if err != nil {
		log.Info("storage unresolvable during delete; releasing finalizer without archive cleanup", "storage", backup.Spec.StorageName)
		return r.releaseFinalizer(ctx, backup)
	}
	// Clean the storage the snapshot wrote to, not whatever the entry was
	// reconfigured to afterwards.
	if st.Type == seaweedv1.BackupStorageFilesystem {
		var snapRef *batchv1.Job
		if snapFound {
			snapRef = &snap
		}
		st.Filesystem = recordedFilesystem(backup, snapRef, st)
		if st.Filesystem == nil {
			log.Info("filesystem store unresolvable during delete; releasing finalizer without archive cleanup")
			return r.releaseFinalizer(ctx, backup)
		}
	}

	jobName := cleanupJobName(backup)
	var job batchv1.Job
	err = r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: jobName}, &job)
	switch {
	case apierrors.IsNotFound(err):
		// Intentionally not owner-referenced: a foreground deletion of the
		// CR would delete the Job mid-run and the reconcile would recreate
		// it forever. The Job is deleted explicitly once it finishes.
		built := buildCleanupJob(&cluster, jobName, backup, st)
		if err := r.Create(ctx, built); err != nil && !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, err
		}
		log.Info("created archive cleanup job", "job", jobName)
		return ctrl.Result{RequeueAfter: backupRequeue}, nil
	case err != nil:
		return ctrl.Result{}, err
	}

	// A Job at this UID-keyed name without our UID label was not created by
	// this backup; wait it out rather than adopt its outcome or delete it.
	if job.Labels[seaweedv1.LabelBackupUID] != string(backup.UID) {
		log.Info("cleanup job name held by an unrelated job", "job", jobName)
		return ctrl.Result{RequeueAfter: backupRequeue}, nil
	}

	done, success := jobFinished(&job)
	if !done {
		return ctrl.Result{RequeueAfter: backupRequeue}, nil
	}
	if !success {
		// Replace a failed Job so the next reconcile retries cleanup instead
		// of wedging the CR on a terminal JobFailed condition.
		if err := r.deleteCleanupJob(ctx, backup, &job); err != nil {
			return ctrl.Result{}, err
		}
		r.Recorder.Event(backup, "Warning", "ArchiveCleanupFailed", "archive cleanup job "+jobName+" failed; retrying")
		return ctrl.Result{RequeueAfter: backupRequeue}, nil
	}
	r.Recorder.Event(backup, "Normal", "ArchiveDeleted", "snapshot archive removed")
	return r.releaseFinalizer(ctx, backup)
}

// recordedFilesystem returns the filesystem store a snapshot actually wrote
// to — the controller-owned status record first, the snapshot Job's
// annotations for backups predating the status field, then the live storage
// entry as a last resort. User-editable annotations on the CR are never
// consulted.
func recordedFilesystem(backup *seaweedv1.SeaweedBackup, snap *batchv1.Job, st seaweedv1.BackupStorageSpec) *seaweedv1.FilesystemBackupStore {
	if backup.Status.FilesystemStorage != nil {
		return backup.Status.FilesystemStorage
	}
	if snap != nil {
		if claim := snap.Annotations[seaweedv1.AnnotationBackupClaim]; claim != "" {
			return &seaweedv1.FilesystemBackupStore{
				ExistingClaim: claim,
				MountPath:     snap.Annotations[seaweedv1.AnnotationBackupMountPath],
				SubPath:       snap.Annotations[seaweedv1.AnnotationBackupSubPath],
			}
		}
	}
	return st.Filesystem
}

// cleanupJobName keys the archive-cleanup Job to the backup's UID, so a Job
// left behind by an earlier generation of a same-named CR is never adopted —
// or deleted — as this backup's cleanup.
func cleanupJobName(backup *seaweedv1.SeaweedBackup) string {
	tag := string(backup.UID)
	if len(tag) > 8 {
		tag = tag[:8]
	}
	if tag == "" {
		return boundedName(backup.Name, "-cln")
	}
	return boundedName(backup.Name+"-"+tag, "-cln")
}

// deleteCleanupJob removes a cleanup Job only when it carries this backup's
// UID label; the Job is deliberately unowned, so the check guards against
// tearing down an unrelated Job that reused the generated name.
func (r *SeaweedBackupReconciler) deleteCleanupJob(ctx context.Context, backup *seaweedv1.SeaweedBackup, job *batchv1.Job) error {
	if job.Labels[seaweedv1.LabelBackupUID] != string(backup.UID) {
		return nil
	}
	if err := r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// releaseFinalizer removes any cleanup Job still around (it is deliberately
// unowned, so nothing else would collect it) and drops BackupFinalizer,
// letting the CR finish deleting.
func (r *SeaweedBackupReconciler) releaseFinalizer(ctx context.Context, backup *seaweedv1.SeaweedBackup) (ctrl.Result, error) {
	var job batchv1.Job
	err := r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: cleanupJobName(backup)}, &job)
	switch {
	case err == nil:
		if err := r.deleteCleanupJob(ctx, backup, &job); err != nil {
			return ctrl.Result{}, err
		}
	case !apierrors.IsNotFound(err):
		return ctrl.Result{}, err
	}
	controllerutil.RemoveFinalizer(backup, BackupFinalizer)
	if err := r.Update(ctx, backup); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// markRunning fills the status fields derivable from the spec and the
// snapshot Job, so a status lost to a failed create-time update or a
// controller restart is restored. Returns whether the status changed.
func markRunning(backup *seaweedv1.SeaweedBackup, st seaweedv1.BackupStorageSpec, jobName, destination string, startTime metav1.Time) bool {
	if startTime.IsZero() {
		startTime = metav1.Now()
	}
	status := &backup.Status
	changed := status.Phase != seaweedv1.BackupPhaseRunning ||
		status.JobName != jobName ||
		status.Destination != destination ||
		status.ObservedGeneration != backup.Generation ||
		status.StartTime == nil ||
		!meta.IsStatusConditionTrue(status.Conditions, seaweedv1.BackupConditionClusterReachable)

	status.Phase = seaweedv1.BackupPhaseRunning
	status.JobName = jobName
	status.Destination = destination
	if st.Type == seaweedv1.BackupStorageFilesystem && st.Filesystem != nil {
		status.FilesystemStorage = st.Filesystem.DeepCopy()
	}
	if status.StartTime == nil {
		status.StartTime = &startTime
	}
	status.ObservedGeneration = backup.Generation
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type: seaweedv1.BackupConditionClusterReachable, Status: metav1.ConditionTrue,
		ObservedGeneration: backup.Generation, Reason: "Reachable", Message: "snapshot job created",
	})
	return changed
}

// pending records a transient blocker and requeues. A backup that already
// reached a terminal phase keeps it; only the blocker condition is updated.
func (r *SeaweedBackupReconciler) pending(ctx context.Context, backup *seaweedv1.SeaweedBackup, reason, msg string) (ctrl.Result, error) {
	if backup.Status.Phase != seaweedv1.BackupPhaseCompleted && backup.Status.Phase != seaweedv1.BackupPhaseFailed {
		backup.Status.Phase = seaweedv1.BackupPhasePending
	}
	meta.SetStatusCondition(&backup.Status.Conditions, metav1.Condition{
		Type: seaweedv1.BackupConditionClusterReachable, Status: metav1.ConditionFalse,
		ObservedGeneration: backup.Generation, Reason: reason, Message: msg,
	})
	if err := r.Status().Update(ctx, backup); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: backupRequeue}, nil
}

// SetupWithManager wires the reconciler into the manager.
func (r *SeaweedBackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&seaweedv1.SeaweedBackup{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}
