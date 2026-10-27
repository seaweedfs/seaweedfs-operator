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
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func testBucketPolicyDoc(resources ...string) string {
	return `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":["s3:GetObject"],"Resource":[` +
		`"` + strings.Join(resources, `","`) + `"],` +
		`"Condition":{"ForAnyValue:StringEquals":{"jwt:groups":"team-a"}}}]}`
}

func testBinding(name string, subjects ...seaweedv1.S3Subject) *seaweedv1.S3PolicyBinding {
	return &seaweedv1.S3PolicyBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media"},
		Spec: seaweedv1.S3PolicyBindingSpec{
			SeaweedRef: iamSeaweedRef(),
			PolicyRef:  seaweedv1.S3PolicyRef{Name: "rw"},
			Subjects:   subjects,
		},
	}
}

func newBindingReconciler(t *testing.T, fa *fakeIAMAdmin, objs ...client.Object) (*S3PolicyBindingReconciler, client.Client) {
	t.Helper()
	scheme := iamTestScheme(t)
	cli := iamTestClient(t, scheme, append([]client.Object{newTestSeaweed()}, objs...)...)
	r := &S3PolicyBindingReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)
	return r, cli
}

func getBinding(t *testing.T, cli client.Client, name string) *seaweedv1.S3PolicyBinding {
	t.Helper()
	var got seaweedv1.S3PolicyBinding
	if err := cli.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: name}, &got); err != nil {
		t.Fatalf("get S3PolicyBinding: %v", err)
	}
	return &got
}

func bindingReadyReason(b *seaweedv1.S3PolicyBinding) string {
	c := meta.FindStatusCondition(b.Status.Conditions, seaweedv1.S3ConditionReady)
	if c == nil {
		return ""
	}
	return c.Reason
}

func TestS3PolicyBinding_AppliesBucketPolicy(t *testing.T) {
	fa := newFakeIAMAdmin()
	fa.seedUser("alice")
	fa.seedBucket("data")
	doc := testBucketPolicyDoc("arn:aws:s3:::data/*", "arn:aws:s3:::data")
	if err := fa.PutPolicy(context.Background(), "rw", doc); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	binding := testBinding("rw-bind",
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindIdentity, Name: "alice"},
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindBucket, Name: "data"},
	)
	r, cli := newBindingReconciler(t, fa, binding)
	key := types.NamespacedName{Namespace: "media", Name: "rw-bind"}
	reconcileStable(t, r, key, 5)

	if got := string(fa.bucketPolicies["data"]); got != doc {
		t.Errorf("bucket policy = %q, want the policy document verbatim", got)
	}
	if got := fa.userPolicies("alice"); len(got) != 1 || got[0] != "rw" {
		t.Errorf("alice policies = %v, want [rw]", got)
	}
	got := getBinding(t, cli, "rw-bind")
	if got.Status.Phase != seaweedv1.S3PhaseReady {
		t.Errorf("phase = %q, want Ready", got.Status.Phase)
	}
	if len(got.Status.AttachedBuckets) != 1 || got.Status.AttachedBuckets[0] != "data" {
		t.Errorf("attachedBuckets = %v, want [data]", got.Status.AttachedBuckets)
	}
}

func TestS3PolicyBinding_BucketSubjectResolvesBucketCR(t *testing.T) {
	fa := newFakeIAMAdmin()
	fa.seedBucket("media-photos")
	doc := testBucketPolicyDoc("media-photos/*")
	if err := fa.PutPolicy(context.Background(), "rw", doc); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	bucket := newTestBucket("photos")
	bucket.Spec.Name = "media-photos"
	binding := testBinding("rw-bind",
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindBucket, Name: "photos"},
	)
	r, cli := newBindingReconciler(t, fa, bucket, binding)
	key := types.NamespacedName{Namespace: "media", Name: "rw-bind"}
	reconcileStable(t, r, key, 5)

	if got := string(fa.bucketPolicies["media-photos"]); got != doc {
		t.Errorf("bucket policy on media-photos = %q, want the policy document", got)
	}
	got := getBinding(t, cli, "rw-bind")
	if len(got.Status.AttachedBuckets) != 1 || got.Status.AttachedBuckets[0] != "media-photos" {
		t.Errorf("attachedBuckets = %v, want [media-photos]", got.Status.AttachedBuckets)
	}
}

func TestS3PolicyBinding_SharesOneDocumentAcrossBuckets(t *testing.T) {
	fa := newFakeIAMAdmin()
	fa.seedBucket("data")
	fa.seedBucket("media")
	doc := testBucketPolicyDoc("arn:aws:s3:::data/*", "arn:aws:s3:::media/*")
	if err := fa.PutPolicy(context.Background(), "rw", doc); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	binding := testBinding("rw-bind",
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindBucket, Name: "data"},
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindBucket, Name: "media"},
	)
	r, cli := newBindingReconciler(t, fa, binding)
	key := types.NamespacedName{Namespace: "media", Name: "rw-bind"}
	reconcileStable(t, r, key, 5)

	for _, b := range []string{"data", "media"} {
		if got := string(fa.bucketPolicies[b]); got != doc {
			t.Errorf("bucket policy on %s = %q, want the shared document", b, got)
		}
	}
	if got := getBinding(t, cli, "rw-bind"); got.Status.Phase != seaweedv1.S3PhaseReady {
		t.Errorf("phase = %q, want Ready", got.Status.Phase)
	}
}

func TestS3PolicyBinding_RejectsIdentityStyleDoc(t *testing.T) {
	fa := newFakeIAMAdmin()
	fa.seedBucket("data")
	if err := fa.PutPolicy(context.Background(), "rw",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::data/*"]}]}`); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	binding := testBinding("rw-bind",
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindBucket, Name: "data"},
	)
	r, cli := newBindingReconciler(t, fa, binding)
	key := types.NamespacedName{Namespace: "media", Name: "rw-bind"}
	reconcileOnce(t, r, key) // finalizer
	reconcileOnce(t, r, key)

	if got := fa.bucketPolicies["data"]; got != nil {
		t.Errorf("no bucket policy should be written for a Principal-less doc, got %q", got)
	}
	got := getBinding(t, cli, "rw-bind")
	if got.Status.Phase != seaweedv1.S3PhaseFailed || bindingReadyReason(got) != "PolicyNotBucketPolicy" {
		t.Errorf("phase/reason = %q/%q, want Failed/PolicyNotBucketPolicy", got.Status.Phase, bindingReadyReason(got))
	}
}

func TestS3PolicyBinding_RejectsForeignResources(t *testing.T) {
	fa := newFakeIAMAdmin()
	fa.seedBucket("data")
	doc := testBucketPolicyDoc("arn:aws:s3:::other-bucket/*")
	if err := fa.PutPolicy(context.Background(), "rw", doc); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	binding := testBinding("rw-bind",
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindBucket, Name: "data"},
	)
	r, cli := newBindingReconciler(t, fa, binding)
	key := types.NamespacedName{Namespace: "media", Name: "rw-bind"}
	reconcileOnce(t, r, key) // finalizer
	reconcileOnce(t, r, key)

	if got := fa.bucketPolicies["data"]; got != nil {
		t.Errorf("no bucket policy should be written when resources name no bound bucket, got %q", got)
	}
	got := getBinding(t, cli, "rw-bind")
	if bindingReadyReason(got) != "PolicyNotBucketPolicy" {
		t.Errorf("reason = %q, want PolicyNotBucketPolicy", bindingReadyReason(got))
	}
}

func TestS3PolicyBinding_MissingBucketStaysPending(t *testing.T) {
	fa := newFakeIAMAdmin()
	fa.seedBucket("data")
	doc := testBucketPolicyDoc("arn:aws:s3:::data/*", "arn:aws:s3:::ghost/*")
	if err := fa.PutPolicy(context.Background(), "rw", doc); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	binding := testBinding("rw-bind",
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindBucket, Name: "data"},
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindBucket, Name: "ghost"},
	)
	r, cli := newBindingReconciler(t, fa, binding)
	key := types.NamespacedName{Namespace: "media", Name: "rw-bind"}
	reconcileOnce(t, r, key) // finalizer
	res := reconcileOnce(t, r, key)
	if res.RequeueAfter == 0 {
		t.Fatal("expected requeue while one bucket is missing")
	}
	if got := string(fa.bucketPolicies["data"]); got != doc {
		t.Errorf("data should still get the policy despite ghost missing, got %q", got)
	}
	got := getBinding(t, cli, "rw-bind")
	if got.Status.Phase != seaweedv1.S3PhasePending {
		t.Errorf("phase = %q, want Pending", got.Status.Phase)
	}
	if len(got.Status.AttachedBuckets) != 1 || got.Status.AttachedBuckets[0] != "data" {
		t.Errorf("attachedBuckets = %v, want [data]", got.Status.AttachedBuckets)
	}
}

func TestS3PolicyBinding_RemovedBucketSubjectClearsPolicy(t *testing.T) {
	fa := newFakeIAMAdmin()
	fa.seedBucket("data")
	fa.seedBucket("media")
	doc := testBucketPolicyDoc("arn:aws:s3:::data/*", "arn:aws:s3:::media/*")
	if err := fa.PutPolicy(context.Background(), "rw", doc); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	binding := testBinding("rw-bind",
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindBucket, Name: "data"},
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindBucket, Name: "media"},
	)
	r, cli := newBindingReconciler(t, fa, binding)
	key := types.NamespacedName{Namespace: "media", Name: "rw-bind"}
	reconcileStable(t, r, key, 5)

	live := getBinding(t, cli, "rw-bind")
	live.Spec.Subjects = []seaweedv1.S3Subject{{Kind: seaweedv1.S3SubjectKindBucket, Name: "data"}}
	if err := cli.Update(context.Background(), live); err != nil {
		t.Fatalf("update: %v", err)
	}
	reconcileStable(t, r, key, 5)

	if got := fa.bucketPolicies["media"]; got != nil {
		t.Errorf("media policy after removal = %q, want cleared", got)
	}
	if got := string(fa.bucketPolicies["data"]); got != doc {
		t.Errorf("data policy should be unchanged, got %q", got)
	}
}

func TestS3PolicyBinding_BucketConflictOlderWins(t *testing.T) {
	fa := newFakeIAMAdmin()
	fa.seedBucket("data")
	doc := testBucketPolicyDoc("arn:aws:s3:::data/*")
	if err := fa.PutPolicy(context.Background(), "rw", doc); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	older := testBinding("aaa-older",
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindBucket, Name: "data"},
	)
	older.CreationTimestamp = metav1.NewTime(time.Unix(1000, 0))
	newer := testBinding("zzz-newer",
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindBucket, Name: "data"},
	)
	newer.CreationTimestamp = metav1.NewTime(time.Unix(2000, 0))
	r, cli := newBindingReconciler(t, fa, older, newer)
	olderKey := types.NamespacedName{Namespace: "media", Name: "aaa-older"}
	newerKey := types.NamespacedName{Namespace: "media", Name: "zzz-newer"}
	reconcileStable(t, r, olderKey, 5)
	reconcileOnce(t, r, newerKey) // finalizer
	reconcileOnce(t, r, newerKey)

	if got := getBinding(t, cli, "aaa-older"); got.Status.Phase != seaweedv1.S3PhaseReady {
		t.Errorf("older phase = %q, want Ready", got.Status.Phase)
	}
	got := getBinding(t, cli, "zzz-newer")
	if got.Status.Phase != seaweedv1.S3PhaseFailed || bindingReadyReason(got) != "BucketConflict" {
		t.Errorf("newer phase/reason = %q/%q, want Failed/BucketConflict", got.Status.Phase, bindingReadyReason(got))
	}
	if len(got.Status.AttachedBuckets) != 0 {
		t.Errorf("newer attachedBuckets = %v, want none", got.Status.AttachedBuckets)
	}
}

func TestS3PolicyBinding_DeleteClearsBucketPolicy(t *testing.T) {
	fa := newFakeIAMAdmin()
	fa.seedBucket("data")
	doc := testBucketPolicyDoc("arn:aws:s3:::data/*")
	if err := fa.PutPolicy(context.Background(), "rw", doc); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	binding := testBinding("rw-bind",
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindBucket, Name: "data"},
	)
	r, cli := newBindingReconciler(t, fa, binding)
	key := types.NamespacedName{Namespace: "media", Name: "rw-bind"}
	reconcileStable(t, r, key, 5)
	if got := fa.bucketPolicies["data"]; got == nil {
		t.Fatal("expected bucket policy to be applied before delete")
	}

	live := getBinding(t, cli, "rw-bind")
	if err := cli.Delete(context.Background(), live); err != nil {
		t.Fatalf("delete: %v", err)
	}
	reconcileStable(t, r, key, 5)

	if got := fa.bucketPolicies["data"]; got != nil {
		t.Errorf("bucket policy after delete = %q, want cleared", got)
	}
	var gone seaweedv1.S3PolicyBinding
	if err := cli.Get(context.Background(), key, &gone); err == nil {
		t.Error("binding should be gone after finalizer completes")
	}
}

func TestS3PolicyBinding_RemovedBucketSubjectClearsPolicyAfterDocBreaks(t *testing.T) {
	fa := newFakeIAMAdmin()
	fa.seedUser("alice")
	fa.seedBucket("data")
	doc := testBucketPolicyDoc("arn:aws:s3:::data/*")
	if err := fa.PutPolicy(context.Background(), "rw", doc); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	binding := testBinding("rw-bind",
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindBucket, Name: "data"},
	)
	r, cli := newBindingReconciler(t, fa, binding)
	key := types.NamespacedName{Namespace: "media", Name: "rw-bind"}
	reconcileStable(t, r, key, 5)

	// Rewriting the policy as an identity-style document must not wedge
	// the binding: removing the bucket subject still clears the stored
	// policy.
	if err := fa.PutPolicy(context.Background(), "rw",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::data/*"]}]}`); err != nil {
		t.Fatalf("update policy: %v", err)
	}
	live := getBinding(t, cli, "rw-bind")
	live.Spec.Subjects = []seaweedv1.S3Subject{{Kind: seaweedv1.S3SubjectKindIdentity, Name: "alice"}}
	if err := cli.Update(context.Background(), live); err != nil {
		t.Fatalf("update: %v", err)
	}
	reconcileStable(t, r, key, 5)

	if got := fa.bucketPolicies["data"]; got != nil {
		t.Errorf("data policy after subject removal = %q, want cleared", got)
	}
	got := getBinding(t, cli, "rw-bind")
	if got.Status.Phase != seaweedv1.S3PhaseReady {
		t.Errorf("phase = %q, want Ready", got.Status.Phase)
	}
	if got := fa.userPolicies("alice"); len(got) != 1 || got[0] != "rw" {
		t.Errorf("alice policies = %v, want [rw]", got)
	}
}

func TestS3PolicyBinding_RejectsStructurallyInvalidDoc(t *testing.T) {
	fa := newFakeIAMAdmin()
	fa.seedBucket("data")
	if err := fa.PutPolicy(context.Background(), "rw",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Resource":["arn:aws:s3:::data/*"]}]}`); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	binding := testBinding("rw-bind",
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindBucket, Name: "data"},
	)
	r, cli := newBindingReconciler(t, fa, binding)
	key := types.NamespacedName{Namespace: "media", Name: "rw-bind"}
	reconcileOnce(t, r, key) // finalizer
	reconcileOnce(t, r, key)

	if got := fa.bucketPolicies["data"]; got != nil {
		t.Errorf("no bucket policy should be written for a statement missing Action, got %q", got)
	}
	got := getBinding(t, cli, "rw-bind")
	if got.Status.Phase != seaweedv1.S3PhaseFailed || bindingReadyReason(got) != "PolicyNotBucketPolicy" {
		t.Errorf("phase/reason = %q/%q, want Failed/PolicyNotBucketPolicy", got.Status.Phase, bindingReadyReason(got))
	}
}

func TestS3PolicyBinding_SameNameIdentityAndBucket(t *testing.T) {
	fa := newFakeIAMAdmin()
	fa.seedUser("data")
	fa.seedBucket("data")
	doc := testBucketPolicyDoc("arn:aws:s3:::data/*")
	if err := fa.PutPolicy(context.Background(), "rw", doc); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	binding := testBinding("rw-bind",
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindIdentity, Name: "data"},
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindBucket, Name: "data"},
	)
	r, _ := newBindingReconciler(t, fa, binding)
	key := types.NamespacedName{Namespace: "media", Name: "rw-bind"}
	reconcileStable(t, r, key, 5)

	if got := fa.userPolicies("data"); len(got) != 1 || got[0] != "rw" {
		t.Errorf("identity policies = %v, want [rw]", got)
	}
	if got := string(fa.bucketPolicies["data"]); got != doc {
		t.Errorf("bucket policy = %q, want the document", got)
	}
}

// A binding that loses a bucket slot to an older peer releases the bucket
// without clearing — the winner may already have written its document.
func TestS3PolicyBinding_ReleaseSkipsClearWhenClaimed(t *testing.T) {
	fa := newFakeIAMAdmin()
	fa.seedBucket("data")
	docNew := testBucketPolicyDoc("arn:aws:s3:::data/*")
	docOld := testBucketPolicyDoc("arn:aws:s3:::data")
	if err := fa.PutPolicy(context.Background(), "rw", docNew); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	if err := fa.PutPolicy(context.Background(), "other", docOld); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	newer := testBinding("zzz-newer",
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindBucket, Name: "data"})
	newer.CreationTimestamp = metav1.NewTime(time.Unix(2000, 0))
	older := testBinding("aaa-older",
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindBucket, Name: "data"})
	older.Spec.PolicyRef.Name = "other"
	older.CreationTimestamp = metav1.NewTime(time.Unix(1000, 0))

	// The newer binding exists alone at first and applies its document;
	// the older claimant appears afterwards and takes the slot.
	r, cli := newBindingReconciler(t, fa, newer)
	newerKey := types.NamespacedName{Namespace: "media", Name: "zzz-newer"}
	olderKey := types.NamespacedName{Namespace: "media", Name: "aaa-older"}
	reconcileStable(t, r, newerKey, 5)
	if got := string(fa.bucketPolicies["data"]); got != docNew {
		t.Fatalf("bucket policy = %q, want the newer document applied first", got)
	}
	if err := cli.Create(context.Background(), older); err != nil {
		t.Fatalf("create older: %v", err)
	}
	reconcileStable(t, r, olderKey, 5)
	if got := string(fa.bucketPolicies["data"]); got != docOld {
		t.Fatalf("bucket policy = %q, want the older winner's document", got)
	}
	// The loser resyncs: it releases the slot but must not erase the
	// winner's document. BucketConflict requeues forever, so drive it
	// by hand.
	reconcileOnce(t, r, newerKey)
	reconcileOnce(t, r, newerKey)
	if got := string(fa.bucketPolicies["data"]); got != docOld {
		t.Errorf("released binding erased the winner's document: %q", got)
	}
}

// Deleting a former owner mid-handoff must not clear the policy the new
// owner just wrote.
func TestS3PolicyBinding_DeleteSkipsClearWhenClaimed(t *testing.T) {
	fa := newFakeIAMAdmin()
	fa.seedBucket("data")
	docNew := testBucketPolicyDoc("arn:aws:s3:::data/*")
	docOld := testBucketPolicyDoc("arn:aws:s3:::data")
	if err := fa.PutPolicy(context.Background(), "rw", docNew); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	if err := fa.PutPolicy(context.Background(), "other", docOld); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	newer := testBinding("zzz-newer",
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindBucket, Name: "data"})
	newer.CreationTimestamp = metav1.NewTime(time.Unix(2000, 0))
	older := testBinding("aaa-older",
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindBucket, Name: "data"})
	older.Spec.PolicyRef.Name = "other"
	older.CreationTimestamp = metav1.NewTime(time.Unix(1000, 0))

	// The newer binding applies first; the older claimant appears later
	// and takes the slot.
	r, cli := newBindingReconciler(t, fa, newer)
	newerKey := types.NamespacedName{Namespace: "media", Name: "zzz-newer"}
	olderKey := types.NamespacedName{Namespace: "media", Name: "aaa-older"}
	reconcileStable(t, r, newerKey, 5)
	if err := cli.Create(context.Background(), older); err != nil {
		t.Fatalf("create older: %v", err)
	}
	reconcileStable(t, r, olderKey, 5)

	// Delete the loser before its next resync — status still records the
	// bucket attached, but the older binding now owns the slot.
	live := getBinding(t, cli, "zzz-newer")
	if err := cli.Delete(context.Background(), live); err != nil {
		t.Fatalf("delete: %v", err)
	}
	reconcileStable(t, r, newerKey, 5)
	if got := string(fa.bucketPolicies["data"]); got != docOld {
		t.Errorf("deleting the former owner erased the active policy: %q", got)
	}
}

// A peer that claims the bucket only through its spec has not installed a
// document, so deleting the owner must still clear its own grant — skipping
// would leave the deleted binding's policy active when the claimant can
// never apply.
func TestS3PolicyBinding_DeleteClearsWhenClaimantNeverApplied(t *testing.T) {
	fa := newFakeIAMAdmin()
	fa.seedBucket("data")
	doc := testBucketPolicyDoc("arn:aws:s3:::data/*")
	if err := fa.PutPolicy(context.Background(), "rw", doc); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	owner := testBinding("aaa-owner",
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindBucket, Name: "data"})
	owner.CreationTimestamp = metav1.NewTime(time.Unix(1000, 0))
	blocked := testBinding("zzz-blocked",
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindBucket, Name: "data"})
	blocked.Spec.PolicyRef.Name = "missing"
	blocked.CreationTimestamp = metav1.NewTime(time.Unix(2000, 0))

	r, cli := newBindingReconciler(t, fa, owner, blocked)
	ownerKey := types.NamespacedName{Namespace: "media", Name: "aaa-owner"}
	reconcileStable(t, r, ownerKey, 5)
	if got := string(fa.bucketPolicies["data"]); got != doc {
		t.Fatalf("bucket policy = %q, want applied before delete", got)
	}

	live := getBinding(t, cli, "aaa-owner")
	if err := cli.Delete(context.Background(), live); err != nil {
		t.Fatalf("delete: %v", err)
	}
	reconcileStable(t, r, ownerKey, 5)
	if got := fa.bucketPolicies["data"]; got != nil {
		t.Errorf("bucket policy after delete = %q, want cleared despite the claimant that never applied", got)
	}
}

// A binding denied by ResourceReferenceGrant can never apply, so its spec
// must not squat a bucket slot a permitted binding could take.
func TestS3PolicyBinding_DeniedPeerDoesNotSquatBucket(t *testing.T) {
	fa := newFakeIAMAdmin()
	fa.seedBucket("data")
	doc := testBucketPolicyDoc("arn:aws:s3:::data/*")
	if err := fa.PutPolicy(context.Background(), "rw", doc); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	denied := testBinding("aaa-denied",
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindBucket, Name: "data"})
	denied.Namespace = "other" // no grant authorizes "other"
	denied.CreationTimestamp = metav1.NewTime(time.Unix(1000, 0))
	permitted := testBinding("zzz-permitted",
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindBucket, Name: "data"})
	permitted.CreationTimestamp = metav1.NewTime(time.Unix(2000, 0))

	r, cli := newBindingReconciler(t, fa, denied, permitted)
	key := types.NamespacedName{Namespace: "media", Name: "zzz-permitted"}
	reconcileStable(t, r, key, 5)

	if got := string(fa.bucketPolicies["data"]); got != doc {
		t.Errorf("bucket policy = %q, want applied despite the denied older claim", got)
	}
	if got := getBinding(t, cli, "zzz-permitted"); got.Status.Phase != seaweedv1.S3PhaseReady {
		t.Errorf("phase = %q, want Ready", got.Status.Phase)
	}
}

// Replacing the document with an identity-style policy while dropping the
// bucket subjects must still revoke: the clear runs before validation can
// fail, so a broken replacement cannot wedge old grants in place.
func TestS3PolicyBinding_InvalidDocStillClearsRemovedBuckets(t *testing.T) {
	fa := newFakeIAMAdmin()
	fa.seedUser("alice")
	fa.seedBucket("data")
	doc := testBucketPolicyDoc("arn:aws:s3:::data/*")
	if err := fa.PutPolicy(context.Background(), "rw", doc); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	binding := testBinding("rw-bind",
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindBucket, Name: "data"})
	r, cli := newBindingReconciler(t, fa, binding)
	key := types.NamespacedName{Namespace: "media", Name: "rw-bind"}
	reconcileStable(t, r, key, 5)
	if got := fa.bucketPolicies["data"]; got == nil {
		t.Fatal("expected bucket policy to be applied")
	}

	if err := fa.PutPolicy(context.Background(), "rw",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`); err != nil {
		t.Fatalf("swap policy: %v", err)
	}
	live := getBinding(t, cli, "rw-bind")
	live.Spec.Subjects = []seaweedv1.S3Subject{
		{Kind: seaweedv1.S3SubjectKindIdentity, Name: "alice"},
	}
	if err := cli.Update(context.Background(), live); err != nil {
		t.Fatalf("update: %v", err)
	}
	reconcileStable(t, r, key, 5)

	if got := fa.bucketPolicies["data"]; got != nil {
		t.Errorf("bucket policy = %q, want cleared despite the invalid document", got)
	}
}

// A bucket released before a validation failure must stop claiming its
// slot — the persisted status must not keep recording it attached.
func TestS3PolicyBinding_ReleasedBucketDropsFromStatusOnFailure(t *testing.T) {
	fa := newFakeIAMAdmin()
	fa.seedBucket("data")
	fa.seedBucket("media")
	doc := testBucketPolicyDoc("arn:aws:s3:::data/*", "arn:aws:s3:::media/*")
	if err := fa.PutPolicy(context.Background(), "rw", doc); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	binding := testBinding("rw-bind",
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindBucket, Name: "data"},
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindBucket, Name: "media"},
	)
	r, cli := newBindingReconciler(t, fa, binding)
	key := types.NamespacedName{Namespace: "media", Name: "rw-bind"}
	reconcileStable(t, r, key, 5)

	if err := fa.PutPolicy(context.Background(), "rw",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`); err != nil {
		t.Fatalf("swap policy: %v", err)
	}
	live := getBinding(t, cli, "rw-bind")
	live.Spec.Subjects = []seaweedv1.S3Subject{
		{Kind: seaweedv1.S3SubjectKindBucket, Name: "data"},
	}
	if err := cli.Update(context.Background(), live); err != nil {
		t.Fatalf("update: %v", err)
	}
	reconcileOnce(t, r, key)

	got := getBinding(t, cli, "rw-bind")
	for _, b := range got.Status.AttachedBuckets {
		if b == "media" {
			t.Errorf("released bucket still recorded attached: %v", got.Status.AttachedBuckets)
		}
	}
	if got := fa.bucketPolicies["media"]; got != nil {
		t.Errorf("media policy = %q, want cleared", got)
	}
}

// A mid-apply failure must not drop cleanup records for buckets still
// owned but not yet re-processed.
func TestS3PolicyBinding_FailedAttachKeepsBucketRecords(t *testing.T) {
	fa := newFakeIAMAdmin()
	fa.seedUser("alice")
	fa.seedBucket("data")
	doc := testBucketPolicyDoc("arn:aws:s3:::data/*")
	if err := fa.PutPolicy(context.Background(), "rw", doc); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	binding := testBinding("rw-bind",
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindIdentity, Name: "alice"},
		seaweedv1.S3Subject{Kind: seaweedv1.S3SubjectKindBucket, Name: "data"})
	r, cli := newBindingReconciler(t, fa, binding)
	key := types.NamespacedName{Namespace: "media", Name: "rw-bind"}
	reconcileStable(t, r, key, 5)

	fa.attachPolicyErr = errors.New("iam unavailable")
	reconcileOnce(t, r, key)

	got := getBinding(t, cli, "rw-bind")
	if len(got.Status.AttachedSubjects) != 1 || got.Status.AttachedSubjects[0] != "alice" {
		t.Errorf("attachedSubjects = %v, want [alice] preserved for cleanup", got.Status.AttachedSubjects)
	}
	if len(got.Status.AttachedBuckets) != 1 || got.Status.AttachedBuckets[0] != "data" {
		t.Errorf("attachedBuckets = %v, want [data] preserved for cleanup", got.Status.AttachedBuckets)
	}
}
