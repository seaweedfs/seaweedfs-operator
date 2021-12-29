package controllers

import (
	"fmt"

	extensionsv1beta1 "k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createAllIngress(m *seaweedv1.Seaweed) *extensionsv1beta1.Ingress {
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
					Host: "filer." + *m.Spec.HostSuffix,
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
					Host: "s3." + *m.Spec.HostSuffix,
					IngressRuleValue: extensionsv1beta1.IngressRuleValue{
						HTTP: &extensionsv1beta1.HTTPIngressRuleValue{
							Paths: []extensionsv1beta1.HTTPIngressPath{
								{
									Path: "/",
									Backend: extensionsv1beta1.IngressBackend{
										ServiceName: m.Name + "-filer",
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

	// add ingress for volume servers
	for i := 0; i < int(m.Spec.Volume.Replicas); i++ {
		dep.Spec.Rules = append(dep.Spec.Rules, extensionsv1beta1.IngressRule{
			Host: fmt.Sprintf("%s-volume-%d.%s", m.Name, i, *m.Spec.HostSuffix),
			IngressRuleValue: extensionsv1beta1.IngressRuleValue{
				HTTP: &extensionsv1beta1.HTTPIngressRuleValue{
					Paths: []extensionsv1beta1.HTTPIngressPath{
						{
							Path: "/",
							Backend: extensionsv1beta1.IngressBackend{
								ServiceName: fmt.Sprintf("%s-volume-%d", m.Name, i),
								ServicePort: intstr.FromInt(seaweedv1.VolumeHTTPPort),
							},
						},
					},
				},
			},
		})
	}

	// Set master instance as the owner and controller
	_ = ctrl.SetControllerReference(m, dep, r.Scheme)
	return dep
}
