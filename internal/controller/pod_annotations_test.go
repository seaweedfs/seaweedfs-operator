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
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// Every component's pod template must carry cluster-level annotations merged
// with component-level ones (component wins on collision), so Helm-style
// checksum annotations trigger rollouts uniformly across components. The
// checksum/secrets key mirrors that motivating pattern.
func TestPodTemplateAnnotations_MergeClusterAndComponent(t *testing.T) {
	cluster := map[string]string{
		"cluster-scope":    "cluster",
		"checksum/secrets": "cluster-sum",
	}
	component := map[string]string{
		"component-scope":  "component",
		"checksum/secrets": "component-sum",
	}
	want := map[string]string{
		"cluster-scope":    "cluster",
		"component-scope":  "component",
		"checksum/secrets": "component-sum",
	}

	componentSpec := seaweedv1.ComponentSpec{Annotations: component}
	volumeConfig := seaweedv1.VolumeServerConfig{
		ComponentSpec: componentSpec,
		ResourceRequirements: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse("1Gi"),
			},
		},
	}
	newSeaweed := func() *seaweedv1.Seaweed {
		return &seaweedv1.Seaweed{
			ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
			Spec: seaweedv1.SeaweedSpec{
				Annotations: cluster,
				Master:      &seaweedv1.MasterSpec{Replicas: 1},
				Filer:       &seaweedv1.FilerSpec{Replicas: 1},
			},
		}
	}
	r := &SeaweedReconciler{}

	cases := []struct {
		name  string
		build func() map[string]string
	}{
		{"master", func() map[string]string {
			m := newSeaweed()
			m.Spec.Master.ComponentSpec = componentSpec
			return r.createMasterStatefulSet(m).Spec.Template.Annotations
		}},
		{"filer", func() map[string]string {
			m := newSeaweed()
			m.Spec.Filer.ComponentSpec = componentSpec
			return r.createFilerStatefulSet(m).Spec.Template.Annotations
		}},
		{"volume statefulset", func() map[string]string {
			m := newSeaweed()
			m.Spec.Volume = &seaweedv1.VolumeSpec{Replicas: 1, VolumeServerConfig: volumeConfig}
			return r.createVolumeServerStatefulSet(m).Spec.Template.Annotations
		}},
		{"volume daemonset", func() map[string]string {
			m := newSeaweed()
			m.Spec.Volume = &seaweedv1.VolumeSpec{
				Kind:               seaweedv1.VolumeServerDaemonSet,
				HostPath:           []seaweedv1.VolumeServerHostPath{{Path: "/mnt/disk0"}},
				VolumeServerConfig: seaweedv1.VolumeServerConfig{ComponentSpec: componentSpec},
			}
			return r.createVolumeServerDaemonSet(m).Spec.Template.Annotations
		}},
		{"admin", func() map[string]string {
			m := newSeaweed()
			m.Spec.Admin = &seaweedv1.AdminSpec{ComponentSpec: componentSpec}
			return r.createAdminStatefulSet(m).Spec.Template.Annotations
		}},
		{"worker", func() map[string]string {
			m := newSeaweed()
			m.Spec.Admin = &seaweedv1.AdminSpec{}
			m.Spec.Worker = &seaweedv1.WorkerSpec{Replicas: 1, ComponentSpec: componentSpec}
			return r.createWorkerDeployment(m).Spec.Template.Annotations
		}},
		{"s3", func() map[string]string {
			m := newSeaweed()
			m.Spec.S3 = &seaweedv1.S3GatewaySpec{Replicas: 1, ComponentSpec: componentSpec}
			return r.buildS3Deployment(m).Spec.Template.Annotations
		}},
		{"sftp", func() map[string]string {
			m := newSeaweed()
			m.Spec.SFTP = &seaweedv1.SFTPSpec{Replicas: 1, ComponentSpec: componentSpec}
			return r.buildSFTPDeployment(m).Spec.Template.Annotations
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.build(); !reflect.DeepEqual(got, want) {
				t.Errorf("pod template annotations = %v, want %v", got, want)
			}
		})
	}
}

// Topology pods layer a third tier on top: cluster + volume + topology, with
// the topology winning on collisions — the same order their labels use.
func TestVolumeTopologyPodTemplateAnnotations_ThreeTierMerge(t *testing.T) {
	topology := &seaweedv1.VolumeTopologySpec{
		VolumeServerConfig: seaweedv1.VolumeServerConfig{
			ComponentSpec: seaweedv1.ComponentSpec{
				Annotations: map[string]string{"topology-scope": "topology", "shared": "topology"},
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
			Annotations: map[string]string{"cluster-scope": "cluster", "shared": "cluster"},
			Master:      &seaweedv1.MasterSpec{Replicas: 1},
			Volume: &seaweedv1.VolumeSpec{
				VolumeServerConfig: seaweedv1.VolumeServerConfig{
					ComponentSpec: seaweedv1.ComponentSpec{
						Annotations: map[string]string{"volume-scope": "volume", "shared": "volume"},
					},
				},
			},
			VolumeTopology: map[string]*seaweedv1.VolumeTopologySpec{"rack1": topology},
		},
	}
	want := map[string]string{
		"cluster-scope":  "cluster",
		"volume-scope":   "volume",
		"topology-scope": "topology",
		"shared":         "topology",
	}

	r := &SeaweedReconciler{}
	got := r.createVolumeServerTopologyStatefulSet(m, "rack1", topology).Spec.Template.Annotations
	if !reflect.DeepEqual(got, want) {
		t.Errorf("topology pod template annotations = %v, want %v", got, want)
	}
}
