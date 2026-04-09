/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Per-component Ingress reconciliation.
//
// The legacy HostSuffix-driven "one big Ingress for everything" path in
// controller_ingress.go is left untouched for backward compatibility.
// This file adds a separate, opt-in path: each component (master, volume,
// filer, filer-S3, admin) can independently declare an IngressSpec and
// the operator will emit a dedicated Ingress pointing at its Service.
//
// Reasons to prefer per-component Ingress over HostSuffix:
//   - different hostnames or TLS secrets per component
//   - controller-specific annotations (nginx/haproxy/traefik) without
//     every component inheriting them
//   - components HostSuffix never covered (master HTTP UI, admin UI,
//     Iceberg REST) get first-class support
package controller

import (
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// ensureComponentIngresses reconciles one Ingress per component whose
// IngressSpec.Enabled is true. Skipped entirely when nothing opts in.
func (r *SeaweedReconciler) ensureComponentIngresses(m *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	if m.Spec.Master != nil && m.Spec.Master.Ingress != nil && m.Spec.Master.Ingress.Enabled {
		if done, res, err := r.ensureComponentIngress(m, "master", m.Name+"-master",
			seaweedv1.MasterHTTPPort, m.Spec.Master.Ingress); done {
			return done, res, err
		}
	}
	if m.Spec.Volume != nil && m.Spec.Volume.Ingress != nil && m.Spec.Volume.Ingress.Enabled {
		if done, res, err := r.ensureComponentIngress(m, "volume", m.Name+"-volume",
			seaweedv1.VolumeHTTPPort, m.Spec.Volume.Ingress); done {
			return done, res, err
		}
	}
	if m.Spec.Filer != nil && m.Spec.Filer.Ingress != nil && m.Spec.Filer.Ingress.Enabled {
		if done, res, err := r.ensureComponentIngress(m, "filer", m.Name+"-filer",
			seaweedv1.FilerHTTPPort, m.Spec.Filer.Ingress); done {
			return done, res, err
		}
	}
	if m.Spec.Filer != nil && m.Spec.Filer.S3Ingress != nil && m.Spec.Filer.S3Ingress.Enabled &&
		m.Spec.Filer.S3 != nil && m.Spec.Filer.S3.Enabled {
		if done, res, err := r.ensureComponentIngress(m, "s3", m.Name+"-filer",
			seaweedv1.FilerS3Port, m.Spec.Filer.S3Ingress); done {
			return done, res, err
		}
	}
	if m.Spec.Admin != nil && m.Spec.Admin.Ingress != nil && m.Spec.Admin.Ingress.Enabled {
		if done, res, err := r.ensureComponentIngress(m, "admin", m.Name+"-admin",
			seaweedv1.AdminHTTPPort, m.Spec.Admin.Ingress); done {
			return done, res, err
		}
	}
	return ReconcileResult(nil)
}

func (r *SeaweedReconciler) ensureComponentIngress(m *seaweedv1.Seaweed, component, serviceName string, servicePort int32, spec *seaweedv1.IngressSpec) (bool, ctrl.Result, error) {
	log := r.Log.WithValues("sw-component-ingress", m.Name, "component", component)
	ingress := buildComponentIngress(m, component, serviceName, servicePort, spec)
	if err := controllerutil.SetControllerReference(m, ingress, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateIngress(ingress)
	log.Info("ensure component ingress " + ingress.Name)
	return ReconcileResult(err)
}

func buildComponentIngress(m *seaweedv1.Seaweed, component, serviceName string, servicePort int32, spec *seaweedv1.IngressSpec) *networkingv1.Ingress {
	pathType := networkingv1.PathTypePrefix
	path := spec.Path
	if path == "" {
		path = "/"
	}
	labels := map[string]string{
		"app.kubernetes.io/managed-by": "seaweedfs-operator",
		"app.kubernetes.io/name":       "seaweedfs",
		"app.kubernetes.io/instance":   m.Name,
		"app.kubernetes.io/component":  component,
	}
	rule := networkingv1.IngressRule{
		Host: spec.Host,
		IngressRuleValue: networkingv1.IngressRuleValue{
			HTTP: &networkingv1.HTTPIngressRuleValue{
				Paths: []networkingv1.HTTPIngressPath{{
					Path:     path,
					PathType: &pathType,
					Backend: networkingv1.IngressBackend{
						Service: &networkingv1.IngressServiceBackend{
							Name: serviceName,
							Port: networkingv1.ServiceBackendPort{Number: servicePort},
						},
					},
				}},
			},
		},
	}

	var tls []networkingv1.IngressTLS
	for _, t := range spec.TLS {
		tls = append(tls, networkingv1.IngressTLS{
			Hosts:      t.Hosts,
			SecretName: t.SecretName,
		})
	}

	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:        m.Name + "-" + component + "-ingress",
			Namespace:   m.Namespace,
			Labels:      labels,
			Annotations: spec.Annotations,
		},
		Spec: networkingv1.IngressSpec{
			IngressClassName: spec.ClassName,
			Rules:            []networkingv1.IngressRule{rule},
			TLS:              tls,
		},
	}
}
