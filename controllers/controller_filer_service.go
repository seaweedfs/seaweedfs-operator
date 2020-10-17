package controllers

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createFilerHeadlessService(m *seaweedv1.Seaweed) *corev1.Service {
	labels := labelsForFiler(m.Name)

	dep := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-filer-headless",
			Namespace: m.Namespace,
			Labels:    labels,
			Annotations: map[string]string{
				"service.alpha.kubernetes.io/tolerate-unready-endpoints": "true",
			},
		},
		Spec: corev1.ServiceSpec{
			ClusterIP:                "None",
			PublishNotReadyAddresses: true,
			Ports: []corev1.ServicePort{
				{
					Name:     "swfs-filer",
					Protocol: corev1.Protocol("TCP"),
					Port:     8888,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: 8888,
					},
				},
				{
					Name:     "swfs-filer-grpc",
					Protocol: corev1.Protocol("TCP"),
					Port:     18888,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: 18888,
					},
				},
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

func (r *SeaweedReconciler) createFilerNodePortService(m *seaweedv1.Seaweed) *corev1.Service {
	labels := labelsForFiler(m.Name)

	dep := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-filer",
			Namespace: m.Namespace,
			Labels:    labels,
			Annotations: map[string]string{
				"service.alpha.kubernetes.io/tolerate-unready-endpoints": "true",
			},
		},
		Spec: corev1.ServiceSpec{
			Type:                     corev1.ServiceTypeNodePort,
			PublishNotReadyAddresses: true,
			Ports: []corev1.ServicePort{
				{
					Name:     "swfs-filer",
					Protocol: corev1.Protocol("TCP"),
					Port:     8888,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: 8888,
					},
					NodePort: 30888,
				},
				{
					Name:     "swfs-filer-grpc",
					Protocol: corev1.Protocol("TCP"),
					Port:     18888,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: 18888,
					},
					NodePort: 31888,
				},
				{
					Name:     "swfs-s3",
					Protocol: corev1.Protocol("TCP"),
					Port:     8333,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: 8333,
					},
					NodePort: 30833,
				},
			},
			Selector: labels,
		},
	}
	return dep
}
