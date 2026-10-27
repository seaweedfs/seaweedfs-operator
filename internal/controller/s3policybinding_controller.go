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
	"bytes"
	"context"
	"encoding/json"
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

	s3policyengine "github.com/seaweedfs/seaweedfs/weed/s3api/policy_engine"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/internal/controller/swadmin"
)

// S3PolicyBindingReconciler reconciles S3PolicyBinding resources by attaching
// a policy to the listed subjects — IAM identities via AttachPolicy, and
// buckets by storing the document as their bucket policy.
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
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=s3policies;s3identities;buckets,verbs=get;list;watch

// Reconcile drives an S3PolicyBinding so the policy is attached to exactly the
// listed subjects.
func (r *S3PolicyBindingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("s3policybinding", req.NamespacedName)

	var binding seaweedv1.S3PolicyBinding
	if err := r.Get(ctx, req.NamespacedName, &binding); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Once attached the binding is pinned to the IAM policy it attached, so a
	// later resource change cannot silently retarget it. Attached subjects
	// recorded without a policy name predate pinning and used the literal
	// reference name.
	var policyName string
	switch {
	case binding.Status.PolicyName != "":
		policyName = binding.Status.PolicyName
	case len(binding.Status.AttachedSubjects) > 0:
		policyName = binding.Spec.PolicyRef.Name
	default:
		var err error
		policyName, err = resolvePolicyIAMName(ctx, r.Client, binding.Namespace, binding.Spec.PolicyRef.Name,
			seaweedRefKey(binding.Spec.SeaweedRef, binding.Namespace))
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	// Cross-namespace seaweedRef needs a grant; skip on deletion to not block cleanup.
	if binding.DeletionTimestamp.IsZero() {
		permitted, err := seaweedRefPermitted(ctx, r.Client, binding.Spec.SeaweedRef, kindS3PolicyBinding, binding.Namespace)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !permitted {
			return r.refForbidden(ctx, &binding, seaweedRefDeniedMessage(binding.Spec.SeaweedRef, kindS3PolicyBinding, binding.Namespace))
		}
		clearIAMCondition(&binding.Status.Conditions, seaweedv1.S3ConditionReferenceGranted)
	}

	target, found, err := resolveSeaweedFiler(ctx, r.Client, binding.Spec.SeaweedRef, binding.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !found {
		if handled, err := releaseFinalizerIfDeleting(ctx, r.Client, r.Recorder, &binding, s3PolicyBindingFinalizer, seaweedRefKey(binding.Spec.SeaweedRef, binding.Namespace)); handled {
			return ctrl.Result{}, err
		}
		return r.clusterNotFound(ctx, &binding)
	}
	setIAMCondition(&binding.Status.Conditions, binding.Generation, seaweedv1.S3ConditionClusterReachable, metav1.ConditionTrue, "Reachable", "")

	admin, err := r.getIAMAdmin(target, log)
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

	// Wait for the policy to exist before attaching it to anyone. The pin is
	// recorded only past this point, so a binding still waiting on its policy
	// keeps following resolution changes.
	policyDoc, err := admin.GetPolicy(ctx, policyName)
	if err != nil {
		if errors.Is(err, ErrIAMNotFound) {
			return r.pending(ctx, &binding, "PolicyMissing",
				fmt.Sprintf("policy %q does not exist yet", policyName))
		}
		return r.fail(ctx, &binding, "PolicyLookupFailed", err.Error())
	}
	binding.Status.PolicyName = policyName

	desired, desiredBuckets, err := resolveSubjects(ctx, r.Client, &binding)
	if err != nil {
		return ctrl.Result{}, err
	}

	// A bucket carries exactly one policy document, so two bindings
	// targeting it would flap on every resync; the older binding owns the
	// slot and the loser releases it.
	var conflicted []string
	conflictedSet := map[string]struct{}{}
	applyBuckets := make([]string, 0, len(desiredBuckets))
	for _, b := range desiredBuckets {
		winner, err := bindingBucketConflict(ctx, r.Client, &binding, b)
		if err != nil {
			return ctrl.Result{}, err
		}
		if winner != nil {
			conflicted = append(conflicted, fmt.Sprintf("%s (owned by %s/%s)", b, winner.Namespace, winner.Name))
			conflictedSet[b] = struct{}{}
			continue
		}
		applyBuckets = append(applyBuckets, b)
	}

	// Validate the document before writing it anywhere: a resource policy
	// needs a Principal per statement and resources scoped to buckets this
	// binding manages — the to-be-applied set plus anything already attached
	// (a shared document keeps statements for a bucket mid-removal valid so
	// the detach below can still run).
	if len(applyBuckets) > 0 || len(binding.Status.AttachedBuckets) > 0 {
		managed := append(append([]string(nil), applyBuckets...), binding.Status.AttachedBuckets...)
		if err := validateBucketPolicyDoc(policyDoc, managed); err != nil {
			return r.fail(ctx, &binding, "PolicyNotBucketPolicy", err.Error())
		}
	}

	// Detach subjects no longer listed (compared against the last applied
	// set). A conflicted bucket is released without clearing: the winning
	// peer may already have written its own document there, and the slot is
	// theirs to manage now.
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
	applySet := map[string]struct{}{}
	for _, b := range applyBuckets {
		applySet[b] = struct{}{}
	}
	for _, prev := range binding.Status.AttachedBuckets {
		if _, keep := applySet[prev]; keep {
			continue
		}
		if _, released := conflictedSet[prev]; released {
			continue
		}
		if err := admin.SetBucketPolicy(ctx, prev, nil); err != nil && !errors.Is(err, swadmin.ErrBucketNotFound) {
			return r.fail(ctx, &binding, "DetachFailed", fmt.Sprintf("clear bucket policy on %q: %s", prev, err.Error()))
		}
	}

	// Attach the policy to each desired subject. A missing identity or
	// bucket is transient and must not block the others: skip it, attach
	// everyone present, and requeue at the end so the binding converges as
	// the missing subjects appear. Real (non-NotFound) errors still fail
	// fast.
	attached := make([]string, 0, len(desired))
	attachedBuckets := make([]string, 0, len(applyBuckets))
	var pendingSubjects []string
	for _, user := range desired {
		if err := admin.AttachPolicy(ctx, user, policyName); err != nil {
			if errors.Is(err, ErrIAMNotFound) {
				pendingSubjects = append(pendingSubjects, user)
				continue
			}
			binding.Status.AttachedSubjects = attached
			binding.Status.AttachedBuckets = attachedBuckets
			return r.fail(ctx, &binding, "AttachFailed", fmt.Sprintf("attach %q to %q: %s", policyName, user, err.Error()))
		}
		attached = append(attached, user)
	}
	for _, b := range applyBuckets {
		if err := applyBucketPolicy(ctx, admin, b, policyDoc); err != nil {
			if errors.Is(err, swadmin.ErrBucketNotFound) {
				pendingSubjects = append(pendingSubjects, b)
				continue
			}
			binding.Status.AttachedSubjects = attached
			binding.Status.AttachedBuckets = attachedBuckets
			return r.fail(ctx, &binding, "AttachFailed", fmt.Sprintf("apply bucket policy on %q: %s", b, err.Error()))
		}
		attachedBuckets = append(attachedBuckets, b)
	}

	binding.Status.AttachedSubjects = attached
	binding.Status.AttachedBuckets = attachedBuckets
	if len(conflicted) > 0 {
		return r.fail(ctx, &binding, "BucketConflict",
			fmt.Sprintf("bucket policy slots claimed by other bindings: %s", strings.Join(conflicted, ", ")))
	}
	if len(pendingSubjects) > 0 {
		return r.pending(ctx, &binding, "SubjectMissing",
			fmt.Sprintf("subjects do not exist yet: %s", strings.Join(pendingSubjects, ", ")))
	}

	binding.Status.ObservedGeneration = binding.Generation
	binding.Status.Phase = seaweedv1.S3PhaseReady
	setIAMCondition(&binding.Status.Conditions, binding.Generation, seaweedv1.S3ConditionReady, metav1.ConditionTrue, "Reconciled", "")
	if err := r.Status().Update(ctx, &binding); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: iamResyncInterval}, nil
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
		for _, b := range binding.Status.AttachedBuckets {
			if err := admin.SetBucketPolicy(ctx, b, nil); err != nil && !errors.Is(err, swadmin.ErrBucketNotFound) {
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

// refForbidden requeues (not Failed) until a ResourceReferenceGrant permits the
// reference.
func (r *S3PolicyBindingReconciler) refForbidden(ctx context.Context, binding *seaweedv1.S3PolicyBinding, message string) (ctrl.Result, error) {
	setIAMCondition(&binding.Status.Conditions, binding.Generation, seaweedv1.S3ConditionReferenceGranted, metav1.ConditionFalse, "ReferenceGrantMissing", message)
	binding.Status.Phase = seaweedv1.S3PhasePending
	if err := r.Status().Update(ctx, binding); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
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

// resolveSubjects returns the deduplicated, sorted lists of IAM user names
// and bucket names the binding's subjects resolve to.
func resolveSubjects(ctx context.Context, c client.Client, binding *seaweedv1.S3PolicyBinding) (identities, buckets []string, err error) {
	clusterKey := seaweedRefKey(binding.Spec.SeaweedRef, binding.Namespace)
	seen := map[string]struct{}{}
	seenBuckets := map[string]struct{}{}
	for _, s := range binding.Spec.Subjects {
		if s.Kind == seaweedv1.S3SubjectKindBucket {
			name, err := resolveBucketSubjectName(ctx, c, binding.Namespace, s.Name, clusterKey)
			if err != nil {
				return nil, nil, err
			}
			if _, dup := seenBuckets[name]; dup {
				continue
			}
			seenBuckets[name] = struct{}{}
			buckets = append(buckets, name)
			continue
		}
		name, err := resolveIdentityIAMName(ctx, c, binding.Namespace, s.Name, clusterKey)
		if err != nil {
			return nil, nil, err
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		identities = append(identities, name)
	}
	sort.Strings(identities)
	sort.Strings(buckets)
	return identities, buckets, nil
}

// validateBucketPolicyDoc checks that an IAM policy document can serve as a
// bucket policy for the bound buckets: a Principal (or NotPrincipal) on every
// statement, s3: actions only, known condition operators, and resources that
// name at least one bound bucket. Unlike the S3 API's per-bucket check,
// resources may also name other bound buckets — one document can be shared
// across a project of buckets, with each statement inert where its resources
// do not match.
func validateBucketPolicyDoc(policyDoc string, buckets []string) error {
	var doc s3policyengine.PolicyDocument
	if err := json.Unmarshal([]byte(policyDoc), &doc); err != nil {
		return fmt.Errorf("policy document cannot serve as a bucket policy: %v", err)
	}
	for i, st := range doc.Statement {
		if st.Principal == nil && st.NotPrincipal == nil {
			return fmt.Errorf("statement %d: bucket policies must specify a Principal", i)
		}
		for _, res := range st.Resource.Strings() {
			if !resourceMatchesAnyBucket(res, buckets) {
				return fmt.Errorf("statement %d: resource %s does not match any bound bucket", i, res)
			}
		}
		for _, res := range st.NotResource.Strings() {
			if !resourceMatchesAnyBucket(res, buckets) {
				return fmt.Errorf("statement %d: NotResource %s does not match any bound bucket", i, res)
			}
		}
		for _, action := range st.Action.Strings() {
			if !strings.HasPrefix(action, "s3:") {
				return fmt.Errorf("statement %d: bucket policies only support S3 actions, got %s", i, action)
			}
		}
		for operator := range st.Condition {
			if _, err := s3policyengine.GetConditionEvaluator(operator); err != nil {
				return fmt.Errorf("statement %d: unsupported condition operator %q: %v", i, operator, err)
			}
		}
	}
	return nil
}

func resourceMatchesAnyBucket(resource string, buckets []string) bool {
	for _, b := range buckets {
		if s3policyengine.ResourceMatchesBucket(resource, b) {
			return true
		}
	}
	return false
}

// applyBucketPolicy stores the policy document as the bucket's policy,
// skipping the write when the stored document is already identical.
func applyBucketPolicy(ctx context.Context, admin IAMAdmin, bucket, policyDoc string) error {
	current, err := admin.GetBucketPolicy(ctx, bucket)
	if err != nil {
		return err
	}
	if bytes.Equal(current, []byte(policyDoc)) {
		return nil
	}
	return admin.SetBucketPolicy(ctx, bucket, []byte(policyDoc))
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
