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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

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
		rec := record.NewFakeRecorder(1)

		handled, err := releaseFinalizerIfDeleting(ctx, cli, rec, bucket, BucketFinalizer, "seaweedfs/prod")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if handled {
			t.Error("an object that is not being deleted must fall through to normal reconciliation")
		}
		if len(bucket.Finalizers) != 1 {
			t.Errorf("finalizer was removed from a live object: %v", bucket.Finalizers)
		}
		if len(rec.Events) != 0 {
			t.Errorf("no event expected for a live object, got %q", <-rec.Events)
		}
	})

	t.Run("deleting object has its finalizer dropped", func(t *testing.T) {
		now := metav1.Now()
		bucket := newTestBucket("photos")
		bucket.Finalizers = []string{BucketFinalizer}
		bucket.DeletionTimestamp = &now
		cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(bucket).Build()
		rec := record.NewFakeRecorder(1)

		handled, err := releaseFinalizerIfDeleting(ctx, cli, rec, bucket, BucketFinalizer, "seaweedfs/prod")
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

		// With the object and the cluster both gone, the Event is the only
		// trace that reclaim was skipped.
		select {
		case ev := <-rec.Events:
			if !strings.Contains(ev, "Warning FinalizerReleased") || !strings.Contains(ev, `"seaweedfs/prod"`) {
				t.Errorf("event = %q, want a FinalizerReleased warning naming the missing cluster", ev)
			}
		default:
			t.Error("expected a FinalizerReleased event to be recorded")
		}
	})

	t.Run("finalizer already absent is still handled", func(t *testing.T) {
		now := metav1.Now()
		bucket := newTestBucket("photos")
		// A second finalizer keeps the object alive for the assertion.
		bucket.Finalizers = []string{"example.com/other"}
		bucket.DeletionTimestamp = &now
		cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(bucket).Build()
		rec := record.NewFakeRecorder(1)

		handled, err := releaseFinalizerIfDeleting(ctx, cli, rec, bucket, BucketFinalizer, "seaweedfs/prod")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !handled {
			t.Error("a deleting object must be reported as handled even when our finalizer is already gone")
		}
		if len(bucket.Finalizers) != 1 || bucket.Finalizers[0] != "example.com/other" {
			t.Errorf("another controller's finalizer must survive, got %v", bucket.Finalizers)
		}
		if len(rec.Events) != 0 {
			t.Errorf("nothing was released, so no event expected, got %q", <-rec.Events)
		}
	})

	t.Run("nil recorder is tolerated", func(t *testing.T) {
		now := metav1.Now()
		bucket := newTestBucket("photos")
		bucket.Finalizers = []string{BucketFinalizer}
		bucket.DeletionTimestamp = &now
		cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(bucket).Build()

		if handled, err := releaseFinalizerIfDeleting(ctx, cli, nil, bucket, BucketFinalizer, "seaweedfs/prod"); !handled || err != nil {
			t.Fatalf("handled=%v err=%v, want handled with no error", handled, err)
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
	if err := cli.Get(context.Background(), key, got); !apierrors.IsNotFound(err) {
		t.Fatalf("bucket still present after its cluster was deleted: %v, finalizers %v", err, got.Finalizers)
	}

	for _, c := range fa.calls {
		if c != "" {
			t.Errorf("no admin call should be attempted against a cluster that does not exist, got: %s", c)
		}
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
				SeaweedRef: seaweedv1.SeaweedReference{Name: "prod"},
				Name:       "renamed-alice",
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
				SeaweedRef: seaweedv1.SeaweedReference{Name: "prod"},
				Name:       "renamed-read",
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

// Letting a renamed object through to deletion is only safe if deletion
// targets what was provisioned. Status pins that name; spec now carries the
// refused rename, which names something this CR never created — and, for IAM,
// possibly someone else's user or policy. reclaimPolicy: Delete must remove
// the former and leave the latter alone.
func TestReconcile_RenamedWhileDeleting_DeletesProvisionedName(t *testing.T) {
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
		fa.existsResp["renamed-photos"] = true // a foreign bucket that happens to carry the new name
		r, cli := testReconciler(t, fa, newTestSeaweed(), bucket)
		key := types.NamespacedName{Namespace: bucket.Namespace, Name: bucket.Name}
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatalf("reconcile: %v", err)
		}

		if !hasCall(fa.calls, "Delete:photos") {
			t.Errorf("the provisioned bucket was orphaned; calls=%v", fa.calls)
		}
		if _, foreign := fa.existsResp["renamed-photos"]; !foreign {
			t.Errorf("a bucket this CR never provisioned was deleted; calls=%v", fa.calls)
		}
		if err := cli.Get(context.Background(), key, &seaweedv1.Bucket{}); !apierrors.IsNotFound(err) {
			t.Fatalf("deleting Bucket remains: %v", err)
		}
	})

	t.Run("identity", func(t *testing.T) {
		scheme := iamTestScheme(t)
		identity := &seaweedv1.S3Identity{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "alice",
				Namespace:         "media",
				Finalizers:        []string{s3IdentityFinalizer},
				DeletionTimestamp: &now,
			},
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
		fa.seedUser("renamed-alice") // a foreign user that happens to carry the new name
		r := &S3IdentityReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
		r.AdminFactory = fakeIAMFactory(fa)
		key := types.NamespacedName{Namespace: identity.Namespace, Name: identity.Name}
		reconcileOnce(t, r, key)

		if _, err := fa.GetUser(context.Background(), "alice"); err == nil {
			t.Errorf("the provisioned IAM user was orphaned; calls=%v", fa.calls)
		}
		if _, err := fa.GetUser(context.Background(), "renamed-alice"); err != nil {
			t.Errorf("an IAM user this CR never provisioned was deleted; calls=%v", fa.calls)
		}
		if err := cli.Get(context.Background(), key, &seaweedv1.S3Identity{}); !apierrors.IsNotFound(err) {
			t.Fatalf("deleting S3Identity remains: %v", err)
		}
	})

	t.Run("policy", func(t *testing.T) {
		scheme := iamTestScheme(t)
		policy := &seaweedv1.S3Policy{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "read",
				Namespace:         "media",
				Finalizers:        []string{s3PolicyFinalizer},
				DeletionTimestamp: &now,
			},
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
		fa.policies["renamed-read"] = "{}" // a foreign policy that happens to carry the new name
		r := &S3PolicyReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
		r.AdminFactory = fakeIAMFactory(fa)
		key := types.NamespacedName{Namespace: policy.Namespace, Name: policy.Name}
		reconcileOnce(t, r, key)

		if _, err := fa.GetPolicy(context.Background(), "read"); err == nil {
			t.Errorf("the provisioned IAM policy was orphaned; calls=%v", fa.calls)
		}
		if _, err := fa.GetPolicy(context.Background(), "renamed-read"); err != nil {
			t.Errorf("an IAM policy this CR never provisioned was deleted; calls=%v", fa.calls)
		}
		if err := cli.Get(context.Background(), key, &seaweedv1.S3Policy{}); !apierrors.IsNotFound(err) {
			t.Fatalf("deleting S3Policy remains: %v", err)
		}
	})
}
