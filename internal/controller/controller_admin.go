package controller

import (
	"context"
	"fmt"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	label "github.com/seaweedfs/seaweedfs-operator/internal/controller/label"
)

func (r *SeaweedReconciler) ensureAdminServers(seaweedCR *seaweedv1.Seaweed) (done bool, result ctrl.Result, err error) {
	_ = context.Background()
	_ = r.Log.WithValues("seaweed", seaweedCR.Name)

	if done, result, err = r.ensureAdminService(seaweedCR); done {
		return
	}

	if done, result, err = r.ensureAdminStatefulSet(seaweedCR); done {
		return
	}

	return
}

func (r *SeaweedReconciler) ensureAdminStatefulSet(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.WithValues("sw-admin-statefulset", seaweedCR.Name)

	adminStatefulSet := r.createAdminStatefulSet(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, adminStatefulSet, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdate(adminStatefulSet, func(existing, desired runtime.Object) error {
		existingStatefulSet := existing.(*appsv1.StatefulSet)
		desiredStatefulSet := desired.(*appsv1.StatefulSet)

		existingStatefulSet.Spec.Replicas = desiredStatefulSet.Spec.Replicas
		existingStatefulSet.Spec.Template.ObjectMeta = desiredStatefulSet.Spec.Template.ObjectMeta
		existingStatefulSet.Spec.Template.Spec = desiredStatefulSet.Spec.Template.Spec
		return nil
	})
	log.Info("ensure admin stateful set " + adminStatefulSet.Name)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureAdminService(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.WithValues("sw-admin-service", seaweedCR.Name)

	adminService := r.createAdminService(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, adminService, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateService(adminService)

	log.Info("ensure admin service " + adminService.Name)

	return ReconcileResult(err)
}

func labelsForAdmin(name string) map[string]string {
	return map[string]string{
		label.ManagedByLabelKey: "seaweedfs-operator",
		label.NameLabelKey:      "seaweedfs",
		label.ComponentLabelKey: "admin",
		label.InstanceLabelKey:  name,
	}
}

func (r *SeaweedReconciler) createAdminService(m *seaweedv1.Seaweed) *corev1.Service {
	labels := labelsForAdmin(m.Name)
	port := seaweedv1.AdminHTTPPort
	if m.Spec.Admin.Port != nil {
		port = int(*m.Spec.Admin.Port)
	}

	ports := []corev1.ServicePort{
		{
			Name:       "admin-http",
			Protocol:   corev1.Protocol("TCP"),
			Port:       int32(port),
			TargetPort: intstr.FromInt(port),
		},
		{
			Name:       "admin-grpc",
			Protocol:   corev1.Protocol("TCP"),
			Port:       int32(port + seaweedv1.GRPCPortDelta),
			TargetPort: intstr.FromInt(port + seaweedv1.GRPCPortDelta),
		},
	}

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-admin",
			Namespace: m.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Ports:    ports,
			Selector: labels,
		},
	}

	// Apply service spec if provided
	if m.Spec.Admin.Service != nil {
		if m.Spec.Admin.Service.Type != "" {
			service.Spec.Type = m.Spec.Admin.Service.Type
		}
		if m.Spec.Admin.Service.Annotations != nil {
			service.ObjectMeta.Annotations = m.Spec.Admin.Service.Annotations
		}
		if m.Spec.Admin.Service.LoadBalancerIP != nil {
			service.Spec.LoadBalancerIP = *m.Spec.Admin.Service.LoadBalancerIP
		}
		if m.Spec.Admin.Service.ClusterIP != nil {
			service.Spec.ClusterIP = *m.Spec.Admin.Service.ClusterIP
		}
	}

	return service
}

func (r *SeaweedReconciler) createAdminStatefulSet(m *seaweedv1.Seaweed) *appsv1.StatefulSet {
	labels := labelsForAdmin(m.Name)
	port := seaweedv1.AdminHTTPPort
	if m.Spec.Admin.Port != nil {
		port = int(*m.Spec.Admin.Port)
	}

	// Build command arguments
	args := []string{"admin"}
	args = append(args, "-port="+strconv.Itoa(port))

	masterAdresses := m.Spec.Admin.Masters

	if masterAdresses == "" {
		masterAdresses = fmt.Sprintf("%s-master:9333", m.Name)
	}

	args = append(args, "-masters="+masterAdresses)

	if m.Spec.Admin.DataDir != "" {
		args = append(args, "-dataDir="+m.Spec.Admin.DataDir)
	}

	if m.Spec.Admin.AdminUser != "" {
		args = append(args, "-adminUser="+m.Spec.Admin.AdminUser)
	}

	if m.Spec.Admin.AdminPassword != "" {
		args = append(args, "-adminPassword="+m.Spec.Admin.AdminPassword)
	}

	// Create container
	container := corev1.Container{
		Name:            "admin",
		Image:           m.Spec.Image,
		ImagePullPolicy: m.BaseAdminSpec().ImagePullPolicy(),
		Args:            args,
		Ports: []corev1.ContainerPort{
			{
				Name:          "admin-http",
				ContainerPort: int32(port),
				Protocol:      corev1.ProtocolTCP,
			},
		},
		Env: append(kubernetesEnvVars, m.BaseAdminSpec().Env()...),
	}

	// Add volume mounts for persistence if enabled
	if m.Spec.Admin.Persistence != nil && m.Spec.Admin.Persistence.Enabled {
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      "admin-data",
			MountPath: *m.Spec.Admin.Persistence.MountPath,
			SubPath:   *m.Spec.Admin.Persistence.SubPath,
		})
	}

	// Create pod template
	podTemplate := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: labels,
		},
		Spec: m.BaseAdminSpec().BuildPodSpec(),
	}
	podTemplate.Spec.Containers = []corev1.Container{container}

	// Add volumes for persistence if enabled
	if m.Spec.Admin.Persistence != nil && m.Spec.Admin.Persistence.Enabled {
		podTemplate.Spec.Volumes = append(podTemplate.Spec.Volumes, corev1.Volume{
			Name: "admin-data",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: m.Name + "-admin",
				},
			},
		})
	}

	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-admin",
			Namespace: m.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &m.Spec.Admin.Replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: podTemplate,
		},
	}

	// Apply StatefulSet update strategy
	statefulSet.Spec.UpdateStrategy.Type = m.BaseAdminSpec().StatefulSetUpdateStrategy()

	return statefulSet
}
