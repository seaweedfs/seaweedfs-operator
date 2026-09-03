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
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func finalizerTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("clientgoscheme: %v", err)
	}
	if err := seaweedv1.AddToScheme(scheme); err != nil {
		t.Fatalf("seaweedv1: %v", err)
	}
	return scheme
}

func TestReleaseFinalizerIfDeleting(t *testing.T) {
	scheme := finalizerTestScheme(t)
	ctx := context.Background()

	t.Run("live object is left alone", func(t *testing.T) {
		bucket := newTestBucket("photos")
		bucket.Finalizers = []string{BucketFinalizer}
		cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(bucket).Build()

		handled, err := releaseFinalizerIfDeleting(ctx, cli, bucket, BucketFinalizer, true, nil, "seaweedfs/prod")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if handled {
			t.Error("an object that is not being deleted must fall through to normal reconciliation")
		}
		if len(bucket.Finalizers) != 1 {
			t.Errorf("finalizer was removed from a live object: %v", bucket.Finalizers)
		}
	})

	t.Run("deleting object without remote cleanup has its finalizer dropped", func(t *testing.T) {
		now := metav1.Now()
		bucket := newTestBucket("photos")
		bucket.Finalizers = []string{BucketFinalizer}
		bucket.DeletionTimestamp = &now
		cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(bucket).Build()

		handled, err := releaseFinalizerIfDeleting(ctx, cli, bucket, BucketFinalizer, false, nil, "seaweedfs/prod")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !handled {
			t.Fatal("expected the deletion to be handled")
		}

		// No other holders, so the fake client reaps it immediately.
		got := &seaweedv1.Bucket{}
		key := types.NamespacedName{Namespace: bucket.Namespace, Name: bucket.Name}
		if err := cli.Get(ctx, key, got); !apierrors.IsNotFound(err) {
			t.Fatalf("expected the object to be gone, got %v with finalizers %v", err, got.Finalizers)
		}
	})

	t.Run("finalizer already absent is still handled", func(t *testing.T) {
		now := metav1.Now()
		bucket := newTestBucket("photos")
		// A second finalizer keeps the object alive for the assertion.
		bucket.Finalizers = []string{"example.com/other"}
		bucket.DeletionTimestamp = &now
		cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(bucket).Build()

		handled, err := releaseFinalizerIfDeleting(ctx, cli, bucket, BucketFinalizer, true, nil, "seaweedfs/prod")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !handled {
			t.Error("a deleting object must be reported as handled even when our finalizer is already gone")
		}
		if len(bucket.Finalizers) != 1 || bucket.Finalizers[0] != "example.com/other" {
			t.Errorf("another controller's finalizer must survive, got %v", bucket.Finalizers)
		}
	})

	t.Run("Delete cleanup waits while namespace is live", func(t *testing.T) {
		now := metav1.Now()
		bucket := newTestBucket("photos")
		bucket.Finalizers = []string{BucketFinalizer}
		bucket.DeletionTimestamp = &now
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: bucket.Namespace}}
		recorder := record.NewFakeRecorder(1)
		cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(namespace, bucket).Build()

		handled, err := releaseFinalizerIfDeleting(ctx, cli, bucket, BucketFinalizer, true, recorder, "seaweedfs/prod")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if handled {
			t.Fatal("remote cleanup must remain pending while the namespace is live")
		}
		if !containsFinalizer(bucket.Finalizers, BucketFinalizer) {
			t.Fatalf("cleanup finalizer was removed: %v", bucket.Finalizers)
		}
		select {
		case event := <-recorder.Events:
			if !strings.Contains(event, "CleanupBlocked") {
				t.Fatalf("event = %q, want CleanupBlocked", event)
			}
		default:
			t.Fatal("expected a CleanupBlocked warning event")
		}
	})

	t.Run("annotation explicitly abandons Delete cleanup", func(t *testing.T) {
		now := metav1.Now()
		bucket := newTestBucket("photos")
		bucket.Finalizers = []string{BucketFinalizer}
		bucket.DeletionTimestamp = &now
		bucket.Annotations = map[string]string{AllowCleanupAbandonmentAnnotation: "true"}
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: bucket.Namespace}}
		recorder := record.NewFakeRecorder(1)
		cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(namespace, bucket).Build()

		handled, err := releaseFinalizerIfDeleting(ctx, cli, bucket, BucketFinalizer, true, recorder, "seaweedfs/prod")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !handled {
			t.Fatal("explicit cleanup abandonment must permit finalization")
		}
		select {
		case event := <-recorder.Events:
			if !strings.Contains(event, "CleanupAbandoned") {
				t.Fatalf("event = %q, want CleanupAbandoned", event)
			}
		default:
			t.Fatal("expected a CleanupAbandoned warning event")
		}
	})
}

func containsFinalizer(finalizers []string, want string) bool {
	for _, finalizer := range finalizers {
		if finalizer == want {
			return true
		}
	}
	return false
}

// A terminating namespace must not be stuck forever when its Seaweed cluster
// was deleted first. Namespace termination is an explicit abandonment boundary.
func TestReconcile_ClusterGoneWhileDeleting_ReleasesBucket(t *testing.T) {
	now := metav1.Now()
	bucket := newTestBucket("photos")
	bucket.Spec.ReclaimPolicy = seaweedv1.BucketReclaimDelete
	bucket.Status.BucketName = "photos"
	bucket.Finalizers = []string{BucketFinalizer}
	bucket.DeletionTimestamp = &now
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: bucket.Namespace, DeletionTimestamp: &now, Finalizers: []string{"test.example/hold"}},
		Spec:       corev1.NamespaceSpec{Finalizers: []corev1.FinalizerName{corev1.FinalizerKubernetes}},
	}

	fa := newFakeAdmin()
	// Deliberately no newTestSeaweed(): the cluster is already gone.
	r, cli := testReconciler(t, fa, namespace, bucket)
	key := types.NamespacedName{Namespace: bucket.Namespace, Name: bucket.Name}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := &seaweedv1.Bucket{}
	if err := cli.Get(context.Background(), key, got); !apierrors.IsNotFound(err) {
		t.Fatalf("bucket still present after its cluster was deleted: %v, finalizers %v", err, got.Finalizers)
	}

	for _, c := range fa.calls {
		if c != "" {
			t.Errorf("no admin call should be attempted against a cluster that does not exist, got: %s", c)
		}
	}
}

func TestReconcile_ClusterGoneDeleteWaitsForCleanup(t *testing.T) {
	now := metav1.Now()
	bucket := newTestBucket("photos")
	bucket.Spec.ReclaimPolicy = seaweedv1.BucketReclaimDelete
	bucket.Status.BucketName = "photos"
	bucket.Finalizers = []string{BucketFinalizer}
	bucket.DeletionTimestamp = &now
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: bucket.Namespace}}

	r, cli := testReconciler(t, newFakeAdmin(), namespace, bucket)
	key := types.NamespacedName{Namespace: bucket.Namespace, Name: bucket.Name}
	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatal("missing cluster cleanup should be retried")
	}

	got := &seaweedv1.Bucket{}
	if err := cli.Get(context.Background(), key, got); err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	if !containsFinalizer(got.Finalizers, BucketFinalizer) {
		t.Fatalf("Delete cleanup finalizer was removed while cluster is absent: %v", got.Finalizers)
	}
}

func TestS3Credentials_ClusterGoneDeletePreservesRemoteCleanup(t *testing.T) {
	now := metav1.Now()
	credential := &seaweedv1.S3Credentials{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "alice-creds",
			Namespace:         "media",
			Finalizers:        []string{s3CredentialsFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: seaweedv1.S3CredentialsSpec{
			SeaweedRef:  iamSeaweedRef(),
			IdentityRef: seaweedv1.S3IdentityRef{Name: "alice"},
			SecretRef:   seaweedv1.S3SecretRef{Name: "alice-secret"},
		},
		Status: seaweedv1.S3CredentialsStatus{
			AccessKey:    "AKIAEXAMPLE",
			IdentityName: "alice",
			SecretName:   "alice-secret",
		},
	}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: credential.Namespace}}
	scheme := iamTestScheme(t)
	cli := iamTestClient(t, scheme, namespace, credential) // no Seaweed cluster
	r := &S3CredentialsReconciler{Client: cli}
	key := types.NamespacedName{Namespace: credential.Namespace, Name: credential.Name}

	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatal("missing cluster cleanup should be retried")
	}

	got := &seaweedv1.S3Credentials{}
	if err := cli.Get(context.Background(), key, got); err != nil {
		t.Fatalf("get credential: %v", err)
	}
	if !containsFinalizer(got.Finalizers, s3CredentialsFinalizer) {
		t.Fatalf("Delete cleanup finalizer was removed while cluster is absent: %v", got.Finalizers)
	}
}

// The API permits an unset name override to be set once. If that first value
// differs from the metadata-name fallback already recorded in status, normal
// reconciliation rejects the rename. Deletion must nevertheless release the
// finalizer when the target cluster no longer exists.
func TestReconcile_ClusterGoneWhileDeleting_RenameMismatchDoesNotBlock(t *testing.T) {
	now := metav1.Now()

	t.Run("bucket", func(t *testing.T) {
		bucket := newTestBucket("photos")
		bucket.Spec.Name = "renamed-photos"
		bucket.Status.BucketName = "photos"
		bucket.Finalizers = []string{BucketFinalizer}
		bucket.DeletionTimestamp = &now

		r, cli := testReconciler(t, newFakeAdmin(), bucket)
		key := types.NamespacedName{Namespace: bucket.Namespace, Name: bucket.Name}
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		if err := cli.Get(context.Background(), key, &seaweedv1.Bucket{}); !apierrors.IsNotFound(err) {
			t.Fatalf("deleting Bucket remains after missing-cluster reconcile: %v", err)
		}
	})

	t.Run("identity", func(t *testing.T) {
		scheme := finalizerTestScheme(t)
		identity := &seaweedv1.S3Identity{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "alice",
				Namespace:         "media",
				Finalizers:        []string{s3IdentityFinalizer},
				DeletionTimestamp: &now,
			},
			Spec: seaweedv1.S3IdentitySpec{
				SeaweedRef:    seaweedv1.SeaweedReference{Name: "prod"},
				Name:          "renamed-alice",
				ReclaimPolicy: seaweedv1.S3ReclaimRetain,
			},
			Status: seaweedv1.S3IdentityStatus{IdentityName: "alice"},
		}
		cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(identity).Build()
		r := &S3IdentityReconciler{Client: cli}
		key := types.NamespacedName{Namespace: identity.Namespace, Name: identity.Name}
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		if err := cli.Get(context.Background(), key, &seaweedv1.S3Identity{}); !apierrors.IsNotFound(err) {
			t.Fatalf("deleting S3Identity remains after missing-cluster reconcile: %v", err)
		}
	})

	t.Run("policy", func(t *testing.T) {
		scheme := finalizerTestScheme(t)
		policy := &seaweedv1.S3Policy{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "read",
				Namespace:         "media",
				Finalizers:        []string{s3PolicyFinalizer},
				DeletionTimestamp: &now,
			},
			Spec: seaweedv1.S3PolicySpec{
				SeaweedRef:    seaweedv1.SeaweedReference{Name: "prod"},
				Name:          "renamed-read",
				ReclaimPolicy: seaweedv1.S3ReclaimRetain,
			},
			Status: seaweedv1.S3PolicyStatus{PolicyName: "read"},
		}
		cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy).Build()
		r := &S3PolicyReconciler{Client: cli}
		key := types.NamespacedName{Namespace: policy.Namespace, Name: policy.Name}
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		if err := cli.Get(context.Background(), key, &seaweedv1.S3Policy{}); !apierrors.IsNotFound(err) {
			t.Fatalf("deleting S3Policy remains after missing-cluster reconcile: %v", err)
		}
	})
}

func TestReconcile_DeletingRenameUsesStatusPinnedName(t *testing.T) {
	now := metav1.Now()

	t.Run("bucket", func(t *testing.T) {
		bucket := newTestBucket("photos")
		bucket.Spec.Name = "renamed-photos"
		bucket.Spec.ReclaimPolicy = seaweedv1.BucketReclaimDelete
		bucket.Status.BucketName = "photos"
		bucket.Finalizers = []string{BucketFinalizer}
		bucket.DeletionTimestamp = &now

		fa := newFakeAdmin()
		fa.existsResp["photos"] = true
		r, cli := testReconciler(t, fa, newTestSeaweed(), bucket)
		key := types.NamespacedName{Namespace: bucket.Namespace, Name: bucket.Name}
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		if err := cli.Get(context.Background(), key, &seaweedv1.Bucket{}); !apierrors.IsNotFound(err) {
			t.Fatalf("deleting Bucket remains: %v", err)
		}
		calls := strings.Join(fa.calls, "\n")
		if !strings.Contains(calls, "Delete:photos") || strings.Contains(calls, "Delete:renamed-photos") {
			t.Fatalf("bucket deletion did not use status-pinned name; calls:\n%s", calls)
		}
	})

	t.Run("identity", func(t *testing.T) {
		scheme := iamTestScheme(t)
		identity := &seaweedv1.S3Identity{
			ObjectMeta: metav1.ObjectMeta{Name: "alice", Namespace: "media", Finalizers: []string{s3IdentityFinalizer}, DeletionTimestamp: &now},
			Spec: seaweedv1.S3IdentitySpec{
				SeaweedRef:    iamSeaweedRef(),
				Name:          "renamed-alice",
				ReclaimPolicy: seaweedv1.S3ReclaimDelete,
			},
			Status: seaweedv1.S3IdentityStatus{IdentityName: "alice"},
		}
		cli := iamTestClient(t, scheme, newTestSeaweed(), identity)
		fa := newFakeIAMAdmin()
		fa.seedUser("alice")
		r := &S3IdentityReconciler{Client: cli}
		r.AdminFactory = fakeIAMFactory(fa)
		key := types.NamespacedName{Namespace: identity.Namespace, Name: identity.Name}
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		if err := cli.Get(context.Background(), key, &seaweedv1.S3Identity{}); !apierrors.IsNotFound(err) {
			t.Fatalf("deleting S3Identity remains: %v", err)
		}
		calls := strings.Join(fa.calls, "\n")
		if !strings.Contains(calls, "DeleteUser:alice") || strings.Contains(calls, "DeleteUser:renamed-alice") {
			t.Fatalf("identity deletion did not use status-pinned name; calls:\n%s", calls)
		}
	})

	t.Run("policy", func(t *testing.T) {
		scheme := iamTestScheme(t)
		policy := &seaweedv1.S3Policy{
			ObjectMeta: metav1.ObjectMeta{Name: "read", Namespace: "media", Finalizers: []string{s3PolicyFinalizer}, DeletionTimestamp: &now},
			Spec: seaweedv1.S3PolicySpec{
				SeaweedRef:    iamSeaweedRef(),
				Name:          "renamed-read",
				ReclaimPolicy: seaweedv1.S3ReclaimDelete,
			},
			Status: seaweedv1.S3PolicyStatus{PolicyName: "read"},
		}
		cli := iamTestClient(t, scheme, newTestSeaweed(), policy)
		fa := newFakeIAMAdmin()
		fa.policies["read"] = "{}"
		r := &S3PolicyReconciler{Client: cli}
		r.AdminFactory = fakeIAMFactory(fa)
		key := types.NamespacedName{Namespace: policy.Namespace, Name: policy.Name}
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		if err := cli.Get(context.Background(), key, &seaweedv1.S3Policy{}); !apierrors.IsNotFound(err) {
			t.Fatalf("deleting S3Policy remains: %v", err)
		}
		calls := strings.Join(fa.calls, "\n")
		if !strings.Contains(calls, "DeletePolicy:read") || strings.Contains(calls, "DeletePolicy:renamed-read") {
			t.Fatalf("policy deletion did not use status-pinned name; calls:\n%s", calls)
		}
	})
}

// A rename attempt must not strand the finalizer. The rename guard runs before
// the deletion path, so a Bucket whose spec.name diverged from
// status.bucketName could never be deleted — the same stuck-finalizer symptom
// as a missing cluster, from a different trigger.
func TestReconcile_RenamedWhileDeleting_StillReleases(t *testing.T) {
	now := metav1.Now()
	bucket := newTestBucket("photos")
	bucket.Spec.ReclaimPolicy = seaweedv1.BucketReclaimRetain
	// Provisioned as "photos", spec since changed — a rename the operator refuses.
	bucket.Status.BucketName = "photos-original"
	bucket.Finalizers = []string{BucketFinalizer}
	bucket.DeletionTimestamp = &now

	fa := newFakeAdmin()
	r, cli := testReconciler(t, fa, newTestSeaweed(), bucket)
	key := types.NamespacedName{Namespace: bucket.Namespace, Name: bucket.Name}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := &seaweedv1.Bucket{}
	if err := cli.Get(context.Background(), key, got); err == nil {
		t.Errorf("a renamed bucket could not be deleted; still present with finalizers %v", got.Finalizers)
	}
}

// The guard must still refuse a rename on a live object.
func TestReconcile_RenamedWhileLive_StillRefused(t *testing.T) {
	bucket := newTestBucket("photos")
	bucket.Status.BucketName = "photos-original"

	fa := newFakeAdmin()
	r, cli := testReconciler(t, fa, newTestSeaweed(), bucket)
	key := types.NamespacedName{Namespace: bucket.Namespace, Name: bucket.Name}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := &seaweedv1.Bucket{}
	if err := cli.Get(context.Background(), key, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Phase != seaweedv1.BucketPhaseFailed {
		t.Errorf("phase = %q, want Failed for a rename on a live bucket", got.Status.Phase)
	}
}
