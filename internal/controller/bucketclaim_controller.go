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

	"github.com/go-logr/logr"
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
	Log    logr.Logger
	Scheme *runtime.Scheme

	adminServers  map[string]*adminServerEntry
	adminMutex    sync.RWMutex
	cleanupTicker *time.Ticker
	stopCleanup   chan struct{}
}

// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=bucketclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=bucketclaims/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=seaweeds,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch

// Reconcile implements the reconciliation logic for BucketClaim
func (r *BucketClaimReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("bucketclaim", req.NamespacedName)

	log.Info("Starting BucketClaim Reconcile")

	// Fetch the BucketClaim instance
	bucketClaim := &seaweedv1.BucketClaim{}
	err := r.Get(ctx, req.NamespacedName, bucketClaim)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			log.Info("BucketClaim not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}

		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get BucketClaim")
		return ctrl.Result{}, err
	}

	// Check if the BucketClaim is being deleted
	if !bucketClaim.DeletionTimestamp.IsZero() {
		log.Info("BucketClaim is being deleted, cleaning up bucket")
		return r.handleDeletion(ctx, bucketClaim)
	}

	// Handle bucket creation/update
	return r.handleReconciliation(ctx, bucketClaim)
}

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
	serviceName := adminServiceURL
	if strings.HasPrefix(serviceName, "http://") {
		serviceName = serviceName[7:] // Remove "http://"
	}

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

	r.Log.V(1).Info("Constructed master address", "adminService", adminServiceURL, "masterAddress", masterAddress)

	return masterAddress, nil
}

// handleReconciliation handles the main reconciliation logic for bucket creation/update
func (r *BucketClaimReconciler) handleReconciliation(ctx context.Context, bucketClaim *seaweedv1.BucketClaim) (ctrl.Result, error) {
	log := r.Log.WithValues("bucketclaim", bucketClaim.Name)

	log.Info("Starting BucketClaim handleReconciliation")

	// Get the referenced Seaweed cluster
	seaweedCluster, err := r.getSeaweedCluster(ctx, bucketClaim)
	if err != nil {
		log.Error(err, "Failed to get Seaweed cluster")
		return r.updateStatus(ctx, bucketClaim, seaweedv1.BucketClaimPhaseFailed, fmt.Sprintf("Failed to get Seaweed cluster: %v", err))
	}

	// Check if admin service is available
	adminService, err := r.getAdminService(seaweedCluster)
	if err != nil {
		log.Error(err, "Failed to get admin service")
		return r.updateStatus(ctx, bucketClaim, seaweedv1.BucketClaimPhaseFailed, fmt.Sprintf("Failed to get admin service: %v", err))
	}

	// Update status to Creating if not already set
	if bucketClaim.Status.Phase == "" || bucketClaim.Status.Phase == seaweedv1.BucketClaimPhasePending {
		if _, err := r.updateStatus(ctx, bucketClaim, seaweedv1.BucketClaimPhaseCreating, "Creating bucket"); err != nil {
			log.Error(err, "Failed to update status to Creating", "error", err)
			return ctrl.Result{}, err
		}
	}

	log.Info("Preparing to check if bucket exists", "adminService", adminService)

	// Check if bucket already exists
	exists, err := r.bucketExists(adminService, bucketClaim.Spec.BucketName)
	if err != nil {
		log.Error(err, "Failed to check if bucket exists")
		return r.updateStatus(ctx, bucketClaim, seaweedv1.BucketClaimPhaseFailed, fmt.Sprintf("Failed to check bucket existence: %v", err))
	}

	log.Info("Bucket existing state", "exists", exists)

	if exists {
		// Bucket exists, update status to Ready
		bucketInfo, err := r.getBucketInfo(adminService, bucketClaim.Spec.BucketName)
		if err != nil {
			log.Error(err, "Failed to get bucket info")
			return r.updateStatus(ctx, bucketClaim, seaweedv1.BucketClaimPhaseFailed, fmt.Sprintf("Failed to get bucket info: %v", err))
		}

		return r.updateStatusWithBucketInfo(ctx, bucketClaim, seaweedv1.BucketClaimPhaseReady, "Bucket is ready", bucketInfo)
	}

	// Create the bucket
	err = r.createBucket(adminService, bucketClaim)
	if err != nil {
		log.Error(err, "Failed to create bucket")
		return r.updateStatus(ctx, bucketClaim, seaweedv1.BucketClaimPhaseFailed, fmt.Sprintf("Failed to create bucket: %v", err))
	}

	log.Info("Bucket created state")

	// Get bucket info after creation
	bucketInfo, err := r.getBucketInfo(adminService, bucketClaim.Spec.BucketName)
	if err != nil {
		log.Error(err, "Failed to get bucket info after creation")
		return r.updateStatus(ctx, bucketClaim, seaweedv1.BucketClaimPhaseFailed, fmt.Sprintf("Failed to get bucket info: %v", err))
	}

	log.Info("Bucket info", "bucketInfo", bucketInfo)

	return r.updateStatusWithBucketInfo(ctx, bucketClaim, seaweedv1.BucketClaimPhaseReady, "Bucket created successfully", bucketInfo)
}

// handleDeletion handles bucket deletion when BucketClaim is being deleted
func (r *BucketClaimReconciler) handleDeletion(ctx context.Context, bucketClaim *seaweedv1.BucketClaim) (ctrl.Result, error) {
	log := r.Log.WithValues("bucketclaim", bucketClaim.Name)

	// Get the referenced Seaweed cluster
	seaweedCluster, err := r.getSeaweedCluster(ctx, bucketClaim)
	if err != nil {
		log.Error(err, "Failed to get Seaweed cluster for deletion")
		return ctrl.Result{}, err
	}

	// Get admin service
	adminService, err := r.getAdminService(seaweedCluster)
	if err != nil {
		log.Error(err, "Failed to get admin service for deletion")
		return ctrl.Result{}, err
	}

	// Delete the bucket
	err = r.deleteBucket(adminService, bucketClaim.Spec.BucketName)
	if err != nil {
		log.Error(err, "Failed to delete bucket")
		// Don't return error to avoid blocking deletion
	}

	log.Info("Bucket deletion completed")
	return ctrl.Result{}, nil
}

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

// bucketExists checks if a bucket exists in the SeaweedFS cluster
func (r *BucketClaimReconciler) bucketExists(adminServiceURL, bucketName string) (bool, error) {
	log := r.Log.WithValues("bucketclaim-bucketExists", bucketName)

	log.Info("Checking if bucket exists", "adminServiceURL", adminServiceURL)

	adminServer, err := r.getAdminServer(adminServiceURL)

	log.Info("Got admin server", "adminServer", adminServer, "err", err)

	if err != nil {
		return false, fmt.Errorf("failed to get admin server: %w", err)
	}

	log.Info("Getting S3 buckets")

	list, err := adminServer.GetS3Buckets()

	log.Info("Got S3 buckets", "list", list, "err", err)
	if err != nil {
		return false, fmt.Errorf("failed to check bucket existence: %w", err)
	}

	for _, bucket := range list {
		log.Info("Checking if bucket exists", "bucketName", bucketName)

		if bucket.Name == bucketName {
			log.Info("Bucket exists", "bucketName", bucketName)
			return true, nil
		}
	}

	log.Info("Bucket does not exist", "bucketName", bucketName)

	return false, nil
}

// getBucketInfo retrieves information about a bucket
func (r *BucketClaimReconciler) getBucketInfo(adminServiceURL, bucketName string) (*seaweedv1.BucketInfo, error) {
	adminServer, err := r.getAdminServer(adminServiceURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get admin server: %w", err)
	}

	bucket, err := adminServer.GetBucketDetails(bucketName)
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

// updateStatus updates the BucketClaim status
func (r *BucketClaimReconciler) updateStatus(ctx context.Context, bucketClaim *seaweedv1.BucketClaim, phase seaweedv1.BucketClaimPhase, message string) (ctrl.Result, error) {
	log := r.Log.WithValues("bucketclaim-update-status", bucketClaim.Name)

	log.Info("Updating status", "phase", phase, "message", message)

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

// updateStatusWithBucketInfo updates the BucketClaim status with bucket information
func (r *BucketClaimReconciler) updateStatusWithBucketInfo(ctx context.Context, bucketClaim *seaweedv1.BucketClaim, phase seaweedv1.BucketClaimPhase, message string, bucketInfo *seaweedv1.BucketInfo) (ctrl.Result, error) {
	bucketClaim.Status.BucketInfo = bucketInfo
	return r.updateStatus(ctx, bucketClaim, phase, message)
}

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

// stopCleanupGoroutine stops the background cleanup goroutine
func (r *BucketClaimReconciler) stopCleanupGoroutine() {
	if r.cleanupTicker != nil {
		r.cleanupTicker.Stop()
	}
	if r.stopCleanup != nil {
		close(r.stopCleanup)
	}
}

// cleanupInactiveAdminServers removes admin servers that haven't been accessed for 5 minutes
func (r *BucketClaimReconciler) cleanupInactiveAdminServers() {
	r.adminMutex.Lock()
	defer r.adminMutex.Unlock()

	inactivityThreshold := 5 * time.Minute
	now := time.Now()

	for adminService, entry := range r.adminServers {
		if now.Sub(entry.lastAccess) > inactivityThreshold {
			delete(r.adminServers, adminService)
			r.Log.Info("Removed inactive admin server", "adminService", adminService, "inactiveFor", now.Sub(entry.lastAccess))
		}
	}
}

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
