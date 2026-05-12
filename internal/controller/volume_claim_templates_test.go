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
)

// TestVCTSemanticallyEqual_HandlesApiserverDefaultedVolumeMode pins
// the regression behavior for the comparator: the operator builds a
// desired PVC template with VolumeMode unset (nil); the apiserver
// fills it with &Filesystem on Create. apiequality.Semantic.DeepEqual
// on the two returns false, which makes reconcileVolumeClaimTemplates
// think the VCT differs on every reconcile, log a warning, and emit
// a Warning event — every 5 seconds, forever.
//
// The test constructs the two states by hand so it's fast and doesn't
// need envtest. The end-to-end version (envtest_volume_claim_templates_test.go)
// proves the same property against a real apiserver round-trip.
func TestVCTSemanticallyEqual_HandlesApiserverDefaultedVolumeMode(t *testing.T) {
	desired := []corev1.PersistentVolumeClaim{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "data"},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("100Gi"),
					},
				},
			},
		},
	}

	// Mimic what the apiserver returns after Create: same fields plus
	// VolumeMode defaulted to Filesystem, and a generated UID/CreationTimestamp
	// that Semantic.DeepEqual would also have caught. Here we only inject
	// the spec-level default, since metadata is excluded from the controller's
	// comparison anyway.
	filesystem := corev1.PersistentVolumeFilesystem
	existing := []corev1.PersistentVolumeClaim{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "data"},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("100Gi"),
					},
				},
				VolumeMode: &filesystem, // <-- the apiserver default
			},
		},
	}

	if !vctSemanticallyEqual(existing, desired) {
		t.Errorf("expected vctSemanticallyEqual to treat nil VolumeMode and &Filesystem as equivalent — apiserver defaults the field on Create, so the in-memory desired and the round-tripped existing must compare equal")
	}
}

func TestVCTSemanticallyEqual_DetectsRealStorageDiff(t *testing.T) {
	mk := func(size string) []corev1.PersistentVolumeClaim {
		return []corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{Name: "data"},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse(size),
					},
				},
			},
		}}
	}
	if vctSemanticallyEqual(mk("100Gi"), mk("200Gi")) {
		t.Errorf("expected size diff to be detected — comparator must not be too lenient")
	}
}

func TestVCTSemanticallyEqual_DetectsRealStorageClassDiff(t *testing.T) {
	mk := func(class string) []corev1.PersistentVolumeClaim {
		var sc *string
		if class != "" {
			sc = &class
		}
		return []corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{Name: "data"},
			Spec:       corev1.PersistentVolumeClaimSpec{StorageClassName: sc},
		}}
	}
	if vctSemanticallyEqual(mk("fast"), mk("slow")) {
		t.Errorf("expected StorageClassName diff to be detected")
	}
	if !vctSemanticallyEqual(mk(""), mk("")) {
		t.Errorf("expected matching nil StorageClassName to be equal")
	}
	if vctSemanticallyEqual(mk(""), mk("fast")) {
		t.Errorf("expected nil vs set StorageClassName to be detected")
	}
}

func TestVCTSemanticallyEqual_DetectsLengthDiff(t *testing.T) {
	one := []corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{Name: "a"}}}
	two := []corev1.PersistentVolumeClaim{
		{ObjectMeta: metav1.ObjectMeta{Name: "a"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "b"}},
	}
	if vctSemanticallyEqual(one, two) {
		t.Errorf("expected length diff to be detected")
	}
}

func TestVCTDifferences_ReturnsEmptyForEqualTemplates(t *testing.T) {
	mk := func() []corev1.PersistentVolumeClaim {
		return []corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{Name: "data"},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("100Gi"),
					},
				},
			},
		}}
	}
	if diffs := vctDifferences(mk(), mk()); len(diffs) != 0 {
		t.Errorf("expected no differences for equal templates, got %v", diffs)
	}
}

func TestVCTDifferences_NamesTheDriftedField(t *testing.T) {
	mk := func(size, class string) []corev1.PersistentVolumeClaim {
		var sc *string
		if class != "" {
			sc = &class
		}
		return []corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{Name: "mount0"},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				StorageClassName: sc,
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse(size),
					},
				},
			},
		}}
	}

	diffs := vctDifferences(mk("100Gi", "fast"), mk("200Gi", "slow"))
	if len(diffs) == 0 {
		t.Fatalf("expected differences to be reported")
	}
	joined := strings.Join(diffs, " ")
	if !strings.Contains(joined, "resources.requests") {
		t.Errorf("expected resources.requests to be named in diff, got %v", diffs)
	}
	if !strings.Contains(joined, "storageClassName") {
		t.Errorf("expected storageClassName to be named in diff, got %v", diffs)
	}
	if !strings.Contains(joined, "mount0") {
		t.Errorf("expected template name to appear in diff prefix, got %v", diffs)
	}
}

func TestVCTDifferences_ReportsLengthMismatchOnly(t *testing.T) {
	one := []corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{Name: "a"}}}
	two := []corev1.PersistentVolumeClaim{
		{ObjectMeta: metav1.ObjectMeta{Name: "a"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "b"}},
	}
	diffs := vctDifferences(one, two)
	if len(diffs) != 1 {
		t.Fatalf("expected exactly one diff for length mismatch, got %v", diffs)
	}
	if !strings.Contains(diffs[0], "length") {
		t.Errorf("expected length diff, got %q", diffs[0])
	}
}

// TestResourceListString_DeterministicAcrossMapIteration pins that the
// diff message sorts resource names. Without this, repeated reconciles
// could format the same ResourceList differently — the Warning event
// body would flap between runs and confuse anyone diffing event logs.
func TestResourceListString_DeterministicAcrossMapIteration(t *testing.T) {
	rl := corev1.ResourceList{
		corev1.ResourceStorage:          resource.MustParse("100Gi"),
		corev1.ResourceEphemeralStorage: resource.MustParse("1Gi"),
		corev1.ResourceMemory:           resource.MustParse("4Gi"),
		corev1.ResourceCPU:              resource.MustParse("2"),
	}
	first := resourceListString(rl)
	for i := 0; i < 50; i++ {
		if got := resourceListString(rl); got != first {
			t.Fatalf("resourceListString output flapped between calls: %q vs %q", first, got)
		}
	}
	// And confirm the order is the expected sorted one, not just stable-by-luck.
	want := "{cpu=2,ephemeral-storage=1Gi,memory=4Gi,storage=100Gi}"
	if first != want {
		t.Errorf("resourceListString = %q, want %q", first, want)
	}
}

func TestVCTSemanticallyEqual_DetectsExplicitlyDifferentVolumeMode(t *testing.T) {
	filesystem := corev1.PersistentVolumeFilesystem
	block := corev1.PersistentVolumeBlock
	a := []corev1.PersistentVolumeClaim{{
		ObjectMeta: metav1.ObjectMeta{Name: "data"},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeMode: &filesystem},
	}}
	b := []corev1.PersistentVolumeClaim{{
		ObjectMeta: metav1.ObjectMeta{Name: "data"},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeMode: &block},
	}}
	if vctSemanticallyEqual(a, b) {
		t.Errorf("expected Filesystem vs Block VolumeMode to be detected — only the nil-vs-Filesystem case should be smoothed")
	}
}
