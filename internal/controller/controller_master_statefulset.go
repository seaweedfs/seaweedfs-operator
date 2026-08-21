package controller

import (
	"fmt"
	"maps"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// masterDataDir returns the path the master's -mdir is mounted at, and whether
// persistence is on at all. The raft log and snapshots live there, so without a
// volume behind it the master loses its identity on every pod recreation.
func masterDataDir(m *seaweedv1.Seaweed) (string, bool) {
	persistence := m.Spec.Master.Persistence
	if persistence == nil || !persistence.Enabled {
		return "", false
	}
	mountPath := "/data"
	if persistence.MountPath != nil {
		mountPath = *persistence.MountPath
	}
	return mountPath, true
}

func buildMasterStartupScript(m *seaweedv1.Seaweed, extraArgs ...string) string {
	command := weedPreamble(m, m.BaseMasterSpec().LoggingArgs(), "master")
	spec := m.Spec.Master
	if spec.VolumePreallocate != nil && *spec.VolumePreallocate {
		command = append(command, "-volumePreallocate")
	}

	if spec.VolumeSizeLimitMB != nil {
		command = append(command, fmt.Sprintf("-volumeSizeLimitMB=%d", *spec.VolumeSizeLimitMB))
	}

	if spec.GarbageThreshold != nil {
		command = append(command, fmt.Sprintf("-garbageThreshold=%s", *spec.GarbageThreshold))
	}

	if spec.PulseSeconds != nil {
		command = append(command, fmt.Sprintf("-pulseSeconds=%d", *spec.PulseSeconds))
	}

	if spec.DefaultReplication != nil {
		command = append(command, fmt.Sprintf("-defaultReplication=%s", *spec.DefaultReplication))
	}

	if m.Spec.Master.MetricsPort != nil {
		command = append(command, fmt.Sprintf("-metricsPort=%d", *m.Spec.Master.MetricsPort))
	}

	// Only the master takes -metrics.address; it hands the value to volume
	// servers, filers and gateways in its heartbeat response, so setting it
	// here turns on push metrics for the whole cluster.
	if m.Spec.MetricsAddress != "" {
		command = append(command, fmt.Sprintf("-metrics.address=%s", m.Spec.MetricsAddress))
	}

	if dataDir, ok := masterDataDir(m); ok {
		command = append(command, fmt.Sprintf("-mdir=%s", dataDir))
	}

	command = append(command, fmt.Sprintf("-ip=$(POD_NAME).%s-master-peer.%s", m.Name, m.Namespace))
	if arg := ipBindArg(spec.IPBind); arg != "" {
		command = append(command, arg)
	}
	command = append(command, fmt.Sprintf("-peers=%s", getMasterPeersString(m)))
	command = append(command, extraArgs...)
	return strings.Join(command, " ")
}

func (r *SeaweedReconciler) createMasterStatefulSet(m *seaweedv1.Seaweed) *appsv1.StatefulSet {
	labels := labelsForMaster(m.Name)
	podLabels := mergePodLabels(labels, m.BaseMasterSpec().Labels())
	annotations := withJWTSigningAnnotation(m, m.BaseMasterSpec().Annotations())
	ports := []corev1.ContainerPort{
		{
			ContainerPort: seaweedv1.MasterHTTPPort,
			Name:          "master-http",
		},
		{
			ContainerPort: seaweedv1.MasterGRPCPort,
			Name:          "master-grpc",
		},
	}
	if m.Spec.Master.MetricsPort != nil {
		ports = append(ports, corev1.ContainerPort{
			ContainerPort: *m.Spec.Master.MetricsPort,
			Name:          "master-metrics",
		})
	}
	replicas := m.Spec.Master.Replicas
	rollingUpdatePartition := int32(0)
	enableServiceLinks := false

	masterPodSpec := m.BaseMasterSpec().BuildPodSpec()
	var masterConfigMounts []corev1.VolumeMount
	// master.toml comes from a Secret when ConfigSecret is set, otherwise from
	// the ConfigMap — and only when the user supplied non-blank content.
	// Mirrors the filer path; see hasMasterConfig for why whitespace-only
	// counts as "no override".
	if sel := masterConfigSecret(m); sel != nil {
		vol, mount := configSecretVolumeAndMount("master-config", "master.toml", sel)
		masterPodSpec.Volumes = append(masterPodSpec.Volumes, vol)
		masterConfigMounts = append(masterConfigMounts, mount)
	} else if hasMasterConfig(m) {
		masterPodSpec.Volumes = append(masterPodSpec.Volumes, corev1.Volume{
			Name: "master-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: m.Name + "-master",
					},
				},
			},
		})
		masterConfigMounts = append(masterConfigMounts, corev1.VolumeMount{
			Name:      "master-config",
			ReadOnly:  true,
			MountPath: componentConfigDir,
		})
	}
	if tlsVols, tlsMounts := tlsVolumesAndMounts(m); len(tlsVols) > 0 {
		masterPodSpec.Volumes = append(masterPodSpec.Volumes, tlsVols...)
		masterConfigMounts = append(masterConfigMounts, tlsMounts...)
	}

	var persistentVolumeClaims []corev1.PersistentVolumeClaim
	if dataDir, ok := masterDataDir(m); ok {
		persistence := m.Spec.Master.Persistence
		// A claim template lends its name to the mount, so the volume is named
		// after the component either way. An existing claim only ever appears as
		// a ClaimName: PVC names may carry dots and run to 253 characters, and a
		// pod volume name may do neither.
		claimName := m.Name + "-master"
		if persistence.ExistingClaim != nil {
			masterPodSpec.Volumes = append(masterPodSpec.Volumes, corev1.Volume{
				Name: claimName,
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: *persistence.ExistingClaim,
					},
				},
			})
		} else {
			accessModes := persistence.AccessModes
			if len(accessModes) == 0 {
				accessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
			}
			persistentVolumeClaims = append(persistentVolumeClaims, corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:        claimName,
					Annotations: maps.Clone(persistence.Annotations),
					Labels:      maps.Clone(persistence.Labels),
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes:      accessModes,
					Resources:        persistence.Resources,
					StorageClassName: persistence.StorageClassName,
					Selector:         persistence.Selector,
					VolumeName:       persistence.VolumeName,
					VolumeMode:       persistence.VolumeMode,
					DataSource:       persistence.DataSource,
				},
			})
		}
		subPath := ""
		if persistence.SubPath != nil {
			subPath = *persistence.SubPath
		}
		masterConfigMounts = append(masterConfigMounts, corev1.VolumeMount{
			Name:      claimName,
			MountPath: dataDir,
			SubPath:   subPath,
		})
	}

	masterPodSpec.EnableServiceLinks = &enableServiceLinks
	masterPodSpec.Containers = []corev1.Container{{
		Name:            "master",
		Image:           m.BaseMasterSpec().Image(),
		ImagePullPolicy: m.BaseMasterSpec().ImagePullPolicy(),
		SecurityContext: m.BaseMasterSpec().ContainerSecurityContext(),
		Env:             append(m.BaseMasterSpec().Env(), kubernetesEnvVars...),
		Resources:       filterContainerResources(m.Spec.Master.ResourceRequirements),
		VolumeMounts:    mergeVolumeMounts(masterConfigMounts, m.BaseMasterSpec().VolumeMounts()),
		Command: []string{
			"/bin/sh",
			"-ec",
			buildMasterStartupScript(m, m.BaseMasterSpec().ExtraArgs()...),
		},
		Ports: ports,
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   "/cluster/status",
					Port:   intstr.FromInt(seaweedv1.MasterHTTPPort),
					Scheme: corev1.URISchemeHTTP,
				},
			},
			InitialDelaySeconds: 5,
			TimeoutSeconds:      15,
			PeriodSeconds:       15,
			SuccessThreshold:    2,
			FailureThreshold:    100,
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   "/cluster/status",
					Port:   intstr.FromInt(seaweedv1.MasterHTTPPort),
					Scheme: corev1.URISchemeHTTP,
				},
			},
			InitialDelaySeconds: 15,
			TimeoutSeconds:      15,
			PeriodSeconds:       15,
			SuccessThreshold:    1,
			FailureThreshold:    6,
		},
	}}
	applyProbeOverrides(&masterPodSpec.Containers[0], m.BaseMasterSpec().ReadinessProbe(), m.BaseMasterSpec().LivenessProbe())
	masterPodSpec.Containers = append(masterPodSpec.Containers, m.BaseMasterSpec().Sidecars()...)
	masterPodSpec.InitContainers = append(masterPodSpec.InitContainers, m.BaseMasterSpec().InitContainers()...)

	dep := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-master",
			Namespace: m.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName:         m.Name + "-master-peer",
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
				Spec: masterPodSpec,
			},
			VolumeClaimTemplates:                 persistentVolumeClaims,
			PersistentVolumeClaimRetentionPolicy: pvcRetentionPolicy(m),
		},
	}
	// Set master instance as the owner and controller
	// ctrl.SetControllerReference(m, dep, r.Scheme)
	return dep
}
