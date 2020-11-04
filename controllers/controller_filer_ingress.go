package controllers

import (
	extensionsv1beta1 "k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createFilerIngress(m *seaweedv1.Seaweed) *extensionsv1beta1.Ingress {
	labels := labelsForIngress(m.Name)

	dep := &extensionsv1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-ingress",
			Namespace: m.Namespace,
			Labels:    labels,
		},
		Spec: extensionsv1beta1.IngressSpec{
			// TLS:   ingressSpec.TLS,
			Rules: []extensionsv1beta1.IngressRule{
				{
					Host: "filer." + *m.Spec.Filer.HostSuffix,
					IngressRuleValue: extensionsv1beta1.IngressRuleValue{
						HTTP: &extensionsv1beta1.HTTPIngressRuleValue{
							Paths: []extensionsv1beta1.HTTPIngressPath{
								{
									Path: "/",
									Backend: extensionsv1beta1.IngressBackend{
										ServiceName: m.Name + "-filer",
										ServicePort: intstr.FromInt(seaweedv1.FilerHTTPPort),
									},
								},
							},
						},
					},
				},
				{
					Host: "s3." + *m.Spec.Filer.HostSuffix,
					IngressRuleValue: extensionsv1beta1.IngressRuleValue{
						HTTP: &extensionsv1beta1.HTTPIngressRuleValue{
							Paths: []extensionsv1beta1.HTTPIngressPath{
								{
									Path: "/",
									Backend: extensionsv1beta1.IngressBackend{
										ServiceName: m.Name + "-s3",
										ServicePort: intstr.FromInt(seaweedv1.FilerS3Port),
									},
								},
							},
						},
					},
				},
			},
		},
	}

	// Set master instance as the owner and controller
	ctrl.SetControllerReference(m, dep, r.Scheme)
	return dep
}
