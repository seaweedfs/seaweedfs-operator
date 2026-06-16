/*


Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/internal/controller/label"
)

// Socket paths and ports mirror the upstream seaweedfs-csi-driver deployment
// so the rendered pods are byte-compatible with what the driver expects.
const (
	csiControllerSocketDir  = "/var/lib/csi/sockets/pluginproxy"
	csiControllerSocketPath = csiControllerSocketDir + "/csi.sock"
	csiNodeSocketDir        = "/csi"
	csiNodeSocketPath       = "unix:///csi/csi.sock"
	csiRegistrationDir      = "/registration"
	csiCacheDir             = "/var/cache/seaweedfs"
	csiMountSocketName      = "seaweedfs-mount.sock"

	// Sidecar healthz ports, distinct so they can coexist in one pod.
	csiPortLiveness    = 9808
	csiPortProvisioner = 9809
	csiPortRegistrar   = 9809
	csiPortResizer     = 9810
	csiPortAttacher    = 9811

	pluginContainerName = "csi-seaweedfs-plugin"
)

var (
	mountPropagationBidirectional = corev1.MountPropagationBidirectional
	hostPathDirectory             = corev1.HostPathDirectory
	hostPathDirectoryOrCreate     = corev1.HostPathDirectoryOrCreate
)

// --- naming -----------------------------------------------------------------
//
// Every object is named with the CR's name so multiple SeaweedCSIDriver
// objects (and the upstream Helm chart) can coexist without colliding on the
// cluster-scoped RBAC and workload names.

func csiName(driver *seaweedv1.SeaweedCSIDriver) string { return "seaweedfs-csi-" + driver.Name }

func csiControllerName(driver *seaweedv1.SeaweedCSIDriver) string {
	return csiName(driver) + "-controller"
}
func csiNodeName(driver *seaweedv1.SeaweedCSIDriver) string  { return csiName(driver) + "-node" }
func csiMountName(driver *seaweedv1.SeaweedCSIDriver) string { return csiName(driver) + "-mount" }
func csiControllerSAName(driver *seaweedv1.SeaweedCSIDriver) string {
	return csiName(driver) + "-controller"
}
func csiNodeSAName(driver *seaweedv1.SeaweedCSIDriver) string { return csiName(driver) + "-node" }

func storageClassName(driver *seaweedv1.SeaweedCSIDriver) string {
	if driver.Spec.StorageClass != nil && driver.Spec.StorageClass.Name != "" {
		return driver.Spec.StorageClass.Name
	}
	return driver.Spec.DriverName
}

// --- labels -----------------------------------------------------------------

func csiSelector(driver *seaweedv1.SeaweedCSIDriver, component string) map[string]string {
	return map[string]string{
		label.NameLabelKey:      "seaweedfs-csi-driver",
		label.InstanceLabelKey:  driver.Name,
		label.ComponentLabelKey: component,
		label.ManagedByLabelKey: "seaweedfs-operator",
	}
}

// csiInstanceLabels returns the labels shared by every object of an instance.
// The instance label is what the finalizer uses to garbage-collect the
// cluster-scoped objects the operator cannot own via owner references.
func csiInstanceLabels(driver *seaweedv1.SeaweedCSIDriver) map[string]string {
	return map[string]string{
		label.NameLabelKey:      "seaweedfs-csi-driver",
		label.InstanceLabelKey:  driver.Name,
		label.ManagedByLabelKey: "seaweedfs-operator",
	}
}

// --- image resolution -------------------------------------------------------

func csiPullPolicy(driver *seaweedv1.SeaweedCSIDriver) corev1.PullPolicy {
	if driver.Spec.ImagePullPolicy != nil {
		return *driver.Spec.ImagePullPolicy
	}
	return corev1.PullIfNotPresent
}

func provisionerImage(driver *seaweedv1.SeaweedCSIDriver) string {
	return orDefault(driver.Spec.Sidecars.Provisioner, "registry.k8s.io/sig-storage/csi-provisioner:v3.5.0")
}
func resizerImage(driver *seaweedv1.SeaweedCSIDriver) string {
	return orDefault(driver.Spec.Sidecars.Resizer, "registry.k8s.io/sig-storage/csi-resizer:v1.8.0")
}
func attacherImage(driver *seaweedv1.SeaweedCSIDriver) string {
	return orDefault(driver.Spec.Sidecars.Attacher, "registry.k8s.io/sig-storage/csi-attacher:v4.3.0")
}
func nodeDriverRegistrarImage(driver *seaweedv1.SeaweedCSIDriver) string {
	return orDefault(driver.Spec.Sidecars.NodeDriverRegistrar, "registry.k8s.io/sig-storage/csi-node-driver-registrar:v2.8.0")
}
func livenessProbeImage(driver *seaweedv1.SeaweedCSIDriver) string {
	return orDefault(driver.Spec.Sidecars.LivenessProbe, "registry.k8s.io/sig-storage/livenessprobe:v2.10.0")
}

func orDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

// --- driver argument/env helpers --------------------------------------------

func attacherEnabled(driver *seaweedv1.SeaweedCSIDriver) bool {
	return driver.Spec.Controller.AttacherEnabled == nil || *driver.Spec.Controller.AttacherEnabled
}

func kubeletPath(driver *seaweedv1.SeaweedCSIDriver) string {
	if driver.Spec.Node.KubeletPath != "" {
		return driver.Spec.Node.KubeletPath
	}
	return seaweedv1.DefaultKubeletPath
}

func mountSocketDir(driver *seaweedv1.SeaweedCSIDriver) string {
	if driver.Spec.MountService.SocketDir != "" {
		return driver.Spec.MountService.SocketDir
	}
	return seaweedv1.DefaultMountSocketDir
}

func mountEndpoint(driver *seaweedv1.SeaweedCSIDriver) string {
	return fmt.Sprintf("unix://%s/%s", mountSocketDir(driver), csiMountSocketName)
}

// tuningArgs appends the optional driver tuning flags shared by the plugin
// containers.
func tuningArgs(driver *seaweedv1.SeaweedCSIDriver, args []string) []string {
	if driver.Spec.LogVerbosity != nil {
		args = append(args, fmt.Sprintf("-v=%d", *driver.Spec.LogVerbosity))
	}
	if driver.Spec.CacheCapacityMB != nil {
		args = append(args, fmt.Sprintf("--cacheCapacityMB=%d", *driver.Spec.CacheCapacityMB))
	}
	if driver.Spec.ConcurrentWriters != nil {
		args = append(args, fmt.Sprintf("--concurrentWriters=%d", *driver.Spec.ConcurrentWriters))
	}
	if driver.Spec.ConcurrentReaders != nil {
		args = append(args, fmt.Sprintf("--concurrentReaders=%d", *driver.Spec.ConcurrentReaders))
	}
	return args
}

// nodeNameEnv returns an env var sourcing the scheduling node name.
func nodeNameEnv(name string) corev1.EnvVar {
	return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"}}}
}

func sidecarLivenessProbe(port int32, path string) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{Path: path, Port: intstr.FromInt32(port)},
		},
		InitialDelaySeconds: 10,
		TimeoutSeconds:      3,
		PeriodSeconds:       60,
	}
}

// --- controller Deployment --------------------------------------------------

func buildControllerDeployment(driver *seaweedv1.SeaweedCSIDriver, dep *appsv1.Deployment, filerAddress string) {
	selector := csiSelector(driver, "controller")
	dep.Labels = selector
	dep.Spec.Replicas = driver.Spec.Controller.Replicas
	dep.Spec.Selector = &metav1.LabelSelector{MatchLabels: selector}
	dep.Spec.Template.ObjectMeta.Labels = selector

	socketVol := corev1.Volume{Name: "socket-dir", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}
	socketMount := corev1.VolumeMount{Name: "socket-dir", MountPath: csiControllerSocketDir}

	pluginArgs := []string{
		"--endpoint=$(CSI_ENDPOINT)",
		fmt.Sprintf("--filer=%s", filerAddress),
		fmt.Sprintf("--driverName=%s", driver.Spec.DriverName),
		"--components=controller",
		fmt.Sprintf("--attacher=%t", attacherEnabled(driver)),
	}

	containers := []corev1.Container{{
		Name:            "seaweedfs-csi-plugin",
		Image:           driver.Spec.Image,
		ImagePullPolicy: csiPullPolicy(driver),
		Args:            tuningArgs(driver, pluginArgs),
		Env:             []corev1.EnvVar{{Name: "CSI_ENDPOINT", Value: "unix://" + csiControllerSocketPath}},
		VolumeMounts:    []corev1.VolumeMount{socketMount},
		Resources:       driver.Spec.Controller.Resources,
	}}

	containers = append(containers,
		csiSidecar(driver, "csi-provisioner", provisionerImage(driver), csiControllerSocketPath, csiPortProvisioner, true, socketMount),
		csiSidecar(driver, "csi-resizer", resizerImage(driver), csiControllerSocketPath, csiPortResizer, true, socketMount),
	)
	if attacherEnabled(driver) {
		containers = append(containers, csiSidecar(driver, "csi-attacher", attacherImage(driver), csiControllerSocketPath, csiPortAttacher, true, socketMount))
	}
	containers = append(containers, csiLivenessSidecar(driver, csiControllerSocketPath, socketMount))

	pod := &dep.Spec.Template.Spec
	pod.ServiceAccountName = csiControllerSAName(driver)
	pod.PriorityClassName = "system-cluster-critical"
	pod.NodeSelector = driver.Spec.Controller.NodeSelector
	pod.Tolerations = driver.Spec.Controller.Tolerations
	pod.Affinity = controllerAffinity(driver, selector)
	pod.ImagePullSecrets = driver.Spec.ImagePullSecrets
	pod.Containers = containers
	pod.Volumes = []corev1.Volume{socketVol}
}

// csiSidecar renders a controller-plane sig-storage sidecar that talks to the
// driver over the shared controller socket.
func csiSidecar(driver *seaweedv1.SeaweedCSIDriver, name, image, socketPath string, port int32, leaderElection bool, socketMount corev1.VolumeMount) corev1.Container {
	args := []string{"--csi-address=$(ADDRESS)", fmt.Sprintf("--http-endpoint=:%d", port)}
	probePath := "/healthz"
	if leaderElection {
		args = append(args, "--leader-election", "--leader-election-namespace=$(NAMESPACE)")
		probePath = "/healthz/leader-election"
	}
	return corev1.Container{
		Name:            name,
		Image:           image,
		ImagePullPolicy: csiPullPolicy(driver),
		Args:            args,
		Env: []corev1.EnvVar{
			{Name: "ADDRESS", Value: socketPath},
			{Name: "NAMESPACE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}},
		},
		Ports:         []corev1.ContainerPort{{Name: "healthz", ContainerPort: port}},
		LivenessProbe: sidecarLivenessProbe(port, probePath),
		VolumeMounts:  []corev1.VolumeMount{socketMount},
	}
}

func csiLivenessSidecar(driver *seaweedv1.SeaweedCSIDriver, socketPath string, socketMount corev1.VolumeMount) corev1.Container {
	return corev1.Container{
		Name:            "csi-liveness-probe",
		Image:           livenessProbeImage(driver),
		ImagePullPolicy: csiPullPolicy(driver),
		Args:            []string{"--csi-address=$(ADDRESS)", fmt.Sprintf("--http-endpoint=:%d", csiPortLiveness)},
		Env:             []corev1.EnvVar{{Name: "ADDRESS", Value: socketPath}},
		Ports:           []corev1.ContainerPort{{Name: "livenessprobe", ContainerPort: csiPortLiveness}},
		VolumeMounts:    []corev1.VolumeMount{socketMount},
	}
}

func controllerAffinity(driver *seaweedv1.SeaweedCSIDriver, selector map[string]string) *corev1.Affinity {
	if driver.Spec.Controller.Affinity != nil {
		return driver.Spec.Controller.Affinity
	}
	// Soft anti-affinity: spread replicas across nodes without blocking
	// scheduling on small clusters.
	return &corev1.Affinity{
		PodAntiAffinity: &corev1.PodAntiAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
				Weight: 100,
				PodAffinityTerm: corev1.PodAffinityTerm{
					LabelSelector: &metav1.LabelSelector{MatchLabels: selector},
					TopologyKey:   "kubernetes.io/hostname",
				},
			}},
		},
	}
}

// --- node DaemonSet ---------------------------------------------------------

func buildNodeDaemonSet(driver *seaweedv1.SeaweedCSIDriver, ds *appsv1.DaemonSet, filerAddress string) {
	selector := csiSelector(driver, "node")
	kube := kubeletPath(driver)
	pluginDir := fmt.Sprintf("%s/plugins/%s", kube, driver.Spec.DriverName)

	ds.Labels = selector
	ds.Spec.Selector = &metav1.LabelSelector{MatchLabels: selector}
	ds.Spec.Template.ObjectMeta.Labels = selector
	ds.Spec.UpdateStrategy = daemonSetUpdateStrategy(driver.Spec.Node.UpdateStrategy, appsv1.RollingUpdateDaemonSetStrategyType)

	pluginArgs := []string{
		"--endpoint=$(CSI_ENDPOINT)",
		fmt.Sprintf("--filer=%s", filerAddress),
		"--nodeid=$(NODE_ID)",
		fmt.Sprintf("--driverName=%s", driver.Spec.DriverName),
		fmt.Sprintf("--mountEndpoint=%s", mountEndpoint(driver)),
		fmt.Sprintf("--cacheDir=%s", csiCacheDir),
		"--components=node",
	}

	plugin := corev1.Container{
		Name:            pluginContainerName,
		Image:           driver.Spec.Image,
		ImagePullPolicy: csiPullPolicy(driver),
		SecurityContext: privilegedSecurityContext(),
		Args:            tuningArgs(driver, pluginArgs),
		Env: []corev1.EnvVar{
			{Name: "CSI_ENDPOINT", Value: csiNodeSocketPath},
			nodeNameEnv("NODE_ID"),
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "plugin-dir", MountPath: csiNodeSocketDir},
			{Name: "plugins-dir", MountPath: kube + "/plugins", MountPropagation: &mountPropagationBidirectional},
			{Name: "pods-mount-dir", MountPath: kube + "/pods", MountPropagation: &mountPropagationBidirectional},
			{Name: "device-dir", MountPath: "/dev"},
			{Name: "cache", MountPath: csiCacheDir},
			{Name: "mount-socket-dir", MountPath: mountSocketDir(driver)},
		},
		Resources: driver.Spec.Node.Resources,
	}

	registrar := corev1.Container{
		Name:            "driver-registrar",
		Image:           nodeDriverRegistrarImage(driver),
		ImagePullPolicy: csiPullPolicy(driver),
		Args: []string{
			"--csi-address=$(ADDRESS)",
			"--kubelet-registration-path=$(DRIVER_REG_SOCK_PATH)",
			fmt.Sprintf("--http-endpoint=:%d", csiPortRegistrar),
		},
		Env: []corev1.EnvVar{
			{Name: "ADDRESS", Value: csiNodeSocketDir + "/csi.sock"},
			{Name: "DRIVER_REG_SOCK_PATH", Value: pluginDir + "/csi.sock"},
			nodeNameEnv("KUBE_NODE_NAME"),
		},
		Ports:         []corev1.ContainerPort{{Name: "healthz", ContainerPort: csiPortRegistrar}},
		LivenessProbe: sidecarLivenessProbe(csiPortRegistrar, "/healthz"),
		VolumeMounts: []corev1.VolumeMount{
			{Name: "plugin-dir", MountPath: csiNodeSocketDir},
			{Name: "registration-dir", MountPath: csiRegistrationDir},
		},
	}

	liveness := corev1.Container{
		Name:            "csi-liveness-probe",
		Image:           livenessProbeImage(driver),
		ImagePullPolicy: csiPullPolicy(driver),
		Args:            []string{"--csi-address=$(ADDRESS)", fmt.Sprintf("--http-endpoint=:%d", csiPortLiveness)},
		Env:             []corev1.EnvVar{{Name: "ADDRESS", Value: csiNodeSocketDir + "/csi.sock"}},
		Ports:           []corev1.ContainerPort{{Name: "livenessprobe", ContainerPort: csiPortLiveness}},
		VolumeMounts:    []corev1.VolumeMount{{Name: "plugin-dir", MountPath: csiNodeSocketDir}},
	}

	pod := &ds.Spec.Template.Spec
	pod.ServiceAccountName = csiNodeSAName(driver)
	pod.PriorityClassName = "system-node-critical"
	pod.HostPID = driver.Spec.Node.HostPID == nil || *driver.Spec.Node.HostPID
	pod.NodeSelector = driver.Spec.Node.NodeSelector
	pod.Tolerations = tolerateAll(driver.Spec.Node.Tolerations)
	pod.ImagePullSecrets = driver.Spec.ImagePullSecrets
	pod.Containers = []corev1.Container{plugin, registrar, liveness}
	pod.Volumes = []corev1.Volume{
		hostPathVolume("registration-dir", kube+"/plugins_registry", &hostPathDirectoryOrCreate),
		hostPathVolume("plugin-dir", pluginDir, &hostPathDirectoryOrCreate),
		hostPathVolume("plugins-dir", kube+"/plugins", &hostPathDirectory),
		hostPathVolume("pods-mount-dir", kube+"/pods", &hostPathDirectory),
		hostPathVolume("device-dir", "/dev", nil),
		{Name: "cache", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		hostPathVolume("mount-socket-dir", mountSocketDir(driver), &hostPathDirectoryOrCreate),
	}
}

// --- mount DaemonSet --------------------------------------------------------

func buildMountDaemonSet(driver *seaweedv1.SeaweedCSIDriver, ds *appsv1.DaemonSet) {
	selector := csiSelector(driver, "mount")
	kube := kubeletPath(driver)

	ds.Labels = selector
	ds.Spec.Selector = &metav1.LabelSelector{MatchLabels: selector}
	ds.Spec.Template.ObjectMeta.Labels = selector
	ds.Spec.UpdateStrategy = daemonSetUpdateStrategy(driver.Spec.MountService.UpdateStrategy, appsv1.OnDeleteDaemonSetStrategyType)

	mount := corev1.Container{
		Name:            "seaweedfs-mount",
		Image:           mountImage(driver),
		ImagePullPolicy: csiPullPolicy(driver),
		SecurityContext: privilegedSecurityContext(),
		Args:            []string{"--endpoint=$(MOUNT_ENDPOINT)"},
		Env:             []corev1.EnvVar{{Name: "MOUNT_ENDPOINT", Value: mountEndpoint(driver)}},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "plugins-dir", MountPath: kube + "/plugins", MountPropagation: &mountPropagationBidirectional},
			{Name: "pods-mount-dir", MountPath: kube + "/pods", MountPropagation: &mountPropagationBidirectional},
			{Name: "device-dir", MountPath: "/dev"},
			{Name: "cache", MountPath: csiCacheDir},
			{Name: "mount-socket-dir", MountPath: mountSocketDir(driver)},
		},
		Resources: driver.Spec.MountService.Resources,
	}

	pod := &ds.Spec.Template.Spec
	pod.ServiceAccountName = csiNodeSAName(driver)
	pod.PriorityClassName = "system-node-critical"
	pod.NodeSelector = driver.Spec.MountService.NodeSelector
	pod.Tolerations = tolerateAll(driver.Spec.MountService.Tolerations)
	pod.ImagePullSecrets = driver.Spec.ImagePullSecrets
	pod.Containers = []corev1.Container{mount}
	pod.Volumes = []corev1.Volume{
		hostPathVolume("plugins-dir", kube+"/plugins", &hostPathDirectory),
		hostPathVolume("pods-mount-dir", kube+"/pods", &hostPathDirectory),
		hostPathVolume("device-dir", "/dev", nil),
		{Name: "cache", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		hostPathVolume("mount-socket-dir", mountSocketDir(driver), &hostPathDirectoryOrCreate),
	}
}

func mountImage(driver *seaweedv1.SeaweedCSIDriver) string {
	return orDefault(driver.Spec.MountService.Image, seaweedv1.DefaultCSIMountImage)
}

func mountServiceEnabled(driver *seaweedv1.SeaweedCSIDriver) bool {
	return driver.Spec.MountService.Enabled == nil || *driver.Spec.MountService.Enabled
}

// --- RBAC -------------------------------------------------------------------

// buildControllerClusterRole renders the union of the upstream
// provisioner / attacher / driver-registrar (controller) roles, which the
// resizer also relies on.
func buildControllerClusterRole(driver *seaweedv1.SeaweedCSIDriver, cr *rbacv1.ClusterRole) {
	cr.Labels = csiInstanceLabels(driver)
	cr.Rules = []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get", "list"}},
		{APIGroups: []string{""}, Resources: []string{"persistentvolumes"}, Verbs: []string{"get", "list", "watch", "create", "delete", "update", "patch"}},
		{APIGroups: []string{""}, Resources: []string{"persistentvolumeclaims"}, Verbs: []string{"get", "list", "watch", "update"}},
		{APIGroups: []string{""}, Resources: []string{"persistentvolumeclaims/status"}, Verbs: []string{"get", "list", "watch", "update", "patch"}},
		{APIGroups: []string{""}, Resources: []string{"events"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch"}},
		{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: []string{"storage.k8s.io"}, Resources: []string{"storageclasses"}, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: []string{"storage.k8s.io"}, Resources: []string{"csinodes"}, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: []string{"storage.k8s.io"}, Resources: []string{"volumeattachments", "volumeattachments/status"}, Verbs: []string{"get", "list", "watch", "update", "patch"}},
		{APIGroups: []string{"snapshot.storage.k8s.io"}, Resources: []string{"volumesnapshots", "volumesnapshotcontents"}, Verbs: []string{"get", "list"}},
	}
}

// buildNodeClusterRole renders the upstream driver-registrar (node) role.
func buildNodeClusterRole(driver *seaweedv1.SeaweedCSIDriver, cr *rbacv1.ClusterRole) {
	cr.Labels = csiInstanceLabels(driver)
	cr.Rules = []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"events"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch"}},
		{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: []string{""}, Resources: []string{"persistentvolumes"}, Verbs: []string{"get", "list", "watch"}},
	}
}

func buildClusterRoleBinding(crb *rbacv1.ClusterRoleBinding, labels map[string]string, roleName, saName, saNamespace string) {
	crb.Labels = labels
	crb.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: roleName}
	crb.Subjects = []rbacv1.Subject{{Kind: "ServiceAccount", Name: saName, Namespace: saNamespace}}
}

func buildLeaderElectionRole(driver *seaweedv1.SeaweedCSIDriver, role *rbacv1.Role) {
	role.Labels = csiInstanceLabels(driver)
	role.Rules = []rbacv1.PolicyRule{
		{APIGroups: []string{"coordination.k8s.io"}, Resources: []string{"leases"}, Verbs: []string{"get", "watch", "list", "delete", "update", "create"}},
	}
}

func buildLeaderElectionRoleBinding(driver *seaweedv1.SeaweedCSIDriver, rb *rbacv1.RoleBinding, roleName, saName string) {
	rb.Labels = csiInstanceLabels(driver)
	rb.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: roleName}
	rb.Subjects = []rbacv1.Subject{{Kind: "ServiceAccount", Name: saName, Namespace: driver.Namespace}}
}

// --- CSIDriver + StorageClass ----------------------------------------------

func buildCSIDriverObject(driver *seaweedv1.SeaweedCSIDriver, obj *storagev1.CSIDriver) {
	attachRequired := attacherEnabled(driver)
	podInfoOnMount := true
	obj.Labels = csiInstanceLabels(driver)
	obj.Spec.AttachRequired = &attachRequired
	obj.Spec.PodInfoOnMount = &podInfoOnMount
	obj.Spec.VolumeLifecycleModes = []storagev1.VolumeLifecycleMode{storagev1.VolumeLifecyclePersistent}
}

func buildStorageClass(driver *seaweedv1.SeaweedCSIDriver, sc *storagev1.StorageClass) {
	scSpec := driver.Spec.StorageClass
	sc.Labels = csiInstanceLabels(driver)
	if sc.Annotations == nil {
		sc.Annotations = map[string]string{}
	}
	if scSpec.IsDefaultClass {
		sc.Annotations["storageclass.kubernetes.io/is-default-class"] = "true"
	} else {
		delete(sc.Annotations, "storageclass.kubernetes.io/is-default-class")
	}
	sc.Provisioner = driver.Spec.DriverName
	sc.Parameters = scSpec.Parameters
	sc.MountOptions = scSpec.MountOptions
	sc.ReclaimPolicy = scSpec.ReclaimPolicy
	sc.VolumeBindingMode = scSpec.VolumeBindingMode
	sc.AllowVolumeExpansion = scSpec.AllowVolumeExpansion
}

// --- shared low-level helpers -----------------------------------------------

func privilegedSecurityContext() *corev1.SecurityContext {
	priv := true
	return &corev1.SecurityContext{
		Privileged:               &priv,
		AllowPrivilegeEscalation: &priv,
		Capabilities:             &corev1.Capabilities{Add: []corev1.Capability{"SYS_ADMIN"}},
	}
}

func hostPathVolume(name, path string, typ *corev1.HostPathType) corev1.Volume {
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: path, Type: typ}}}
}

// tolerateAll prepends a blanket toleration so the node-level pods schedule on
// tainted nodes, then appends the user's tolerations.
func tolerateAll(extra []corev1.Toleration) []corev1.Toleration {
	return append([]corev1.Toleration{{Operator: corev1.TolerationOpExists}}, extra...)
}

func daemonSetUpdateStrategy(t, def appsv1.DaemonSetUpdateStrategyType) appsv1.DaemonSetUpdateStrategy {
	if t == "" {
		t = def
	}
	return appsv1.DaemonSetUpdateStrategy{Type: t}
}
