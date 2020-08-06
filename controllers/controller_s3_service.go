package controllers

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createS3Service(m *seaweedv1.Seaweed) *corev1.Service {
	labels := labelsForS3(m.Name)

	dep := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-s3",
			Namespace: m.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:     "swfs-s3",
					Protocol: corev1.Protocol("TCP"),
					Port:     8333,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: 8333,
					},
				},
			},
			Selector: labels,
		},
	}
	return dep
}
