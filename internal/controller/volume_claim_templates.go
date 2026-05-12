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
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
)

// vctSemanticallyEqual returns true when two VolumeClaimTemplates slices
// describe the same intent over the fields the operator actually owns,
// tolerating apiserver-side defaulting that round-trips a Created
// StatefulSet's VCT entries differently from what the operator
// constructed in memory.
//
// A naive apiequality.Semantic.DeepEqual on the whole slice returns
// false on every reconcile because the apiserver defaults
// Spec.VolumeMode to Filesystem when the operator leaves it nil. The
// reconciler then logs "VolumeClaimTemplates differ but cannot be
// auto-applied" every 5 seconds (the requeue cadence), spamming the
// events stream and confusing operators into thinking their CR isn't
// taking effect.
//
// Comparison surface, per field the operator can set in any of the
// component statefulset builders:
//
//   - Name              required, exact match
//   - AccessModes       Semantic.DeepEqual
//   - Resources.Requests Semantic.DeepEqual (resource.Quantity comparison)
//   - StorageClassName  pointer-aware: nil == nil, both set must match
//   - Selector          Semantic.DeepEqual on *metav1.LabelSelector
//   - VolumeName        exact string match
//   - VolumeMode        nil-equivalent-to-Filesystem; Filesystem vs
//     Block etc. is still a real diff
//   - DataSource        Semantic.DeepEqual
//
// Fields the operator never sets and the apiserver may populate
// (DataSourceRef pre-CSI-snapshots-graduation, status, etc.) are
// intentionally not compared. If a future operator change starts
// setting one of those, add it here and to the unit tests.
func vctSemanticallyEqual(a, b []corev1.PersistentVolumeClaim) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !pvcSemanticallyEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

func pvcSemanticallyEqual(a, b corev1.PersistentVolumeClaim) bool {
	if a.Name != b.Name {
		return false
	}
	if !apiequality.Semantic.DeepEqual(a.Spec.AccessModes, b.Spec.AccessModes) {
		return false
	}
	if !apiequality.Semantic.DeepEqual(a.Spec.Resources.Requests, b.Spec.Resources.Requests) {
		return false
	}
	if !apiequality.Semantic.DeepEqual(a.Spec.StorageClassName, b.Spec.StorageClassName) {
		return false
	}
	if !apiequality.Semantic.DeepEqual(a.Spec.Selector, b.Spec.Selector) {
		return false
	}
	if a.Spec.VolumeName != b.Spec.VolumeName {
		return false
	}
	if !volumeModeSemanticallyEqual(a.Spec.VolumeMode, b.Spec.VolumeMode) {
		return false
	}
	if !apiequality.Semantic.DeepEqual(a.Spec.DataSource, b.Spec.DataSource) {
		return false
	}
	return true
}

// vctDifferences returns a human-readable description of the first
// few field-level differences between two VolumeClaimTemplates slices,
// using the same comparison surface as vctSemanticallyEqual.
//
// It exists so that the "VolumeClaimTemplates differ" Warning event
// can name what actually drifted, rather than forcing operators to
// guess. If nothing drifted, the slice is empty (and vctSemanticallyEqual
// will have already returned true). Output is capped to avoid producing
// multi-kilobyte event messages when many fields differ at once.
func vctDifferences(a, b []corev1.PersistentVolumeClaim) []string {
	const maxDiffs = 5
	var diffs []string

	if len(a) != len(b) {
		diffs = append(diffs, fmt.Sprintf("length: existing=%d desired=%d", len(a), len(b)))
		return diffs
	}

	for i := range a {
		diffs = append(diffs, pvcDifferences(a[i], b[i], i)...)
		if len(diffs) >= maxDiffs {
			diffs = diffs[:maxDiffs]
			diffs = append(diffs, "...")
			break
		}
	}
	return diffs
}

func pvcDifferences(a, b corev1.PersistentVolumeClaim, index int) []string {
	var diffs []string
	prefix := fmt.Sprintf("template[%d]", index)
	if a.Name != "" {
		prefix = fmt.Sprintf("template[%d:%s]", index, a.Name)
	}

	if a.Name != b.Name {
		diffs = append(diffs, fmt.Sprintf("%s.name: %q vs %q", prefix, a.Name, b.Name))
	}
	if !apiequality.Semantic.DeepEqual(a.Spec.AccessModes, b.Spec.AccessModes) {
		diffs = append(diffs, fmt.Sprintf("%s.accessModes: %v vs %v", prefix, a.Spec.AccessModes, b.Spec.AccessModes))
	}
	if !apiequality.Semantic.DeepEqual(a.Spec.Resources.Requests, b.Spec.Resources.Requests) {
		diffs = append(diffs, fmt.Sprintf("%s.resources.requests: %s vs %s", prefix, resourceListString(a.Spec.Resources.Requests), resourceListString(b.Spec.Resources.Requests)))
	}
	if !apiequality.Semantic.DeepEqual(a.Spec.StorageClassName, b.Spec.StorageClassName) {
		diffs = append(diffs, fmt.Sprintf("%s.storageClassName: %s vs %s", prefix, stringPtr(a.Spec.StorageClassName), stringPtr(b.Spec.StorageClassName)))
	}
	if !apiequality.Semantic.DeepEqual(a.Spec.Selector, b.Spec.Selector) {
		diffs = append(diffs, fmt.Sprintf("%s.selector differs", prefix))
	}
	if a.Spec.VolumeName != b.Spec.VolumeName {
		diffs = append(diffs, fmt.Sprintf("%s.volumeName: %q vs %q", prefix, a.Spec.VolumeName, b.Spec.VolumeName))
	}
	if !volumeModeSemanticallyEqual(a.Spec.VolumeMode, b.Spec.VolumeMode) {
		diffs = append(diffs, fmt.Sprintf("%s.volumeMode: %s vs %s", prefix, volumeModeString(a.Spec.VolumeMode), volumeModeString(b.Spec.VolumeMode)))
	}
	if !apiequality.Semantic.DeepEqual(a.Spec.DataSource, b.Spec.DataSource) {
		diffs = append(diffs, fmt.Sprintf("%s.dataSource differs", prefix))
	}
	return diffs
}

func resourceListString(rl corev1.ResourceList) string {
	if len(rl) == 0 {
		return "{}"
	}
	parts := make([]string, 0, len(rl))
	for name, q := range rl {
		parts = append(parts, fmt.Sprintf("%s=%s", name, q.String()))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func stringPtr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%q", *p)
}

func volumeModeString(p *corev1.PersistentVolumeMode) string {
	if p == nil {
		return "<nil>"
	}
	return string(*p)
}

// volumeModeSemanticallyEqual treats nil VolumeMode as Filesystem,
// matching apiserver behavior on Create. With this normalization, the
// "operator left it nil → apiserver stored &Filesystem → re-read
// returns &Filesystem" flow no longer trips a false-positive diff.
// Real differences (Filesystem vs Block, set vs explicit Block, etc.)
// still propagate.
func volumeModeSemanticallyEqual(a, b *corev1.PersistentVolumeMode) bool {
	aMode := corev1.PersistentVolumeFilesystem
	bMode := corev1.PersistentVolumeFilesystem
	if a != nil {
		aMode = *a
	}
	if b != nil {
		bMode = *b
	}
	return aMode == bMode
}
