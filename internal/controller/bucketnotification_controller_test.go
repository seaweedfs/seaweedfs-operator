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

func testNotificationReconciler(t *testing.T, objs ...client.Object) (*BucketNotificationReconciler, client.Client) {
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
		WithStatusSubresource(&seaweedv1.BucketNotification{}).
		Build()
	r := &BucketNotificationReconciler{
		Client: cli,
		Log:    logf.FromContext(context.Background()),
		Scheme: scheme,
	}
	return r, cli
}

func newNotificationTestObjects() (*seaweedv1.Seaweed, *seaweedv1.Bucket) {
	sw := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
		Spec:       seaweedv1.SeaweedSpec{Master: &seaweedv1.MasterSpec{Replicas: 3}},
	}
	bucket := &seaweedv1.Bucket{
		ObjectMeta: metav1.ObjectMeta{Name: "my-bucket", Namespace: "default"},
		Spec:       seaweedv1.BucketSpec{ClusterRef: &seaweedv1.BucketClusterRef{Name: "prod"}},
		Status:     seaweedv1.BucketStatus{BucketName: "my-bucket", Phase: seaweedv1.BucketPhaseReady},
	}
	return sw, bucket
}

func newTestBucketNotification() *seaweedv1.BucketNotification {
	return &seaweedv1.BucketNotification{
		ObjectMeta: metav1.ObjectMeta{Name: "events", Namespace: "default"},
		Spec: seaweedv1.BucketNotificationSpec{
			BucketRef: seaweedv1.BucketNotificationBucketRef{Name: "my-bucket"},
			Rules: []seaweedv1.BucketNotificationRule{{
				Name: "created",
				Destination: seaweedv1.BucketNotificationDestination{
					Kafka: &seaweedv1.BucketNotificationKafkaDestination{
						Brokers: []string{"kafka:9092"},
						Topic:   "bucket-events",
					},
				},
			}},
		},
	}
}

var notificationKey = types.NamespacedName{Namespace: "default", Name: "events"}

func reconcileNotificationN(t *testing.T, r *BucketNotificationReconciler, key types.NamespacedName, n int) ctrl.Result {
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

func getNotification(t *testing.T, cli client.Client, key types.NamespacedName) *seaweedv1.BucketNotification {
	t.Helper()
	var n seaweedv1.BucketNotification
	if err := cli.Get(context.Background(), key, &n); err != nil {
		t.Fatalf("get notification: %v", err)
	}
	return &n
}

func TestNotificationBucketNotFound(t *testing.T) {
	r, cli := testNotificationReconciler(t, newTestBucketNotification())

	res := reconcileNotificationN(t, r, notificationKey, 1)
	if res.RequeueAfter == 0 {
		t.Error("expected a requeue while the bucket is missing")
	}
	n := getNotification(t, cli, notificationKey)
	if n.Status.Phase != seaweedv1.BucketPhasePending {
		t.Errorf("phase = %q, want Pending", n.Status.Phase)
	}
	if c := meta.FindStatusCondition(n.Status.Conditions, seaweedv1.BucketNotificationConditionBucketResolved); c == nil || c.Reason != "BucketNotFound" {
		t.Errorf("BucketResolved condition = %+v, want reason BucketNotFound", c)
	}
}

func TestNotificationBucketNotReady(t *testing.T) {
	sw, bucket := newNotificationTestObjects()
	bucket.Status.BucketName = ""
	bucket.Status.Phase = seaweedv1.BucketPhasePending
	r, cli := testNotificationReconciler(t, sw, bucket, newTestBucketNotification())

	reconcileNotificationN(t, r, notificationKey, 1)
	n := getNotification(t, cli, notificationKey)
	if n.Status.Phase != seaweedv1.BucketPhasePending {
		t.Errorf("phase = %q, want Pending", n.Status.Phase)
	}
	if c := meta.FindStatusCondition(n.Status.Conditions, seaweedv1.BucketNotificationConditionBucketResolved); c == nil || c.Reason != "BucketNotReady" {
		t.Errorf("BucketResolved condition = %+v, want reason BucketNotReady", c)
	}
}

// TestNotificationRegressedBucket pins that a provisioned Bucket which later
// fails or terminates still counts as unready even though status.bucketName
// is retained.
func TestNotificationRegressedBucket(t *testing.T) {
	for _, phase := range []seaweedv1.BucketPhase{seaweedv1.BucketPhaseFailed, seaweedv1.BucketPhaseTerminating} {
		t.Run(string(phase), func(t *testing.T) {
			sw, bucket := newNotificationTestObjects()
			bucket.Status.Phase = phase
			r, cli := testNotificationReconciler(t, sw, bucket, newTestBucketNotification())

			reconcileNotificationN(t, r, notificationKey, 1)
			n := getNotification(t, cli, notificationKey)
			if n.Status.Phase != seaweedv1.BucketPhasePending {
				t.Errorf("phase = %q, want Pending", n.Status.Phase)
			}
			if c := meta.FindStatusCondition(n.Status.Conditions, seaweedv1.BucketNotificationConditionBucketResolved); c == nil || c.Reason != "BucketNotReady" {
				t.Errorf("BucketResolved condition = %+v, want reason BucketNotReady", c)
			}
		})
	}
}

func TestNotificationBucketDeleting(t *testing.T) {
	sw, bucket := newNotificationTestObjects()
	bucket.Finalizers = []string{"test/finalizer"}
	bucket.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	r, cli := testNotificationReconciler(t, sw, bucket, newTestBucketNotification())

	reconcileNotificationN(t, r, notificationKey, 1)
	n := getNotification(t, cli, notificationKey)
	if n.Status.Phase != seaweedv1.BucketPhasePending {
		t.Errorf("phase = %q, want Pending", n.Status.Phase)
	}
}

// TestNotificationResolvedButNotDelivered pins the resolution-only contract:
// a fully resolved notification pins its target on status but never reports
// the rules as active while delivery is unwired.
func TestNotificationResolvedButNotDelivered(t *testing.T) {
	sw, bucket := newNotificationTestObjects()
	r, cli := testNotificationReconciler(t, sw, bucket, newTestBucketNotification())
	r.ResyncInterval = 2 * time.Minute

	res := reconcileNotificationN(t, r, notificationKey, 1)
	if res.RequeueAfter != 2*time.Minute {
		t.Errorf("RequeueAfter = %v, want the resync cadence", res.RequeueAfter)
	}
	n := getNotification(t, cli, notificationKey)
	if n.Status.Phase != seaweedv1.BucketPhasePending {
		t.Errorf("phase = %q, want Pending", n.Status.Phase)
	}
	if c := meta.FindStatusCondition(n.Status.Conditions, seaweedv1.BucketNotificationConditionReady); c == nil || c.Status != metav1.ConditionFalse || c.Reason != "EnforcementUnavailable" {
		t.Errorf("Ready condition = %+v, want False/EnforcementUnavailable", c)
	}
	if c := meta.FindStatusCondition(n.Status.Conditions, seaweedv1.BucketNotificationConditionBucketResolved); c == nil || c.Status != metav1.ConditionTrue {
		t.Errorf("BucketResolved condition = %+v, want True", c)
	}
	if n.Status.AppliedRules != 0 {
		t.Errorf("appliedRules = %d, want 0 while delivery is unwired", n.Status.AppliedRules)
	}
	if n.Status.BucketName != "my-bucket" || n.Status.ClusterName != "prod" || n.Status.ClusterNamespace != "default" {
		t.Errorf("recorded target = %q/%q/%q", n.Status.ClusterNamespace, n.Status.ClusterName, n.Status.BucketName)
	}
}

// TestNotificationRuleConflict pins deterministic conflict resolution: with a
// colliding rule name the earlier claim stays resolved and only the later
// resource fails; deleting the earlier claim frees the name.
func TestNotificationRuleConflict(t *testing.T) {
	sw, bucket := newNotificationTestObjects()
	owner := newTestBucketNotification()
	owner.Name = "aaa-owner"
	loser := newTestBucketNotification()
	loser.Name = "zzz-loser"
	loser.Status.AppliedRules = 2 // stale count must be cleared on failure
	r, cli := testNotificationReconciler(t, sw, bucket, owner, loser)

	loserKey := types.NamespacedName{Namespace: "default", Name: "zzz-loser"}
	reconcileNotificationN(t, r, loserKey, 1)
	n := getNotification(t, cli, loserKey)
	if n.Status.Phase != seaweedv1.BucketPhaseFailed {
		t.Errorf("loser phase = %q, want Failed", n.Status.Phase)
	}
	if c := meta.FindStatusCondition(n.Status.Conditions, seaweedv1.BucketNotificationConditionRuleConflict); c == nil || c.Status != metav1.ConditionTrue {
		t.Errorf("RuleConflict condition = %+v, want True", c)
	}
	if n.Status.AppliedRules != 0 {
		t.Errorf("appliedRules = %d, want 0", n.Status.AppliedRules)
	}

	ownerKey := types.NamespacedName{Namespace: "default", Name: "aaa-owner"}
	reconcileNotificationN(t, r, ownerKey, 1)
	n = getNotification(t, cli, ownerKey)
	if c := meta.FindStatusCondition(n.Status.Conditions, seaweedv1.BucketNotificationConditionRuleConflict); c != nil {
		t.Errorf("owner must not report a conflict, got %+v", c)
	}
	if n.Status.Phase != seaweedv1.BucketPhasePending {
		t.Errorf("owner phase = %q, want Pending (resolved)", n.Status.Phase)
	}

	if err := cli.Delete(context.Background(), owner); err != nil {
		t.Fatalf("delete owner: %v", err)
	}
	reconcileNotificationN(t, r, loserKey, 1)
	n = getNotification(t, cli, loserKey)
	if n.Status.Phase != seaweedv1.BucketPhasePending {
		t.Errorf("freed loser phase = %q, want Pending (resolved)", n.Status.Phase)
	}
	if c := meta.FindStatusCondition(n.Status.Conditions, seaweedv1.BucketNotificationConditionRuleConflict); c != nil {
		t.Errorf("RuleConflict should clear once the name is free, got %+v", c)
	}
}

// TestNotificationDependencyLoss pins that a resolved notification regresses
// when its bucket disappears.
func TestNotificationDependencyLoss(t *testing.T) {
	sw, bucket := newNotificationTestObjects()
	r, cli := testNotificationReconciler(t, sw, bucket, newTestBucketNotification())

	reconcileNotificationN(t, r, notificationKey, 1)
	if c := meta.FindStatusCondition(getNotification(t, cli, notificationKey).Status.Conditions, seaweedv1.BucketNotificationConditionBucketResolved); c == nil || c.Status != metav1.ConditionTrue {
		t.Fatalf("precondition: BucketResolved should be True, got %+v", c)
	}

	if err := cli.Delete(context.Background(), bucket); err != nil {
		t.Fatalf("delete bucket: %v", err)
	}
	reconcileNotificationN(t, r, notificationKey, 1)

	n := getNotification(t, cli, notificationKey)
	if n.Status.Phase != seaweedv1.BucketPhasePending {
		t.Errorf("phase = %q, want Pending", n.Status.Phase)
	}
	if c := meta.FindStatusCondition(n.Status.Conditions, seaweedv1.BucketNotificationConditionBucketResolved); c == nil || c.Reason != "BucketNotFound" {
		t.Errorf("BucketResolved condition = %+v, want reason BucketNotFound", c)
	}
	if n.Status.AppliedRules != 0 {
		t.Errorf("appliedRules = %d, want 0", n.Status.AppliedRules)
	}
}

func TestNotificationMapBucketToNotifications(t *testing.T) {
	match := newTestBucketNotification()
	other := newTestBucketNotification()
	other.Name = "other"
	other.Spec.BucketRef.Name = "different-bucket"
	r, _ := testNotificationReconciler(t, match, other)

	bucket := &seaweedv1.Bucket{ObjectMeta: metav1.ObjectMeta{Name: "my-bucket", Namespace: "default"}}
	reqs := r.mapBucketToNotifications(context.Background(), bucket)
	if len(reqs) != 1 || reqs[0].Name != "events" {
		t.Fatalf("expected only the referencing notification, got %+v", reqs)
	}
}

func TestNotificationMapNotificationToPeers(t *testing.T) {
	a := newTestBucketNotification()
	a.Name = "a"
	b := newTestBucketNotification()
	b.Name = "b"
	c := newTestBucketNotification()
	c.Name = "c"
	c.Spec.BucketRef.Name = "other-bucket"
	r, _ := testNotificationReconciler(t, a, b, c)

	reqs := r.mapNotificationToPeers(context.Background(), a)
	if len(reqs) != 1 || reqs[0].Name != "b" {
		t.Fatalf("expected only same-bucket peer b, got %+v", reqs)
	}
}
