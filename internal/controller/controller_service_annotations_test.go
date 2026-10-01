package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

func testService(annotations map[string]string, svcType corev1.ServiceType) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "sw-test",
			Namespace:   "ns",
			Annotations: annotations,
		},
		Spec: corev1.ServiceSpec{
			Type: svcType,
			Ports: []corev1.ServicePort{
				{Name: "http", Port: 80},
			},
		},
	}
}

// A Service annotation the operator once rendered must not outlive its
// removal from the spec — cloud LB annotations left behind after a
// LoadBalancer -> ClusterIP change keep the service treated as a load
// balancer. Keys the operator never rendered stay.
func TestCreateOrUpdateServiceDropsStaleAnnotations(t *testing.T) {
	ctx := context.Background()
	r, _ := componentIngressTestReconciler(t)
	r.Log = logf.FromContext(ctx)

	lbKey := "service.beta.kubernetes.io/aws-load-balancer-type"
	if _, err := r.CreateOrUpdateService(testService(map[string]string{lbKey: "nlb"}, corev1.ServiceTypeLoadBalancer)); err != nil {
		t.Fatalf("create: %v", err)
	}

	// An annotation stamped by another controller is not the operator's to remove.
	existing := &corev1.Service{}
	if err := r.Get(ctx, types.NamespacedName{Name: "sw-test", Namespace: "ns"}, existing); err != nil {
		t.Fatalf("get: %v", err)
	}
	existing.Annotations["foreign.example.com/managed"] = "keep"
	if err := r.Update(ctx, existing); err != nil {
		t.Fatalf("stamp foreign annotation: %v", err)
	}

	got, err := r.CreateOrUpdateService(testService(nil, corev1.ServiceTypeClusterIP))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if v, ok := got.Annotations[lbKey]; ok {
		t.Fatalf("stale LB annotation survived: %q", v)
	}
	if got.Annotations["foreign.example.com/managed"] != "keep" {
		t.Fatal("foreign annotation was removed")
	}
	if got.Spec.Type != corev1.ServiceTypeClusterIP {
		t.Fatalf("type = %q, want ClusterIP", got.Spec.Type)
	}
}

// A Service that predates the bookkeeping annotation carries no rendered-key
// record; the merge must not guess and delete annotations it cannot prove it
// wrote.
func TestCreateOrUpdateServiceKeepsUnrecordedAnnotations(t *testing.T) {
	ctx := context.Background()
	r, _ := componentIngressTestReconciler(t)
	r.Log = logf.FromContext(ctx)

	if err := r.Create(ctx, testService(map[string]string{"pre-existing.example.com/key": "v"}, corev1.ServiceTypeClusterIP)); err != nil {
		t.Fatalf("seed service: %v", err)
	}
	got, err := r.CreateOrUpdateService(testService(nil, corev1.ServiceTypeClusterIP))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.Annotations["pre-existing.example.com/key"] != "v" {
		t.Fatal("unrecorded annotation was pruned")
	}
}
