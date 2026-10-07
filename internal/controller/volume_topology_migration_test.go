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
	"sort"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// topologyOnlySeaweed returns a CR whose volume servers live entirely in
// spec.volumeTopology — the shape the relaxed webhook now admits.
func topologyOnlySeaweed() *seaweedv1.Seaweed {
	return &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
		Spec: seaweedv1.SeaweedSpec{
			Image:  "seaweedfs/seaweedfs:latest",
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			VolumeTopology: map[string]*seaweedv1.VolumeTopologySpec{
				"dc1": {
					Replicas:   1,
					Rack:       "r1",
					DataCenter: "dc1",
					VolumeServerConfig: seaweedv1.VolumeServerConfig{
						ResourceRequirements: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse("1Gi"),
							},
						},
					},
				},
			},
		},
	}
}

// flatVolumeService renders one of the Services the flat volume path used to
// own, labelled and owned the way ensureVolumeServerService leaves them.
func flatVolumeService(name, namespace string, uid types.UID) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labelsForVolumeServer("test"),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: seaweedv1.GroupVersion.String(),
				Kind:       "Seaweed",
				Name:       "test",
				UID:        uid,
				Controller: ptr.To(true),
			}},
		},
	}
}

// Once volumeTopology takes over, a leftover flat StatefulSet must be drained
// and removed rather than orphaned. The drain is gated on the master's volume
// counts: populated servers are evacuated in the background while the set is
// held, and only confirmed-empty servers are scaled away.
func TestEnsureVolumeServersTopologyRetiresFlatWorkload(t *testing.T) {
	m := topologyOnlySeaweed()
	ctx := context.Background()

	t.Run("populated flat set is held and evacuated", func(t *testing.T) {
		fa := &fakeVolumeAdmin{counts: map[string]int{
			volumeServerNodeAddress(m, 0): 4,
			volumeServerNodeAddress(m, 1): 7,
		}}
		r := newEvacTestReconciler(t, fa,
			m,
			volumeSTS("test-volume", "default", 2),
			flatVolumeService("test-volume-peer", "default", m.UID),
			flatVolumeService("test-volume-0", "default", m.UID),
			flatVolumeService("test-volume-1", "default", m.UID),
		)

		done, _, err := r.ensureVolumeServers(ctx, m)
		if err != nil {
			t.Fatalf("ensureVolumeServers: %v", err)
		}
		if !done {
			t.Fatal("expected the drain hold to stop reconciliation")
		}

		// The topology workload must exist so evacuated data has somewhere to go.
		topoSTS := &appsv1.StatefulSet{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: "default", Name: "test-volume-dc1"}, topoSTS); err != nil {
			t.Fatalf("topology StatefulSet was not created: %v", err)
		}

		// The flat set stays at full size while its servers hold data...
		flatSTS := &appsv1.StatefulSet{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: "default", Name: "test-volume"}, flatSTS); err != nil {
			t.Fatalf("flat StatefulSet must be kept while draining: %v", err)
		}
		if got := ptr.Deref(flatSTS.Spec.Replicas, 0); got != 2 {
			t.Fatalf("flat replicas = %d, want 2 held while draining", got)
		}
		// ...and the highest populated server is being evacuated. Its
		// Services must also stay up — the pod's registered peer DNS name
		// is how the evacuation traffic reaches it.
		waitForEvacuation(t, fa, volumeServerNodeAddress(m, 1))
		for _, svc := range []string{"test-volume-peer", "test-volume-0", "test-volume-1"} {
			if err := r.Get(ctx, types.NamespacedName{Namespace: "default", Name: svc}, &corev1.Service{}); err != nil {
				t.Fatalf("flat service %s removed while workload still draining: %v", svc, err)
			}
		}
	})

	t.Run("drained ordinals scale down then the set and services are removed", func(t *testing.T) {
		fa := &fakeVolumeAdmin{counts: map[string]int{
			volumeServerNodeAddress(m, 0): 0,
			volumeServerNodeAddress(m, 1): 0,
		}}
		r := newEvacTestReconciler(t, fa,
			m,
			volumeSTS("test-volume", "default", 2),
			flatVolumeService("test-volume-peer", "default", m.UID),
			flatVolumeService("test-volume-0", "default", m.UID),
			flatVolumeService("test-volume-1", "default", m.UID),
		)

		done, _, err := r.ensureVolumeServers(ctx, m)
		if err != nil {
			t.Fatalf("ensureVolumeServers: %v", err)
		}
		if !done {
			t.Fatal("expected a requeue while the drained StatefulSet is removed")
		}
		if err := r.Get(ctx, types.NamespacedName{Namespace: "default", Name: "test-volume"}, &appsv1.StatefulSet{}); !apierrors.IsNotFound(err) {
			t.Fatalf("flat StatefulSet should be deleted once drained, got err=%v", err)
		}

		// Second pass: workload is gone, so the flat Services are pruned.
		done, _, err = r.ensureVolumeServers(ctx, m)
		if err != nil {
			t.Fatalf("ensureVolumeServers: %v", err)
		}
		if done {
			t.Fatal("expected reconciliation to finish once nothing flat remains")
		}
		for _, svc := range []string{"test-volume-peer", "test-volume-0", "test-volume-1"} {
			if err := r.Get(ctx, types.NamespacedName{Namespace: "default", Name: svc}, &corev1.Service{}); !apierrors.IsNotFound(err) {
				t.Fatalf("flat service %s should be pruned, got err=%v", svc, err)
			}
		}

		// The topology resources must survive the flat prune.
		for _, svc := range []string{"test-volume-dc1-peer", "test-volume-dc1-0"} {
			if err := r.Get(ctx, types.NamespacedName{Namespace: "default", Name: svc}, &corev1.Service{}); err != nil {
				t.Fatalf("topology service %s must not be pruned: %v", svc, err)
			}
		}
	})

	t.Run("partial drain removes only confirmed-empty ordinals", func(t *testing.T) {
		fa := &fakeVolumeAdmin{counts: map[string]int{
			volumeServerNodeAddress(m, 0): 3, // still populated
			volumeServerNodeAddress(m, 1): 0, // drained
		}}
		r := newEvacTestReconciler(t, fa,
			m,
			volumeSTS("test-volume", "default", 2),
		)

		done, _, err := r.ensureVolumeServers(ctx, m)
		if err != nil {
			t.Fatalf("ensureVolumeServers: %v", err)
		}
		if !done {
			t.Fatal("expected a requeue while the remaining server drains")
		}
		flatSTS := &appsv1.StatefulSet{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: "default", Name: "test-volume"}, flatSTS); err != nil {
			t.Fatalf("flat StatefulSet: %v", err)
		}
		if got := ptr.Deref(flatSTS.Spec.Replicas, 0); got != 1 {
			t.Fatalf("flat replicas = %d, want 1 (drained ordinal removed)", got)
		}
		waitForEvacuation(t, fa, volumeServerNodeAddress(m, 0))
	})
}

// The HostSuffix all-in-one Ingress used to dereference spec.volume
// unconditionally, panicking on topology-only CRs during reconciliation. It
// must now publish each topology replica's per-pod host instead — the same
// name the pod advertises as -publicUrl.
func TestCreateAllIngressTopologyOnly(t *testing.T) {
	suffix := "example.com"
	m := topologyOnlySeaweed()
	m.Spec.HostSuffix = &suffix

	r, _ := componentIngressTestReconciler(t, m)
	ing := r.createAllIngress(m)

	var hosts []string
	for _, rule := range ing.Spec.Rules {
		hosts = append(hosts, rule.Host)
	}
	sort.Strings(hosts)

	want := []string{
		"filer.example.com",
		"test-volume-dc1-0.example.com",
	}
	sort.Strings(want)
	if len(hosts) != len(want) {
		t.Fatalf("hosts = %v, want %v", hosts, want)
	}
	for i := range want {
		if hosts[i] != want[i] {
			t.Fatalf("hosts = %v, want %v", hosts, want)
		}
	}
}

// With both spec.volume and spec.volumeTopology present, the all-in-one
// Ingress publishes flat and topology replica hosts side by side.
func TestCreateAllIngressFlatAndTopology(t *testing.T) {
	suffix := "example.com"
	m := topologyOnlySeaweed()
	m.Spec.HostSuffix = &suffix
	m.Spec.Volume = &seaweedv1.VolumeSpec{Replicas: 2}

	r, _ := componentIngressTestReconciler(t, m)
	ing := r.createAllIngress(m)

	var hosts []string
	for _, rule := range ing.Spec.Rules {
		hosts = append(hosts, rule.Host)
	}
	sort.Strings(hosts)

	want := []string{
		"filer.example.com",
		"test-volume-0.example.com",
		"test-volume-1.example.com",
		"test-volume-dc1-0.example.com",
	}
	sort.Strings(want)
	if len(hosts) != len(want) {
		t.Fatalf("hosts = %v, want %v", hosts, want)
	}
	for i := range want {
		if hosts[i] != want[i] {
			t.Fatalf("hosts = %v, want %v", hosts, want)
		}
	}
}

// While a flat workload is mid-drain it is still running, so status must
// count its live replicas — but never the removed spec.volume.replicas.
func TestGetVolumeStatusCountsDrainingFlatWorkload(t *testing.T) {
	m := topologyOnlySeaweed()
	// spec.volume is still set here, as in a mid-migration CR whose storage
	// requests are inherited by the topology groups.
	m.Spec.Volume = &seaweedv1.VolumeSpec{Replicas: 5}

	sts := volumeSTS("test-volume", "default", 2)
	sts.Status.ReadyReplicas = 1
	r := newEvacTestReconciler(t, &fakeVolumeAdmin{}, m, sts)

	status, err := r.getVolumeStatus(context.Background(), m)
	if err != nil {
		t.Fatalf("getVolumeStatus: %v", err)
	}
	// Flat contributes its live 2/1, topology its desired 1/0 — the stale
	// spec.volume.replicas=5 must not appear.
	if status.Replicas != 3 || status.ReadyReplicas != 1 {
		t.Fatalf("status = %d/%d, want 3/1 (2 live flat + 1 topology)", status.ReadyReplicas, status.Replicas)
	}
}
