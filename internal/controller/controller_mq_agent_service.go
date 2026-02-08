package controller

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createMQAgentService(m *seaweedv1.Seaweed) *corev1.Service {
	labels := labelsForMQAgent(m.Name)

	port := int32(seaweedv1.MQAgentGRPCPort)
	if m.Spec.MessageQueue.Agent.Port != nil {
		port = *m.Spec.MessageQueue.Agent.Port
	}

	ports := []corev1.ServicePort{
		{
			Name:       "mq-agent-grpc",
			Protocol:   corev1.ProtocolTCP,
			Port:       port,
			TargetPort: intstr.FromInt(int(port)),
		},
	}

	metricsPort := resolveMetricsPort(m, m.Spec.MessageQueue.Agent.MetricsPort)
	if metricsPort != nil {
		ports = append(ports, corev1.ServicePort{
			Name:       "mq-agent-metrics",
			Protocol:   corev1.ProtocolTCP,
			Port:       *metricsPort,
			TargetPort: intstr.FromInt(int(*metricsPort)),
		})
	}

	dep := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-mq-agent",
			Namespace: m.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Ports:    ports,
			Selector: labels,
		},
	}

	if m.Spec.MessageQueue.Agent.Service != nil {
		svcSpec := m.Spec.MessageQueue.Agent.Service
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
