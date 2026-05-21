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

// Issue #251: ComponentSpec.PodSecurityContext and ComponentSpec.ContainerSecurityContext
// must reach the rendered pod template / operator-managed container for every
// component. These tests pin the wiring at the controller-build layer (no
// apiserver round-trip) so a refactor that drops one of the security-context
// assignments fails in CI without needing the e2e kind cluster.

func samplePodSecurityContext() *corev1.PodSecurityContext {
	runAsUser := int64(1000)
	fsGroup := int64(2000)
	runAsNonRoot := true
	return &corev1.PodSecurityContext{
		RunAsUser:    &runAsUser,
		RunAsNonRoot: &runAsNonRoot,
		FSGroup:      &fsGroup,
	}
}

func sampleContainerSecurityContext() *corev1.SecurityContext {
	allowPrivilegeEscalation := false
	readOnlyRootFilesystem := true
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: &allowPrivilegeEscalation,
		ReadOnlyRootFilesystem:   &readOnlyRootFilesystem,
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
	}
}

// mainContainer returns the operator-managed container — the named one — from a
// rendered pod template, so the assertion is not fooled by appended sidecars.
func mainContainer(t *testing.T, containers []corev1.Container, name string) corev1.Container {
	t.Helper()
	for _, c := range containers {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("container %q not found in %#v", name, containers)
	return corev1.Container{}
}

// assertSecurityContexts checks that every field set by samplePodSecurityContext
// and sampleContainerSecurityContext survives propagation, so a bug that drops
// any single field fails the test.
func assertSecurityContexts(t *testing.T, podSpec corev1.PodSpec, containerName string) {
	t.Helper()
	psc := podSpec.SecurityContext
	if psc == nil {
		t.Fatalf("%s pod securityContext = nil, want it propagated", containerName)
	}
	if psc.RunAsUser == nil || *psc.RunAsUser != 1000 {
		t.Fatalf("%s pod securityContext.runAsUser = %v, want 1000", containerName, psc.RunAsUser)
	}
	if psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot {
		t.Fatalf("%s pod securityContext.runAsNonRoot = %v, want true", containerName, psc.RunAsNonRoot)
	}
	if psc.FSGroup == nil || *psc.FSGroup != 2000 {
		t.Fatalf("%s pod securityContext.fsGroup = %v, want 2000", containerName, psc.FSGroup)
	}
	c := mainContainer(t, podSpec.Containers, containerName)
	csc := c.SecurityContext
	if csc == nil {
		t.Fatalf("%s container securityContext = nil, want it propagated", containerName)
	}
	if csc.AllowPrivilegeEscalation == nil || *csc.AllowPrivilegeEscalation {
		t.Fatalf("%s container securityContext.allowPrivilegeEscalation = %v, want false",
			containerName, csc.AllowPrivilegeEscalation)
	}
	if csc.ReadOnlyRootFilesystem == nil || !*csc.ReadOnlyRootFilesystem {
		t.Fatalf("%s container securityContext.readOnlyRootFilesystem = %v, want true",
			containerName, csc.ReadOnlyRootFilesystem)
	}
	if csc.Capabilities == nil || !containsCapability(csc.Capabilities.Drop, "ALL") {
		t.Fatalf("%s container securityContext.capabilities.drop = %v, want it to include ALL",
			containerName, csc.Capabilities)
	}
}

func containsCapability(caps []corev1.Capability, want corev1.Capability) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}

func componentSpecWithSecurityContext() seaweedv1.ComponentSpec {
	return seaweedv1.ComponentSpec{
		PodSecurityContext:       samplePodSecurityContext(),
		ContainerSecurityContext: sampleContainerSecurityContext(),
	}
}

func TestCreateMasterStatefulSet_PropagatesSecurityContext(t *testing.T) {
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{
				Replicas:      1,
				ComponentSpec: componentSpecWithSecurityContext(),
			},
		},
	}
	r := &SeaweedReconciler{}
	sts := r.createMasterStatefulSet(m)
	assertSecurityContexts(t, sts.Spec.Template.Spec, "master")
}

func TestCreateFilerStatefulSet_PropagatesSecurityContext(t *testing.T) {
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			Filer: &seaweedv1.FilerSpec{
				Replicas:      1,
				ComponentSpec: componentSpecWithSecurityContext(),
			},
		},
	}
	r := &SeaweedReconciler{}
	sts := r.createFilerStatefulSet(m)
	assertSecurityContexts(t, sts.Spec.Template.Spec, "filer")
}

func TestCreateVolumeServerStatefulSet_PropagatesSecurityContext(t *testing.T) {
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			Volume: &seaweedv1.VolumeSpec{
				Replicas: 1,
				VolumeServerConfig: seaweedv1.VolumeServerConfig{
					ComponentSpec: componentSpecWithSecurityContext(),
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
	assertSecurityContexts(t, sts.Spec.Template.Spec, "volume")
}

func TestCreateVolumeServerTopologyStatefulSet_PropagatesSecurityContext(t *testing.T) {
	// Topology pods render through buildTopologyPodSpec + getContainerSecurityContext,
	// a separate code path from BuildPodSpec. Pin the wiring on that branch too.
	topology := &seaweedv1.VolumeTopologySpec{
		VolumeServerConfig: seaweedv1.VolumeServerConfig{
			ComponentSpec: componentSpecWithSecurityContext(),
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
	assertSecurityContexts(t, sts.Spec.Template.Spec, "volume")
}

// A topology group that does not set its own securityContext inherits the flat
// spec.volume value, mirroring how env and resources fall back.
func TestCreateVolumeServerTopologyStatefulSet_InheritsVolumeSecurityContext(t *testing.T) {
	topology := &seaweedv1.VolumeTopologySpec{
		VolumeServerConfig: seaweedv1.VolumeServerConfig{
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
			Volume: &seaweedv1.VolumeSpec{
				VolumeServerConfig: seaweedv1.VolumeServerConfig{
					ComponentSpec: componentSpecWithSecurityContext(),
				},
			},
			VolumeTopology: map[string]*seaweedv1.VolumeTopologySpec{
				"rack1": topology,
			},
		},
	}
	r := &SeaweedReconciler{}
	sts := r.createVolumeServerTopologyStatefulSet(m, "rack1", topology)
	assertSecurityContexts(t, sts.Spec.Template.Spec, "volume")
}

func TestCreateAdminStatefulSet_PropagatesSecurityContext(t *testing.T) {
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			Admin: &seaweedv1.AdminSpec{
				ComponentSpec: componentSpecWithSecurityContext(),
			},
		},
	}
	r := &SeaweedReconciler{}
	sts := r.createAdminStatefulSet(m)
	assertSecurityContexts(t, sts.Spec.Template.Spec, "admin")
}

func TestBuildS3Deployment_PropagatesSecurityContext(t *testing.T) {
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			Filer:  &seaweedv1.FilerSpec{Replicas: 1},
			S3: &seaweedv1.S3GatewaySpec{
				Replicas:      1,
				ComponentSpec: componentSpecWithSecurityContext(),
			},
		},
	}
	r := &SeaweedReconciler{}
	dep := r.buildS3Deployment(m)
	assertSecurityContexts(t, dep.Spec.Template.Spec, "s3")
}

func TestBuildSFTPDeployment_PropagatesSecurityContext(t *testing.T) {
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			Filer:  &seaweedv1.FilerSpec{Replicas: 1},
			SFTP: &seaweedv1.SFTPSpec{
				Replicas:      1,
				ComponentSpec: componentSpecWithSecurityContext(),
			},
		},
	}
	r := &SeaweedReconciler{}
	dep := r.buildSFTPDeployment(m)
	assertSecurityContexts(t, dep.Spec.Template.Spec, "sftp")
}

func TestCreateWorkerDeployment_PropagatesSecurityContext(t *testing.T) {
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			Admin:  &seaweedv1.AdminSpec{},
			Worker: &seaweedv1.WorkerSpec{
				Replicas:      1,
				ComponentSpec: componentSpecWithSecurityContext(),
			},
		},
	}
	r := &SeaweedReconciler{}
	dep := r.createWorkerDeployment(m)
	assertSecurityContexts(t, dep.Spec.Template.Spec, "worker")
}

// When neither field is set, the rendered pod template and container must leave
// securityContext nil — the operator does not synthesize one.
func TestCreateMasterStatefulSet_NoSecurityContextByDefault(t *testing.T) {
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1},
		},
	}
	r := &SeaweedReconciler{}
	sts := r.createMasterStatefulSet(m)
	if sc := sts.Spec.Template.Spec.SecurityContext; sc != nil {
		t.Fatalf("master pod securityContext should default to nil, got %#v", sc)
	}
	c := mainContainer(t, sts.Spec.Template.Spec.Containers, "master")
	if c.SecurityContext != nil {
		t.Fatalf("master container securityContext should default to nil, got %#v", c.SecurityContext)
	}
}
