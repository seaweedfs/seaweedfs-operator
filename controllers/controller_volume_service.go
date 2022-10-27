package controllers

import (
	"fmt"

	"github.com/seaweedfs/seaweedfs-operator/controllers/label"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createVolumeServerPeerService(m *seaweedv1.Seaweed) *corev1.Service {
	labels := labelsForVolumeServer(m.Name)
	ports := []corev1.ServicePort{
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
	}
	if m.Spec.Volume.MetricsPort != nil {
		ports = append(ports, corev1.ServicePort{
			Name:       "volume-metrics",
			Protocol:   corev1.Protocol("TCP"),
			Port:       *m.Spec.Volume.MetricsPort,
			TargetPort: intstr.FromInt(int(*m.Spec.Volume.MetricsPort)),
		})
	}

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
			Ports:                    ports,
			Selector:                 labels,
		},
	}
	return dep
}
func (r *SeaweedReconciler) createVolumeServerService(m *seaweedv1.Seaweed, i int) *corev1.Service {
	labels := labelsForVolumeServer(m.Name)
	serviceName := fmt.Sprintf("%s-volume-%d", m.Name, i)
	labels[label.PodName] = serviceName
	ports := []corev1.ServicePort{
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
	}
	if m.Spec.Volume.MetricsPort != nil {
		ports = append(ports, corev1.ServicePort{
			Name:       "volume-metrics",
			Protocol:   corev1.Protocol("TCP"),
			Port:       *m.Spec.Volume.MetricsPort,
			TargetPort: intstr.FromInt(int(*m.Spec.Volume.MetricsPort)),
		})
	}

	dep := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: m.Namespace,
			Labels:    labels,
			Annotations: map[string]string{
				"service.alpha.kubernetes.io/tolerate-unready-endpoints": "true",
			},
		},
		Spec: corev1.ServiceSpec{
			PublishNotReadyAddresses: true,
			Ports:                    ports,
			Selector:                 labels,
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
