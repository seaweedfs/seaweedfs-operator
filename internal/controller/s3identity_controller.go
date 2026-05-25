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

// S3IdentityReconciler reconciles S3Identity resources into IAM users on the
// target Seaweed cluster.
type S3IdentityReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	iamAdminProvider
}

// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=s3identities,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=s3identities/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=s3identities/finalizers,verbs=update

// Reconcile drives an S3Identity to match its spec: create/update/delete the
// underlying IAM user.
func (r *S3IdentityReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("s3identity", req.NamespacedName)

	var identity seaweedv1.S3Identity
	if err := r.Get(ctx, req.NamespacedName, &identity); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	name := identity.Spec.Name
	if name == "" {
		name = identity.Name
	}

	// Refuse rename attempts once provisioned.
	if identity.Status.IdentityName != "" && identity.Status.IdentityName != name {
		return r.fail(ctx, &identity, "IdentityRenameNotSupported",
			fmt.Sprintf("identity name change from %q to %q is not supported; restore the original name or recreate the resource",
				identity.Status.IdentityName, name))
	}

	filer, found, err := resolveSeaweedFiler(ctx, r.Client, identity.Spec.SeaweedRef, identity.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !found {
		return r.clusterNotFound(ctx, &identity)
	}
	setIAMCondition(&identity.Status.Conditions, identity.Generation, seaweedv1.S3ConditionClusterReachable, metav1.ConditionTrue, "Reachable", "")

	admin, err := r.getIAMAdmin(filer, log)
	if err != nil {
		return ctrl.Result{}, err
	}

	if !identity.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &identity, name, admin)
	}

	if !controllerutil.ContainsFinalizer(&identity, s3IdentityFinalizer) {
		controllerutil.AddFinalizer(&identity, s3IdentityFinalizer)
		if err := r.Update(ctx, &identity); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	displayName, email := "", ""
	if identity.Spec.Account != nil {
		displayName = identity.Spec.Account.DisplayName
		email = identity.Spec.Account.Email
	}

	user, err := admin.GetUser(ctx, name)
	switch {
	case errors.Is(err, ErrIAMNotFound):
		if err := admin.CreateUser(ctx, name, displayName, email, identity.Spec.Disabled); err != nil {
			return r.fail(ctx, &identity, "CreateFailed", err.Error())
		}
		log.Info("created IAM user", "name", name)
	case err != nil:
		return r.fail(ctx, &identity, "LookupFailed", err.Error())
	default:
		// Exists: reconcile the managed fields if they drift.
		if userStateDiffers(user, displayName, email, identity.Spec.Disabled) {
			if err := admin.SetUserState(ctx, name, displayName, email, identity.Spec.Disabled); err != nil {
				return r.fail(ctx, &identity, "UpdateFailed", err.Error())
			}
			log.Info("updated IAM user state", "name", name)
		}
	}

	identity.Status.IdentityName = name
	identity.Status.ObservedGeneration = identity.Generation
	identity.Status.Phase = seaweedv1.S3PhaseReady
	setIAMCondition(&identity.Status.Conditions, identity.Generation, seaweedv1.S3ConditionReady, metav1.ConditionTrue, "Reconciled", "")
	if err := r.Status().Update(ctx, &identity); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *S3IdentityReconciler) handleDeletion(ctx context.Context, identity *seaweedv1.S3Identity, name string, admin IAMAdmin) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(identity, s3IdentityFinalizer) {
		return ctrl.Result{}, nil
	}
	identity.Status.Phase = seaweedv1.S3PhaseTerminating

	if identity.Spec.ReclaimPolicy != seaweedv1.S3ReclaimRetain {
		if err := admin.DeleteUser(ctx, name); err != nil {
			setIAMCondition(&identity.Status.Conditions, identity.Generation, seaweedv1.S3ConditionReady, metav1.ConditionFalse, "DeleteFailed", err.Error())
			if updateErr := r.Status().Update(ctx, identity); updateErr != nil {
				r.Log.Error(updateErr, "status update during deletion")
			}
			return ctrl.Result{}, err
		}
	}

	controllerutil.RemoveFinalizer(identity, s3IdentityFinalizer)
	if err := r.Update(ctx, identity); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *S3IdentityReconciler) clusterNotFound(ctx context.Context, identity *seaweedv1.S3Identity) (ctrl.Result, error) {
	setIAMCondition(&identity.Status.Conditions, identity.Generation, seaweedv1.S3ConditionClusterReachable, metav1.ConditionFalse, "ClusterRefNotFound",
		fmt.Sprintf("Seaweed %q not found", identity.Spec.SeaweedRef.Name))
	identity.Status.Phase = seaweedv1.S3PhasePending
	if err := r.Status().Update(ctx, identity); err != nil {
		r.Log.Error(err, "status update")
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
}

func (r *S3IdentityReconciler) fail(ctx context.Context, identity *seaweedv1.S3Identity, reason, message string) (ctrl.Result, error) {
	r.Log.Info("s3identity reconcile failed", "reason", reason, "message", message)
	identity.Status.Phase = seaweedv1.S3PhaseFailed
	setIAMCondition(&identity.Status.Conditions, identity.Generation, seaweedv1.S3ConditionReady, metav1.ConditionFalse, reason, message)
	if err := r.Status().Update(ctx, identity); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
}

// userStateDiffers reports whether the IAM user's managed fields (account and
// disabled flag) diverge from the desired spec.
func userStateDiffers(user *swadmin.IAMUser, displayName, email string, disabled bool) bool {
	if user == nil {
		return true
	}
	return user.DisplayName != displayName || user.Email != email || user.Disabled != disabled
}

// SetupWithManager wires the reconciler into the manager.
func (r *S3IdentityReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.AdminFactory == nil {
		r.AdminFactory = NewSwadminIAMAdmin
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&seaweedv1.S3Identity{}).
		Complete(r)
}
