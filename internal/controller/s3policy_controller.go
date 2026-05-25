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

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// S3PolicyReconciler reconciles S3Policy resources into IAM policies on the
// target Seaweed cluster.
type S3PolicyReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	iamAdminProvider
}

// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=s3policies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=s3policies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=s3policies/finalizers,verbs=update

// Reconcile drives an S3Policy to match its spec: put/delete the underlying
// IAM policy document.
func (r *S3PolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("s3policy", req.NamespacedName)

	var policy seaweedv1.S3Policy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	name := policy.Spec.Name
	if name == "" {
		name = policy.Name
	}

	if policy.Status.PolicyName != "" && policy.Status.PolicyName != name {
		return r.fail(ctx, &policy, "PolicyRenameNotSupported",
			fmt.Sprintf("policy name change from %q to %q is not supported; restore the original name or recreate the resource",
				policy.Status.PolicyName, name))
	}

	filer, found, err := resolveSeaweedFiler(ctx, r.Client, policy.Spec.SeaweedRef, policy.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !found {
		return r.clusterNotFound(ctx, &policy)
	}
	setIAMCondition(&policy.Status.Conditions, policy.Generation, seaweedv1.S3ConditionClusterReachable, metav1.ConditionTrue, "Reachable", "")

	admin, err := r.getIAMAdmin(filer, log)
	if err != nil {
		return ctrl.Result{}, err
	}

	if !policy.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &policy, name, admin)
	}

	if !controllerutil.ContainsFinalizer(&policy, s3PolicyFinalizer) {
		controllerutil.AddFinalizer(&policy, s3PolicyFinalizer)
		if err := r.Update(ctx, &policy); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	document, err := buildPolicyDocument(&policy.Spec)
	if err != nil {
		return r.fail(ctx, &policy, "InvalidPolicy", err.Error())
	}
	if err := admin.PutPolicy(ctx, name, document); err != nil {
		return r.fail(ctx, &policy, "PutFailed", err.Error())
	}
	log.Info("applied IAM policy", "name", name)

	policy.Status.PolicyName = name
	policy.Status.ObservedGeneration = policy.Generation
	policy.Status.Phase = seaweedv1.S3PhaseReady
	setIAMCondition(&policy.Status.Conditions, policy.Generation, seaweedv1.S3ConditionReady, metav1.ConditionTrue, "Reconciled", "")
	if err := r.Status().Update(ctx, &policy); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *S3PolicyReconciler) handleDeletion(ctx context.Context, policy *seaweedv1.S3Policy, name string, admin IAMAdmin) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(policy, s3PolicyFinalizer) {
		return ctrl.Result{}, nil
	}
	policy.Status.Phase = seaweedv1.S3PhaseTerminating

	if policy.Spec.ReclaimPolicy != seaweedv1.S3ReclaimRetain {
		// Idempotent: a policy already gone must not block finalizer removal.
		if err := admin.DeletePolicy(ctx, name); err != nil && !errors.Is(err, ErrIAMNotFound) {
			setIAMCondition(&policy.Status.Conditions, policy.Generation, seaweedv1.S3ConditionReady, metav1.ConditionFalse, "DeleteFailed", err.Error())
			if updateErr := r.Status().Update(ctx, policy); updateErr != nil {
				r.Log.Error(updateErr, "status update during deletion")
			}
			return ctrl.Result{}, err
		}
	}

	controllerutil.RemoveFinalizer(policy, s3PolicyFinalizer)
	if err := r.Update(ctx, policy); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *S3PolicyReconciler) clusterNotFound(ctx context.Context, policy *seaweedv1.S3Policy) (ctrl.Result, error) {
	setIAMCondition(&policy.Status.Conditions, policy.Generation, seaweedv1.S3ConditionClusterReachable, metav1.ConditionFalse, "ClusterRefNotFound",
		fmt.Sprintf("Seaweed %q not found", policy.Spec.SeaweedRef.Name))
	policy.Status.Phase = seaweedv1.S3PhasePending
	if err := r.Status().Update(ctx, policy); err != nil {
		r.Log.Error(err, "status update")
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
}

func (r *S3PolicyReconciler) fail(ctx context.Context, policy *seaweedv1.S3Policy, reason, message string) (ctrl.Result, error) {
	r.Log.Info("s3policy reconcile failed", "reason", reason, "message", message)
	policy.Status.Phase = seaweedv1.S3PhaseFailed
	setIAMCondition(&policy.Status.Conditions, policy.Generation, seaweedv1.S3ConditionReady, metav1.ConditionFalse, reason, message)
	if err := r.Status().Update(ctx, policy); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
}

// SetupWithManager wires the reconciler into the manager.
func (r *S3PolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.AdminFactory == nil {
		r.AdminFactory = NewSwadminIAMAdmin
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&seaweedv1.S3Policy{}).
		Complete(r)
}
