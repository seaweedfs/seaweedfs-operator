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

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
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
func (r *BucketLifecyclePolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("bucketlifecyclepolicy", req.NamespacedName)

	var policy seaweedv1.BucketLifecyclePolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	deleting := !policy.DeletionTimestamp.IsZero()
	if deleting && !controllerutil.ContainsFinalizer(&policy, BucketLifecyclePolicyFinalizer) {
		return ctrl.Result{}, nil
	}

	// Resolve the referenced bucket (same namespace). When the bucket — or its
	// cluster, below — is already gone there is nothing left to clean up, so a
	// deleting policy just drops its finalizer rather than blocking forever.
	var bucket seaweedv1.Bucket
	bucketKey := types.NamespacedName{Namespace: policy.Namespace, Name: policy.Spec.BucketRef.Name}
	if err := r.Get(ctx, bucketKey, &bucket); err != nil {
		if apierrors.IsNotFound(err) {
			if deleting {
				return r.removeFinalizer(ctx, &policy)
			}
			r.setCondition(&policy, seaweedv1.BucketLifecyclePolicyConditionBucketResolved, metav1.ConditionFalse, "BucketNotFound",
				fmt.Sprintf("Bucket %q not found in namespace %q", policy.Spec.BucketRef.Name, policy.Namespace))
			return r.pending(ctx, &policy)
		}
		return ctrl.Result{}, err
	}

	bucketName := bucket.Status.BucketName
	if bucketName == "" {
		if deleting {
			return r.removeFinalizer(ctx, &policy)
		}
		r.setCondition(&policy, seaweedv1.BucketLifecyclePolicyConditionBucketResolved, metav1.ConditionFalse, "BucketNotReady",
			fmt.Sprintf("Bucket %q is not provisioned yet", policy.Spec.BucketRef.Name))
		return r.pending(ctx, &policy)
	}
	r.setCondition(&policy, seaweedv1.BucketLifecyclePolicyConditionBucketResolved, metav1.ConditionTrue, "Resolved", "")

	seaweedNS := bucket.Spec.ClusterRef.Namespace
	if seaweedNS == "" {
		seaweedNS = bucket.Namespace
	}
	var seaweed seaweedv1.Seaweed
	if err := r.Get(ctx, types.NamespacedName{Namespace: seaweedNS, Name: bucket.Spec.ClusterRef.Name}, &seaweed); err != nil {
		if apierrors.IsNotFound(err) {
			if deleting {
				return r.removeFinalizer(ctx, &policy)
			}
			r.setCondition(&policy, seaweedv1.BucketLifecyclePolicyConditionBucketResolved, metav1.ConditionFalse, "ClusterRefNotFound",
				fmt.Sprintf("Seaweed %q not found in namespace %q", bucket.Spec.ClusterRef.Name, seaweedNS))
			return r.pending(ctx, &policy)
		}
		return ctrl.Result{}, err
	}

	masters := getMasterPeersString(&seaweed)
	filer := getFilerAddress(&seaweed)
	adminKey, err := loadFilerAdminSigningKey(ctx, r.Client, &seaweed)
	if err != nil {
		return ctrl.Result{}, err
	}
	dialOption, _, err := loadSeaweedGrpcDialOption(ctx, r.Client, &seaweed)
	if err != nil {
		return ctrl.Result{}, err
	}
	admin, err := r.AdminFactory(masters, filer, adminKey, dialOption, log)
	if err != nil {
		return ctrl.Result{}, err
	}
	defer closeBucketAdmin(admin, log)

	if deleting {
		return r.handleDeletion(ctx, &policy, bucketName, admin)
	}

	if !controllerutil.ContainsFinalizer(&policy, BucketLifecyclePolicyFinalizer) {
		controllerutil.AddFinalizer(&policy, BucketLifecyclePolicyFinalizer)
		if err := r.Update(ctx, &policy); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	return r.reconcilePolicy(ctx, &policy, bucketName, admin)
}

// reconcilePolicy applies the desired lifecycle configuration to the bucket,
// skipping the write when the bucket already matches.
func (r *BucketLifecyclePolicyReconciler) reconcilePolicy(ctx context.Context, policy *seaweedv1.BucketLifecyclePolicy, bucketName string, admin BucketAdmin) (ctrl.Result, error) {
	log := r.Log.WithValues("bucketlifecyclepolicy", types.NamespacedName{Namespace: policy.Namespace, Name: policy.Name})

	desired, err := buildLifecycleXML(policy.Spec.Rules)
	if err != nil {
		return r.failPhase(ctx, policy, "BuildFailed", err.Error())
	}
	current, err := admin.GetBucketLifecycle(ctx, bucketName)
	if err != nil {
		return r.failPhase(ctx, policy, "ReadFailed", err.Error())
	}
	if !lifecycleConfigEqual(current, desired) {
		if err := admin.SetBucketLifecycle(ctx, bucketName, desired); err != nil {
			return r.failPhase(ctx, policy, "ApplyFailed", err.Error())
		}
		log.Info("applied lifecycle configuration", "bucket", bucketName, "rules", len(policy.Spec.Rules))
	}

	policy.Status.BucketName = bucketName
	policy.Status.ObservedGeneration = policy.Generation
	policy.Status.AppliedRules = int32(len(policy.Spec.Rules))
	policy.Status.Phase = seaweedv1.BucketPhaseReady
	r.setCondition(policy, seaweedv1.BucketLifecyclePolicyConditionReady, metav1.ConditionTrue, "Reconciled", "")
	if err := r.Status().Update(ctx, policy); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// handleDeletion clears the lifecycle configuration when reclaimPolicy is
// Delete, then removes the finalizer.
func (r *BucketLifecyclePolicyReconciler) handleDeletion(ctx context.Context, policy *seaweedv1.BucketLifecyclePolicy, bucketName string, admin BucketAdmin) (ctrl.Result, error) {
	policy.Status.Phase = seaweedv1.BucketPhaseTerminating

	if policy.Spec.ReclaimPolicy != seaweedv1.BucketReclaimRetain {
		if err := admin.SetBucketLifecycle(ctx, bucketName, nil); err != nil {
			r.setCondition(policy, seaweedv1.BucketLifecyclePolicyConditionReady, metav1.ConditionFalse, "CleanupFailed", err.Error())
			if updateErr := r.Status().Update(ctx, policy); updateErr != nil {
				r.Log.Error(updateErr, "status update during deletion")
			}
			return ctrl.Result{}, err
		}
	}

	return r.removeFinalizer(ctx, policy)
}

// pending records a Pending phase and requeues on the transient cadence so the
// policy is retried once its bucket (or cluster) becomes available.
func (r *BucketLifecyclePolicyReconciler) pending(ctx context.Context, policy *seaweedv1.BucketLifecyclePolicy) (ctrl.Result, error) {
	policy.Status.Phase = seaweedv1.BucketPhasePending
	if err := r.Status().Update(ctx, policy); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
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

func (r *BucketLifecyclePolicyReconciler) failPhase(ctx context.Context, policy *seaweedv1.BucketLifecyclePolicy, reason, message string) (ctrl.Result, error) {
	r.Log.Info("reconcile failed", "reason", reason, "message", message)
	policy.Status.Phase = seaweedv1.BucketPhaseFailed
	r.setCondition(policy, seaweedv1.BucketLifecyclePolicyConditionReady, metav1.ConditionFalse, reason, message)
	if err := r.Status().Update(ctx, policy); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
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

// SetupWithManager wires the reconciler into the controller-runtime manager.
func (r *BucketLifecyclePolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.AdminFactory == nil {
		r.AdminFactory = NewSwadminBucketAdmin
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&seaweedv1.BucketLifecyclePolicy{}).
		Watches(&seaweedv1.Bucket{}, handler.EnqueueRequestsFromMapFunc(r.mapBucketToPolicies)).
		Complete(r)
}
