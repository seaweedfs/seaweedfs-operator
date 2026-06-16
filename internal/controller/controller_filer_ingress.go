package controller

import (
	"fmt"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createAllIngress(m *seaweedv1.Seaweed) *networkingv1.Ingress {
	log := r.Log.WithValues("sw-create-ingress", m.Name)
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
					Host: "filer." + *m.Spec.HostSuffix,
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
			},
		},
	}

	// Route s3.<hostSuffix> to whichever S3 backend is enabled: the
	// standalone gateway Service (preferred) or, for the deprecated
	// embedded path, the filer Service. Skip the rule entirely when
	// neither is on so we never publish a host that resolves to a
	// Service port that does not exist.
	if s3Svc, s3Port, ok := s3IngressBackend(m); ok {
		dep.Spec.Rules = append(dep.Spec.Rules, networkingv1.IngressRule{
			Host: "s3." + *m.Spec.HostSuffix,
			IngressRuleValue: networkingv1.IngressRuleValue{
				HTTP: &networkingv1.HTTPIngressRuleValue{
					Paths: []networkingv1.HTTPIngressPath{
						{
							Path:     "/",
							PathType: &pathType,
							Backend: networkingv1.IngressBackend{
								Service: &networkingv1.IngressServiceBackend{
									Name: s3Svc,
									Port: networkingv1.ServiceBackendPort{
										Number: s3Port,
									},
								},
							},
						},
					},
				},
			},
		})
	}

	// add ingress for volume servers
	for i := 0; i < int(m.Spec.Volume.Replicas); i++ {
		dep.Spec.Rules = append(dep.Spec.Rules, networkingv1.IngressRule{
			Host: fmt.Sprintf("%s-volume-%d.%s", m.Name, i, *m.Spec.HostSuffix),
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

	// Set master instance as the owner and controller
	if err := ctrl.SetControllerReference(m, dep, r.Scheme); err != nil {
		log.Error(err, "set controller reference for Ingress failed")
	}
	return dep
}

// s3IngressBackend returns the Service name and port the all-in-one
// HostSuffix Ingress should route the s3.<suffix> host to, plus whether any
// S3 path is enabled at all. The standalone gateway (Spec.S3) takes
// precedence over the deprecated embedded filer S3 (Spec.Filer.S3).
func s3IngressBackend(m *seaweedv1.Seaweed) (string, int32, bool) {
	if m.Spec.S3 != nil {
		return m.Name + "-s3", s3EffectivePort(m), true
	}
	if m.Spec.Filer != nil && m.Spec.Filer.S3 != nil && m.Spec.Filer.S3.Enabled {
		return m.Name + "-filer", seaweedv1.FilerS3Port, true
	}
	return "", 0, false
}
