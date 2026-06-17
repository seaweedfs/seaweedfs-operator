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
	// In topology-only deployments spec.volume is omitted (the operator
	// skips the flat -volume StatefulSet entirely), so all flat-Volume
	// fallbacks below must be nil-safe — reading m.Spec.Volume.<field>
	// directly panics otherwise.
	var fallback seaweedv1.VolumeServerConfig
	if m.Spec.Volume != nil {
		fallback = m.Spec.Volume.VolumeServerConfig
	}

	commands := weedPreamble(m, topologyLoggingArgs(m, topologySpec), "volume")
	commands = append(commands, fmt.Sprintf("-port=%d", seaweedv1.VolumeHTTPPort))

	// Configure max volume counts with fallback
	maxVolumeCounts := getVolumeServerConfigValue(topologySpec.MaxVolumeCounts, fallback.MaxVolumeCounts)
	if maxVolumeCounts != nil {
		commands = append(commands, fmt.Sprintf("-max=%d", *maxVolumeCounts))
	} else {
		commands = append(commands, "-max=0")
	}

	commands = append(commands, fmt.Sprintf("-ip=$(POD_NAME).%s-volume-%s-peer.%s", m.Name, topologyName, m.Namespace))
	if m.Spec.HostSuffix != nil && *m.Spec.HostSuffix != "" {
		commands = append(commands, fmt.Sprintf("-publicUrl=$(POD_NAME).%s", *m.Spec.HostSuffix))
	}
	commands = append(commands, fmt.Sprintf("-mserver=%s", getMasterPeersString(m)))
	commands = append(commands, fmt.Sprintf("-dir=%s", strings.Join(dirs, ",")))

	// Configure metrics port with fallback
	metricsPort := getMetricsPort(m, topologySpec)
	if metricsPort != nil {
		commands = append(commands, fmt.Sprintf("-metricsPort=%d", *metricsPort))
	}

	// Always include rack and datacenter for topology groups
	commands = append(commands, fmt.Sprintf("-rack=%s", topologySpec.Rack))
	commands = append(commands, fmt.Sprintf("-dataCenter=%s", topologySpec.DataCenter))

	// Add volume server configuration parameters with fallback
	compactionMBps := getVolumeServerConfigValue(topologySpec.CompactionMBps, fallback.CompactionMBps)
	if compactionMBps != nil {
		commands = append(commands, fmt.Sprintf("-compactionMBps=%d", *compactionMBps))
	}
	fileSizeLimitMB := getVolumeServerConfigValue(topologySpec.FileSizeLimitMB, fallback.FileSizeLimitMB)
	if fileSizeLimitMB != nil {
		commands = append(commands, fmt.Sprintf("-fileSizeLimitMB=%d", *fileSizeLimitMB))
	}
	fixJpgOrientation := getVolumeServerConfigValue(topologySpec.FixJpgOrientation, fallback.FixJpgOrientation)
	if fixJpgOrientation != nil {
		commands = append(commands, fmt.Sprintf("-fixJpgOrientation=%t", *fixJpgOrientation))
	}
	idleTimeout := getVolumeServerConfigValue(topologySpec.IdleTimeout, fallback.IdleTimeout)
	if idleTimeout != nil {
		commands = append(commands, fmt.Sprintf("-idleTimeout=%d", *idleTimeout))
	}
	minFreeSpacePercent := getVolumeServerConfigValue(topologySpec.MinFreeSpacePercent, fallback.MinFreeSpacePercent)
	if minFreeSpacePercent != nil {
		commands = append(commands, fmt.Sprintf("-minFreeSpacePercent=%d", *minFreeSpacePercent))
	}
	commands = append(commands, topologySpec.ExtraArgs...)

	return strings.Join(commands, " ")
}

// buildVolumeServerStartupScript renders the flat volume server command. maxArg
// is the `-max` value (single count or per-directory list) and ipArg is the
// advertised address (peer DNS for a StatefulSet, $(POD_IP) for a DaemonSet).
func buildVolumeServerStartupScript(m *seaweedv1.Seaweed, dirs []string, maxArg, ipArg string, extraArgs ...string) string {
	commands := weedPreamble(m, m.BaseVolumeSpec().LoggingArgs(), "volume")
	commands = append(commands, fmt.Sprintf("-port=%d", seaweedv1.VolumeHTTPPort))
	commands = append(commands, "-max="+maxArg)
	commands = append(commands, "-ip="+ipArg)
	// $(POD_NAME) is a random suffix for DaemonSet pods, so a HostSuffix-based
	// publicUrl would be unresolvable — only emit it for StatefulSet ordinals.
	if m.Spec.HostSuffix != nil && *m.Spec.HostSuffix != "" && !m.Spec.Volume.IsDaemonSet() {
		commands = append(commands, fmt.Sprintf("-publicUrl=$(POD_NAME).%s", *m.Spec.HostSuffix))
	}
	commands = append(commands, fmt.Sprintf("-mserver=%s", getMasterPeersString(m)))
	commands = append(commands, fmt.Sprintf("-dir=%s", strings.Join(dirs, ",")))

	// Configure metrics port
	if m.Spec.Volume.MetricsPort != nil {
		commands = append(commands, fmt.Sprintf("-metricsPort=%d", *m.Spec.Volume.MetricsPort))
	}

	// Configure topology placement
	if m.Spec.Volume.Rack != nil && *m.Spec.Volume.Rack != "" {
		commands = append(commands, fmt.Sprintf("-rack=%s", *m.Spec.Volume.Rack))
	}
	if m.Spec.Volume.DataCenter != nil && *m.Spec.Volume.DataCenter != "" {
		commands = append(commands, fmt.Sprintf("-dataCenter=%s", *m.Spec.Volume.DataCenter))
	}

	// Add volume server configuration parameters from VolumeServerConfig
	if m.Spec.Volume.CompactionMBps != nil {
		commands = append(commands, fmt.Sprintf("-compactionMBps=%d", *m.Spec.Volume.CompactionMBps))
	}
	if m.Spec.Volume.FileSizeLimitMB != nil {
		commands = append(commands, fmt.Sprintf("-fileSizeLimitMB=%d", *m.Spec.Volume.FileSizeLimitMB))
	}
	if m.Spec.Volume.FixJpgOrientation != nil {
		commands = append(commands, fmt.Sprintf("-fixJpgOrientation=%t", *m.Spec.Volume.FixJpgOrientation))
	}
	if m.Spec.Volume.IdleTimeout != nil {
		commands = append(commands, fmt.Sprintf("-idleTimeout=%d", *m.Spec.Volume.IdleTimeout))
	}
	if m.Spec.Volume.MinFreeSpacePercent != nil {
		commands = append(commands, fmt.Sprintf("-minFreeSpacePercent=%d", *m.Spec.Volume.MinFreeSpacePercent))
	}
	commands = append(commands, extraArgs...)

	return strings.Join(commands, " ")
}

// volumeServerDisks is the rendered storage for a flat volume server: container
// mounts, pod volumes, optional PVC templates, the -dir list, and the matching
// -max argument.
type volumeServerDisks struct {
	mounts  []corev1.VolumeMount
	volumes []corev1.Volume
	pvcs    []corev1.PersistentVolumeClaim
	dirs    []string
	maxArg  string
}

// volumeServerDisksFor renders node-local HostPath disks when configured,
// otherwise the default PVC-per-disk layout.
func volumeServerDisksFor(m *seaweedv1.Seaweed) volumeServerDisks {
	if len(m.Spec.Volume.HostPath) > 0 {
		return hostPathVolumeDisks(m.Spec.Volume)
	}
	return pvcVolumeDisks(m)
}

// pvcVolumeDisks renders one PVC-backed disk per spec.volumeServerDiskCount.
func pvcVolumeDisks(m *seaweedv1.Seaweed) volumeServerDisks {
	volumeCount := 1 // default value
	if m.Spec.VolumeServerDiskCount != nil {
		volumeCount = int(*m.Spec.VolumeServerDiskCount)
	}

	volumeRequests := corev1.ResourceList{}
	if m.Spec.Volume.Requests != nil {
		if storageRequest, ok := m.Spec.Volume.Requests[corev1.ResourceStorage]; ok {
			volumeRequests[corev1.ResourceStorage] = storageRequest
		}
	}

	var d volumeServerDisks
	for i := 0; i < volumeCount; i++ {
		d.mounts = append(d.mounts, corev1.VolumeMount{
			Name:      fmt.Sprintf("mount%d", i),
			ReadOnly:  false,
			MountPath: fmt.Sprintf("/data%d/", i),
		})
		d.volumes = append(d.volumes, corev1.Volume{
			Name: fmt.Sprintf("mount%d", i),
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: fmt.Sprintf("mount%d", i),
					ReadOnly:  false,
				},
			},
		})
		d.pvcs = append(d.pvcs, corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("mount%d", i),
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				StorageClassName: m.Spec.Volume.StorageClassName,
				Selector:         m.Spec.Volume.StorageSelector.DeepCopy(),
				AccessModes: []corev1.PersistentVolumeAccessMode{
					corev1.ReadWriteOnce,
				},
				Resources: corev1.VolumeResourceRequirements{
					Requests: volumeRequests,
				},
			},
		})
		d.dirs = append(d.dirs, fmt.Sprintf("/data%d", i))
	}
	d.maxArg = volumeServerGlobalMaxArg(m.Spec.Volume.MaxVolumeCounts)
	return d
}

// hostPathVolumeDisks renders one node-local hostPath disk per HostPath entry
// and creates no PVCs. A per-directory -max list is emitted when any entry sets
// MaxVolumeCount (0 for unset entries), otherwise the single global value.
func hostPathVolumeDisks(vol *seaweedv1.VolumeSpec) volumeServerDisks {
	var d volumeServerDisks
	maxParts := make([]string, len(vol.HostPath))
	perDirMax := false
	for i, hp := range vol.HostPath {
		name := fmt.Sprintf("mount%d", i)
		d.mounts = append(d.mounts, corev1.VolumeMount{
			Name:      name,
			ReadOnly:  false,
			MountPath: fmt.Sprintf("/data%d/", i),
		})
		hostPathType := corev1.HostPathDirectoryOrCreate
		if hp.Type != nil {
			hostPathType = *hp.Type
		}
		d.volumes = append(d.volumes, corev1.Volume{
			Name: name,
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: hp.Path,
					Type: &hostPathType,
				},
			},
		})
		d.dirs = append(d.dirs, fmt.Sprintf("/data%d", i))
		if hp.MaxVolumeCount != nil {
			perDirMax = true
			maxParts[i] = fmt.Sprintf("%d", *hp.MaxVolumeCount)
		} else {
			// Unset entries fall back to the global limit so a partially
			// specified list does not silently make some disks unlimited.
			maxParts[i] = volumeServerGlobalMaxArg(vol.MaxVolumeCounts)
		}
	}
	if perDirMax {
		d.maxArg = strings.Join(maxParts, ",")
	} else {
		d.maxArg = volumeServerGlobalMaxArg(vol.MaxVolumeCounts)
	}
	return d
}

// volumeServerGlobalMaxArg mirrors the historical single-value -max: the count
// when positive, otherwise 0 (auto-size against free disk space).
func volumeServerGlobalMaxArg(maxVolumeCounts *int32) string {
	if maxVolumeCounts != nil && *maxVolumeCounts > 0 {
		return fmt.Sprintf("%d", *maxVolumeCounts)
	}
	return "0"
}

// buildFlatVolumePodSpec assembles the pod spec shared by the flat volume
// StatefulSet and DaemonSet. ipArg is the address advertised to the masters.
func (r *SeaweedReconciler) buildFlatVolumePodSpec(m *seaweedv1.Seaweed, disks volumeServerDisks, ipArg string) corev1.PodSpec {
	enableServiceLinks := false

	volumeMounts := disks.mounts
	volumes := disks.volumes
	if tlsVols, tlsMounts := tlsVolumesAndMounts(m); len(tlsVols) > 0 {
		volumes = append(volumes, tlsVols...)
		volumeMounts = append(volumeMounts, tlsMounts...)
	}

	ports := []corev1.ContainerPort{
		{ContainerPort: seaweedv1.VolumeHTTPPort, Name: "volume-http"},
		{ContainerPort: seaweedv1.VolumeGRPCPort, Name: "volume-grpc"},
	}
	if m.Spec.Volume.MetricsPort != nil {
		ports = append(ports, corev1.ContainerPort{ContainerPort: *m.Spec.Volume.MetricsPort, Name: "volume-metrics"})
	}

	volumePodSpec := m.BaseVolumeSpec().BuildPodSpec()
	volumePodSpec.EnableServiceLinks = &enableServiceLinks
	volumePodSpec.Containers = []corev1.Container{{
		Name:            "volume",
		Image:           m.Spec.Image,
		ImagePullPolicy: m.BaseVolumeSpec().ImagePullPolicy(),
		SecurityContext: m.BaseVolumeSpec().ContainerSecurityContext(),
		Env:             append(m.BaseVolumeSpec().Env(), kubernetesEnvVars...),
		Resources:       filterContainerResources(m.Spec.Volume.ResourceRequirements),
		Command: []string{
			"/bin/sh",
			"-ec",
			buildVolumeServerStartupScript(m, disks.dirs, disks.maxArg, ipArg, m.BaseVolumeSpec().ExtraArgs()...),
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
		VolumeMounts: mergeVolumeMounts(volumeMounts, m.BaseVolumeSpec().VolumeMounts()),
	}}
	volumePodSpec.Containers = append(volumePodSpec.Containers, m.BaseVolumeSpec().Sidecars()...)
	volumePodSpec.InitContainers = append(volumePodSpec.InitContainers, m.BaseVolumeSpec().InitContainers()...)
	volumePodSpec.Volumes = append(volumePodSpec.Volumes, volumes...)
	return volumePodSpec
}

func (r *SeaweedReconciler) createVolumeServerStatefulSet(m *seaweedv1.Seaweed) *appsv1.StatefulSet {
	labels := labelsForVolumeServer(m.Name)
	podLabels := mergePodLabels(labels, m.BaseVolumeSpec().Labels())
	annotations := m.Spec.Volume.Annotations
	replicas := int32(m.Spec.Volume.Replicas)
	rollingUpdatePartition := int32(0)

	disks := volumeServerDisksFor(m)
	ipArg := fmt.Sprintf("$(POD_NAME).%s-volume-peer.%s", m.Name, m.Namespace)
	volumePodSpec := r.buildFlatVolumePodSpec(m, disks, ipArg)

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
					Labels:      podLabels,
					Annotations: annotations,
				},
				Spec: volumePodSpec,
			},
			VolumeClaimTemplates: disks.pvcs,
		},
	}
	return dep
}

// createVolumeServerDaemonSet renders the flat volume server as a DaemonSet
// (one per selected node) backed by HostPath disks, advertising $(POD_IP).
func (r *SeaweedReconciler) createVolumeServerDaemonSet(m *seaweedv1.Seaweed) *appsv1.DaemonSet {
	labels := labelsForVolumeServer(m.Name)
	podLabels := mergePodLabels(labels, m.BaseVolumeSpec().Labels())
	annotations := m.Spec.Volume.Annotations

	disks := volumeServerDisksFor(m)
	volumePodSpec := r.buildFlatVolumePodSpec(m, disks, "$(POD_IP)")

	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-volume",
			Namespace: m.Namespace,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
				Type: appsv1.RollingUpdateDaemonSetStrategyType,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      podLabels,
					Annotations: annotations,
				},
				Spec: volumePodSpec,
			},
		},
	}
}

func (r *SeaweedReconciler) createVolumeServerTopologyStatefulSet(m *seaweedv1.Seaweed, topologyName string, topologySpec *seaweedv1.VolumeTopologySpec) *appsv1.StatefulSet {
	labels := labelsForVolumeServerTopology(m.Name, topologyName)
	// 3-tier inheritance for topology pods: cluster + volume + topology, with
	// the topology winning on collisions. BaseVolumeSpec().Labels() already
	// returns cluster+volume merged.
	podLabels := mergePodLabels(labels, mergeLabels(m.BaseVolumeSpec().Labels(), topologySpec.Labels))
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
	// Get resource requirements with fallback logic
	resourceRequirements := getResourceRequirements(m, topologySpec)
	volumeRequests := corev1.ResourceList{}
	if resourceRequirements.Requests != nil {
		if storageRequest, ok := resourceRequirements.Requests[corev1.ResourceStorage]; ok {
			volumeRequests[corev1.ResourceStorage] = storageRequest
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
				StorageClassName: getStorageClassName(m, topologySpec),
				Selector:         getStorageSelector(m, topologySpec).DeepCopy(),
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
	if tlsVols, tlsMounts := tlsVolumesAndMounts(m); len(tlsVols) > 0 {
		volumes = append(volumes, tlsVols...)
		volumeMounts = append(volumeMounts, tlsMounts...)
	}
	volumePodSpec.Containers = []corev1.Container{{
		Name:            "volume",
		Image:           m.Spec.Image,
		ImagePullPolicy: getImagePullPolicy(m, topologySpec),
		SecurityContext: getContainerSecurityContext(m, topologySpec),
		Env:             append(getEnvVars(m, topologySpec), kubernetesEnvVars...),
		Resources:       filterContainerResources(resourceRequirements),
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
	volumePodSpec.Containers = append(volumePodSpec.Containers, topologySpec.Sidecars...)
	volumePodSpec.InitContainers = append(volumePodSpec.InitContainers, topologySpec.InitContainers...)
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
					Labels:      podLabels,
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

	if topologySpec.ServiceAccountName != nil {
		podSpec.ServiceAccountName = *topologySpec.ServiceAccountName
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

	if sc := getPodSecurityContext(m, topologySpec); sc != nil {
		podSpec.SecurityContext = sc
	}

	return podSpec
}

// getPodSecurityContext resolves the pod-level securityContext for a topology
// group: the topology's own value wins, otherwise it inherits the flat
// spec.volume value (nil-safe for topology-only deployments), otherwise nil.
func getPodSecurityContext(m *seaweedv1.Seaweed, topologySpec *seaweedv1.VolumeTopologySpec) *corev1.PodSecurityContext {
	if topologySpec != nil && topologySpec.PodSecurityContext != nil {
		return topologySpec.PodSecurityContext
	}
	if m.Spec.Volume != nil && m.Spec.Volume.PodSecurityContext != nil {
		return m.Spec.Volume.PodSecurityContext
	}
	return nil
}

// getContainerSecurityContext resolves the container-level securityContext for
// a topology group's volume container, mirroring getPodSecurityContext's
// topology-then-flat-volume fallback.
func getContainerSecurityContext(m *seaweedv1.Seaweed, topologySpec *seaweedv1.VolumeTopologySpec) *corev1.SecurityContext {
	if topologySpec != nil && topologySpec.ContainerSecurityContext != nil {
		return topologySpec.ContainerSecurityContext
	}
	if m.Spec.Volume != nil && m.Spec.Volume.ContainerSecurityContext != nil {
		return m.Spec.Volume.ContainerSecurityContext
	}
	return nil
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
