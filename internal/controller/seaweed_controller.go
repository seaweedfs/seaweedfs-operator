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
	"strings"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
)

// ErrStatefulSetDeleted is a sentinel error returned when a StatefulSet is
// deleted to apply immutable VolumeClaimTemplates changes. Callers should
// handle this by requeueing immediately rather than treating it as an error.
var ErrStatefulSetDeleted = fmt.Errorf("StatefulSet deleted for VolumeClaimTemplates update")

const (
	ComponentMaster = "master"
	ComponentVolume = "volume"
	ComponentFiler  = "filer"
	ComponentAdmin  = "admin"
	ComponentWorker = "worker"
)

// Reconcile cadences. The reconciler pulls itself back periodically via
// RequeueAfter rather than relying solely on watches, so transient
// status changes the apiserver-side reflector might miss still get
// caught. The interval depends on whether the cluster has converged:
//
//   - requeueWhileReconciling: status hasn't reached Ready yet — pods
//     are still rolling out, components are coming up. Tight cadence
//     so the user sees Replicas/ReadyReplicas catch up promptly.
//   - requeueWhenReady: every owned component reports its desired
//     replica count is fully ready. The reconciler is just refreshing
//     status as a safety net; a slower cadence dramatically reduces
//     the per-CR baseline load on the apiserver and the operator's
//     log volume in steady state.
//
// At the previous unconditional 5s, a single Seaweed CR generated
// ~17,280 reconcile passes / day even when nothing changed. With 1m
// in steady state, ~1,440 / day — same convergence properties for
// transitions, much less noise at rest.
const (
	requeueWhileReconciling = 5 * time.Second
	requeueWhenReady        = 1 * time.Minute
)

// reconcileRequeueAfter returns the interval Reconcile should request
// based on whether status reports the cluster is Ready. Extracted so
// the cadence policy is unit-testable without driving a full
// reconcile pass.
func reconcileRequeueAfter(isReady bool) time.Duration {
	if isReady {
		return requeueWhenReady
	}
	return requeueWhileReconciling
}

// SeaweedReconciler reconciles a Seaweed object
type SeaweedReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=seaweeds,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=seaweeds/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=extensions,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=servicemonitors,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cert-manager.io,resources=issuers;certificates,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch

// Reconcile implements the reconciliation logic
func (r *SeaweedReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("seaweed", req.NamespacedName)

	log.Info("start Reconcile ...")

	seaweedCR, done, result, err := r.findSeaweedCustomResourceInstance(ctx, log, req)
	if done {
		return result, err
	}

	// TLS must be reconciled first: component pod specs reference the
	// server Secret and security ConfigMap names, and skipping these when
	// cert-manager is absent is the difference between reconciling cleanly
	// and crashlooping pods whose VolumeMounts can never be satisfied.
	if done, result, err = r.ensureTLS(ctx, seaweedCR); done {
		return result, err
	}

	if done, result, err = r.ensureMaster(seaweedCR); done {
		return result, err
	}

	if done, result, err = r.ensureVolumeServers(ctx, seaweedCR); done {
		return result, err
	}

	if seaweedCR.Spec.Filer != nil {
		if done, result, err = r.ensureFilerServers(ctx, seaweedCR); done {
			return result, err
		}
	}

	// Note: Standalone IAM has been removed. IAM is now embedded in S3 by default.
	// Use filer.s3.enabled=true to enable S3 with embedded IAM.

	if seaweedCR.Spec.Admin != nil {
		if done, result, err = r.ensureAdminServers(seaweedCR); done {
			return result, err
		}
	}

	if seaweedCR.Spec.Worker != nil {
		if seaweedCR.Spec.Admin == nil {
			log.Info("Worker requires admin server to be configured, skipping worker reconciliation")
		} else {
			if done, result, err = r.ensureWorkers(ctx, seaweedCR); done {
				return result, err
			}
		}
	}

	if done, result, err = r.ensureS3Gateway(ctx, seaweedCR); done {
		return result, err
	}

	if done, result, err = r.ensureSFTPGateway(ctx, seaweedCR); done {
		return result, err
	}

	if done, result, err = r.ensureSeaweedIngress(seaweedCR); done {
		return result, err
	}

	// Per-component Ingress (opt-in via ComponentSpec.Ingress). Runs
	// after the legacy HostSuffix Ingress so that clusters using both
	// still see their all-in-one Ingress updated first. Takes ctx
	// because the prune step lists existing Ingresses.
	if done, result, err = r.ensureComponentIngresses(ctx, seaweedCR); done {
		return result, err
	}

	if false {
		if done, result, err = r.maintenance(seaweedCR); done {
			return result, err
		}
	}

	// Update status. The returned readiness flag chooses the requeue
	// cadence: tight while the cluster is still rolling out, slower
	// once everything reports Ready (the periodic loop is then just
	// a safety net for events the watch might have missed).
	isReady, err := r.updateStatus(ctx, seaweedCR)
	if err != nil {
		log.Error(err, "Failed to update Seaweed status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: reconcileRequeueAfter(isReady)}, nil
}

func (r *SeaweedReconciler) findSeaweedCustomResourceInstance(ctx context.Context, log logr.Logger, req ctrl.Request) (*seaweedv1.Seaweed, bool, ctrl.Result, error) {
	// fetch the master instance
	seaweedCR := &seaweedv1.Seaweed{}
	err := r.Get(ctx, req.NamespacedName, seaweedCR)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			log.Info("Seaweed CR not found. Ignoring since object must be deleted")
			return nil, true, ctrl.Result{RequeueAfter: time.Second * 5}, nil
		}
		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get SeaweedCR")
		return nil, true, ctrl.Result{}, err
	}
	log.Info("Get master " + seaweedCR.Name)
	return seaweedCR, false, ctrl.Result{}, nil
}

func (r *SeaweedReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&seaweedv1.Seaweed{}).
		Complete(r)
}

// updateStatus refreshes the Seaweed CR's status from the live state of
// every owned component, then writes the result. It also returns the
// readiness signal so Reconcile can decide how aggressively to requeue
// itself: 5s while components are still rolling out (status converges
// quickly), 1m once Ready=True (watches catch real changes; the periodic
// loop is just a belt-and-suspenders safety net).
func (r *SeaweedReconciler) updateStatus(ctx context.Context, seaweedCR *seaweedv1.Seaweed) (isReady bool, err error) {
	log := r.Log.WithValues("seaweed", seaweedCR.Name)

	// Get master statefulset status
	masterStatus, err := r.getComponentStatus(ctx, seaweedCR, ComponentMaster)
	if err != nil {
		log.Error(err, "Failed to get master status")
		return false, err
	}

	// Get volume statefulset status
	volumeStatus, err := r.getComponentStatus(ctx, seaweedCR, ComponentVolume)
	if err != nil {
		log.Error(err, "Failed to get volume status")
		return false, err
	}

	// Get filer statefulset status (if enabled)
	var filerStatus seaweedv1.ComponentStatus
	if seaweedCR.Spec.Filer != nil {
		filerStatus, err = r.getComponentStatus(ctx, seaweedCR, ComponentFiler)
		if err != nil {
			log.Error(err, "Failed to get filer status")
			return false, err
		}
	}

	// Get admin statefulset status (if enabled)
	var adminStatus seaweedv1.ComponentStatus
	if seaweedCR.Spec.Admin != nil {
		adminStatus, err = r.getComponentStatus(ctx, seaweedCR, ComponentAdmin)
		if err != nil {
			log.Error(err, "Failed to get admin status")
			return false, err
		}
	}

	// Get worker statefulset status (if enabled)
	var workerStatus seaweedv1.ComponentStatus
	if seaweedCR.Spec.Worker != nil && seaweedCR.Spec.Admin != nil {
		workerStatus, err = r.getComponentStatus(ctx, seaweedCR, ComponentWorker)
		if err != nil {
			log.Error(err, "Failed to get worker status")
			return false, err
		}
	}

	// Get standalone S3 gateway status (if enabled)
	s3Status, err := r.getS3Status(ctx, seaweedCR)
	if err != nil {
		log.Error(err, "Failed to get S3 gateway status")
		return false, err
	}

	// Get standalone SFTP gateway status (if enabled)
	sftpStatus, err := r.getSFTPStatus(ctx, seaweedCR)
	if err != nil {
		log.Error(err, "Failed to get SFTP gateway status")
		return false, err
	}

	// Determine if cluster is ready
	// Master must have replicas and all must be ready
	isReady = masterStatus.Replicas > 0 && masterStatus.ReadyReplicas == masterStatus.Replicas
	// Volume is ready if disabled (0 replicas) or all configured replicas are ready
	isReady = isReady && (volumeStatus.Replicas == 0 || volumeStatus.ReadyReplicas == volumeStatus.Replicas)

	// Filer is checked only if enabled
	if seaweedCR.Spec.Filer != nil {
		isReady = isReady && (filerStatus.Replicas == 0 || filerStatus.ReadyReplicas == filerStatus.Replicas)
	}

	// Admin is checked only if enabled
	if seaweedCR.Spec.Admin != nil {
		isReady = isReady && (adminStatus.Replicas == 0 || adminStatus.ReadyReplicas == adminStatus.Replicas)
	}

	// Worker is checked only if enabled (requires admin)
	if seaweedCR.Spec.Worker != nil && seaweedCR.Spec.Admin != nil {
		isReady = isReady && (workerStatus.Replicas == 0 || workerStatus.ReadyReplicas == workerStatus.Replicas)
	}

	// Standalone S3 gateway is checked only if enabled.
	if seaweedCR.Spec.S3 != nil {
		isReady = isReady && (s3Status.Replicas == 0 || s3Status.ReadyReplicas == s3Status.Replicas)
	}

	// Standalone SFTP gateway is checked only if enabled.
	if seaweedCR.Spec.SFTP != nil {
		isReady = isReady && (sftpStatus.Replicas == 0 || sftpStatus.ReadyReplicas == sftpStatus.Replicas)
	}

	// Update status
	seaweedCR.Status.ObservedGeneration = seaweedCR.Generation
	seaweedCR.Status.Master = masterStatus
	seaweedCR.Status.Volume = volumeStatus
	if seaweedCR.Spec.Filer != nil {
		seaweedCR.Status.Filer = filerStatus
	} else {
		seaweedCR.Status.Filer = seaweedv1.ComponentStatus{}
	}
	if seaweedCR.Spec.Admin != nil {
		seaweedCR.Status.Admin = adminStatus
	} else {
		seaweedCR.Status.Admin = seaweedv1.ComponentStatus{}
	}
	if seaweedCR.Spec.Worker != nil && seaweedCR.Spec.Admin != nil {
		seaweedCR.Status.Worker = workerStatus
	} else {
		seaweedCR.Status.Worker = seaweedv1.ComponentStatus{}
	}
	// Always write the live s3Status: when Spec.S3 is nil but a
	// Deployment still exists (tear-down in progress) we want the CR to
	// show the real replica counts rather than zero. getS3Status
	// returns the empty ComponentStatus{} once the Deployment is gone,
	// which matches the steady-state "no gateway" view.
	seaweedCR.Status.S3 = s3Status
	// Same rationale as S3 above.
	seaweedCR.Status.SFTP = sftpStatus

	// Build informative status message (for NotReady condition)
	notReadyMessage := strings.Join([]string{
		fmt.Sprintf("Master: %d/%d ready", masterStatus.ReadyReplicas, masterStatus.Replicas),
		fmt.Sprintf("Volume: %d/%d ready", volumeStatus.ReadyReplicas, volumeStatus.Replicas),
	}, ", ")
	if seaweedCR.Spec.Filer != nil {
		notReadyMessage += fmt.Sprintf(", Filer: %d/%d ready", filerStatus.ReadyReplicas, filerStatus.Replicas)
	}
	if seaweedCR.Spec.Admin != nil {
		notReadyMessage += fmt.Sprintf(", Admin: %d/%d ready", adminStatus.ReadyReplicas, adminStatus.Replicas)
	}
	if seaweedCR.Spec.Worker != nil && seaweedCR.Spec.Admin != nil {
		notReadyMessage += fmt.Sprintf(", Worker: %d/%d ready", workerStatus.ReadyReplicas, workerStatus.Replicas)
	}

	// Update conditions
	readyCondition := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		ObservedGeneration: seaweedCR.Generation,
		Reason:             "NotReady",
		Message:            notReadyMessage,
	}

	if isReady {
		readyCondition.Status = metav1.ConditionTrue
		readyCondition.Reason = "Ready"
		readyCondition.Message = "Seaweed cluster is ready"
	}

	// Use idiomatic Kubernetes helper to manage conditions
	meta.SetStatusCondition(&seaweedCR.Status.Conditions, readyCondition)

	// Update the status, handling conflicts gracefully
	if err := r.Status().Update(ctx, seaweedCR); err != nil {
		// Handle conflicts gracefully: they often occur due to concurrent status updates.
		if errors.IsConflict(err) {
			log.V(2).Info("Conflict while updating Seaweed status; will retry on next reconciliation")
			// Do not treat conflict as a hard error to avoid unnecessary requeues.
			// Force the fast cadence: the persisted status was NOT updated
			// (the conflict aborted our write), so as far as any observer
			// is concerned the CR may still report stale NotReady values.
			// Returning isReady=true here would defer the retry by up to
			// a minute. Return false so the next reconcile fires within
			// requeueWhileReconciling and the status catches up promptly.
			return false, nil
		}
		return false, err
	}

	log.Info("Updated Seaweed status", "ready", isReady)
	return isReady, nil
}

func (r *SeaweedReconciler) getComponentStatus(ctx context.Context, seaweedCR *seaweedv1.Seaweed, component string) (seaweedv1.ComponentStatus, error) {
	switch component {
	case ComponentMaster:
		if seaweedCR.Spec.Master != nil {
			return r.getStatefulSetStatus(ctx, seaweedCR.Namespace, seaweedCR.Name+"-master", seaweedCR.Spec.Master.Replicas)
		}
	case ComponentVolume:
		return r.getVolumeStatus(ctx, seaweedCR)
	case ComponentFiler:
		if seaweedCR.Spec.Filer != nil {
			return r.getStatefulSetStatus(ctx, seaweedCR.Namespace, seaweedCR.Name+"-filer", seaweedCR.Spec.Filer.Replicas)
		}
	case ComponentAdmin:
		if seaweedCR.Spec.Admin != nil {
			return r.getStatefulSetStatus(ctx, seaweedCR.Namespace, seaweedCR.Name+"-admin", 1)
		}
	case ComponentWorker:
		if seaweedCR.Spec.Worker != nil {
			return r.getDeploymentStatus(ctx, seaweedCR.Namespace, seaweedCR.Name+"-worker", seaweedCR.Spec.Worker.Replicas)
		}
	}
	return seaweedv1.ComponentStatus{}, nil
}

func (r *SeaweedReconciler) getStatefulSetStatus(ctx context.Context, namespace, name string, desiredReplicas int32) (seaweedv1.ComponentStatus, error) {
	status := seaweedv1.ComponentStatus{
		Replicas: desiredReplicas,
	}

	// Get the StatefulSet
	statefulSet := &appsv1.StatefulSet{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, statefulSet); err != nil {
		if errors.IsNotFound(err) {
			// StatefulSet not yet created
			return status, nil
		}
		return status, err
	}

	// Use StatefulSet's ready replica count
	status.ReadyReplicas = statefulSet.Status.ReadyReplicas
	return status, nil
}

func (r *SeaweedReconciler) getDeploymentStatus(ctx context.Context, namespace, name string, desiredReplicas int32) (seaweedv1.ComponentStatus, error) {
	status := seaweedv1.ComponentStatus{
		Replicas: desiredReplicas,
	}

	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, deployment); err != nil {
		if errors.IsNotFound(err) {
			return status, nil
		}
		return status, err
	}

	status.ReadyReplicas = deployment.Status.ReadyReplicas
	return status, nil
}

func (r *SeaweedReconciler) getVolumeStatus(ctx context.Context, seaweedCR *seaweedv1.Seaweed) (seaweedv1.ComponentStatus, error) {
	status := seaweedv1.ComponentStatus{}

	// Aggregate volume status from base spec and topology groups
	totalDesiredReplicas := int32(0)
	totalReadyReplicas := int32(0)

	// Check base volume spec
	if seaweedCR.Spec.Volume != nil {
		baseStatus, err := r.getStatefulSetStatus(ctx, seaweedCR.Namespace, seaweedCR.Name+"-volume", seaweedCR.Spec.Volume.Replicas)
		if err != nil {
			return status, err
		}
		totalDesiredReplicas += baseStatus.Replicas
		totalReadyReplicas += baseStatus.ReadyReplicas
	}

	// Check volume topology groups
	for topologyName, topologySpec := range seaweedCR.Spec.VolumeTopology {
		if topologySpec == nil {
			continue
		}
		statefulSetName := fmt.Sprintf("%s-volume-%s", seaweedCR.Name, topologyName)
		topologyStatus, err := r.getStatefulSetStatus(ctx, seaweedCR.Namespace, statefulSetName, topologySpec.Replicas)
		if err != nil {
			return status, err
		}
		totalDesiredReplicas += topologyStatus.Replicas
		totalReadyReplicas += topologyStatus.ReadyReplicas
	}

	status.Replicas = totalDesiredReplicas
	status.ReadyReplicas = totalReadyReplicas
	return status, nil
}

func (r *SeaweedReconciler) reconcileVolumeClaimTemplates(ctx context.Context, seaweedCR *seaweedv1.Seaweed, existing, desired *appsv1.StatefulSet) error {
	if vctSemanticallyEqual(existing.Spec.VolumeClaimTemplates, desired.Spec.VolumeClaimTemplates) {
		return nil
	}

	// Only auto-delete for the empty→non-empty transition (adding persistence).
	// Removal or in-place mutation of VolumeClaimTemplates could destroy existing
	// PVCs, so we refuse those and ask the user to handle it manually.
	if len(existing.Spec.VolumeClaimTemplates) == 0 && len(desired.Spec.VolumeClaimTemplates) > 0 {
		r.Log.Info("VolumeClaimTemplates added, deleting StatefulSet for recreation",
			"statefulset", existing.Name,
			"namespace", existing.Namespace)

		if r.Recorder != nil {
			r.Recorder.Eventf(seaweedCR, corev1.EventTypeNormal, "VolumeClaimTemplatesChanged",
				"Deleting StatefulSet %s to add VolumeClaimTemplates", existing.Name)
		}

		if err := r.Delete(ctx, existing); err != nil {
			return fmt.Errorf("failed to delete StatefulSet %s for VolumeClaimTemplates update: %w", existing.Name, err)
		}

		return ErrStatefulSetDeleted
	}

	// Warn but don't fail reconciliation — this requires manual intervention.
	r.Log.Info("VolumeClaimTemplates differ but cannot be auto-applied. Delete the StatefulSet manually to apply changes.",
		"statefulset", existing.Name,
		"namespace", existing.Namespace)

	if r.Recorder != nil {
		r.Recorder.Eventf(seaweedCR, corev1.EventTypeWarning, "VolumeClaimTemplatesMismatch",
			"VolumeClaimTemplates on %s differ but cannot be auto-applied. Delete the StatefulSet manually to apply changes.", existing.Name)
	}

	return nil
}
