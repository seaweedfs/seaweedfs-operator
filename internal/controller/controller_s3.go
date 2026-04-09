/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Standalone S3 gateway reconciliation.
//
// The gateway is a stateless Deployment (not a StatefulSet — it has no
// per-pod identity requirements), in front of which sits a single
// Service. It connects to the filer via the filer's headless peer
// Service, so the filer must be enabled in the same Seaweed CR.
//
// This is the preferred way to expose S3. The older FilerSpec.S3 path
// (embedded S3 inside every filer pod) is retained for backward
// compatibility but is deprecated. When both are set the webhook
// rejects the CR.
package controller

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/internal/controller/label"
)

const (
	s3Component = "s3"
)

func labelsForS3(name string) map[string]string {
	return map[string]string{
		label.ManagedByLabelKey: "seaweedfs-operator",
		label.NameLabelKey:      "seaweedfs",
		label.ComponentLabelKey: s3Component,
		label.InstanceLabelKey:  name,
	}
}

// s3EffectivePort returns the port the S3 gateway should listen on.
func s3EffectivePort(m *seaweedv1.Seaweed) int32 {
	if m.Spec.S3 != nil && m.Spec.S3.Port != nil {
		return *m.Spec.S3.Port
	}
	return seaweedv1.FilerS3Port // 8333
}

func (r *SeaweedReconciler) ensureS3Gateway(ctx context.Context, m *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	if m.Spec.S3 == nil {
		return ReconcileResult(nil)
	}
	// The standalone gateway dials the filer via its peer Service, so
	// requiring a filer in the same CR keeps us from shipping a dangling
	// Deployment that can never come up.
	if m.Spec.Filer == nil {
		r.Log.Info("SeaweedSpec.S3 set but Filer is nil; skipping standalone S3 reconciliation",
			"seaweed", m.Name)
		return ReconcileResult(nil)
	}

	if done, res, err := r.ensureS3Service(m); done {
		return done, res, err
	}
	if done, res, err := r.ensureS3Deployment(ctx, m); done {
		return done, res, err
	}
	if m.Spec.S3.MetricsPort != nil {
		if done, res, err := r.ensureS3ServiceMonitor(m); done {
			return done, res, err
		}
	}
	if m.Spec.S3.Ingress != nil && m.Spec.S3.Ingress.Enabled {
		if done, res, err := r.ensureComponentIngress(m, s3Component, m.Name+"-s3",
			s3EffectivePort(m), m.Spec.S3.Ingress); done {
			return done, res, err
		}
	}
	return ReconcileResult(nil)
}

func (r *SeaweedReconciler) ensureS3Service(m *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	svc := r.buildS3Service(m)
	if err := controllerutil.SetControllerReference(m, svc, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateService(svc)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) buildS3Service(m *seaweedv1.Seaweed) *corev1.Service {
	labels := labelsForS3(m.Name)
	ports := []corev1.ServicePort{{
		Name:       "s3-http",
		Port:       s3EffectivePort(m),
		TargetPort: intstr.FromInt(int(s3EffectivePort(m))),
		Protocol:   corev1.ProtocolTCP,
	}}
	if m.Spec.S3.MetricsPort != nil {
		ports = append(ports, corev1.ServicePort{
			Name:       "s3-metrics",
			Port:       *m.Spec.S3.MetricsPort,
			TargetPort: intstr.FromInt(int(*m.Spec.S3.MetricsPort)),
			Protocol:   corev1.ProtocolTCP,
		})
	}
	svcType := corev1.ServiceTypeClusterIP
	var annotations map[string]string
	var clusterIP string
	var lbIP string
	if m.Spec.S3.Service != nil {
		if m.Spec.S3.Service.Type != "" {
			svcType = m.Spec.S3.Service.Type
		}
		annotations = m.Spec.S3.Service.Annotations
		if m.Spec.S3.Service.ClusterIP != nil {
			clusterIP = *m.Spec.S3.Service.ClusterIP
		}
		if m.Spec.S3.Service.LoadBalancerIP != nil {
			lbIP = *m.Spec.S3.Service.LoadBalancerIP
		}
	}
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        m.Name + "-s3",
			Namespace:   m.Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: corev1.ServiceSpec{
			Type:           svcType,
			Selector:       labels,
			Ports:          ports,
			ClusterIP:      clusterIP,
			LoadBalancerIP: lbIP,
		},
	}
}

func (r *SeaweedReconciler) ensureS3Deployment(ctx context.Context, m *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	dep := r.buildS3Deployment(m)
	if err := controllerutil.SetControllerReference(m, dep, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateDeployment(dep)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) buildS3Deployment(m *seaweedv1.Seaweed) *appsv1.Deployment {
	labels := labelsForS3(m.Name)
	replicas := m.Spec.S3.Replicas

	podSpec := m.BaseS3Spec().BuildPodSpec()
	var volumeMounts []corev1.VolumeMount
	if m.Spec.S3.ConfigSecret != nil && m.Spec.S3.ConfigSecret.Name != "" {
		podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
			Name: "s3-config",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: m.Spec.S3.ConfigSecret.Name,
				},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "s3-config",
			ReadOnly:  true,
			MountPath: "/etc/sw",
		})
	}
	if tlsVols, tlsMounts := tlsVolumesAndMounts(m); len(tlsVols) > 0 {
		podSpec.Volumes = append(podSpec.Volumes, tlsVols...)
		volumeMounts = append(volumeMounts, tlsMounts...)
	}

	ports := []corev1.ContainerPort{{
		Name:          "s3-http",
		ContainerPort: s3EffectivePort(m),
	}}
	if m.Spec.S3.MetricsPort != nil {
		ports = append(ports, corev1.ContainerPort{
			Name:          "s3-metrics",
			ContainerPort: *m.Spec.S3.MetricsPort,
		})
	}

	podSpec.Containers = []corev1.Container{{
		Name:            "s3",
		Image:           m.Spec.Image,
		ImagePullPolicy: m.BaseS3Spec().ImagePullPolicy(),
		Env:             append(m.BaseS3Spec().Env(), kubernetesEnvVars...),
		Resources:       filterContainerResources(m.Spec.S3.ResourceRequirements),
		VolumeMounts:    mergeVolumeMounts(volumeMounts, m.BaseS3Spec().VolumeMounts()),
		Command: []string{
			"/bin/sh",
			"-ec",
			buildS3GatewayStartupScript(m, m.BaseS3Spec().ExtraArgs()...),
		},
		Ports: ports,
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/status",
					Port: intstr.FromInt(int(s3EffectivePort(m))),
				},
			},
			InitialDelaySeconds: 10,
			TimeoutSeconds:      3,
			PeriodSeconds:       15,
			SuccessThreshold:    1,
			FailureThreshold:    100,
		},
	}}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-s3",
			Namespace: m.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: m.Spec.S3.Annotations,
				},
				Spec: podSpec,
			},
		},
	}
}

func buildS3GatewayStartupScript(m *seaweedv1.Seaweed, extraArgs ...string) string {
	commands := []string{"weed", "-logtostderr=true"}
	if arg := tlsConfigDirArg(m); arg != "" {
		commands = append(commands, arg)
	}
	commands = append(commands, "s3")
	commands = append(commands, fmt.Sprintf("-port=%d", s3EffectivePort(m)))
	commands = append(commands, fmt.Sprintf("-filer=%s-filer:%d", m.Name, seaweedv1.FilerHTTPPort))
	if m.Spec.S3.ConfigSecret != nil && m.Spec.S3.ConfigSecret.Key != "" {
		commands = append(commands, "-config=/etc/sw/"+m.Spec.S3.ConfigSecret.Key)
	}
	if m.Spec.S3.DomainName != nil && *m.Spec.S3.DomainName != "" {
		commands = append(commands, "-domainName="+*m.Spec.S3.DomainName)
	}
	if !m.Spec.S3.IAM {
		commands = append(commands, "-iam=false")
	}
	if m.Spec.S3.MetricsPort != nil {
		commands = append(commands, fmt.Sprintf("-metricsPort=%d", *m.Spec.S3.MetricsPort))
	}
	commands = append(commands, extraArgs...)
	return strings.Join(commands, " ")
}

func (r *SeaweedReconciler) ensureS3ServiceMonitor(m *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	sm := r.createS3ServiceMonitor(m)
	if err := controllerutil.SetControllerReference(m, sm, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateServiceMonitor(sm)
	return ReconcileResult(err)
}

// getS3Status retrieves the S3 Deployment status for the top-level
// status subresource. Nil result == gateway not configured.
func (r *SeaweedReconciler) getS3Status(ctx context.Context, m *seaweedv1.Seaweed) (seaweedv1.ComponentStatus, error) {
	if m.Spec.S3 == nil {
		return seaweedv1.ComponentStatus{}, nil
	}
	status := seaweedv1.ComponentStatus{Replicas: m.Spec.S3.Replicas}
	dep := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Namespace: m.Namespace, Name: m.Name + "-s3"}, dep)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return status, nil
		}
		return status, err
	}
	status.ReadyReplicas = dep.Status.ReadyReplicas
	return status, nil
}

// client and runtime imports are used by getS3Status — keeping them at
// file scope avoids churn if more helpers land here later.
var (
	_ = client.ObjectKey{}
	_ = runtime.Unknown{}
)
