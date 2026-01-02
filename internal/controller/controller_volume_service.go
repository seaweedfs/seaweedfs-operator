package controller

import (
	"fmt"

	"github.com/seaweedfs/seaweedfs-operator/internal/controller/label"
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

	metricsPort := resolveMetricsPort(m, m.Spec.Volume.MetricsPort)

	if metricsPort != nil {
		ports = append(ports, corev1.ServicePort{
			Name:       "volume-metrics",
			Protocol:   corev1.Protocol("TCP"),
			Port:       *metricsPort,
			TargetPort: intstr.FromInt(int(*metricsPort)),
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

	metricsPort := resolveMetricsPort(m, m.Spec.Volume.MetricsPort)

	if metricsPort != nil {
		ports = append(ports, corev1.ServicePort{
			Name:       "volume-metrics",
			Protocol:   corev1.Protocol("TCP"),
			Port:       *metricsPort,
			TargetPort: intstr.FromInt(int(*metricsPort)),
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

func (r *SeaweedReconciler) createVolumeServerTopologyPeerService(m *seaweedv1.Seaweed, topologyName string) *corev1.Service {
	labels := labelsForVolumeServerTopology(m.Name, topologyName)
	labels["seaweedfs/service-role"] = "peer"
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

	// Get metrics port from topology spec
	topologySpec := m.Spec.VolumeTopology[topologyName]
	if topologySpec.MetricsPort != nil {
		ports = append(ports, corev1.ServicePort{
			Name:       "volume-metrics",
			Protocol:   corev1.Protocol("TCP"),
			Port:       *topologySpec.MetricsPort,
			TargetPort: intstr.FromInt(int(*topologySpec.MetricsPort)),
		})
	}

	dep := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-volume-%s-peer", m.Name, topologyName),
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

func (r *SeaweedReconciler) createVolumeServerTopologyService(m *seaweedv1.Seaweed, topologyName string, i int) *corev1.Service {
	labels := labelsForVolumeServerTopology(m.Name, topologyName)
	serviceName := fmt.Sprintf("%s-volume-%s-%d", m.Name, topologyName, i)
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

	// Get metrics port from topology spec
	topologySpec := m.Spec.VolumeTopology[topologyName]
	if topologySpec.MetricsPort != nil {
		ports = append(ports, corev1.ServicePort{
			Name:       "volume-metrics",
			Protocol:   corev1.Protocol("TCP"),
			Port:       *topologySpec.MetricsPort,
			TargetPort: intstr.FromInt(int(*topologySpec.MetricsPort)),
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
			// Use StatefulSet pod name label to select the specific pod
			Selector: map[string]string{
				"statefulset.kubernetes.io/pod-name": serviceName,
			},
		},
	}

	// Apply service specification with fallback logic
	svcSpec := getServiceSpec(m, topologySpec)
	if svcSpec != nil {
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

// getServiceSpec returns the topology-specific service spec if present, otherwise returns the cluster-level service spec
func getServiceSpec(m *seaweedv1.Seaweed, topologySpec *seaweedv1.VolumeTopologySpec) *seaweedv1.ServiceSpec {
	if topologySpec != nil && topologySpec.Service != nil {
		return topologySpec.Service
	}
	if m.Spec.Volume != nil && m.Spec.Volume.Service != nil {
		return m.Spec.Volume.Service
	}
	return nil
}
