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
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func int32Ptr(v int32) *int32 { return &v }

func makeHostPathSeaweed(kind seaweedv1.VolumeServerKind, hostPaths []seaweedv1.VolumeServerHostPath) *seaweedv1.Seaweed {
	return &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Image:  "chrislusf/seaweedfs:latest",
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			Volume: &seaweedv1.VolumeSpec{
				Replicas: 3,
				Kind:     kind,
				HostPath: hostPaths,
			},
		},
	}
}

// volumeContainerCommand returns the rendered volume command from a pod spec.
func volumeContainerCommand(t *testing.T, podSpec corev1.PodSpec) string {
	t.Helper()
	for _, c := range podSpec.Containers {
		if c.Name == "volume" {
			if len(c.Command) != 3 {
				t.Fatalf("volume container Command = %v, want 3 elements", c.Command)
			}
			return c.Command[2]
		}
	}
	t.Fatal("no volume container found")
	return ""
}

func TestVolumeDaemonSet_UsesHostPathAndPodIP(t *testing.T) {
	m := makeHostPathSeaweed(seaweedv1.VolumeServerDaemonSet, []seaweedv1.VolumeServerHostPath{
		{Path: "/mnt/disk0"},
		{Path: "/mnt/disk1"},
	})

	r := &SeaweedReconciler{}
	ds := r.createVolumeServerDaemonSet(m)

	if ds.Name != "sw-volume" {
		t.Errorf("DaemonSet name = %q, want sw-volume", ds.Name)
	}
	if ds.Spec.UpdateStrategy.Type != "RollingUpdate" {
		t.Errorf("DaemonSet update strategy = %q, want RollingUpdate", ds.Spec.UpdateStrategy.Type)
	}

	// Two hostPath volumes, no PVC sources.
	var hostPaths []string
	for _, v := range ds.Spec.Template.Spec.Volumes {
		if v.HostPath != nil {
			hostPaths = append(hostPaths, v.HostPath.Path)
			if v.HostPath.Type == nil || *v.HostPath.Type != corev1.HostPathDirectoryOrCreate {
				t.Errorf("hostPath %q type = %v, want DirectoryOrCreate default", v.HostPath.Path, v.HostPath.Type)
			}
		}
		if v.PersistentVolumeClaim != nil {
			t.Errorf("DaemonSet must not reference a PVC volume source, got %q", v.PersistentVolumeClaim.ClaimName)
		}
	}
	if got, want := strings.Join(hostPaths, ","), "/mnt/disk0,/mnt/disk1"; got != want {
		t.Errorf("hostPath volumes = %q, want %q", got, want)
	}

	cmd := volumeContainerCommand(t, ds.Spec.Template.Spec)
	if !strings.Contains(cmd, "-ip=$(POD_IP)") {
		t.Errorf("DaemonSet startup must advertise $(POD_IP); got %q", cmd)
	}
	if !strings.Contains(cmd, "-dir=/data0,/data1") {
		t.Errorf("DaemonSet startup -dir mismatch; got %q", cmd)
	}
}

func TestVolumeStatefulSet_HostPathHasNoPVCTemplates(t *testing.T) {
	m := makeHostPathSeaweed(seaweedv1.VolumeServerStatefulSet, []seaweedv1.VolumeServerHostPath{
		{Path: "/mnt/disk0"},
	})

	r := &SeaweedReconciler{}
	sts := r.createVolumeServerStatefulSet(m)

	if len(sts.Spec.VolumeClaimTemplates) != 0 {
		t.Errorf("hostPath-backed StatefulSet must not create PVC templates, got %d", len(sts.Spec.VolumeClaimTemplates))
	}

	var hostPathFound bool
	for _, v := range sts.Spec.Template.Spec.Volumes {
		if v.HostPath != nil && v.HostPath.Path == "/mnt/disk0" {
			hostPathFound = true
		}
	}
	if !hostPathFound {
		t.Error("expected a hostPath volume for /mnt/disk0")
	}

	// StatefulSet still advertises the stable per-pod DNS name.
	cmd := volumeContainerCommand(t, sts.Spec.Template.Spec)
	if !strings.Contains(cmd, "-ip=$(POD_NAME).sw-volume-peer.ns") {
		t.Errorf("StatefulSet startup must advertise the peer DNS name; got %q", cmd)
	}
}

func TestVolumeHostPath_PerDirectoryMaxVolumeCount(t *testing.T) {
	m := makeHostPathSeaweed(seaweedv1.VolumeServerDaemonSet, []seaweedv1.VolumeServerHostPath{
		{Path: "/mnt/disk0", MaxVolumeCount: int32Ptr(7)},
		{Path: "/mnt/disk1"},
		{Path: "/mnt/disk2", MaxVolumeCount: int32Ptr(9)},
	})

	r := &SeaweedReconciler{}
	ds := r.createVolumeServerDaemonSet(m)
	cmd := volumeContainerCommand(t, ds.Spec.Template.Spec)

	// Unset entries fall back to 0 within the per-directory list.
	if !strings.Contains(cmd, "-max=7,0,9") {
		t.Errorf("per-directory -max mismatch; got %q", cmd)
	}
}

func TestVolumeHostPath_PerDirMaxFallsBackToGlobalForUnsetEntries(t *testing.T) {
	m := makeHostPathSeaweed(seaweedv1.VolumeServerDaemonSet, []seaweedv1.VolumeServerHostPath{
		{Path: "/mnt/disk0", MaxVolumeCount: int32Ptr(7)},
		{Path: "/mnt/disk1"},
	})
	m.Spec.Volume.MaxVolumeCounts = int32Ptr(5)

	r := &SeaweedReconciler{}
	ds := r.createVolumeServerDaemonSet(m)
	cmd := volumeContainerCommand(t, ds.Spec.Template.Spec)

	// Unset entry uses the global limit (5), not unlimited (0).
	if !strings.Contains(cmd, "-max=7,5") {
		t.Errorf("unset per-directory entry should fall back to global max; got %q", cmd)
	}
}

func TestVolumeDaemonSet_SkipsPublicURL(t *testing.T) {
	m := makeHostPathSeaweed(seaweedv1.VolumeServerDaemonSet, []seaweedv1.VolumeServerHostPath{
		{Path: "/mnt/disk0"},
	})
	suffix := "seaweed.example.com"
	m.Spec.HostSuffix = &suffix

	r := &SeaweedReconciler{}
	cmd := volumeContainerCommand(t, r.createVolumeServerDaemonSet(m).Spec.Template.Spec)
	if strings.Contains(cmd, "-publicUrl") {
		t.Errorf("DaemonSet must not advertise a publicUrl from the random pod name; got %q", cmd)
	}

	// A StatefulSet with the same HostSuffix still emits publicUrl.
	m.Spec.Volume.Kind = seaweedv1.VolumeServerStatefulSet
	m.Spec.Volume.HostPath = nil
	m.Spec.Volume.Requests = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}
	stsCmd := volumeContainerCommand(t, r.createVolumeServerStatefulSet(m).Spec.Template.Spec)
	if !strings.Contains(stsCmd, "-publicUrl=$(POD_NAME)."+suffix) {
		t.Errorf("StatefulSet should still advertise publicUrl; got %q", stsCmd)
	}
}

func TestVolumeHostPath_GlobalMaxVolumeCountWhenNoPerDir(t *testing.T) {
	m := makeHostPathSeaweed(seaweedv1.VolumeServerDaemonSet, []seaweedv1.VolumeServerHostPath{
		{Path: "/mnt/disk0"},
		{Path: "/mnt/disk1"},
	})
	m.Spec.Volume.MaxVolumeCounts = int32Ptr(12)

	r := &SeaweedReconciler{}
	ds := r.createVolumeServerDaemonSet(m)
	cmd := volumeContainerCommand(t, ds.Spec.Template.Spec)

	if !strings.Contains(cmd, "-max=12") {
		t.Errorf("global -max mismatch; got %q", cmd)
	}
	if strings.Contains(cmd, "-max=12,") {
		t.Errorf("expected single global -max, not a per-directory list; got %q", cmd)
	}
}

func TestVolumeHostPath_RespectsExplicitHostPathType(t *testing.T) {
	dir := corev1.HostPathDirectory
	m := makeHostPathSeaweed(seaweedv1.VolumeServerDaemonSet, []seaweedv1.VolumeServerHostPath{
		{Path: "/mnt/disk0", Type: &dir},
	})

	r := &SeaweedReconciler{}
	ds := r.createVolumeServerDaemonSet(m)

	for _, v := range ds.Spec.Template.Spec.Volumes {
		if v.HostPath != nil && v.HostPath.Path == "/mnt/disk0" {
			if v.HostPath.Type == nil || *v.HostPath.Type != corev1.HostPathDirectory {
				t.Errorf("explicit hostPath type not honored: %v", v.HostPath.Type)
			}
			return
		}
	}
	t.Fatal("hostPath volume for /mnt/disk0 not found")
}
