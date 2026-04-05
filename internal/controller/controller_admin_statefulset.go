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

func buildAdminStartupScript(m *seaweedv1.Seaweed, extraArgs ...string) string {
	commands := []string{"weed", "-logtostderr=true", "admin"}
	commands = append(commands, fmt.Sprintf("-port=%d", seaweedv1.AdminHTTPPort))
	commands = append(commands, fmt.Sprintf("-master=%s", getMasterPeersString(m)))
	if m.Spec.Admin.MetricsPort != nil {
		commands = append(commands, fmt.Sprintf("-metricsPort=%d", *m.Spec.Admin.MetricsPort))
	}
	commands = append(commands, extraArgs...)

	return strings.Join(commands, " ")
}

func (r *SeaweedReconciler) createAdminStatefulSet(m *seaweedv1.Seaweed) *appsv1.StatefulSet {
	labels := labelsForAdmin(m.Name)
	annotations := m.BaseAdminSpec().Annotations()
	ports := []corev1.ContainerPort{
		{
			ContainerPort: seaweedv1.AdminHTTPPort,
			Name:          "admin-http",
		},
		{
			ContainerPort: seaweedv1.AdminGRPCPort,
			Name:          "admin-grpc",
		},
	}
	if m.Spec.Admin.MetricsPort != nil {
		ports = append(ports, corev1.ContainerPort{
			ContainerPort: *m.Spec.Admin.MetricsPort,
			Name:          "admin-metrics",
		})
	}
	replicas := int32(1)
	rollingUpdatePartition := int32(0)
	enableServiceLinks := false

	adminPodSpec := m.BaseAdminSpec().BuildPodSpec()

	var volumeMounts []corev1.VolumeMount

	// Mount credentials secret if provided
	if m.Spec.Admin.CredentialsSecret != nil && m.Spec.Admin.CredentialsSecret.Name != "" {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "admin-credentials",
			ReadOnly:  true,
			MountPath: "/etc/sw/admin",
		})
		adminPodSpec.Volumes = append(adminPodSpec.Volumes, corev1.Volume{
			Name: "admin-credentials",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: m.Spec.Admin.CredentialsSecret.Name,
				},
			},
		})
	}

	adminPodSpec.EnableServiceLinks = &enableServiceLinks
	adminPodSpec.Containers = []corev1.Container{{
		Name:            "admin",
		Image:           m.Spec.Image,
		ImagePullPolicy: m.BaseAdminSpec().ImagePullPolicy(),
		Env:             append(m.BaseAdminSpec().Env(), kubernetesEnvVars...),
		Resources:       filterContainerResources(m.Spec.Admin.ResourceRequirements),
		VolumeMounts:    mergeVolumeMounts(volumeMounts, m.BaseAdminSpec().VolumeMounts()),
		Command: []string{
			"/bin/sh",
			"-ec",
			buildAdminStartupScript(m, m.BaseAdminSpec().ExtraArgs()...),
		},
		Ports: ports,
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   "/health",
					Port:   intstr.FromInt(seaweedv1.AdminHTTPPort),
					Scheme: corev1.URISchemeHTTP,
				},
			},
			InitialDelaySeconds: 10,
			TimeoutSeconds:      3,
			PeriodSeconds:       15,
			SuccessThreshold:    1,
			FailureThreshold:    6,
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   "/health",
					Port:   intstr.FromInt(seaweedv1.AdminHTTPPort),
					Scheme: corev1.URISchemeHTTP,
				},
			},
			InitialDelaySeconds: 20,
			TimeoutSeconds:      3,
			PeriodSeconds:       30,
			SuccessThreshold:    1,
			FailureThreshold:    6,
		},
	}}

	dep := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-admin",
			Namespace: m.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName:         m.Name + "-admin-peer",
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
				Spec: adminPodSpec,
			},
		},
	}
	return dep
}
