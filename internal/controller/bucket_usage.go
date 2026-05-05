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
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// DefaultUsageRefreshInterval is the cadence at which bucket usage stats
// are recomputed when the operator is started without a CLI override.
const DefaultUsageRefreshInterval = 5 * time.Minute

// bucketUsageRunnable is a controller-runtime Runnable that periodically
// refreshes status.usage on every Bucket. It is registered with the
// manager so its lifecycle is bound to the manager's context — graceful
// shutdown cancels the loop instead of leaking the goroutine.
type bucketUsageRunnable struct {
	r        *BucketReconciler
	interval time.Duration
}

// Start blocks until ctx is cancelled, calling refreshAllUsage on each
// tick. It satisfies sigs.k8s.io/controller-runtime/pkg/manager.Runnable.
func (u *bucketUsageRunnable) Start(ctx context.Context) error {
	log := u.r.Log.WithName("bucket-usage")
	log.Info("starting bucket usage refresher", "interval", u.interval)

	ticker := time.NewTicker(u.interval)
	defer ticker.Stop()

	// Run once immediately so freshly-restarted operators populate
	// status.usage without waiting a full interval.
	u.r.refreshAllUsage(ctx, log)

	for {
		select {
		case <-ctx.Done():
			log.Info("stopping bucket usage refresher")
			return nil
		case <-ticker.C:
			u.r.refreshAllUsage(ctx, log)
		}
	}
}

// refreshAllUsage groups buckets by Seaweed cluster, fetches collection
// stats once per cluster, then updates each bucket's status.usage.
// Errors during a single cluster pass are logged and skipped — the next
// tick retries.
func (r *BucketReconciler) refreshAllUsage(ctx context.Context, log logr.Logger) {
	var bucketList seaweedv1.BucketList
	if err := r.List(ctx, &bucketList); err != nil {
		log.Error(err, "list buckets for usage refresh")
		return
	}

	// Group by clusterRef so each cluster gets one collection.list call
	// even when many buckets target it.
	type clusterKey struct{ ns, name string }
	groups := map[clusterKey][]*seaweedv1.Bucket{}
	for i := range bucketList.Items {
		b := &bucketList.Items[i]
		if b.Status.BucketName == "" {
			// Not yet successfully reconciled by the main loop.
			continue
		}
		ns := b.Spec.ClusterRef.Namespace
		if ns == "" {
			ns = b.Namespace
		}
		key := clusterKey{ns, b.Spec.ClusterRef.Name}
		groups[key] = append(groups[key], b)
	}

	for key, group := range groups {
		r.refreshClusterUsage(ctx, log, key.ns, key.name, group)
	}
}

func (r *BucketReconciler) refreshClusterUsage(ctx context.Context, log logr.Logger, seaweedNS, seaweedName string, buckets []*seaweedv1.Bucket) {
	var seaweed seaweedv1.Seaweed
	if err := r.Get(ctx, types.NamespacedName{Namespace: seaweedNS, Name: seaweedName}, &seaweed); err != nil {
		log.Error(err, "resolve clusterRef for usage refresh", "seaweed", seaweedName)
		return
	}
	masters := getMasterPeersString(&seaweed)
	filer := getFilerAddress(&seaweed)
	admin, err := r.getAdmin(seaweedNS, seaweedName, masters, filer, r.Log)
	if err != nil {
		log.Error(err, "build admin for usage refresh", "seaweed", seaweedName)
		return
	}

	stats, err := admin.ListCollectionStats(ctx)
	if err != nil {
		log.Error(err, "ListCollectionStats", "seaweed", seaweedName)
		return
	}
	now := metav1.Now()

	for _, b := range buckets {
		s := stats[b.Status.BucketName] // zero value when bucket has no objects yet
		newUsage := &seaweedv1.BucketUsage{
			ObjectCount: s.FileCount,
			SizeBytes:   s.SizeBytes,
			LastUpdated: &now,
		}
		// Skip the API write when nothing material changed. The
		// LastUpdated bump on its own is not worth a status round
		// trip; usage refresh is a best-effort observation.
		if usageEqual(b.Status.Usage, newUsage) {
			continue
		}
		// Patch with MergeFrom rather than Update so a concurrent
		// status write from the spec reconciler (e.g., a Conditions
		// update) does not race with this refresh and lose either
		// side. The merge is computed against the snapshot we listed,
		// so only status.usage is actually sent.
		patch := client.MergeFrom(b.DeepCopy())
		b.Status.Usage = newUsage
		if err := r.Status().Patch(ctx, b, patch); err != nil {
			log.Error(err, "status patch during usage refresh", "bucket", b.Name)
		}
	}
}

func usageEqual(a, b *seaweedv1.BucketUsage) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.ObjectCount == b.ObjectCount && a.SizeBytes == b.SizeBytes
}

// addUsageRunnable attaches the periodic usage refresher to the manager.
// Called from BucketReconciler.SetupWithManager when interval > 0.
func (r *BucketReconciler) addUsageRunnable(mgr ctrl.Manager, interval time.Duration) error {
	return mgr.Add(&bucketUsageRunnable{r: r, interval: interval})
}
