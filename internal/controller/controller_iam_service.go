package controller

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createIAMService(m *seaweedv1.Seaweed) *corev1.Service {
	labels := labelsForIAM(m.Name)

	iamPort := int32(seaweedv1.FilerIAMPort)
	if m.Spec.IAM.Port != nil {
		iamPort = *m.Spec.IAM.Port
	}

	ports := []corev1.ServicePort{
		{
			Name:       "iam-http",
			Protocol:   corev1.Protocol("TCP"),
			Port:       iamPort,
			TargetPort: intstr.FromInt(int(iamPort)),
		},
	}

	if m.Spec.IAM.MetricsPort != nil {
		ports = append(ports, corev1.ServicePort{
			Name:       "iam-metrics",
			Protocol:   corev1.Protocol("TCP"),
			Port:       *m.Spec.IAM.MetricsPort,
			TargetPort: intstr.FromInt(int(*m.Spec.IAM.MetricsPort)),
		})
	}

	dep := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-iam",
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

	if m.Spec.IAM.Service != nil {
		svcSpec := m.Spec.IAM.Service
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
