package controller

import (
	"context"
	"sync"
	"testing"

	"github.com/go-logr/logr"
	monitorv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// resetServiceMonitorCRDCache makes the next serviceMonitorCRDAvailable call
// probe the test's client instead of reusing another test's answer.
func resetServiceMonitorCRDCache(t *testing.T) {
	t.Helper()
	reset := func() {
		serviceMonitorCRDMu.Lock()
		defer serviceMonitorCRDMu.Unlock()
		serviceMonitorCRDOnce = sync.Once{}
		serviceMonitorCRDAvailable = false
	}
	reset()
	t.Cleanup(reset)
}

func serviceMonitorTestReconciler(t *testing.T, withCRD bool, objs ...client.Object) *SeaweedReconciler {
	t.Helper()
	resetServiceMonitorCRDCache(t)

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := seaweedv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if withCRD {
		if err := monitorv1.AddToScheme(scheme); err != nil {
			t.Fatal(err)
		}
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &SeaweedReconciler{Client: cli, Scheme: scheme, Log: logr.Discard()}
}

func serviceMonitorTestSeaweed(port *int32, enabled *bool) *seaweedv1.Seaweed {
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "test", UID: "sw-uid"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1, MetricsPort: port},
		},
	}
	if enabled != nil {
		m.Spec.ServiceMonitor = &seaweedv1.ServiceMonitorSpec{Enabled: enabled}
	}
	return m
}

func boolPtr(b bool) *bool { return &b }

func ownedServiceMonitor(t *testing.T, m *seaweedv1.Seaweed, name string, owned bool) *monitorv1.ServiceMonitor {
	t.Helper()
	sm := &monitorv1.ServiceMonitor{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: m.Namespace}}
	if owned {
		scheme := runtime.NewScheme()
		_ = seaweedv1.AddToScheme(scheme)
		if err := controllerutil.SetControllerReference(m, sm, scheme); err != nil {
			t.Fatal(err)
		}
	}
	return sm
}

func serviceMonitorExists(t *testing.T, r *SeaweedReconciler, name string) bool {
	t.Helper()
	err := r.Get(context.Background(), client.ObjectKey{Namespace: "test", Name: name}, &monitorv1.ServiceMonitor{})
	if err == nil {
		return true
	}
	if apierrors.IsNotFound(err) {
		return false
	}
	t.Fatalf("get ServiceMonitor %s: %v", name, err)
	return false
}

func TestPruneServiceMonitors(t *testing.T) {
	ctx := context.Background()
	port := metricsPort(9327)

	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "test", UID: "sw-uid"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1, MetricsPort: port},
			VolumeTopology: map[string]*seaweedv1.VolumeTopologySpec{
				"dc1": {VolumeServerConfig: seaweedv1.VolumeServerConfig{MetricsPort: port}},
			},
			Filer: &seaweedv1.FilerSpec{Replicas: 1, MetricsPort: port},
		},
	}
	objs := []client.Object{
		ownedServiceMonitor(t, m, "sw-master", true),
		ownedServiceMonitor(t, m, "sw-filer", true),
		ownedServiceMonitor(t, m, "sw-volume", true),
		ownedServiceMonitor(t, m, "sw-volume-dc1", true),
		ownedServiceMonitor(t, m, "sw-volume-dc2", true),
		ownedServiceMonitor(t, m, "sw-admin", true),
		ownedServiceMonitor(t, m, "sw-user", false),
	}

	for _, enabled := range []bool{true, false} {
		m.Spec.ServiceMonitor = &seaweedv1.ServiceMonitorSpec{Enabled: boolPtr(enabled)}
		r := serviceMonitorTestReconciler(t, true, objs...)
		if done, _, err := r.pruneServiceMonitors(ctx, m); err != nil || done {
			t.Fatalf("enabled=%v pruneServiceMonitors: done=%v err=%v", enabled, done, err)
		}

		want := map[string]bool{
			"sw-master":     enabled,
			"sw-filer":      enabled,
			"sw-volume-dc1": enabled,
			"sw-user":       true,
		}
		for _, sm := range objs {
			if got := serviceMonitorExists(t, r, sm.GetName()); got != want[sm.GetName()] {
				t.Errorf("enabled=%v %s: exists=%v, want %v", enabled, sm.GetName(), got, want[sm.GetName()])
			}
		}
	}
}

func TestReconcileServiceMonitor(t *testing.T) {
	ctx := context.Background()
	key := client.ObjectKey{Namespace: "test", Name: "sw-master"}

	tests := []struct {
		name       string
		port       *int32
		enabled    *bool
		existing   bool
		owned      bool
		wantExists bool
	}{
		{name: "created by default", port: metricsPort(9327), wantExists: true},
		{name: "created when enabled", port: metricsPort(9327), enabled: boolPtr(true), wantExists: true},
		{name: "not created when disabled", port: metricsPort(9327), enabled: boolPtr(false)},
		{name: "removed when disabled", port: metricsPort(9327), enabled: boolPtr(false), existing: true, owned: true},
		{name: "removed with the metrics port", existing: true, owned: true},
		{name: "one the Seaweed does not control is kept", enabled: boolPtr(false), existing: true, wantExists: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := serviceMonitorTestSeaweed(tt.port, tt.enabled)
			var objs []client.Object
			if tt.existing {
				objs = append(objs, ownedServiceMonitor(t, m, key.Name, tt.owned))
			}
			r := serviceMonitorTestReconciler(t, true, objs...)

			done, _, err := r.ensureMasterServiceMonitor(ctx, m)
			if err != nil || done {
				t.Fatalf("ensureMasterServiceMonitor: done=%v err=%v", done, err)
			}

			sm := &monitorv1.ServiceMonitor{}
			err = r.Get(ctx, key, sm)
			if !tt.wantExists {
				if !apierrors.IsNotFound(err) {
					t.Fatalf("expected no ServiceMonitor, got err=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected the ServiceMonitor: %v", err)
			}
			if tt.existing && !tt.owned && len(sm.OwnerReferences) != 0 {
				t.Fatalf("a ServiceMonitor the Seaweed does not control was taken over: %v", sm.OwnerReferences)
			}
			if !tt.existing && !metav1.IsControlledBy(sm, m) {
				t.Fatalf("expected the Seaweed to control the ServiceMonitor, got %v", sm.OwnerReferences)
			}
		})
	}
}

func TestReconcileServiceMonitorWithoutCRD(t *testing.T) {
	ctx := context.Background()
	for _, port := range []*int32{metricsPort(9327), nil} {
		r := serviceMonitorTestReconciler(t, false)
		if _, _, err := r.ensureMasterServiceMonitor(ctx, serviceMonitorTestSeaweed(port, nil)); err != nil {
			t.Fatalf("metricsPort=%v: a cluster without the CRD must not fail reconcile: %v", port, err)
		}
	}
}

// Every component goes through the same switch.
func TestServiceMonitorsDisabledForEveryComponent(t *testing.T) {
	ctx := context.Background()
	port := metricsPort(9327)
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "test", UID: "sw-uid"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1, MetricsPort: port},
			Volume: &seaweedv1.VolumeSpec{Replicas: 1, VolumeServerConfig: seaweedv1.VolumeServerConfig{MetricsPort: port}},
			VolumeTopology: map[string]*seaweedv1.VolumeTopologySpec{
				"dc1": {VolumeServerConfig: seaweedv1.VolumeServerConfig{MetricsPort: port}},
			},
			Filer:  &seaweedv1.FilerSpec{Replicas: 1, MetricsPort: port},
			S3:     &seaweedv1.S3GatewaySpec{Replicas: 1, MetricsPort: port},
			SFTP:   &seaweedv1.SFTPSpec{Replicas: 1, MetricsPort: port},
			Admin:  &seaweedv1.AdminSpec{MetricsPort: port},
			Worker: &seaweedv1.WorkerSpec{Replicas: 1, MetricsPort: port},
		},
	}

	for _, enabled := range []bool{true, false} {
		m.Spec.ServiceMonitor = &seaweedv1.ServiceMonitorSpec{Enabled: boolPtr(enabled)}
		r := serviceMonitorTestReconciler(t, true)

		ensures := map[string]func() (bool, error){
			"sw-master": func() (bool, error) { d, _, err := r.ensureMasterServiceMonitor(ctx, m); return d, err },
			"sw-volume": func() (bool, error) { d, _, err := r.ensureVolumeServerServiceMonitor(ctx, m); return d, err },
			"sw-volume-dc1": func() (bool, error) {
				d, _, err := r.ensureVolumeServerTopologyServiceMonitor(ctx, m, "dc1", m.Spec.VolumeTopology["dc1"])
				return d, err
			},
			"sw-filer":  func() (bool, error) { d, _, err := r.ensureFilerServiceMonitor(ctx, m); return d, err },
			"sw-s3":     func() (bool, error) { d, _, err := r.ensureS3ServiceMonitor(ctx, m); return d, err },
			"sw-sftp":   func() (bool, error) { d, _, err := r.ensureSFTPServiceMonitor(ctx, m); return d, err },
			"sw-admin":  func() (bool, error) { d, _, err := r.ensureAdminServiceMonitor(ctx, m); return d, err },
			"sw-worker": func() (bool, error) { d, _, err := r.ensureWorkerServiceMonitor(ctx, m); return d, err },
		}
		for name, ensure := range ensures {
			if done, err := ensure(); err != nil || done {
				t.Fatalf("enabled=%v %s: done=%v err=%v", enabled, name, done, err)
			}
			err := r.Get(ctx, client.ObjectKey{Namespace: "test", Name: name}, &monitorv1.ServiceMonitor{})
			if enabled && err != nil {
				t.Errorf("enabled: expected ServiceMonitor %s: %v", name, err)
			}
			if !enabled && !apierrors.IsNotFound(err) {
				t.Errorf("disabled: expected no ServiceMonitor %s, got err=%v", name, err)
			}
		}
	}
}
