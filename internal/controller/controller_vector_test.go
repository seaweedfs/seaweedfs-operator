package controller

import (
	"context"
	"strings"
	"testing"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func vectorCluster() *seaweedv1.Seaweed {
	return &seaweedv1.Seaweed{ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns", UID: "owner"}, Spec: seaweedv1.SeaweedSpec{
		Image: "chrislusf/seaweedfs:4.53", Master: &seaweedv1.MasterSpec{Replicas: 1}, Filer: &seaweedv1.FilerSpec{}, Vector: &seaweedv1.VectorSpec{Replicas: 2},
	}}
}

func TestVectorDeployment(t *testing.T) {
	r := &SeaweedReconciler{}
	m := vectorCluster()
	for _, metrics := range []bool{false, true} {
		if metrics {
			m.Spec.Vector.MetricsPort = ptr.To(int32(9329))
		}
		deployment := r.createVectorDeployment(m)
		pod := &deployment.Spec.Template.Spec
		c := podContainer(t, pod, "vector")
		if c == nil || *deployment.Spec.Replicas != 2 {
			t.Fatal("missing vector engine replicas")
		}
		args := strings.Join(c.Args, " ")
		if !strings.Contains(strings.Join(c.Command, " "), `--listen="$POD_IP:8090"`) || !strings.Contains(args, "--masters=") {
			t.Fatalf("discovery args: %s", args)
		}
		if metrics != strings.Contains(args, "--metrics-port=9329") || metrics != (c.ReadinessProbe.HTTPGet != nil) {
			t.Fatalf("metrics/probes: %+v", c)
		}
		if !metrics && c.ReadinessProbe.TCPSocket == nil {
			t.Fatal("legacy image needs TCP probe")
		}
		if c.StartupProbe == nil || c.StartupProbe.FailureThreshold < 60 || pod.NodeSelector["kubernetes.io/os"] != "linux" {
			t.Fatal("missing startup or platform guard")
		}
		ports := r.createVectorService(m).Spec.Ports
		if len(ports) != 1+map[bool]int{true: 1}[metrics] {
			t.Fatal(ports)
		}
	}
	if !desiredServiceMonitorNames(m)["sw-vector"] {
		t.Fatal("vector monitor would be pruned")
	}
	if r.createVectorServiceMonitor(m).Spec.Endpoints[0].Port != "vector-metrics" {
		t.Fatal("incorrect monitor target")
	}
}

func TestVectorTLS(t *testing.T) {
	m := vectorCluster()
	m.Spec.TLS = &seaweedv1.TLSSpec{Enabled: true, Source: &seaweedv1.TLSSource{Volume: &corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "tls"}}}}
	c := podContainer(t, &(&SeaweedReconciler{}).createVectorDeployment(m).Spec.Template.Spec, "vector")
	found := false
	for _, e := range c.Env {
		if e.Name == "WEED_VECTOR_SECURITY_TOML" {
			found = true
		}
	}
	if !found || len(c.VolumeMounts) == 0 {
		t.Fatal("missing vector security configuration")
	}
	config := renderSecurityTOML(seaweedv1.JWTSigningSpec{}, jwtSigningKeys{}, m.Spec.TLS)
	if !strings.Contains(config, "[grpc.vector]") || !strings.Contains(config, "[https.client]") {
		t.Fatal(config)
	}
}

func TestVectorRemovalPrunesOwnedResources(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = seaweedv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	m := vectorCluster()
	r := &SeaweedReconciler{Scheme: scheme}
	d := r.createVectorDeployment(m)
	svc := r.createVectorService(m)
	_ = controllerutil.SetControllerReference(m, d, scheme)
	_ = controllerutil.SetControllerReference(m, svc, scheme)
	r.Client = fake.NewClientBuilder().WithScheme(scheme).WithObjects(m, d, svc).Build()
	m.Spec.Vector = nil
	if _, _, err := r.ensureVectors(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	var deployments appsv1.DeploymentList
	if err := r.List(context.Background(), &deployments, client.InNamespace("ns")); err != nil || len(deployments.Items) != 0 {
		t.Fatalf("stale vectors: %+v %v", deployments, err)
	}
}
