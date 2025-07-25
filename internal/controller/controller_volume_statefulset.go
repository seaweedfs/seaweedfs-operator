package controller

import (
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func buildVolumeServerStartupScript(m *seaweedv1.Seaweed, dirs []string) string {
	commands := []string{"weed", "-logtostderr=true", "volume"}
	commands = append(commands, fmt.Sprintf("-port=%d", seaweedv1.VolumeHTTPPort))
	commands = append(commands, "-max=0")
	commands = append(commands, fmt.Sprintf("-ip=$(POD_NAME).%s-volume-peer.%s", m.Name, m.Namespace))

	if m.Spec.HostSuffix != nil && *m.Spec.HostSuffix != "" {
		commands = append(commands, fmt.Sprintf("-publicUrl=$(POD_NAME).%s", *m.Spec.HostSuffix))
	}

	commands = append(commands, fmt.Sprintf("-mserver=%s", getMasterPeersString(m)))
	commands = append(commands, fmt.Sprintf("-dir=%s", strings.Join(dirs, ",")))

	metricsPort := resolveMetricsPort(m, m.Spec.Volume.MetricsPort)

	if metricsPort != nil {
		commands = append(commands, fmt.Sprintf("-metricsPort=%d", *metricsPort))
	}

	return strings.Join(commands, " ")
}

// getStorageClassName safely gets the storage class name from the storage spec
func getStorageClassName(storage *seaweedv1.StorageSpec) *string {
	if storage != nil {
		return storage.StorageClassName
	}
	return nil
}

func (r *SeaweedReconciler) createVolumeServerStatefulSet(m *seaweedv1.Seaweed) *appsv1.StatefulSet {
	labels := labelsForVolumeServer(m.Name)
	annotations := m.Spec.Volume.Annotations
	ports := []corev1.ContainerPort{
		{
			ContainerPort: seaweedv1.VolumeHTTPPort,
			Name:          "volume-http",
		},
		{
			ContainerPort: seaweedv1.VolumeGRPCPort,
			Name:          "volume-grpc",
		},
	}

	metricsPort := resolveMetricsPort(m, m.Spec.Volume.MetricsPort)

	if metricsPort != nil {
		ports = append(ports, corev1.ContainerPort{
			ContainerPort: *metricsPort,
			Name:          "volume-metrics",
		})
	}
	replicas := m.Spec.Volume.Replicas
	rollingUpdatePartition := int32(0)
	enableServiceLinks := false

	// Set default storage configuration if not specified
	var volumeCount int
	if m.Spec.Storage != nil {
		volumeCount = int(m.Spec.Storage.VolumeServerDiskCount)
	} else {
		// Default to 1 disk if storage spec is not provided
		volumeCount = 1
	}

	// Set default storage request if not specified
	var volumeRequests corev1.ResourceList
	if m.Spec.Volume.Requests != nil {
		volumeRequests = m.Spec.Volume.Requests
	} else {
		// Default to 4Gi if no requests specified
		volumeRequests = corev1.ResourceList{
			corev1.ResourceStorage: resource.MustParse("4Gi"),
		}
	}

	// connect all the disks
	var volumeMounts []corev1.VolumeMount
	var volumes []corev1.Volume
	var persistentVolumeClaims []corev1.PersistentVolumeClaim
	var dirs []string

	for i := 0; i < volumeCount; i++ {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      fmt.Sprintf("mount%d", i),
			ReadOnly:  false,
			MountPath: fmt.Sprintf("/data%d/", i),
		})
		volumes = append(volumes, corev1.Volume{
			Name: fmt.Sprintf("mount%d", i),
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: fmt.Sprintf("mount%d", i),
					ReadOnly:  false,
				},
			},
		})
		persistentVolumeClaims = append(persistentVolumeClaims, corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("mount%d", i),
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				StorageClassName: resolveStorageClassName(getStorageClassName(m.Spec.Storage), m.Spec.Volume.StorageClassName),
				AccessModes: []corev1.PersistentVolumeAccessMode{
					corev1.ReadWriteOnce,
				},
				Resources: corev1.VolumeResourceRequirements{
					Requests: volumeRequests,
				},
			},
		})
		dirs = append(dirs, fmt.Sprintf("/data%d", i))
	}

	volumePodSpec := m.BaseVolumeSpec().BuildPodSpec()
	volumePodSpec.EnableServiceLinks = &enableServiceLinks
	volumePodSpec.Containers = []corev1.Container{{
		Name:            "volume",
		Image:           m.Spec.Image,
		ImagePullPolicy: m.BaseVolumeSpec().ImagePullPolicy(),
		Env:             append(m.BaseVolumeSpec().Env(), kubernetesEnvVars...),
		Resources:       filterContainerResources(m.Spec.Volume.ResourceRequirements),
		Command: []string{
			"/bin/sh",
			"-ec",
			buildVolumeServerStartupScript(m, dirs),
		},
		Ports: ports,
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   "/healthz",
					Port:   intstr.FromInt(seaweedv1.VolumeHTTPPort),
					Scheme: corev1.URISchemeHTTP,
				},
			},
			InitialDelaySeconds: 15,
			TimeoutSeconds:      5,
			PeriodSeconds:       90,
			SuccessThreshold:    1,
			FailureThreshold:    100,
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   "/healthz",
					Port:   intstr.FromInt(seaweedv1.VolumeHTTPPort),
					Scheme: corev1.URISchemeHTTP,
				},
			},
			InitialDelaySeconds: 20,
			TimeoutSeconds:      5,
			PeriodSeconds:       90,
			SuccessThreshold:    1,
			FailureThreshold:    6,
		},
		VolumeMounts: volumeMounts,
	}}
	volumePodSpec.Volumes = volumes

	dep := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-volume",
			Namespace: m.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName:         m.Name + "-volume-peer",
			PodManagementPolicy: appsv1.ParallelPodManagement,
			Replicas:            &replicas,
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.RollingUpdateStatefulSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{
					Partition: &rollingUpdatePartition,
				},
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: annotations,
				},
				Spec: volumePodSpec,
			},
			VolumeClaimTemplates: persistentVolumeClaims,
		},
	}
	return dep
}
