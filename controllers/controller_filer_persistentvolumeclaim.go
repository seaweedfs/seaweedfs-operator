package controllers

import (
	corev1 "k8s.io/api/core/v1"
	resource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createFilerPersistentVolumeClaim(m *seaweedv1.Seaweed) *corev1.PersistentVolumeClaim {
	if !m.Spec.Filer.Persistence.Enabled {
		return &corev1.PersistentVolumeClaim{}
	}

	if m.Spec.Filer.Persistence.ExistingClaim != nil {
		return &corev1.PersistentVolumeClaim{}
	}

	labels := labelsForFiler(m.Name)

	accessModes := []corev1.PersistentVolumeAccessMode{
		corev1.ReadWriteOnce,
	}

	if m.Spec.Filer.Persistence.AccessModes != nil {
		accessModes = m.Spec.Filer.Persistence.AccessModes
	}

	resources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceStorage: *resource.NewQuantity(4, "Gi"),
		},
	}
	if m.Spec.Filer.Persistence.Resources.Requests != nil {
		resources = m.Spec.Filer.Persistence.Resources
	}

	dep := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-filer",
			Namespace: m.Namespace,
			Labels:    labels,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      accessModes,
			Resources:        resources,
			StorageClassName: m.Spec.Filer.Persistence.StorageClassName,
			Selector:         m.Spec.Filer.Persistence.Selector,
			DataSource:       m.Spec.Filer.Persistence.DataSource,
		},
	}

	return dep
}
