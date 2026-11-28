package controller

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func TestComponentServiceAccountsSkipsRetiredFlatVolume(t *testing.T) {
	cr := &seaweedv1.Seaweed{Spec: seaweedv1.SeaweedSpec{
		Volume: &seaweedv1.VolumeSpec{VolumeServerConfig: seaweedv1.VolumeServerConfig{
			ComponentSpec: seaweedv1.ComponentSpec{ServiceAccountName: ptr.To("flat")},
		}},
		VolumeTopology: map[string]*seaweedv1.VolumeTopologySpec{
			"rack1": {Replicas: 1, VolumeServerConfig: seaweedv1.VolumeServerConfig{
				ComponentSpec: seaweedv1.ComponentSpec{ServiceAccountName: ptr.To("rack")},
			}},
		},
	}}
	want := []componentServiceAccount{{component: "volume/rack1", name: "rack"}}
	if got := componentServiceAccounts(cr); !reflect.DeepEqual(got, want) {
		t.Fatalf("retired flat volume: got %v, want %v", got, want)
	}

	cr.Spec.Volume.Replicas = 1
	want = []componentServiceAccount{{component: "volume", name: "flat"}, {component: "volume/rack1", name: "rack"}}
	if got := componentServiceAccounts(cr); !reflect.DeepEqual(got, want) {
		t.Fatalf("flat volume next to groups: got %v, want %v", got, want)
	}
}

func TestEnsureComponentServiceAccounts(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{seaweedv1.AddToScheme, corev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatalf("add scheme: %v", err)
		}
	}
	for _, tc := range []struct {
		name        string
		annotations map[string]string
		denied      bool
	}{
		{"unannotated", nil, true},
		{"annotated", map[string]string{seaweedv1.ServiceAccountAllowedAnnotation: "true"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cr := &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
				Spec: seaweedv1.SeaweedSpec{Master: &seaweedv1.MasterSpec{
					Replicas:      1,
					ComponentSpec: seaweedv1.ComponentSpec{ServiceAccountName: ptr.To("master-sa")},
				}},
			}
			sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "master-sa", Namespace: "ns", Annotations: tc.annotations}}
			r := &SeaweedReconciler{
				Client:   fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&seaweedv1.Seaweed{}).WithObjects(cr, sa).Build(),
				Recorder: record.NewFakeRecorder(4),
			}
			done, _, err := r.ensureComponentServiceAccounts(ctx, cr)
			if err != nil {
				t.Fatalf("ensureComponentServiceAccounts: %v", err)
			}
			if done != tc.denied {
				t.Fatalf("done = %v, want %v", done, tc.denied)
			}
			if got := meta.IsStatusConditionTrue(cr.Status.Conditions, componentServiceAccountDeniedCondition); got != tc.denied {
				t.Fatalf("ServiceAccountDenied condition = %v, want %v", got, tc.denied)
			}
		})
	}
}
