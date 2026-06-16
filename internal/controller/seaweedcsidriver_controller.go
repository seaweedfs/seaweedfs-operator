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
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/internal/controller/label"
)

const (
	seaweedCSIDriverFinalizer = "seaweed.seaweedfs.com/csidriver-protection"

	// Steady-state resync once the driver is Ready. Watches catch real
	// changes; this is the safety-net cadence, matching seaweed_controller.go.
	csiResyncInterval = 1 * time.Minute
)

// SeaweedCSIDriverReconciler deploys and reconciles the SeaweedFS CSI driver:
// the controller Deployment, the per-node and mount DaemonSets, the
// cluster-scoped CSIDriver object and RBAC, and an optional StorageClass.
type SeaweedCSIDriverReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=seaweedcsidrivers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=seaweedcsidrivers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=seaweedcsidrivers/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments;daemonsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=storage.k8s.io,resources=csidrivers;storageclasses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings;roles;rolebindings,verbs=get;list;watch;create;update;patch;delete;bind;escalate
// The driver ServiceAccounts run with the permissions below; the operator must
// hold them (plus bind/escalate above) to create the backing roles without
// privilege escalation.
// +kubebuilder:rbac:groups=core,resources=persistentvolumes;persistentvolumeclaims;nodes;pods;secrets;events,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=storage.k8s.io,resources=volumeattachments;csinodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshots;volumesnapshotcontents,verbs=get;list
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

// Reconcile drives a SeaweedCSIDriver towards its spec.
func (r *SeaweedCSIDriverReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var driver seaweedv1.SeaweedCSIDriver
	if err := r.Get(ctx, req.NamespacedName, &driver); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !driver.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &driver)
	}

	if !controllerutil.ContainsFinalizer(&driver, seaweedCSIDriverFinalizer) {
		controllerutil.AddFinalizer(&driver, seaweedCSIDriverFinalizer)
		if err := r.Update(ctx, &driver); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	driver.Status.DriverName = driver.Spec.DriverName

	// A CSI driver name is node-global: refuse to deploy a second object that
	// claims a name another SeaweedCSIDriver already owns, so two reconcilers
	// don't fight over the kubelet's driver registration.
	if conflict, err := r.driverNameConflict(ctx, &driver); err != nil {
		return ctrl.Result{}, err
	} else if conflict != "" {
		return r.markConflict(ctx, &driver, conflict)
	}
	apimeta.RemoveStatusCondition(&driver.Status.Conditions, seaweedv1.CSIConditionDriverNameConflict)

	// Resolve the filer endpoint the driver mounts, gating cross-namespace
	// SeaweedRefs on a ResourceReferenceGrant (consistent with Bucket/IAM).
	filerAddress, result, done, err := r.resolveFiler(ctx, &driver)
	if err != nil || done {
		return result, err
	}
	driver.Status.ResolvedFilerAddress = filerAddress

	if err := r.ensureAll(ctx, &driver, filerAddress); err != nil {
		return r.fail(ctx, &driver, "ReconcileFailed", err.Error())
	}

	ready, err := r.refreshStatus(ctx, &driver)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Status().Update(ctx, &driver); err != nil {
		return ctrl.Result{}, err
	}

	requeue := requeueAfterTransient
	if ready {
		requeue = csiResyncInterval
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// ensureAll reconciles every managed object. Cluster-scoped children
// (ClusterRole/Binding, CSIDriver, StorageClass) cannot carry an owner
// reference to a namespaced CR, so they are tracked by the instance label and
// removed via the finalizer instead.
func (r *SeaweedCSIDriverReconciler) ensureAll(ctx context.Context, driver *seaweedv1.SeaweedCSIDriver, filerAddress string) error {
	if err := r.ensureServiceAccounts(ctx, driver); err != nil {
		return fmt.Errorf("service accounts: %w", err)
	}
	if err := r.ensureRBAC(ctx, driver); err != nil {
		return fmt.Errorf("rbac: %w", err)
	}
	if err := r.ensureCSIDriverObject(ctx, driver); err != nil {
		return fmt.Errorf("csidriver object: %w", err)
	}
	if err := r.ensureControllerDeployment(ctx, driver, filerAddress); err != nil {
		return fmt.Errorf("controller deployment: %w", err)
	}
	if err := r.ensureNodeDaemonSet(ctx, driver, filerAddress); err != nil {
		return fmt.Errorf("node daemonset: %w", err)
	}
	if err := r.ensureMountDaemonSet(ctx, driver); err != nil {
		return fmt.Errorf("mount daemonset: %w", err)
	}
	if err := r.ensureStorageClass(ctx, driver); err != nil {
		return fmt.Errorf("storageclass: %w", err)
	}
	return nil
}

// resolveFiler returns the filer HTTP address the driver should mount. The
// bool return is true when the caller should stop and return (result, err) —
// used for the not-yet-permitted / cluster-not-found requeue paths.
func (r *SeaweedCSIDriverReconciler) resolveFiler(ctx context.Context, driver *seaweedv1.SeaweedCSIDriver) (string, ctrl.Result, bool, error) {
	if driver.Spec.FilerAddress != "" {
		apimeta.RemoveStatusCondition(&driver.Status.Conditions, seaweedv1.CSIConditionReferenceGranted)
		r.setCondition(driver, seaweedv1.CSIConditionClusterReachable, metav1.ConditionTrue, "ExternalFiler", "")
		return driver.Spec.FilerAddress, ctrl.Result{}, false, nil
	}

	if driver.Spec.SeaweedRef == nil {
		// The CEL "exactly one of seaweedRef/filerAddress" rule prevents this
		// at admission; guard anyway so a hand-edited object can't panic here.
		r.setCondition(driver, seaweedv1.CSIConditionClusterReachable, metav1.ConditionFalse, "NoFilerTarget",
			"exactly one of seaweedRef or filerAddress must be set")
		res, err := r.patchPending(ctx, driver)
		return "", res, true, err
	}
	ref := *driver.Spec.SeaweedRef

	permitted, err := seaweedRefPermitted(ctx, r.Client, ref, kindSeaweedCSIDriver, driver.Namespace)
	if err != nil {
		return "", ctrl.Result{}, true, err
	}
	if !permitted {
		r.setCondition(driver, seaweedv1.CSIConditionReferenceGranted, metav1.ConditionFalse, "ReferenceGrantMissing",
			seaweedRefDeniedMessage(ref, kindSeaweedCSIDriver, driver.Namespace))
		res, err := r.patchPending(ctx, driver)
		return "", res, true, err
	}
	apimeta.RemoveStatusCondition(&driver.Status.Conditions, seaweedv1.CSIConditionReferenceGranted)

	target, found, err := resolveSeaweedFiler(ctx, r.Client, ref, driver.Namespace)
	if err != nil {
		return "", ctrl.Result{}, true, err
	}
	if !found {
		r.setCondition(driver, seaweedv1.CSIConditionClusterReachable, metav1.ConditionFalse, "ClusterRefNotFound",
			fmt.Sprintf("Seaweed %q not found", ref.Name))
		res, err := r.patchPending(ctx, driver)
		return "", res, true, err
	}
	r.setCondition(driver, seaweedv1.CSIConditionClusterReachable, metav1.ConditionTrue, "Reachable", "")
	return target.address, ctrl.Result{}, false, nil
}

// driverNameConflict returns the name of an existing SeaweedCSIDriver that
// already owns this object's driverName, or "" if this object is the rightful
// owner. The oldest object (by creation time, UID as tie-break) wins.
func (r *SeaweedCSIDriverReconciler) driverNameConflict(ctx context.Context, driver *seaweedv1.SeaweedCSIDriver) (string, error) {
	var list seaweedv1.SeaweedCSIDriverList
	if err := r.List(ctx, &list); err != nil {
		return "", err
	}
	for i := range list.Items {
		other := &list.Items[i]
		if other.UID == driver.UID || !other.DeletionTimestamp.IsZero() {
			continue
		}
		if other.Spec.DriverName != driver.Spec.DriverName {
			continue
		}
		if othersTurn(other, driver) {
			return fmt.Sprintf("%s/%s", other.Namespace, other.Name), nil
		}
	}
	return "", nil
}

// othersTurn reports whether other should win ownership of a shared driverName
// over driver: earlier creation wins, UID breaks ties deterministically.
func othersTurn(other, driver *seaweedv1.SeaweedCSIDriver) bool {
	if !other.CreationTimestamp.Equal(&driver.CreationTimestamp) {
		return other.CreationTimestamp.Before(&driver.CreationTimestamp)
	}
	return string(other.UID) < string(driver.UID)
}

// --- ensure helpers ---------------------------------------------------------

func (r *SeaweedCSIDriverReconciler) ensureServiceAccounts(ctx context.Context, driver *seaweedv1.SeaweedCSIDriver) error {
	for _, name := range []string{csiControllerSAName(driver), csiNodeSAName(driver)} {
		sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: driver.Namespace}}
		if err := r.ensureOwned(ctx, driver, sa, func() error {
			sa.Labels = csiInstanceLabels(driver)
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (r *SeaweedCSIDriverReconciler) ensureRBAC(ctx context.Context, driver *seaweedv1.SeaweedCSIDriver) error {
	controllerRole := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: csiControllerName(driver)}}
	if err := r.ensureClusterScoped(ctx, controllerRole, func() error {
		buildControllerClusterRole(driver, controllerRole)
		return nil
	}); err != nil {
		return err
	}
	nodeRole := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: csiNodeName(driver)}}
	if err := r.ensureClusterScoped(ctx, nodeRole, func() error {
		buildNodeClusterRole(driver, nodeRole)
		return nil
	}); err != nil {
		return err
	}

	controllerBinding := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: csiControllerName(driver)}}
	if err := r.ensureClusterScoped(ctx, controllerBinding, func() error {
		buildClusterRoleBinding(controllerBinding, csiInstanceLabels(driver), csiControllerName(driver), csiControllerSAName(driver), driver.Namespace)
		return nil
	}); err != nil {
		return err
	}
	nodeBinding := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: csiNodeName(driver)}}
	if err := r.ensureClusterScoped(ctx, nodeBinding, func() error {
		buildClusterRoleBinding(nodeBinding, csiInstanceLabels(driver), csiNodeName(driver), csiNodeSAName(driver), driver.Namespace)
		return nil
	}); err != nil {
		return err
	}

	// Leader election for the controller sidecars is namespaced, so it can be
	// owned and garbage-collected with the CR.
	leRole := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: csiControllerName(driver), Namespace: driver.Namespace}}
	if err := r.ensureOwned(ctx, driver, leRole, func() error {
		buildLeaderElectionRole(driver, leRole)
		return nil
	}); err != nil {
		return err
	}
	leBinding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: csiControllerName(driver), Namespace: driver.Namespace}}
	return r.ensureOwned(ctx, driver, leBinding, func() error {
		buildLeaderElectionRoleBinding(driver, leBinding, csiControllerName(driver), csiControllerSAName(driver))
		return nil
	})
}

func (r *SeaweedCSIDriverReconciler) ensureCSIDriverObject(ctx context.Context, driver *seaweedv1.SeaweedCSIDriver) error {
	obj := &storagev1.CSIDriver{ObjectMeta: metav1.ObjectMeta{Name: driver.Spec.DriverName}}
	err := r.ensureClusterScoped(ctx, obj, func() error {
		buildCSIDriverObject(driver, obj)
		return nil
	})
	// CSIDriver.Spec.attachRequired is immutable, so flipping attacherEnabled
	// makes the in-place update fail with Invalid. Recreate the object instead
	// of parking the reconcile in Failed; the next pass creates it with the new
	// spec. The kubelet tolerates a brief gap in registration, but leftover
	// VolumeAttachments still need an admin to clean up.
	if apierrors.IsInvalid(err) {
		if delErr := r.deleteIfExists(ctx, obj); delErr != nil {
			return delErr
		}
		r.Recorder.Eventf(driver, corev1.EventTypeWarning, "CSIDriverRecreated",
			"CSIDriver %q has an immutable spec change (attacherEnabled); recreating", driver.Spec.DriverName)
		return nil
	}
	return err
}

func (r *SeaweedCSIDriverReconciler) ensureControllerDeployment(ctx context.Context, driver *seaweedv1.SeaweedCSIDriver, filerAddress string) error {
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: csiControllerName(driver), Namespace: driver.Namespace}}
	return r.ensureOwned(ctx, driver, dep, func() error {
		buildControllerDeployment(driver, dep, filerAddress)
		return nil
	})
}

func (r *SeaweedCSIDriverReconciler) ensureNodeDaemonSet(ctx context.Context, driver *seaweedv1.SeaweedCSIDriver, filerAddress string) error {
	ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: csiNodeName(driver), Namespace: driver.Namespace}}
	return r.ensureOwned(ctx, driver, ds, func() error {
		buildNodeDaemonSet(driver, ds, filerAddress)
		return nil
	})
}

func (r *SeaweedCSIDriverReconciler) ensureMountDaemonSet(ctx context.Context, driver *seaweedv1.SeaweedCSIDriver) error {
	ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: csiMountName(driver), Namespace: driver.Namespace}}
	if !mountServiceEnabled(driver) {
		return r.deleteIfExists(ctx, ds)
	}
	return r.ensureOwned(ctx, driver, ds, func() error {
		buildMountDaemonSet(driver, ds)
		return nil
	})
}

func (r *SeaweedCSIDriverReconciler) ensureStorageClass(ctx context.Context, driver *seaweedv1.SeaweedCSIDriver) error {
	sc := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: storageClassName(driver)}}
	if driver.Spec.StorageClass == nil {
		return r.deleteIfExists(ctx, sc)
	}
	return r.ensureClusterScoped(ctx, sc, func() error {
		buildStorageClass(driver, sc)
		return nil
	})
}

func (r *SeaweedCSIDriverReconciler) ensureOwned(ctx context.Context, driver *seaweedv1.SeaweedCSIDriver, obj client.Object, mutate func() error) error {
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, obj, func() error {
		if err := mutate(); err != nil {
			return err
		}
		return ctrl.SetControllerReference(driver, obj, r.Scheme)
	})
	return err
}

func (r *SeaweedCSIDriverReconciler) ensureClusterScoped(ctx context.Context, obj client.Object, mutate func() error) error {
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, obj, mutate)
	return err
}

// refreshStatus reads back the live controller Deployment and node DaemonSet,
// reflects their readiness into status, and returns whether the driver is
// fully Ready.
func (r *SeaweedCSIDriverReconciler) refreshStatus(ctx context.Context, driver *seaweedv1.SeaweedCSIDriver) (bool, error) {
	var dep appsv1.Deployment
	if err := r.Get(ctx, client.ObjectKey{Namespace: driver.Namespace, Name: csiControllerName(driver)}, &dep); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	var ds appsv1.DaemonSet
	if err := r.Get(ctx, client.ObjectKey{Namespace: driver.Namespace, Name: csiNodeName(driver)}, &ds); err != nil {
		return false, client.IgnoreNotFound(err)
	}

	driver.Status.Controller = seaweedv1.CSIComponentStatus{Desired: dep.Status.Replicas, Ready: dep.Status.ReadyReplicas}
	driver.Status.Node = seaweedv1.CSIComponentStatus{Desired: ds.Status.DesiredNumberScheduled, Ready: ds.Status.NumberReady}

	controllerReady := dep.Status.ReadyReplicas > 0
	// A DaemonSet with no eligible nodes (DesiredNumberScheduled==0) is
	// vacuously "ready" — there is nowhere to run, not a failure.
	nodeReady := ds.Status.NumberReady >= ds.Status.DesiredNumberScheduled

	r.setCondition(driver, seaweedv1.CSIConditionControllerAvailable, boolToStatus(controllerReady), reasonAvailable(controllerReady),
		fmt.Sprintf("%d/%d controller replicas ready", dep.Status.ReadyReplicas, dep.Status.Replicas))
	r.setCondition(driver, seaweedv1.CSIConditionNodeAvailable, boolToStatus(nodeReady), reasonAvailable(nodeReady),
		fmt.Sprintf("%d/%d node pods ready", ds.Status.NumberReady, ds.Status.DesiredNumberScheduled))

	ready := controllerReady && nodeReady
	driver.Status.ObservedGeneration = driver.Generation
	switch {
	case ready:
		driver.Status.Phase = seaweedv1.CSIDriverPhaseReady
		r.setCondition(driver, seaweedv1.CSIConditionReady, metav1.ConditionTrue, "Reconciled", "")
	case controllerReady || driver.Status.Node.Ready > 0:
		driver.Status.Phase = seaweedv1.CSIDriverPhaseDegraded
		r.setCondition(driver, seaweedv1.CSIConditionReady, metav1.ConditionFalse, "PartiallyAvailable", "controller or node plugin not fully ready")
	default:
		driver.Status.Phase = seaweedv1.CSIDriverPhasePending
		r.setCondition(driver, seaweedv1.CSIConditionReady, metav1.ConditionFalse, "RollingOut", "waiting for controller and node plugin")
	}
	return ready, nil
}

// handleDeletion removes the cluster-scoped children the operator could not
// own via owner references, then drops the finalizer. Namespaced children
// (Deployment, DaemonSets, ServiceAccounts, Role, RoleBinding) are
// garbage-collected by their owner references.
func (r *SeaweedCSIDriverReconciler) handleDeletion(ctx context.Context, driver *seaweedv1.SeaweedCSIDriver) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(driver, seaweedCSIDriverFinalizer) {
		return ctrl.Result{}, nil
	}
	driver.Status.Phase = seaweedv1.CSIDriverPhaseTerminating

	instanceLabels := client.MatchingLabels(csiInstanceLabels(driver))
	if err := r.DeleteAllOf(ctx, &rbacv1.ClusterRoleBinding{}, instanceLabels); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.DeleteAllOf(ctx, &rbacv1.ClusterRole{}, instanceLabels); err != nil {
		return ctrl.Result{}, err
	}

	// Only delete the cluster-scoped CSIDriver object if no other
	// SeaweedCSIDriver still claims this driverName.
	if conflict, err := r.driverNameConflict(ctx, driver); err != nil {
		return ctrl.Result{}, err
	} else if conflict == "" {
		if err := r.deleteIfExists(ctx, &storagev1.CSIDriver{ObjectMeta: metav1.ObjectMeta{Name: driver.Spec.DriverName}}); err != nil {
			return ctrl.Result{}, err
		}
	}
	if driver.Spec.StorageClass != nil {
		// Only delete the StorageClass this object actually created. Two
		// drivers can resolve to the same StorageClass name (a shared
		// driverName, or an explicit name collision), so key the delete on the
		// instance label — a conflict loser never created the class and must
		// not yank the owner's.
		var sc storagev1.StorageClass
		switch err := r.Get(ctx, client.ObjectKey{Name: storageClassName(driver)}, &sc); {
		case apierrors.IsNotFound(err):
			// already gone
		case err != nil:
			return ctrl.Result{}, err
		case sc.Labels[label.InstanceLabelKey] == driver.Name:
			if err := r.deleteIfExists(ctx, &sc); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	controllerutil.RemoveFinalizer(driver, seaweedCSIDriverFinalizer)
	if err := r.Update(ctx, driver); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *SeaweedCSIDriverReconciler) deleteIfExists(ctx context.Context, obj client.Object) error {
	return client.IgnoreNotFound(r.Delete(ctx, obj))
}

func (r *SeaweedCSIDriverReconciler) markConflict(ctx context.Context, driver *seaweedv1.SeaweedCSIDriver, owner string) (ctrl.Result, error) {
	r.setCondition(driver, seaweedv1.CSIConditionDriverNameConflict, metav1.ConditionTrue, "DriverNameTaken",
		fmt.Sprintf("driverName %q is already managed by SeaweedCSIDriver %s", driver.Spec.DriverName, owner))
	driver.Status.Phase = seaweedv1.CSIDriverPhaseFailed
	if err := r.Status().Update(ctx, driver); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
}

func (r *SeaweedCSIDriverReconciler) patchPending(ctx context.Context, driver *seaweedv1.SeaweedCSIDriver) (ctrl.Result, error) {
	driver.Status.Phase = seaweedv1.CSIDriverPhasePending
	if err := r.Status().Update(ctx, driver); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
}

func (r *SeaweedCSIDriverReconciler) fail(ctx context.Context, driver *seaweedv1.SeaweedCSIDriver, reason, message string) (ctrl.Result, error) {
	r.Log.Info("seaweedcsidriver reconcile failed", "reason", reason, "message", message)
	driver.Status.Phase = seaweedv1.CSIDriverPhaseFailed
	r.setCondition(driver, seaweedv1.CSIConditionReady, metav1.ConditionFalse, reason, message)
	if err := r.Status().Update(ctx, driver); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
}

func (r *SeaweedCSIDriverReconciler) setCondition(driver *seaweedv1.SeaweedCSIDriver, condType string, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&driver.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		ObservedGeneration: driver.Generation,
		Reason:             reason,
		Message:            message,
	})
}

// SetupWithManager wires the reconciler into the manager. It owns the
// namespaced children directly; the cluster-scoped CSIDriver/StorageClass and
// ClusterRole/Binding are reconciled imperatively and cleaned up via the
// finalizer.
func (r *SeaweedCSIDriverReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&seaweedv1.SeaweedCSIDriver{}).
		Owns(&appsv1.Deployment{}).
		Owns(&appsv1.DaemonSet{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Complete(r)
}

func boolToStatus(b bool) metav1.ConditionStatus {
	if b {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

func reasonAvailable(b bool) string {
	if b {
		return "Available"
	}
	return "Unavailable"
}
