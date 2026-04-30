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
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

const (
	// BucketFinalizer protects the Bucket CR from deletion until the
	// reconciler has had a chance to honor reclaimPolicy.
	BucketFinalizer = "seaweed.seaweedfs.com/bucket-protection"

	// AnnotationAppliedAccess records the IAM identities that the
	// reconciler last reconciled access for, so users removed from
	// spec.access can have their grants revoked on the next pass.
	// Stored as a comma-joined sorted list of identity names.
	AnnotationAppliedAccess = "bucket.seaweed.seaweedfs.com/applied-access"

	// requeueAfterTransient is the backoff used when an external
	// dependency (the filer, an IAM identity) is missing but expected to
	// arrive shortly.
	requeueAfterTransient = 30 * time.Second
)

// BucketReconciler reconciles Bucket resources by translating spec changes
// into `weed shell s3.bucket.*` and `fs.configure` calls against the target
// Seaweed cluster.
type BucketReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// AdminFactory creates a BucketAdmin for the master peers of the
	// target Seaweed cluster. Tests inject a fake; production wires
	// NewSwadminBucketAdmin.
	AdminFactory BucketAdminFactory

	// adminCache holds one BucketAdmin per (Seaweed CR identity, masters
	// string). swadmin.NewSeaweedAdmin spawns a background goroutine that
	// keeps a master connection alive, so caching avoids leaking one
	// goroutine per Reconcile. Entries are not actively evicted; if the
	// underlying Seaweed CR's master replica count changes, the next
	// Reconcile inserts a new entry under a different key and the old
	// connection lingers until operator restart. That is consistent with
	// the existing seaweed_maintenance.go pattern; a follow-up can plumb
	// a cancelable context through swadmin.SeaweedAdmin to do better.
	adminCache map[string]BucketAdmin
	adminMu    sync.Mutex
}

// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=buckets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=buckets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=buckets/finalizers,verbs=update

// Reconcile implements the bucket reconciliation logic.
func (r *BucketReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("bucket", req.NamespacedName)

	var bucket seaweedv1.Bucket
	if err := r.Get(ctx, req.NamespacedName, &bucket); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	bucketName := resolvedBucketName(&bucket)

	// Refuse rename attempts. Once a bucket is provisioned, status.bucketName
	// pins the name; any divergence indicates the user changed spec.name
	// after the fact. Renames in S3 / SeaweedFS are not a thing — the safe
	// move is to surface the error and let the user recreate.
	if bucket.Status.BucketName != "" && bucket.Status.BucketName != bucketName {
		msg := fmt.Sprintf("bucket name change from %q to %q is not supported; restore the original name or recreate the resource",
			bucket.Status.BucketName, bucketName)
		return r.failPhase(ctx, &bucket, seaweedv1.BucketPhaseFailed, "BucketRenameNotSupported", msg)
	}

	// Resolve the cluster reference. Cross-namespace is allowed.
	seaweedNS := bucket.Spec.ClusterRef.Namespace
	if seaweedNS == "" {
		seaweedNS = bucket.Namespace
	}
	var seaweed seaweedv1.Seaweed
	if err := r.Get(ctx, types.NamespacedName{Namespace: seaweedNS, Name: bucket.Spec.ClusterRef.Name}, &seaweed); err != nil {
		if apierrors.IsNotFound(err) {
			r.setCondition(&bucket, seaweedv1.BucketConditionClusterReachable, metav1.ConditionFalse, "ClusterRefNotFound",
				fmt.Sprintf("Seaweed %q not found in namespace %q", bucket.Spec.ClusterRef.Name, seaweedNS))
			bucket.Status.Phase = seaweedv1.BucketPhasePending
			if updateErr := r.Status().Update(ctx, &bucket); updateErr != nil {
				log.Error(updateErr, "status update")
			}
			return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
		}
		return ctrl.Result{}, err
	}
	r.setCondition(&bucket, seaweedv1.BucketConditionClusterReachable, metav1.ConditionTrue, "Reachable", "")

	masters := getMasterPeersString(&seaweed)
	admin, err := r.getAdmin(seaweedNS, seaweed.Name, masters, log)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Deletion path.
	if !bucket.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &bucket, bucketName, admin)
	}

	// Add finalizer if missing. Requeue immediately to pick up the
	// updated metadata before reconciling spec.
	if !controllerutil.ContainsFinalizer(&bucket, BucketFinalizer) {
		controllerutil.AddFinalizer(&bucket, BucketFinalizer)
		if err := r.Update(ctx, &bucket); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	return r.reconcileBucket(ctx, &bucket, bucketName, admin)
}

// reconcileBucket drives the create/update path: existence check, then
// each per-aspect command in dependency order (lock requires versioning,
// versioning requires the bucket exists, etc.). Failures surface as
// status conditions and return errors so controller-runtime requeues.
func (r *BucketReconciler) reconcileBucket(ctx context.Context, bucket *seaweedv1.Bucket, bucketName string, admin BucketAdmin) (ctrl.Result, error) {
	log := r.Log.WithValues("bucket", types.NamespacedName{Namespace: bucket.Namespace, Name: bucket.Name})

	exists, err := admin.BucketExists(ctx, bucketName)
	if err != nil {
		return r.failPhase(ctx, bucket, seaweedv1.BucketPhaseFailed, "ExistsCheckFailed", err.Error())
	}

	// Detect adoption attempt: bucket exists on the filer but our status
	// has never recorded its name. Refuse; users must remove the foreign
	// bucket manually before this CR can own it.
	if exists && bucket.Status.BucketName == "" {
		r.setCondition(bucket, seaweedv1.BucketConditionBucketAlreadyExists, metav1.ConditionTrue, "AlreadyExists",
			fmt.Sprintf("a bucket named %q already exists on cluster %q and was not created by this resource; adoption is not supported",
				bucketName, bucket.Spec.ClusterRef.Name))
		return r.failPhase(ctx, bucket, seaweedv1.BucketPhaseFailed, "BucketAlreadyExists",
			fmt.Sprintf("bucket %q already exists; refusing to adopt", bucketName))
	}

	if !exists {
		if err := admin.CreateBucket(ctx, bucketName, bucket.Spec.Owner, bucket.Spec.ObjectLock); err != nil {
			if errors.Is(err, ErrBucketAlreadyExists) {
				// Lost a race; fall through and treat as adoption-refused.
				r.setCondition(bucket, seaweedv1.BucketConditionBucketAlreadyExists, metav1.ConditionTrue, "AlreadyExists",
					fmt.Sprintf("bucket %q was created by another agent between exists check and create", bucketName))
				return r.failPhase(ctx, bucket, seaweedv1.BucketPhaseFailed, "BucketAlreadyExists", err.Error())
			}
			return r.failPhase(ctx, bucket, seaweedv1.BucketPhaseFailed, "CreateFailed", err.Error())
		}
		log.Info("created bucket", "name", bucketName, "owner", bucket.Spec.Owner, "withLock", bucket.Spec.ObjectLock)
	}

	// Object Lock: enable when requested and not already on. Versioning is
	// auto-enabled by the underlying command.
	if bucket.Spec.ObjectLock && !bucket.Status.ObjectLockEnabled {
		if err := admin.EnableObjectLock(ctx, bucketName); err != nil {
			return r.failPhase(ctx, bucket, seaweedv1.BucketPhaseFailed, "ObjectLockFailed", err.Error())
		}
	}
	r.setCondition(bucket, seaweedv1.BucketConditionObjectLockEnabled, boolToCondStatus(bucket.Spec.ObjectLock), "Reconciled", "")

	// Versioning: only push a state when one is requested. "Off" with no
	// prior state is a no-op (we don't try to "disable" — there's no
	// command for that, and CEL prevents transitioning back to Off).
	if bucket.Spec.Versioning != "" && bucket.Spec.Versioning != seaweedv1.VersioningOff {
		if err := admin.SetVersioning(ctx, bucketName, string(bucket.Spec.Versioning)); err != nil {
			if errors.Is(err, ErrObjectLockBlocksSuspend) {
				return r.failPhase(ctx, bucket, seaweedv1.BucketPhaseFailed, "VersioningSuspendBlocked", err.Error())
			}
			return r.failPhase(ctx, bucket, seaweedv1.BucketPhaseFailed, "VersioningFailed", err.Error())
		}
	}

	// Quota.
	if bucket.Spec.Quota != nil {
		sizeMiB, err := quantityToMiB(bucket.Spec.Quota.Size)
		if err != nil {
			return r.failPhase(ctx, bucket, seaweedv1.BucketPhaseFailed, "QuotaInvalid", err.Error())
		}
		if sizeMiB <= 0 {
			if err := admin.RemoveQuota(ctx, bucketName); err != nil {
				return r.failPhase(ctx, bucket, seaweedv1.BucketPhaseFailed, "QuotaRemoveFailed", err.Error())
			}
		} else {
			if err := admin.SetQuota(ctx, bucketName, sizeMiB, bucket.Spec.Quota.Enforce); err != nil {
				return r.failPhase(ctx, bucket, seaweedv1.BucketPhaseFailed, "QuotaFailed", err.Error())
			}
		}
		r.setCondition(bucket, seaweedv1.BucketConditionQuotaEnforced, boolToCondStatus(bucket.Spec.Quota.Enforce && sizeMiB > 0), "Reconciled", "")
	} else if bucket.Status.Quota != nil {
		// User removed the quota block — clear it on the filer.
		if err := admin.RemoveQuota(ctx, bucketName); err != nil {
			return r.failPhase(ctx, bucket, seaweedv1.BucketPhaseFailed, "QuotaRemoveFailed", err.Error())
		}
		r.setCondition(bucket, seaweedv1.BucketConditionQuotaEnforced, metav1.ConditionFalse, "Removed", "")
	}

	// Owner. Empty Owner with a previously-set owner means "remove".
	if bucket.Spec.Owner != "" {
		if bucket.Status.OwnerIdentity != bucket.Spec.Owner {
			if err := admin.SetOwner(ctx, bucketName, bucket.Spec.Owner); err != nil {
				return r.failPhase(ctx, bucket, seaweedv1.BucketPhaseFailed, "OwnerFailed", err.Error())
			}
		}
	} else if bucket.Status.OwnerIdentity != "" {
		if err := admin.RemoveOwner(ctx, bucketName); err != nil {
			return r.failPhase(ctx, bucket, seaweedv1.BucketPhaseFailed, "OwnerFailed", err.Error())
		}
	}

	// Access grants: declarative reconcile against the previous applied
	// list (recorded as an annotation). Users no longer in spec have
	// their bucket grants stripped; users in spec get their actions
	// (re)set to the requested set.
	prevUsers := readAppliedAccessAnnotation(bucket)
	desired := map[string]string{}
	for _, g := range bucket.Spec.Access {
		desired[g.User] = joinActions(g.Actions)
	}
	for user, actions := range desired {
		if err := admin.SetAccess(ctx, bucketName, user, actions); err != nil {
			return r.failPhase(ctx, bucket, seaweedv1.BucketPhaseFailed, "AccessFailed",
				fmt.Sprintf("set access for user %q: %s", user, err.Error()))
		}
	}
	for _, user := range prevUsers {
		if _, kept := desired[user]; kept {
			continue
		}
		if err := admin.SetAccess(ctx, bucketName, user, "none"); err != nil {
			return r.failPhase(ctx, bucket, seaweedv1.BucketPhaseFailed, "AccessRevokeFailed",
				fmt.Sprintf("revoke access for user %q: %s", user, err.Error()))
		}
	}
	if err := r.writeAppliedAccessAnnotation(ctx, bucket, desired); err != nil {
		return ctrl.Result{}, err
	}

	// Placement (fs.configure) — only call when at least one field set,
	// since fs.configure with no flags is a no-op write.
	if args := placementArgs(bucket.Spec.Placement); len(args) > 0 {
		prefix := "/buckets/" + bucketName + "/"
		if err := admin.Configure(ctx, prefix, args); err != nil {
			return r.failPhase(ctx, bucket, seaweedv1.BucketPhaseFailed, "PlacementFailed", err.Error())
		}
	}

	// Status: record observed state and mark Ready.
	bucket.Status.BucketName = bucketName
	bucket.Status.ObservedGeneration = bucket.Generation
	bucket.Status.Versioning = bucket.Spec.Versioning
	bucket.Status.ObjectLockEnabled = bucket.Spec.ObjectLock
	bucket.Status.OwnerIdentity = bucket.Spec.Owner
	if bucket.Spec.Quota != nil {
		sizeMiB, _ := quantityToMiB(bucket.Spec.Quota.Size)
		bucket.Status.Quota = &seaweedv1.BucketStatusQuota{
			SizeBytes: sizeMiB * 1024 * 1024,
			Enforced:  bucket.Spec.Quota.Enforce && sizeMiB > 0,
		}
	} else {
		bucket.Status.Quota = nil
	}
	bucket.Status.Phase = seaweedv1.BucketPhaseReady
	r.setCondition(bucket, seaweedv1.BucketConditionReady, metav1.ConditionTrue, "Reconciled", "")
	// Clear the rare conditions when they no longer apply.
	r.clearCondition(bucket, seaweedv1.BucketConditionBucketAlreadyExists)
	r.clearCondition(bucket, seaweedv1.BucketConditionDeleteBlockedByRetention)

	if err := r.Status().Update(ctx, bucket); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// handleDeletion drives the reclaim-policy path. Retain just removes the
// finalizer; Delete attempts s3.bucket.delete and surfaces retention
// blocks as a condition with a backoff retry.
func (r *BucketReconciler) handleDeletion(ctx context.Context, bucket *seaweedv1.Bucket, bucketName string, admin BucketAdmin) (ctrl.Result, error) {
	log := r.Log.WithValues("bucket", types.NamespacedName{Namespace: bucket.Namespace, Name: bucket.Name})

	if !controllerutil.ContainsFinalizer(bucket, BucketFinalizer) {
		return ctrl.Result{}, nil
	}

	bucket.Status.Phase = seaweedv1.BucketPhaseTerminating

	if bucket.Spec.ReclaimPolicy == seaweedv1.BucketReclaimDelete {
		err := admin.DeleteBucket(ctx, bucketName)
		switch {
		case err == nil, errors.Is(err, ErrBucketNotFound):
			// Clean exit, fall through to finalizer removal.
		case errors.Is(err, ErrRetentionBlocksDelete):
			r.setCondition(bucket, seaweedv1.BucketConditionDeleteBlockedByRetention, metav1.ConditionTrue, "RetentionActive",
				"bucket has objects under Object Lock retention or legal hold; flip reclaimPolicy to Retain and clear retention manually if you intend to delete")
			if updateErr := r.Status().Update(ctx, bucket); updateErr != nil {
				log.Error(updateErr, "status update during deletion")
			}
			return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
		default:
			r.setCondition(bucket, seaweedv1.BucketConditionReady, metav1.ConditionFalse, "DeleteFailed", err.Error())
			if updateErr := r.Status().Update(ctx, bucket); updateErr != nil {
				log.Error(updateErr, "status update during deletion")
			}
			return ctrl.Result{}, err
		}
	}

	controllerutil.RemoveFinalizer(bucket, BucketFinalizer)
	if err := r.Update(ctx, bucket); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// failPhase records the failure on Status, persists, and returns a
// requeue with a nil error. Returning a non-nil error alongside
// RequeueAfter would make controller-runtime ignore RequeueAfter and
// fall back to its default exponential backoff — but the failure is
// already captured on Status (Phase=Failed, Ready=False with
// reason+message), so the manager does not need an error return to
// know the reconcile didn't succeed. The deterministic
// `requeueAfterTransient` cadence is preserved.
//
// A non-nil error is still returned when the Status().Update itself
// fails so the manager retries on its rate limiter; that case
// signals an unhealthy API server, not a per-bucket failure.
func (r *BucketReconciler) failPhase(ctx context.Context, bucket *seaweedv1.Bucket, phase seaweedv1.BucketPhase, reason, message string) (ctrl.Result, error) {
	log := r.Log.WithValues("bucket", types.NamespacedName{Namespace: bucket.Namespace, Name: bucket.Name})
	log.Info("reconcile failed", "phase", phase, "reason", reason, "message", message)
	bucket.Status.Phase = phase
	r.setCondition(bucket, seaweedv1.BucketConditionReady, metav1.ConditionFalse, reason, message)
	if err := r.Status().Update(ctx, bucket); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
}

func (r *BucketReconciler) setCondition(bucket *seaweedv1.Bucket, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&bucket.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		ObservedGeneration: bucket.Generation,
		Reason:             reason,
		Message:            message,
	})
}

func (r *BucketReconciler) clearCondition(bucket *seaweedv1.Bucket, condType string) {
	meta.RemoveStatusCondition(&bucket.Status.Conditions, condType)
}

// getAdmin returns a cached BucketAdmin for the (Seaweed CR, masters)
// pair, creating one via the factory on first use. See the comment on
// adminCache about the goroutine-leak trade-off.
func (r *BucketReconciler) getAdmin(ns, name, masters string, log logr.Logger) (BucketAdmin, error) {
	key := ns + "/" + name + "@" + masters
	r.adminMu.Lock()
	defer r.adminMu.Unlock()
	if r.adminCache == nil {
		r.adminCache = make(map[string]BucketAdmin)
	}
	if a, ok := r.adminCache[key]; ok {
		return a, nil
	}
	a, err := r.AdminFactory(masters, log)
	if err != nil {
		return nil, err
	}
	r.adminCache[key] = a
	return a, nil
}

// SetupWithManager wires the reconciler into the controller-runtime manager.
func (r *BucketReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.AdminFactory == nil {
		r.AdminFactory = NewSwadminBucketAdmin
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&seaweedv1.Bucket{}).
		Complete(r)
}

// resolvedBucketName returns spec.Name when set, falling back to metadata.name.
func resolvedBucketName(b *seaweedv1.Bucket) string {
	if b.Spec.Name != "" {
		return b.Spec.Name
	}
	return b.Name
}

// quantityToMiB converts a resource.Quantity to whole MiB, rounding up.
// Returns an error for negative or non-integer-byte quantities (which
// resource.Quantity does support but make no sense as a bucket size).
func quantityToMiB(q resource.Quantity) (int64, error) {
	if q.Sign() < 0 {
		return 0, fmt.Errorf("quota size must be non-negative, got %s", q.String())
	}
	bytes, ok := q.AsInt64()
	if !ok {
		return 0, fmt.Errorf("quota size %s cannot be represented as int64 bytes", q.String())
	}
	const mib = int64(1024 * 1024)
	if bytes == 0 {
		return 0, nil
	}
	return (bytes + mib - 1) / mib, nil
}

// joinActions canonicalizes a list of access actions to the comma form the
// underlying s3.bucket.access command expects. Empty list returns "none"
// so the user's bucket grants are revoked rather than left unchanged.
func joinActions(actions []seaweedv1.BucketAccessAction) string {
	if len(actions) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(actions))
	for _, a := range actions {
		parts = append(parts, string(a))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// placementArgs flattens the BucketPlacement struct into the argv form
// expected by `weed shell fs.configure`. Only set fields are emitted so a
// re-reconcile with a partial placement spec doesn't accidentally clobber
// fields the user left at their cluster default.
func placementArgs(p *seaweedv1.BucketPlacement) []string {
	if p == nil {
		return nil
	}
	var out []string
	if p.Replication != "" {
		out = append(out, "-replication="+p.Replication)
	}
	if p.DiskType != "" {
		out = append(out, "-disk="+p.DiskType)
	}
	if p.TTL != "" {
		out = append(out, "-ttl="+p.TTL)
	}
	if p.Fsync {
		out = append(out, "-fsync=true")
	}
	if p.WORM {
		out = append(out, "-worm=true")
	}
	if p.ReadOnly {
		out = append(out, "-readOnly=true")
	}
	if p.DataCenter != "" {
		out = append(out, "-dataCenter="+p.DataCenter)
	}
	if p.Rack != "" {
		out = append(out, "-rack="+p.Rack)
	}
	if p.DataNode != "" {
		out = append(out, "-dataNode="+p.DataNode)
	}
	if p.VolumeGrowthCount != nil {
		out = append(out, fmt.Sprintf("-volumeGrowthCount=%d", *p.VolumeGrowthCount))
	}
	return out
}

func boolToCondStatus(b bool) metav1.ConditionStatus {
	if b {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

// readAppliedAccessAnnotation parses the comma-joined user list from the
// applied-access annotation. Returns an empty slice when the annotation is
// missing or empty.
func readAppliedAccessAnnotation(b *seaweedv1.Bucket) []string {
	val := b.Annotations[AnnotationAppliedAccess]
	if val == "" {
		return nil
	}
	parts := strings.Split(val, ",")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (r *BucketReconciler) writeAppliedAccessAnnotation(ctx context.Context, bucket *seaweedv1.Bucket, desired map[string]string) error {
	users := make([]string, 0, len(desired))
	for u := range desired {
		users = append(users, u)
	}
	sort.Strings(users)
	newVal := strings.Join(users, ",")

	if bucket.Annotations[AnnotationAppliedAccess] == newVal {
		return nil
	}
	if bucket.Annotations == nil {
		bucket.Annotations = map[string]string{}
	}
	bucket.Annotations[AnnotationAppliedAccess] = newVal
	return r.Update(ctx, bucket)
}
