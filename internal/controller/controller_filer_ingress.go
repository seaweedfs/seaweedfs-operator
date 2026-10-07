package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/internal/controller/label"
)

func (r *SeaweedReconciler) createAllIngress(ctx context.Context, m *seaweedv1.Seaweed) (*networkingv1.Ingress, error) {
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
			// The legacy HostSuffix Ingress is HTTP-only. For TLS, use the
			// per-component ingress blocks (e.g. filer.s3Ingress.tls).
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

	// Add one rule per live per-replica volume Service: flat servers publish
	// <name>-volume-<i>, topology groups <name>-volume-<group>-<i> — the same
	// names the pods advertise as -publicUrl under HostSuffix. Rules follow
	// the Services rather than the spec's replica counts so a pod held for
	// evacuation keeps its host while it serves and a removed replica loses
	// it as soon as its Service is gone. A list failure surfaces as an error
	// instead of silently dropping the hosts for one pass.
	volumeRule := func(serviceName string) networkingv1.IngressRule {
		return networkingv1.IngressRule{
			Host: serviceName + "." + *m.Spec.HostSuffix,
			IngressRuleValue: networkingv1.IngressRuleValue{
				HTTP: &networkingv1.HTTPIngressRuleValue{
					Paths: []networkingv1.HTTPIngressPath{
						{
							Path:     "/",
							PathType: &pathType,
							Backend: networkingv1.IngressBackend{
								Service: &networkingv1.IngressServiceBackend{
									Name: serviceName,
									Port: networkingv1.ServiceBackendPort{
										Number: seaweedv1.VolumeHTTPPort,
									},
								},
							},
						},
					},
				},
			},
		}
	}
	volumeServices := &corev1.ServiceList{}
	if err := r.List(ctx, volumeServices,
		client.InNamespace(m.Namespace),
		client.MatchingLabels{
			label.ManagedByLabelKey: "seaweedfs-operator",
			label.ComponentLabelKey: "volume",
			label.InstanceLabelKey:  m.Name,
		},
	); err != nil {
		return nil, fmt.Errorf("list volume services for ingress rules: %w", err)
	}
	var serviceNames []string
	for i := range volumeServices.Items {
		svc := &volumeServices.Items[i]
		if !isOwnedBy(svc.OwnerReferences, m.UID) {
			continue
		}
		if !strings.HasPrefix(svc.Name, m.Name+"-volume-") || strings.HasSuffix(svc.Name, "-peer") {
			continue
		}
		serviceNames = append(serviceNames, svc.Name)
	}
	// Deterministic rule order so IngressEqual does not see a spurious diff.
	sort.Strings(serviceNames)
	for _, serviceName := range serviceNames {
		dep.Spec.Rules = append(dep.Spec.Rules, volumeRule(serviceName))
	}

	// Set master instance as the owner and controller
	if err := ctrl.SetControllerReference(m, dep, r.Scheme); err != nil {
		log.Error(err, "set controller reference for Ingress failed")
	}
	return dep, nil
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
