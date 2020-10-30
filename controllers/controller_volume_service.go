package controllers

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createVolumeServerPeerService(m *seaweedv1.Seaweed) *corev1.Service {
	labels := labelsForVolumeServer(m.Name)

	dep := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-volume-peer",
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
					Name:       "volume-http",
					Protocol:   corev1.Protocol("TCP"),
					Port:       seaweedv1.VolumeHTTPPort,
					TargetPort: intstr.FromInt(seaweedv1.VolumeHTTPPort),
				},
				{
					Name:       "volume-grpc",
					Protocol:   corev1.Protocol("TCP"),
					Port:       seaweedv1.VolumeGRPCPort,
					TargetPort: intstr.FromInt(seaweedv1.VolumeGRPCPort),
				},
			},
			Selector: labels,
		},
	}
	return dep
}
func (r *SeaweedReconciler) createVolumeServerService(m *seaweedv1.Seaweed) *corev1.Service {
	labels := labelsForVolumeServer(m.Name)

	dep := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-volume",
			Namespace: m.Namespace,
			Labels:    labels,
			Annotations: map[string]string{
				"service.alpha.kubernetes.io/tolerate-unready-endpoints": "true",
			},
		},
		Spec: corev1.ServiceSpec{
			PublishNotReadyAddresses: true,
			Ports: []corev1.ServicePort{
				{
					Name:       "volume-http",
					Protocol:   corev1.Protocol("TCP"),
					Port:       seaweedv1.VolumeHTTPPort,
					TargetPort: intstr.FromInt(seaweedv1.VolumeHTTPPort),
				},
				{
					Name:       "volume-grpc",
					Protocol:   corev1.Protocol("TCP"),
					Port:       seaweedv1.VolumeGRPCPort,
					TargetPort: intstr.FromInt(seaweedv1.VolumeGRPCPort),
				},
			},
			Selector: labels,
		},
	}

	if m.Spec.Volume.Service != nil {
		svcSpec := m.Spec.Volume.Service
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
