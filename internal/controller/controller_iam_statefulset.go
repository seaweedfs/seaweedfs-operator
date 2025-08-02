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

func buildIAMStartupScript(m *seaweedv1.Seaweed) string {
	commands := []string{"weed", "-logtostderr=true", "iam"}
	commands = append(commands, fmt.Sprintf("-master=%s", getMasterPeersString(m)))
	commands = append(commands, fmt.Sprintf("-filer=%s-filer:%d", m.Name, seaweedv1.FilerHTTPPort))

	// Use custom port if specified, otherwise use default
	iamPort := int32(seaweedv1.FilerIAMPort)
	if m.Spec.IAM.Port != nil {
		iamPort = *m.Spec.IAM.Port
	}
	commands = append(commands, fmt.Sprintf("-port=%d", iamPort))

	if m.Spec.IAM.MetricsPort != nil {
		commands = append(commands, fmt.Sprintf("-metricsPort=%d", *m.Spec.IAM.MetricsPort))
	}

	return strings.Join(commands, " ")
}

func (r *SeaweedReconciler) createIAMStatefulSet(m *seaweedv1.Seaweed) *appsv1.StatefulSet {
	labels := labelsForIAM(m.Name)
	annotations := m.Spec.IAM.Annotations

	iamPort := int32(seaweedv1.FilerIAMPort)
	if m.Spec.IAM.Port != nil {
		iamPort = *m.Spec.IAM.Port
	}

	ports := []corev1.ContainerPort{
		{
			ContainerPort: iamPort,
			Name:          "iam-http",
		},
	}

	if m.Spec.IAM.MetricsPort != nil {
		ports = append(ports, corev1.ContainerPort{
			ContainerPort: *m.Spec.IAM.MetricsPort,
			Name:          "iam-metrics",
		})
	}

	replicas := int32(m.Spec.IAM.Replicas)
	rollingUpdatePartition := int32(0)
	enableServiceLinks := false

	iamPodSpec := m.BaseIAMSpec().BuildPodSpec()
	iamPodSpec.EnableServiceLinks = &enableServiceLinks
	iamPodSpec.Containers = []corev1.Container{
		{
			Name:            "iam",
			Image:           m.Spec.Image,
			ImagePullPolicy: m.BaseIAMSpec().ImagePullPolicy(),
			Env: []corev1.EnvVar{
				{
					Name: "POD_IP",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "status.podIP",
						},
					},
				},
				{
					Name: "POD_NAME",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.name",
						},
					},
				},
				{
					Name: "NAMESPACE",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.namespace",
						},
					},
				},
			},
			Command: []string{
				"/bin/sh",
				"-ec",
				buildIAMStartupScript(m),
			},
			Ports: ports,
			ReadinessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path:   "/",
						Port:   intstr.FromInt(int(iamPort)),
						Scheme: corev1.URISchemeHTTP,
					},
				},
				InitialDelaySeconds: 10,
				PeriodSeconds:       45,
				SuccessThreshold:    2,
				FailureThreshold:    10,
			},
			LivenessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path:   "/",
						Port:   intstr.FromInt(int(iamPort)),
						Scheme: corev1.URISchemeHTTP,
					},
				},
				InitialDelaySeconds: 20,
				PeriodSeconds:       30,
				SuccessThreshold:    1,
				FailureThreshold:    5,
			},
			Resources: m.Spec.IAM.ResourceRequirements,
		},
	}

	iamPodSpec.Volumes = []corev1.Volume{}
	if m.Spec.IAM.Config != nil {
		iamPodSpec.Volumes = append(iamPodSpec.Volumes, corev1.Volume{
			Name: "iam-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: m.Name + "-iam",
					},
				},
			},
		})
		volumeMounts := []corev1.VolumeMount{
			{
				Name:      "iam-config",
				ReadOnly:  true,
				MountPath: "/etc/seaweedfs",
			},
		}
		iamPodSpec.Containers[0].VolumeMounts = volumeMounts
	}

	dep := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-iam",
			Namespace: m.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: m.Name + "-iam",
			Replicas:    &replicas,
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: m.Spec.StatefulSetUpdateStrategy,
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
				Spec: iamPodSpec,
			},
		},
	}
	return dep
}
