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
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	"github.com/seaweedfs/seaweedfs-operator/internal/controller/swadmin"
)

// S3RoleReconciler reconciles S3Role resources into STS roles on the target
// Seaweed cluster's IAM service.
type S3RoleReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	iamAdminProvider
}

// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=s3roles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=s3roles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=s3roles/finalizers,verbs=update
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=s3oidcproviders,verbs=get;list;watch
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=s3policies,verbs=get;list;watch

// Reconcile drives an S3Role to match its spec: put/delete the STS role whose
// trust policy admits the listed subjects of the referenced OIDC provider.
func (r *S3RoleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("s3role", req.NamespacedName)

	var role seaweedv1.S3Role
	if err := r.Get(ctx, req.NamespacedName, &role); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	name := roleIAMName(&role)
	clusterKey := seaweedRefKey(role.Spec.SeaweedRef, role.Namespace)

	// Cross-namespace seaweedRef needs a grant; skip on deletion to not block cleanup.
	if role.DeletionTimestamp.IsZero() {
		permitted, err := seaweedRefPermitted(ctx, r.Client, role.Spec.SeaweedRef, kindS3Role, role.Namespace)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !permitted {
			if err := r.withdrawIfChanged(ctx, &role, log); err != nil {
				return ctrl.Result{}, err
			}
			return r.refForbidden(ctx, &role, seaweedRefDeniedMessage(role.Spec.SeaweedRef, kindS3Role, role.Namespace))
		}
		clearIAMCondition(&role.Status.Conditions, seaweedv1.S3ConditionReferenceGranted)

		// Role names are global per cluster: the oldest CR claiming a name
		// owns it, later claimants conflict instead of co-managing it.
		owner, err := roleConflict(ctx, r.Client, &role, name)
		if err != nil {
			return ctrl.Result{}, err
		}
		if owner != nil {
			if role.Status.RoleName == name {
				// Lost a tie with another CR that also recorded the name: give
				// it up without deleting the role the winner now writes.
				role.Status.RoleName, role.Status.RoleArn, role.Status.AttachedPolicies = "", "", nil
			} else if err := r.withdrawIfChanged(ctx, &role, log); err != nil {
				// A rename onto a held name is a change that cannot be applied.
				return ctrl.Result{}, err
			}
			return r.fail(ctx, &role, "Conflict",
				fmt.Sprintf("IAM role %q on Seaweed %s is already managed by S3Role %s/%s", name, clusterKey, owner.Namespace, owner.Name))
		}
	}

	target, found, err := resolveSeaweedFiler(ctx, r.Client, role.Spec.SeaweedRef, role.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !found {
		if handled, err := releaseFinalizerIfDeleting(ctx, r.Client, r.Recorder, &role, s3RoleFinalizer, clusterKey); handled {
			return ctrl.Result{}, err
		}
		return r.clusterNotFound(ctx, &role)
	}
	setIAMCondition(&role.Status.Conditions, role.Generation, seaweedv1.S3ConditionClusterReachable, metav1.ConditionTrue, "Reachable", "")

	// Refuse renames once provisioned, after missing-cluster cleanup so an
	// invalid rename cannot trap the finalizer.
	admin, err := r.getIAMAdmin(target, log)
	if err != nil {
		return ctrl.Result{}, err
	}

	if role.DeletionTimestamp.IsZero() && role.Status.RoleName != "" && role.Status.RoleName != name {
		// The rest of the edit may narrow the role; it cannot be applied.
		message := fmt.Sprintf("role name change from %q to %q is not supported; restore the original name or recreate the resource", role.Status.RoleName, name)
		if withdrawn, err := r.withdraw(ctx, &role, admin, role.Status.RoleName); err != nil {
			return r.fail(ctx, &role, "DeleteFailed", err.Error())
		} else if withdrawn {
			message += "; the role is withdrawn until it can be applied in full"
		}
		return r.fail(ctx, &role, "RoleRenameNotSupported", message)
	}

	if !role.DeletionTimestamp.IsZero() {
		// Delete the role this CR provisioned, not one a refused rename names.
		return r.handleDeletion(ctx, &role, provisionedName(role.Status.RoleName, name), admin)
	}

	if !controllerutil.ContainsFinalizer(&role, s3RoleFinalizer) {
		controllerutil.AddFinalizer(&role, s3RoleFinalizer)
		if err := r.Update(ctx, &role); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	issuer, reason, message, err := r.resolveIssuer(ctx, &role, clusterKey)
	if err != nil {
		return ctrl.Result{}, err
	}
	if issuer == "" {
		return r.waitFor(ctx, &role, admin, name, reason, message)
	}

	policies := make([]string, 0, len(role.Spec.PolicyRefs))
	for _, ref := range role.Spec.PolicyRefs {
		policyName, err := resolvePolicyIAMName(ctx, r.Client, role.Namespace, ref.Name, clusterKey)
		if err != nil {
			return ctrl.Result{}, err
		}
		if _, err := admin.GetPolicy(ctx, policyName); err != nil {
			if errors.Is(err, ErrIAMNotFound) {
				return r.waitFor(ctx, &role, admin, name, "PolicyMissing", fmt.Sprintf("policy %q does not exist yet", policyName))
			}
			return r.fail(ctx, &role, "PolicyLookupFailed", err.Error())
		}
		policies = append(policies, policyName)
	}

	trust, err := buildWebIdentityTrustPolicy(issuer, role.Spec.WebIdentity.Subjects)
	if err != nil {
		return r.fail(ctx, &role, "InvalidTrustPolicy", err.Error())
	}
	// Record the name before PutRole: if the status write after it failed and
	// the CR were deleted, an empty status.roleName would skip DeleteRole and
	// leave the role behind. DeleteRole tolerates a role PutRole never made.
	if role.Status.RoleName == "" {
		role.Status.RoleName = name
		if err := r.Status().Update(ctx, &role); err != nil {
			return ctrl.Result{}, err
		}
	}
	arn, err := admin.PutRole(ctx, swadmin.IAMRole{
		Name:               name,
		TrustPolicy:        trust,
		AttachedPolicies:   policies,
		Description:        role.Spec.Description,
		MaxSessionDuration: role.Spec.MaxSessionDuration,
	})
	if err != nil {
		return r.fail(ctx, &role, "PutFailed", err.Error())
	}
	log.Info("applied IAM role", "name", name, "arn", arn)

	role.Status.RoleArn = arn
	role.Status.AttachedPolicies = policies
	role.Status.ObservedGeneration = role.Generation
	role.Status.Phase = seaweedv1.S3PhaseReady
	setIAMCondition(&role.Status.Conditions, role.Generation, seaweedv1.S3ConditionReady, metav1.ConditionTrue, "Reconciled", "")
	if err := r.Status().Update(ctx, &role); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: iamResyncInterval}, nil
}

// waitFor holds a role whose spec cannot be applied yet because its provider
// or a policy is not ready. A role never provisioned just waits. A provisioned
// role whose spec changed since it was last applied is deleted until the new
// spec can be applied in full: applying part of it could widen access (a new
// subject or issuer without a policy that denies) or keep a removed grant (a
// dropped subject, issuer or policy), so a spec that cannot be applied admits
// no one. A dependency lost without a spec change leaves the role alone; it
// still matches its spec.
func (r *S3RoleReconciler) waitFor(ctx context.Context, role *seaweedv1.S3Role, admin IAMAdmin, name, reason, message string) (ctrl.Result, error) {
	withdrawn, err := r.withdraw(ctx, role, admin, name)
	if err != nil {
		return r.fail(ctx, role, "DeleteFailed", err.Error())
	}
	if withdrawn {
		message += "; the role is withdrawn until its changed spec can be applied in full"
	}
	return r.pending(ctx, role, reason, message)
}

// withdraw deletes the role this CR provisioned, under name, when its spec has
// changed since it was last applied (see waitFor). It reports whether it did.
func (r *S3RoleReconciler) withdraw(ctx context.Context, role *seaweedv1.S3Role, admin IAMAdmin, name string) (bool, error) {
	if role.Status.RoleName == "" || role.Status.ObservedGeneration == role.Generation {
		return false, nil
	}
	if err := admin.DeleteRole(ctx, name); err != nil {
		return false, err
	}
	if role.Status.RoleArn != "" {
		r.Log.Info("withdrew IAM role until its changed spec can be applied", "name", name)
	}
	role.Status.RoleArn = ""
	role.Status.AttachedPolicies = nil
	return true, nil
}

// withdrawIfChanged is withdraw for the paths that return before an IAM
// client exists. A cluster that cannot be resolved has no role to withdraw.
func (r *S3RoleReconciler) withdrawIfChanged(ctx context.Context, role *seaweedv1.S3Role, log logr.Logger) error {
	if role.Status.RoleName == "" || role.Status.ObservedGeneration == role.Generation {
		return nil
	}
	target, found, err := resolveSeaweedFiler(ctx, r.Client, role.Spec.SeaweedRef, role.Namespace)
	if err != nil || !found {
		return err
	}
	admin, err := r.getIAMAdmin(target, log)
	if err != nil {
		return err
	}
	_, err = r.withdraw(ctx, role, admin, role.Status.RoleName)
	return err
}

// resolveIssuer returns the issuer URL of the role's S3OIDCProvider once that
// provider is registered on the same cluster. While it is not, issuer is empty
// and reason/message say why.
func (r *S3RoleReconciler) resolveIssuer(ctx context.Context, role *seaweedv1.S3Role, clusterKey string) (issuer, reason, message string, err error) {
	ref := role.Spec.WebIdentity.ProviderRef.Name
	var provider seaweedv1.S3OIDCProvider
	err = r.Get(ctx, types.NamespacedName{Namespace: role.Namespace, Name: ref}, &provider)
	switch {
	case apierrors.IsNotFound(err):
		return "", "ProviderMissing", fmt.Sprintf("S3OIDCProvider %q does not exist", ref), nil
	case err != nil:
		return "", "", "", err
	}
	if seaweedRefKey(provider.Spec.SeaweedRef, provider.Namespace) != clusterKey {
		return "", "ProviderClusterMismatch", fmt.Sprintf("S3OIDCProvider %q targets Seaweed %s, not %s",
			ref, seaweedRefKey(provider.Spec.SeaweedRef, provider.Namespace), clusterKey), nil
	}
	if provider.Status.Phase != seaweedv1.S3PhaseReady || provider.Status.ProviderArn == "" {
		return "", "ProviderNotReady", fmt.Sprintf("S3OIDCProvider %q is not registered yet", ref), nil
	}
	return provider.Spec.IssuerURL, "", "", nil
}

// buildWebIdentityTrustPolicy renders the trust policy SeaweedFS STS evaluates
// for AssumeRoleWithWebIdentity. Two SeaweedFS specifics differ from AWS: a
// provider registered through the IAM service is matched by its issuer URL,
// not its ARN, and the subject condition key is "oidc:sub", not
// "<issuer-host>:sub".
func buildWebIdentityTrustPolicy(issuer string, subjects []string) (string, error) {
	if issuer == "" || len(subjects) == 0 {
		return "", fmt.Errorf("a trust policy needs an issuer and at least one subject")
	}
	sorted := slices.Clone(subjects)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	doc := map[string]any{
		"Version": "2012-10-17",
		"Statement": []any{map[string]any{
			"Effect":    "Allow",
			"Principal": map[string]any{"Federated": issuer},
			"Action":    []string{"sts:AssumeRoleWithWebIdentity"},
			"Condition": map[string]any{"StringEquals": map[string]any{"oidc:sub": sorted}},
		}},
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (r *S3RoleReconciler) handleDeletion(ctx context.Context, role *seaweedv1.S3Role, name string, admin IAMAdmin) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(role, s3RoleFinalizer) {
		return ctrl.Result{}, nil
	}
	role.Status.Phase = seaweedv1.S3PhaseTerminating

	// The role is deleted even when another S3Role claims its name. Handing it
	// over live would keep this CR's subjects and policies granted until the
	// claimant applies its own spec, and forever if the claimant never can; the
	// claimant recreates the role from its own spec instead (mapToNameClaimants
	// requeues it).
	if role.Spec.ReclaimPolicy != seaweedv1.S3ReclaimRetain && role.Status.RoleName != "" {
		if err := admin.DeleteRole(ctx, name); err != nil && !errors.Is(err, ErrIAMNotFound) {
			setIAMCondition(&role.Status.Conditions, role.Generation, seaweedv1.S3ConditionReady, metav1.ConditionFalse, "DeleteFailed", err.Error())
			if updateErr := r.Status().Update(ctx, role); updateErr != nil {
				r.Log.Error(updateErr, "status update during deletion")
			}
			return ctrl.Result{}, err
		}
	}

	controllerutil.RemoveFinalizer(role, s3RoleFinalizer)
	if err := r.Update(ctx, role); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// pending requeues (not Failed) while a dependency is not ready yet.
func (r *S3RoleReconciler) pending(ctx context.Context, role *seaweedv1.S3Role, reason, message string) (ctrl.Result, error) {
	role.Status.Phase = seaweedv1.S3PhasePending
	setIAMCondition(&role.Status.Conditions, role.Generation, seaweedv1.S3ConditionReady, metav1.ConditionFalse, reason, message)
	if err := r.Status().Update(ctx, role); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
}

// refForbidden requeues (not Failed) until a ResourceReferenceGrant permits the
// reference.
func (r *S3RoleReconciler) refForbidden(ctx context.Context, role *seaweedv1.S3Role, message string) (ctrl.Result, error) {
	setIAMCondition(&role.Status.Conditions, role.Generation, seaweedv1.S3ConditionReferenceGranted, metav1.ConditionFalse, "ReferenceGrantMissing", message)
	role.Status.Phase = seaweedv1.S3PhasePending
	if err := r.Status().Update(ctx, role); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
}

func (r *S3RoleReconciler) clusterNotFound(ctx context.Context, role *seaweedv1.S3Role) (ctrl.Result, error) {
	setIAMCondition(&role.Status.Conditions, role.Generation, seaweedv1.S3ConditionClusterReachable, metav1.ConditionFalse, "ClusterRefNotFound",
		fmt.Sprintf("Seaweed %q not found", role.Spec.SeaweedRef.Name))
	role.Status.Phase = seaweedv1.S3PhasePending
	if err := r.Status().Update(ctx, role); err != nil {
		r.Log.Error(err, "status update")
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
}

func (r *S3RoleReconciler) fail(ctx context.Context, role *seaweedv1.S3Role, reason, message string) (ctrl.Result, error) {
	r.Log.Info("s3role reconcile failed", "reason", reason, "message", message)
	role.Status.Phase = seaweedv1.S3PhaseFailed
	setIAMCondition(&role.Status.Conditions, role.Generation, seaweedv1.S3ConditionReady, metav1.ConditionFalse, reason, message)
	if err := r.Status().Update(ctx, role); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
}

// mapToNameClaimants enqueues the other claimants of a role's IAM name so a
// conflicted CR is promoted as soon as the owner changes or goes away.
func (r *S3RoleReconciler) mapToNameClaimants(ctx context.Context, obj client.Object) []reconcile.Request {
	role, ok := obj.(*seaweedv1.S3Role)
	if !ok {
		return nil
	}
	// The name it records may differ from its spec's after a refused rename;
	// waiters on either are promoted when it changes or goes away.
	names := []string{roleIAMName(role)}
	if role.Status.RoleName != "" && role.Status.RoleName != names[0] {
		names = append(names, role.Status.RoleName)
	}
	var reqs []reconcile.Request
	for _, name := range names {
		peers, err := roleClaimants(ctx, r.Client, role, name)
		if err != nil {
			return nil
		}
		for _, peer := range peers {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(peer)})
		}
	}
	return reqs
}

// mapProviderToRoles enqueues the roles that trust a provider, so a role
// follows its provider becoming Ready without waiting for its requeue.
func (r *S3RoleReconciler) mapProviderToRoles(ctx context.Context, obj client.Object) []reconcile.Request {
	var roles seaweedv1.S3RoleList
	if err := r.List(ctx, &roles, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range roles.Items {
		if roles.Items[i].Spec.WebIdentity.ProviderRef.Name == obj.GetName() {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&roles.Items[i])})
		}
	}
	return reqs
}

// SetupWithManager wires the reconciler into the manager.
func (r *S3RoleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.AdminFactory == nil {
		r.AdminFactory = NewSwadminIAMAdmin
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&seaweedv1.S3Role{}).
		Watches(&seaweedv1.S3Role{}, handler.EnqueueRequestsFromMapFunc(r.mapToNameClaimants)).
		Watches(&seaweedv1.S3OIDCProvider{}, handler.EnqueueRequestsFromMapFunc(r.mapProviderToRoles)).
		Complete(r)
}
