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

func buildFilerStartupScript(m *seaweedv1.Seaweed, extraArgs ...string) string {
	commands := []string{"weed", "-logtostderr=true", "filer"}
	commands = append(commands, fmt.Sprintf("-port=%d", seaweedv1.FilerHTTPPort))
	commands = append(commands, fmt.Sprintf("-ip=$(POD_NAME).%s-filer-peer.%s", m.Name, m.Namespace))
	commands = append(commands, fmt.Sprintf("-master=%s", getMasterPeersString(m)))
	if s3Config := m.Spec.Filer.S3; s3Config != nil && s3Config.Enabled {
		commands = append(commands, "-s3")
		if s3Config.ConfigSecret != nil && s3Config.ConfigSecret.Name != "" {
			commands = append(commands, "-s3.config=/etc/sw/"+s3Config.ConfigSecret.Key)
		}
		// IAM is now embedded in S3 by default (enabled with -iam=true, which is the default)
		// Only add -iam=false if explicitly disabled
		if !m.Spec.Filer.IAM {
			commands = append(commands, "-iam=false")
		}
		// Note: IAM API is available on the same port as S3 (FilerS3Port) when embedded
	}
	if m.Spec.Filer.MetricsPort != nil {
		commands = append(commands, fmt.Sprintf("-metricsPort=%d", *m.Spec.Filer.MetricsPort))
	}
	commands = append(commands, extraArgs...)

	return strings.Join(commands, " ")
}

func (r *SeaweedReconciler) createFilerStatefulSet(m *seaweedv1.Seaweed) *appsv1.StatefulSet {
	labels := labelsForFiler(m.Name)
	annotations := m.Spec.Filer.Annotations
	ports := []corev1.ContainerPort{
		{
			ContainerPort: seaweedv1.FilerHTTPPort,
			Name:          "filer-http",
		},
		{
			ContainerPort: seaweedv1.FilerGRPCPort,
			Name:          "filer-grpc",
		},
	}
	if m.Spec.Filer.S3 != nil && m.Spec.Filer.S3.Enabled {
		ports = append(ports, corev1.ContainerPort{
			ContainerPort: seaweedv1.FilerS3Port,
			Name:          "filer-s3",
		})
		// Note: IAM is now embedded in S3 by default (same port as S3)
		// No separate IAM port needed when using embedded IAM
	}
	if m.Spec.Filer.MetricsPort != nil {
		ports = append(ports, corev1.ContainerPort{
			ContainerPort: *m.Spec.Filer.MetricsPort,
			Name:          "filer-metrics",
		})
	}
	replicas := int32(m.Spec.Filer.Replicas)
	rollingUpdatePartition := int32(0)
	enableServiceLinks := false

	filerPodSpec := m.BaseFilerSpec().BuildPodSpec()
	filerPodSpec.Volumes = append(filerPodSpec.Volumes, corev1.Volume{
		Name: "filer-config",
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: m.Name + "-filer",
				},
			},
		},
	})
	volumeMounts := []corev1.VolumeMount{
		{
			Name:      "filer-config",
			ReadOnly:  true,
			MountPath: "/etc/seaweedfs",
		},
	}

	if m.Spec.Filer.S3 != nil && m.Spec.Filer.S3.Enabled && m.Spec.Filer.S3.ConfigSecret != nil && m.Spec.Filer.S3.ConfigSecret.Name != "" {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "config-users",
			ReadOnly:  true,
			MountPath: "/etc/sw",
		})
		filerPodSpec.Volumes = append(filerPodSpec.Volumes,
			corev1.Volume{
				Name: "config-users",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: m.Spec.Filer.S3.ConfigSecret.Name,
					},
				},
			})
	}

	var persistentVolumeClaims []corev1.PersistentVolumeClaim
	if m.Spec.Filer.Persistence != nil && m.Spec.Filer.Persistence.Enabled {
		claimName := m.Name + "-filer"
		if m.Spec.Filer.Persistence.ExistingClaim != nil {
			claimName = *m.Spec.Filer.Persistence.ExistingClaim
		}
		if m.Spec.Filer.Persistence.ExistingClaim == nil {
			accessModes := m.Spec.Filer.Persistence.AccessModes
			if len(accessModes) == 0 {
				accessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
			}
			persistentVolumeClaims = append(persistentVolumeClaims, corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: claimName,
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes:      accessModes,
					Resources:        m.Spec.Filer.Persistence.Resources,
					StorageClassName: m.Spec.Filer.Persistence.StorageClassName,
					Selector:         m.Spec.Filer.Persistence.Selector,
					VolumeName:       m.Spec.Filer.Persistence.VolumeName,
					VolumeMode:       m.Spec.Filer.Persistence.VolumeMode,
					DataSource:       m.Spec.Filer.Persistence.DataSource,
				},
			})
		} else {
			filerPodSpec.Volumes = append(filerPodSpec.Volumes, corev1.Volume{
				Name: claimName,
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: claimName,
						ReadOnly:  false,
					},
				},
			})
		}
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      claimName,
			ReadOnly:  false,
			MountPath: *m.Spec.Filer.Persistence.MountPath,
			SubPath:   *m.Spec.Filer.Persistence.SubPath,
		})
	}
	filerPodSpec.EnableServiceLinks = &enableServiceLinks
	filerPodSpec.Containers = []corev1.Container{{
		Name:            "filer",
		Image:           m.Spec.Image,
		ImagePullPolicy: m.BaseFilerSpec().ImagePullPolicy(),
		Env:             append(m.BaseFilerSpec().Env(), kubernetesEnvVars...),
		Resources:       filterContainerResources(m.Spec.Filer.ResourceRequirements),
		VolumeMounts:    mergeVolumeMounts(volumeMounts, m.BaseFilerSpec().VolumeMounts()),
		Command: []string{
			"/bin/sh",
			"-ec",
			buildFilerStartupScript(m, m.BaseFilerSpec().ExtraArgs()...),
		},
		Ports: ports,
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   "/",
					Port:   intstr.FromInt(seaweedv1.FilerHTTPPort),
					Scheme: corev1.URISchemeHTTP,
				},
			},
			InitialDelaySeconds: 10,
			TimeoutSeconds:      3,
			PeriodSeconds:       15,
			SuccessThreshold:    1,
			FailureThreshold:    100,
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   "/",
					Port:   intstr.FromInt(seaweedv1.FilerHTTPPort),
					Scheme: corev1.URISchemeHTTP,
				},
			},
			InitialDelaySeconds: 20,
			TimeoutSeconds:      3,
			PeriodSeconds:       30,
			SuccessThreshold:    1,
			FailureThreshold:    6,
		},
	}}

	dep := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-filer",
			Namespace: m.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName:         m.Name + "-filer-peer",
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
				Spec: filerPodSpec,
			},
			VolumeClaimTemplates: persistentVolumeClaims,
		},
	}
	return dep
}
