package controller

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

const (
	masterPeerAddressPattern = "%s-master-%d.%s-master-peer.%s:9333"
)

var (
	kubernetesEnvVars = []corev1.EnvVar{
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
	}
)

func ReconcileResult(err error) (bool, ctrl.Result, error) {
	if err != nil {
		return true, ctrl.Result{}, err
	}
	return false, ctrl.Result{}, nil
}

func getMasterAddresses(namespace string, name string, replicas int32) []string {
	peersAddresses := make([]string, 0, replicas)
	for i := int32(0); i < replicas; i++ {
		peersAddresses = append(peersAddresses, fmt.Sprintf(masterPeerAddressPattern, name, i, name, namespace))
	}
	return peersAddresses
}

func getMasterPeersString(m *seaweedv1.Seaweed) string {
	return strings.Join(getMasterAddresses(m.Namespace, m.Name, m.Spec.Master.Replicas), ",")
}

// Note: IAM is now embedded in S3 by default (on the same port as S3: FilerS3Port).
// The getIAMPort function has been removed since standalone IAM is no longer supported.

func copyAnnotations(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}
	dst := map[string]string{}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// mergeAnnotations merges cluster-level annotations with component-level annotations
// Component-level annotations take precedence over cluster-level ones
func mergeAnnotations(clusterAnnotations, componentAnnotations map[string]string) map[string]string {
	if clusterAnnotations == nil && componentAnnotations == nil {
		return nil
	}

	merged := map[string]string{}

	// Add cluster-level annotations first
	for k, v := range clusterAnnotations {
		merged[k] = v
	}

	// Override with component-level annotations
	for k, v := range componentAnnotations {
		merged[k] = v
	}

	return merged
}

// mergeNodeSelector merges cluster-level nodeSelector with component-level nodeSelector
// Component-level nodeSelector takes precedence over cluster-level ones
func mergeNodeSelector(clusterNodeSelector, componentNodeSelector map[string]string) map[string]string {
	if clusterNodeSelector == nil && componentNodeSelector == nil {
		return nil
	}

	merged := map[string]string{}

	// Add cluster-level nodeSelector first
	for k, v := range clusterNodeSelector {
		merged[k] = v
	}

	// Override with component-level nodeSelector
	for k, v := range componentNodeSelector {
		merged[k] = v
	}

	return merged
}

// filterContainerResources removes storage resources that are not valid for container specifications
// while keeping resources like ephemeral-storage that are valid for containers
func filterContainerResources(resources corev1.ResourceRequirements) corev1.ResourceRequirements {
	filtered := corev1.ResourceRequirements{}

	if resources.Requests != nil {
		filtered.Requests = corev1.ResourceList{}
		for resource, quantity := range resources.Requests {
			// Exclude storage resources that are only valid for PVCs
			if resource != corev1.ResourceStorage {
				filtered.Requests[resource] = quantity
			}
		}
	}

	if resources.Limits != nil {
		filtered.Limits = corev1.ResourceList{}
		for resource, quantity := range resources.Limits {
			// Exclude storage resources that are only valid for PVCs
			if resource != corev1.ResourceStorage {
				filtered.Limits[resource] = quantity
			}
		}
	}

	return filtered
}

// getStorageClassName returns the storage class name with fallback logic
func getStorageClassName(m *seaweedv1.Seaweed, topologySpec *seaweedv1.VolumeTopologySpec) *string {
	if topologySpec != nil && topologySpec.StorageClassName != nil {
		return topologySpec.StorageClassName
	}
	if m.Spec.Volume != nil && m.Spec.Volume.StorageClassName != nil {
		return m.Spec.Volume.StorageClassName
	}
	return nil
}

// getResourceRequirements returns the resource requirements with fallback logic
func getResourceRequirements(m *seaweedv1.Seaweed, topologySpec *seaweedv1.VolumeTopologySpec) corev1.ResourceRequirements {
	// Start with base resources from spec.volume, if available
	resources := corev1.ResourceRequirements{}
	if m.Spec.Volume != nil {
		resources = m.Spec.Volume.ResourceRequirements
	}

	// If no topology spec, return base
	if topologySpec == nil {
		return resources
	}

	// Override with topology-specific resources if they are provided
	if len(topologySpec.ResourceRequirements.Requests) > 0 {
		resources.Requests = topologySpec.ResourceRequirements.Requests
	}
	if len(topologySpec.ResourceRequirements.Limits) > 0 {
		resources.Limits = topologySpec.ResourceRequirements.Limits
	}

	return resources
}

// getMetricsPort returns the metrics port with fallback logic
func getMetricsPort(m *seaweedv1.Seaweed, topologySpec *seaweedv1.VolumeTopologySpec) *int32 {
	if topologySpec != nil && topologySpec.MetricsPort != nil {
		return topologySpec.MetricsPort
	}
	if m.Spec.Volume != nil && m.Spec.Volume.MetricsPort != nil {
		return m.Spec.Volume.MetricsPort
	}
	return nil
}

// getServiceSpec returns the service spec with fallback logic
func getServiceSpec(m *seaweedv1.Seaweed, topologySpec *seaweedv1.VolumeTopologySpec) *seaweedv1.ServiceSpec {
	if topologySpec != nil && topologySpec.Service != nil {
		return topologySpec.Service
	}
	if m.Spec.Volume != nil && m.Spec.Volume.Service != nil {
		return m.Spec.Volume.Service
	}
	return nil
}

// getVolumeServerConfigValue returns volume server config values with fallback logic
func getVolumeServerConfigValue[T any](topologyValue, volumeValue *T) *T {
	if topologyValue != nil {
		return topologyValue
	}
	return volumeValue
}
