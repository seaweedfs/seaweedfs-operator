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
	"github.com/seaweedfs/seaweedfs-operator/internal/controller/swadmin"
)

// S3OIDCProviderReconciler reconciles S3OIDCProvider resources into trusted
// OIDC identity providers on the target Seaweed cluster's IAM service.
type S3OIDCProviderReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	iamAdminProvider
}

// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=s3oidcproviders,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=s3oidcproviders/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=s3oidcproviders/finalizers,verbs=update

// Reconcile drives an S3OIDCProvider to match its spec: register/update or
// delete the trusted OIDC provider keyed by issuer URL.
func (r *S3OIDCProviderReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("s3oidcprovider", req.NamespacedName)

	var provider seaweedv1.S3OIDCProvider
	if err := r.Get(ctx, req.NamespacedName, &provider); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	issuer := provider.Spec.IssuerURL

	// Cross-namespace seaweedRef needs a grant; skip on deletion to not block cleanup.
	if provider.DeletionTimestamp.IsZero() {
		permitted, err := seaweedRefPermitted(ctx, r.Client, provider.Spec.SeaweedRef, kindS3OIDCProvider, provider.Namespace)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !permitted {
			return r.refForbidden(ctx, &provider, seaweedRefDeniedMessage(provider.Spec.SeaweedRef, kindS3OIDCProvider, provider.Namespace))
		}
		clearIAMCondition(&provider.Status.Conditions, seaweedv1.S3ConditionReferenceGranted)
	}

	target, found, err := resolveSeaweedFiler(ctx, r.Client, provider.Spec.SeaweedRef, provider.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !found {
		// Cluster already gone: nothing to clean up from its IAM service, so let
		// deletion proceed instead of requeuing forever on a missing reference.
		if !provider.DeletionTimestamp.IsZero() {
			controllerutil.RemoveFinalizer(&provider, s3OIDCProviderFinalizer)
			if err := r.Update(ctx, &provider); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		return r.clusterNotFound(ctx, &provider)
	}
	setIAMCondition(&provider.Status.Conditions, provider.Generation, seaweedv1.S3ConditionClusterReachable, metav1.ConditionTrue, "Reachable", "")

	admin, err := r.getIAMAdmin(target, log)
	if err != nil {
		return ctrl.Result{}, err
	}

	if !provider.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &provider, issuer, admin)
	}

	if !controllerutil.ContainsFinalizer(&provider, s3OIDCProviderFinalizer) {
		controllerutil.AddFinalizer(&provider, s3OIDCProviderFinalizer)
		if err := r.Update(ctx, &provider); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	arn, err := admin.PutOIDCProvider(ctx, swadmin.OIDCProvider{
		IssuerURL:   issuer,
		ClientIDs:   provider.Spec.ClientIDs,
		Thumbprints: provider.Spec.Thumbprints,
	})
	if err != nil {
		return r.fail(ctx, &provider, "PutFailed", err.Error())
	}
	log.Info("registered OIDC provider", "issuer", issuer, "arn", arn)

	provider.Status.ProviderArn = arn
	provider.Status.ObservedGeneration = provider.Generation
	provider.Status.Phase = seaweedv1.S3PhaseReady
	setIAMCondition(&provider.Status.Conditions, provider.Generation, seaweedv1.S3ConditionReady, metav1.ConditionTrue, "Reconciled", "")
	if err := r.Status().Update(ctx, &provider); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *S3OIDCProviderReconciler) handleDeletion(ctx context.Context, provider *seaweedv1.S3OIDCProvider, issuer string, admin IAMAdmin) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(provider, s3OIDCProviderFinalizer) {
		return ctrl.Result{}, nil
	}
	provider.Status.Phase = seaweedv1.S3PhaseTerminating

	// Only delete if we actually registered one (ProviderArn recorded). Skipping
	// when it was never created keeps a failed/unregistered CR from deadlocking
	// on the delete call.
	if provider.Spec.ReclaimPolicy != seaweedv1.S3ReclaimRetain && provider.Status.ProviderArn != "" {
		// Idempotent: a provider already gone must not block finalizer removal.
		if err := admin.DeleteOIDCProvider(ctx, issuer); err != nil && !errors.Is(err, ErrIAMNotFound) {
			setIAMCondition(&provider.Status.Conditions, provider.Generation, seaweedv1.S3ConditionReady, metav1.ConditionFalse, "DeleteFailed", err.Error())
			if updateErr := r.Status().Update(ctx, provider); updateErr != nil {
				r.Log.Error(updateErr, "status update during deletion")
			}
			return ctrl.Result{}, err
		}
	}

	controllerutil.RemoveFinalizer(provider, s3OIDCProviderFinalizer)
	if err := r.Update(ctx, provider); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// refForbidden requeues (not Failed) until a ResourceReferenceGrant permits the
// reference.
func (r *S3OIDCProviderReconciler) refForbidden(ctx context.Context, provider *seaweedv1.S3OIDCProvider, message string) (ctrl.Result, error) {
	setIAMCondition(&provider.Status.Conditions, provider.Generation, seaweedv1.S3ConditionReferenceGranted, metav1.ConditionFalse, "ReferenceGrantMissing", message)
	provider.Status.Phase = seaweedv1.S3PhasePending
	if err := r.Status().Update(ctx, provider); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
}

func (r *S3OIDCProviderReconciler) clusterNotFound(ctx context.Context, provider *seaweedv1.S3OIDCProvider) (ctrl.Result, error) {
	setIAMCondition(&provider.Status.Conditions, provider.Generation, seaweedv1.S3ConditionClusterReachable, metav1.ConditionFalse, "ClusterRefNotFound",
		fmt.Sprintf("Seaweed %q not found", provider.Spec.SeaweedRef.Name))
	provider.Status.Phase = seaweedv1.S3PhasePending
	if err := r.Status().Update(ctx, provider); err != nil {
		r.Log.Error(err, "status update")
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
}

func (r *S3OIDCProviderReconciler) fail(ctx context.Context, provider *seaweedv1.S3OIDCProvider, reason, message string) (ctrl.Result, error) {
	r.Log.Info("s3oidcprovider reconcile failed", "reason", reason, "message", message)
	provider.Status.Phase = seaweedv1.S3PhaseFailed
	setIAMCondition(&provider.Status.Conditions, provider.Generation, seaweedv1.S3ConditionReady, metav1.ConditionFalse, reason, message)
	if err := r.Status().Update(ctx, provider); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
}

// SetupWithManager wires the reconciler into the manager.
func (r *S3OIDCProviderReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.AdminFactory == nil {
		r.AdminFactory = NewSwadminIAMAdmin
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&seaweedv1.S3OIDCProvider{}).
		Complete(r)
}
