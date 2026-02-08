package controller

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

const (
	masterPeerAddressPattern = "%s-master-%d.%s-master-peer.%s.svc.cluster.local:9333"
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

// getMQBrokerServiceAddress returns the MQ broker service address for agent connection
func getMQBrokerServiceAddress(m *seaweedv1.Seaweed) string {
	port := seaweedv1.MQBrokerGRPCPort
	if m.Spec.MessageQueue.Broker.Port != nil {
		port = int(*m.Spec.MessageQueue.Broker.Port)
	}
	return fmt.Sprintf("%s-mq-broker.%s.svc.cluster.local:%d", m.Name, m.Namespace, port)
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

// resolveStorageClassName returns the component-specific storage class name if set,
// otherwise falls back to the global storage class name
func resolveStorageClassName(globalStorageClassName, componentStorageClassName *string) *string {
	if componentStorageClassName != nil {
		return componentStorageClassName
	}
	return globalStorageClassName
}

// resolveMetricsPort returns the metrics port for a component
// if the global metrics is enabled, it returns the global metrics port
// otherwise it returns the component-specific metrics port
func resolveMetricsPort(m *seaweedv1.Seaweed, componentMetricsPort *int32) *int32 {
	if m.Spec.Metrics != nil && m.Spec.Metrics.Enabled {
		if m.Spec.Metrics.MetricsPort != nil {
			return m.Spec.Metrics.MetricsPort
		}

		var defaultMetricsPort int32 = 5555

		return &defaultMetricsPort
	} else if componentMetricsPort != nil {
		return componentMetricsPort
	}

	return nil
}
