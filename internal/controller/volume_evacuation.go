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
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// evacuationRetryBackoff bounds how often a failed background evacuation is
// retried. A stuck evacuation (e.g. a volume with no replication-safe
// destination) errors quickly, and the reconcile loop revisits a draining
// cluster every few seconds; without a floor that would relaunch the move on
// every pass.
const evacuationRetryBackoff = 30 * time.Second

// evacuationTracker is the controller's in-memory registry of background volume
// server evacuations, keyed by node id (<host>:<port>). It is purely a
// concurrency guard and retry throttle: the authoritative "is this server
// drained?" signal is the master's volume count, never this tracker, so losing
// its state on an operator restart only re-triggers an evacuation that is then
// a fast no-op against an already-empty server.
type evacuationTracker struct {
	mu     sync.Mutex
	states map[string]*evacuationState
	now    func() time.Time // overridable in tests
}

type evacuationState struct {
	running   bool
	lastErr   error
	nextRetry time.Time
}

func newEvacuationTracker() *evacuationTracker {
	return &evacuationTracker{
		states: map[string]*evacuationState{},
		now:    time.Now,
	}
}

// start launches fn in the background for node unless an evacuation is already
// running for it, or a previous attempt failed within the retry backoff window.
// It reports whether a new goroutine was started so the caller can emit an
// event only on a genuine (re)launch.
func (t *evacuationTracker) start(node string, fn func() error) bool {
	t.mu.Lock()
	st := t.states[node]
	if st == nil {
		st = &evacuationState{}
		t.states[node] = st
	}
	if st.running || (st.lastErr != nil && t.now().Before(st.nextRetry)) {
		t.mu.Unlock()
		return false
	}
	st.running = true
	t.mu.Unlock()

	go func() {
		err := fn()
		t.mu.Lock()
		st.running = false
		st.lastErr = err
		if err != nil {
			st.nextRetry = t.now().Add(evacuationRetryBackoff)
		}
		t.mu.Unlock()
	}()
	return true
}

// lastErr returns the error from node's most recent finished evacuation, or nil
// if none has finished (or the last one succeeded).
func (t *evacuationTracker) lastErr(node string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if st := t.states[node]; st != nil {
		return st.lastErr
	}
	return nil
}

// forget drops node's state once it is no longer a scale-down target, so a
// later scale-down of a freshly repopulated server starts from a clean slate.
func (t *evacuationTracker) forget(node string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.states, node)
}

// evacuationDecision computes, for a volume StatefulSet whose desired replica
// count is below its current size, the replica count it may safely run at right
// now and the single highest still-populated doomed server that should be
// evacuated next ("" if none needs evacuating this pass).
//
// StatefulSets always delete the highest-ordinal pod first, so a server is only
// removable once it AND every higher-ordinal doomed server are drained. counts
// maps node id -> hosted volume/EC-shard count; a node missing from counts is
// not registered with the master and is treated as "not yet confirmed drained"
// (hold), because the operator must not delete a pod whose data it cannot see.
func evacuationDecision(desired, current int32, nodeFor func(int32) string, counts map[string]int) (allowed int32, evacuate string) {
	if desired >= current {
		return desired, ""
	}
	allowed = current
	for ord := current - 1; ord >= desired; ord-- {
		node := nodeFor(ord)
		if n, known := counts[node]; known && n == 0 {
			allowed = ord // drained: this server (and all above it) may go
			continue
		} else if known && n > 0 {
			evacuate = node // highest server still holding data: drain it next
		}
		// Either still populated or not visible in the topology yet: stop here so
		// no pod at or below this ordinal is removed until it is confirmed empty.
		break
	}
	if allowed < desired {
		allowed = desired
	}
	return allowed, evacuate
}

// allowedVolumeServerReplicas returns the replica count the named volume
// StatefulSet may run at this reconcile pass. On creation, scale-up, or steady
// state it is simply desired and no master call is made. On scale-down it caps
// the count so a volume server pod is removed only after the master confirms it
// holds no data; the highest still-populated server is evacuated in the
// background first. nodeFor maps a pod ordinal to its master node id.
func (r *SeaweedReconciler) allowedVolumeServerReplicas(ctx context.Context, m *seaweedv1.Seaweed, stsName string, desired int32, nodeFor func(int32) string) (int32, error) {
	// Gating depends on the master admin and the evacuation tracker, both wired
	// by SetupWithManager. When absent (e.g. a reconciler built directly in an
	// unrelated test) fall back to the plain desired count.
	if r.VolumeAdminFactory == nil || r.evac == nil {
		return desired, nil
	}

	sts := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Namespace: m.Namespace, Name: stsName}, sts)
	if apierrors.IsNotFound(err) {
		return desired, nil // not created yet: nothing to drain
	}
	if err != nil {
		return desired, err
	}

	current := ptr.Deref(sts.Spec.Replicas, 0)
	if desired >= current {
		return desired, nil // creation already handled above; this is scale-up/steady
	}

	counts, err := r.volumeServerVolumeCounts(ctx, m)
	if err != nil {
		// Without an authoritative drain signal we must not shrink: hold the
		// StatefulSet at its current size and retry on the next pass.
		r.Log.Error(err, "cannot read volume topology; holding volume scale-down", "statefulset", stsName)
		return current, nil
	}

	allowed, evacuate := evacuationDecision(desired, current, nodeFor, counts)
	if evacuate != "" {
		r.startVolumeServerEvacuation(ctx, m, evacuate)
	}
	// Drop tracker state for every server that is now leaving so a future
	// scale-down of a repopulated pod at the same ordinal starts fresh.
	for ord := allowed; ord < current; ord++ {
		r.evac.forget(nodeFor(ord))
	}
	return allowed, nil
}

// startVolumeServerEvacuation kicks off (or, after the retry backoff, retries) a
// background evacuation of node. The heavy data move runs on its own goroutine
// with a detached context so it survives this reconcile returning; the master
// volume count gates the actual pod removal.
func (r *SeaweedReconciler) startVolumeServerEvacuation(ctx context.Context, m *seaweedv1.Seaweed, node string) {
	masters := getMasterPeersString(m)
	dialOption, _, err := loadSeaweedGrpcDialOption(ctx, r.Client, m)
	if err != nil {
		r.Log.Error(err, "cannot build gRPC dial option for evacuation", "node", node)
		return
	}

	prevErr := r.evac.lastErr(node)
	started := r.evac.start(node, func() error {
		admin, err := r.VolumeAdminFactory(masters, dialOption, r.Log)
		if err != nil {
			return err
		}
		defer admin.Close()
		return admin.EvacuateServer(context.Background(), node)
	})
	if !started {
		return // already running, or waiting out the retry backoff
	}

	r.Log.Info("evacuating volume server before scale-down", "node", node)
	if prevErr != nil {
		r.recordVolumeEvent(m, corev1.EventTypeWarning, "VolumeServerEvacuationFailed",
			"Retrying evacuation of volume server %s after failure: %v", node, prevErr)
	} else {
		r.recordVolumeEvent(m, corev1.EventTypeNormal, "VolumeServerEvacuating",
			"Evacuating volume server %s before scale-down", node)
	}
}

// volumeServerVolumeCounts builds a short-lived admin and asks the master for
// the per-server volume counts used to gate scale-down.
func (r *SeaweedReconciler) volumeServerVolumeCounts(ctx context.Context, m *seaweedv1.Seaweed) (map[string]int, error) {
	masters := getMasterPeersString(m)
	dialOption, _, err := loadSeaweedGrpcDialOption(ctx, r.Client, m)
	if err != nil {
		return nil, err
	}
	admin, err := r.VolumeAdminFactory(masters, dialOption, r.Log)
	if err != nil {
		return nil, err
	}
	defer admin.Close()
	return admin.VolumeServerVolumeCounts(ctx)
}

func (r *SeaweedReconciler) recordVolumeEvent(m *seaweedv1.Seaweed, eventType, reason, messageFmt string, args ...interface{}) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(m, eventType, reason, messageFmt, args...)
}

// volumeServerNodeAddress is the master node id of the flat volume StatefulSet
// pod at the given ordinal: the pod's stable peer DNS name plus the volume HTTP
// port, matching the `-ip`/`-port` the pod registers with (see
// buildVolumeServerStartupScript).
func volumeServerNodeAddress(m *seaweedv1.Seaweed, ordinal int32) string {
	return fmt.Sprintf("%s-volume-%d.%s-volume-peer.%s:%d",
		m.Name, ordinal, m.Name, m.Namespace, seaweedv1.VolumeHTTPPort)
}

// volumeServerTopologyNodeAddress is volumeServerNodeAddress for a topology
// group's StatefulSet (see buildVolumeServerStartupScriptWithTopology).
func volumeServerTopologyNodeAddress(m *seaweedv1.Seaweed, topology string, ordinal int32) string {
	return fmt.Sprintf("%s-volume-%s-%d.%s-volume-%s-peer.%s:%d",
		m.Name, topology, ordinal, m.Name, topology, m.Namespace, seaweedv1.VolumeHTTPPort)
}
