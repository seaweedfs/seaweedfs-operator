package controller

import (
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func getAdminAddress(m *seaweedv1.Seaweed) string {
	return fmt.Sprintf("%s-admin:%d", m.Name, seaweedv1.AdminHTTPPort)
}

func buildWorkerStartupScript(m *seaweedv1.Seaweed, extraArgs ...string) string {
	commands := weedPreamble(m, m.BaseWorkerSpec().LoggingArgs(), "worker")
	commands = append(commands, fmt.Sprintf("-admin=%s", getAdminAddress(m)))
	if m.Spec.Worker.Persistence != nil && m.Spec.Worker.Persistence.Enabled {
		mountPath := "/data"
		if m.Spec.Worker.Persistence.MountPath != nil {
			mountPath = *m.Spec.Worker.Persistence.MountPath
		}
		commands = append(commands, fmt.Sprintf("-workingDir=%s", mountPath))
	}
	if m.Spec.Worker.JobType != nil {
		commands = append(commands, fmt.Sprintf("-jobType=%s", *m.Spec.Worker.JobType))
	}
	if m.Spec.Worker.MaxDetect != nil {
		commands = append(commands, fmt.Sprintf("-maxDetect=%d", *m.Spec.Worker.MaxDetect))
	}
	if m.Spec.Worker.MaxExecute != nil {
		commands = append(commands, fmt.Sprintf("-maxExecute=%d", *m.Spec.Worker.MaxExecute))
	}
	if m.Spec.Worker.MetricsPort != nil {
		commands = append(commands, fmt.Sprintf("-metricsPort=%d", *m.Spec.Worker.MetricsPort))
	}
	commands = append(commands, extraArgs...)

	return strings.Join(commands, " ")
}

// buildRustWorkerStartupScript renders the worker-lance sidecar's
// /usr/bin/weed-worker command line.
func buildRustWorkerStartupScript(m *seaweedv1.Seaweed) string {
	commands := []string{"/usr/bin/weed-worker"}
	commands = append(commands, fmt.Sprintf("--admin=%s", getAdminAddress(m)))
	// The Rust default id is fixed, so replicas > 1 would collide without the pod name.
	commands = append(commands, "--id=$(POD_NAME)")
	// The sidecar itself is gated on ServesLance; the nil guard covers direct callers.
	lancePort := int32(seaweedv1.FilerLancePort)
	if m.Spec.Filer != nil {
		lancePort = m.Spec.Filer.Lance.LanceEffectivePort()
	}
	commands = append(commands, fmt.Sprintf("--namespace=http://%s-filer:%d", m.Name, lancePort))
	// The Rust binary reads no security.toml; it takes the same certificates as flags.
	if tlsEffective(m) {
		commands = append(commands,
			fmt.Sprintf("--tls-ca=%s/ca.crt", tlsMountPath),
			fmt.Sprintf("--tls-cert=%s/tls.crt", tlsMountPath),
			fmt.Sprintf("--tls-key=%s/tls.key", tlsMountPath))
	}
	// -maxExecute defaults to 4 but --max-concurrency to 1, so render the shared 4.
	maxConcurrency := int32(4)
	if m.Spec.Worker.MaxExecute != nil {
		maxConcurrency = *m.Spec.Worker.MaxExecute
	}
	commands = append(commands, fmt.Sprintf("--max-concurrency=%d", maxConcurrency))
	// weed-worker binds metrics to loopback by default; probes need the pod IP.
	commands = append(commands, fmt.Sprintf("--metrics-port=%d", seaweedv1.WorkerLanceMetricsPort))
	commands = append(commands, "--metrics-ip=0.0.0.0")

	// Images before 4.45 predate the binary; off amd64/arm64 it is an empty, exit-0 placeholder.
	guard := `if [ ! -s /usr/bin/weed-worker ]; then echo "weed-worker is missing or empty in this image ($(uname -m)); it ships in images 4.45+ on amd64/arm64" >&2; exit 1; fi`

	return guard + "\n" + strings.Join(commands, " ")
}

// workerProbes builds the /ready and /health probes a worker container serves on its metrics port.
func workerProbes(port int32) (readiness, liveness *corev1.Probe) {
	readiness = &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path:   "/ready",
				Port:   intstr.FromInt(int(port)),
				Scheme: corev1.URISchemeHTTP,
			},
		},
		InitialDelaySeconds: 10,
		TimeoutSeconds:      3,
		PeriodSeconds:       15,
		SuccessThreshold:    1,
		FailureThreshold:    6,
	}
	liveness = &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path:   "/health",
				Port:   intstr.FromInt(int(port)),
				Scheme: corev1.URISchemeHTTP,
			},
		},
		InitialDelaySeconds: 20,
		TimeoutSeconds:      3,
		PeriodSeconds:       30,
		SuccessThreshold:    1,
		FailureThreshold:    6,
	}
	return readiness, liveness
}

func (r *SeaweedReconciler) createWorkerDeployment(m *seaweedv1.Seaweed) *appsv1.Deployment {
	labels := labelsForWorker(m.Name)
	podLabels := mergePodLabels(labels, m.BaseWorkerSpec().Labels())
	annotations := withJWTSigningAnnotation(m, m.BaseWorkerSpec().Annotations())
	var ports []corev1.ContainerPort
	if m.Spec.Worker.MetricsPort != nil {
		ports = append(ports, corev1.ContainerPort{
			ContainerPort: *m.Spec.Worker.MetricsPort,
			Name:          "worker-metrics",
		})
	}
	replicas := m.Spec.Worker.Replicas
	enableServiceLinks := false

	workerPodSpec := m.BaseWorkerSpec().BuildPodSpec()

	var volumeMounts []corev1.VolumeMount
	if m.Spec.Worker.Persistence != nil && m.Spec.Worker.Persistence.Enabled {
		mountPath := "/data"
		if m.Spec.Worker.Persistence.MountPath != nil {
			mountPath = *m.Spec.Worker.Persistence.MountPath
		}
		subPath := ""
		if m.Spec.Worker.Persistence.SubPath != nil {
			subPath = *m.Spec.Worker.Persistence.SubPath
		}
		// Deployments cannot own per-replica PVCs (no VolumeClaimTemplates
		// equivalent). Two supported modes:
		//   - ExistingClaim set: mount that shared PVC (caller is
		//     responsible for RWX if replicas > 1).
		//   - Otherwise: fall back to emptyDir so -workingDir points at a
		//     valid scratch path. Worker state is already ephemeral
		//     (admin re-dispatches jobs after restart), so this is the
		//     safer default than a shared RWO PVC.
		volumeName := "worker-data"
		if m.Spec.Worker.Persistence.ExistingClaim != nil {
			workerPodSpec.Volumes = append(workerPodSpec.Volumes, corev1.Volume{
				Name: volumeName,
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: *m.Spec.Worker.Persistence.ExistingClaim,
					},
				},
			})
		} else {
			workerPodSpec.Volumes = append(workerPodSpec.Volumes, corev1.Volume{
				Name:         volumeName,
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			})
		}
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      volumeName,
			MountPath: mountPath,
			SubPath:   subPath,
		})
	}
	tlsVols, tlsMounts := tlsVolumesAndMounts(m)
	if len(tlsVols) > 0 {
		workerPodSpec.Volumes = append(workerPodSpec.Volumes, tlsVols...)
		volumeMounts = append(volumeMounts, tlsMounts...)
	}

	container := corev1.Container{
		Name:            "worker",
		Image:           m.BaseWorkerSpec().Image(),
		ImagePullPolicy: m.BaseWorkerSpec().ImagePullPolicy(),
		SecurityContext: m.BaseWorkerSpec().ContainerSecurityContext(),
		Env:             append(m.BaseWorkerSpec().Env(), kubernetesEnvVars...),
		Resources:       filterContainerResources(m.Spec.Worker.ResourceRequirements),
		VolumeMounts:    mergeVolumeMounts(volumeMounts, m.BaseWorkerSpec().VolumeMounts()),
		Command: []string{
			"/bin/sh",
			"-ec",
			buildWorkerStartupScript(m, m.BaseWorkerSpec().ExtraArgs()...),
		},
		Ports: ports,
	}

	// Only add health probes if metricsPort is set (worker exposes /health and
	// /ready on metricsPort). The readinessProbe/livenessProbe overrides applied
	// below therefore only take effect when metricsPort is set; without a probe
	// there is nothing to tune.
	if m.Spec.Worker.MetricsPort != nil {
		container.ReadinessProbe, container.LivenessProbe = workerProbes(*m.Spec.Worker.MetricsPort)
		applyProbeOverrides(&container, m.BaseWorkerSpec().ReadinessProbe(), m.BaseWorkerSpec().LivenessProbe())
	}

	containers := []corev1.Container{container}
	// The two workers share no jobs: weed-worker serves the lance_* family.
	if m.Spec.Filer.ServesLance() {
		lanceContainer := corev1.Container{
			Name:            "worker-lance",
			Image:           m.BaseWorkerSpec().Image(),
			ImagePullPolicy: m.BaseWorkerSpec().ImagePullPolicy(),
			SecurityContext: m.BaseWorkerSpec().ContainerSecurityContext(),
			Env:             append(m.BaseWorkerSpec().Env(), kubernetesEnvVars...),
			// No persistence: it backs -workingDir, which the Rust worker does not take.
			VolumeMounts: tlsMounts,
			Command: []string{
				"/bin/sh",
				"-ec",
				buildRustWorkerStartupScript(m),
			},
			Ports: []corev1.ContainerPort{{
				ContainerPort: seaweedv1.WorkerLanceMetricsPort,
				Name:          "lance-metrics",
			}},
		}
		lanceContainer.ReadinessProbe, lanceContainer.LivenessProbe = workerProbes(seaweedv1.WorkerLanceMetricsPort)
		applyProbeOverrides(&lanceContainer, m.BaseWorkerSpec().ReadinessProbe(), m.BaseWorkerSpec().LivenessProbe())
		containers = append(containers, lanceContainer)
	}

	workerPodSpec.EnableServiceLinks = &enableServiceLinks
	workerPodSpec.Containers = append(containers, m.BaseWorkerSpec().Sidecars()...)
	workerPodSpec.InitContainers = append(workerPodSpec.InitContainers, m.BaseWorkerSpec().InitContainers()...)

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-worker",
			Namespace: m.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      podLabels,
					Annotations: annotations,
				},
				Spec: workerPodSpec,
			},
		},
	}
}
