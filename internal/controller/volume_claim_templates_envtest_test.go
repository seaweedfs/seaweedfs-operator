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
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// TestReconcileVolumeClaimTemplates_RoundTripWithApiserverDefaults is the
// end-to-end version of the regression test for issue #224. It builds a
// volume-server StatefulSet exactly the way the controller does in
// production, hands it to a real apiserver, reads it back (now carrying
// the apiserver's defaulted PVC fields), and asserts that
// reconcileVolumeClaimTemplates does NOT report drift.
//
// On master with apiequality.Semantic.DeepEqual, this test fails: the
// apiserver writes &Filesystem into Spec.VolumeMode and the in-memory
// desired still has nil, so DeepEqual returns false. With the
// vctSemanticallyEqual comparator introduced in this PR, it passes.
//
// The unit test in volume_claim_templates_test.go covers the comparator
// in isolation; this test catches the case where the operator and
// apiserver disagree about a default that the unit test author didn't
// foresee — any future apiserver-side default that lands on a PVC field
// the comparator doesn't already handle would surface here.
func TestReconcileVolumeClaimTemplates_RoundTripWithApiserverDefaults(t *testing.T) {
	_, cli := mustEnvtest(t)
	ctx := context.Background()

	ns := newTestNamespace(t, ctx, cli, "vct-roundtrip")
	t.Cleanup(func() {
		_ = cli.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	})

	cr := minimalVolumeSeaweedCR(ns)

	r := &SeaweedReconciler{
		Client: cli,
		Log:    logf.FromContext(ctx),
		Scheme: cli.Scheme(),
	}

	// Build the desired SS the exact way the controller does, then
	// realize it on the apiserver. The apiserver will fill in defaults
	// on the PVC templates (notably Spec.VolumeMode).
	desired := r.createVolumeServerStatefulSet(cr)
	desired.Namespace = ns
	if err := cli.Create(ctx, desired); err != nil {
		t.Fatalf("create StatefulSet: %v", err)
	}

	// Read it back — this is the version reconcileVolumeClaimTemplates
	// would compare against on a follow-up reconcile.
	var existing appsv1.StatefulSet
	key := types.NamespacedName{Namespace: ns, Name: desired.Name}
	if err := cli.Get(ctx, key, &existing); err != nil {
		t.Fatalf("get StatefulSet: %v", err)
	}

	// Sanity: apiserver actually injected a non-nil VolumeMode. Without
	// that the test isn't proving anything beyond the unit test.
	if len(existing.Spec.VolumeClaimTemplates) == 0 {
		t.Fatalf("expected at least one VCT, got 0")
	}
	if existing.Spec.VolumeClaimTemplates[0].Spec.VolumeMode == nil {
		t.Fatalf("expected apiserver to default Spec.VolumeMode after Create; got nil — apiserver behavior may have changed")
	}

	// The actual fix property: rebuild the desired (operator's
	// in-memory view) and ask whether reconcile would think it
	// differs from what's on the apiserver.
	freshDesired := r.createVolumeServerStatefulSet(cr)
	if !vctSemanticallyEqual(existing.Spec.VolumeClaimTemplates, freshDesired.Spec.VolumeClaimTemplates) {
		t.Errorf("vctSemanticallyEqual reports drift after apiserver round-trip; reconciler would log a Warning event every reconcile")
		t.Errorf("  existing VCTs:  %+v", existing.Spec.VolumeClaimTemplates)
		t.Errorf("  desired  VCTs:  %+v", freshDesired.Spec.VolumeClaimTemplates)
	}
}

// minimalVolumeSeaweedCR builds the smallest Seaweed CR that still
// drives createVolumeServerStatefulSet through the persistence path
// (volumeCount > 0, with a real storage request). Anything else the
// helper reads off the spec uses defaults via component_accessor.go.
func minimalVolumeSeaweedCR(namespace string) *seaweedv1.Seaweed {
	one := int32(1)
	return &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vct",
			Namespace: namespace,
		},
		Spec: seaweedv1.SeaweedSpec{
			Image:                 "chrislusf/seaweedfs:latest",
			VolumeServerDiskCount: &one,
			Master:                &seaweedv1.MasterSpec{Replicas: 1},
			Volume: &seaweedv1.VolumeSpec{
				Replicas: 1,
				VolumeServerConfig: seaweedv1.VolumeServerConfig{
					ResourceRequirements: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
					},
				},
			},
		},
	}
}

// newTestNamespace creates a uniquely-named Namespace so concurrent
// envtest tests don't step on each other's StatefulSets/PVCs. Returns
// the namespace name. Caller is responsible for cleanup via t.Cleanup.
func newTestNamespace(t *testing.T, ctx context.Context, cli client.Client, prefix string) string {
	t.Helper()
	name := fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	if err := cli.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
		t.Fatalf("create namespace %s: %v", name, err)
	}
	return name
}
