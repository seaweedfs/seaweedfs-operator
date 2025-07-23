package controller

import (
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func buildFilerBackupStartupScript(m *seaweedv1.Seaweed) string {
	filerAddress := fmt.Sprintf("%s-filer:8888", m.Name)

	commands := []string{"weed", "-logtostderr=true", "filer.backup", "-filer=" + filerAddress}

	return strings.Join(commands, " ")
}

// buildStubProbeHandler creates a ProbeHandler that uses Exec with echo command
func buildStubProbeHandler() corev1.ProbeHandler {
	return corev1.ProbeHandler{
		Exec: &corev1.ExecAction{
			Command: []string{
				"/bin/sh",
				"-c",
				"echo OK",
			},
		},
	}
}

func (r *SeaweedReconciler) createFilerBackupStatefulSet(m *seaweedv1.Seaweed) *appsv1.StatefulSet {
	labels := labelsForFilerBackup(m.Name)
	annotations := m.Spec.FilerBackup.Annotations

	replicas := int32(m.Spec.FilerBackup.Replicas)
	rollingUpdatePartition := int32(0)
	enableServiceLinks := false

	filerBackupPodSpec := m.BaseFilerBackupSpec().BuildPodSpec()
	filerBackupPodSpec.Volumes = []corev1.Volume{
		{
			Name: "filer-backup-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: m.Name + "-filer-backup",
					},
				},
			},
		},
	}
	volumeMounts := []corev1.VolumeMount{
		{
			Name:      "filer-backup-config",
			ReadOnly:  true,
			MountPath: "/etc/seaweedfs",
		},
	}
	var persistentVolumeClaims []corev1.PersistentVolumeClaim
	if m.Spec.FilerBackup.Persistence != nil && m.Spec.FilerBackup.Persistence.Enabled {
		claimName := m.Name + "-filer-backup"
		if m.Spec.FilerBackup.Persistence.ExistingClaim != nil {
			claimName = *m.Spec.FilerBackup.Persistence.ExistingClaim
		}
		if m.Spec.FilerBackup.Persistence.ExistingClaim == nil {
			persistentVolumeClaims = append(persistentVolumeClaims, corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: claimName,
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes:      m.Spec.FilerBackup.Persistence.AccessModes,
					Resources:        m.Spec.FilerBackup.Persistence.Resources,
					StorageClassName: resolveStorageClassName(m.Spec.Storage.StorageClassName, m.Spec.FilerBackup.Persistence.StorageClassName),
					Selector:         m.Spec.FilerBackup.Persistence.Selector,
					VolumeName:       m.Spec.FilerBackup.Persistence.VolumeName,
					VolumeMode:       m.Spec.FilerBackup.Persistence.VolumeMode,
					DataSource:       m.Spec.FilerBackup.Persistence.DataSource,
				},
			})
		}
		filerBackupPodSpec.Volumes = append(filerBackupPodSpec.Volumes, corev1.Volume{
			Name: claimName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: claimName,
					ReadOnly:  false,
				},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      claimName,
			ReadOnly:  false,
			MountPath: *m.Spec.FilerBackup.Persistence.MountPath,
			SubPath:   *m.Spec.FilerBackup.Persistence.SubPath,
		})
	}
	filerBackupPodSpec.EnableServiceLinks = &enableServiceLinks
	filerBackupPodSpec.Containers = []corev1.Container{{
		Name:            "filer-backup",
		Image:           m.Spec.Image,
		ImagePullPolicy: m.BaseFilerBackupSpec().ImagePullPolicy(),
		Env:             append(m.BaseFilerBackupSpec().Env(), kubernetesEnvVars...),
		VolumeMounts:    volumeMounts,
		Command: []string{
			"/bin/sh",
			"-ec",
			buildFilerBackupStartupScript(m),
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler:        buildStubProbeHandler(),
			InitialDelaySeconds: 10,
			TimeoutSeconds:      3,
			PeriodSeconds:       15,
			SuccessThreshold:    1,
			FailureThreshold:    20,
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler:        buildStubProbeHandler(),
			InitialDelaySeconds: 20,
			TimeoutSeconds:      3,
			PeriodSeconds:       30,
			SuccessThreshold:    1,
			FailureThreshold:    6,
		},
	}}

	dep := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-filer-backup",
			Namespace: m.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName:         m.Name + "-filer-backup-peer",
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
				Spec: filerBackupPodSpec,
			},
			VolumeClaimTemplates: persistentVolumeClaims,
		},
	}
	return dep
}
