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

	"github.com/go-logr/logr"
	"google.golang.org/grpc"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func testLifecycleReconciler(t *testing.T, fa *fakeBucketAdmin, objs ...client.Object) (*BucketLifecyclePolicyReconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("clientgoscheme: %v", err)
	}
	if err := seaweedv1.AddToScheme(scheme); err != nil {
		t.Fatalf("seaweedv1: %v", err)
	}
	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&seaweedv1.BucketLifecyclePolicy{}).
		Build()
	r := &BucketLifecyclePolicyReconciler{
		Client: cli,
		Log:    logf.FromContext(context.Background()),
		Scheme: scheme,
		AdminFactory: func(_, _ string, _ []byte, _ grpc.DialOption, _ logr.Logger) (BucketAdmin, error) {
			return fa, nil
		},
	}
	return r, cli
}

func newLifecycleTestObjects() (*seaweedv1.Seaweed, *seaweedv1.Bucket) {
	sw := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
		Spec:       seaweedv1.SeaweedSpec{Master: &seaweedv1.MasterSpec{Replicas: 3}},
	}
	bucket := &seaweedv1.Bucket{
		ObjectMeta: metav1.ObjectMeta{Name: "my-bucket", Namespace: "default"},
		Spec:       seaweedv1.BucketSpec{ClusterRef: seaweedv1.BucketClusterRef{Name: "prod"}},
		Status:     seaweedv1.BucketStatus{BucketName: "my-bucket"},
	}
	return sw, bucket
}

func newTestLifecyclePolicy() *seaweedv1.BucketLifecyclePolicy {
	return &seaweedv1.BucketLifecyclePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "expire-logs", Namespace: "default"},
		Spec: seaweedv1.BucketLifecyclePolicySpec{
			BucketRef: seaweedv1.BucketLifecycleRef{Name: "my-bucket"},
			Rules: []seaweedv1.BucketLifecycleRule{{
				ID:         "expire-archived",
				Prefix:     "archived/",
				Status:     seaweedv1.BucketLifecycleRuleEnabled,
				Expiration: &seaweedv1.BucketLifecycleExpiration{Days: 90},
			}},
		},
	}
}

var lifecyclePolicyKey = types.NamespacedName{Namespace: "default", Name: "expire-logs"}

// reconcileLifecycle drives Reconcile until it neither requeues nor errors.
func reconcileLifecycle(t *testing.T, r *BucketLifecyclePolicyReconciler, key types.NamespacedName) {
	t.Helper()
	for i := 0; i < 5; i++ {
		res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
		if err != nil {
			t.Fatalf("reconcile step %d: %v", i, err)
		}
		if !res.Requeue && res.RequeueAfter == 0 {
			return
		}
	}
	t.Fatalf("reconcile did not converge")
}

// reconcileLifecycleN runs Reconcile n times and returns the last result, for
// paths that settle on a steady requeue (Pending / Conflict) not convergence.
func reconcileLifecycleN(t *testing.T, r *BucketLifecyclePolicyReconciler, key types.NamespacedName, n int) ctrl.Result {
	t.Helper()
	var res ctrl.Result
	for i := 0; i < n; i++ {
		var err error
		res, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
		if err != nil {
			t.Fatalf("reconcile step %d: %v", i, err)
		}
	}
	return res
}

func getLifecyclePolicy(t *testing.T, cli client.Client, key types.NamespacedName) *seaweedv1.BucketLifecyclePolicy {
	t.Helper()
	var p seaweedv1.BucketLifecyclePolicy
	if err := cli.Get(context.Background(), key, &p); err != nil {
		t.Fatalf("get policy: %v", err)
	}
	return &p
}

func TestLifecyclePolicyBucketNotFound(t *testing.T) {
	fa := newFakeAdmin()
	r, cli := testLifecycleReconciler(t, fa, newTestLifecyclePolicy())

	res := reconcileLifecycleN(t, r, lifecyclePolicyKey, 2)
	if res.RequeueAfter == 0 {
		t.Error("expected a requeue while the bucket is missing")
	}
	p := getLifecyclePolicy(t, cli, lifecyclePolicyKey)
	if p.Status.Phase != seaweedv1.BucketPhasePending {
		t.Errorf("phase = %q, want Pending", p.Status.Phase)
	}
	if c := meta.FindStatusCondition(p.Status.Conditions, seaweedv1.BucketLifecyclePolicyConditionBucketResolved); c == nil || c.Status != metav1.ConditionFalse {
		t.Errorf("BucketResolved condition = %+v, want False", c)
	}
	if countCalls(fa.calls, "SetLifecycle:") != 0 {
		t.Errorf("admin lifecycle should not be touched, got %v", fa.calls)
	}
}

func TestLifecyclePolicyBucketNotProvisioned(t *testing.T) {
	fa := newFakeAdmin()
	sw, bucket := newLifecycleTestObjects()
	bucket.Status.BucketName = ""
	r, cli := testLifecycleReconciler(t, fa, sw, bucket, newTestLifecyclePolicy())

	reconcileLifecycleN(t, r, lifecyclePolicyKey, 2)
	p := getLifecyclePolicy(t, cli, lifecyclePolicyKey)
	if p.Status.Phase != seaweedv1.BucketPhasePending {
		t.Errorf("phase = %q, want Pending", p.Status.Phase)
	}
}

// TestLifecyclePolicyReadyRegressesOnDependencyLoss pins that a policy drops
// Ready=False when its bucket disappears after a successful reconcile.
func TestLifecyclePolicyReadyRegressesOnDependencyLoss(t *testing.T) {
	fa := newFakeAdmin()
	sw, bucket := newLifecycleTestObjects()
	r, cli := testLifecycleReconciler(t, fa, sw, bucket, newTestLifecyclePolicy())

	reconcileLifecycle(t, r, lifecyclePolicyKey)
	if c := meta.FindStatusCondition(getLifecyclePolicy(t, cli, lifecyclePolicyKey).Status.Conditions, seaweedv1.BucketLifecyclePolicyConditionReady); c == nil || c.Status != metav1.ConditionTrue {
		t.Fatalf("precondition: Ready should be True, got %+v", c)
	}

	if err := cli.Delete(context.Background(), bucket); err != nil {
		t.Fatalf("delete bucket: %v", err)
	}
	reconcileLifecycleN(t, r, lifecyclePolicyKey, 1)

	p := getLifecyclePolicy(t, cli, lifecyclePolicyKey)
	if p.Status.Phase != seaweedv1.BucketPhasePending {
		t.Errorf("phase = %q, want Pending", p.Status.Phase)
	}
	if c := meta.FindStatusCondition(p.Status.Conditions, seaweedv1.BucketLifecyclePolicyConditionReady); c == nil || c.Status != metav1.ConditionFalse {
		t.Errorf("Ready condition = %+v, want False", c)
	}
}

func TestLifecyclePolicyApply(t *testing.T) {
	fa := newFakeAdmin()
	sw, bucket := newLifecycleTestObjects()
	r, cli := testLifecycleReconciler(t, fa, sw, bucket, newTestLifecyclePolicy())

	reconcileLifecycle(t, r, lifecyclePolicyKey)

	p := getLifecyclePolicy(t, cli, lifecyclePolicyKey)
	if p.Status.Phase != seaweedv1.BucketPhaseReady {
		t.Errorf("phase = %q, want Ready", p.Status.Phase)
	}
	if p.Status.AppliedRules != 1 {
		t.Errorf("appliedRules = %d, want 1", p.Status.AppliedRules)
	}
	if p.Status.BucketName != "my-bucket" || p.Status.ClusterName != "prod" || p.Status.ClusterNamespace != "default" {
		t.Errorf("recorded target = %q/%q/%q", p.Status.ClusterNamespace, p.Status.ClusterName, p.Status.BucketName)
	}
	if !controllerHasFinalizer(p) {
		t.Error("expected finalizer to be added")
	}
	if len(fa.lifecycle["my-bucket"]) == 0 {
		t.Fatal("expected lifecycle XML to be written")
	}
}

func TestLifecyclePolicyIdempotent(t *testing.T) {
	fa := newFakeAdmin()
	sw, bucket := newLifecycleTestObjects()
	r, cli := testLifecycleReconciler(t, fa, sw, bucket, newTestLifecyclePolicy())

	reconcileLifecycle(t, r, lifecyclePolicyKey)
	before := countCalls(fa.calls, "SetLifecycle:")
	if before == 0 {
		t.Fatal("expected an initial SetLifecycle")
	}
	rv := getLifecyclePolicy(t, cli, lifecyclePolicyKey).ResourceVersion

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: lifecyclePolicyKey}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if after := countCalls(fa.calls, "SetLifecycle:"); after != before {
		t.Errorf("SetLifecycle called again on steady state: before=%d after=%d", before, after)
	}
	// A no-op reconcile must not write status (which would emit an update event
	// and re-trigger the controller).
	if got := getLifecyclePolicy(t, cli, lifecyclePolicyKey).ResourceVersion; got != rv {
		t.Errorf("steady-state reconcile wrote status: resourceVersion %s -> %s", rv, got)
	}
}

// TestLifecyclePolicyReadyRequeuesForDrift pins the periodic resync: a Ready
// policy must request a RequeueAfter equal to ResyncInterval so the reconciler
// keeps re-verifying filer state it cannot watch.
func TestLifecyclePolicyReadyRequeuesForDrift(t *testing.T) {
	fa := newFakeAdmin()
	sw, bucket := newLifecycleTestObjects()
	r, _ := testLifecycleReconciler(t, fa, sw, bucket, newTestLifecyclePolicy())
	r.ResyncInterval = 2 * time.Minute

	// First pass adds the finalizer (Requeue:true); the second applies and
	// settles on the resync cadence.
	res := reconcileLifecycleN(t, r, lifecyclePolicyKey, 2)
	if res.RequeueAfter != 2*time.Minute {
		t.Fatalf("RequeueAfter=%v, want the 2m resync cadence", res.RequeueAfter)
	}
}

// TestLifecyclePolicyReappliesConfigLostOutOfBand is the regression guard for
// drift recovery: once a policy is Ready, a later reconcile must reapply the
// lifecycle XML if it vanished from the filer out-of-band (e.g. a cluster
// rebuild) rather than trusting status.
func TestLifecyclePolicyReappliesConfigLostOutOfBand(t *testing.T) {
	fa := newFakeAdmin()
	sw, bucket := newLifecycleTestObjects()
	r, cli := testLifecycleReconciler(t, fa, sw, bucket, newTestLifecyclePolicy())
	r.ResyncInterval = 2 * time.Minute

	// First two passes add the finalizer and apply the configuration.
	reconcileLifecycleN(t, r, lifecyclePolicyKey, 2)
	if p := getLifecyclePolicy(t, cli, lifecyclePolicyKey); p.Status.Phase != seaweedv1.BucketPhaseReady {
		t.Fatalf("precondition: phase=%q want Ready", p.Status.Phase)
	}
	applied := countCalls(fa.calls, "SetLifecycle:")
	if applied == 0 {
		t.Fatal("precondition: expected an initial SetLifecycle")
	}

	// The lifecycle config disappears from the filer while the CR stays Ready.
	delete(fa.lifecycle, "my-bucket")

	// The periodic requeue brings us back; the read now misses the config, so
	// it is reapplied — no operator restart.
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: lifecyclePolicyKey}); err != nil {
		t.Fatalf("drift reconcile: %v", err)
	}
	if got := countCalls(fa.calls, "SetLifecycle:"); got != applied+1 {
		t.Fatalf("expected lifecycle reapplied after out-of-band loss; SetLifecycle calls %d -> %d", applied, got)
	}
}

// TestLifecyclePolicyClearsTTLsWhenXMLMatches pins that legacy TTL cleanup runs
// on takeover even when the bucket's lifecycle XML already matches the policy
// (so SetBucketLifecycle is skipped).
func TestLifecyclePolicyClearsTTLsWhenXMLMatches(t *testing.T) {
	fa := newFakeAdmin()
	sw, bucket := newLifecycleTestObjects()
	policy := newTestLifecyclePolicy()
	desired, err := buildLifecycleXML(policy.Spec.Rules)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	fa.lifecycle = map[string][]byte{"my-bucket": desired}
	r, _ := testLifecycleReconciler(t, fa, sw, bucket, policy)

	reconcileLifecycle(t, r, lifecyclePolicyKey)

	if countCalls(fa.calls, "SetLifecycle:") != 0 {
		t.Errorf("XML already matched; SetLifecycle should be skipped, got %v", fa.calls)
	}
	if countCalls(fa.calls, "ClearLegacyTTLs:") == 0 {
		t.Error("legacy TTL cleanup must run even when the XML matches")
	}
}

func TestLifecyclePolicyDeleteClearsConfig(t *testing.T) {
	fa := newFakeAdmin()
	sw, bucket := newLifecycleTestObjects()
	r, cli := testLifecycleReconciler(t, fa, sw, bucket, newTestLifecyclePolicy())

	reconcileLifecycle(t, r, lifecyclePolicyKey)
	if len(fa.lifecycle["my-bucket"]) == 0 {
		t.Fatal("precondition: config should be applied")
	}

	deleteAndReconcile(t, r, cli, lifecyclePolicyKey)

	if _, ok := fa.lifecycle["my-bucket"]; ok {
		t.Error("expected lifecycle config to be cleared on delete")
	}
	assertPolicyGone(t, cli, lifecyclePolicyKey)
}

func TestLifecyclePolicyDeleteRetain(t *testing.T) {
	fa := newFakeAdmin()
	sw, bucket := newLifecycleTestObjects()
	policy := newTestLifecyclePolicy()
	policy.Spec.ReclaimPolicy = seaweedv1.BucketReclaimRetain
	r, cli := testLifecycleReconciler(t, fa, sw, bucket, policy)

	reconcileLifecycle(t, r, lifecyclePolicyKey)
	deleteAndReconcile(t, r, cli, lifecyclePolicyKey)

	if _, ok := fa.lifecycle["my-bucket"]; !ok {
		t.Error("Retain must leave the lifecycle config in place")
	}
}

// TestLifecyclePolicyDeleteBeforeApply pins that deleting a policy that never
// applied does not erase a pre-existing (e.g. manual) lifecycle config.
func TestLifecyclePolicyDeleteBeforeApply(t *testing.T) {
	fa := newFakeAdmin()
	fa.lifecycle = map[string][]byte{"my-bucket": []byte("<LifecycleConfiguration></LifecycleConfiguration>")}
	policy := newTestLifecyclePolicy()
	policy.Finalizers = []string{BucketLifecyclePolicyFinalizer}
	// No bucket/seaweed and no recorded status: the policy never applied.
	r, cli := testLifecycleReconciler(t, fa, policy)

	deleteAndReconcile(t, r, cli, lifecyclePolicyKey)

	if _, ok := fa.lifecycle["my-bucket"]; !ok {
		t.Error("a policy that never applied must not clear an existing config")
	}
	if countCalls(fa.calls, "SetLifecycle:") != 0 {
		t.Errorf("no lifecycle write expected, got %v", fa.calls)
	}
	assertPolicyGone(t, cli, lifecyclePolicyKey)
}

// TestLifecyclePolicyDeleteWithBucketGone pins that cleanup still happens when
// the referenced Bucket CR has been removed but its cluster (and the underlying
// bucket) remain.
func TestLifecyclePolicyDeleteWithBucketGone(t *testing.T) {
	fa := newFakeAdmin()
	sw, bucket := newLifecycleTestObjects()
	r, cli := testLifecycleReconciler(t, fa, sw, bucket, newTestLifecyclePolicy())

	reconcileLifecycle(t, r, lifecyclePolicyKey)
	if err := cli.Delete(context.Background(), bucket); err != nil {
		t.Fatalf("delete bucket: %v", err)
	}

	deleteAndReconcile(t, r, cli, lifecyclePolicyKey)

	if _, ok := fa.lifecycle["my-bucket"]; ok {
		t.Error("expected cleanup via recorded cluster even with the Bucket CR gone")
	}
	assertPolicyGone(t, cli, lifecyclePolicyKey)
}

// TestLifecyclePolicyDeleteBucketAlreadyGone pins that a cleanup hitting a
// missing bucket releases the policy instead of getting stuck in Terminating.
func TestLifecyclePolicyDeleteBucketAlreadyGone(t *testing.T) {
	fa := newFakeAdmin()
	sw, bucket := newLifecycleTestObjects()
	r, cli := testLifecycleReconciler(t, fa, sw, bucket, newTestLifecyclePolicy())

	reconcileLifecycle(t, r, lifecyclePolicyKey)
	fa.lifecycleErr = ErrBucketNotFound

	deleteAndReconcile(t, r, cli, lifecyclePolicyKey)
	assertPolicyGone(t, cli, lifecyclePolicyKey)
}

// TestLifecyclePolicyConflict pins that two policies targeting one bucket don't
// fight: the deterministic owner applies, the other marks a conflict and never
// writes.
func TestLifecyclePolicyConflict(t *testing.T) {
	fa := newFakeAdmin()
	sw, bucket := newLifecycleTestObjects()
	owner := newTestLifecyclePolicy()
	owner.Name = "aaa-owner"
	loser := newTestLifecyclePolicy()
	loser.Name = "zzz-loser"
	r, cli := testLifecycleReconciler(t, fa, sw, bucket, owner, loser)

	loserKey := types.NamespacedName{Namespace: "default", Name: "zzz-loser"}
	res := reconcileLifecycleN(t, r, loserKey, 2)
	if res.RequeueAfter == 0 {
		t.Error("conflicting policy should requeue to retry takeover")
	}
	lp := getLifecyclePolicy(t, cli, loserKey)
	if lp.Status.Phase != seaweedv1.BucketPhaseFailed {
		t.Errorf("loser phase = %q, want Failed", lp.Status.Phase)
	}
	if c := meta.FindStatusCondition(lp.Status.Conditions, seaweedv1.BucketLifecyclePolicyConditionReady); c == nil || c.Reason != "Conflict" {
		t.Errorf("loser Ready condition = %+v, want reason Conflict", c)
	}
	if lp.Status.BucketName != "" {
		t.Error("loser must not record an applied marker")
	}
	if countCalls(fa.calls, "SetLifecycle:") != 0 {
		t.Errorf("loser must not write lifecycle, got %v", fa.calls)
	}

	ownerKey := types.NamespacedName{Namespace: "default", Name: "aaa-owner"}
	reconcileLifecycle(t, r, ownerKey)
	if len(fa.lifecycle["my-bucket"]) == 0 {
		t.Error("owner should apply the lifecycle config")
	}
}

func TestLifecyclePolicyMapPolicyToPeers(t *testing.T) {
	fa := newFakeAdmin()
	a := newTestLifecyclePolicy()
	a.Name = "a"
	b := newTestLifecyclePolicy()
	b.Name = "b"
	c := newTestLifecyclePolicy()
	c.Name = "c"
	c.Spec.BucketRef.Name = "other-bucket"
	r, _ := testLifecycleReconciler(t, fa, a, b, c)

	reqs := r.mapPolicyToPeers(context.Background(), a)
	if len(reqs) != 1 || reqs[0].Name != "b" {
		t.Fatalf("expected only same-bucket peer b, got %+v", reqs)
	}
}

func TestLifecyclePolicyMapBucketToPolicies(t *testing.T) {
	fa := newFakeAdmin()
	match := newTestLifecyclePolicy()
	other := newTestLifecyclePolicy()
	other.Name = "other"
	other.Spec.BucketRef.Name = "different-bucket"
	r, _ := testLifecycleReconciler(t, fa, match, other)

	bucket := &seaweedv1.Bucket{ObjectMeta: metav1.ObjectMeta{Name: "my-bucket", Namespace: "default"}}
	reqs := r.mapBucketToPolicies(context.Background(), bucket)
	if len(reqs) != 1 || reqs[0].Name != "expire-logs" {
		t.Fatalf("expected only the referencing policy, got %+v", reqs)
	}
}

func deleteAndReconcile(t *testing.T, r *BucketLifecyclePolicyReconciler, cli client.Client, key types.NamespacedName) {
	t.Helper()
	p := &seaweedv1.BucketLifecyclePolicy{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}
	if err := cli.Delete(context.Background(), p); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile during deletion: %v", err)
	}
}

func assertPolicyGone(t *testing.T, cli client.Client, key types.NamespacedName) {
	t.Helper()
	var p seaweedv1.BucketLifecyclePolicy
	if err := cli.Get(context.Background(), key, &p); !apierrors.IsNotFound(err) {
		t.Errorf("expected policy to be gone after finalizer removal, got err=%v", err)
	}
}

func controllerHasFinalizer(p *seaweedv1.BucketLifecyclePolicy) bool {
	for _, f := range p.Finalizers {
		if f == BucketLifecyclePolicyFinalizer {
			return true
		}
	}
	return false
}

func countCalls(calls []string, prefix string) int {
	n := 0
	for _, c := range calls {
		if len(c) >= len(prefix) && c[:len(prefix)] == prefix {
			n++
		}
	}
	return n
}
