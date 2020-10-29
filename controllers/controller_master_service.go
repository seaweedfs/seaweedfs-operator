package controllers

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createMasterPeerService(m *seaweedv1.Seaweed) *corev1.Service {
	labels := labelsForMaster(m.Name)

	dep := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-master-peer",
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
					Name:       "swfs-master",
					Protocol:   corev1.Protocol("TCP"),
					Port:       seaweedv1.MasterHTTPPort,
					TargetPort: intstr.FromInt(seaweedv1.MasterHTTPPort),
				},
				{
					Name:       "swfs-master-grpc",
					Protocol:   corev1.Protocol("TCP"),
					Port:       seaweedv1.MasterGRPCPort,
					TargetPort: intstr.FromInt(seaweedv1.MasterGRPCPort),
				},
			},
			Selector: labels,
		},
	}
	// Set master instance as the owner and controller
	// ctrl.SetControllerReference(m, dep, r.Scheme)
	return dep
}

func (r *SeaweedReconciler) createMasterService(m *seaweedv1.Seaweed) *corev1.Service {
	labels := labelsForMaster(m.Name)

	dep := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-master",
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
					Name:       "swfs-master",
					Protocol:   corev1.Protocol("TCP"),
					Port:       seaweedv1.MasterHTTPPort,
					TargetPort: intstr.FromInt(seaweedv1.MasterHTTPPort),
				},
				{
					Name:       "swfs-master-grpc",
					Protocol:   corev1.Protocol("TCP"),
					Port:       seaweedv1.MasterGRPCPort,
					TargetPort: intstr.FromInt(seaweedv1.MasterGRPCPort),
				},
			},
			Selector: labels,
		},
	}

	return dep
}
