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

	dep := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name,
			Namespace: m.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Image: "chrislusf/seaweedfs:latest",
						Name:  "master",
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
							"weed",
							"master",
							"-volumeSizeLimitMB=1000",
							"-ip=$(POD_NAME)",
							fmt.Sprintf("-peers=%s-0:9333,%s-1:9333,%s-2:9333", m.Name, m.Name, m.Name),
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
					}},
				},
			},
		},
	}
	// Set master instance as the owner and controller
	// ctrl.SetControllerReference(m, dep, r.Scheme)
	return dep
}
