package controllers

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createFilerStatefulSet(m *seaweedv1.Seaweed) *appsv1.StatefulSet {
	labels := labelsForFiler(m.Name)
	replicas := int32(m.Spec.Filer.Replicas)
	rollingUpdatePartition := int32(0)
	enableServiceLinks := false

	filerPodSpec := m.BaseFilerSpec().BuildPodSpec()
	filerPodSpec.Volumes = []corev1.Volume{
		{
			Name: "filer-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: m.Name + "-filer",
					},
				},
			},
		},
	}
	filerPodSpec.EnableServiceLinks = &enableServiceLinks
	filerPodSpec.Containers = []corev1.Container{{
		Name:            "seaweedfs",
		Image:           m.Spec.Image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "filer-config",
				ReadOnly:  true,
				MountPath: "/etc/seaweedfs/filer.toml",
			},
		},
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
			fmt.Sprintf("weed filer -port=8888 %s %s -s3",
				fmt.Sprintf("-ip=$(POD_NAME).%s-filer", m.Name),
				fmt.Sprintf("-master=%s", getMasterPeersString(m.Name, m.Spec.Master.Replicas)),
			),
		},
		Ports: []corev1.ContainerPort{
			{
				ContainerPort: seaweedv1.FilerHTTPPort,
				Name:          "swfs-filer",
			},
			{
				ContainerPort: seaweedv1.FilerGRPCPort,
			},
			{
				ContainerPort: seaweedv1.FilerS3Port,
				Name:          "swfs-s3",
			},
		},
		ReadinessProbe: &corev1.Probe{
			Handler: corev1.Handler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   "/",
					Port:   intstr.FromInt(seaweedv1.FilerHTTPPort),
					Scheme: corev1.URISchemeHTTP,
				},
			},
			InitialDelaySeconds: 10,
			TimeoutSeconds:      3,
			PeriodSeconds:       15,
			SuccessThreshold:    1,
			FailureThreshold:    100,
		},
		LivenessProbe: &corev1.Probe{
			Handler: corev1.Handler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   "/",
					Port:   intstr.FromInt(seaweedv1.FilerHTTPPort),
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
			Name:      m.Name + "-filer",
			Namespace: m.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName:         m.Name + "-filer",
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
					Labels: labels,
				},
				Spec: filerPodSpec,
			},
		},
	}
	return dep
}
