package controller

import (
	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func (r *SeaweedReconciler) createAdminService(m *seaweedv1.Seaweed) *corev1.Service {
	labels := labelsForAdmin(m.Name)
	port := seaweedv1.AdminHTTPPort
	if m.Spec.Admin.Port != nil {
		port = int(*m.Spec.Admin.Port)
	}

	ports := []corev1.ServicePort{
		{
			Name:       "admin-http",
			Protocol:   corev1.Protocol("TCP"),
			Port:       int32(port),
			TargetPort: intstr.FromInt(port),
		},
	}

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-admin",
			Namespace: m.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Ports:    ports,
			Selector: labels,
		},
	}

	// Apply service spec if provided
	if m.Spec.Admin.Service != nil {
		if m.Spec.Admin.Service.Type != "" {
			service.Spec.Type = m.Spec.Admin.Service.Type
		}
		if m.Spec.Admin.Service.Annotations != nil {
			service.ObjectMeta.Annotations = m.Spec.Admin.Service.Annotations
		}
		if m.Spec.Admin.Service.LoadBalancerIP != nil {
			service.Spec.LoadBalancerIP = *m.Spec.Admin.Service.LoadBalancerIP
		}
		if m.Spec.Admin.Service.ClusterIP != nil {
			service.Spec.ClusterIP = *m.Spec.Admin.Service.ClusterIP
		}
	}

	return service
}
