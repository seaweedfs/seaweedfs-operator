package controllers

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createMasterStatefulSet(m *seaweedv1.Seaweed) *appsv1.StatefulSet {
	labels := labelsForMaster(m.Name)
	replicas := int32(MasterClusterSize)
	rollingUpdatePartition := int32(0)
	enableServiceLinks := false

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
				Spec: corev1.PodSpec{
					/*
						Affinity: &corev1.Affinity{
							PodAntiAffinity: &corev1.PodAntiAffinity{
								RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
									{
										LabelSelector: &metav1.LabelSelector{
											MatchLabels: labels,
										},
										TopologyKey: "kubernetes.io/hostname",
									},
								},
							},
						},
					*/
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
							fmt.Sprintf("sleep 60; weed master -volumePreallocate -volumeSizeLimitMB=1000 %s %s",
								fmt.Sprintf("-ip=$(POD_NAME).%s-master", m.Name),
								fmt.Sprintf("-peers=%s-master-0.%s-master:9333,%s-master-1.%s-master:9333,%s-master-2.%s-master:9333",
									m.Name, m.Name, m.Name, m.Name, m.Name, m.Name),
							),
						},
						Ports: []corev1.ContainerPort{
							{
								ContainerPort: 9333,
								Name:          "swfs-master",
							},
							{
								ContainerPort: 19333,
							},
						},
						/*
							ReadinessProbe: &corev1.Probe{
								Handler: corev1.Handler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/cluster/status",
										Port: intstr.IntOrString{
											Type:   0,
											IntVal: 9333,
										},
										Scheme: "http",
									},
								},
								InitialDelaySeconds: 5,
								TimeoutSeconds:      0,
								PeriodSeconds:       15,
								SuccessThreshold:    2,
								FailureThreshold:    100,
							},
							LivenessProbe: &corev1.Probe{
								Handler: corev1.Handler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/cluster/status",
										Port: intstr.IntOrString{
											Type:   0,
											IntVal: 9333,
										},
										Scheme: "http",
									},
								},
								InitialDelaySeconds: 20,
								TimeoutSeconds:      0,
								PeriodSeconds:       10,
								SuccessThreshold:    1,
								FailureThreshold:    6,
							},

						*/
					}},
				},
			},
		},
	}
	// Set master instance as the owner and controller
	// ctrl.SetControllerReference(m, dep, r.Scheme)
	return dep
}
