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

// controllerRef returns the owner reference the operator stamps on every
// object it reconciles for m.
func controllerRef(m *seaweedv1.Seaweed, kind string) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: seaweedv1.GroupVersion.String(),
		Kind:       "Seaweed",
		Name:       m.Name,
		UID:        m.UID,
		Controller: ptr.To(true),
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

// flatVolumeSTS is the legacy <name>-volume StatefulSet as the flat path left
// it, owned by m.
func flatVolumeSTS(m *seaweedv1.Seaweed, replicas int32) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            m.Name + "-volume",
			Namespace:       m.Namespace,
			OwnerReferences: []metav1.OwnerReference{controllerRef(m, "StatefulSet")},
		},
		Spec: appsv1.StatefulSetSpec{Replicas: ptr.To(replicas)},
	}
}

// seedReadyTopologySTS installs a converged <name>-volume-<group> StatefulSet
// built from the controller's own desired object — matching claim templates
// are required, since reconcileVolumeClaimTemplates deletes and recreates the
// set on mismatch — then marked fully ready so the retirement gate opens.
func seedReadyTopologySTS(t *testing.T, ctx context.Context, r *SeaweedReconciler, m *seaweedv1.Seaweed, group string) {
	t.Helper()
	sts := r.createVolumeServerTopologyStatefulSet(m, group, m.Spec.VolumeTopology[group])
	sts.OwnerReferences = []metav1.OwnerReference{controllerRef(m, "StatefulSet")}
	if err := r.Create(ctx, sts); err != nil {
		t.Fatalf("create topology StatefulSet: %v", err)
	}
	sts.Status.ReadyReplicas = ptr.Deref(sts.Spec.Replicas, 0)
	if err := r.Status().Update(ctx, sts); err != nil {
		t.Fatalf("mark topology StatefulSet ready: %v", err)
	}
}

// flatDaemonSetWithPod builds the legacy DaemonSet and one of its pods the
// way the DaemonSet controller leaves them: the pod is controlled by the
// DaemonSet and registers with the master as <podIP>:8444.
func flatDaemonSetWithPod(m *seaweedv1.Seaweed, podName, podIP string) (*appsv1.DaemonSet, *corev1.Pod) {
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            m.Name + "-volume",
			Namespace:       m.Namespace,
			UID:             "ds-uid-1",
			OwnerReferences: []metav1.OwnerReference{controllerRef(m, "DaemonSet")},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: m.Namespace,
			Labels:    labelsForVolumeServer(m.Name),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "DaemonSet",
				Name:       ds.Name,
				UID:        ds.UID,
				Controller: ptr.To(true),
			}},
		},
		Status: corev1.PodStatus{PodIP: podIP},
	}
	return ds, pod
}

// Once volumeTopology takes over, a leftover flat StatefulSet must be drained
// and removed rather than orphaned. The drain is gated twice: the topology
// groups must have ready capacity first, then the master's volume counts
// decide which flat servers may go — populated ones are evacuated in the
// background and only confirmed-empty servers are scaled away.
func TestEnsureVolumeServersTopologyRetiresFlatWorkload(t *testing.T) {
	m := topologyOnlySeaweed()
	ctx := context.Background()

	t.Run("flat set is held until topology replicas are ready", func(t *testing.T) {
		fa := &fakeVolumeAdmin{counts: map[string]int{
			volumeServerNodeAddress(m, 0): 0,
			volumeServerNodeAddress(m, 1): 0,
		}}
		r := newEvacTestReconciler(t, fa,
			m,
			flatVolumeSTS(m, 2),
			flatVolumeService("test-volume-peer", "default", m.UID),
		)

		done, _, err := r.ensureVolumeServers(ctx, m)
		if err != nil {
			t.Fatalf("ensureVolumeServers: %v", err)
		}
		if !done {
			t.Fatal("expected a hold while topology capacity is pending")
		}

		// The topology StatefulSet exists (created this pass) but reports no
		// ready replicas, so the flat set — even fully drained — must stay.
		flatSTS := &appsv1.StatefulSet{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: "default", Name: "test-volume"}, flatSTS); err != nil {
			t.Fatalf("flat StatefulSet must be kept until topology is ready: %v", err)
		}
		if got := ptr.Deref(flatSTS.Spec.Replicas, 0); got != 2 {
			t.Fatalf("flat replicas = %d, want 2 held for missing capacity", got)
		}
	})

	t.Run("populated flat set is held and evacuated", func(t *testing.T) {
		fa := &fakeVolumeAdmin{counts: map[string]int{
			volumeServerNodeAddress(m, 0): 4,
			volumeServerNodeAddress(m, 1): 7,
		}}
		r := newEvacTestReconciler(t, fa,
			m,
			flatVolumeSTS(m, 2),
			flatVolumeService("test-volume-peer", "default", m.UID),
			flatVolumeService("test-volume-0", "default", m.UID),
			flatVolumeService("test-volume-1", "default", m.UID),
		)
		seedReadyTopologySTS(t, ctx, r, m, "dc1")

		done, _, err := r.ensureVolumeServers(ctx, m)
		if err != nil {
			t.Fatalf("ensureVolumeServers: %v", err)
		}
		if !done {
			t.Fatal("expected the drain hold to stop reconciliation")
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

	t.Run("drained flat set and its services are removed", func(t *testing.T) {
		fa := &fakeVolumeAdmin{counts: map[string]int{
			volumeServerNodeAddress(m, 0): 0,
			volumeServerNodeAddress(m, 1): 0,
		}}
		r := newEvacTestReconciler(t, fa,
			m,
			flatVolumeSTS(m, 2),
			flatVolumeService("test-volume-peer", "default", m.UID),
			flatVolumeService("test-volume-0", "default", m.UID),
			flatVolumeService("test-volume-1", "default", m.UID),
		)
		seedReadyTopologySTS(t, ctx, r, m, "dc1")

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
			flatVolumeSTS(m, 2),
		)
		seedReadyTopologySTS(t, ctx, r, m, "dc1")

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

	t.Run("workload not owned by this CR is left alone", func(t *testing.T) {
		foreignSTS := flatVolumeSTS(m, 2)
		foreignSTS.OwnerReferences = nil
		r := newEvacTestReconciler(t, &fakeVolumeAdmin{}, m, foreignSTS)
		seedReadyTopologySTS(t, ctx, r, m, "dc1")

		if _, _, err := r.ensureVolumeServers(ctx, m); err != nil {
			t.Fatalf("ensureVolumeServers: %v", err)
		}
		got := &appsv1.StatefulSet{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: "default", Name: "test-volume"}, got); err != nil {
			t.Fatalf("unowned StatefulSet must not be deleted: %v", err)
		}
		if replicas := ptr.Deref(got.Spec.Replicas, 0); replicas != 2 {
			t.Fatalf("unowned StatefulSet scaled to %d, want untouched 2", replicas)
		}
	})

	t.Run("populated flat DaemonSet is held and evacuated", func(t *testing.T) {
		ds, pod := flatDaemonSetWithPod(m, "test-volume-abcde", "10.0.0.5")
		fa := &fakeVolumeAdmin{counts: map[string]int{
			"10.0.0.5:8444": 6,
		}}
		r := newEvacTestReconciler(t, fa, m, ds, pod)
		seedReadyTopologySTS(t, ctx, r, m, "dc1")

		done, _, err := r.ensureVolumeServers(ctx, m)
		if err != nil {
			t.Fatalf("ensureVolumeServers: %v", err)
		}
		if !done {
			t.Fatal("expected the DaemonSet drain to hold reconciliation")
		}
		if err := r.Get(ctx, types.NamespacedName{Namespace: "default", Name: "test-volume"}, &appsv1.DaemonSet{}); err != nil {
			t.Fatalf("DaemonSet must be kept while its server holds volumes: %v", err)
		}
		waitForEvacuation(t, fa, "10.0.0.5:8444")
	})

	t.Run("drained flat DaemonSet is removed", func(t *testing.T) {
		ds, pod := flatDaemonSetWithPod(m, "test-volume-abcde", "10.0.0.5")
		fa := &fakeVolumeAdmin{counts: map[string]int{
			"10.0.0.5:8444": 0,
		}}
		r := newEvacTestReconciler(t, fa, m, ds, pod)
		seedReadyTopologySTS(t, ctx, r, m, "dc1")

		done, _, err := r.ensureVolumeServers(ctx, m)
		if err != nil {
			t.Fatalf("ensureVolumeServers: %v", err)
		}
		if !done {
			t.Fatal("expected a requeue while the drained DaemonSet is removed")
		}
		if err := r.Get(ctx, types.NamespacedName{Namespace: "default", Name: "test-volume"}, &appsv1.DaemonSet{}); !apierrors.IsNotFound(err) {
			t.Fatalf("flat DaemonSet should be deleted once drained, got err=%v", err)
		}
	})
}

// The HostSuffix all-in-one Ingress publishes a host per live per-replica
// volume Service — the same name the pod advertises as -publicUrl — for flat
// and topology servers alike. A replica held during evacuation keeps its
// host; a pruned Service's host disappears.
func TestCreateAllIngressVolumeHosts(t *testing.T) {
	ctx := context.Background()
	suffix := "example.com"

	t.Run("topology-only CR publishes topology replica hosts", func(t *testing.T) {
		m := topologyOnlySeaweed()
		m.Spec.HostSuffix = &suffix
		topoSvc := flatVolumeService("test-volume-dc1-0", "default", m.UID)
		topoSvc.Labels["seaweedfs/topology"] = "dc1"
		r, _ := componentIngressTestReconciler(t, m, topoSvc)

		ing, err := r.createAllIngress(ctx, m)
		if err != nil {
			t.Fatalf("createAllIngress: %v", err)
		}
		var hosts []string
		for _, rule := range ing.Spec.Rules {
			hosts = append(hosts, rule.Host)
		}
		sort.Strings(hosts)
		want := []string{"filer.example.com", "test-volume-dc1-0.example.com"}
		if len(hosts) != len(want) || hosts[0] != want[0] || hosts[1] != want[1] {
			t.Fatalf("hosts = %v, want %v", hosts, want)
		}
	})

	t.Run("flat services still serving keep their hosts", func(t *testing.T) {
		m := topologyOnlySeaweed()
		m.Spec.HostSuffix = &suffix
		// Mid-migration: flat services still exist alongside topology ones.
		topoSvc := flatVolumeService("test-volume-dc1-0", "default", m.UID)
		topoSvc.Labels["seaweedfs/topology"] = "dc1"
		r, _ := componentIngressTestReconciler(t, m,
			topoSvc,
			flatVolumeService("test-volume-0", "default", m.UID),
			flatVolumeService("test-volume-peer", "default", m.UID),
		)

		ing, err := r.createAllIngress(ctx, m)
		if err != nil {
			t.Fatalf("createAllIngress: %v", err)
		}
		var hosts []string
		for _, rule := range ing.Spec.Rules {
			hosts = append(hosts, rule.Host)
		}
		sort.Strings(hosts)
		// test-volume-peer is not a per-replica Service and gets no host.
		want := []string{
			"filer.example.com",
			"test-volume-0.example.com",
			"test-volume-dc1-0.example.com",
		}
		if len(hosts) != len(want) {
			t.Fatalf("hosts = %v, want %v", hosts, want)
		}
		for i := range want {
			if hosts[i] != want[i] {
				t.Fatalf("hosts = %v, want %v", hosts, want)
			}
		}
	})

	t.Run("pruned flat services lose their hosts", func(t *testing.T) {
		m := topologyOnlySeaweed()
		m.Spec.HostSuffix = &suffix
		m.Spec.Volume = &seaweedv1.VolumeSpec{Replicas: 2} // kept for inheritance
		topoSvc := flatVolumeService("test-volume-dc1-0", "default", m.UID)
		topoSvc.Labels["seaweedfs/topology"] = "dc1"
		r, _ := componentIngressTestReconciler(t, m, topoSvc)

		ing, err := r.createAllIngress(ctx, m)
		if err != nil {
			t.Fatalf("createAllIngress: %v", err)
		}
		for _, rule := range ing.Spec.Rules {
			if rule.Host == "test-volume-0.example.com" || rule.Host == "test-volume-1.example.com" {
				t.Fatalf("dead flat host %s still published after services pruned", rule.Host)
			}
		}
	})
}

// While a flat workload is mid-drain it is still running, so status must
// count its live replicas — but never the removed spec.volume.replicas, and
// never a foreign object that happens to share the generated name.
func TestGetVolumeStatusCountsDrainingFlatWorkload(t *testing.T) {
	m := topologyOnlySeaweed()
	// spec.volume is still set here, as in a mid-migration CR whose storage
	// requests are inherited by the topology groups.
	m.Spec.Volume = &seaweedv1.VolumeSpec{Replicas: 5}

	t.Run("owned flat set reports live replicas", func(t *testing.T) {
		sts := flatVolumeSTS(m, 2)
		sts.Status.ReadyReplicas = 1
		r := newEvacTestReconciler(t, &fakeVolumeAdmin{}, m, sts)

		status, err := r.getVolumeStatus(context.Background(), m)
		if err != nil {
			t.Fatalf("getVolumeStatus: %v", err)
		}
		// Flat contributes its live 2/1, topology its desired 1/0 — the
		// stale spec.volume.replicas=5 must not appear.
		if status.Replicas != 3 || status.ReadyReplicas != 1 {
			t.Fatalf("status = %d/%d, want 3/1 (2 live flat + 1 topology)", status.ReadyReplicas, status.Replicas)
		}
	})

	t.Run("unowned flat set is ignored", func(t *testing.T) {
		sts := flatVolumeSTS(m, 2)
		sts.Status.ReadyReplicas = 1
		sts.OwnerReferences = nil
		r := newEvacTestReconciler(t, &fakeVolumeAdmin{}, m, sts)

		status, err := r.getVolumeStatus(context.Background(), m)
		if err != nil {
			t.Fatalf("getVolumeStatus: %v", err)
		}
		if status.Replicas != 1 || status.ReadyReplicas != 0 {
			t.Fatalf("status = %d/%d, want 1/0 (topology only)", status.ReadyReplicas, status.Replicas)
		}
	})
}
