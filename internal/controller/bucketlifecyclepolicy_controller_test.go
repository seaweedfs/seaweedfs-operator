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
	policy := newTestLifecyclePolicy()
	r, cli := testLifecycleReconciler(t, fa, policy)
	key := types.NamespacedName{Namespace: "default", Name: "expire-logs"}

	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Error("expected a requeue while the bucket is missing")
	}
	p := getLifecyclePolicy(t, cli, key)
	if p.Status.Phase != seaweedv1.BucketPhasePending {
		t.Errorf("phase = %q, want Pending", p.Status.Phase)
	}
	if c := meta.FindStatusCondition(p.Status.Conditions, seaweedv1.BucketLifecyclePolicyConditionBucketResolved); c == nil || c.Status != metav1.ConditionFalse {
		t.Errorf("BucketResolved condition = %+v, want False", c)
	}
	if len(fa.calls) != 0 {
		t.Errorf("admin should not be touched, got %v", fa.calls)
	}
}

func TestLifecyclePolicyBucketNotProvisioned(t *testing.T) {
	fa := newFakeAdmin()
	sw, bucket := newLifecycleTestObjects()
	bucket.Status.BucketName = ""
	policy := newTestLifecyclePolicy()
	r, cli := testLifecycleReconciler(t, fa, sw, bucket, policy)
	key := types.NamespacedName{Namespace: "default", Name: "expire-logs"}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	p := getLifecyclePolicy(t, cli, key)
	if p.Status.Phase != seaweedv1.BucketPhasePending {
		t.Errorf("phase = %q, want Pending", p.Status.Phase)
	}
}

func TestLifecyclePolicyApply(t *testing.T) {
	fa := newFakeAdmin()
	sw, bucket := newLifecycleTestObjects()
	policy := newTestLifecyclePolicy()
	r, cli := testLifecycleReconciler(t, fa, sw, bucket, policy)
	key := types.NamespacedName{Namespace: "default", Name: "expire-logs"}

	reconcileLifecycle(t, r, key)

	p := getLifecyclePolicy(t, cli, key)
	if p.Status.Phase != seaweedv1.BucketPhaseReady {
		t.Errorf("phase = %q, want Ready", p.Status.Phase)
	}
	if p.Status.AppliedRules != 1 {
		t.Errorf("appliedRules = %d, want 1", p.Status.AppliedRules)
	}
	if p.Status.BucketName != "my-bucket" {
		t.Errorf("bucketName = %q, want my-bucket", p.Status.BucketName)
	}
	if !controllerHasFinalizer(p) {
		t.Error("expected finalizer to be added")
	}
	if applied := fa.lifecycle["my-bucket"]; len(applied) == 0 {
		t.Fatal("expected lifecycle XML to be written")
	}
}

func TestLifecyclePolicyIdempotent(t *testing.T) {
	fa := newFakeAdmin()
	sw, bucket := newLifecycleTestObjects()
	policy := newTestLifecyclePolicy()
	r, cli := testLifecycleReconciler(t, fa, sw, bucket, policy)
	key := types.NamespacedName{Namespace: "default", Name: "expire-logs"}

	reconcileLifecycle(t, r, key)

	before := countCalls(fa.calls, "SetLifecycle:")
	if before == 0 {
		t.Fatal("expected an initial SetLifecycle")
	}
	// A steady-state reconcile must not rewrite an already-matching config.
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if after := countCalls(fa.calls, "SetLifecycle:"); after != before {
		t.Errorf("SetLifecycle called again on steady state: before=%d after=%d", before, after)
	}
	_ = cli
}

func TestLifecyclePolicyDeleteClearsConfig(t *testing.T) {
	fa := newFakeAdmin()
	sw, bucket := newLifecycleTestObjects()
	fa.lifecycle = map[string][]byte{"my-bucket": []byte("<LifecycleConfiguration></LifecycleConfiguration>")}
	policy := newTestLifecyclePolicy()
	policy.Finalizers = []string{BucketLifecyclePolicyFinalizer}
	r, cli := testLifecycleReconciler(t, fa, sw, bucket, policy)
	key := types.NamespacedName{Namespace: "default", Name: "expire-logs"}

	if err := cli.Delete(context.Background(), policy); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, ok := fa.lifecycle["my-bucket"]; ok {
		t.Error("expected lifecycle config to be cleared on delete")
	}
	var p seaweedv1.BucketLifecyclePolicy
	if err := cli.Get(context.Background(), key, &p); !apierrors.IsNotFound(err) {
		t.Errorf("expected policy to be gone after finalizer removal, got err=%v", err)
	}
}

func TestLifecyclePolicyDeleteRetain(t *testing.T) {
	fa := newFakeAdmin()
	sw, bucket := newLifecycleTestObjects()
	fa.lifecycle = map[string][]byte{"my-bucket": []byte("<LifecycleConfiguration></LifecycleConfiguration>")}
	policy := newTestLifecyclePolicy()
	policy.Spec.ReclaimPolicy = seaweedv1.BucketReclaimRetain
	policy.Finalizers = []string{BucketLifecyclePolicyFinalizer}
	r, cli := testLifecycleReconciler(t, fa, sw, bucket, policy)
	key := types.NamespacedName{Namespace: "default", Name: "expire-logs"}

	if err := cli.Delete(context.Background(), policy); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, ok := fa.lifecycle["my-bucket"]; !ok {
		t.Error("Retain must leave the lifecycle config in place")
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
