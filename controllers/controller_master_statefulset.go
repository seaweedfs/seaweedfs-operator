package controllers

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createMasterStatefulSet(m *seaweedv1.Seaweed) *appsv1.StatefulSet {
	labels := labelsForMaster(m.Name)
	replicas := m.Spec.Master.Replicas
	rollingUpdatePartition := int32(0)
	enableServiceLinks := false

	masterPodSpec := m.BaseMasterSpec().BuildPodSpec()
	masterPodSpec.EnableServiceLinks = &enableServiceLinks
	masterPodSpec.Containers = []corev1.Container{{
		Name:            "seaweedfs",
		Image:           m.Spec.Image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Env:             append(m.BaseMasterSpec().Env(), kubernetesEnvVars...),
		Command: []string{
			"/bin/sh",
			"-ec",
			fmt.Sprintf("sleep 60; weed master -volumePreallocate -volumeSizeLimitMB=1000 %s %s",
				fmt.Sprintf("-ip=$(POD_NAME).%s-master", m.Name),
				fmt.Sprintf("-peers=%s", getMasterPeersString(m.Name, m.Spec.Master.Replicas)),
			),
		},
		Ports: []corev1.ContainerPort{
			{
				ContainerPort: seaweedv1.MasterHTTPPort,
				Name:          "swfs-master",
			},
			{
				ContainerPort: seaweedv1.MasterGRPCPort,
			},
		},
		ReadinessProbe: &corev1.Probe{
			Handler: corev1.Handler{
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
			Handler: corev1.Handler{
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
			ServiceName:         m.Name + "-master",
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
				Spec: masterPodSpec,
			},
		},
	}
	// Set master instance as the owner and controller
	// ctrl.SetControllerReference(m, dep, r.Scheme)
	return dep
}
