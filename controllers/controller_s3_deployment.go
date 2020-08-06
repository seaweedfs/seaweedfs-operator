package controllers

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createS3Deployment(m *seaweedv1.Seaweed) *appsv1.Deployment {
	labels := labelsForS3(m.Name)
	replicas := int32(m.Spec.S3Count)
	enableServiceLinks := false

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-s3",
			Namespace: m.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
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
						Image:           "chrislusf/seaweedfs:latest",
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
							fmt.Sprintf("weed s3 -port=8333 %s",
								fmt.Sprintf("-filer=$(POD_NAME).%s-filer:8888", m.Name),
							),
						},
						Ports: []corev1.ContainerPort{
							{
								ContainerPort: 8333,
								Name:          "swfs-s3",
							},
							{
								ContainerPort: 18333,
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
	return dep
}
