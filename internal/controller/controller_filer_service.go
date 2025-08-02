package controller

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createFilerPeerService(m *seaweedv1.Seaweed) *corev1.Service {
	labels := labelsForFiler(m.Name)
	ports := []corev1.ServicePort{
		{
			Name:       "filer-http",
			Protocol:   corev1.Protocol("TCP"),
			Port:       seaweedv1.FilerHTTPPort,
			TargetPort: intstr.FromInt(seaweedv1.FilerHTTPPort),
		},
		{
			Name:       "filer-grpc",
			Protocol:   corev1.Protocol("TCP"),
			Port:       seaweedv1.FilerGRPCPort,
			TargetPort: intstr.FromInt(seaweedv1.FilerGRPCPort),
		},
	}
	if m.Spec.Filer.S3 != nil && m.Spec.Filer.S3.Enabled {
		ports = append(ports, corev1.ServicePort{
			Name:       "filer-s3",
			Protocol:   corev1.Protocol("TCP"),
			Port:       seaweedv1.FilerS3Port,
			TargetPort: intstr.FromInt(seaweedv1.FilerS3Port),
		})
	}
	if m.Spec.Filer.IAM {
		iamPort := int32(seaweedv1.FilerIAMPort)
		if m.Spec.IAM != nil && m.Spec.IAM.Port != nil {
			iamPort = *m.Spec.IAM.Port
		}
		ports = append(ports, corev1.ServicePort{
			Name:       "filer-iam",
			Protocol:   corev1.Protocol("TCP"),
			Port:       iamPort,
			TargetPort: intstr.FromInt(int(iamPort)),
		})
	}
	if m.Spec.Filer.MetricsPort != nil {
		ports = append(ports, corev1.ServicePort{
			Name:       "filer-metrics",
			Protocol:   corev1.Protocol("TCP"),
			Port:       *m.Spec.Filer.MetricsPort,
			TargetPort: intstr.FromInt(int(*m.Spec.Filer.MetricsPort)),
		})
	}

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
			Ports:                    ports,
			Selector:                 labels,
		},
	}
	return dep
}

func (r *SeaweedReconciler) createFilerService(m *seaweedv1.Seaweed) *corev1.Service {
	labels := labelsForFiler(m.Name)
	ports := []corev1.ServicePort{
		{
			Name:       "filer-http",
			Protocol:   corev1.Protocol("TCP"),
			Port:       seaweedv1.FilerHTTPPort,
			TargetPort: intstr.FromInt(seaweedv1.FilerHTTPPort),
		},
		{
			Name:       "filer-grpc",
			Protocol:   corev1.Protocol("TCP"),
			Port:       seaweedv1.FilerGRPCPort,
			TargetPort: intstr.FromInt(seaweedv1.FilerGRPCPort),
		},
	}
	if m.Spec.Filer.S3 != nil && m.Spec.Filer.S3.Enabled {
		ports = append(ports, corev1.ServicePort{
			Name:       "filer-s3",
			Protocol:   corev1.Protocol("TCP"),
			Port:       seaweedv1.FilerS3Port,
			TargetPort: intstr.FromInt(seaweedv1.FilerS3Port),
		})
	}
	if m.Spec.Filer.IAM {
		iamPort := int32(seaweedv1.FilerIAMPort)
		if m.Spec.IAM != nil && m.Spec.IAM.Port != nil {
			iamPort = *m.Spec.IAM.Port
		}
		ports = append(ports, corev1.ServicePort{
			Name:       "filer-iam",
			Protocol:   corev1.Protocol("TCP"),
			Port:       iamPort,
			TargetPort: intstr.FromInt(int(iamPort)),
		})
	}
	if m.Spec.Filer.MetricsPort != nil {
		ports = append(ports, corev1.ServicePort{
			Name:       "filer-metrics",
			Protocol:   corev1.Protocol("TCP"),
			Port:       *m.Spec.Filer.MetricsPort,
			TargetPort: intstr.FromInt(int(*m.Spec.Filer.MetricsPort)),
		})
	}

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
			Ports:                    ports,
			Selector:                 labels,
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
