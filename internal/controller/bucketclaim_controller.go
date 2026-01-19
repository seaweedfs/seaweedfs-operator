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
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/internal/admin"
)

// adminServerEntry tracks an admin server with its last access time
type adminServerEntry struct {
	server     *admin.AdminServer
	lastAccess time.Time
}

// BucketClaimReconciler reconciles a BucketClaim object
type BucketClaimReconciler struct {
	client.Client
	Log    *zap.SugaredLogger
	Scheme *runtime.Scheme

	adminServers  map[string]*adminServerEntry
	adminMutex    sync.RWMutex
	cleanupTicker *time.Ticker
	stopCleanup   chan struct{}
}

// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=bucketclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=bucketclaims/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=bucketclaims/finalizers,verbs=update
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=seaweeds,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete

const bucketClaimFinalizer = "bucketclaim.seaweed.seaweedfs.com/finalizer"

//#region Reconcile

// Reconcile implements the reconciliation logic for BucketClaim
func (r *BucketClaimReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.With("bucketclaim", req.NamespacedName)

	log.Debugw("starting bucketclaim Reconcile", "bucketclaim", req.NamespacedName)

	// Fetch the BucketClaim instance
	bucketClaim := &seaweedv1.BucketClaim{}
	err := r.Get(ctx, req.NamespacedName, bucketClaim)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			log.Debug("bucketclaim not found. ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}

		// Error reading the object - requeue the request.
		log.Errorw("failed to get BucketClaim", "error", err)
		return ctrl.Result{}, err
	}

	// Check if the BucketClaim is being deleted
	if !bucketClaim.DeletionTimestamp.IsZero() {
		// The object is being deleted
		if containsString(bucketClaim.GetFinalizers(), bucketClaimFinalizer) {
			// Our finalizer is present, so lets handle cleanup
			log.Debug("bucketclaim is being deleted, running finalizer cleanup")
			if err := r.handleDeletion(ctx, bucketClaim); err != nil {
				// If deletion fails, return error so we can retry
				log.Errorw("failed to handle deletion", "error", err)
				return ctrl.Result{}, err
			}

			// Remove our finalizer from the list and update it.
			bucketClaim.SetFinalizers(removeString(bucketClaim.GetFinalizers(), bucketClaimFinalizer))
			if err := r.Update(ctx, bucketClaim); err != nil {
				log.Errorw("failed to remove finalizer", "error", err)
				return ctrl.Result{}, err
			}
			log.Debug("finalizer removed, bucketclaim cleanup completed")
		}
		// Stop reconciliation as the item is being deleted
		return ctrl.Result{}, nil
	}

	// Add finalizer if it doesn't exist
	if !containsString(bucketClaim.GetFinalizers(), bucketClaimFinalizer) {
		bucketClaim.SetFinalizers(append(bucketClaim.GetFinalizers(), bucketClaimFinalizer))
		if err := r.Update(ctx, bucketClaim); err != nil {
			log.Errorw("failed to add finalizer", "error", err)
			return ctrl.Result{}, err
		}
		log.Debug("finalizer added to bucketclaim")
	}

	// Handle bucket creation/update
	return r.handleReconciliation(ctx, bucketClaim)
}

//#endregion

//#region getAdminServer

func (r *BucketClaimReconciler) getAdminServer(adminService string) (*admin.AdminServer, error) {
	r.adminMutex.Lock()
	defer r.adminMutex.Unlock()

	entry, ok := r.adminServers[adminService]

	if !ok {
		// Get the master addresses for this admin service
		masterAddresses, err := r.getMasterAddressesForAdminService(adminService)
		if err != nil {
			return nil, fmt.Errorf("failed to get master addresses: %w", err)
		}

		entry = &adminServerEntry{
			server:     admin.NewAdminServer(masterAddresses, r.Log),
			lastAccess: time.Now(),
		}

		r.adminServers[adminService] = entry
	} else {
		entry.lastAccess = time.Now()
	}
	return entry.server, nil
}

// getMasterAddressesForAdminService extracts master addresses from the admin service URL
func (r *BucketClaimReconciler) getMasterAddressesForAdminService(adminServiceURL string) (string, error) {
	// Extract cluster name and namespace from admin service URL
	// Format: http://{cluster-name}-admin.{namespace}.svc.cluster.local:{port}
	// We need to construct master addresses like: {cluster-name}-master.{namespace}.svc.cluster.local:9333

	// Parse the admin service URL to extract cluster name and namespace
	// Remove http:// prefix
	serviceName := strings.TrimPrefix(adminServiceURL, "http://")

	// Split by first dot to get cluster-admin part
	parts := strings.SplitN(serviceName, ".", 2)
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid admin service URL format: %s", adminServiceURL)
	}

	clusterAdminPart := parts[0] // e.g., "seaweed-sample-admin"
	rest := parts[1]             // e.g., "default.svc.cluster.local:23646"

	// Extract cluster name by removing "-admin" suffix
	if !strings.HasSuffix(clusterAdminPart, "-admin") {
		return "", fmt.Errorf("invalid admin service name format: %s", clusterAdminPart)
	}
	clusterName := clusterAdminPart[:len(clusterAdminPart)-6] // Remove "-admin"

	// Extract namespace by taking the first part before "svc.cluster.local"
	namespaceParts := strings.SplitN(rest, ".", 2)
	if len(namespaceParts) < 2 {
		return "", fmt.Errorf("invalid service URL format: %s", rest)
	}
	namespace := namespaceParts[0]

	// Construct master service address
	masterAddress := fmt.Sprintf("%s-master.%s.svc.cluster.local:9333", clusterName, namespace)

	r.Log.Debugw("Constructed master address", "adminService", adminServiceURL, "masterAddress", masterAddress)

	return masterAddress, nil
}

//#endregion

//#region handleReconciliation

// handleReconciliation handles the main reconciliation logic for bucket creation/update
func (r *BucketClaimReconciler) handleReconciliation(ctx context.Context, bucketClaim *seaweedv1.BucketClaim) (ctrl.Result, error) {
	log := r.Log.With("bucketclaim", bucketClaim.Name)

	log.Debug("executing handleReconciliation")

	// Get the referenced Seaweed cluster
	seaweedCluster, err := r.getSeaweedCluster(ctx, bucketClaim)
	if err != nil {
		log.Errorw("failed to get seaweed cluster", "error", err)
		return r.updateStatus(ctx, bucketClaim, seaweedv1.BucketClaimPhaseFailed, fmt.Sprintf("Failed to get Seaweed cluster: %v", err))
	}

	// Check if admin service is available
	adminService, err := r.getAdminService(seaweedCluster)
	if err != nil {
		log.Errorw("failed to get admin service", "error", err)
		return r.updateStatus(ctx, bucketClaim, seaweedv1.BucketClaimPhaseFailed, fmt.Sprintf("Failed to get admin service: %v", err))
	}

	// Update status to Creating if not already set
	if bucketClaim.Status.Phase == "" || bucketClaim.Status.Phase == seaweedv1.BucketClaimPhasePending {
		if _, err := r.updateStatus(ctx, bucketClaim, seaweedv1.BucketClaimPhaseCreating, "Creating bucket"); err != nil {
			log.Errorw("failed to update status to creating", "error", err)
			return ctrl.Result{}, err
		}
	}

	log.Debugw("preparing to check if bucket exists", "adminService", adminService)

	// Check if bucket already exists
	exists, err := r.bucketExists(adminService, bucketClaim.Spec.BucketName)
	if err != nil {
		log.Errorw("failed to check if bucket exists", "error", err)
		return r.updateStatus(ctx, bucketClaim, seaweedv1.BucketClaimPhaseFailed, fmt.Sprintf("Failed to check bucket existence: %v", err))
	}

	log.Debugw("bucket existing state", "exists", exists)

	if exists {
		// Bucket exists, update status to Ready
		bucketInfo, err := r.getBucketInfo(adminService, bucketClaim.Spec.BucketName, false)

		if err != nil {
			log.Errorw("failed to get bucket info", "error", err)
			return r.updateStatus(ctx, bucketClaim, seaweedv1.BucketClaimPhaseFailed, fmt.Sprintf("Failed to get bucket info: %v", err))
		}

		// Create or update S3 credentials secret if enabled (default is true when secret is nil or enabled is true)
		var secretInfo *seaweedv1.BucketSecretInfo
		if bucketClaim.Spec.Secret == nil || bucketClaim.Spec.Secret.Enabled {
			secretInfo, err = r.createS3CredentialsSecret(ctx, bucketClaim, seaweedCluster)
			if err != nil {
				log.Errorw("failed to create S3 credentials secret", "error", err)
				return r.updateStatus(ctx, bucketClaim, seaweedv1.BucketClaimPhaseFailed, fmt.Sprintf("Failed to create S3 credentials secret: %v", err))
			}
		}

		return r.updateStatusWithBucketAndSecretInfo(ctx, bucketClaim, seaweedv1.BucketClaimPhaseReady, "Bucket is ready", bucketInfo, secretInfo)
	}

	// Create the bucket
	err = r.createBucket(adminService, bucketClaim)
	if err != nil {
		log.Errorw("failed to create bucket", "error", err)
		return r.updateStatus(ctx, bucketClaim, seaweedv1.BucketClaimPhaseFailed, fmt.Sprintf("Failed to create bucket: %v", err))
	}

	log.Debug("bucket created state")

	// Get bucket info after creation
	bucketInfo, err := r.getBucketInfo(adminService, bucketClaim.Spec.BucketName, false)
	if err != nil {
		log.Errorw("failed to get bucket info after creation", "error", err)
		return r.updateStatus(ctx, bucketClaim, seaweedv1.BucketClaimPhaseFailed, fmt.Sprintf("Failed to get bucket info: %v", err))
	}

	// Create S3 credentials secret if enabled (default is true when secret is nil or enabled is true)
	var secretInfo *seaweedv1.BucketSecretInfo
	if bucketClaim.Spec.Secret == nil || bucketClaim.Spec.Secret.Enabled {
		secretInfo, err = r.createS3CredentialsSecret(ctx, bucketClaim, seaweedCluster)
		if err != nil {
			log.Errorw("failed to create S3 credentials secret", "error", err)
			return r.updateStatus(ctx, bucketClaim, seaweedv1.BucketClaimPhaseFailed, fmt.Sprintf("Failed to create S3 credentials secret: %v", err))
		}
	}

	// Update status with both bucket and secret info
	return r.updateStatusWithBucketAndSecretInfo(ctx, bucketClaim, seaweedv1.BucketClaimPhaseReady, "Bucket created successfully", bucketInfo, secretInfo)
}

//#endregion

//#region handleDeletion

// handleDeletion handles bucket deletion when BucketClaim is being deleted
func (r *BucketClaimReconciler) handleDeletion(ctx context.Context, bucketClaim *seaweedv1.BucketClaim) error {
	log := r.Log.With("bucketclaim", bucketClaim.Name)

	// Get the referenced Seaweed cluster
	seaweedCluster, err := r.getSeaweedCluster(ctx, bucketClaim)
	if err != nil {
		log.Errorw("failed to get seaweed cluster for deletion", "error", err)
		return err
	}

	// Get admin service
	adminService, err := r.getAdminService(seaweedCluster)
	if err != nil {
		log.Errorw("failed to get admin service for deletion", "error", err)
		return err
	}

	// Delete the bucket only if deletionPolicy is "Delete"
	if bucketClaim.Spec.DeletionPolicy == seaweedv1.BucketDeletionPolicyDelete {
		err = r.deleteBucket(adminService, bucketClaim.Spec.BucketName)
		if err != nil {
			log.Errorw("failed to delete bucket", "error", err)
			// Don't return error to avoid blocking deletion if bucket doesn't exist
		}
	} else {
		log.Infow("retaining bucket due to deletionPolicy", "bucket", bucketClaim.Spec.BucketName)
	}

	// Delete the S3 user if credentials secret is enabled (default is true when secret is nil or enabled is true)
	if bucketClaim.Spec.Secret == nil || bucketClaim.Spec.Secret.Enabled {
		err = r.deleteS3User(bucketClaim, seaweedCluster)
		if err != nil {
			log.Errorw("failed to delete S3 user", "error", err)
			return fmt.Errorf("failed to delete S3 user: %w", err)
		}
	}

	// Delete the S3 credentials secret if enabled (default is true when secret is nil or enabled is true)
	if bucketClaim.Spec.Secret == nil || bucketClaim.Spec.Secret.Enabled {
		err = r.deleteS3CredentialsSecret(ctx, bucketClaim)
		if err != nil {
			log.Errorw("failed to delete S3 credentials secret", "error", err)
			return fmt.Errorf("failed to delete S3 credentials secret: %w", err)
		}
	}

	log.Debug("bucket deletion completed")
	return nil
}

//#endregion

//#region deleteS3CredentialsSecret

func (r *BucketClaimReconciler) deleteS3CredentialsSecret(ctx context.Context, bucketClaim *seaweedv1.BucketClaim) error {
	log := r.Log.With("bucketclaim", bucketClaim.Name)

	// Skip deletion only if secret is explicitly disabled
	if bucketClaim.Spec.Secret != nil && !bucketClaim.Spec.Secret.Enabled {
		log.Debug("credentials secret is not enabled, skipping deletion")
		return nil
	}

	// Default secret name to BucketClaim name
	secretName := bucketClaim.Name
	if bucketClaim.Spec.Secret != nil && bucketClaim.Spec.Secret.Name != "" {
		secretName = bucketClaim.Spec.Secret.Name
	}

	err := r.Delete(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: bucketClaim.Namespace,
		},
	})

	if err != nil {
		return fmt.Errorf("failed to delete S3 credentials secret: %w", err)
	}

	return nil
}

//#endregion

//#region deleteS3User

// deleteS3User deletes the S3 user associated with the bucket claim
func (r *BucketClaimReconciler) deleteS3User(bucketClaim *seaweedv1.BucketClaim, seaweedCluster *seaweedv1.Seaweed) error {
	log := r.Log.With("bucketclaim", bucketClaim.Name)

	// Get admin server for S3 user management
	adminServiceURL := fmt.Sprintf("http://%s-admin.%s.svc.cluster.local:%d", seaweedCluster.Name, seaweedCluster.Namespace, seaweedv1.AdminHTTPPort)
	adminServer, err := r.getAdminServer(adminServiceURL)
	if err != nil {
		return fmt.Errorf("failed to get admin server: %w", err)
	}

	// Generate the same username that was used during creation
	username := formatUsername(bucketClaim)

	// Check if user exists before trying to delete
	_, err = adminServer.GetObjectStoreUserDetails(username)
	if err != nil {
		// Check if user doesn't exist (might have been already deleted)
		if strings.Contains(err.Error(), "not found") {
			log.Debugw("S3 user already deleted", "username", username)
			return nil
		}
		return fmt.Errorf("failed to check S3 user existence: %w", err)
	}

	// Delete the S3 user
	err = adminServer.DeleteObjectStoreUser(username)
	if err != nil {
		return fmt.Errorf("failed to delete S3 user: %w", err)
	}

	log.Debugw("deleted S3 user", "username", username)
	return nil
}

//#endregion

//#region getSeaweedCluster

// getSeaweedCluster retrieves the referenced Seaweed cluster
func (r *BucketClaimReconciler) getSeaweedCluster(ctx context.Context, bucketClaim *seaweedv1.BucketClaim) (*seaweedv1.Seaweed, error) {
	namespace := bucketClaim.Spec.ClusterRef.Namespace
	if namespace == "" {
		namespace = bucketClaim.Namespace
	}

	seaweedCluster := &seaweedv1.Seaweed{}
	err := r.Get(ctx, client.ObjectKey{
		Namespace: namespace,
		Name:      bucketClaim.Spec.ClusterRef.Name,
	}, seaweedCluster)
	if err != nil {
		return nil, fmt.Errorf("failed to get Seaweed cluster %s in namespace %s: %w",
			bucketClaim.Spec.ClusterRef.Name, namespace, err)
	}

	return seaweedCluster, nil
}

//#endregion

//#region getAdminService

// getAdminService retrieves the admin service for the Seaweed cluster
func (r *BucketClaimReconciler) getAdminService(seaweedCluster *seaweedv1.Seaweed) (string, error) {
	// Check if admin is enabled
	if seaweedCluster.Spec.Admin == nil {
		return "", fmt.Errorf("admin service is not enabled for Seaweed cluster %s", seaweedCluster.Name)
	}

	// Construct admin service URL
	port := seaweedv1.AdminHTTPPort
	if seaweedCluster.Spec.Admin.Port != nil {
		port = int(*seaweedCluster.Spec.Admin.Port)
	}

	adminServiceURL := fmt.Sprintf("http://%s-admin.%s.svc.cluster.local:%d",
		seaweedCluster.Name, seaweedCluster.Namespace, port)

	return adminServiceURL, nil
}

//#endregion

//#region bucketExists

// bucketExists checks if a bucket exists in the SeaweedFS cluster
func (r *BucketClaimReconciler) bucketExists(adminServiceURL, bucketName string) (bool, error) {
	log := r.Log.With("bucketclaim-bucketExists", bucketName)

	log.Debug("checking if bucket exists", "adminServiceURL", adminServiceURL)

	adminServer, err := r.getAdminServer(adminServiceURL)

	if err != nil {
		return false, fmt.Errorf("failed to get admin server: %w", err)
	}

	log.Debug("getting S3 buckets")

	list, err := adminServer.GetS3Buckets()

	if err != nil {
		return false, fmt.Errorf("failed to check bucket existence: %w", err)
	}

	for _, bucket := range list {
		log.Debug("checking if bucket exists", "bucketName", bucketName)

		if bucket.Name == bucketName {
			log.Debug("bucket exists", "bucketName", bucketName)
			return true, nil
		}
	}

	log.Debug("bucket does not exist", "bucketName", bucketName)

	return false, nil
}

// getBucketInfo retrieves information about a bucket

//#endregion

//#region getBucketInfo

func (r *BucketClaimReconciler) getBucketInfo(adminServiceURL, bucketName string, includeObjects bool) (*seaweedv1.BucketInfo, error) {
	adminServer, err := r.getAdminServer(adminServiceURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get admin server: %w", err)
	}

	bucket, err := adminServer.GetBucketDetails(bucketName, includeObjects)
	if err != nil {
		return nil, fmt.Errorf("failed to get bucket info: %w", err)
	}

	bucketInfo := &seaweedv1.BucketInfo{
		Name: bucketName,
	}

	bucketInfo.CreatedAt = &metav1.Time{Time: bucket.Bucket.CreatedAt}

	// Set creation time if not set
	if bucketInfo.CreatedAt == nil {
		now := metav1.Now()
		bucketInfo.CreatedAt = &now
	}

	return bucketInfo, nil
}

// Helper function to convert quota size and unit to bytes

//#endregion

//#region convertQuotaToBytes

func convertQuotaToBytes(size int64, unit string) int64 {
	if size <= 0 {
		return 0
	}

	switch strings.ToUpper(unit) {
	case "TB":
		return size * 1024 * 1024 * 1024 * 1024
	case "GB":
		return size * 1024 * 1024 * 1024
	case "MB":
		return size * 1024 * 1024
	default:
		// Default to MB if unit is not recognized
		return size * 1024 * 1024
	}
}

//#endregion

func formatUsername(bucketClaim *seaweedv1.BucketClaim) string {
	return fmt.Sprintf("claim-%s-%s", bucketClaim.Namespace, bucketClaim.Name)
}

// createS3CredentialsSecret creates a Kubernetes secret with S3 credentials
func (r *BucketClaimReconciler) createS3CredentialsSecret(ctx context.Context, bucketClaim *seaweedv1.BucketClaim, seaweedCluster *seaweedv1.Seaweed) (*seaweedv1.BucketSecretInfo, error) {
	log := r.Log.With("bucketclaim", bucketClaim.Name)

	// Skip creation only if secret is explicitly disabled
	if bucketClaim.Spec.Secret != nil && !bucketClaim.Spec.Secret.Enabled {
		return nil, nil
	}

	// Get admin server for S3 user management
	adminServiceURL := fmt.Sprintf("http://%s-admin.%s.svc.cluster.local:%d", seaweedCluster.Name, seaweedCluster.Namespace, seaweedv1.AdminHTTPPort)
	adminServer, err := r.getAdminServer(adminServiceURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get admin server: %w", err)
	}

	// Generate unique username for this bucket claim
	username := formatUsername(bucketClaim)

	// Create S3 user with bucket-specific permissions
	userReq := admin.CreateUserRequest{
		Username: username,
		// Allow all S3 operations on this specific bucket
		Actions: []string{
			fmt.Sprintf("Read:%s", bucketClaim.Spec.BucketName),
			fmt.Sprintf("Write:%s", bucketClaim.Spec.BucketName),
			fmt.Sprintf("List:%s", bucketClaim.Spec.BucketName),
			fmt.Sprintf("Tagging:%s", bucketClaim.Spec.BucketName),
			fmt.Sprintf("Admin:%s", bucketClaim.Spec.BucketName),
		},
		GenerateKey: true,
	}

	user, err := adminServer.CreateObjectStoreUser(userReq)
	if err != nil {
		// Check if user already exists (might happen during reconciliation)
		if strings.Contains(err.Error(), "already exists") {
			// Get existing user details
			userDetails, err := adminServer.GetObjectStoreUserDetails(username)
			if err != nil {
				return nil, fmt.Errorf("failed to get existing user details: %w", err)
			}

			// Use the first access key if available
			if len(userDetails.AccessKeys) > 0 {
				user = &admin.ObjectStoreUser{
					Username:    username,
					Email:       userDetails.Email,
					AccessKey:   userDetails.AccessKeys[0].AccessKey,
					SecretKey:   userDetails.AccessKeys[0].SecretKey,
					Permissions: userDetails.Actions,
				}
			} else {
				// Create new access key for existing user
				accessKeyInfo, err := adminServer.CreateAccessKey(username)
				if err != nil {
					return nil, fmt.Errorf("failed to create access key for existing user: %w", err)
				}
				user = &admin.ObjectStoreUser{
					Username:    username,
					Email:       userDetails.Email,
					AccessKey:   accessKeyInfo.AccessKey,
					SecretKey:   accessKeyInfo.SecretKey,
					Permissions: userDetails.Actions,
				}
			}
		} else {
			return nil, fmt.Errorf("failed to create S3 user: %w", err)
		}
	}

	// Determine secret name (default to BucketClaim name)
	secretName := bucketClaim.Name
	if bucketClaim.Spec.Secret != nil && bucketClaim.Spec.Secret.Name != "" {
		secretName = bucketClaim.Spec.Secret.Name
	}

	// Determine S3 endpoint
	s3Endpoint := fmt.Sprintf("http://%s-filer.%s.svc.cluster.local:8333", seaweedCluster.Name, seaweedCluster.Namespace)

	// Determine S3 region
	s3Region := bucketClaim.Spec.Region
	if s3Region == "" {
		s3Region = "us-east-1" // Default region
	}

	// Create secret data using the S3 user credentials
	secretData := map[string][]byte{
		"access-key-id":         []byte(user.AccessKey),
		"secret-access-key":     []byte(user.SecretKey),
		"endpoint":              []byte(s3Endpoint),
		"region":                []byte(s3Region),
		"bucket":                []byte(bucketClaim.Spec.BucketName),
		"S3_ACCESS_KEY_ID":      []byte(user.AccessKey),
		"S3_SECRET_ACCESS_KEY":  []byte(user.SecretKey),
		"S3_REGION":             []byte(s3Region),
		"S3_BUCKET":             []byte(bucketClaim.Spec.BucketName),
		"AWS_ACCESS_KEY_ID":     []byte(user.AccessKey),
		"AWS_SECRET_ACCESS_KEY": []byte(user.SecretKey),
		"AWS_REGION":            []byte(s3Region),
		"AWS_BUCKET":            []byte(bucketClaim.Spec.BucketName),
	}

	// Create secret object
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: bucketClaim.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "seaweedfs-operator",
				"app.kubernetes.io/component":  "bucket-credentials",
				"seaweed.seaweedfs.com/bucket": bucketClaim.Spec.BucketName,
			},
			Annotations: map[string]string{
				"seaweed.seaweedfs.com/created-by": "bucketclaim-controller",
				"seaweed.seaweedfs.com/bucket":     bucketClaim.Spec.BucketName,
				"seaweed.seaweedfs.com/s3-user":    username,
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: secretData,
	}

	// Add custom labels and annotations if specified
	if bucketClaim.Spec.Secret != nil && bucketClaim.Spec.Secret.Labels != nil {
		for k, v := range bucketClaim.Spec.Secret.Labels {
			secret.Labels[k] = v
		}
	}

	if bucketClaim.Spec.Secret != nil && bucketClaim.Spec.Secret.Annotations != nil {
		for k, v := range bucketClaim.Spec.Secret.Annotations {
			secret.Annotations[k] = v
		}
	}

	// Set owner reference to the BucketClaim
	if err := ctrl.SetControllerReference(bucketClaim, secret, r.Scheme); err != nil {
		return nil, fmt.Errorf("failed to set controller reference: %w", err)
	}

	// Create or update the secret
	err = r.Create(ctx, secret)
	if err != nil {
		if errors.IsAlreadyExists(err) {
			// Update existing secret
			existingSecret := &corev1.Secret{}
			err = r.Get(ctx, client.ObjectKey{Name: secretName, Namespace: bucketClaim.Namespace}, existingSecret)
			if err != nil {
				return nil, fmt.Errorf("failed to get existing secret: %w", err)
			}

			existingSecret.Data = secretData
			existingSecret.Labels = secret.Labels
			existingSecret.Annotations = secret.Annotations

			err = r.Update(ctx, existingSecret)
			if err != nil {
				return nil, fmt.Errorf("failed to update secret: %w", err)
			}
		} else {
			return nil, fmt.Errorf("failed to create secret: %w", err)
		}
	}

	log.Debugw("created S3 credentials secret", "secretName", secretName, "bucketName", bucketClaim.Spec.BucketName)

	return &seaweedv1.BucketSecretInfo{
		Name:      secretName,
		Namespace: bucketClaim.Namespace,
	}, nil
}

//#endregion

//#region createBucket

// createBucket creates a new bucket in the SeaweedFS cluster
func (r *BucketClaimReconciler) createBucket(adminServiceURL string, bucketClaim *seaweedv1.BucketClaim) error {
	adminServer, err := r.getAdminServer(adminServiceURL)
	if err != nil {
		return fmt.Errorf("failed to get admin server: %w", err)
	}

	quota := bucketClaim.Spec.Quota

	versioningEnabled := bucketClaim.Spec.VersioningEnabled

	objectLockEnabled := bucketClaim.Spec.ObjectLock.Enabled
	objectLockMode := bucketClaim.Spec.ObjectLock.Mode
	objectLockDuration := bucketClaim.Spec.ObjectLock.Duration

	quotaBytes := convertQuotaToBytes(quota.Size, quota.Unit)

	err = adminServer.CreateS3BucketWithObjectLock(bucketClaim.Spec.BucketName, quotaBytes, quota.Enabled, versioningEnabled, objectLockEnabled, objectLockMode, objectLockDuration)
	if err != nil {
		return fmt.Errorf("failed to create bucket: %w", err)
	}

	return nil
}

// deleteBucket deletes a bucket from the SeaweedFS cluster
func (r *BucketClaimReconciler) deleteBucket(adminServiceURL, bucketName string) error {
	adminServer, err := r.getAdminServer(adminServiceURL)
	if err != nil {
		return fmt.Errorf("failed to get admin server: %w", err)
	}

	err = adminServer.DeleteS3Bucket(bucketName)
	if err != nil {
		return fmt.Errorf("failed to delete bucket: %w", err)
	}

	return nil
}

//#endregion

//#region updateStatus

// updateStatus updates the BucketClaim status
func (r *BucketClaimReconciler) updateStatus(ctx context.Context, bucketClaim *seaweedv1.BucketClaim, phase seaweedv1.BucketClaimPhase, message string) (ctrl.Result, error) {
	log := r.Log.With("bucketclaim-update-status", bucketClaim.Name)

	log.Debug("updating status", "phase", phase, "message", message)

	bucketClaim.Status.Phase = phase
	bucketClaim.Status.Message = message
	bucketClaim.Status.LastUpdateTime = &metav1.Time{Time: time.Now()}

	// Add condition
	condition := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             string(phase),
		Message:            message,
		LastTransitionTime: metav1.Time{Time: time.Now()},
	}

	if phase == seaweedv1.BucketClaimPhaseFailed {
		condition.Status = metav1.ConditionFalse
	}

	// Update or add condition
	found := false
	for i, c := range bucketClaim.Status.Conditions {
		if c.Type == "Ready" {
			bucketClaim.Status.Conditions[i] = condition
			found = true
			break
		}
	}

	if !found {
		bucketClaim.Status.Conditions = append(bucketClaim.Status.Conditions, condition)
	}

	if err := r.Status().Update(ctx, bucketClaim); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
	}

	// Determine requeue interval based on phase
	switch phase {
	case seaweedv1.BucketClaimPhaseCreating:
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	case seaweedv1.BucketClaimPhaseReady:
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	case seaweedv1.BucketClaimPhaseFailed:
		return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
	default:
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
}

//#endregion

//#region updateStatusWithBucketAndSecretInfo

// updateStatusWithBucketAndSecretInfo updates the BucketClaim status with both bucket and secret information
func (r *BucketClaimReconciler) updateStatusWithBucketAndSecretInfo(ctx context.Context, bucketClaim *seaweedv1.BucketClaim, phase seaweedv1.BucketClaimPhase, message string, bucketInfo *seaweedv1.BucketInfo, secretInfo *seaweedv1.BucketSecretInfo) (ctrl.Result, error) {
	bucketClaim.Status.BucketInfo = bucketInfo
	bucketClaim.Status.SecretInfo = secretInfo
	return r.updateStatus(ctx, bucketClaim, phase, message)
}

//#endregion

//#region startCleanupGoroutine

// startCleanupGoroutine starts the background cleanup goroutine
func (r *BucketClaimReconciler) startCleanupGoroutine() {
	r.cleanupTicker = time.NewTicker(1 * time.Minute) // Check every minute
	r.stopCleanup = make(chan struct{})

	go func() {
		for {
			select {
			case <-r.cleanupTicker.C:
				r.cleanupInactiveAdminServers()
			case <-r.stopCleanup:
				r.cleanupTicker.Stop()
				return
			}
		}
	}()
}

//#endregion

//#region stopCleanupGoroutine

// stopCleanupGoroutine stops the background cleanup goroutine
func (r *BucketClaimReconciler) stopCleanupGoroutine() {
	if r.cleanupTicker != nil {
		r.cleanupTicker.Stop()
	}
	if r.stopCleanup != nil {
		close(r.stopCleanup)
	}
}

//#endregion

//#region cleanupInactiveAdminServers

// cleanupInactiveAdminServers removes admin servers that haven't been accessed for 5 minutes
func (r *BucketClaimReconciler) cleanupInactiveAdminServers() {
	r.adminMutex.Lock()
	defer r.adminMutex.Unlock()

	inactivityThreshold := 5 * time.Minute
	now := time.Now()

	for adminService, entry := range r.adminServers {
		if now.Sub(entry.lastAccess) > inactivityThreshold {
			delete(r.adminServers, adminService)
			r.Log.Debugw("removed inactive admin server", "adminService", adminService, "inactiveFor", now.Sub(entry.lastAccess))
		}
	}
}

//#endregion

//#region SetupWithManager

// SetupWithManager sets up the controller with the manager
func (r *BucketClaimReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Initialize the admin servers map
	r.adminServers = make(map[string]*adminServerEntry)

	// Start the cleanup goroutine
	r.startCleanupGoroutine()

	// Register cleanup on manager shutdown
	mgr.Add(cleanupRunnable{r})

	return ctrl.NewControllerManagedBy(mgr).
		For(&seaweedv1.BucketClaim{}).
		Complete(r)
}

//#endregion

//#region cleanupRunnable

// cleanupRunnable implements the Runnable interface to ensure cleanup on shutdown
type cleanupRunnable struct {
	reconciler *BucketClaimReconciler
}

func (c cleanupRunnable) Start(ctx context.Context) error {
	// Wait for context cancellation
	<-ctx.Done()
	// Stop the cleanup goroutine when context is cancelled
	c.reconciler.stopCleanupGoroutine()
	return nil
}

//#endregion

//#region Helper functions for finalizers

// containsString checks if a slice contains a string
func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

// removeString removes a string from a slice
func removeString(slice []string, s string) []string {
	var result []string
	for _, item := range slice {
		if item != s {
			result = append(result, item)
		}
	}
	return result
}

//#endregion
