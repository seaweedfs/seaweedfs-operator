package controller

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func componentIngressTestReconciler(t *testing.T, objs ...runtime.Object) (*SeaweedReconciler, *runtime.Scheme) {
	t.Helper()
	scheme := runtime.NewScheme()
	// clientgoscheme registers networking.k8s.io/v1 (Ingress) too.
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("clientgoscheme: %v", err)
	}
	if err := seaweedv1.AddToScheme(scheme); err != nil {
		t.Fatalf("seaweedv1: %v", err)
	}
	clientObjs := make([]runtime.Object, 0, len(objs))
	clientObjs = append(clientObjs, objs...)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(clientObjs...).Build()
	return &SeaweedReconciler{Client: cli, Scheme: scheme, Log: logr.Discard()}, scheme
}

// The filer serves gRPC on FilerHTTPPort+10000 and an Ingress backend can
// only carry one protocol, so the gRPC endpoint gets its own Ingress on its
// own host. Verify the opt-in renders an Ingress pointed at the filer
// Service on the gRPC port, carrying the user's annotations.
func TestEnsureComponentIngressesFilerGRPC(t *testing.T) {
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns", UID: "test-uid"},
		Spec: seaweedv1.SeaweedSpec{
			Filer: &seaweedv1.FilerSpec{
				Replicas: 1,
				GRPCIngress: &seaweedv1.IngressSpec{
					Enabled: true,
					Host:    "filer-grpc.example.com",
					Annotations: map[string]string{
						"nginx.ingress.kubernetes.io/backend-protocol": "GRPC",
					},
				},
			},
		},
	}
	r, _ := componentIngressTestReconciler(t, m)

	if _, _, err := r.ensureComponentIngresses(context.Background(), m); err != nil {
		t.Fatalf("ensureComponentIngresses: %v", err)
	}

	ing := &networkingv1.Ingress{}
	if err := r.Get(context.Background(), types.NamespacedName{
		Name:      "sw-filer-grpc-ingress",
		Namespace: "ns",
	}, ing); err != nil {
		t.Fatalf("get filer-grpc ingress: %v", err)
	}

	if len(ing.Spec.Rules) != 1 {
		t.Fatalf("rules = %d, want 1", len(ing.Spec.Rules))
	}
	rule := ing.Spec.Rules[0]
	if rule.Host != "filer-grpc.example.com" {
		t.Errorf("host = %q, want filer-grpc.example.com", rule.Host)
	}
	backend := rule.HTTP.Paths[0].Backend.Service
	if backend.Name != "sw-filer" {
		t.Errorf("service = %q, want sw-filer", backend.Name)
	}
	if backend.Port.Number != seaweedv1.FilerGRPCPort {
		t.Errorf("port = %d, want %d (FilerGRPCPort)", backend.Port.Number, seaweedv1.FilerGRPCPort)
	}
	if got := ing.Annotations["nginx.ingress.kubernetes.io/backend-protocol"]; got != "GRPC" {
		t.Errorf("backend-protocol annotation = %q, want GRPC", got)
	}
}

// The gRPC Ingress is opt-in and independent of the HTTP filer Ingress: with
// only GRPCIngress disabled (or absent), no gRPC Ingress is rendered, and the
// prune loop reaps a previously-created one when it is turned off.
func TestEnsureComponentIngressesFilerGRPCDisabledAndPruned(t *testing.T) {
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns", UID: "test-uid"},
		Spec: seaweedv1.SeaweedSpec{
			Filer: &seaweedv1.FilerSpec{
				Replicas: 1,
				GRPCIngress: &seaweedv1.IngressSpec{
					Enabled: true,
					Host:    "filer-grpc.example.com",
				},
			},
		},
	}
	r, _ := componentIngressTestReconciler(t, m)

	// First pass creates it.
	if _, _, err := r.ensureComponentIngresses(context.Background(), m); err != nil {
		t.Fatalf("ensureComponentIngresses (create): %v", err)
	}
	ing := &networkingv1.Ingress{}
	if err := r.Get(context.Background(), types.NamespacedName{
		Name: "sw-filer-grpc-ingress", Namespace: "ns",
	}, ing); err != nil {
		t.Fatalf("gRPC ingress should exist after create: %v", err)
	}

	// Opt out: the prune loop should reap the managed Ingress.
	m.Spec.Filer.GRPCIngress.Enabled = false
	if _, _, err := r.ensureComponentIngresses(context.Background(), m); err != nil {
		t.Fatalf("ensureComponentIngresses (prune): %v", err)
	}
	err := r.Get(context.Background(), types.NamespacedName{
		Name: "sw-filer-grpc-ingress", Namespace: "ns",
	}, ing)
	if !isNotFoundErr(err) {
		t.Fatalf("gRPC ingress should be pruned after opt-out, get err = %v", err)
	}
}
