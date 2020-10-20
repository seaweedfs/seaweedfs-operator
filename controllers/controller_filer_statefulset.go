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
	replicas := int32(m.Spec.FilerCount)
	rollingUpdatePartition := int32(0)
	enableServiceLinks := false

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
				Spec: corev1.PodSpec{
					EnableServiceLinks: &enableServiceLinks,
					Containers: []corev1.Container{{
						Name:            "seaweedfs",
						Image:           m.Spec.Image,
						ImagePullPolicy: corev1.PullIfNotPresent,
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
								fmt.Sprintf("-master=%s-master-0.%s-master:9333,%s-master-1.%s-master:9333,%s-master-2.%s-master:9333",
									m.Name, m.Name, m.Name, m.Name, m.Name, m.Name),
							),
						},
						Ports: []corev1.ContainerPort{
							{
								ContainerPort: 8888,
								Name:          "swfs-filer",
							},
							{
								ContainerPort: 18888,
							},
							{
								ContainerPort: 8333,
								Name:          "swfs-s3",
							},
						},
						ReadinessProbe: &corev1.Probe{
							Handler: corev1.Handler{
								HTTPGet: &corev1.HTTPGetAction{
									Path:   "/",
									Port:   intstr.FromInt(8888),
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
									Port:   intstr.FromInt(8888),
									Scheme: corev1.URISchemeHTTP,
								},
							},
							InitialDelaySeconds: 20,
							TimeoutSeconds:      3,
							PeriodSeconds:       30,
							SuccessThreshold:    1,
							FailureThreshold:    6,
						},
					}},
				},
			},
		},
	}
	return dep
}
