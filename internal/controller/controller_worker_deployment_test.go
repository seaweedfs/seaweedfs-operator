package controller

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// newWorkerCluster leaves spec.filer.lance unset; tests that need the sidecar enable it.
func newWorkerCluster(worker *seaweedv1.WorkerSpec) *seaweedv1.Seaweed {
	return &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			Admin:  &seaweedv1.AdminSpec{},
			Filer:  &seaweedv1.FilerSpec{Replicas: 1},
			Worker: worker,
		},
	}
}

func podContainer(t *testing.T, pod *corev1.PodSpec, name string) *corev1.Container {
	t.Helper()
	for i := range pod.Containers {
		if pod.Containers[i].Name == name {
			return &pod.Containers[i]
		}
	}
	return nil
}

func TestBuildRustWorkerStartupScript(t *testing.T) {
	maxExecute := int32(7)
	maxDetect := int32(2)
	metricsPort := int32(9350)
	jobType := "all"
	m := newWorkerCluster(&seaweedv1.WorkerSpec{
		Replicas:    1,
		MetricsPort: &metricsPort,
		MaxExecute:  &maxExecute,
		MaxDetect:   &maxDetect,
		JobType:     &jobType,
	})

	got := buildRustWorkerStartupScript(m)
	for _, want := range []string{
		"if [ ! -s /usr/bin/weed-worker ]; then",
		"it ships in images 4.45+ on amd64/arm64",
		"/usr/bin/weed-worker --admin=sw-admin:23646",
		"--id=$(POD_NAME)",
		"--namespace=http://sw-filer:9101",
		"--max-concurrency=7",
		"--metrics-port=9328",
		"--metrics-ip=0.0.0.0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in rust worker script, got %q", want, got)
		}
	}
	// weed-worker exits on unknown flags; Go-only knobs must stay off its command line.
	for _, absent := range []string{"9350", "-jobType", "-maxDetect", "weed "} {
		if strings.Contains(got, absent) {
			t.Errorf("unexpected %q in rust worker script %q", absent, got)
		}
	}
}

func TestBuildRustWorkerStartupScript_TLS(t *testing.T) {
	certManagerCRDMu.Lock()
	prev := certManagerCRDAvailable
	certManagerCRDAvailable = true
	certManagerCRDMu.Unlock()
	defer func() {
		certManagerCRDMu.Lock()
		certManagerCRDAvailable = prev
		certManagerCRDMu.Unlock()
	}()

	m := newWorkerCluster(&seaweedv1.WorkerSpec{Replicas: 1})
	m.Spec.TLS = &seaweedv1.TLSSpec{Enabled: true}

	got := buildRustWorkerStartupScript(m)
	for _, want := range []string{
		"--tls-ca=/etc/sw-tls/ca.crt",
		"--tls-cert=/etc/sw-tls/tls.crt",
		"--tls-key=/etc/sw-tls/tls.key",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in TLS rust worker script, got %q", want, got)
		}
	}
}

// The shared default is the Go worker's 4, not the Rust binary's 1.
func TestBuildRustWorkerStartupScript_DefaultConcurrency(t *testing.T) {
	m := newWorkerCluster(&seaweedv1.WorkerSpec{Replicas: 1})
	if got := buildRustWorkerStartupScript(m); !strings.Contains(got, "--max-concurrency=4") {
		t.Errorf("expected the shared default --max-concurrency=4, got %q", got)
	}
}

// The namespace URL must carry the same effective port the filer service publishes.
func TestBuildRustWorkerStartupScript_CustomLancePort(t *testing.T) {
	port := int32(19101)
	m := newWorkerCluster(&seaweedv1.WorkerSpec{Replicas: 1})
	m.Spec.Filer.Lance = &seaweedv1.LanceConfig{Enabled: true, Port: &port}

	if got := buildRustWorkerStartupScript(m); !strings.Contains(got, "--namespace=http://sw-filer:19101") {
		t.Errorf("expected custom lance port in namespace URL, got %q", got)
	}
}

func TestCreateWorkerDeploymentLanceSidecar(t *testing.T) {
	r := &SeaweedReconciler{}

	// The sidecar exits 1 on images without weed-worker, so an unset block must not run it.
	m := newWorkerCluster(&seaweedv1.WorkerSpec{Replicas: 1})
	pod := &r.createWorkerDeployment(m).Spec.Template.Spec
	if podContainer(t, pod, "worker-lance") != nil {
		t.Errorf("expected no sidecar with lance unset, got %v", pod.Containers)
	}

	m.Spec.Filer.Lance = &seaweedv1.LanceConfig{Enabled: true}
	pod = &r.createWorkerDeployment(m).Spec.Template.Spec

	worker := podContainer(t, pod, "worker")
	if worker == nil {
		t.Fatalf("worker container missing, got %v", pod.Containers)
	}
	if script := worker.Command[len(worker.Command)-1]; !strings.Contains(script, "weed") || !strings.Contains(script, " worker") {
		t.Errorf("expected the Go container to run weed worker, got %q", script)
	}

	lance := podContainer(t, pod, "worker-lance")
	if lance == nil {
		t.Fatalf("worker-lance sidecar missing, got %v", pod.Containers)
	}
	if script := lance.Command[len(lance.Command)-1]; !strings.Contains(script, "/usr/bin/weed-worker --admin") {
		t.Errorf("expected the sidecar to run /usr/bin/weed-worker, got %q", script)
	}
	if len(lance.Ports) != 1 || lance.Ports[0].ContainerPort != seaweedv1.WorkerLanceMetricsPort {
		t.Errorf("expected the sidecar to expose %d, got %v", seaweedv1.WorkerLanceMetricsPort, lance.Ports)
	}
	if lance.ReadinessProbe == nil || lance.ReadinessProbe.HTTPGet.Port.IntValue() != seaweedv1.WorkerLanceMetricsPort {
		t.Errorf("expected sidecar readiness probe on %d, got %v", seaweedv1.WorkerLanceMetricsPort, lance.ReadinessProbe)
	}
	if lance.LivenessProbe == nil || lance.LivenessProbe.HTTPGet.Port.IntValue() != seaweedv1.WorkerLanceMetricsPort {
		t.Errorf("expected sidecar liveness probe on %d, got %v", seaweedv1.WorkerLanceMetricsPort, lance.LivenessProbe)
	}

	m.Spec.Filer.Lance = &seaweedv1.LanceConfig{Enabled: false}
	pod = &r.createWorkerDeployment(m).Spec.Template.Spec
	if podContainer(t, pod, "worker-lance") != nil {
		t.Errorf("expected no sidecar with lance explicitly disabled, got %v", pod.Containers)
	}
}

// Persistence backs -workingDir, which only the Go worker takes.
func TestCreateWorkerDeploymentPersistencePlacement(t *testing.T) {
	m := newWorkerCluster(&seaweedv1.WorkerSpec{
		Replicas:    1,
		Persistence: &seaweedv1.PersistenceSpec{Enabled: true},
	})
	m.Spec.Filer.Lance = &seaweedv1.LanceConfig{Enabled: true}
	r := &SeaweedReconciler{}
	pod := &r.createWorkerDeployment(m).Spec.Template.Spec

	mounted := func(c *corev1.Container) bool {
		for _, vm := range c.VolumeMounts {
			if vm.Name == "worker-data" {
				return true
			}
		}
		return false
	}
	if !mounted(podContainer(t, pod, "worker")) {
		t.Errorf("expected the Go container to mount worker-data")
	}
	if mounted(podContainer(t, pod, "worker-lance")) {
		t.Errorf("expected the sidecar not to mount worker-data")
	}
}
