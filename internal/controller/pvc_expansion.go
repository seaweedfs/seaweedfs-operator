package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// handleVolumeExpansion applies VolumeClaimTemplates drift that is limited to
// a storage size increase by patching the live PVCs in place — the template
// itself is immutable on the StatefulSet and is never modified. It reports
// whether every drifted template was a storage-only change; any other drift is
// left for the caller's warning path.
func (r *SeaweedReconciler) handleVolumeExpansion(ctx context.Context, seaweedCR *seaweedv1.Seaweed, existing, desired *appsv1.StatefulSet) (bool, error) {
	if len(existing.Spec.VolumeClaimTemplates) != len(desired.Spec.VolumeClaimTemplates) {
		return false, nil
	}

	handled := true
	for i := range desired.Spec.VolumeClaimTemplates {
		desiredClaim := desired.Spec.VolumeClaimTemplates[i]
		var existingClaim *corev1.PersistentVolumeClaim
		for j := range existing.Spec.VolumeClaimTemplates {
			if existing.Spec.VolumeClaimTemplates[j].Name == desiredClaim.Name {
				existingClaim = &existing.Spec.VolumeClaimTemplates[j]
				break
			}
		}
		if existingClaim == nil || !pvcEqualExceptStorageRequests(*existingClaim, desiredClaim) {
			handled = false
			continue
		}
		if err := r.expandClaimPVCs(ctx, seaweedCR, existing, *existingClaim, desiredClaim); err != nil {
			return handled, err
		}
	}
	return handled, nil
}

// pvcEqualExceptStorageRequests compares two claim templates on
// pvcSemanticallyEqual's surface with the resources.requests diff masked out.
func pvcEqualExceptStorageRequests(a, b corev1.PersistentVolumeClaim) bool {
	a.Spec.Resources.Requests = b.Spec.Resources.Requests
	return pvcSemanticallyEqual(a, b)
}

// expandClaimPVCs grows each live PVC spawned from a claim template whose
// desired storage request increased. PVCs already at or above the target —
// including ones whose earlier patch the CSI driver is still fulfilling — are
// skipped, so a reconcile during an in-flight resize is a no-op.
func (r *SeaweedReconciler) expandClaimPVCs(ctx context.Context, seaweedCR *seaweedv1.Seaweed, statefulSet *appsv1.StatefulSet, existingClaim, desiredClaim corev1.PersistentVolumeClaim) error {
	target := desiredClaim.Spec.Resources.Requests.Storage()
	current := existingClaim.Spec.Resources.Requests.Storage()
	if target == nil || (current != nil && target.Cmp(*current) <= 0) {
		return nil
	}

	// StatefulSet PVCs are named <claim>-<statefulset>-<ordinal>. Listing by
	// prefix also reaches claims retained beyond the current replica count by
	// persistentVolumeClaimRetentionPolicy.
	pvcList := &corev1.PersistentVolumeClaimList{}
	if err := r.List(ctx, pvcList, client.InNamespace(statefulSet.Namespace)); err != nil {
		return err
	}
	prefix := desiredClaim.Name + "-" + statefulSet.Name + "-"

	warned := false
	for i := range pvcList.Items {
		pvc := &pvcList.Items[i]
		if !strings.HasPrefix(pvc.Name, prefix) {
			continue
		}
		if _, err := strconv.Atoi(strings.TrimPrefix(pvc.Name, prefix)); err != nil {
			continue
		}

		if actual := pvc.Spec.Resources.Requests.Storage(); actual != nil && actual.Cmp(*target) >= 0 {
			continue
		}
		if pvcResizeInProgress(pvc) {
			continue
		}

		allowed, storageClassName, err := r.claimAllowsExpansion(ctx, pvc, desiredClaim.Spec.StorageClassName)
		if err != nil {
			return err
		}
		if !allowed {
			if !warned {
				warned = true
				r.Log.Info("storage class does not allow volume expansion",
					"statefulset", statefulSet.Name, "pvc", pvc.Name, "storageclass", storageClassName)
				if r.Recorder != nil {
					r.Recorder.Eventf(seaweedCR, corev1.EventTypeWarning, "VolumeExpansionNotSupported",
						"StorageClass %s does not allow volume expansion: PVC %s of StatefulSet %s stays at its current size instead of requested %s. Recreate the StatefulSet manually to apply.", storageClassName, pvc.Name, statefulSet.Name, target.String())
				}
			}
			continue
		}

		previous := pvc.Spec.Resources.Requests.Storage()
		if pvc.Spec.Resources.Requests == nil {
			pvc.Spec.Resources.Requests = corev1.ResourceList{}
		}
		pvc.Spec.Resources.Requests[corev1.ResourceStorage] = *target
		if err := r.Update(ctx, pvc); err != nil {
			return fmt.Errorf("failed to expand PVC %s to %s: %w", pvc.Name, target.String(), err)
		}
		if r.Recorder != nil {
			r.Recorder.Eventf(seaweedCR, corev1.EventTypeNormal, "VolumeClaimExpanded",
				"Expanded PVC %s of StatefulSet %s from %s to %s", pvc.Name, statefulSet.Name, storageQuantityString(previous), target.String())
		}
	}
	return nil
}

// pvcResizeInProgress reports whether controller-side expansion is still
// running. FileSystemResizePending is deliberately not blocking: it lingers
// until a pod restart we don't perform, so suppressing on it would wedge any
// later, larger request.
func pvcResizeInProgress(pvc *corev1.PersistentVolumeClaim) bool {
	for _, condition := range pvc.Status.Conditions {
		if condition.Type == corev1.PersistentVolumeClaimResizing && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// claimAllowsExpansion resolves the claim's effective StorageClass — the
// PVC's own (apiserver-defaulted) name, then the template's, then the cluster
// default — and reports whether it sets allowVolumeExpansion. A missing or
// explicitly empty class is not expandable.
func (r *SeaweedReconciler) claimAllowsExpansion(ctx context.Context, pvc *corev1.PersistentVolumeClaim, templateStorageClassName *string) (bool, string, error) {
	name := pvc.Spec.StorageClassName
	if name == nil {
		name = templateStorageClassName
	}
	if name != nil && *name == "" {
		return false, "<none>", nil
	}

	var sc *storagev1.StorageClass
	if name != nil {
		sc = &storagev1.StorageClass{}
		err := r.Get(ctx, types.NamespacedName{Name: *name}, sc)
		if errors.IsNotFound(err) {
			return false, *name, nil
		}
		if err != nil {
			return false, *name, err
		}
	} else {
		scList := &storagev1.StorageClassList{}
		if err := r.List(ctx, scList); err != nil {
			return false, "<default>", err
		}
		for i := range scList.Items {
			candidate := &scList.Items[i]
			annotations := candidate.Annotations
			if annotations["storageclass.kubernetes.io/is-default-class"] != "true" &&
				annotations["storageclass.beta.kubernetes.io/is-default-class"] != "true" {
				continue
			}
			// Kubernetes resolves multiple defaults to the newest one.
			if sc == nil || candidate.CreationTimestamp.After(sc.CreationTimestamp.Time) {
				sc = candidate
			}
		}
		if sc == nil {
			return false, "<default>", nil
		}
	}

	allowed := sc.AllowVolumeExpansion != nil && *sc.AllowVolumeExpansion
	return allowed, sc.Name, nil
}

func storageQuantityString(q *resource.Quantity) string {
	if q == nil {
		return "<unset>"
	}
	return q.String()
}
