package v1

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func adminScriptValidator(t *testing.T, saAnnotations map[string]string) *AdminScriptCustomValidator {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{AddToScheme, corev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatalf("add scheme: %v", err)
		}
	}
	cluster := &Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"},
		Spec:       SeaweedSpec{Image: "chrislusf/seaweedfs:4.01"},
	}
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "runner", Namespace: "ns", Annotations: saAnnotations}}
	return &AdminScriptCustomValidator{Reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster, sa).Build()}
}

func identityScript(image string) *AdminScript {
	return &AdminScript{
		ObjectMeta: metav1.ObjectMeta{Name: "script", Namespace: "ns"},
		Spec: AdminScriptSpec{
			ClusterRef:         AdminScriptClusterRef{Name: "cluster"},
			Image:              ptr.To(image),
			ServiceAccountName: "runner",
		},
	}
}

func TestAdminScriptValidateUpdateUnchangedIdentity(t *testing.T) {
	ctx := context.Background()
	v := adminScriptValidator(t, nil)

	oldScript := identityScript("ghcr.io/other/seaweedfs:4.01")
	terminating := oldScript.DeepCopy()
	terminating.DeletionTimestamp = ptr.To(metav1.Now())
	terminating.Finalizers = nil
	if _, err := v.ValidateUpdate(ctx, oldScript, terminating); err != nil {
		t.Fatalf("finalizer removal with unchanged identity rejected: %v", err)
	}

	changedSA := oldScript.DeepCopy()
	changedSA.Spec.ServiceAccountName = "other"
	if _, err := v.ValidateUpdate(ctx, oldScript, changedSA); err == nil {
		t.Fatal("update to a missing ServiceAccount was allowed")
	}

	changedImage := oldScript.DeepCopy()
	changedImage.Spec.Image = ptr.To("ghcr.io/other/seaweedfs:4.02")
	if _, err := v.ValidateUpdate(ctx, oldScript, changedImage); err == nil {
		t.Fatal("update to a foreign image was allowed")
	}
}

func TestAdminScriptValidateCreate(t *testing.T) {
	ctx := context.Background()
	approved := map[string]string{ServiceAccountAllowedAnnotation: "true"}

	if _, err := adminScriptValidator(t, approved).ValidateCreate(ctx, identityScript("docker.io/chrislusf/seaweedfs:4.02")); err != nil {
		t.Fatalf("retagged cluster image with an approved ServiceAccount rejected: %v", err)
	}
	if _, err := adminScriptValidator(t, nil).ValidateCreate(ctx, identityScript("chrislusf/seaweedfs:4.02")); err == nil {
		t.Fatal("unannotated ServiceAccount was allowed")
	}
	if _, err := adminScriptValidator(t, approved).ValidateCreate(ctx, identityScript("ghcr.io/other/seaweedfs:4.01")); err == nil {
		t.Fatal("foreign image repository was allowed")
	}
}
