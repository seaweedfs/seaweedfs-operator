package controller

import (
	appsv1 "k8s.io/api/apps/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// pvcRetentionPolicy renders spec.enablePVReclaim as a StatefulSet
// persistentVolumeClaimRetentionPolicy.
//
// whenScaled is the knob the field describes: it decides the fate of the PVCs
// a scale-in orphans. whenDeleted is pinned to Retain so tearing down the CR
// never takes the data with it — that path is not what the field promises, and
// silently deleting on cluster delete would be an unpleasant surprise.
//
// The policy is always returned, rather than left nil when disabled, so that
// flipping the field back off actually restores Retain on a StatefulSet that
// already carries Delete. Retain/Retain is also the Kubernetes default, so
// emitting it explicitly changes nothing for clusters that never set the field.
func pvcRetentionPolicy(m *seaweedv1.Seaweed) *appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy {
	whenScaled := appsv1.RetainPersistentVolumeClaimRetentionPolicyType
	if m.Spec.EnablePVReclaim != nil && *m.Spec.EnablePVReclaim {
		whenScaled = appsv1.DeletePersistentVolumeClaimRetentionPolicyType
	}
	return &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
		WhenDeleted: appsv1.RetainPersistentVolumeClaimRetentionPolicyType,
		WhenScaled:  whenScaled,
	}
}
