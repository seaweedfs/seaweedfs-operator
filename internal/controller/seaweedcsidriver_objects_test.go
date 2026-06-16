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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func testDriver() *seaweedv1.SeaweedCSIDriver {
	return &seaweedv1.SeaweedCSIDriver{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "storage"},
		Spec: seaweedv1.SeaweedCSIDriverSpec{
			FilerAddress: "my-filer:8888",
			DriverName:   seaweedv1.DefaultCSIDriverName,
			Image:        seaweedv1.DefaultCSIDriverImage,
		},
	}
}

func containerByName(t *testing.T, containers []corev1.Container, name string) corev1.Container {
	t.Helper()
	for _, c := range containers {
		if c.Name == name {
			return c
		}
	}
	require.Failf(t, "container not found", "no container named %q in %v", name, containerNames(containers))
	return corev1.Container{}
}

func containerNames(containers []corev1.Container) []string {
	names := make([]string, 0, len(containers))
	for _, c := range containers {
		names = append(names, c.Name)
	}
	return names
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func envValue(t *testing.T, envs []corev1.EnvVar, name string) corev1.EnvVar {
	t.Helper()
	for _, e := range envs {
		if e.Name == name {
			return e
		}
	}
	require.Failf(t, "env not found", "no env %q", name)
	return corev1.EnvVar{}
}

func volumeByName(t *testing.T, vols []corev1.Volume, name string) corev1.Volume {
	t.Helper()
	for _, v := range vols {
		if v.Name == name {
			return v
		}
	}
	require.Failf(t, "volume not found", "no volume %q", name)
	return corev1.Volume{}
}

func mountByName(t *testing.T, mounts []corev1.VolumeMount, name string) corev1.VolumeMount {
	t.Helper()
	for _, m := range mounts {
		if m.Name == name {
			return m
		}
	}
	require.Failf(t, "mount not found", "no mount %q", name)
	return corev1.VolumeMount{}
}

func TestBuildControllerDeployment(t *testing.T) {
	d := testDriver()
	dep := &appsv1.Deployment{}
	buildControllerDeployment(d, dep, "my-filer:8888")

	pod := dep.Spec.Template.Spec
	assert.Equal(t, "seaweedfs-csi-sw-controller", pod.ServiceAccountName)
	assert.Equal(t, "system-cluster-critical", pod.PriorityClassName)

	// plugin + provisioner + resizer + attacher + liveness
	require.ElementsMatch(t,
		[]string{"seaweedfs-csi-plugin", "csi-provisioner", "csi-resizer", "csi-attacher", "csi-liveness-probe"},
		containerNames(pod.Containers))

	plugin := containerByName(t, pod.Containers, "seaweedfs-csi-plugin")
	assert.Equal(t, seaweedv1.DefaultCSIDriverImage, plugin.Image)
	assert.True(t, hasArg(plugin.Args, "--components=controller"), "controller component")
	assert.True(t, hasArg(plugin.Args, "--attacher=true"), "attacher enabled by default")
	assert.True(t, hasArg(plugin.Args, "--filer=my-filer:8888"), "filer arg")
	assert.True(t, hasArg(plugin.Args, "--driverName=seaweedfs-csi-driver"))
	assert.Nil(t, plugin.SecurityContext, "controller plugin is not privileged")
	assert.Equal(t, "unix://"+csiControllerSocketPath, envValue(t, plugin.Env, "CSI_ENDPOINT").Value)

	// sidecars use leader election scoped to the pod namespace via downward API
	prov := containerByName(t, pod.Containers, "csi-provisioner")
	assert.True(t, hasArg(prov.Args, "--leader-election"))
	assert.True(t, hasArg(prov.Args, "--leader-election-namespace=$(NAMESPACE)"))
	assert.Equal(t, csiControllerSocketPath, envValue(t, prov.Env, "ADDRESS").Value)
	assert.Equal(t, "metadata.namespace", envValue(t, prov.Env, "NAMESPACE").ValueFrom.FieldRef.FieldPath)

	// every container shares the controller socket emptyDir
	for _, c := range pod.Containers {
		m := mountByName(t, c.VolumeMounts, "socket-dir")
		assert.Equal(t, csiControllerSocketDir, m.MountPath, "container %s", c.Name)
	}
	require.NotNil(t, volumeByName(t, pod.Volumes, "socket-dir").EmptyDir)
}

func TestBuildControllerDeploymentAttacherDisabled(t *testing.T) {
	d := testDriver()
	off := false
	d.Spec.Controller.AttacherEnabled = &off

	dep := &appsv1.Deployment{}
	buildControllerDeployment(d, dep, "my-filer:8888")

	assert.NotContains(t, containerNames(dep.Spec.Template.Spec.Containers), "csi-attacher")
	plugin := containerByName(t, dep.Spec.Template.Spec.Containers, "seaweedfs-csi-plugin")
	assert.True(t, hasArg(plugin.Args, "--attacher=false"))
}

func TestBuildNodeDaemonSet(t *testing.T) {
	d := testDriver()
	ds := &appsv1.DaemonSet{}
	buildNodeDaemonSet(d, ds, "my-filer:8888")

	pod := ds.Spec.Template.Spec
	assert.Equal(t, "seaweedfs-csi-sw-node", pod.ServiceAccountName)
	assert.Equal(t, "system-node-critical", pod.PriorityClassName)
	assert.True(t, pod.HostPID, "hostPID defaults true")
	assert.Equal(t, appsv1.RollingUpdateDaemonSetStrategyType, ds.Spec.UpdateStrategy.Type)

	// blanket toleration so the node plugin schedules on tainted nodes
	require.NotEmpty(t, pod.Tolerations)
	assert.Equal(t, corev1.TolerationOpExists, pod.Tolerations[0].Operator)

	plugin := containerByName(t, pod.Containers, pluginContainerName)
	require.NotNil(t, plugin.SecurityContext)
	assert.True(t, *plugin.SecurityContext.Privileged)
	assert.Contains(t, plugin.SecurityContext.Capabilities.Add, corev1.Capability("SYS_ADMIN"))
	assert.True(t, hasArg(plugin.Args, "--components=node"))
	assert.True(t, hasArg(plugin.Args, "--cacheDir="+csiCacheDir))
	assert.True(t, hasArg(plugin.Args, "--mountEndpoint="+mountEndpoint(d)))
	assert.True(t, hasArg(plugin.Args, "--nodeid=$(NODE_ID)"))
	assert.Equal(t, csiNodeSocketPath, envValue(t, plugin.Env, "CSI_ENDPOINT").Value)
	assert.Equal(t, "spec.nodeName", envValue(t, plugin.Env, "NODE_ID").ValueFrom.FieldRef.FieldPath)

	// the kubelet bind mounts must propagate bidirectionally
	for _, name := range []string{"plugins-dir", "pods-mount-dir"} {
		m := mountByName(t, plugin.VolumeMounts, name)
		require.NotNil(t, m.MountPropagation, name)
		assert.Equal(t, corev1.MountPropagationBidirectional, *m.MountPropagation, name)
	}

	reg := containerByName(t, pod.Containers, "driver-registrar")
	assert.Equal(t, "/csi/csi.sock", envValue(t, reg.Env, "ADDRESS").Value, "registrar wants a path, not a unix:// url")
	assert.Equal(t, "/var/lib/kubelet/plugins/seaweedfs-csi-driver/csi.sock", envValue(t, reg.Env, "DRIVER_REG_SOCK_PATH").Value)

	// host-path volume types
	assert.Equal(t, corev1.HostPathDirectoryOrCreate, *volumeByName(t, pod.Volumes, "registration-dir").HostPath.Type)
	assert.Equal(t, corev1.HostPathDirectoryOrCreate, *volumeByName(t, pod.Volumes, "plugin-dir").HostPath.Type)
	assert.Equal(t, "/var/lib/kubelet/plugins/seaweedfs-csi-driver", volumeByName(t, pod.Volumes, "plugin-dir").HostPath.Path)
	assert.Equal(t, corev1.HostPathDirectory, *volumeByName(t, pod.Volumes, "plugins-dir").HostPath.Type)
}

func TestBuildNodeDaemonSetCustomKubeletPath(t *testing.T) {
	d := testDriver()
	d.Spec.Node.KubeletPath = "/var/lib/k0s/kubelet"
	off := false
	d.Spec.Node.HostPID = &off

	ds := &appsv1.DaemonSet{}
	buildNodeDaemonSet(d, ds, "my-filer:8888")
	pod := ds.Spec.Template.Spec

	assert.False(t, pod.HostPID)
	reg := containerByName(t, pod.Containers, "driver-registrar")
	assert.Equal(t, "/var/lib/k0s/kubelet/plugins/seaweedfs-csi-driver/csi.sock", envValue(t, reg.Env, "DRIVER_REG_SOCK_PATH").Value)
	assert.Equal(t, "/var/lib/k0s/kubelet/plugins_registry", volumeByName(t, pod.Volumes, "registration-dir").HostPath.Path)
	assert.Equal(t, "/var/lib/k0s/kubelet/pods", volumeByName(t, pod.Volumes, "pods-mount-dir").HostPath.Path)

	plugin := containerByName(t, pod.Containers, pluginContainerName)
	assert.Equal(t, "/var/lib/k0s/kubelet/plugins", mountByName(t, plugin.VolumeMounts, "plugins-dir").MountPath)
}

func TestNodeAndMountShareSocket(t *testing.T) {
	d := testDriver()
	node := &appsv1.DaemonSet{}
	mount := &appsv1.DaemonSet{}
	buildNodeDaemonSet(d, node, "my-filer:8888")
	buildMountDaemonSet(d, mount)

	plugin := containerByName(t, node.Spec.Template.Spec.Containers, pluginContainerName)
	mountC := containerByName(t, mount.Spec.Template.Spec.Containers, "seaweedfs-mount")

	// the node plugin must dial the exact socket the mount service listens on
	wantEndpoint := mountEndpoint(d)
	assert.True(t, hasArg(plugin.Args, "--mountEndpoint="+wantEndpoint))
	assert.Equal(t, wantEndpoint, envValue(t, mountC.Env, "MOUNT_ENDPOINT").Value)

	// and the socket dir host path must be shared
	assert.Equal(t, mountSocketDir(d), volumeByName(t, node.Spec.Template.Spec.Volumes, "mount-socket-dir").HostPath.Path)
	assert.Equal(t, mountSocketDir(d), volumeByName(t, mount.Spec.Template.Spec.Volumes, "mount-socket-dir").HostPath.Path)
}

func TestBuildMountDaemonSet(t *testing.T) {
	d := testDriver()
	ds := &appsv1.DaemonSet{}
	buildMountDaemonSet(d, ds)
	pod := ds.Spec.Template.Spec

	assert.Equal(t, appsv1.OnDeleteDaemonSetStrategyType, ds.Spec.UpdateStrategy.Type, "mount defaults to OnDelete")
	assert.Equal(t, "seaweedfs-csi-sw-node", pod.ServiceAccountName, "mount runs under the node SA")
	mountC := containerByName(t, pod.Containers, "seaweedfs-mount")
	assert.Equal(t, seaweedv1.DefaultCSIMountImage, mountC.Image)
	require.NotNil(t, mountC.SecurityContext)
	assert.True(t, *mountC.SecurityContext.Privileged)
	for _, name := range []string{"plugins-dir", "pods-mount-dir"} {
		m := mountByName(t, mountC.VolumeMounts, name)
		require.NotNil(t, m.MountPropagation, name)
		assert.Equal(t, corev1.MountPropagationBidirectional, *m.MountPropagation, name)
	}
}

func TestBuildControllerClusterRoleCoversSidecars(t *testing.T) {
	d := testDriver()
	cr := &rbacv1.ClusterRole{}
	buildControllerClusterRole(d, cr)

	// spot-check the permissions each controller sidecar needs at runtime
	requireRule(t, cr.Rules, "", "persistentvolumes", "create", "delete", "update", "patch")
	requireRule(t, cr.Rules, "", "persistentvolumeclaims", "update")
	requireRule(t, cr.Rules, "", "events", "create")
	requireRule(t, cr.Rules, "storage.k8s.io", "volumeattachments", "update", "patch")
	requireRule(t, cr.Rules, "storage.k8s.io", "csinodes", "get")
	requireRule(t, cr.Rules, "storage.k8s.io", "storageclasses", "list")
}

func TestBuildNodeClusterRole(t *testing.T) {
	d := testDriver()
	cr := &rbacv1.ClusterRole{}
	buildNodeClusterRole(d, cr)
	requireRule(t, cr.Rules, "", "nodes", "get")
	requireRule(t, cr.Rules, "", "persistentvolumes", "get")
	requireRule(t, cr.Rules, "", "events", "create")
}

func TestBuildCSIDriverObject(t *testing.T) {
	d := testDriver()
	obj := &storagev1.CSIDriver{}
	buildCSIDriverObject(d, obj)
	require.NotNil(t, obj.Spec.AttachRequired)
	assert.True(t, *obj.Spec.AttachRequired, "attachRequired follows attacherEnabled")
	require.NotNil(t, obj.Spec.PodInfoOnMount)
	assert.True(t, *obj.Spec.PodInfoOnMount)
	assert.Equal(t, []storagev1.VolumeLifecycleMode{storagev1.VolumeLifecyclePersistent}, obj.Spec.VolumeLifecycleModes)

	off := false
	d.Spec.Controller.AttacherEnabled = &off
	obj2 := &storagev1.CSIDriver{}
	buildCSIDriverObject(d, obj2)
	assert.False(t, *obj2.Spec.AttachRequired, "attachRequired off when attacher disabled")
}

func TestBuildStorageClass(t *testing.T) {
	d := testDriver()
	d.Spec.StorageClass = &seaweedv1.CSIStorageClassSpec{IsDefaultClass: true, Parameters: map[string]string{"replication": "001"}}
	sc := &storagev1.StorageClass{}
	buildStorageClass(d, sc)
	assert.Equal(t, seaweedv1.DefaultCSIDriverName, sc.Provisioner)
	assert.Equal(t, "true", sc.Annotations["storageclass.kubernetes.io/is-default-class"])
	assert.Equal(t, "001", sc.Parameters["replication"])

	// toggling default off removes the annotation on the next reconcile
	d.Spec.StorageClass.IsDefaultClass = false
	buildStorageClass(d, sc)
	_, ok := sc.Annotations["storageclass.kubernetes.io/is-default-class"]
	assert.False(t, ok)
}

func TestTuningArgs(t *testing.T) {
	d := testDriver()
	assert.Empty(t, tuningArgs(d, nil), "no tuning args when unset")

	v := int32(2)
	cap := int32(512)
	d.Spec.LogVerbosity = &v
	d.Spec.CacheCapacityMB = &cap
	args := tuningArgs(d, nil)
	assert.True(t, hasArg(args, "-v=2"))
	assert.True(t, hasArg(args, "--cacheCapacityMB=512"))
}

func TestDefaultsAndHelpers(t *testing.T) {
	d := testDriver()
	assert.True(t, attacherEnabled(d), "attacher defaults on")
	assert.True(t, mountServiceEnabled(d), "mount service defaults on")
	assert.Equal(t, seaweedv1.DefaultKubeletPath, kubeletPath(d))
	assert.Equal(t, seaweedv1.DefaultMountSocketDir, mountSocketDir(d))
	assert.Equal(t, "unix:///var/lib/seaweedfs-mount/seaweedfs-mount.sock", mountEndpoint(d))
	assert.Equal(t, seaweedv1.DefaultCSIDriverName, storageClassName(d), "storageclass name falls back to driver name")

	d.Spec.StorageClass = &seaweedv1.CSIStorageClassSpec{Name: "custom-sc"}
	assert.Equal(t, "custom-sc", storageClassName(d))

	disabled := false
	d.Spec.MountService.Enabled = &disabled
	assert.False(t, mountServiceEnabled(d))
}

// requireRule asserts the rule set grants every verb on (group, resource).
func requireRule(t *testing.T, rules []rbacv1.PolicyRule, group, resource string, verbs ...string) {
	t.Helper()
	for _, r := range rules {
		if !contains(r.APIGroups, group) || !contains(r.Resources, resource) {
			continue
		}
		missing := false
		for _, v := range verbs {
			if !contains(r.Verbs, v) {
				missing = true
				break
			}
		}
		if !missing {
			return
		}
	}
	require.Failf(t, "missing rbac rule", "no rule granting %v on %s/%s", verbs, group, resource)
}

func contains(haystack []string, needle string) bool {
	return strings.Contains(" "+strings.Join(haystack, " ")+" ", " "+needle+" ")
}
