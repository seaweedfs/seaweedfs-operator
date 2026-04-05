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
	commands := []string{"weed", "-logtostderr=true", "worker"}
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

func (r *SeaweedReconciler) createWorkerStatefulSet(m *seaweedv1.Seaweed) *appsv1.StatefulSet {
	labels := labelsForWorker(m.Name)
	annotations := m.Spec.Worker.Annotations
	var ports []corev1.ContainerPort
	if m.Spec.Worker.MetricsPort != nil {
		ports = append(ports, corev1.ContainerPort{
			ContainerPort: *m.Spec.Worker.MetricsPort,
			Name:          "worker-metrics",
		})
	}
	replicas := int32(m.Spec.Worker.Replicas)
	rollingUpdatePartition := int32(0)
	enableServiceLinks := false

	workerPodSpec := m.BaseWorkerSpec().BuildPodSpec()

	var volumeMounts []corev1.VolumeMount

	var persistentVolumeClaims []corev1.PersistentVolumeClaim
	if m.Spec.Worker.Persistence != nil && m.Spec.Worker.Persistence.Enabled {
		claimName := m.Name + "-worker"
		if m.Spec.Worker.Persistence.ExistingClaim != nil {
			claimName = *m.Spec.Worker.Persistence.ExistingClaim
		}
		if m.Spec.Worker.Persistence.ExistingClaim == nil {
			accessModes := m.Spec.Worker.Persistence.AccessModes
			if len(accessModes) == 0 {
				accessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
			}
			persistentVolumeClaims = append(persistentVolumeClaims, corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: claimName,
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes:      accessModes,
					Resources:        m.Spec.Worker.Persistence.Resources,
					StorageClassName: m.Spec.Worker.Persistence.StorageClassName,
					Selector:         m.Spec.Worker.Persistence.Selector,
					VolumeName:       m.Spec.Worker.Persistence.VolumeName,
					VolumeMode:       m.Spec.Worker.Persistence.VolumeMode,
					DataSource:       m.Spec.Worker.Persistence.DataSource,
				},
			})
		} else {
			workerPodSpec.Volumes = append(workerPodSpec.Volumes, corev1.Volume{
				Name: claimName,
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: claimName,
						ReadOnly:  false,
					},
				},
			})
		}
		mountPath := "/data"
		if m.Spec.Worker.Persistence.MountPath != nil {
			mountPath = *m.Spec.Worker.Persistence.MountPath
		}
		subPath := ""
		if m.Spec.Worker.Persistence.SubPath != nil {
			subPath = *m.Spec.Worker.Persistence.SubPath
		}
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      claimName,
			ReadOnly:  false,
			MountPath: mountPath,
			SubPath:   subPath,
		})
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
	workerPodSpec.Containers = []corev1.Container{container}

	dep := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-worker",
			Namespace: m.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName:         m.Name + "-worker-peer",
			PodManagementPolicy: appsv1.ParallelPodManagement,
			Replicas:            &replicas,
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.RollingUpdateStatefulSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{
					Partition: &rollingUpdatePartition,
				},
			},
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
			VolumeClaimTemplates: persistentVolumeClaims,
		},
	}
	return dep
}
