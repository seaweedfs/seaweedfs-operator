package controller

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createAdminPeerService(m *seaweedv1.Seaweed) *corev1.Service {
	labels := labelsForAdmin(m.Name)
	ports := []corev1.ServicePort{
		{
			Name:       "admin-http",
			Protocol:   corev1.Protocol("TCP"),
			Port:       seaweedv1.AdminHTTPPort,
			TargetPort: intstr.FromInt(seaweedv1.AdminHTTPPort),
		},
		{
			Name:       "admin-grpc",
			Protocol:   corev1.Protocol("TCP"),
			Port:       seaweedv1.AdminGRPCPort,
			TargetPort: intstr.FromInt(seaweedv1.AdminGRPCPort),
		},
	}
	if m.Spec.Admin.MetricsPort != nil {
		ports = append(ports, corev1.ServicePort{
			Name:       "admin-metrics",
			Protocol:   corev1.Protocol("TCP"),
			Port:       *m.Spec.Admin.MetricsPort,
			TargetPort: intstr.FromInt(int(*m.Spec.Admin.MetricsPort)),
		})
	}

	dep := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-admin-peer",
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

func (r *SeaweedReconciler) createAdminService(m *seaweedv1.Seaweed) *corev1.Service {
	labels := labelsForAdmin(m.Name)
	ports := []corev1.ServicePort{
		{
			Name:       "admin-http",
			Protocol:   corev1.Protocol("TCP"),
			Port:       seaweedv1.AdminHTTPPort,
			TargetPort: intstr.FromInt(seaweedv1.AdminHTTPPort),
		},
		{
			Name:       "admin-grpc",
			Protocol:   corev1.Protocol("TCP"),
			Port:       seaweedv1.AdminGRPCPort,
			TargetPort: intstr.FromInt(seaweedv1.AdminGRPCPort),
		},
	}
	if m.Spec.Admin.MetricsPort != nil {
		ports = append(ports, corev1.ServicePort{
			Name:       "admin-metrics",
			Protocol:   corev1.Protocol("TCP"),
			Port:       *m.Spec.Admin.MetricsPort,
			TargetPort: intstr.FromInt(int(*m.Spec.Admin.MetricsPort)),
		})
	}

	dep := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-admin",
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

	if m.Spec.Admin.Service != nil {
		svcSpec := m.Spec.Admin.Service
		for k, v := range svcSpec.Annotations {
			dep.Annotations[k] = v
		}

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
