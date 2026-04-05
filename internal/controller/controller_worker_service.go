package controller

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createWorkerPeerService(m *seaweedv1.Seaweed) *corev1.Service {
	labels := labelsForWorker(m.Name)
	var ports []corev1.ServicePort
	if m.Spec.Worker.MetricsPort != nil {
		ports = append(ports, corev1.ServicePort{
			Name:       "worker-metrics",
			Protocol:   corev1.Protocol("TCP"),
			Port:       *m.Spec.Worker.MetricsPort,
			TargetPort: intstr.FromInt(int(*m.Spec.Worker.MetricsPort)),
		})
	}

	dep := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-worker-peer",
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

func (r *SeaweedReconciler) createWorkerService(m *seaweedv1.Seaweed) *corev1.Service {
	labels := labelsForWorker(m.Name)
	var ports []corev1.ServicePort
	if m.Spec.Worker.MetricsPort != nil {
		ports = append(ports, corev1.ServicePort{
			Name:       "worker-metrics",
			Protocol:   corev1.Protocol("TCP"),
			Port:       *m.Spec.Worker.MetricsPort,
			TargetPort: intstr.FromInt(int(*m.Spec.Worker.MetricsPort)),
		})
	}

	dep := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-worker",
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
	return dep
}
