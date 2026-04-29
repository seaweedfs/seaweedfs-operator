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
	commands := []string{"weed", "-logtostderr=true"}
	if arg := tlsConfigDirArg(m); arg != "" {
		commands = append(commands, arg)
	}
	commands = append(commands, "worker")
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

func (r *SeaweedReconciler) createWorkerDeployment(m *seaweedv1.Seaweed) *appsv1.Deployment {
	labels := labelsForWorker(m.Name)
	annotations := m.BaseWorkerSpec().Annotations()
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
	if tlsVols, tlsMounts := tlsVolumesAndMounts(m); len(tlsVols) > 0 {
		workerPodSpec.Volumes = append(workerPodSpec.Volumes, tlsVols...)
		volumeMounts = append(volumeMounts, tlsMounts...)
	}

	container := corev1.Container{
		Name:            "worker",
		Image:           m.Spec.Image,
		ImagePullPolicy: m.BaseWorkerSpec().ImagePullPolicy(),
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

	// Only add health probes if metricsPort is set (worker exposes /health and /ready on metricsPort)
	if m.Spec.Worker.MetricsPort != nil {
		container.ReadinessProbe = &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   "/ready",
					Port:   intstr.FromInt(int(*m.Spec.Worker.MetricsPort)),
					Scheme: corev1.URISchemeHTTP,
				},
			},
			InitialDelaySeconds: 10,
			TimeoutSeconds:      3,
			PeriodSeconds:       15,
			SuccessThreshold:    1,
			FailureThreshold:    6,
		}
		container.LivenessProbe = &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   "/health",
					Port:   intstr.FromInt(int(*m.Spec.Worker.MetricsPort)),
					Scheme: corev1.URISchemeHTTP,
				},
			},
			InitialDelaySeconds: 20,
			TimeoutSeconds:      3,
			PeriodSeconds:       30,
			SuccessThreshold:    1,
			FailureThreshold:    6,
		}
	}

	workerPodSpec.EnableServiceLinks = &enableServiceLinks
	workerPodSpec.Containers = append([]corev1.Container{container}, m.BaseWorkerSpec().Sidecars()...)

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
					Labels:      labels,
					Annotations: annotations,
				},
				Spec: workerPodSpec,
			},
		},
	}
}
