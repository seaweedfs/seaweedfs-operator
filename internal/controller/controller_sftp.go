/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Standalone SFTP gateway reconciliation.
//
// The gateway is a stateless Deployment (no per-pod identity) fronted by
// a single Service. It connects to the filer via the filer's in-cluster
// Service, so the webhook requires a Filer in the same Seaweed CR.
//
// User auth and SSH host keys are supplied via two optional Secrets:
//   - UserStoreSecret → mounted at /etc/sw/seaweedfs_sftp_config, passed
//     to weed as -userStoreFile. Omit to run in public/no-auth mode.
//   - HostKeysSecret → mounted read-only at /etc/sw/ssh. Omit to let the
//     server generate an ephemeral key on startup (dev only).
package controller

import (
	"context"
	"fmt"
	"strings"

	monitorv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/internal/controller/label"
)

const (
	sftpComponent = "sftp"
)

func labelsForSFTP(name string) map[string]string {
	return map[string]string{
		label.ManagedByLabelKey: "seaweedfs-operator",
		label.NameLabelKey:      "seaweedfs",
		label.ComponentLabelKey: sftpComponent,
		label.InstanceLabelKey:  name,
	}
}

// sftpEffectivePort returns the port the SFTP gateway listens on.
func sftpEffectivePort(m *seaweedv1.Seaweed) int32 {
	if m.Spec.SFTP != nil && m.Spec.SFTP.Port != nil {
		return *m.Spec.SFTP.Port
	}
	return seaweedv1.SFTPPort
}

func (r *SeaweedReconciler) ensureSFTPGateway(ctx context.Context, m *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	// Tear-down path: when the user removes Spec.SFTP the operator must
	// prune whatever the prior reconcile created, otherwise clients keep
	// reaching a gateway that is no longer managed.
	if m.Spec.SFTP == nil {
		return ReconcileResult(r.deleteSFTPGateway(ctx, m))
	}
	// The gateway dials the filer; without one it would start but every
	// client request would fail. Webhook already rejects this
	// combination on create/update, but we re-check at reconcile time in
	// case an older CR slipped through.
	if m.Spec.Filer == nil {
		r.Log.Info("SeaweedSpec.SFTP set but Filer is nil; tearing down standalone SFTP",
			"seaweed", m.Name)
		return ReconcileResult(r.deleteSFTPGateway(ctx, m))
	}

	if done, res, err := r.ensureSFTPService(m); done {
		return done, res, err
	}
	if done, res, err := r.ensureSFTPDeployment(ctx, m); done {
		return done, res, err
	}
	if m.Spec.SFTP.MetricsPort != nil {
		if done, res, err := r.ensureSFTPServiceMonitor(m); done {
			return done, res, err
		}
	} else {
		// MetricsPort toggled off — delete any ServiceMonitor we left
		// behind. Gate on the CRD so we do not call an API the cluster
		// cannot serve.
		if r.serviceMonitorCRDAvailable() {
			sm := &monitorv1.ServiceMonitor{
				ObjectMeta: metav1.ObjectMeta{Name: m.Name + "-sftp", Namespace: m.Namespace},
			}
			if err := r.Delete(ctx, sm); err != nil && !apierrors.IsNotFound(err) {
				return ReconcileResult(err)
			}
		}
	}
	if m.Spec.SFTP.Ingress != nil && m.Spec.SFTP.Ingress.Enabled {
		if done, res, err := r.ensureComponentIngress(m, sftpComponent, m.Name+"-sftp",
			sftpEffectivePort(m), m.Spec.SFTP.Ingress); done {
			return done, res, err
		}
	} else {
		ing := &networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Name: m.Name + "-sftp-ingress", Namespace: m.Namespace},
		}
		if err := r.Delete(ctx, ing); err != nil && !apierrors.IsNotFound(err) {
			return ReconcileResult(err)
		}
	}
	return ReconcileResult(nil)
}

// deleteSFTPGateway deletes the full set of resources the SFTP gateway
// reconciler creates: Deployment, Service, optional Ingress, optional
// ServiceMonitor. All calls are IsNotFound-safe.
func (r *SeaweedReconciler) deleteSFTPGateway(ctx context.Context, m *seaweedv1.Seaweed) error {
	name := m.Name + "-sftp"
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: m.Namespace}}
	if err := r.Delete(ctx, dep); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: m.Namespace}}
	if err := r.Delete(ctx, svc); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: name + "-ingress", Namespace: m.Namespace}}
	if err := r.Delete(ctx, ing); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if r.serviceMonitorCRDAvailable() {
		sm := &monitorv1.ServiceMonitor{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: m.Namespace}}
		if err := r.Delete(ctx, sm); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (r *SeaweedReconciler) ensureSFTPService(m *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	svc := r.buildSFTPService(m)
	if err := controllerutil.SetControllerReference(m, svc, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateService(svc)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) buildSFTPService(m *seaweedv1.Seaweed) *corev1.Service {
	labels := labelsForSFTP(m.Name)
	ports := []corev1.ServicePort{{
		Name:       "sftp",
		Port:       sftpEffectivePort(m),
		TargetPort: intstr.FromInt(int(sftpEffectivePort(m))),
		Protocol:   corev1.ProtocolTCP,
	}}
	if m.Spec.SFTP.MetricsPort != nil {
		ports = append(ports, corev1.ServicePort{
			Name:       "sftp-metrics",
			Port:       *m.Spec.SFTP.MetricsPort,
			TargetPort: intstr.FromInt(int(*m.Spec.SFTP.MetricsPort)),
			Protocol:   corev1.ProtocolTCP,
		})
	}
	svcType := corev1.ServiceTypeClusterIP
	var annotations map[string]string
	var clusterIP string
	var lbIP string
	if m.Spec.SFTP.Service != nil {
		if m.Spec.SFTP.Service.Type != "" {
			svcType = m.Spec.SFTP.Service.Type
		}
		annotations = m.Spec.SFTP.Service.Annotations
		if m.Spec.SFTP.Service.ClusterIP != nil {
			clusterIP = *m.Spec.SFTP.Service.ClusterIP
		}
		if m.Spec.SFTP.Service.LoadBalancerIP != nil {
			lbIP = *m.Spec.SFTP.Service.LoadBalancerIP
		}
	}
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        m.Name + "-sftp",
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

func (r *SeaweedReconciler) ensureSFTPDeployment(ctx context.Context, m *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	dep := r.buildSFTPDeployment(m)
	if err := controllerutil.SetControllerReference(m, dep, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateDeployment(dep)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) buildSFTPDeployment(m *seaweedv1.Seaweed) *appsv1.Deployment {
	labels := labelsForSFTP(m.Name)
	replicas := m.Spec.SFTP.Replicas

	podSpec := m.BaseSFTPSpec().BuildPodSpec()
	var volumeMounts []corev1.VolumeMount

	if m.Spec.SFTP.UserStoreSecret != nil && m.Spec.SFTP.UserStoreSecret.Name != "" && m.Spec.SFTP.UserStoreSecret.Key != "" {
		// Project only the referenced key: if the user points at a
		// shared Secret with other keys in it, those stay out of the
		// pod (least privilege).
		podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
			Name: "sftp-userstore",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: m.Spec.SFTP.UserStoreSecret.Name,
					Items: []corev1.KeyToPath{{
						Key:  m.Spec.SFTP.UserStoreSecret.Key,
						Path: m.Spec.SFTP.UserStoreSecret.Key,
					}},
				},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "sftp-userstore",
			ReadOnly:  true,
			MountPath: "/etc/sw",
		})
	}
	if m.Spec.SFTP.HostKeysSecret != nil && m.Spec.SFTP.HostKeysSecret.Name != "" {
		podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
			Name: "sftp-hostkeys",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: m.Spec.SFTP.HostKeysSecret.Name,
				},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "sftp-hostkeys",
			ReadOnly:  true,
			MountPath: "/etc/sw/ssh",
		})
	}
	if tlsVols, tlsMounts := tlsVolumesAndMounts(m); len(tlsVols) > 0 {
		podSpec.Volumes = append(podSpec.Volumes, tlsVols...)
		volumeMounts = append(volumeMounts, tlsMounts...)
	}

	ports := []corev1.ContainerPort{{
		Name:          "sftp",
		ContainerPort: sftpEffectivePort(m),
	}}
	if m.Spec.SFTP.MetricsPort != nil {
		ports = append(ports, corev1.ContainerPort{
			Name:          "sftp-metrics",
			ContainerPort: *m.Spec.SFTP.MetricsPort,
		})
	}

	podSpec.Containers = []corev1.Container{{
		Name:            "sftp",
		Image:           m.Spec.Image,
		ImagePullPolicy: m.BaseSFTPSpec().ImagePullPolicy(),
		Env:             append(m.BaseSFTPSpec().Env(), kubernetesEnvVars...),
		Resources:       filterContainerResources(m.Spec.SFTP.ResourceRequirements),
		VolumeMounts:    mergeVolumeMounts(volumeMounts, m.BaseSFTPSpec().VolumeMounts()),
		Command: []string{
			"/bin/sh",
			"-ec",
			buildSFTPGatewayStartupScript(m, m.BaseSFTPSpec().ExtraArgs()...),
		},
		Ports: ports,
		// TCP probe: SFTP is SSH-over-TCP, no HTTP endpoint to hit.
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{
					Port: intstr.FromInt(int(sftpEffectivePort(m))),
				},
			},
			InitialDelaySeconds: 5,
			TimeoutSeconds:      3,
			PeriodSeconds:       10,
			SuccessThreshold:    1,
			FailureThreshold:    6,
		},
	}}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-sftp",
			Namespace: m.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: m.Spec.SFTP.Annotations,
				},
				Spec: podSpec,
			},
		},
	}
}

func buildSFTPGatewayStartupScript(m *seaweedv1.Seaweed, extraArgs ...string) string {
	commands := []string{"weed", "-logtostderr=true"}
	if arg := tlsConfigDirArg(m); arg != "" {
		commands = append(commands, arg)
	}
	commands = append(commands, "sftp")
	commands = append(commands, fmt.Sprintf("-port=%d", sftpEffectivePort(m)))
	commands = append(commands, fmt.Sprintf("-filer=%s-filer:%d", m.Name, seaweedv1.FilerHTTPPort))
	if m.Spec.SFTP.MetricsPort != nil {
		commands = append(commands, fmt.Sprintf("-metricsPort=%d", *m.Spec.SFTP.MetricsPort))
	}
	if m.Spec.SFTP.UserStoreSecret != nil && m.Spec.SFTP.UserStoreSecret.Key != "" {
		commands = append(commands, "-userStoreFile=/etc/sw/"+m.Spec.SFTP.UserStoreSecret.Key)
	}
	if m.Spec.SFTP.HostKeysSecret != nil && m.Spec.SFTP.HostKeysSecret.Name != "" {
		commands = append(commands, "-hostKeysFolder=/etc/sw/ssh")
	}
	if m.Spec.SFTP.AuthMethods != nil && *m.Spec.SFTP.AuthMethods != "" {
		commands = append(commands, "-authMethods="+*m.Spec.SFTP.AuthMethods)
	}
	if m.Spec.SFTP.MaxAuthTries != nil {
		commands = append(commands, fmt.Sprintf("-maxAuthTries=%d", *m.Spec.SFTP.MaxAuthTries))
	}
	commands = append(commands, extraArgs...)
	return strings.Join(commands, " ")
}

func (r *SeaweedReconciler) ensureSFTPServiceMonitor(m *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	sm := r.createSFTPServiceMonitor(m)
	if err := controllerutil.SetControllerReference(m, sm, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateServiceMonitor(sm)
	return ReconcileResult(err)
}

// getSFTPStatus retrieves the SFTP Deployment status for the top-level
// status subresource. Mirrors getS3Status: we always probe the cluster
// (even when Spec.SFTP is nil) so tear-down progress is visible.
func (r *SeaweedReconciler) getSFTPStatus(ctx context.Context, m *seaweedv1.Seaweed) (seaweedv1.ComponentStatus, error) {
	dep := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Namespace: m.Namespace, Name: m.Name + "-sftp"}, dep)
	if err != nil {
		if apierrors.IsNotFound(err) {
			if m.Spec.SFTP != nil {
				return seaweedv1.ComponentStatus{Replicas: m.Spec.SFTP.Replicas}, nil
			}
			return seaweedv1.ComponentStatus{}, nil
		}
		return seaweedv1.ComponentStatus{}, err
	}
	status := seaweedv1.ComponentStatus{ReadyReplicas: dep.Status.ReadyReplicas}
	if m.Spec.SFTP != nil {
		status.Replicas = m.Spec.SFTP.Replicas
	} else if dep.Spec.Replicas != nil {
		status.Replicas = *dep.Spec.Replicas
	}
	return status, nil
}
