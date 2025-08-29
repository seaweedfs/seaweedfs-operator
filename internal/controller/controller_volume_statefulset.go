package controller

import (
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func buildVolumeServerStartupScriptWithTopology(m *seaweedv1.Seaweed, dirs []string, topologyName string, topologySpec *seaweedv1.VolumeTopologySpec) string {
	commands := []string{"weed", "-logtostderr=true", "volume"}
	commands = append(commands, fmt.Sprintf("-port=%d", seaweedv1.VolumeHTTPPort))

	// Configure max volume counts
	if topologySpec.MaxVolumeCounts != nil {
		commands = append(commands, fmt.Sprintf("-max=%d", *topologySpec.MaxVolumeCounts))
	} else {
		commands = append(commands, "-max=0")
	}

	commands = append(commands, fmt.Sprintf("-ip=$(POD_NAME).%s-volume-%s-peer.%s", m.Name, topologyName, m.Namespace))
	if m.Spec.HostSuffix != nil && *m.Spec.HostSuffix != "" {
		commands = append(commands, fmt.Sprintf("-publicUrl=$(POD_NAME).%s", *m.Spec.HostSuffix))
	}
	commands = append(commands, fmt.Sprintf("-mserver=%s", getMasterPeersString(m)))
	commands = append(commands, fmt.Sprintf("-dir=%s", strings.Join(dirs, ",")))

	// Configure metrics port
	if topologySpec.MetricsPort != nil {
		commands = append(commands, fmt.Sprintf("-metricsPort=%d", *topologySpec.MetricsPort))
	}

	// Always include rack and datacenter for topology groups
	commands = append(commands, fmt.Sprintf("-rack=%s", topologySpec.Rack))
	commands = append(commands, fmt.Sprintf("-dataCenter=%s", topologySpec.DataCenter))

	// Add topology-specific volume server configuration parameters
	if topologySpec.CompactionMBps != nil {
		commands = append(commands, fmt.Sprintf("-compactionMBps=%d", *topologySpec.CompactionMBps))
	}
	if topologySpec.FileSizeLimitMB != nil {
		commands = append(commands, fmt.Sprintf("-fileSizeLimitMB=%d", *topologySpec.FileSizeLimitMB))
	}
	if topologySpec.FixJpgOrientation != nil {
		commands = append(commands, fmt.Sprintf("-fixJpgOrientation=%t", *topologySpec.FixJpgOrientation))
	}
	if topologySpec.IdleTimeout != nil {
		commands = append(commands, fmt.Sprintf("-idleTimeout=%d", *topologySpec.IdleTimeout))
	}
	if topologySpec.MinFreeSpacePercent != nil {
		commands = append(commands, fmt.Sprintf("-minFreeSpacePercent=%d", *topologySpec.MinFreeSpacePercent))
	}

	return strings.Join(commands, " ")
}

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
	if m.Spec.Volume.MetricsPort != nil {
		commands = append(commands, fmt.Sprintf("-metricsPort=%d", *m.Spec.Volume.MetricsPort))
	}
	if m.Spec.Volume.Rack != nil && *m.Spec.Volume.Rack != "" {
		commands = append(commands, fmt.Sprintf("-rack=%s", *m.Spec.Volume.Rack))
	}
	if m.Spec.Volume.DataCenter != nil && *m.Spec.Volume.DataCenter != "" {
		commands = append(commands, fmt.Sprintf("-dataCenter=%s", *m.Spec.Volume.DataCenter))
	}

	return strings.Join(commands, " ")
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
	if m.Spec.Volume.MetricsPort != nil {
		ports = append(ports, corev1.ContainerPort{
			ContainerPort: *m.Spec.Volume.MetricsPort,
			Name:          "volume-metrics",
		})
	}
	replicas := int32(m.Spec.Volume.Replicas)
	rollingUpdatePartition := int32(0)
	enableServiceLinks := false

	var volumeCount int
	if m.Spec.VolumeServerDiskCount != nil {
		volumeCount = int(*m.Spec.VolumeServerDiskCount)
	} else {
		volumeCount = 1 // default value
	}
	volumeRequests := corev1.ResourceList{
		corev1.ResourceStorage: m.Spec.Volume.Requests[corev1.ResourceStorage],
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
				StorageClassName: m.Spec.Volume.StorageClassName,
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

func (r *SeaweedReconciler) createVolumeServerTopologyStatefulSet(m *seaweedv1.Seaweed, topologyName string, topologySpec *seaweedv1.VolumeTopologySpec) *appsv1.StatefulSet {
	labels := labelsForVolumeServerTopology(m.Name, topologyName)
	annotations := mergeAnnotations(m.Spec.Annotations, topologySpec.Annotations)
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
	if topologySpec.MetricsPort != nil {
		ports = append(ports, corev1.ContainerPort{
			ContainerPort: *topologySpec.MetricsPort,
			Name:          "volume-metrics",
		})
	}
	replicas := int32(topologySpec.Replicas)
	rollingUpdatePartition := int32(0)
	enableServiceLinks := false

	var volumeCount int
	if m.Spec.VolumeServerDiskCount != nil {
		volumeCount = int(*m.Spec.VolumeServerDiskCount)
	} else {
		volumeCount = 1 // default value
	}
	volumeRequests := corev1.ResourceList{
		corev1.ResourceStorage: topologySpec.Requests[corev1.ResourceStorage],
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
				StorageClassName: topologySpec.StorageClassName,
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

	// Build pod spec based on topology configuration
	volumePodSpec := buildTopologyPodSpec(m, topologySpec)
	volumePodSpec.EnableServiceLinks = &enableServiceLinks
	volumePodSpec.Containers = []corev1.Container{{
		Name:            "volume",
		Image:           m.Spec.Image,
		ImagePullPolicy: getImagePullPolicy(m, topologySpec),
		Env:             append(getEnvVars(m, topologySpec), kubernetesEnvVars...),
		Resources:       filterContainerResources(topologySpec.ResourceRequirements),
		Command: []string{
			"/bin/sh",
			"-ec",
			buildVolumeServerStartupScriptWithTopology(m, dirs, topologyName, topologySpec),
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
			Name:      fmt.Sprintf("%s-volume-%s", m.Name, topologyName),
			Namespace: m.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName:         fmt.Sprintf("%s-volume-%s-peer", m.Name, topologyName),
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

// Helper functions for topology-aware pod configuration
func buildTopologyPodSpec(m *seaweedv1.Seaweed, topologySpec *seaweedv1.VolumeTopologySpec) corev1.PodSpec {
	podSpec := corev1.PodSpec{}

	// Use topology-specific configuration where available, fallback to global
	if topologySpec.Affinity != nil {
		podSpec.Affinity = topologySpec.Affinity
	} else if m.Spec.Affinity != nil {
		podSpec.Affinity = m.Spec.Affinity
	}

	// Merge cluster-level and topology-level node selectors
	podSpec.NodeSelector = mergeNodeSelector(m.Spec.NodeSelector, topologySpec.NodeSelector)

	if topologySpec.Tolerations != nil {
		podSpec.Tolerations = topologySpec.Tolerations
	} else if m.Spec.Tolerations != nil {
		podSpec.Tolerations = m.Spec.Tolerations
	}

	if topologySpec.PriorityClassName != nil {
		podSpec.PriorityClassName = *topologySpec.PriorityClassName
	}

	if topologySpec.SchedulerName != nil {
		podSpec.SchedulerName = *topologySpec.SchedulerName
	} else if m.Spec.SchedulerName != "" {
		podSpec.SchedulerName = m.Spec.SchedulerName
	}

	if topologySpec.ImagePullSecrets != nil {
		podSpec.ImagePullSecrets = topologySpec.ImagePullSecrets
	} else if m.Spec.ImagePullSecrets != nil {
		podSpec.ImagePullSecrets = m.Spec.ImagePullSecrets
	}

	if topologySpec.TerminationGracePeriodSeconds != nil {
		podSpec.TerminationGracePeriodSeconds = topologySpec.TerminationGracePeriodSeconds
	}

	// Set host network configuration
	if topologySpec.HostNetwork != nil {
		podSpec.HostNetwork = *topologySpec.HostNetwork
	} else if m.Spec.HostNetwork != nil {
		podSpec.HostNetwork = *m.Spec.HostNetwork
	}

	return podSpec
}

func getImagePullPolicy(m *seaweedv1.Seaweed, topologySpec *seaweedv1.VolumeTopologySpec) corev1.PullPolicy {
	if topologySpec != nil && topologySpec.ImagePullPolicy != nil {
		return *topologySpec.ImagePullPolicy
	}
	if m.Spec.ImagePullPolicy != "" {
		return m.Spec.ImagePullPolicy
	}
	return corev1.PullIfNotPresent
}

func getEnvVars(m *seaweedv1.Seaweed, topologySpec *seaweedv1.VolumeTopologySpec) []corev1.EnvVar {
	if topologySpec != nil && topologySpec.Env != nil {
		return topologySpec.Env
	}
	if m.Spec.Volume != nil && m.Spec.Volume.Env != nil {
		return m.Spec.Volume.Env
	}
	return []corev1.EnvVar{}
}
