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

	// Terminal: nothing more to do. The owned Job is retained for log access
	// and GC'd with the CR.
	if backup.Status.Phase == seaweedv1.BackupPhaseCompleted || backup.Status.Phase == seaweedv1.BackupPhaseFailed {
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
		built, dest := buildSnapshotJob(&cluster, jobName, &backup, st)
		if err := controllerutil.SetControllerReference(&backup, built, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, built); err != nil && !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, err
		}
		log.Info("created snapshot job", "job", jobName, "storage", backup.Spec.StorageName)
		now := metav1.Now()
		backup.Status.Phase = seaweedv1.BackupPhaseRunning
		backup.Status.JobName = jobName
		backup.Status.Destination = dest
		backup.Status.StartTime = &now
		backup.Status.ObservedGeneration = backup.Generation
		meta.SetStatusCondition(&backup.Status.Conditions, metav1.Condition{
			Type: seaweedv1.BackupConditionClusterReachable, Status: metav1.ConditionTrue,
			ObservedGeneration: backup.Generation, Reason: "Reachable", Message: "snapshot job created",
		})
		return ctrl.Result{}, r.Status().Update(ctx, &backup)
	case err != nil:
		return ctrl.Result{}, err
	}

	// Job exists: reflect its state.
	done, success := jobFinished(&job)
	if !done {
		if backup.Status.Phase != seaweedv1.BackupPhaseRunning {
			backup.Status.Phase = seaweedv1.BackupPhaseRunning
			if err := r.Status().Update(ctx, &backup); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: backupRequeue}, nil
	}

	now := metav1.Now()
	backup.Status.CompletionTime = &now
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

// pending records a transient blocker and requeues.
func (r *SeaweedBackupReconciler) pending(ctx context.Context, backup *seaweedv1.SeaweedBackup, reason, msg string) (ctrl.Result, error) {
	backup.Status.Phase = seaweedv1.BackupPhasePending
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
