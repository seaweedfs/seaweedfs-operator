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
	"sort"
	"strings"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// S3PolicyBindingReconciler reconciles S3PolicyBinding resources by attaching
// a policy to (and detaching it from) the listed IAM identities.
type S3PolicyBindingReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	iamAdminProvider
}

// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=s3policybindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=s3policybindings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=s3policybindings/finalizers,verbs=update

// Reconcile drives an S3PolicyBinding so the policy is attached to exactly the
// listed subjects.
func (r *S3PolicyBindingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("s3policybinding", req.NamespacedName)

	var binding seaweedv1.S3PolicyBinding
	if err := r.Get(ctx, req.NamespacedName, &binding); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	policyName := binding.Spec.PolicyRef.Name

	filer, adminKey, found, err := resolveSeaweedFiler(ctx, r.Client, binding.Spec.SeaweedRef, binding.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !found {
		return r.clusterNotFound(ctx, &binding)
	}
	setIAMCondition(&binding.Status.Conditions, binding.Generation, seaweedv1.S3ConditionClusterReachable, metav1.ConditionTrue, "Reachable", "")

	admin, err := r.getIAMAdmin(filer, adminKey, log)
	if err != nil {
		return ctrl.Result{}, err
	}

	if !binding.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &binding, policyName, admin)
	}

	if !controllerutil.ContainsFinalizer(&binding, s3PolicyBindingFinalizer) {
		controllerutil.AddFinalizer(&binding, s3PolicyBindingFinalizer)
		if err := r.Update(ctx, &binding); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Wait for the policy to exist before attaching it to anyone.
	if _, err := admin.GetPolicy(ctx, policyName); err != nil {
		if errors.Is(err, ErrIAMNotFound) {
			return r.pending(ctx, &binding, "PolicyMissing",
				fmt.Sprintf("policy %q does not exist yet", policyName))
		}
		return r.fail(ctx, &binding, "PolicyLookupFailed", err.Error())
	}

	desired := desiredSubjects(binding.Spec.Subjects)

	// Detach subjects no longer listed (compared against the last applied set).
	desiredSet := map[string]struct{}{}
	for _, s := range desired {
		desiredSet[s] = struct{}{}
	}
	for _, prev := range binding.Status.AttachedSubjects {
		if _, keep := desiredSet[prev]; keep {
			continue
		}
		if err := admin.DetachPolicy(ctx, prev, policyName); err != nil && !errors.Is(err, ErrIAMNotFound) {
			return r.fail(ctx, &binding, "DetachFailed", fmt.Sprintf("detach %q from %q: %s", policyName, prev, err.Error()))
		}
	}

	// Attach the policy to each desired subject. A missing identity is
	// transient and must not block the others: skip it, attach everyone
	// present, and requeue at the end so the binding converges as the
	// missing identities appear. Real (non-NotFound) errors still fail fast.
	attached := make([]string, 0, len(desired))
	var pendingSubjects []string
	for _, user := range desired {
		if err := admin.AttachPolicy(ctx, user, policyName); err != nil {
			if errors.Is(err, ErrIAMNotFound) {
				pendingSubjects = append(pendingSubjects, user)
				continue
			}
			binding.Status.AttachedSubjects = attached
			return r.fail(ctx, &binding, "AttachFailed", fmt.Sprintf("attach %q to %q: %s", policyName, user, err.Error()))
		}
		attached = append(attached, user)
	}

	binding.Status.AttachedSubjects = attached
	if len(pendingSubjects) > 0 {
		return r.pending(ctx, &binding, "SubjectMissing",
			fmt.Sprintf("identities do not exist yet: %s", strings.Join(pendingSubjects, ", ")))
	}

	binding.Status.ObservedGeneration = binding.Generation
	binding.Status.Phase = seaweedv1.S3PhaseReady
	setIAMCondition(&binding.Status.Conditions, binding.Generation, seaweedv1.S3ConditionReady, metav1.ConditionTrue, "Reconciled", "")
	if err := r.Status().Update(ctx, &binding); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *S3PolicyBindingReconciler) handleDeletion(ctx context.Context, binding *seaweedv1.S3PolicyBinding, policyName string, admin IAMAdmin) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(binding, s3PolicyBindingFinalizer) {
		return ctrl.Result{}, nil
	}
	binding.Status.Phase = seaweedv1.S3PhaseTerminating

	if binding.Spec.ReclaimPolicy != seaweedv1.S3ReclaimRetain {
		for _, user := range binding.Status.AttachedSubjects {
			if err := admin.DetachPolicy(ctx, user, policyName); err != nil && !errors.Is(err, ErrIAMNotFound) {
				setIAMCondition(&binding.Status.Conditions, binding.Generation, seaweedv1.S3ConditionReady, metav1.ConditionFalse, "DetachFailed", err.Error())
				if updateErr := r.Status().Update(ctx, binding); updateErr != nil {
					r.Log.Error(updateErr, "status update during deletion")
				}
				return ctrl.Result{}, err
			}
		}
	}

	controllerutil.RemoveFinalizer(binding, s3PolicyBindingFinalizer)
	if err := r.Update(ctx, binding); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *S3PolicyBindingReconciler) clusterNotFound(ctx context.Context, binding *seaweedv1.S3PolicyBinding) (ctrl.Result, error) {
	setIAMCondition(&binding.Status.Conditions, binding.Generation, seaweedv1.S3ConditionClusterReachable, metav1.ConditionFalse, "ClusterRefNotFound",
		fmt.Sprintf("Seaweed %q not found", binding.Spec.SeaweedRef.Name))
	binding.Status.Phase = seaweedv1.S3PhasePending
	if err := r.Status().Update(ctx, binding); err != nil {
		r.Log.Error(err, "status update")
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
}

// pending records a transient unmet dependency (missing policy or subject) and
// requeues without marking the binding Failed.
func (r *S3PolicyBindingReconciler) pending(ctx context.Context, binding *seaweedv1.S3PolicyBinding, reason, message string) (ctrl.Result, error) {
	binding.Status.Phase = seaweedv1.S3PhasePending
	setIAMCondition(&binding.Status.Conditions, binding.Generation, seaweedv1.S3ConditionReady, metav1.ConditionFalse, reason, message)
	if err := r.Status().Update(ctx, binding); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
}

func (r *S3PolicyBindingReconciler) fail(ctx context.Context, binding *seaweedv1.S3PolicyBinding, reason, message string) (ctrl.Result, error) {
	r.Log.Info("s3policybinding reconcile failed", "reason", reason, "message", message)
	binding.Status.Phase = seaweedv1.S3PhaseFailed
	setIAMCondition(&binding.Status.Conditions, binding.Generation, seaweedv1.S3ConditionReady, metav1.ConditionFalse, reason, message)
	if err := r.Status().Update(ctx, binding); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
}

// desiredSubjects returns the deduplicated, sorted list of IAM user names from
// the binding's subjects.
func desiredSubjects(subjects []seaweedv1.S3Subject) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(subjects))
	for _, s := range subjects {
		if _, dup := seen[s.Name]; dup {
			continue
		}
		seen[s.Name] = struct{}{}
		out = append(out, s.Name)
	}
	sort.Strings(out)
	return out
}

// SetupWithManager wires the reconciler into the manager.
func (r *S3PolicyBindingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.AdminFactory == nil {
		r.AdminFactory = NewSwadminIAMAdmin
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&seaweedv1.S3PolicyBinding{}).
		Complete(r)
}
