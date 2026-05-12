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

// getFilerAddress returns the HTTP host:port for the Seaweed CR's filer Service.
// Required for s3.bucket.* shell commands; seaweedfs adds GRPCPortDelta internally.
func getFilerAddress(m *seaweedv1.Seaweed) string {
	return fmt.Sprintf("%s-filer.%s:%d", m.Name, m.Namespace, seaweedv1.FilerHTTPPort)
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

// mergePodLabels returns the pod template label set: user-supplied labels first,
// then operator-managed selector labels layered on top so the StatefulSet/Deployment
// selector keeps matching its pods even when the user supplies a colliding key.
func mergePodLabels(selectorLabels, userLabels map[string]string) map[string]string {
	if len(userLabels) == 0 {
		return selectorLabels
	}
	merged := map[string]string{}
	for k, v := range userLabels {
		merged[k] = v
	}
	for k, v := range selectorLabels {
		merged[k] = v
	}
	return merged
}

// mergeLabels merges two label sets. The second argument takes precedence
// over the first on key collisions — used to layer component- or
// topology-level labels on top of cluster/volume-level defaults.
func mergeLabels(base, override map[string]string) map[string]string {
	if base == nil && override == nil {
		return nil
	}

	merged := map[string]string{}
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range override {
		merged[k] = v
	}

	return merged
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

func mergeVolumeMounts(base, override []corev1.VolumeMount) []corev1.VolumeMount {
	m := make(map[string]struct{})
	for _, vm := range override {
		m[vm.MountPath] = struct{}{}
	}

	merged := make([]corev1.VolumeMount, 0, len(base)+len(override))
	for _, vm := range base {
		if _, exists := m[vm.MountPath]; !exists {
			merged = append(merged, vm)
		}
	}
	merged = append(merged, override...)
	return merged
}

// tlsEffective reports whether TLS should actually be wired into this
// reconcile pass. In addition to the user's Spec.TLS.Enabled flag it
// requires the cert-manager CRD probe to have succeeded — otherwise
// the operator has not (and cannot) reconcile the Secret/ConfigMap
// that the pod mounts would reference, so adding those mounts would
// pin the pod in ContainerCreating with a missing-volume-source error.
//
// Keeping this gated on the cached probe means TLS.Enabled=true in a
// cluster without cert-manager silently degrades to "no TLS" rather
// than breaking the whole cluster — matching the soft no-op contract
// documented on TLSSpec.
func tlsEffective(m *seaweedv1.Seaweed) bool {
	return tlsEnabled(m) && certManagerAvailableCached()
}

// tlsVolumesAndMounts returns the set of pod volumes and container mounts
// that wire the shared TLS Secret and security.toml ConfigMap into a
// component pod. Returns empty slices when TLS is disabled or the
// cert-manager CRD probe has failed — callers can safely append
// unconditionally. Every component that speaks gRPC should use this
// helper so the paths stay in sync with renderSecurityTOML.
func tlsVolumesAndMounts(m *seaweedv1.Seaweed) ([]corev1.Volume, []corev1.VolumeMount) {
	if !tlsEffective(m) {
		return nil, nil
	}
	volumes := []corev1.Volume{
		{
			Name: tlsVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: TLSServerSecretName(m),
				},
			},
		},
		{
			Name: securityVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: SecurityConfigMapName(m),
					},
				},
			},
		},
	}
	mounts := []corev1.VolumeMount{
		{
			Name:      tlsVolumeName,
			ReadOnly:  true,
			MountPath: tlsMountPath,
		},
		{
			Name:      securityVolumeName,
			ReadOnly:  true,
			MountPath: securityConfigMountPath,
		},
	}
	return volumes, mounts
}

// tlsConfigDirArg returns the additional top-level `weed` flag that tells
// viper to look in the security config mount path, or the empty string
// when TLS is not effective (disabled by spec or cert-manager absent).
func tlsConfigDirArg(m *seaweedv1.Seaweed) string {
	if !tlsEffective(m) {
		return ""
	}
	return "-config_dir=" + securityConfigMountPath
}
