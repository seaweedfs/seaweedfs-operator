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
