/*
Copyright 2024.

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
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// Issue #249: ComponentSpec.InitContainers must reach the rendered pod
// template for every component. These tests pin the wiring at the
// controller-build layer (no apiserver round-trip) so a refactor that
// drops one of the .InitContainers() append calls fails in CI without
// needing the e2e kind cluster to run.

func newInitContainer(name string) corev1.Container {
	return corev1.Container{
		Name:    name,
		Image:   "busybox:1.36",
		Command: []string{"sh", "-c", "true"},
	}
}

func TestCreateMasterStatefulSet_PropagatesInitContainers(t *testing.T) {
	want := newInitContainer("master-init")
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{
				Replicas: 1,
				ComponentSpec: seaweedv1.ComponentSpec{
					InitContainers: []corev1.Container{want},
				},
			},
		},
	}
	r := &SeaweedReconciler{}
	sts := r.createMasterStatefulSet(m)
	got := sts.Spec.Template.Spec.InitContainers
	if len(got) != 1 || got[0].Name != want.Name || got[0].Image != want.Image {
		t.Fatalf("master initContainers = %#v, want one container named %q", got, want.Name)
	}
}

func TestCreateFilerStatefulSet_PropagatesInitContainers(t *testing.T) {
	// Two init containers exercises the canonical use case in #249
	// (wait for TCP, then wait for schema before the filer starts).
	first := newInitContainer("wait-cassandra")
	second := newInitContainer("wait-keyspace")
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			Filer: &seaweedv1.FilerSpec{
				Replicas: 1,
				ComponentSpec: seaweedv1.ComponentSpec{
					InitContainers: []corev1.Container{first, second},
				},
			},
		},
	}
	r := &SeaweedReconciler{}
	sts := r.createFilerStatefulSet(m)
	got := sts.Spec.Template.Spec.InitContainers
	if len(got) != 2 {
		t.Fatalf("filer initContainers len = %d, want 2 (%#v)", len(got), got)
	}
	if got[0].Name != first.Name || got[1].Name != second.Name {
		t.Fatalf("filer initContainers order = [%q, %q], want [%q, %q]",
			got[0].Name, got[1].Name, first.Name, second.Name)
	}
}

func TestCreateVolumeServerStatefulSet_PropagatesInitContainers(t *testing.T) {
	want := newInitContainer("volume-init")
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			Volume: &seaweedv1.VolumeSpec{
				Replicas: 1,
				VolumeServerConfig: seaweedv1.VolumeServerConfig{
					ComponentSpec: seaweedv1.ComponentSpec{
						InitContainers: []corev1.Container{want},
					},
					ResourceRequirements: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
					},
				},
			},
		},
	}
	r := &SeaweedReconciler{}
	sts := r.createVolumeServerStatefulSet(m)
	got := sts.Spec.Template.Spec.InitContainers
	if len(got) != 1 || got[0].Name != want.Name {
		t.Fatalf("volume initContainers = %#v, want one container named %q", got, want.Name)
	}
}

func TestCreateVolumeServerTopologyStatefulSet_PropagatesInitContainers(t *testing.T) {
	// Topology pods render through buildTopologyPodSpec, a separate code
	// path from BuildPodSpec. Pin the wiring on that branch too.
	want := newInitContainer("topology-init")
	topology := &seaweedv1.VolumeTopologySpec{
		VolumeServerConfig: seaweedv1.VolumeServerConfig{
			ComponentSpec: seaweedv1.ComponentSpec{
				InitContainers: []corev1.Container{want},
			},
			ResourceRequirements: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			},
		},
		Replicas:   1,
		Rack:       "rack1",
		DataCenter: "dc1",
	}
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			VolumeTopology: map[string]*seaweedv1.VolumeTopologySpec{
				"rack1": topology,
			},
		},
	}
	r := &SeaweedReconciler{}
	sts := r.createVolumeServerTopologyStatefulSet(m, "rack1", topology)
	got := sts.Spec.Template.Spec.InitContainers
	if len(got) != 1 || got[0].Name != want.Name {
		t.Fatalf("topology initContainers = %#v, want one container named %q", got, want.Name)
	}
}

func TestCreateAdminStatefulSet_PropagatesInitContainers(t *testing.T) {
	want := newInitContainer("admin-init")
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			Admin: &seaweedv1.AdminSpec{
				ComponentSpec: seaweedv1.ComponentSpec{
					InitContainers: []corev1.Container{want},
				},
			},
		},
	}
	r := &SeaweedReconciler{}
	sts := r.createAdminStatefulSet(m)
	got := sts.Spec.Template.Spec.InitContainers
	if len(got) != 1 || got[0].Name != want.Name {
		t.Fatalf("admin initContainers = %#v, want one container named %q", got, want.Name)
	}
}

func TestBuildS3Deployment_PropagatesInitContainers(t *testing.T) {
	want := newInitContainer("s3-init")
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			Filer:  &seaweedv1.FilerSpec{Replicas: 1},
			S3: &seaweedv1.S3GatewaySpec{
				Replicas: 1,
				ComponentSpec: seaweedv1.ComponentSpec{
					InitContainers: []corev1.Container{want},
				},
			},
		},
	}
	r := &SeaweedReconciler{}
	dep := r.buildS3Deployment(m)
	got := dep.Spec.Template.Spec.InitContainers
	if len(got) != 1 || got[0].Name != want.Name {
		t.Fatalf("s3 initContainers = %#v, want one container named %q", got, want.Name)
	}
}

func TestBuildSFTPDeployment_PropagatesInitContainers(t *testing.T) {
	want := newInitContainer("sftp-init")
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			Filer:  &seaweedv1.FilerSpec{Replicas: 1},
			SFTP: &seaweedv1.SFTPSpec{
				Replicas: 1,
				ComponentSpec: seaweedv1.ComponentSpec{
					InitContainers: []corev1.Container{want},
				},
			},
		},
	}
	r := &SeaweedReconciler{}
	dep := r.buildSFTPDeployment(m)
	got := dep.Spec.Template.Spec.InitContainers
	if len(got) != 1 || got[0].Name != want.Name {
		t.Fatalf("sftp initContainers = %#v, want one container named %q", got, want.Name)
	}
}

func TestCreateWorkerDeployment_PropagatesInitContainers(t *testing.T) {
	want := newInitContainer("worker-init")
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			Admin:  &seaweedv1.AdminSpec{},
			Worker: &seaweedv1.WorkerSpec{
				Replicas: 1,
				ComponentSpec: seaweedv1.ComponentSpec{
					InitContainers: []corev1.Container{want},
				},
			},
		},
	}
	r := &SeaweedReconciler{}
	dep := r.createWorkerDeployment(m)
	got := dep.Spec.Template.Spec.InitContainers
	if len(got) != 1 || got[0].Name != want.Name {
		t.Fatalf("worker initContainers = %#v, want one container named %q", got, want.Name)
	}
}

// When InitContainers is not set, the rendered pod template must not have
// any init containers — the operator does not generate its own.
func TestCreateMasterStatefulSet_NoInitContainersByDefault(t *testing.T) {
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1},
		},
	}
	r := &SeaweedReconciler{}
	sts := r.createMasterStatefulSet(m)
	if got := sts.Spec.Template.Spec.InitContainers; len(got) != 0 {
		t.Fatalf("master initContainers should default to empty, got %#v", got)
	}
}
