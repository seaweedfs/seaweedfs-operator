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

// getIAMPort returns the IAM port to use, checking both standalone and embedded configurations
func getIAMPort(m *seaweedv1.Seaweed) int32 {
	iamPort := int32(seaweedv1.FilerIAMPort)
	if m.Spec.IAM != nil && m.Spec.IAM.Port != nil {
		iamPort = *m.Spec.IAM.Port
	}
	return iamPort
}

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
