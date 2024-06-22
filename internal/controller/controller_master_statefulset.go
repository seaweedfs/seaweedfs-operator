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

func buildMasterStartupScript(m *seaweedv1.Seaweed) string {
	command := []string{"weed", "-logtostderr=true", "master"}
	spec := m.Spec.Master
	if spec.VolumePreallocate != nil && *spec.VolumePreallocate {
		command = append(command, "-volumePreallocate")
	}

	if spec.VolumeSizeLimitMB != nil {
		command = append(command, fmt.Sprintf("-volumeSizeLimitMB=%d", *spec.VolumeSizeLimitMB))
	}

	if spec.GarbageThreshold != nil {
		command = append(command, fmt.Sprintf("-garbageThreshold=%s", *spec.GarbageThreshold))
	}

	if spec.PulseSeconds != nil {
		command = append(command, fmt.Sprintf("-pulseSeconds=%d", *spec.PulseSeconds))
	}

	if spec.DefaultReplication != nil {
		command = append(command, fmt.Sprintf("-defaultReplication=%s", *spec.DefaultReplication))
	}

	if m.Spec.Master.MetricsPort != nil {
		command = append(command, fmt.Sprintf("-metricsPort=%d", *m.Spec.Master.MetricsPort))
	}

	command = append(command, fmt.Sprintf("-ip=$(POD_NAME).%s-master-peer.%s", m.Name, m.Namespace))
	command = append(command, fmt.Sprintf("-peers=%s", getMasterPeersString(m)))
	return strings.Join(command, " ")
}

func (r *SeaweedReconciler) createMasterStatefulSet(m *seaweedv1.Seaweed) *appsv1.StatefulSet {
	labels := labelsForMaster(m.Name)
	annotations := m.Spec.Master.Annotations
	ports := []corev1.ContainerPort{
		{
			ContainerPort: seaweedv1.MasterHTTPPort,
			Name:          "master-http",
		},
		{
			ContainerPort: seaweedv1.MasterGRPCPort,
			Name:          "master-grpc",
		},
	}
	if m.Spec.Master.MetricsPort != nil {
		ports = append(ports, corev1.ContainerPort{
			ContainerPort: *m.Spec.Master.MetricsPort,
			Name:          "master-metrics",
		})
	}
	replicas := m.Spec.Master.Replicas
	rollingUpdatePartition := int32(0)
	enableServiceLinks := false

	masterPodSpec := m.BaseMasterSpec().BuildPodSpec()
	masterPodSpec.Volumes = []corev1.Volume{
		{
			Name: "master-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: m.Name + "-master",
					},
				},
			},
		},
	}
	masterPodSpec.EnableServiceLinks = &enableServiceLinks
	masterPodSpec.Containers = []corev1.Container{{
		Name:            "master",
		Image:           m.Spec.Image,
		ImagePullPolicy: m.BaseMasterSpec().ImagePullPolicy(),
		Env:             append(m.BaseMasterSpec().Env(), kubernetesEnvVars...),
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "master-config",
				ReadOnly:  true,
				MountPath: "/etc/seaweedfs",
			},
		},
		Command: []string{
			"/bin/sh",
			"-ec",
			buildMasterStartupScript(m),
		},
		Ports: ports,
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   "/cluster/status",
					Port:   intstr.FromInt(seaweedv1.MasterHTTPPort),
					Scheme: corev1.URISchemeHTTP,
				},
			},
			InitialDelaySeconds: 5,
			TimeoutSeconds:      15,
			PeriodSeconds:       15,
			SuccessThreshold:    2,
			FailureThreshold:    100,
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   "/cluster/status",
					Port:   intstr.FromInt(seaweedv1.MasterHTTPPort),
					Scheme: corev1.URISchemeHTTP,
				},
			},
			InitialDelaySeconds: 15,
			TimeoutSeconds:      15,
			PeriodSeconds:       15,
			SuccessThreshold:    1,
			FailureThreshold:    6,
		},
	}}

	dep := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-master",
			Namespace: m.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName:         m.Name + "-master-peer",
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
				Spec: masterPodSpec,
			},
		},
	}
	// Set master instance as the owner and controller
	// ctrl.SetControllerReference(m, dep, r.Scheme)
	return dep
}
