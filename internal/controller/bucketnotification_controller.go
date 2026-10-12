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
	"reflect"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// BucketNotificationReconciler resolves and validates BucketNotification
// resources. Event delivery itself is performed by the notification bridge
// (see https://github.com/seaweedfs/seaweedfs-operator/issues/410): this
// reconciler establishes which rules apply to which bucket on which cluster
// and surfaces conflicts; the bridge consumes the resulting resources and
// fans matching filer events out to each rule's destination.
//
// TODO(#410): enforcement. Options under discussion are a bridge Deployment
// per Seaweed cluster subscribing via the filer's SubscribeMetadata stream
// (per-rule Kafka topics/webhooks work today), or rendering the aggregated
// rules into a managed notification.toml once core supports multiple sinks.
// When enforcement lands, this reconciler gains a finalizer plus whatever
// cluster-side cleanup the chosen mechanism requires.
type BucketNotificationReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// ResyncInterval is the steady-state cadence at which a Ready resource
	// re-enters Reconcile. Zero disables the requeue. main.go defaults it to
	// DefaultBucketResyncInterval.
	ResyncInterval time.Duration
}

// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=bucketnotifications,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=bucketnotifications/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=bucketnotifications/finalizers,verbs=update
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=buckets,verbs=get;list;watch

// Reconcile implements the notification reconciliation logic.
func (r *BucketNotificationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	log := r.Log.WithValues("bucketnotification", req.NamespacedName)
	_ = log

	var notification seaweedv1.BucketNotification
	if err := r.Get(ctx, req.NamespacedName, &notification); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !notification.DeletionTimestamp.IsZero() {
		// Nothing external is owned yet; when enforcement lands this is where
		// cluster-side cleanup happens under a finalizer.
		return ctrl.Result{}, nil
	}

	// Persist status once, and only when it actually changed, so a no-op
	// reconcile doesn't emit an update event.
	base := notification.Status.DeepCopy()
	defer func() {
		if err != nil || reflect.DeepEqual(*base, notification.Status) {
			return
		}
		if uerr := r.Status().Update(ctx, &notification); uerr != nil {
			result, err = ctrl.Result{}, uerr
		}
	}()

	// Resolve the referenced bucket (same namespace).
	var bucket seaweedv1.Bucket
	bucketKey := types.NamespacedName{Namespace: notification.Namespace, Name: notification.Spec.BucketRef.Name}
	if err := r.Get(ctx, bucketKey, &bucket); err != nil {
		if apierrors.IsNotFound(err) {
			return r.pending(&notification, "BucketNotFound",
				fmt.Sprintf("Bucket %q not found in namespace %q", notification.Spec.BucketRef.Name, notification.Namespace)), nil
		}
		return ctrl.Result{}, err
	}

	bucketName := bucket.Status.BucketName
	if bucketName == "" {
		return r.pending(&notification, "BucketNotReady",
			fmt.Sprintf("Bucket %q is not provisioned yet", notification.Spec.BucketRef.Name)), nil
	}
	r.setCondition(&notification, seaweedv1.BucketNotificationConditionBucketResolved, metav1.ConditionTrue, "Resolved", "")

	// Resolve the cluster the bucket lives on.
	seaweedNS := bucket.Spec.ClusterRef.Namespace
	if seaweedNS == "" {
		seaweedNS = bucket.Namespace
	}
	var seaweed seaweedv1.Seaweed
	if err := r.Get(ctx, types.NamespacedName{Namespace: seaweedNS, Name: bucket.Spec.ClusterRef.Name}, &seaweed); err != nil {
		if apierrors.IsNotFound(err) {
			return r.pending(&notification, "ClusterRefNotFound",
				fmt.Sprintf("Seaweed %q not found in namespace %q", bucket.Spec.ClusterRef.Name, seaweedNS)), nil
		}
		return ctrl.Result{}, err
	}

	// Multiple BucketNotifications may target the same bucket, but rule names
	// must be unique across them — the bridge aggregates by name.
	conflict, err := r.ruleConflict(ctx, &notification)
	if err != nil {
		return ctrl.Result{}, err
	}
	if conflict != "" {
		r.setCondition(&notification, seaweedv1.BucketNotificationConditionRuleConflict, metav1.ConditionTrue, "DuplicateRuleName", conflict)
		return r.failPhase(&notification, "RuleConflict", conflict), nil
	}
	r.clearCondition(&notification, seaweedv1.BucketNotificationConditionRuleConflict)

	// TODO(#410): publish the resolved rule set to the enforcement layer
	// (bridge config or rendered notification.toml).

	notification.Status.BucketName = bucketName
	notification.Status.ClusterName = seaweed.Name
	notification.Status.ClusterNamespace = seaweedNS
	notification.Status.ObservedGeneration = notification.Generation
	notification.Status.AppliedRules = int32(len(notification.Spec.Rules))
	notification.Status.Phase = seaweedv1.BucketPhaseReady
	r.setCondition(&notification, seaweedv1.BucketNotificationConditionReady, metav1.ConditionTrue, "Reconciled", "")
	return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
}

// ruleConflict returns a message describing the first rule name on this
// resource that another BucketNotification already claims for the same
// bucket, or "" when there is no collision.
func (r *BucketNotificationReconciler) ruleConflict(ctx context.Context, notification *seaweedv1.BucketNotification) (string, error) {
	var peers seaweedv1.BucketNotificationList
	if err := r.List(ctx, &peers, client.InNamespace(notification.Namespace)); err != nil {
		return "", err
	}
	for i := range peers.Items {
		p := &peers.Items[i]
		if p.Name == notification.Name || p.Spec.BucketRef.Name != notification.Spec.BucketRef.Name || !p.DeletionTimestamp.IsZero() {
			continue
		}
		for _, mine := range notification.Spec.Rules {
			for _, theirs := range p.Spec.Rules {
				if mine.Name == theirs.Name {
					return fmt.Sprintf("rule %q conflicts with a rule of the same name on BucketNotification %q", mine.Name, p.Name), nil
				}
			}
		}
	}
	return "", nil
}

// pending records a Pending phase with the dependency that is missing,
// clearing readiness so a previously-Ready resource doesn't report stale
// readiness once it can no longer reconcile. It requeues on the transient
// cadence.
func (r *BucketNotificationReconciler) pending(notification *seaweedv1.BucketNotification, reason, message string) ctrl.Result {
	notification.Status.Phase = seaweedv1.BucketPhasePending
	r.setCondition(notification, seaweedv1.BucketNotificationConditionBucketResolved, metav1.ConditionFalse, reason, message)
	r.setCondition(notification, seaweedv1.BucketNotificationConditionReady, metav1.ConditionFalse, reason, message)
	return ctrl.Result{RequeueAfter: requeueAfterTransient}
}

func (r *BucketNotificationReconciler) failPhase(notification *seaweedv1.BucketNotification, reason, message string) ctrl.Result {
	r.Log.Info("reconcile failed", "reason", reason, "message", message)
	notification.Status.Phase = seaweedv1.BucketPhaseFailed
	r.setCondition(notification, seaweedv1.BucketNotificationConditionReady, metav1.ConditionFalse, reason, message)
	return ctrl.Result{RequeueAfter: requeueAfterTransient}
}

func (r *BucketNotificationReconciler) setCondition(notification *seaweedv1.BucketNotification, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&notification.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		ObservedGeneration: notification.Generation,
		Reason:             reason,
		Message:            message,
	})
}

func (r *BucketNotificationReconciler) clearCondition(notification *seaweedv1.BucketNotification, condType string) {
	meta.RemoveStatusCondition(&notification.Status.Conditions, condType)
}

// mapBucketToNotifications enqueues the notifications that reference a Bucket
// so they reconcile as soon as the bucket is provisioned, instead of waiting
// for the periodic requeue.
func (r *BucketNotificationReconciler) mapBucketToNotifications(ctx context.Context, obj client.Object) []reconcile.Request {
	bucket, ok := obj.(*seaweedv1.Bucket)
	if !ok {
		return nil
	}
	var notifications seaweedv1.BucketNotificationList
	if err := r.List(ctx, &notifications, client.InNamespace(bucket.Namespace)); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range notifications.Items {
		if notifications.Items[i].Spec.BucketRef.Name == bucket.Name {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&notifications.Items[i])})
		}
	}
	return reqs
}

// mapNotificationToPeers enqueues the other notifications targeting the same
// bucket so a conflict loser clears (or takes the name) promptly when a peer
// changes or is deleted.
func (r *BucketNotificationReconciler) mapNotificationToPeers(ctx context.Context, obj client.Object) []reconcile.Request {
	changed, ok := obj.(*seaweedv1.BucketNotification)
	if !ok {
		return nil
	}
	var notifications seaweedv1.BucketNotificationList
	if err := r.List(ctx, &notifications, client.InNamespace(changed.Namespace)); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range notifications.Items {
		n := &notifications.Items[i]
		if n.Name != changed.Name && n.Spec.BucketRef.Name == changed.Spec.BucketRef.Name {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(n)})
		}
	}
	return reqs
}

// SetupWithManager wires the reconciler into the controller-runtime manager.
func (r *BucketNotificationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&seaweedv1.BucketNotification{}).
		Watches(&seaweedv1.Bucket{}, handler.EnqueueRequestsFromMapFunc(r.mapBucketToNotifications)).
		// Only react to peers on spec/create/delete, never status-only updates,
		// so two same-bucket notifications don't ping-pong off each other's status.
		Watches(&seaweedv1.BucketNotification{}, handler.EnqueueRequestsFromMapFunc(r.mapNotificationToPeers),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}
