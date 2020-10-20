package controllers

import (
	"fmt"
	"log"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createVolumeServerStatefulSet(m *seaweedv1.Seaweed) *appsv1.StatefulSet {
	labels := labelsForVolumeServer(m.Name)
	replicas := int32(m.Spec.VolumeServerCount)
	rollingUpdatePartition := int32(0)
	enableServiceLinks := false

	volumeQuantity := fmt.Sprintf("%dGi", m.Spec.VolumeServerDiskSizeInGiB)
	volumeCount := int(m.Spec.VolumeServerDiskCount)
	quantity, err := resource.ParseQuantity(volumeQuantity)
	if err != nil {
		log.Fatalf("can not parse quantity %s", volumeQuantity)
	}
	volumeRequests := make(corev1.ResourceList)
	volumeRequests[corev1.ResourceStorage] = quantity

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
				AccessModes: []corev1.PersistentVolumeAccessMode{
					corev1.ReadWriteOnce,
				},
				Resources: corev1.ResourceRequirements{
					Requests: volumeRequests,
				},
			},
		})
		dirs = append(dirs, fmt.Sprintf("/data%d", i))
	}

	dep := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-volume",
			Namespace: m.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName:         m.Name + "-volume",
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
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					EnableServiceLinks: &enableServiceLinks,
					Containers: []corev1.Container{{
						Name:            "seaweedfs",
						Image:           m.Spec.Image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Env: []corev1.EnvVar{
							{
								Name: "POD_IP",
								ValueFrom: &corev1.EnvVarSource{
									FieldRef: &corev1.ObjectFieldSelector{
										FieldPath: "status.podIP",
									},
								},
							},
							{
								Name: "POD_NAME",
								ValueFrom: &corev1.EnvVarSource{
									FieldRef: &corev1.ObjectFieldSelector{
										FieldPath: "metadata.name",
									},
								},
							},
							{
								Name: "NAMESPACE",
								ValueFrom: &corev1.EnvVarSource{
									FieldRef: &corev1.ObjectFieldSelector{
										FieldPath: "metadata.namespace",
									},
								},
							},
						},
						Command: []string{
							"/bin/sh",
							"-ec",
							fmt.Sprintf("weed volume -port=8444 -max=0 %s %s %s",
								fmt.Sprintf("-ip=$(POD_NAME).%s-volume", m.Name),
								fmt.Sprintf("-dir=%s", strings.Join(dirs, ",")),
								fmt.Sprintf("-mserver=%s-master-0.%s-master:9333,%s-master-1.%s-master:9333,%s-master-2.%s-master:9333",
									m.Name, m.Name, m.Name, m.Name, m.Name, m.Name),
							),
						},
						Ports: []corev1.ContainerPort{
							{
								ContainerPort: 8444,
								Name:          "swfs-volume",
							},
							{
								ContainerPort: 18444,
							},
						},
						ReadinessProbe: &corev1.Probe{
							Handler: corev1.Handler{
								HTTPGet: &corev1.HTTPGetAction{
									Path:   "/status",
									Port:   intstr.FromInt(8444),
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
							Handler: corev1.Handler{
								HTTPGet: &corev1.HTTPGetAction{
									Path:   "/status",
									Port:   intstr.FromInt(8444),
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
					}},

					Volumes: volumes,
				},
			},
			VolumeClaimTemplates: persistentVolumeClaims,
		},
	}
	return dep
}
