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
	"slices"
	"sort"
	"strings"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	s3policyengine "github.com/seaweedfs/seaweedfs/weed/s3api/policy_engine"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/internal/controller/swadmin"
)

// S3PolicyBindingReconciler reconciles S3PolicyBinding resources by attaching
// a policy to the listed subjects — IAM identities via AttachPolicy, and
// buckets by merging the document's statements into their bucket policy.
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

	desired, desiredBuckets, err := resolveSubjects(ctx, r.Client, &binding)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Detach subjects no longer listed (compared against the last applied
	// set). A released bucket's document is recomputed from the bindings
	// still claiming it, so this binding's statements are revoked without
	// erasing grants others installed.
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
	for _, b := range desiredBuckets {
		applySet[b] = struct{}{}
	}
	for _, prev := range binding.Status.AttachedBuckets {
		if _, keep := applySet[prev]; keep {
			continue
		}
		if err := r.releaseBucketPolicy(ctx, admin, &binding, prev); err != nil && !errors.Is(err, swadmin.ErrBucketNotFound) {
			return r.fail(ctx, &binding, "DetachFailed", fmt.Sprintf("clear bucket policy on %q: %s", prev, err.Error()))
		}
	}

	// Buckets just released still count for document validation — a shared
	// document stays valid mid-removal — but they must stop claiming their
	// slot: prune the records before a failure below persists them.
	binding.Status.AttachedSubjects = slices.DeleteFunc(binding.Status.AttachedSubjects, func(s string) bool {
		_, keep := desiredSet[s]
		return !keep
	})
	managed := append(append([]string(nil), desiredBuckets...), binding.Status.AttachedBuckets...)
	binding.Status.AttachedBuckets = slices.DeleteFunc(binding.Status.AttachedBuckets, func(b string) bool {
		_, keep := applySet[b]
		return !keep
	})

	// Attaching waits for the policy to exist; the pin is recorded only past
	// this point. Revocation runs first so a deleted policy cannot keep
	// removed grants installed.
	policyDoc, err := admin.GetPolicy(ctx, policyName)
	if err != nil {
		if errors.Is(err, ErrIAMNotFound) {
			return r.pending(ctx, &binding, "PolicyMissing",
				fmt.Sprintf("policy %q does not exist yet", policyName))
		}
		return r.fail(ctx, &binding, "PolicyLookupFailed", err.Error())
	}
	binding.Status.PolicyName = policyName

	// A bucket stores a single policy document, so every binding claiming it
	// contributes its statements to a merged document; deterministic
	// contributor order keeps all bindings computing the same result. Each
	// contribution is validated against the buckets its own binding manages
	// — a broken contributor fails only its own reconcile — while the
	// binding's document must be usable wherever it applies.
	if len(desiredBuckets) > 0 {
		if err := validateBucketPolicyDoc(policyDoc, managed); err != nil {
			return r.fail(ctx, &binding, "PolicyNotBucketPolicy", err.Error())
		}
	}
	mergedDocs := make(map[string]string, len(desiredBuckets))
	policyDocs := map[string]string{policyName: policyDoc}
	for _, b := range desiredBuckets {
		merged, err := r.mergedBucketPolicy(ctx, admin, &binding, b, policyDoc, policyDocs)
		if err != nil {
			return r.fail(ctx, &binding, "PolicyNotBucketPolicy", err.Error())
		}
		mergedDocs[b] = merged
	}

	// Attach the policy to each desired subject. A missing identity or
	// bucket is transient and must not block the others: skip it, attach
	// everyone present, and requeue at the end so the binding converges as
	// the missing subjects appear. Real (non-NotFound) errors still fail
	// fast.
	attached := make([]string, 0, len(desired))
	attachedBuckets := make([]string, 0, len(desiredBuckets))
	var pendingSubjects []string
	// preserve records the attached set after a mid-apply failure without
	// dropping buckets still owned but not yet re-processed — losing those
	// records would orphan their installed policies.
	preserve := func() {
		binding.Status.AttachedSubjects = mergeKeptBuckets(binding.Status.AttachedSubjects, attached, desiredSet)
		binding.Status.AttachedBuckets = mergeKeptBuckets(binding.Status.AttachedBuckets, attachedBuckets, applySet)
	}
	for _, user := range desired {
		if err := admin.AttachPolicy(ctx, user, policyName); err != nil {
			if errors.Is(err, ErrIAMNotFound) {
				pendingSubjects = append(pendingSubjects, user)
				continue
			}
			preserve()
			return r.fail(ctx, &binding, "AttachFailed", fmt.Sprintf("attach %q to %q: %s", policyName, user, err.Error()))
		}
		attached = append(attached, user)
	}
	for _, b := range desiredBuckets {
		if err := applyBucketPolicy(ctx, admin, b, mergedDocs[b]); err != nil {
			if errors.Is(err, swadmin.ErrBucketNotFound) {
				pendingSubjects = append(pendingSubjects, b)
				continue
			}
			preserve()
			return r.fail(ctx, &binding, "AttachFailed", fmt.Sprintf("apply bucket policy on %q: %s", b, err.Error()))
		}
		attachedBuckets = append(attachedBuckets, b)
	}

	binding.Status.AttachedSubjects = attached
	binding.Status.AttachedBuckets = attachedBuckets
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
			if err := r.releaseBucketPolicy(ctx, admin, binding, b); err != nil && !errors.Is(err, swadmin.ErrBucketNotFound) {
				setIAMCondition(&binding.Status.Conditions, binding.Generation, seaweedv1.S3ConditionReady, metav1.ConditionFalse, "DetachFailed", err.Error())
				if updateErr := r.Status().Update(ctx, binding); updateErr != nil {
					r.Log.Error(updateErr, "status update during deletion")
				}
				return ctrl.Result{}, err
			}
		}
	} else {
		// Retained bucket grants are restamped in the stored document so
		// the next merge — which rebuilds it from live bindings alone —
		// keeps carrying them.
		for _, b := range binding.Status.AttachedBuckets {
			if err := r.retainBucketPolicy(ctx, admin, binding, policyName, b); err != nil && !errors.Is(err, swadmin.ErrBucketNotFound) {
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
// bucket policy for the bound buckets: the generic structural and size rules
// the S3 gateway enforces on PutBucketPolicy, a Principal (or NotPrincipal)
// on every statement, s3: actions only, known condition operators, and
// resources that name at least one bound bucket. Unlike the S3 API's
// per-bucket check, resources may also name other bound buckets — one
// document can be shared across a project of buckets, with each statement
// inert where its resources do not match.
func validateBucketPolicyDoc(policyDoc string, buckets []string) error {
	if len(policyDoc) > s3policyengine.MaxBucketPolicySize {
		return fmt.Errorf("policy document exceeds the %d byte bucket policy limit", s3policyengine.MaxBucketPolicySize)
	}
	doc, err := s3policyengine.ParsePolicy(policyDoc)
	if err != nil {
		return fmt.Errorf("policy document cannot serve as a bucket policy: %v", err)
	}
	for i, st := range doc.Statement {
		if strings.HasPrefix(st.Sid, retainedStatementSid) {
			return fmt.Errorf("statement %d: Sid %q uses the reserved %q prefix", i, st.Sid, retainedStatementSid)
		}
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

// releaseBucketPolicy removes this binding's contribution from a bucket it
// no longer manages: the stored document is rewritten from the surviving
// claimants' policies plus any retained statements, or cleared when nothing
// remains. A remainder that cannot be written leaves the stored document
// alone — the survivors' own reconciles rebuild it.
func (r *S3PolicyBindingReconciler) releaseBucketPolicy(ctx context.Context, admin IAMAdmin, binding *seaweedv1.S3PolicyBinding, bucket string) error {
	contributors, err := r.bucketContributors(ctx, admin, binding, bucket, "", map[string]string{})
	if err != nil {
		return err
	}
	retained, err := r.storedRetained(ctx, admin, bucket)
	if err != nil {
		return err
	}
	if len(contributors) == 0 && len(retained) == 0 {
		return admin.SetBucketPolicy(ctx, bucket, nil)
	}
	doc, err := assembleBucketPolicy(contributors, retained)
	if err != nil {
		return err
	}
	if len(doc) > s3policyengine.MaxBucketPolicySize {
		return fmt.Errorf("surviving contributors exceed the %d byte bucket policy limit", s3policyengine.MaxBucketPolicySize)
	}
	return applyBucketPolicy(ctx, admin, bucket, doc)
}

// retainBucketPolicy persists this binding's statements in the stored
// document beyond deletion: they are restamped with the retained marker so
// later merges keep carrying the grant. The mark replaces the statements in
// place, so a retried deletion does not add them twice.
func (r *S3PolicyBindingReconciler) retainBucketPolicy(ctx context.Context, admin IAMAdmin, binding *seaweedv1.S3PolicyBinding, policyName, bucket string) error {
	own, err := admin.GetPolicy(ctx, policyName)
	if err != nil {
		if errors.Is(err, ErrIAMNotFound) {
			return nil
		}
		return err
	}
	stored, err := admin.GetBucketPolicy(ctx, bucket)
	if err != nil {
		return err
	}
	doc, err := restampRetained(string(stored), own)
	if err != nil {
		return err
	}
	if len(doc) > s3policyengine.MaxBucketPolicySize {
		return fmt.Errorf("policy on %q exceeds the %d byte bucket policy limit", bucket, s3policyengine.MaxBucketPolicySize)
	}
	return applyBucketPolicy(ctx, admin, bucket, doc)
}

// storedRetained extracts the retained statements of a bucket's stored
// policy, treating a missing bucket as empty.
func (r *S3PolicyBindingReconciler) storedRetained(ctx context.Context, admin IAMAdmin, bucket string) ([]json.RawMessage, error) {
	stored, err := admin.GetBucketPolicy(ctx, bucket)
	if err != nil {
		if errors.Is(err, swadmin.ErrBucketNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return retainedStatements(stored)
}

// bucketPolicyContributor is one binding adding statements to a bucket's
// stored policy document, keyed for deterministic contributor ordering.
type bucketPolicyContributor struct {
	key string
	doc string
}

// bucketContributors collects the live bindings feeding a bucket's stored
// policy document: self (when selfDoc is given) plus the other claimants.
// Contributors sort by namespaced name so every binding assembles the same
// document. A peer whose policy is gone or unusable as a bucket policy —
// one its own reconcile would reject — contributes nothing and never
// blocks the others.
func (r *S3PolicyBindingReconciler) bucketContributors(ctx context.Context, admin IAMAdmin, self *seaweedv1.S3PolicyBinding, bucket, selfDoc string, policyDocs map[string]string) ([]bucketPolicyContributor, error) {
	cluster := seaweedRefKey(self.Spec.SeaweedRef, self.Namespace)
	var contributors []bucketPolicyContributor
	if selfDoc != "" {
		contributors = append(contributors, bucketPolicyContributor{
			key: self.Namespace + "/" + self.Name,
			doc: selfDoc,
		})
	}
	peers, err := bindingBucketClaimants(ctx, r.Client, self, bucket)
	if err != nil {
		return nil, err
	}
	for _, peer := range peers {
		if !peer.DeletionTimestamp.IsZero() {
			continue
		}
		// A peer whose cluster reference lost its grant contributes
		// nothing — its own reconcile is gated the same way, and a
		// revoked grant must not keep feeding statements into the merge.
		permitted, err := seaweedRefPermitted(ctx, r.Client, peer.Spec.SeaweedRef, kindS3PolicyBinding, peer.Namespace)
		if err != nil {
			return nil, err
		}
		if !permitted {
			continue
		}
		policyName := peer.Status.PolicyName
		if policyName == "" {
			policyName, err = resolvePolicyIAMName(ctx, r.Client, peer.Namespace, peer.Spec.PolicyRef.Name, cluster)
			if err != nil {
				return nil, err
			}
		}
		doc, cached := policyDocs[policyName]
		if !cached {
			doc, err = admin.GetPolicy(ctx, policyName)
			if err != nil {
				if errors.Is(err, ErrIAMNotFound) {
					continue
				}
				return nil, err
			}
			policyDocs[policyName] = doc
		}
		claimed, err := bindingClaimedBuckets(ctx, r.Client, peer, cluster)
		if err != nil {
			return nil, err
		}
		buckets := make([]string, 0, len(claimed))
		for b := range claimed {
			buckets = append(buckets, b)
		}
		if err := validateBucketPolicyDoc(doc, buckets); err != nil {
			continue
		}
		contributors = append(contributors, bucketPolicyContributor{
			key: peer.Namespace + "/" + peer.Name,
			doc: doc,
		})
	}
	slices.SortFunc(contributors, func(a, b bucketPolicyContributor) int {
		return strings.Compare(a.key, b.key)
	})
	return contributors, nil
}

// assembleBucketPolicy builds the document to store on a bucket from its
// contributors and the retained statements already present. A lone
// contributor with nothing retained keeps its document verbatim.
func assembleBucketPolicy(contributors []bucketPolicyContributor, retained []json.RawMessage) (string, error) {
	if len(contributors) == 1 && len(retained) == 0 {
		return contributors[0].doc, nil
	}
	docs := make([]string, 0, len(contributors))
	for _, c := range contributors {
		docs = append(docs, c.doc)
	}
	statements, err := mergedStatements(docs)
	if err != nil {
		return "", err
	}
	statements = append(statements, retained...)
	return assemblePolicyDocument(statements)
}

// mergedBucketPolicy assembles the policy document to store on bucket from
// every binding claiming it plus statements an earlier Retain deletion left
// marked in the stored document. The result must fit the bucket policy size
// limit — an oversized combination fails explicitly rather than silently
// truncating grants.
func (r *S3PolicyBindingReconciler) mergedBucketPolicy(ctx context.Context, admin IAMAdmin, self *seaweedv1.S3PolicyBinding, bucket, selfDoc string, policyDocs map[string]string) (string, error) {
	contributors, err := r.bucketContributors(ctx, admin, self, bucket, selfDoc, policyDocs)
	if err != nil {
		return "", err
	}
	retained, err := r.storedRetained(ctx, admin, bucket)
	if err != nil {
		return "", err
	}
	merged, err := assembleBucketPolicy(contributors, retained)
	if err != nil {
		return "", err
	}
	if len(merged) > s3policyengine.MaxBucketPolicySize {
		return "", fmt.Errorf("merged policy exceeds the %d byte bucket policy limit", s3policyengine.MaxBucketPolicySize)
	}
	return merged, nil
}

// mergeKeptBuckets unions the buckets attached this pass with the previously
// attached ones still desired, so a mid-apply failure does not drop cleanup
// records for policies still installed.
func mergeKeptBuckets(prev, attached []string, applySet map[string]struct{}) []string {
	kept := map[string]struct{}{}
	for _, b := range attached {
		kept[b] = struct{}{}
	}
	for _, b := range prev {
		if _, ok := applySet[b]; ok {
			kept[b] = struct{}{}
		}
	}
	out := make([]string, 0, len(kept))
	for b := range kept {
		out = append(out, b)
	}
	sort.Strings(out)
	return out
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

// mapBindingToClusterPeers enqueues the other bindings on the same cluster
// so a contributor joining, leaving, or changing its subjects rebuilds the
// shared bucket documents promptly instead of leaving a removed grant
// installed until the periodic resync.
func (r *S3PolicyBindingReconciler) mapBindingToClusterPeers(ctx context.Context, obj client.Object) []reconcile.Request {
	changed, ok := obj.(*seaweedv1.S3PolicyBinding)
	if !ok {
		return nil
	}
	var bindings seaweedv1.S3PolicyBindingList
	if err := r.List(ctx, &bindings); err != nil {
		return nil
	}
	cluster := seaweedRefKey(changed.Spec.SeaweedRef, changed.Namespace)
	var reqs []reconcile.Request
	for i := range bindings.Items {
		peer := &bindings.Items[i]
		if peer.Namespace == changed.Namespace && peer.Name == changed.Name {
			continue
		}
		if seaweedRefKey(peer.Spec.SeaweedRef, peer.Namespace) == cluster {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(peer)})
		}
	}
	return reqs
}

// SetupWithManager wires the reconciler into the manager.
func (r *S3PolicyBindingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.AdminFactory == nil {
		r.AdminFactory = NewSwadminIAMAdmin
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&seaweedv1.S3PolicyBinding{}).
		// Only spec/create/delete events feed peer merges — status-only
		// updates would ping-pong contributors off each other's records.
		Watches(&seaweedv1.S3PolicyBinding{}, handler.EnqueueRequestsFromMapFunc(r.mapBindingToClusterPeers),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}
