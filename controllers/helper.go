package controllers

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	masterPeerAddressPattern                 = "%s-master-%d.%s-master-peer:9333"
	filerPeerAddressPattern                  = "%s-filer-%d.%s-filer-peer:9333"
	masterPeerAddressWithNamespacePattern    = "%s-master-%d.%s-master-peer.%s:9333"
	filerServiceAddressWithNamespacePattern  = "%s-filer.%s:8888"
	masterServiceAddressWithNamespacePattern = "%s-master.%s:9333"
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

func getFilerAddresses(name string, replicas int32) []string {
	peersAddresses := make([]string, 0, replicas)
	for i := int32(0); i < replicas; i++ {
		peersAddresses = append(peersAddresses, fmt.Sprintf(filerPeerAddressPattern, name, i, name))
	}
	return peersAddresses
}

func getFilerPeersString(name string, replicas int32) string {
	return strings.Join(getFilerAddresses(name, replicas), ",")
}

func getMasterAddresses(name string, replicas int32) []string {
	peersAddresses := make([]string, 0, replicas)
	for i := int32(0); i < replicas; i++ {
		peersAddresses = append(peersAddresses, fmt.Sprintf(masterPeerAddressPattern, name, i, name))
	}
	return peersAddresses
}

func getMasterPeersString(name string, replicas int32) string {
	return strings.Join(getMasterAddresses(name, replicas), ",")
}

func getMasterAddressesWithNamespace(name, namespace string, replicas int32) []string {
	peersAddresses := make([]string, 0, replicas)
	for i := int32(0); i < replicas; i++ {
		peersAddresses = append(peersAddresses, fmt.Sprintf(masterPeerAddressWithNamespacePattern, name, i, name, namespace))
	}
	return peersAddresses
}

func getMasterPeersStringWithNamespace(name, namespace string, replicas int32) string {
	return strings.Join(getMasterAddressesWithNamespace(name, namespace, replicas), ",")
}

func getFilerServiceAddressWithNamespace(name, namespace string) string {
	return fmt.Sprintf(filerServiceAddressWithNamespacePattern, name, namespace)
}

func getMasterServiceAddressWithNamespace(name, namespace string) string {
	return fmt.Sprintf(masterServiceAddressWithNamespacePattern, name, namespace)
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
