package controllers

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createFilerPeerService(m *seaweedv1.Seaweed) *corev1.Service {
	labels := labelsForFiler(m.Name)

	dep := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-filer-peer",
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
					Name:       "swfs-filer",
					Protocol:   corev1.Protocol("TCP"),
					Port:       seaweedv1.FilerHTTPPort,
					TargetPort: intstr.FromInt(seaweedv1.FilerHTTPPort),
				},
				{
					Name:       "swfs-filer-grpc",
					Protocol:   corev1.Protocol("TCP"),
					Port:       seaweedv1.FilerGRPCPort,
					TargetPort: intstr.FromInt(seaweedv1.FilerGRPCPort),
				},
				{
					Name:       "swfs-s3",
					Protocol:   corev1.Protocol("TCP"),
					Port:       seaweedv1.FilerS3Port,
					TargetPort: intstr.FromInt(seaweedv1.FilerS3Port),
				},
			},
			Selector: labels,
		},
	}
	return dep
}

func (r *SeaweedReconciler) createFilerService(m *seaweedv1.Seaweed) *corev1.Service {
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
			Type:                     corev1.ServiceTypeClusterIP,
			PublishNotReadyAddresses: true,
			Ports: []corev1.ServicePort{
				{
					Name:       "swfs-filer",
					Protocol:   corev1.Protocol("TCP"),
					Port:       seaweedv1.FilerHTTPPort,
					TargetPort: intstr.FromInt(seaweedv1.FilerHTTPPort),
				},
				{
					Name:       "swfs-filer-grpc",
					Protocol:   corev1.Protocol("TCP"),
					Port:       seaweedv1.FilerGRPCPort,
					TargetPort: intstr.FromInt(seaweedv1.FilerGRPCPort),
				},
				{
					Name:       "swfs-s3",
					Protocol:   corev1.Protocol("TCP"),
					Port:       seaweedv1.FilerS3Port,
					TargetPort: intstr.FromInt(seaweedv1.FilerS3Port),
				},
			},
			Selector: labels,
		},
	}

	if m.Spec.Filer.Service != nil {
		svcSpec := m.Spec.Filer.Service
		dep.Annotations = copyAnnotations(svcSpec.Annotations)

		if svcSpec.Type != "" {
			dep.Spec.Type = svcSpec.Type
		}

		if svcSpec.ClusterIP != nil {
			dep.Spec.ClusterIP = *svcSpec.ClusterIP
		}

		if svcSpec.LoadBalancerIP != nil {
			dep.Spec.LoadBalancerIP = *svcSpec.LoadBalancerIP
		}
	}
	return dep
}
