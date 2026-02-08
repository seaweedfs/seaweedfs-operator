package controller

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createMQBrokerPeerService(m *seaweedv1.Seaweed) *corev1.Service {
	labels := labelsForMQBroker(m.Name)
	port := int32(seaweedv1.MQBrokerGRPCPort)
	if m.Spec.MessageQueue.Broker.Port != nil {
		port = *m.Spec.MessageQueue.Broker.Port
	}

	ports := []corev1.ServicePort{
		{
			Name:       "mq-broker-grpc",
			Protocol:   corev1.ProtocolTCP,
			Port:       port,
			TargetPort: intstr.FromInt(int(port)),
		},
	}

	metricsPort := resolveMetricsPort(m, m.Spec.MessageQueue.Broker.MetricsPort)
	if metricsPort != nil {
		ports = append(ports, corev1.ServicePort{
			Name:       "mq-broker-metrics",
			Protocol:   corev1.ProtocolTCP,
			Port:       *metricsPort,
			TargetPort: intstr.FromInt(int(*metricsPort)),
		})
	}

	dep := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-mq-broker-peer",
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

func (r *SeaweedReconciler) createMQBrokerService(m *seaweedv1.Seaweed) *corev1.Service {
	labels := labelsForMQBroker(m.Name)

	port := int32(seaweedv1.MQBrokerGRPCPort)
	if m.Spec.MessageQueue.Broker.Port != nil {
		port = *m.Spec.MessageQueue.Broker.Port
	}

	ports := []corev1.ServicePort{
		{
			Name:       "mq-broker-grpc",
			Protocol:   corev1.ProtocolTCP,
			Port:       port,
			TargetPort: intstr.FromInt(int(port)),
		},
	}

	metricsPort := resolveMetricsPort(m, m.Spec.MessageQueue.Broker.MetricsPort)
	if metricsPort != nil {
		ports = append(ports, corev1.ServicePort{
			Name:       "mq-broker-metrics",
			Protocol:   corev1.ProtocolTCP,
			Port:       *metricsPort,
			TargetPort: intstr.FromInt(int(*metricsPort)),
		})
	}

	dep := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-mq-broker",
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

	if m.Spec.MessageQueue.Broker.Service != nil {
		svcSpec := m.Spec.MessageQueue.Broker.Service
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
