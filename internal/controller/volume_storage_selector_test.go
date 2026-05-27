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

func makeStorageSelectorSeaweed(diskCount int32, selector *metav1.LabelSelector) *seaweedv1.Seaweed {
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
					StorageSelector: selector,
				},
			},
		},
	}
}

func TestVolumeStatefulSet_PropagatesStorageSelectorToEachPVCTemplate(t *testing.T) {
	sel := &metav1.LabelSelector{
		MatchLabels: map[string]string{"tier": "fast"},
		MatchExpressions: []metav1.LabelSelectorRequirement{{
			Key:      "zone",
			Operator: metav1.LabelSelectorOpIn,
			Values:   []string{"us-east-1a", "us-east-1b"},
		}},
	}
	m := makeStorageSelectorSeaweed(2, sel)

	r := &SeaweedReconciler{}
	sts := r.createVolumeServerStatefulSet(m)

	if got := len(sts.Spec.VolumeClaimTemplates); got != 2 {
		t.Fatalf("VolumeClaimTemplates len = %d, want 2 (one per disk)", got)
	}
	for i, pvc := range sts.Spec.VolumeClaimTemplates {
		if !apiequality.Semantic.DeepEqual(pvc.Spec.Selector, sel) {
			t.Errorf("PVC[%d].Spec.Selector = %#v, want %#v", i, pvc.Spec.Selector, sel)
		}
	}
}

func TestVolumeStatefulSet_StorageSelectorIsDeepCopiedPerPVC(t *testing.T) {
	// Each PVC template must own its selector — mutating one must not bleed
	// into the others or back into the CR.
	sel := &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "fast"}}
	m := makeStorageSelectorSeaweed(3, sel)

	r := &SeaweedReconciler{}
	sts := r.createVolumeServerStatefulSet(m)
	if len(sts.Spec.VolumeClaimTemplates) != 3 {
		t.Fatalf("VolumeClaimTemplates len = %d, want 3", len(sts.Spec.VolumeClaimTemplates))
	}

	first := sts.Spec.VolumeClaimTemplates[0].Spec.Selector
	if first == sel {
		t.Errorf("PVC[0].Spec.Selector aliases the CR's selector pointer; deep copy expected")
	}
	for i, pvc := range sts.Spec.VolumeClaimTemplates {
		if i > 0 && pvc.Spec.Selector == first {
			t.Errorf("PVC[%d].Spec.Selector aliases PVC[0]'s selector pointer; per-PVC deep copy expected", i)
		}
	}

	sts.Spec.VolumeClaimTemplates[0].Spec.Selector.MatchLabels["tier"] = "mutated"
	if sts.Spec.VolumeClaimTemplates[1].Spec.Selector.MatchLabels["tier"] != "fast" {
		t.Errorf("mutating PVC[0] selector leaked into PVC[1]: %#v", sts.Spec.VolumeClaimTemplates[1].Spec.Selector)
	}
	if sel.MatchLabels["tier"] != "fast" {
		t.Errorf("mutating PVC[0] selector leaked back to the CR's selector: %#v", sel)
	}
}

func TestVolumeStatefulSet_NilSelectorYieldsNilPVCSelector(t *testing.T) {
	m := makeStorageSelectorSeaweed(1, nil)

	r := &SeaweedReconciler{}
	sts := r.createVolumeServerStatefulSet(m)

	if len(sts.Spec.VolumeClaimTemplates) != 1 {
		t.Fatalf("VolumeClaimTemplates len = %d, want 1", len(sts.Spec.VolumeClaimTemplates))
	}
	if sts.Spec.VolumeClaimTemplates[0].Spec.Selector != nil {
		t.Errorf("PVC.Spec.Selector = %#v, want nil", sts.Spec.VolumeClaimTemplates[0].Spec.Selector)
	}
}

func TestVolumeTopologyStatefulSet_PropagatesStorageSelector_TopologyWins(t *testing.T) {
	flatSel := &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "fast"}}
	topoSel := &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "ultra"}}

	diskCount := int32(1)
	topology := &seaweedv1.VolumeTopologySpec{
		VolumeServerConfig: seaweedv1.VolumeServerConfig{
			ResourceRequirements: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("30Gi"),
				},
			},
			StorageSelector: topoSel,
		},
		Replicas:   1,
		Rack:       "rack1",
		DataCenter: "dc1",
	}
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Master:                &seaweedv1.MasterSpec{Replicas: 1},
			VolumeServerDiskCount: &diskCount,
			Volume: &seaweedv1.VolumeSpec{
				Replicas: 1,
				VolumeServerConfig: seaweedv1.VolumeServerConfig{
					StorageSelector: flatSel,
				},
			},
			VolumeTopology: map[string]*seaweedv1.VolumeTopologySpec{
				"rack1": topology,
			},
		},
	}

	r := &SeaweedReconciler{}
	sts := r.createVolumeServerTopologyStatefulSet(m, "rack1", topology)

	if len(sts.Spec.VolumeClaimTemplates) != 1 {
		t.Fatalf("VolumeClaimTemplates len = %d, want 1", len(sts.Spec.VolumeClaimTemplates))
	}
	if !apiequality.Semantic.DeepEqual(sts.Spec.VolumeClaimTemplates[0].Spec.Selector, topoSel) {
		t.Errorf("topology PVC.Spec.Selector = %#v, want %#v", sts.Spec.VolumeClaimTemplates[0].Spec.Selector, topoSel)
	}
}

func TestVolumeTopologyStatefulSet_FallsBackToFlatStorageSelector(t *testing.T) {
	flatSel := &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "fast"}}

	diskCount := int32(1)
	topology := &seaweedv1.VolumeTopologySpec{
		VolumeServerConfig: seaweedv1.VolumeServerConfig{
			ResourceRequirements: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("30Gi"),
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
			Master:                &seaweedv1.MasterSpec{Replicas: 1},
			VolumeServerDiskCount: &diskCount,
			Volume: &seaweedv1.VolumeSpec{
				Replicas: 1,
				VolumeServerConfig: seaweedv1.VolumeServerConfig{
					StorageSelector: flatSel,
				},
			},
			VolumeTopology: map[string]*seaweedv1.VolumeTopologySpec{
				"rack1": topology,
			},
		},
	}

	r := &SeaweedReconciler{}
	sts := r.createVolumeServerTopologyStatefulSet(m, "rack1", topology)

	if !apiequality.Semantic.DeepEqual(sts.Spec.VolumeClaimTemplates[0].Spec.Selector, flatSel) {
		t.Errorf("topology PVC.Spec.Selector = %#v, want %#v", sts.Spec.VolumeClaimTemplates[0].Spec.Selector, flatSel)
	}
}

func TestGetStorageSelector_NilSafe(t *testing.T) {
	// Topology-only deployments omit spec.volume; helper must not panic.
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec:       seaweedv1.SeaweedSpec{Master: &seaweedv1.MasterSpec{Replicas: 1}},
	}
	if got := getStorageSelector(m, nil); got != nil {
		t.Errorf("getStorageSelector with nil Volume and nil topology = %#v, want nil", got)
	}

	topoSel := &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "ultra"}}
	topo := &seaweedv1.VolumeTopologySpec{
		VolumeServerConfig: seaweedv1.VolumeServerConfig{StorageSelector: topoSel},
	}
	if got := getStorageSelector(m, topo); !apiequality.Semantic.DeepEqual(got, topoSel) {
		t.Errorf("getStorageSelector topology-only = %#v, want %#v", got, topoSel)
	}
}
