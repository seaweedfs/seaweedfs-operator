package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
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

		handled, err := releaseFinalizerIfDeleting(ctx, cli, bucket, BucketFinalizer)
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

	t.Run("deleting object has its finalizer dropped", func(t *testing.T) {
		now := metav1.Now()
		bucket := newTestBucket("photos")
		bucket.Finalizers = []string{BucketFinalizer}
		bucket.DeletionTimestamp = &now
		cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(bucket).Build()

		handled, err := releaseFinalizerIfDeleting(ctx, cli, bucket, BucketFinalizer)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !handled {
			t.Fatal("expected the deletion to be handled")
		}

		// No other holders, so the fake client reaps it immediately.
		got := &seaweedv1.Bucket{}
		key := types.NamespacedName{Namespace: bucket.Namespace, Name: bucket.Name}
		if err := cli.Get(ctx, key, got); err == nil {
			t.Errorf("expected the object to be gone, still present with finalizers %v", got.Finalizers)
		}
	})

	t.Run("finalizer already absent is still handled", func(t *testing.T) {
		now := metav1.Now()
		bucket := newTestBucket("photos")
		// A second finalizer keeps the object alive for the assertion.
		bucket.Finalizers = []string{"example.com/other"}
		bucket.DeletionTimestamp = &now
		cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(bucket).Build()

		handled, err := releaseFinalizerIfDeleting(ctx, cli, bucket, BucketFinalizer)
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
}

// A Bucket whose cluster was deleted first must not be stuck forever. The
// cluster lookup used to return RequeueAfter before the deletion path was
// reached, so the finalizer was never removed and the object could not go away.
func TestReconcile_ClusterGoneWhileDeleting_ReleasesBucket(t *testing.T) {
	now := metav1.Now()
	bucket := newTestBucket("photos")
	bucket.Spec.ReclaimPolicy = seaweedv1.BucketReclaimDelete
	bucket.Status.BucketName = "photos"
	bucket.Finalizers = []string{BucketFinalizer}
	bucket.DeletionTimestamp = &now

	fa := newFakeAdmin()
	// Deliberately no newTestSeaweed(): the cluster is already gone.
	r, cli := testReconciler(t, fa, bucket)
	key := types.NamespacedName{Namespace: bucket.Namespace, Name: bucket.Name}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := &seaweedv1.Bucket{}
	if err := cli.Get(context.Background(), key, got); err == nil {
		t.Errorf("bucket still present after its cluster was deleted, finalizers %v", got.Finalizers)
	}

	for _, c := range fa.calls {
		if c != "" {
			t.Errorf("no admin call should be attempted against a cluster that does not exist, got: %s", c)
		}
	}
}
