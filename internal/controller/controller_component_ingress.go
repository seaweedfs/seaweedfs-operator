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
	"context"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func isNotFoundErr(err error) bool { return apierrors.IsNotFound(err) }

// componentIngressManagedByLabel is the label value the operator stamps on
// every Ingress it reconciles under the per-component path. Used by the
// prune step to find Ingresses it is responsible for without touching the
// legacy HostSuffix all-in-one Ingress.
const componentIngressManagedByLabel = "seaweedfs-operator-component"

// ensureComponentIngresses reconciles one Ingress per component whose
// IngressSpec.Enabled is true, and deletes any previously managed Ingress
// that is no longer desired. Skipped entirely when no component opts in
// AND no managed Ingress currently exists (the list+prune still runs in
// that case to catch opt-out transitions).
func (r *SeaweedReconciler) ensureComponentIngresses(ctx context.Context, m *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	// Desired Ingress set: maps Ingress name → reconcile args.
	type desired struct {
		component   string
		serviceName string
		servicePort int32
		spec        *seaweedv1.IngressSpec
	}
	wanted := map[string]desired{}
	if m.Spec.Master != nil && m.Spec.Master.Ingress != nil && m.Spec.Master.Ingress.Enabled {
		wanted[m.Name+"-master-ingress"] = desired{"master", m.Name + "-master", seaweedv1.MasterHTTPPort, m.Spec.Master.Ingress}
	}
	if m.Spec.Volume != nil && m.Spec.Volume.Ingress != nil && m.Spec.Volume.Ingress.Enabled {
		wanted[m.Name+"-volume-ingress"] = desired{"volume", m.Name + "-volume", seaweedv1.VolumeHTTPPort, m.Spec.Volume.Ingress}
	}
	if m.Spec.Filer != nil && m.Spec.Filer.Ingress != nil && m.Spec.Filer.Ingress.Enabled {
		wanted[m.Name+"-filer-ingress"] = desired{"filer", m.Name + "-filer", seaweedv1.FilerHTTPPort, m.Spec.Filer.Ingress}
	}
	if m.Spec.Filer != nil && m.Spec.Filer.S3Ingress != nil && m.Spec.Filer.S3Ingress.Enabled &&
		m.Spec.Filer.S3 != nil && m.Spec.Filer.S3.Enabled {
		wanted[m.Name+"-s3-ingress"] = desired{"s3", m.Name + "-filer", seaweedv1.FilerS3Port, m.Spec.Filer.S3Ingress}
	}
	if m.Spec.Admin != nil && m.Spec.Admin.Ingress != nil && m.Spec.Admin.Ingress.Enabled {
		wanted[m.Name+"-admin-ingress"] = desired{"admin", m.Name + "-admin", seaweedv1.AdminHTTPPort, m.Spec.Admin.Ingress}
	}

	// Upsert the desired set.
	for _, d := range wanted {
		if done, res, err := r.ensureComponentIngress(m, d.component, d.serviceName, d.servicePort, d.spec); done {
			return done, res, err
		}
	}

	// Prune any previously managed Ingress that is no longer desired.
	// We scope the list to this CR instance + our managed-by marker so
	// the legacy HostSuffix Ingress (labelled component=ingress rather
	// than the per-component marker) is never touched.
	existing := &networkingv1.IngressList{}
	if err := r.List(ctx, existing,
		client.InNamespace(m.Namespace),
		client.MatchingLabels{
			"app.kubernetes.io/instance":   m.Name,
			"app.kubernetes.io/managed-by": componentIngressManagedByLabel,
		},
	); err != nil {
		return ReconcileResult(err)
	}
	for i := range existing.Items {
		ing := &existing.Items[i]
		if _, keep := wanted[ing.Name]; keep {
			continue
		}
		// Belt-and-braces: only delete if we own it, so an unrelated
		// Ingress that happens to have our labels (unlikely but cheap
		// to guard) is not accidentally reaped.
		if !isOwnedBy(ing.OwnerReferences, m.UID) {
			continue
		}
		r.Log.Info("pruning component ingress no longer in spec",
			"seaweed", m.Name, "ingress", ing.Name)
		if err := r.Delete(ctx, ing); err != nil && !isNotFoundErr(err) {
			return ReconcileResult(err)
		}
	}

	return ReconcileResult(nil)
}

// isOwnedBy reports whether any of the given owner references points at
// the controller UID. Mirrors the "is this object mine" check the prune
// loop needs without pulling in controller-runtime's ownership helpers.
func isOwnedBy(refs []metav1.OwnerReference, uid types.UID) bool {
	for _, r := range refs {
		if r.UID == uid && r.Controller != nil && *r.Controller {
			return true
		}
	}
	return false
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
	// The managed-by value is intentionally component-specific
	// (seaweedfs-operator-component) rather than the generic
	// "seaweedfs-operator" used elsewhere. This lets ensureComponentIngresses
	// scope its prune List to Ingresses this code path owns without
	// accidentally picking up the legacy HostSuffix all-in-one Ingress.
	labels := map[string]string{
		"app.kubernetes.io/managed-by": componentIngressManagedByLabel,
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
