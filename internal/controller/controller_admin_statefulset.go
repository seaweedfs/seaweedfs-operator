package controller

import (
	"fmt"
	"strconv"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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
			{
				Name:          "admin-grpc",
				ContainerPort: int32(port + seaweedv1.GRPCPortDelta),
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
