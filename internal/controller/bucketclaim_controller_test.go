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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func TestBucketClaimReconciler_CreateS3CredentialsSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, seaweedv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Create a test BucketClaim with secret enabled
	bucketClaim := &seaweedv1.BucketClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-bucket",
			Namespace: "default",
		},
		Spec: seaweedv1.BucketClaimSpec{
			BucketName: "test-bucket",
			Region:     "us-east-1",
			ClusterRef: seaweedv1.ClusterReference{
				Name:      "test-cluster",
				Namespace: "default",
			},
			Secret: &seaweedv1.BucketSecretSpec{
				Enabled: true,
				Name:    "test-bucket-credentials",
				Labels: map[string]string{
					"test-label": "test-value",
				},
				Annotations: map[string]string{
					"test-annotation": "test-value",
				},
			},
		},
	}

	// Create a test Seaweed cluster
	seaweedCluster := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: seaweedv1.SeaweedSpec{
			Filer: &seaweedv1.FilerSpec{
				S3: &seaweedv1.S3Config{
					Enabled: true,
				},
			},
		},
	}

	// Create fake client
	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(bucketClaim, seaweedCluster).
		Build()

	// Create reconciler
	reconciler := &BucketClaimReconciler{
		Client: client,
		Scheme: scheme,
		Log:    zap.NewNop().Sugar(),
	}

	// Test secret creation
	secretInfo, err := reconciler.createS3CredentialsSecret(context.Background(), bucketClaim, seaweedCluster)
	require.NoError(t, err)
	assert.NotNil(t, secretInfo)
	assert.Equal(t, "test-bucket-credentials", secretInfo.Name)
	assert.Equal(t, "default", secretInfo.Namespace)

	// Verify secret was created
	secret := &corev1.Secret{}
	err = client.Get(context.Background(), types.NamespacedName{
		Name:      "test-bucket-credentials",
		Namespace: "default",
	}, secret)
	require.NoError(t, err)

	// Verify secret data
	assert.Contains(t, secret.Data, "access-key-id")
	assert.Contains(t, secret.Data, "secret-access-key")
	assert.Contains(t, secret.Data, "endpoint")
	assert.Contains(t, secret.Data, "region")
	assert.Contains(t, secret.Data, "bucket")

	// Verify labels and annotations
	assert.Equal(t, "test-value", secret.Labels["test-label"])
	assert.Equal(t, "test-value", secret.Annotations["test-annotation"])
	assert.Equal(t, "test-bucket", secret.Labels["seaweed.seaweedfs.com/bucket"])
	assert.Equal(t, "bucketclaim-controller", secret.Annotations["seaweed.seaweedfs.com/created-by"])

	// Verify owner reference
	assert.Len(t, secret.OwnerReferences, 1)
	assert.Equal(t, "BucketClaim", secret.OwnerReferences[0].Kind)
	assert.Equal(t, "test-bucket", secret.OwnerReferences[0].Name)
}

func TestBucketClaimReconciler_CreateS3CredentialsSecret_Disabled(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, seaweedv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Create a test BucketClaim with secret disabled
	bucketClaim := &seaweedv1.BucketClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-bucket",
			Namespace: "default",
		},
		Spec: seaweedv1.BucketClaimSpec{
			BucketName: "test-bucket",
			Region:     "us-east-1",
			ClusterRef: seaweedv1.ClusterReference{
				Name:      "test-cluster",
				Namespace: "default",
			},
			Secret: &seaweedv1.BucketSecretSpec{
				Enabled: false,
			},
		},
	}

	// Create a test Seaweed cluster
	seaweedCluster := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: seaweedv1.SeaweedSpec{
			Filer: &seaweedv1.FilerSpec{
				S3: &seaweedv1.S3Config{
					Enabled: true,
				},
			},
		},
	}

	// Create fake client
	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(bucketClaim, seaweedCluster).
		Build()

	// Create reconciler
	reconciler := &BucketClaimReconciler{
		Client: client,
		Scheme: scheme,
		Log:    zap.NewNop().Sugar(),
	}

	// Test secret creation (should return nil when disabled)
	secretInfo, err := reconciler.createS3CredentialsSecret(context.Background(), bucketClaim, seaweedCluster)
	require.NoError(t, err)
	assert.Nil(t, secretInfo)
}

func TestBucketClaimReconciler_SetupWithManager(t *testing.T) {
	// Create a fake client
	scheme := runtime.NewScheme()
	_ = seaweedv1.AddToScheme(scheme)

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	// Create the reconciler
	reconciler := &BucketClaimReconciler{
		Client: client,
		Log:    zap.NewNop().Sugar(),
		Scheme: scheme,
	}

	// Create a mock manager
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
	})
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// Test that the reconciler can be set up with the manager
	err = reconciler.SetupWithManager(mgr)
	if err != nil {
		t.Fatalf("Failed to setup reconciler with manager: %v", err)
	}
}

func TestBucketClaimReconciler_getSeaweedCluster(t *testing.T) {
	// Create a fake client
	scheme := runtime.NewScheme()
	_ = seaweedv1.AddToScheme(scheme)

	// Create a test Seaweed cluster
	seaweedCluster := &seaweedv1.Seaweed{
		Spec: seaweedv1.SeaweedSpec{
			Version: "3.67",
			Master: &seaweedv1.MasterSpec{
				Replicas: 1,
			},
			Volume: &seaweedv1.VolumeSpec{
				Replicas: 3,
			},
			Filer: &seaweedv1.FilerSpec{
				Replicas: 1,
				S3: &seaweedv1.S3Config{
					Enabled: true,
				},
			},
			Admin: &seaweedv1.AdminSpec{
				Replicas: 1,
				Port:     func() *int32 { p := int32(23646); return &p }(),
			},
		},
	}
	seaweedCluster.Name = "test-seaweed"
	seaweedCluster.Namespace = "default"

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(seaweedCluster).
		Build()

	// Create the reconciler
	reconciler := &BucketClaimReconciler{
		Client: client,
		Log:    zap.NewNop().Sugar(),
		Scheme: scheme,
	}

	// Create a test BucketClaim
	bucketClaim := &seaweedv1.BucketClaim{
		Spec: seaweedv1.BucketClaimSpec{
			BucketName: "test-bucket",
			ClusterRef: seaweedv1.ClusterReference{
				Name:      "test-seaweed",
				Namespace: "default",
			},
		},
	}

	// Test getting the Seaweed cluster
	ctx := context.Background()
	cluster, err := reconciler.getSeaweedCluster(ctx, bucketClaim)
	if err != nil {
		t.Fatalf("Failed to get Seaweed cluster: %v", err)
	}

	if cluster.Name != "test-seaweed" {
		t.Errorf("Expected cluster name 'test-seaweed', got '%s'", cluster.Name)
	}
}

func TestBucketClaimReconciler_getAdminService(t *testing.T) {
	// Create a test Seaweed cluster with admin
	seaweedCluster := &seaweedv1.Seaweed{
		Spec: seaweedv1.SeaweedSpec{
			Admin: &seaweedv1.AdminSpec{
				Replicas: 1,
				Port:     func() *int32 { p := int32(23646); return &p }(),
			},
		},
	}
	seaweedCluster.Name = "test-seaweed"
	seaweedCluster.Namespace = "default"

	// Create the reconciler
	reconciler := &BucketClaimReconciler{}

	// Test getting admin service URL
	adminURL, err := reconciler.getAdminService(seaweedCluster)
	if err != nil {
		t.Fatalf("Failed to get admin service: %v", err)
	}

	expectedURL := "http://test-seaweed-admin.default.svc.cluster.local:23646"
	if adminURL != expectedURL {
		t.Errorf("Expected admin URL '%s', got '%s'", expectedURL, adminURL)
	}
}

func TestBucketClaimReconciler_getAdminService_NoAdmin(t *testing.T) {
	// Create a test Seaweed cluster without admin
	seaweedCluster := &seaweedv1.Seaweed{
		Spec: seaweedv1.SeaweedSpec{
			// No admin spec
		},
	}
	seaweedCluster.Name = "test-seaweed"
	seaweedCluster.Namespace = "default"

	// Create the reconciler
	reconciler := &BucketClaimReconciler{}

	// Test getting admin service URL should fail
	_, err := reconciler.getAdminService(seaweedCluster)
	if err == nil {
		t.Fatal("Expected error when admin service is not enabled")
	}

	expectedError := "admin service is not enabled for Seaweed cluster test-seaweed"
	if err.Error() != expectedError {
		t.Errorf("Expected error '%s', got '%s'", expectedError, err.Error())
	}
}

func TestBucketClaimReconciler_AdminServerCleanup(t *testing.T) {
	// Create the reconciler
	reconciler := &BucketClaimReconciler{
		adminServers: make(map[string]*adminServerEntry),
	}

	// Test admin server creation and access time update
	adminService1 := "http://test1:8080"
	adminService2 := "http://test2:8080"

	// Get admin server for the first time
	server1, err := reconciler.getAdminServer(adminService1)
	if err != nil {
		t.Fatalf("Failed to get admin server: %v", err)
	}
	if server1 == nil {
		t.Fatal("Expected admin server to be created")
	}

	// Check that entry was created
	entry1, exists := reconciler.adminServers[adminService1]
	if !exists {
		t.Fatal("Expected admin server entry to be created")
	}

	// Get admin server for the second time - should update access time
	time.Sleep(10 * time.Millisecond) // Small delay to ensure time difference
	server1Again, err := reconciler.getAdminServer(adminService1)
	if err != nil {
		t.Fatalf("Failed to get admin server again: %v", err)
	}
	if server1Again != server1 {
		t.Fatal("Expected same admin server instance")
	}

	// Check that access time was updated
	entry1Updated, exists := reconciler.adminServers[adminService1]
	if !exists {
		t.Fatal("Expected admin server entry to still exist")
	}
	if entry1Updated.lastAccess.Before(entry1.lastAccess) {
		t.Fatal("Expected access time to be updated")
	}

	// Get another admin server
	server2, err := reconciler.getAdminServer(adminService2)
	if err != nil {
		t.Fatalf("Failed to get second admin server: %v", err)
	}
	if server2 == nil {
		t.Fatal("Expected second admin server to be created")
	}

	// Check that both entries exist
	if len(reconciler.adminServers) != 2 {
		t.Fatalf("Expected 2 admin server entries, got %d", len(reconciler.adminServers))
	}

	// Test cleanup functionality
	// Set the first entry to be old (more than 5 minutes)
	entry1.lastAccess = time.Now().Add(-6 * time.Minute)

	// Run cleanup
	reconciler.cleanupInactiveAdminServers()

	// Check that only the old entry was removed
	if len(reconciler.adminServers) != 1 {
		t.Fatalf("Expected 1 admin server entry after cleanup, got %d", len(reconciler.adminServers))
	}

	// Check that the second entry still exists
	if _, exists := reconciler.adminServers[adminService2]; !exists {
		t.Fatal("Expected second admin server entry to still exist after cleanup")
	}

	// Check that the first entry was removed
	if _, exists := reconciler.adminServers[adminService1]; exists {
		t.Fatal("Expected first admin server entry to be removed after cleanup")
	}
}
