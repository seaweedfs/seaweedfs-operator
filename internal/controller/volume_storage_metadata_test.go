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
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// tridentAnnotations is the motivating case: CSI provisioners (here NetApp
// Trident) read these off the PVC at provision time.
var tridentAnnotations = map[string]string{
	"trident.netapp.io/snapshotPolicy":  "none",
	"trident.netapp.io/snapshotReserve": "0",
}

func makeStorageMetadataSeaweed(diskCount int32, annotations, labels map[string]string) *seaweedv1.Seaweed {
	return &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Master:                &seaweedv1.MasterSpec{Replicas: 1},
			VolumeServerDiskCount: &diskCount,
			Volume: &seaweedv1.VolumeSpec{
				Replicas: 3,
				VolumeServerConfig: seaweedv1.VolumeServerConfig{
					ResourceRequirements: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("30Gi"),
						},
					},
					StorageAnnotations: annotations,
					StorageLabels:      labels,
				},
			},
		},
	}
}

func TestVolumeStatefulSet_PropagatesStorageAnnotationsAndLabelsToEachPVCTemplate(t *testing.T) {
	labels := map[string]string{"team": "storage"}
	m := makeStorageMetadataSeaweed(2, tridentAnnotations, labels)

	r := &SeaweedReconciler{}
	sts := r.createVolumeServerStatefulSet(m)

	if got := len(sts.Spec.VolumeClaimTemplates); got != 2 {
		t.Fatalf("VolumeClaimTemplates len = %d, want 2 (one per disk)", got)
	}
	for i, pvc := range sts.Spec.VolumeClaimTemplates {
		if !apiequality.Semantic.DeepEqual(pvc.Annotations, tridentAnnotations) {
			t.Errorf("PVC[%d].Annotations = %#v, want %#v", i, pvc.Annotations, tridentAnnotations)
		}
		if !apiequality.Semantic.DeepEqual(pvc.Labels, labels) {
			t.Errorf("PVC[%d].Labels = %#v, want %#v", i, pvc.Labels, labels)
		}
	}
}

// Each per-disk PVC must own its maps — mutating one must not bleed into the
// others or the CR spec. Mirrors the storageSelector deep-copy guarantee.
func TestVolumeStatefulSet_StorageMetadataIsClonedPerPVC(t *testing.T) {
	anno := map[string]string{"trident.netapp.io/snapshotPolicy": "none"}
	m := makeStorageMetadataSeaweed(3, anno, nil)

	r := &SeaweedReconciler{}
	sts := r.createVolumeServerStatefulSet(m)
	if len(sts.Spec.VolumeClaimTemplates) != 3 {
		t.Fatalf("VolumeClaimTemplates len = %d, want 3", len(sts.Spec.VolumeClaimTemplates))
	}

	sts.Spec.VolumeClaimTemplates[0].Annotations["trident.netapp.io/snapshotPolicy"] = "mutated"
	if sts.Spec.VolumeClaimTemplates[1].Annotations["trident.netapp.io/snapshotPolicy"] != "none" {
		t.Errorf("mutating PVC[0] annotations leaked into PVC[1]: %#v", sts.Spec.VolumeClaimTemplates[1].Annotations)
	}
	if anno["trident.netapp.io/snapshotPolicy"] != "none" {
		t.Errorf("mutating PVC[0] annotations leaked back to the CR spec: %#v", anno)
	}
}

// Pod annotations (spec.volume.annotations) and PVC annotations
// (storageAnnotations) are independent surfaces — neither must leak into the other.
func TestVolumeStatefulSet_PVCAnnotationsAreSeparateFromPodAnnotations(t *testing.T) {
	m := makeStorageMetadataSeaweed(1, tridentAnnotations, nil)
	m.Spec.Volume.Annotations = map[string]string{"pod-only": "yes"}

	r := &SeaweedReconciler{}
	sts := r.createVolumeServerStatefulSet(m)

	pvc := sts.Spec.VolumeClaimTemplates[0]
	if _, leaked := pvc.Annotations["pod-only"]; leaked {
		t.Errorf("pod annotation leaked onto PVC template: %#v", pvc.Annotations)
	}
	if _, leaked := sts.Spec.Template.Annotations["trident.netapp.io/snapshotPolicy"]; leaked {
		t.Errorf("PVC annotation leaked onto pod template: %#v", sts.Spec.Template.Annotations)
	}
}

func TestVolumeStatefulSet_NilStorageMetadataYieldsNilPVCMetadata(t *testing.T) {
	m := makeStorageMetadataSeaweed(1, nil, nil)

	r := &SeaweedReconciler{}
	sts := r.createVolumeServerStatefulSet(m)

	pvc := sts.Spec.VolumeClaimTemplates[0]
	if pvc.Annotations != nil {
		t.Errorf("PVC.Annotations = %#v, want nil", pvc.Annotations)
	}
	if pvc.Labels != nil {
		t.Errorf("PVC.Labels = %#v, want nil", pvc.Labels)
	}
}

func TestVolumeTopologyStatefulSet_StorageMetadata_TopologyWinsAndFallsBack(t *testing.T) {
	flatAnno := map[string]string{"trident.netapp.io/snapshotPolicy": "flat"}
	topoAnno := map[string]string{"trident.netapp.io/snapshotPolicy": "topo"}

	diskCount := int32(1)
	newCR := func(topo *seaweedv1.VolumeTopologySpec) *seaweedv1.Seaweed {
		return &seaweedv1.Seaweed{
			ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
			Spec: seaweedv1.SeaweedSpec{
				Master:                &seaweedv1.MasterSpec{Replicas: 1},
				VolumeServerDiskCount: &diskCount,
				Volume: &seaweedv1.VolumeSpec{
					Replicas: 1,
					VolumeServerConfig: seaweedv1.VolumeServerConfig{
						StorageAnnotations: flatAnno,
					},
				},
				VolumeTopology: map[string]*seaweedv1.VolumeTopologySpec{"rack1": topo},
			},
		}
	}
	baseTopo := func() *seaweedv1.VolumeTopologySpec {
		return &seaweedv1.VolumeTopologySpec{
			VolumeServerConfig: seaweedv1.VolumeServerConfig{
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("30Gi")},
				},
			},
			Replicas:   1,
			Rack:       "rack1",
			DataCenter: "dc1",
		}
	}

	// Topology override wins.
	topoWin := baseTopo()
	topoWin.StorageAnnotations = topoAnno
	r := &SeaweedReconciler{}
	sts := r.createVolumeServerTopologyStatefulSet(newCR(topoWin), "rack1", topoWin)
	if !apiequality.Semantic.DeepEqual(sts.Spec.VolumeClaimTemplates[0].Annotations, topoAnno) {
		t.Errorf("topology PVC.Annotations = %#v, want %#v", sts.Spec.VolumeClaimTemplates[0].Annotations, topoAnno)
	}

	// Topology unset falls back to flat spec.volume.
	topoFallback := baseTopo()
	sts = r.createVolumeServerTopologyStatefulSet(newCR(topoFallback), "rack1", topoFallback)
	if !apiequality.Semantic.DeepEqual(sts.Spec.VolumeClaimTemplates[0].Annotations, flatAnno) {
		t.Errorf("topology PVC.Annotations = %#v, want fallback %#v", sts.Spec.VolumeClaimTemplates[0].Annotations, flatAnno)
	}
}

func TestGetStorageAnnotationsAndLabels_NilSafe(t *testing.T) {
	// Topology-only deployments omit spec.volume; helpers must not panic.
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec:       seaweedv1.SeaweedSpec{Master: &seaweedv1.MasterSpec{Replicas: 1}},
	}
	if got := getStorageAnnotations(m, nil); got != nil {
		t.Errorf("getStorageAnnotations with nil Volume and nil topology = %#v, want nil", got)
	}
	if got := getStorageLabels(m, nil); got != nil {
		t.Errorf("getStorageLabels with nil Volume and nil topology = %#v, want nil", got)
	}
}

// An explicitly empty topology map carries no intent for a bare map, so it must
// inherit the cluster-level value rather than silently erasing it for that rack.
func TestGetStorageAnnotationsAndLabels_EmptyTopologyMapInheritsCluster(t *testing.T) {
	clusterAnno := map[string]string{"trident.netapp.io/snapshotPolicy": "none"}
	clusterLabels := map[string]string{"team": "storage"}
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			Volume: &seaweedv1.VolumeSpec{
				VolumeServerConfig: seaweedv1.VolumeServerConfig{
					StorageAnnotations: clusterAnno,
					StorageLabels:      clusterLabels,
				},
			},
		},
	}
	topo := &seaweedv1.VolumeTopologySpec{
		VolumeServerConfig: seaweedv1.VolumeServerConfig{
			StorageAnnotations: map[string]string{},
			StorageLabels:      map[string]string{},
		},
	}
	if got := getStorageAnnotations(m, topo); !apiequality.Semantic.DeepEqual(got, clusterAnno) {
		t.Errorf("empty topology annotations should inherit cluster value, got %#v want %#v", got, clusterAnno)
	}
	if got := getStorageLabels(m, topo); !apiequality.Semantic.DeepEqual(got, clusterLabels) {
		t.Errorf("empty topology labels should inherit cluster value, got %#v want %#v", got, clusterLabels)
	}
}

func TestFilerStatefulSet_PropagatesPersistenceAnnotationsAndLabelsToPVCTemplate(t *testing.T) {
	labels := map[string]string{"team": "storage"}
	// MountPath/SubPath carry kubebuilder defaults the apiserver normally
	// fills; set them explicitly since this unit test bypasses defaulting.
	mountPath := "/data"
	subPath := ""
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Image:  "chrislusf/seaweedfs:3.96",
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			Volume: &seaweedv1.VolumeSpec{Replicas: 1},
			Filer: &seaweedv1.FilerSpec{
				Replicas: 1,
				Persistence: &seaweedv1.PersistenceSpec{
					Enabled:     true,
					MountPath:   &mountPath,
					SubPath:     &subPath,
					Annotations: tridentAnnotations,
					Labels:      labels,
				},
			},
		},
	}

	r := &SeaweedReconciler{}
	sts := r.createFilerStatefulSet(m)

	if got := len(sts.Spec.VolumeClaimTemplates); got != 1 {
		t.Fatalf("filer VolumeClaimTemplates len = %d, want 1", got)
	}
	pvc := sts.Spec.VolumeClaimTemplates[0]
	if !apiequality.Semantic.DeepEqual(pvc.Annotations, tridentAnnotations) {
		t.Errorf("filer PVC.Annotations = %#v, want %#v", pvc.Annotations, tridentAnnotations)
	}
	if !apiequality.Semantic.DeepEqual(pvc.Labels, labels) {
		t.Errorf("filer PVC.Labels = %#v, want %#v", pvc.Labels, labels)
	}
}
