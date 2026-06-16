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
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/grpc"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// fakeVolumeAdmin is a test double for VolumeAdmin. It is safe for concurrent
// use because EvacuateServer runs on a background goroutine.
type fakeVolumeAdmin struct {
	mu sync.Mutex

	counts    map[string]int
	countsErr error

	evacErr   error
	evacuated []string
	// evacGate, when non-nil, blocks EvacuateServer until the test closes it.
	evacGate chan struct{}

	countsCalls int
	closeCalls  int
}

func (f *fakeVolumeAdmin) VolumeServerVolumeCounts(_ context.Context) (map[string]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.countsCalls++
	if f.countsErr != nil {
		return nil, f.countsErr
	}
	cp := make(map[string]int, len(f.counts))
	for k, v := range f.counts {
		cp[k] = v
	}
	return cp, nil
}

func (f *fakeVolumeAdmin) EvacuateServer(_ context.Context, node string) error {
	f.mu.Lock()
	gate := f.evacGate
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evacuated = append(f.evacuated, node)
	return f.evacErr
}

func (f *fakeVolumeAdmin) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeCalls++
	return nil
}

func (f *fakeVolumeAdmin) evacuatedNodes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.evacuated...)
}

func newEvacTestReconciler(t *testing.T, fa *fakeVolumeAdmin, objs ...client.Object) *SeaweedReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("clientgoscheme: %v", err)
	}
	if err := seaweedv1.AddToScheme(scheme); err != nil {
		t.Fatalf("seaweedv1: %v", err)
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &SeaweedReconciler{
		Client: cli,
		Log:    logf.FromContext(context.Background()),
		Scheme: scheme,
		VolumeAdminFactory: func(_ string, _ grpc.DialOption, _ logr.Logger) (VolumeAdmin, error) {
			return fa, nil
		},
		evac: newEvacuationTracker(),
	}
}

func evacTestSeaweed() *seaweedv1.Seaweed {
	return &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: seaweedv1.SeaweedSpec{
			Image:  "seaweedfs/seaweedfs:latest",
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			Volume: &seaweedv1.VolumeSpec{Replicas: 2},
		},
	}
}

func volumeSTS(name, namespace string, replicas int32) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       appsv1.StatefulSetSpec{Replicas: ptr.To(replicas)},
	}
}

func waitForEvacuation(t *testing.T, fa *fakeVolumeAdmin, node string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, n := range fa.evacuatedNodes() {
			if n == node {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("evacuation of %s was not triggered; evacuated=%v", node, fa.evacuatedNodes())
}

func TestEvacuationDecision(t *testing.T) {
	nodeFor := func(ord int32) string { return nodeName(ord) }
	cases := []struct {
		name         string
		desired      int32
		current      int32
		counts       map[string]int
		wantAllowed  int32
		wantEvacuate string
	}{
		{
			name:    "scale up returns desired, evacuates nothing",
			desired: 4, current: 2, wantAllowed: 4, wantEvacuate: "",
		},
		{
			name:    "steady state returns desired",
			desired: 3, current: 3, wantAllowed: 3, wantEvacuate: "",
		},
		{
			name:    "top server still populated holds and evacuates it",
			desired: 2, current: 5,
			counts:      map[string]int{nodeName(4): 7, nodeName(3): 7, nodeName(2): 7},
			wantAllowed: 5, wantEvacuate: nodeName(4),
		},
		{
			name:    "top server drained shrinks by one and evacuates next",
			desired: 2, current: 5,
			counts:      map[string]int{nodeName(4): 0, nodeName(3): 7, nodeName(2): 7},
			wantAllowed: 4, wantEvacuate: nodeName(3),
		},
		{
			name:    "several top servers drained removed together",
			desired: 2, current: 5,
			counts:      map[string]int{nodeName(4): 0, nodeName(3): 0, nodeName(2): 7},
			wantAllowed: 3, wantEvacuate: nodeName(2),
		},
		{
			name:    "all doomed servers drained shrinks to desired",
			desired: 2, current: 5,
			counts:      map[string]int{nodeName(4): 0, nodeName(3): 0, nodeName(2): 0},
			wantAllowed: 2, wantEvacuate: "",
		},
		{
			name:    "top server not in topology holds without evacuating",
			desired: 2, current: 4,
			counts:      map[string]int{nodeName(2): 0}, // ordinal 3 missing
			wantAllowed: 4, wantEvacuate: "",
		},
		{
			name:    "drained top then missing holds at the missing one",
			desired: 1, current: 4,
			counts:      map[string]int{nodeName(3): 0, nodeName(1): 0}, // ordinal 2 missing
			wantAllowed: 3, wantEvacuate: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotAllowed, gotEvacuate := evacuationDecision(tc.desired, tc.current, nodeFor, tc.counts)
			if gotAllowed != tc.wantAllowed {
				t.Errorf("allowed = %d, want %d", gotAllowed, tc.wantAllowed)
			}
			if gotEvacuate != tc.wantEvacuate {
				t.Errorf("evacuate = %q, want %q", gotEvacuate, tc.wantEvacuate)
			}
		})
	}
}

// nodeName is a compact stand-in node id for evacuationDecision table tests.
func nodeName(ord int32) string { return "node-" + itoa(ord) }

func itoa(i int32) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [12]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}

func TestVolumeServerNodeAddress(t *testing.T) {
	m := evacTestSeaweed()
	if got, want := volumeServerNodeAddress(m, 3), "test-volume-3.test-volume-peer.default:8444"; got != want {
		t.Errorf("volumeServerNodeAddress = %q, want %q", got, want)
	}
	if got, want := volumeServerTopologyNodeAddress(m, "dc1", 2), "test-volume-dc1-2.test-volume-dc1-peer.default:8444"; got != want {
		t.Errorf("volumeServerTopologyNodeAddress = %q, want %q", got, want)
	}
}

func TestAllowedVolumeServerReplicas_NoStatefulSetReturnsDesired(t *testing.T) {
	fa := &fakeVolumeAdmin{}
	r := newEvacTestReconciler(t, fa, evacTestSeaweed())
	got, err := r.allowedVolumeServerReplicas(context.Background(), evacTestSeaweed(), "test-volume", 2,
		func(ord int32) string { return volumeServerNodeAddress(evacTestSeaweed(), ord) })
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Errorf("allowed = %d, want 2", got)
	}
	if fa.countsCalls != 0 {
		t.Errorf("expected no master call when StatefulSet is absent, got %d", fa.countsCalls)
	}
}

func TestAllowedVolumeServerReplicas_ScaleUpSkipsMaster(t *testing.T) {
	fa := &fakeVolumeAdmin{}
	m := evacTestSeaweed()
	r := newEvacTestReconciler(t, fa, m, volumeSTS("test-volume", "default", 2))
	got, err := r.allowedVolumeServerReplicas(context.Background(), m, "test-volume", 5,
		func(ord int32) string { return volumeServerNodeAddress(m, ord) })
	if err != nil {
		t.Fatal(err)
	}
	if got != 5 {
		t.Errorf("allowed = %d, want 5", got)
	}
	if fa.countsCalls != 0 {
		t.Errorf("expected no master call on scale-up, got %d", fa.countsCalls)
	}
}

func TestAllowedVolumeServerReplicas_HoldsAndEvacuatesPopulatedServer(t *testing.T) {
	m := evacTestSeaweed()
	fa := &fakeVolumeAdmin{counts: map[string]int{
		volumeServerNodeAddress(m, 2): 9,
	}}
	r := newEvacTestReconciler(t, fa, m, volumeSTS("test-volume", "default", 3))
	got, err := r.allowedVolumeServerReplicas(context.Background(), m, "test-volume", 2,
		func(ord int32) string { return volumeServerNodeAddress(m, ord) })
	if err != nil {
		t.Fatal(err)
	}
	if got != 3 {
		t.Errorf("allowed = %d, want 3 (held while draining)", got)
	}
	waitForEvacuation(t, fa, volumeServerNodeAddress(m, 2))
}

func TestAllowedVolumeServerReplicas_ShrinksAsServersDrain(t *testing.T) {
	m := evacTestSeaweed()
	// Ordinal 2 already empty, ordinal 1 (still desired) is not doomed.
	fa := &fakeVolumeAdmin{counts: map[string]int{
		volumeServerNodeAddress(m, 2): 0,
	}}
	r := newEvacTestReconciler(t, fa, m, volumeSTS("test-volume", "default", 3))
	got, err := r.allowedVolumeServerReplicas(context.Background(), m, "test-volume", 2,
		func(ord int32) string { return volumeServerNodeAddress(m, ord) })
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Errorf("allowed = %d, want 2 (drained server removed)", got)
	}
}

func TestAllowedVolumeServerReplicas_HoldsWhenTopologyUnavailable(t *testing.T) {
	m := evacTestSeaweed()
	fa := &fakeVolumeAdmin{countsErr: errors.New("masters unreachable")}
	r := newEvacTestReconciler(t, fa, m, volumeSTS("test-volume", "default", 5))
	got, err := r.allowedVolumeServerReplicas(context.Background(), m, "test-volume", 2,
		func(ord int32) string { return volumeServerNodeAddress(m, ord) })
	if err != nil {
		t.Fatalf("expected a held scale-down, not an error: %v", err)
	}
	if got != 5 {
		t.Errorf("allowed = %d, want 5 (hold current when drain state is unknown)", got)
	}
	if len(fa.evacuatedNodes()) != 0 {
		t.Errorf("no server should be evacuated when topology is unknown, got %v", fa.evacuatedNodes())
	}
}

func TestEvacuationTracker_DeduplicatesAndBacksOff(t *testing.T) {
	tr := newEvacuationTracker()
	clock := time.Unix(0, 0)
	tr.now = func() time.Time { return clock }

	release := make(chan struct{})
	var runs int
	var mu sync.Mutex
	run := func() error {
		<-release
		mu.Lock()
		runs++
		mu.Unlock()
		return errors.New("boom")
	}

	if !tr.start("n", run) {
		t.Fatal("first start should launch")
	}
	if tr.start("n", run) {
		t.Fatal("second start should be deduplicated while running")
	}
	close(release) // let the first attempt finish (and fail)

	// Wait for the goroutine to record the failure.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if tr.lastErr("n") != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if tr.lastErr("n") == nil {
		t.Fatal("expected recorded failure")
	}

	// Within the backoff window a retry is suppressed.
	noop := func() error { return nil }
	if tr.start("n", noop) {
		t.Fatal("retry within backoff window should be suppressed")
	}
	// Past the backoff window it is allowed again.
	clock = clock.Add(evacuationRetryBackoff + time.Second)
	if !tr.start("n", noop) {
		t.Fatal("retry after backoff window should launch")
	}

	// forget clears state so a fresh scale-down starts clean.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && tr.lastErr("n") != nil {
		time.Sleep(5 * time.Millisecond)
	}
	tr.forget("n")
	if tr.lastErr("n") != nil {
		t.Fatal("forget should drop tracker state")
	}
}
