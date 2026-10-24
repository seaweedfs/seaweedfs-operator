package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func TestComponentEndpoint(t *testing.T) {
	filer := &seaweedv1.FilerSpec{Replicas: 1}
	tlsSecret := &corev1.LocalObjectReference{Name: "s3-tls"}

	cases := []struct {
		name      string
		spec      seaweedv1.SeaweedSpec
		component string
		wantSvc   string
		wantURL   string
	}{
		{"master", seaweedv1.SeaweedSpec{Master: &seaweedv1.MasterSpec{Replicas: 1}},
			ComponentMaster, "sw-master", "http://sw-master.ns.svc:9333"},
		{"master unset", seaweedv1.SeaweedSpec{},
			ComponentMaster, "", ""},
		{"filer", seaweedv1.SeaweedSpec{Filer: filer},
			ComponentFiler, "sw-filer", "http://sw-filer.ns.svc:8888"},
		{"admin", seaweedv1.SeaweedSpec{Admin: &seaweedv1.AdminSpec{}},
			ComponentAdmin, "sw-admin", "http://sw-admin.ns.svc:23646"},
		{"volume reports none", seaweedv1.SeaweedSpec{Volume: &seaweedv1.VolumeSpec{Replicas: 1}},
			ComponentVolume, "", ""},
		{"worker reports none", seaweedv1.SeaweedSpec{Worker: &seaweedv1.WorkerSpec{Replicas: 1}},
			ComponentWorker, "", ""},
		{"s3", seaweedv1.SeaweedSpec{Filer: filer, S3: &seaweedv1.S3GatewaySpec{Replicas: 1}},
			s3Component, "sw-s3", "http://sw-s3.ns.svc:8333"},
		{"s3 custom port", seaweedv1.SeaweedSpec{Filer: filer, S3: &seaweedv1.S3GatewaySpec{Replicas: 1, Port: ptr.To(int32(9000))}},
			s3Component, "sw-s3", "http://sw-s3.ns.svc:9000"},
		{"s3 https", seaweedv1.SeaweedSpec{Filer: filer, S3: &seaweedv1.S3GatewaySpec{Replicas: 1, HTTPSPort: ptr.To(int32(8334)), TLSSecret: tlsSecret}},
			s3Component, "sw-s3", "https://sw-s3.ns.svc:8334"},
		{"s3 httpsOnly", seaweedv1.SeaweedSpec{Filer: filer, S3: &seaweedv1.S3GatewaySpec{Replicas: 1, HTTPSPort: ptr.To(int32(8334)), HTTPSOnly: true, TLSSecret: tlsSecret}},
			s3Component, "sw-s3", "https://sw-s3.ns.svc:8334"},
		{"s3 without filer tears down", seaweedv1.SeaweedSpec{S3: &seaweedv1.S3GatewaySpec{Replicas: 1}},
			s3Component, "", ""},
		{"s3 unset", seaweedv1.SeaweedSpec{Filer: filer},
			s3Component, "", ""},
		{"sftp", seaweedv1.SeaweedSpec{Filer: filer, SFTP: &seaweedv1.SFTPSpec{Replicas: 1}},
			sftpComponent, "sw-sftp", "sftp://sw-sftp.ns.svc:2222"},
		{"sftp custom port", seaweedv1.SeaweedSpec{Filer: filer, SFTP: &seaweedv1.SFTPSpec{Replicas: 1, Port: ptr.To(int32(2022))}},
			sftpComponent, "sw-sftp", "sftp://sw-sftp.ns.svc:2022"},
		{"sftp without filer tears down", seaweedv1.SeaweedSpec{SFTP: &seaweedv1.SFTPSpec{Replicas: 1}},
			sftpComponent, "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
				Spec:       tc.spec,
			}
			svc, endpoint := componentEndpoint(m, tc.component)
			if svc != tc.wantSvc || endpoint != tc.wantURL {
				t.Errorf("componentEndpoint = (%q, %q), want (%q, %q)", svc, endpoint, tc.wantSvc, tc.wantURL)
			}
		})
	}
}

// TestStatusEndpoints drives a full reconcile and checks that the CR's
// status publishes the Service name and client URL for every component
// that fronts one.
func TestStatusEndpoints(t *testing.T) {
	_, cli := mustEnvtest(t)
	ctx := context.Background()

	ns := newTestNamespace(t, ctx, cli, "endpoints")
	t.Cleanup(func() {
		_ = cli.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	})

	concurrentStart := true
	diskCount := int32(1)
	cr := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "seaweed", Namespace: ns},
		Spec: seaweedv1.SeaweedSpec{
			Image:                 "chrislusf/seaweedfs:latest",
			VolumeServerDiskCount: &diskCount,
			Master:                &seaweedv1.MasterSpec{Replicas: 1, ConcurrentStart: &concurrentStart},
			Volume: &seaweedv1.VolumeSpec{
				Replicas: 1,
				VolumeServerConfig: seaweedv1.VolumeServerConfig{
					ResourceRequirements: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
					},
				},
			},
			Filer: &seaweedv1.FilerSpec{Replicas: 1},
			S3:    &seaweedv1.S3GatewaySpec{Replicas: 1},
			SFTP:  &seaweedv1.SFTPSpec{Replicas: 1},
		},
	}
	if err := cli.Create(ctx, cr); err != nil {
		t.Fatalf("create Seaweed CR: %v", err)
	}

	r := &SeaweedReconciler{Client: cli, Log: logf.FromContext(ctx), Scheme: cli.Scheme()}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: ns}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := &seaweedv1.Seaweed{}
	if err := cli.Get(ctx, req.NamespacedName, got); err != nil {
		t.Fatalf("get Seaweed: %v", err)
	}

	want := map[string]seaweedv1.ComponentStatus{
		"master": {Service: "seaweed-master", Endpoint: "http://seaweed-master." + ns + ".svc:9333"},
		"filer":  {Service: "seaweed-filer", Endpoint: "http://seaweed-filer." + ns + ".svc:8888"},
		"s3":     {Service: "seaweed-s3", Endpoint: "http://seaweed-s3." + ns + ".svc:8333"},
		"sftp":   {Service: "seaweed-sftp", Endpoint: "sftp://seaweed-sftp." + ns + ".svc:2222"},
	}
	got_ := map[string]seaweedv1.ComponentStatus{
		"master": got.Status.Master,
		"filer":  got.Status.Filer,
		"s3":     got.Status.S3,
		"sftp":   got.Status.SFTP,
	}
	for name, w := range want {
		g := got_[name]
		if g.Service != w.Service || g.Endpoint != w.Endpoint {
			t.Errorf("status.%s = (service %q, endpoint %q), want (%q, %q)", name, g.Service, g.Endpoint, w.Service, w.Endpoint)
		}
	}
	if got.Status.Volume.Endpoint != "" || got.Status.Worker.Endpoint != "" {
		t.Errorf("volume/worker endpoints should stay empty, got %q / %q",
			got.Status.Volume.Endpoint, got.Status.Worker.Endpoint)
	}
}
