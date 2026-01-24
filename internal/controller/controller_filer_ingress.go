package controller

import (
	"fmt"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createAllIngress(m *seaweedv1.Seaweed) *networkingv1.Ingress {
	log := r.Log.With("sw-create-ingress", m.Name)
	labels := labelsForIngress(m.Name)
	pathType := networkingv1.PathTypePrefix

	dep := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-ingress",
			Namespace: m.Namespace,
			Labels:    labels,
		},
		Spec: networkingv1.IngressSpec{
			// TLS:   ingressSpec.TLS,
			Rules: []networkingv1.IngressRule{
				{
					Host: "filer." + *m.Spec.Ingress.HostSuffix,
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{
								{
									Path:     "/",
									PathType: &pathType,
									Backend: networkingv1.IngressBackend{
										Service: &networkingv1.IngressServiceBackend{
											Name: m.Name + "-filer",
											Port: networkingv1.ServiceBackendPort{
												Number: seaweedv1.FilerHTTPPort,
											},
										},
									},
								},
							},
						},
					},
				},
				{
					Host: "s3." + *m.Spec.Ingress.HostSuffix,
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{
								{
									Path:     "/",
									PathType: &pathType,
									Backend: networkingv1.IngressBackend{
										Service: &networkingv1.IngressServiceBackend{
											Name: m.Name + "-filer",
											Port: networkingv1.ServiceBackendPort{
												Number: seaweedv1.FilerS3Port,
											},
										},
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
	if m.Spec.Volume != nil {
		for i := 0; i < int(m.Spec.Volume.Replicas); i++ {
			dep.Spec.Rules = append(dep.Spec.Rules, networkingv1.IngressRule{
				Host: fmt.Sprintf("%s-volume-%d.%s", m.Name, i, *m.Spec.Ingress.HostSuffix),
				IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{
						Paths: []networkingv1.HTTPIngressPath{
							{
								Path:     "/",
								PathType: &pathType,
								Backend: networkingv1.IngressBackend{
									Service: &networkingv1.IngressServiceBackend{
										Name: fmt.Sprintf("%s-volume-%d", m.Name, i),
										Port: networkingv1.ServiceBackendPort{
											Number: seaweedv1.VolumeHTTPPort,
										},
									},
								},
							},
						},
					},
				},
			})
		}
	}

	// add ingress for volume topology servers
	for topologyName, topologySpec := range m.Spec.VolumeTopology {
		for i := 0; i < int(topologySpec.Replicas); i++ {
			dep.Spec.Rules = append(dep.Spec.Rules, networkingv1.IngressRule{
				Host: fmt.Sprintf("%s-volume-%s-%d.%s", m.Name, topologyName, i, *m.Spec.Ingress.HostSuffix),
				IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{
						Paths: []networkingv1.HTTPIngressPath{
							{
								Path:     "/",
								PathType: &pathType,
								Backend: networkingv1.IngressBackend{
									Service: &networkingv1.IngressServiceBackend{
										Name: fmt.Sprintf("%s-volume-%s-%d", m.Name, topologyName, i),
										Port: networkingv1.ServiceBackendPort{
											Number: seaweedv1.VolumeHTTPPort,
										},
									},
								},
							},
						},
					},
				},
			})
		}
	}

	// Set master instance as the owner and controller
	if err := ctrl.SetControllerReference(m, dep, r.Scheme); err != nil {
		log.Error(err, "set controller reference for Ingress failed")
	}
	return dep
}
