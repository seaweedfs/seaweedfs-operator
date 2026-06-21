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
	"errors"
	"fmt"
	"reflect"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// BucketLifecyclePolicyFinalizer keeps the CR around long enough for the
// reconciler to honor reclaimPolicy before the rules are forgotten.
const BucketLifecyclePolicyFinalizer = "seaweed.seaweedfs.com/bucketlifecyclepolicy-protection"

// BucketLifecyclePolicyReconciler reconciles a bucket's S3 lifecycle
// configuration from the rules declared on a BucketLifecyclePolicy. The cluster
// and bucket name are resolved from the referenced Bucket in the same namespace.
type BucketLifecyclePolicyReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// AdminFactory creates a BucketAdmin for the target Seaweed cluster.
	// Tests inject a fake; production wires NewSwadminBucketAdmin.
	AdminFactory BucketAdminFactory
}

// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=bucketlifecyclepolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=bucketlifecyclepolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=bucketlifecyclepolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=buckets,verbs=get;list;watch

// Reconcile implements the lifecycle policy reconciliation logic.
func (r *BucketLifecyclePolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	log := r.Log.WithValues("bucketlifecyclepolicy", req.NamespacedName)

	var policy seaweedv1.BucketLifecyclePolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Deletion is driven entirely off recorded status so cleanup works even
	// when the referenced Bucket is already gone.
	if !policy.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &policy, log)
	}

	// Add the finalizer before doing any work, so a policy that later applies
	// successfully is always cleaned up on deletion.
	if !controllerutil.ContainsFinalizer(&policy, BucketLifecyclePolicyFinalizer) {
		controllerutil.AddFinalizer(&policy, BucketLifecyclePolicyFinalizer)
		if err := r.Update(ctx, &policy); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Persist status once, and only when it actually changed, so a no-op
	// reconcile doesn't emit an update event (which would re-trigger this
	// controller and churn admin reads/writes).
	base := policy.Status.DeepCopy()
	defer func() {
		if err != nil || reflect.DeepEqual(*base, policy.Status) {
			return
		}
		if uerr := r.Status().Update(ctx, &policy); uerr != nil {
			result, err = ctrl.Result{}, uerr
		}
	}()

	// Resolve the referenced bucket (same namespace).
	var bucket seaweedv1.Bucket
	bucketKey := types.NamespacedName{Namespace: policy.Namespace, Name: policy.Spec.BucketRef.Name}
	if err := r.Get(ctx, bucketKey, &bucket); err != nil {
		if apierrors.IsNotFound(err) {
			return r.pending(&policy, "BucketNotFound",
				fmt.Sprintf("Bucket %q not found in namespace %q", policy.Spec.BucketRef.Name, policy.Namespace)), nil
		}
		return ctrl.Result{}, err
	}

	bucketName := bucket.Status.BucketName
	if bucketName == "" {
		return r.pending(&policy, "BucketNotReady",
			fmt.Sprintf("Bucket %q is not provisioned yet", policy.Spec.BucketRef.Name)), nil
	}
	r.setCondition(&policy, seaweedv1.BucketLifecyclePolicyConditionBucketResolved, metav1.ConditionTrue, "Resolved", "")

	// A bucket's lifecycle is a single document, so only one policy may own it.
	// Pick a deterministic owner; everyone else marks a conflict and stands down
	// instead of fighting last-writer-wins.
	owner, err := r.bucketOwner(ctx, &policy)
	if err != nil {
		return ctrl.Result{}, err
	}
	if owner != policy.Name {
		return r.conflict(&policy, owner), nil
	}

	seaweedNS := bucket.Spec.ClusterRef.Namespace
	if seaweedNS == "" {
		seaweedNS = bucket.Namespace
	}
	var seaweed seaweedv1.Seaweed
	if err := r.Get(ctx, types.NamespacedName{Namespace: seaweedNS, Name: bucket.Spec.ClusterRef.Name}, &seaweed); err != nil {
		if apierrors.IsNotFound(err) {
			return r.pending(&policy, "ClusterRefNotFound",
				fmt.Sprintf("Seaweed %q not found in namespace %q", bucket.Spec.ClusterRef.Name, seaweedNS)), nil
		}
		return ctrl.Result{}, err
	}

	admin, err := r.adminFor(ctx, &seaweed, log)
	if err != nil {
		return ctrl.Result{}, err
	}
	defer closeBucketAdmin(admin, log)

	return r.reconcilePolicy(ctx, &policy, seaweedNS, seaweed.Name, bucketName, admin)
}

// reconcilePolicy applies the desired lifecycle configuration to the bucket,
// skipping the write when the bucket already matches, and records the resolved
// target on status so deletion can clean up independently of the Bucket CR.
func (r *BucketLifecyclePolicyReconciler) reconcilePolicy(ctx context.Context, policy *seaweedv1.BucketLifecyclePolicy, clusterNS, clusterName, bucketName string, admin BucketAdmin) (ctrl.Result, error) {
	log := r.Log.WithValues("bucketlifecyclepolicy", types.NamespacedName{Namespace: policy.Namespace, Name: policy.Name})

	desired, err := buildLifecycleXML(policy.Spec.Rules)
	if err != nil {
		return r.failPhase(policy, "BuildFailed", err.Error()), nil
	}
	// Run independently of the XML diff below so a bucket adopted with already
	// matching rules still has its legacy day-TTL entries cleared.
	if err := admin.ClearLegacyBucketTTLs(ctx, bucketName); err != nil {
		return r.failPhase(policy, "TTLCleanupFailed", err.Error()), nil
	}
	current, err := admin.GetBucketLifecycle(ctx, bucketName)
	if err != nil {
		return r.failPhase(policy, "ReadFailed", err.Error()), nil
	}
	if !lifecycleConfigEqual(current, desired) {
		if err := admin.SetBucketLifecycle(ctx, bucketName, desired); err != nil {
			return r.failPhase(policy, "ApplyFailed", err.Error()), nil
		}
		log.Info("applied lifecycle configuration", "bucket", bucketName, "rules", len(policy.Spec.Rules))
	}

	policy.Status.BucketName = bucketName
	policy.Status.ClusterName = clusterName
	policy.Status.ClusterNamespace = clusterNS
	policy.Status.ObservedGeneration = policy.Generation
	policy.Status.AppliedRules = int32(len(policy.Spec.Rules))
	policy.Status.Phase = seaweedv1.BucketPhaseReady
	r.setCondition(policy, seaweedv1.BucketLifecyclePolicyConditionReady, metav1.ConditionTrue, "Reconciled", "")
	return ctrl.Result{}, nil
}

// handleDeletion clears the lifecycle configuration when reclaimPolicy is Delete
// and this policy actually applied one, then removes the finalizer. The target
// cluster and bucket come from status, so a Bucket removed under reclaimPolicy
// Retain still has its rules cleared.
func (r *BucketLifecyclePolicyReconciler) handleDeletion(ctx context.Context, policy *seaweedv1.BucketLifecyclePolicy, log logr.Logger) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(policy, BucketLifecyclePolicyFinalizer) {
		return ctrl.Result{}, nil
	}

	// Nothing was applied (or the user opted out), so there is nothing this
	// policy owns to clean up.
	if policy.Status.BucketName == "" || policy.Spec.ReclaimPolicy == seaweedv1.BucketReclaimRetain {
		return r.removeFinalizer(ctx, policy)
	}

	var seaweed seaweedv1.Seaweed
	clusterKey := types.NamespacedName{Namespace: policy.Status.ClusterNamespace, Name: policy.Status.ClusterName}
	if err := r.Get(ctx, clusterKey, &seaweed); err != nil {
		if apierrors.IsNotFound(err) {
			// The cluster is gone, and the bucket with it; release rather than block.
			log.Info("recorded cluster not found; releasing without lifecycle cleanup", "cluster", clusterKey)
			return r.removeFinalizer(ctx, policy)
		}
		return ctrl.Result{}, err
	}

	admin, err := r.adminFor(ctx, &seaweed, log)
	if err != nil {
		return ctrl.Result{}, err
	}
	defer closeBucketAdmin(admin, log)

	if err := admin.ClearLegacyBucketTTLs(ctx, policy.Status.BucketName); err != nil {
		return r.cleanupFailed(ctx, policy, err, log)
	}
	if err := admin.SetBucketLifecycle(ctx, policy.Status.BucketName, nil); err != nil {
		// The bucket is already gone, so its lifecycle config is too; release.
		if errors.Is(err, ErrBucketNotFound) {
			log.Info("bucket already gone; releasing without lifecycle cleanup", "bucket", policy.Status.BucketName)
			return r.removeFinalizer(ctx, policy)
		}
		return r.cleanupFailed(ctx, policy, err, log)
	}

	return r.removeFinalizer(ctx, policy)
}

// cleanupFailed records a deletion cleanup failure and returns the error so the
// reconcile is retried with the finalizer still in place.
func (r *BucketLifecyclePolicyReconciler) cleanupFailed(ctx context.Context, policy *seaweedv1.BucketLifecyclePolicy, err error, log logr.Logger) (ctrl.Result, error) {
	policy.Status.Phase = seaweedv1.BucketPhaseTerminating
	r.setCondition(policy, seaweedv1.BucketLifecyclePolicyConditionReady, metav1.ConditionFalse, "CleanupFailed", err.Error())
	if updateErr := r.Status().Update(ctx, policy); updateErr != nil {
		log.Error(updateErr, "status update during deletion")
	}
	return ctrl.Result{}, err
}

// adminFor builds a BucketAdmin for the given Seaweed cluster.
func (r *BucketLifecyclePolicyReconciler) adminFor(ctx context.Context, seaweed *seaweedv1.Seaweed, log logr.Logger) (BucketAdmin, error) {
	adminKey, err := loadFilerAdminSigningKey(ctx, r.Client, seaweed)
	if err != nil {
		return nil, err
	}
	dialOption, _, err := loadSeaweedGrpcDialOption(ctx, r.Client, seaweed)
	if err != nil {
		return nil, err
	}
	return r.AdminFactory(getMasterPeersString(seaweed), getFilerAddress(seaweed), adminKey, dialOption, log)
}

// bucketOwner returns the name of the policy that should manage the referenced
// bucket's lifecycle among the same-namespace policies pointing at it: the
// oldest, breaking ties by name. Policies being deleted are skipped so a
// terminating owner hands off to the next in line.
func (r *BucketLifecyclePolicyReconciler) bucketOwner(ctx context.Context, policy *seaweedv1.BucketLifecyclePolicy) (string, error) {
	var policies seaweedv1.BucketLifecyclePolicyList
	if err := r.List(ctx, &policies, client.InNamespace(policy.Namespace)); err != nil {
		return "", err
	}
	owner := policy
	for i := range policies.Items {
		p := &policies.Items[i]
		if p.Name == policy.Name || p.Spec.BucketRef.Name != policy.Spec.BucketRef.Name || !p.DeletionTimestamp.IsZero() {
			continue
		}
		if policyPrecedes(p, owner) {
			owner = p
		}
	}
	return owner.Name, nil
}

// policyPrecedes orders policies by creation time, breaking ties by name.
func policyPrecedes(a, b *seaweedv1.BucketLifecyclePolicy) bool {
	if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
		return a.CreationTimestamp.Before(&b.CreationTimestamp)
	}
	return a.Name < b.Name
}

// conflict marks a policy that lost ownership of its bucket and relinquishes its
// applied marker so it never cleans up another policy's configuration.
func (r *BucketLifecyclePolicyReconciler) conflict(policy *seaweedv1.BucketLifecyclePolicy, owner string) ctrl.Result {
	policy.Status.BucketName = ""
	policy.Status.ClusterName = ""
	policy.Status.ClusterNamespace = ""
	policy.Status.AppliedRules = 0
	return r.failPhase(policy, "Conflict",
		fmt.Sprintf("bucket %q lifecycle is managed by BucketLifecyclePolicy %q", policy.Spec.BucketRef.Name, owner))
}

// pending records a Pending phase with the dependency that is missing, clearing
// readiness so a previously-Ready policy doesn't report stale readiness once it
// can no longer reconcile. It requeues on the transient cadence.
func (r *BucketLifecyclePolicyReconciler) pending(policy *seaweedv1.BucketLifecyclePolicy, reason, message string) ctrl.Result {
	policy.Status.Phase = seaweedv1.BucketPhasePending
	r.setCondition(policy, seaweedv1.BucketLifecyclePolicyConditionBucketResolved, metav1.ConditionFalse, reason, message)
	r.setCondition(policy, seaweedv1.BucketLifecyclePolicyConditionReady, metav1.ConditionFalse, reason, message)
	return ctrl.Result{RequeueAfter: requeueAfterTransient}
}

func (r *BucketLifecyclePolicyReconciler) removeFinalizer(ctx context.Context, policy *seaweedv1.BucketLifecyclePolicy) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(policy, BucketLifecyclePolicyFinalizer) {
		return ctrl.Result{}, nil
	}
	controllerutil.RemoveFinalizer(policy, BucketLifecyclePolicyFinalizer)
	if err := r.Update(ctx, policy); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *BucketLifecyclePolicyReconciler) failPhase(policy *seaweedv1.BucketLifecyclePolicy, reason, message string) ctrl.Result {
	r.Log.Info("reconcile failed", "reason", reason, "message", message)
	policy.Status.Phase = seaweedv1.BucketPhaseFailed
	r.setCondition(policy, seaweedv1.BucketLifecyclePolicyConditionReady, metav1.ConditionFalse, reason, message)
	return ctrl.Result{RequeueAfter: requeueAfterTransient}
}

func (r *BucketLifecyclePolicyReconciler) setCondition(policy *seaweedv1.BucketLifecyclePolicy, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		ObservedGeneration: policy.Generation,
		Reason:             reason,
		Message:            message,
	})
}

// mapBucketToPolicies enqueues the policies that reference a Bucket so they
// reconcile as soon as the bucket is provisioned, instead of waiting for the
// periodic requeue.
func (r *BucketLifecyclePolicyReconciler) mapBucketToPolicies(ctx context.Context, obj client.Object) []reconcile.Request {
	bucket, ok := obj.(*seaweedv1.Bucket)
	if !ok {
		return nil
	}
	var policies seaweedv1.BucketLifecyclePolicyList
	if err := r.List(ctx, &policies, client.InNamespace(bucket.Namespace)); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range policies.Items {
		if policies.Items[i].Spec.BucketRef.Name == bucket.Name {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&policies.Items[i])})
		}
	}
	return reqs
}

// mapPolicyToPeers enqueues the other policies targeting the same bucket so a
// conflict loser takes over promptly when the owner changes or is deleted.
func (r *BucketLifecyclePolicyReconciler) mapPolicyToPeers(ctx context.Context, obj client.Object) []reconcile.Request {
	changed, ok := obj.(*seaweedv1.BucketLifecyclePolicy)
	if !ok {
		return nil
	}
	var policies seaweedv1.BucketLifecyclePolicyList
	if err := r.List(ctx, &policies, client.InNamespace(changed.Namespace)); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range policies.Items {
		p := &policies.Items[i]
		if p.Name != changed.Name && p.Spec.BucketRef.Name == changed.Spec.BucketRef.Name {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(p)})
		}
	}
	return reqs
}

// SetupWithManager wires the reconciler into the controller-runtime manager.
func (r *BucketLifecyclePolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.AdminFactory == nil {
		r.AdminFactory = NewSwadminBucketAdmin
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&seaweedv1.BucketLifecyclePolicy{}).
		Watches(&seaweedv1.Bucket{}, handler.EnqueueRequestsFromMapFunc(r.mapBucketToPolicies)).
		// Only react to peers on spec/create/delete, never status-only updates,
		// so two same-bucket policies don't ping-pong off each other's status.
		Watches(&seaweedv1.BucketLifecyclePolicy{}, handler.EnqueueRequestsFromMapFunc(r.mapPolicyToPeers),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}
