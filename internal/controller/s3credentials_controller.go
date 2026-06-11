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
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/internal/controller/swadmin"
)

const (
	// s3CredentialsManagedAnnotation marks a Secret the operator created (as
	// opposed to one the user pre-created). Only managed Secrets are deleted
	// when the owning S3Credentials is removed with reclaimPolicy: Delete.
	s3CredentialsManagedAnnotation = "s3credentials.seaweed.seaweedfs.com/managed"

	defaultAccessKeyField = "accessKey"
	defaultSecretKeyField = "secretKey"
)

// S3CredentialsReconciler reconciles S3Credentials resources into IAM access
// keys mirrored into a Kubernetes Secret.
type S3CredentialsReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	iamAdminProvider
}

// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=s3credentials,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=s3credentials/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=s3credentials/finalizers,verbs=update
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=s3identities,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

// Reconcile drives an S3Credentials so the identity owns the access key held
// in the referenced Secret, generating and storing one when needed.
func (r *S3CredentialsReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("s3credentials", req.NamespacedName)

	var cred seaweedv1.S3Credentials
	if err := r.Get(ctx, req.NamespacedName, &cred); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Deletion cleans up the key on the identity it was provisioned for, even
	// if the referenced S3Identity is already gone.
	var user string
	if !cred.DeletionTimestamp.IsZero() && cred.Status.IdentityName != "" {
		user = cred.Status.IdentityName
	} else {
		var err error
		user, err = resolveIdentityIAMName(ctx, r.Client, cred.Namespace, cred.Spec.IdentityRef.Name,
			seaweedRefKey(cred.Spec.SeaweedRef, cred.Namespace))
		if err != nil {
			return ctrl.Result{}, err
		}
	}
	secretName := cred.Spec.SecretRef.Name
	if secretName == "" {
		secretName = cred.Name
	}
	secretNamespace := cred.Spec.SecretRef.Namespace
	if secretNamespace == "" {
		secretNamespace = cred.Namespace
	}

	// Cross-namespace seaweedRef needs a grant; skip on deletion to not block
	// cleanup. The secretRef is gated separately in reconcileKey.
	if cred.DeletionTimestamp.IsZero() {
		permitted, err := seaweedRefPermitted(ctx, r.Client, cred.Spec.SeaweedRef, kindS3Credentials, cred.Namespace)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !permitted {
			return r.refForbidden(ctx, &cred, seaweedRefDeniedMessage(cred.Spec.SeaweedRef, kindS3Credentials, cred.Namespace))
		}
		// Clear any stale denial; the secretRef gate in reconcileKey re-sets it.
		clearIAMCondition(&cred.Status.Conditions, seaweedv1.S3ConditionReferenceGranted)
	}

	target, found, err := resolveSeaweedFiler(ctx, r.Client, cred.Spec.SeaweedRef, cred.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !found {
		return r.clusterNotFound(ctx, &cred)
	}
	setIAMCondition(&cred.Status.Conditions, cred.Generation, seaweedv1.S3ConditionClusterReachable, metav1.ConditionTrue, "Reachable", "")

	admin, err := r.getIAMAdmin(target, log)
	if err != nil {
		return ctrl.Result{}, err
	}

	if !cred.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &cred, user, secretName, secretNamespace, admin)
	}

	if !controllerutil.ContainsFinalizer(&cred, s3CredentialsFinalizer) {
		controllerutil.AddFinalizer(&cred, s3CredentialsFinalizer)
		if err := r.Update(ctx, &cred); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Wait for the identity to exist before provisioning a key for it.
	iamUser, err := admin.GetUser(ctx, user)
	if err != nil {
		if errors.Is(err, ErrIAMNotFound) {
			return r.pending(ctx, &cred, "IdentityMissing", fmt.Sprintf("identity %q does not exist yet", user))
		}
		return r.fail(ctx, &cred, "IdentityLookupFailed", err.Error())
	}

	return r.reconcileKey(ctx, &cred, user, secretName, secretNamespace, iamUser, admin)
}

// reconcileKey ensures the identity owns exactly the access key recorded in
// the Secret. It adopts a user-populated Secret, generates and stores a fresh
// key pair when the Secret is missing/incomplete, and removes a previously
// generated key it is replacing.
func (r *S3CredentialsReconciler) reconcileKey(ctx context.Context, cred *seaweedv1.S3Credentials, user, secretName, secretNamespace string, iamUser *swadmin.IAMUser, admin IAMAdmin) (ctrl.Result, error) {
	akField, skField := credentialFields(cred)

	crossNamespace := secretNamespace != cred.Namespace

	// Cross-namespace secretRef needs its own grant in the Secret's namespace.
	if crossNamespace {
		permitted, err := secretRefPermitted(ctx, r.Client, secretNamespace, secretName, cred.Namespace)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !permitted {
			return r.refForbidden(ctx, cred, secretRefDeniedMessage(secretNamespace, secretName, cred.Namespace))
		}
	}

	secret, secretFound, err := r.getSecret(ctx, secretNamespace, secretName)
	if err != nil {
		return ctrl.Result{}, err
	}

	// A cross-namespace Secret must already exist; the controller never creates
	// Secrets in foreign namespaces (owner references are namespace-scoped).
	if crossNamespace && !secretFound {
		return r.pending(ctx, cred, "SecretNotFound",
			fmt.Sprintf("cross-namespace Secret %s/%s does not exist", secretNamespace, secretName))
	}

	var existingAK, existingSK string
	if secretFound {
		existingAK = string(secret.Data[akField])
		existingSK = string(secret.Data[skField])
	}

	desiredAK, desiredSK := existingAK, existingSK
	if desiredAK == "" || desiredSK == "" {
		// Generate a fresh key pair to provision and store.
		desiredAK, desiredSK, err = swadmin.GenerateKeyPair()
		if err != nil {
			return r.fail(ctx, cred, "GenerateFailed", err.Error())
		}
	}

	// Register the key on the identity if it is not already present.
	if !containsString(iamUser.AccessKeys, desiredAK) {
		if err := admin.CreateAccessKey(ctx, user, desiredAK, desiredSK); err != nil {
			return r.fail(ctx, cred, "CreateAccessKeyFailed", err.Error())
		}
	}

	// Mirror the key pair into the Secret.
	if err := r.writeSecret(ctx, cred, secret, secretFound, secretName, secretNamespace, akField, skField, desiredAK, desiredSK); err != nil {
		return ctrl.Result{}, err
	}

	// Remove a previously generated key we just replaced.
	if cred.Status.AccessKey != "" && cred.Status.AccessKey != desiredAK {
		if err := admin.DeleteAccessKey(ctx, user, cred.Status.AccessKey); err != nil {
			return r.fail(ctx, cred, "RotateCleanupFailed", err.Error())
		}
	}

	cred.Status.AccessKey = desiredAK
	cred.Status.IdentityName = user
	if secretNamespace != cred.Namespace {
		cred.Status.SecretName = secretNamespace + "/" + secretName
	} else {
		cred.Status.SecretName = secretName
	}
	cred.Status.ObservedGeneration = cred.Generation
	cred.Status.Phase = seaweedv1.S3PhaseReady
	setIAMCondition(&cred.Status.Conditions, cred.Generation, seaweedv1.S3ConditionReady, metav1.ConditionTrue, "Reconciled", "")
	if err := r.Status().Update(ctx, cred); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// writeSecret creates or updates the Secret holding the key pair. A Secret the
// operator creates is annotated as managed so the finalizer can delete it; a
// pre-existing Secret is updated in place but never marked managed.
func (r *S3CredentialsReconciler) writeSecret(ctx context.Context, cred *seaweedv1.S3Credentials, secret *corev1.Secret, secretFound bool, secretName, secretNamespace, akField, skField, ak, sk string) error {
	if !secretFound {
		newSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:        secretName,
				Namespace:   secretNamespace,
				Annotations: map[string]string{s3CredentialsManagedAnnotation: "true"},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				akField: []byte(ak),
				skField: []byte(sk),
			},
		}
		if err := controllerutil.SetControllerReference(cred, newSecret, r.Scheme); err != nil {
			return err
		}
		return r.Create(ctx, newSecret)
	}

	if string(secret.Data[akField]) == ak && string(secret.Data[skField]) == sk {
		return nil
	}
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	secret.Data[akField] = []byte(ak)
	secret.Data[skField] = []byte(sk)
	return r.Update(ctx, secret)
}

func (r *S3CredentialsReconciler) handleDeletion(ctx context.Context, cred *seaweedv1.S3Credentials, user, secretName, secretNamespace string, admin IAMAdmin) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(cred, s3CredentialsFinalizer) {
		return ctrl.Result{}, nil
	}
	cred.Status.Phase = seaweedv1.S3PhaseTerminating

	if cred.Spec.ReclaimPolicy != seaweedv1.S3ReclaimRetain {
		if cred.Status.AccessKey != "" {
			// Idempotent: a key already gone must not block finalizer removal.
			if err := admin.DeleteAccessKey(ctx, user, cred.Status.AccessKey); err != nil && !errors.Is(err, ErrIAMNotFound) {
				setIAMCondition(&cred.Status.Conditions, cred.Generation, seaweedv1.S3ConditionReady, metav1.ConditionFalse, "DeleteAccessKeyFailed", err.Error())
				if updateErr := r.Status().Update(ctx, cred); updateErr != nil {
					r.Log.Error(updateErr, "status update during deletion")
				}
				return ctrl.Result{}, err
			}
		}
		// Never touch a Secret in a foreign namespace. The controller did not
		// create it there and another S3Credentials may legitimately own it.
		if secretNamespace == cred.Namespace {
			if err := r.deleteManagedSecret(ctx, secretNamespace, secretName); err != nil {
				return ctrl.Result{}, err
			}
		}
	} else {
		// Retain: an operator-created Secret carries a controller owner
		// reference, so removing the finalizer would let garbage collection
		// delete it along with the access key we are deliberately keeping.
		// Orphan it first so both the key and the Secret survive.
		if secretNamespace == cred.Namespace {
			if err := r.orphanManagedSecret(ctx, secretNamespace, secretName); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	controllerutil.RemoveFinalizer(cred, s3CredentialsFinalizer)
	if err := r.Update(ctx, cred); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// deleteManagedSecret removes the Secret only if the operator created it
// (carries the managed annotation). A user-managed Secret is left untouched.
func (r *S3CredentialsReconciler) deleteManagedSecret(ctx context.Context, namespace, name string) error {
	secret, found, err := r.getSecret(ctx, namespace, name)
	if err != nil || !found {
		return err
	}
	if secret.Annotations[s3CredentialsManagedAnnotation] != "true" {
		return nil
	}
	if err := r.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// orphanManagedSecret strips the controller owner reference from an
// operator-created Secret so it is not garbage-collected when the
// S3Credentials CR is deleted under reclaimPolicy: Retain. A user-managed
// Secret (no managed annotation) is left untouched.
func (r *S3CredentialsReconciler) orphanManagedSecret(ctx context.Context, namespace, name string) error {
	secret, found, err := r.getSecret(ctx, namespace, name)
	if err != nil || !found {
		return err
	}
	if secret.Annotations[s3CredentialsManagedAnnotation] != "true" || len(secret.OwnerReferences) == 0 {
		return nil
	}
	secret.OwnerReferences = nil
	return r.Update(ctx, secret)
}

func (r *S3CredentialsReconciler) getSecret(ctx context.Context, namespace, name string) (*corev1.Secret, bool, error) {
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &secret, true, nil
}

// refForbidden requeues (not Failed) until a ResourceReferenceGrant permits the
// reference.
func (r *S3CredentialsReconciler) refForbidden(ctx context.Context, cred *seaweedv1.S3Credentials, message string) (ctrl.Result, error) {
	setIAMCondition(&cred.Status.Conditions, cred.Generation, seaweedv1.S3ConditionReferenceGranted, metav1.ConditionFalse, "ReferenceGrantMissing", message)
	cred.Status.Phase = seaweedv1.S3PhasePending
	if err := r.Status().Update(ctx, cred); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
}

func (r *S3CredentialsReconciler) clusterNotFound(ctx context.Context, cred *seaweedv1.S3Credentials) (ctrl.Result, error) {
	setIAMCondition(&cred.Status.Conditions, cred.Generation, seaweedv1.S3ConditionClusterReachable, metav1.ConditionFalse, "ClusterRefNotFound",
		fmt.Sprintf("Seaweed %q not found", cred.Spec.SeaweedRef.Name))
	cred.Status.Phase = seaweedv1.S3PhasePending
	if err := r.Status().Update(ctx, cred); err != nil {
		r.Log.Error(err, "status update")
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
}

func (r *S3CredentialsReconciler) pending(ctx context.Context, cred *seaweedv1.S3Credentials, reason, message string) (ctrl.Result, error) {
	cred.Status.Phase = seaweedv1.S3PhasePending
	setIAMCondition(&cred.Status.Conditions, cred.Generation, seaweedv1.S3ConditionReady, metav1.ConditionFalse, reason, message)
	if err := r.Status().Update(ctx, cred); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
}

func (r *S3CredentialsReconciler) fail(ctx context.Context, cred *seaweedv1.S3Credentials, reason, message string) (ctrl.Result, error) {
	r.Log.Info("s3credentials reconcile failed", "reason", reason, "message", message)
	cred.Status.Phase = seaweedv1.S3PhaseFailed
	setIAMCondition(&cred.Status.Conditions, cred.Generation, seaweedv1.S3ConditionReady, metav1.ConditionFalse, reason, message)
	if err := r.Status().Update(ctx, cred); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
}

// credentialFields resolves the Secret data keys, applying the CRD defaults
// when unset (so the controller is correct even if defaulting did not run).
func credentialFields(cred *seaweedv1.S3Credentials) (akField, skField string) {
	akField = cred.Spec.SecretRef.AccessKeyField
	if akField == "" {
		akField = defaultAccessKeyField
	}
	skField = cred.Spec.SecretRef.SecretKeyField
	if skField == "" {
		skField = defaultSecretKeyField
	}
	return akField, skField
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// SetupWithManager wires the reconciler into the manager.
func (r *S3CredentialsReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.AdminFactory == nil {
		r.AdminFactory = NewSwadminIAMAdmin
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&seaweedv1.S3Credentials{}).
		Owns(&corev1.Secret{}).
		Complete(r)
}
