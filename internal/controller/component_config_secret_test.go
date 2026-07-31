package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// master.toml and filer.toml can carry credentials (remote storage backends,
// the filer's metadata store password), so both components accept a
// configSecret reference instead of the inline config string.

func podVolume(t *testing.T, pod *corev1.PodSpec, name string) *corev1.Volume {
	t.Helper()
	for i := range pod.Volumes {
		if pod.Volumes[i].Name == name {
			return &pod.Volumes[i]
		}
	}
	t.Fatalf("volume %q not found in pod spec", name)
	return nil
}

func containerMount(t *testing.T, c *corev1.Container, name string) *corev1.VolumeMount {
	t.Helper()
	for i := range c.VolumeMounts {
		if c.VolumeMounts[i].Name == name {
			return &c.VolumeMounts[i]
		}
	}
	t.Fatalf("volume mount %q not found in container %q", name, c.Name)
	return nil
}

func newConfigSecretCluster(master *seaweedv1.MasterSpec, filer *seaweedv1.FilerSpec) *seaweedv1.Seaweed {
	return &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec:       seaweedv1.SeaweedSpec{Master: master, Filer: filer},
	}
}

// The Secret key is free-form, so it has to be projected under the fixed
// master.toml/filer.toml name weed looks for — mounting the Secret wholesale
// would land the config at /etc/seaweedfs/<key> and be silently ignored.
func TestMasterConfigSecret_ProjectsKeyAsMasterToml(t *testing.T) {
	r := &SeaweedReconciler{}
	m := newConfigSecretCluster(&seaweedv1.MasterSpec{
		Replicas: 1,
		ConfigSecret: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "master-config-secret"},
			Key:                  "master-toml",
		},
	}, nil)

	pod := &r.createMasterStatefulSet(m).Spec.Template.Spec
	vol := podVolume(t, pod, "master-config")
	if vol.Secret == nil {
		t.Fatalf("expected master-config to be a Secret volume, got %+v", vol.VolumeSource)
	}
	if vol.Secret.SecretName != "master-config-secret" {
		t.Errorf("expected secretName master-config-secret, got %q", vol.Secret.SecretName)
	}
	want := []corev1.KeyToPath{{Key: "master-toml", Path: "master.toml"}}
	if len(vol.Secret.Items) != 1 || vol.Secret.Items[0] != want[0] {
		t.Errorf("expected items %+v, got %+v", want, vol.Secret.Items)
	}
	if mount := containerMount(t, &pod.Containers[0], "master-config"); mount.MountPath != "/etc/seaweedfs" {
		t.Errorf("expected mount at /etc/seaweedfs, got %q", mount.MountPath)
	}
}

func TestFilerConfigSecret_ProjectsKeyAsFilerToml(t *testing.T) {
	r := &SeaweedReconciler{}
	m := newConfigSecretCluster(&seaweedv1.MasterSpec{Replicas: 1}, &seaweedv1.FilerSpec{
		Replicas: 1,
		ConfigSecret: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "filer-config-secret"},
			Key:                  "filer-toml",
		},
	})

	pod := &r.createFilerStatefulSet(m).Spec.Template.Spec
	vol := podVolume(t, pod, "filer-config")
	if vol.Secret == nil {
		t.Fatalf("expected filer-config to be a Secret volume, got %+v", vol.VolumeSource)
	}
	if vol.Secret.SecretName != "filer-config-secret" {
		t.Errorf("expected secretName filer-config-secret, got %q", vol.Secret.SecretName)
	}
	want := []corev1.KeyToPath{{Key: "filer-toml", Path: "filer.toml"}}
	if len(vol.Secret.Items) != 1 || vol.Secret.Items[0] != want[0] {
		t.Errorf("expected items %+v, got %+v", want, vol.Secret.Items)
	}
	if mount := containerMount(t, filerContainer(t, pod), "filer-config"); mount.MountPath != "/etc/seaweedfs" {
		t.Errorf("expected mount at /etc/seaweedfs, got %q", mount.MountPath)
	}
}

// The CRD rejects setting both, but an object that predates the rule must
// still reconcile to one config source rather than two volumes named alike.
func TestConfigSecret_TakesPrecedenceOverInlineConfig(t *testing.T) {
	r := &SeaweedReconciler{}
	inline := "[master.maintenance]\n"
	m := newConfigSecretCluster(&seaweedv1.MasterSpec{
		Replicas: 1,
		Config:   &inline,
		ConfigSecret: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "master-config-secret"},
			Key:                  "master.toml",
		},
	}, &seaweedv1.FilerSpec{
		Replicas: 1,
		Config:   &inline,
		ConfigSecret: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "filer-config-secret"},
			Key:                  "filer.toml",
		},
	})

	if cm := r.createMasterConfigMap(m); cm != nil {
		t.Errorf("expected no master ConfigMap when configSecret is set, got %q", cm.Name)
	}
	if cm := r.createFilerConfigMap(m); cm != nil {
		t.Errorf("expected no filer ConfigMap when configSecret is set, got %q", cm.Name)
	}

	masterPod := &r.createMasterStatefulSet(m).Spec.Template.Spec
	if vol := podVolume(t, masterPod, "master-config"); vol.ConfigMap != nil {
		t.Errorf("expected master-config to come from the Secret, got ConfigMap %q", vol.ConfigMap.Name)
	}
	filerPod := &r.createFilerStatefulSet(m).Spec.Template.Spec
	if vol := podVolume(t, filerPod, "filer-config"); vol.ConfigMap != nil {
		t.Errorf("expected filer-config to come from the Secret, got ConfigMap %q", vol.ConfigMap.Name)
	}
}

// A selector missing a name or a key cannot be projected. Mounting it anyway
// would put an empty /etc/seaweedfs over the component's own defaults — the
// crashloop hasFilerConfig exists to avoid — so it counts as unset and the
// inline config still applies.
func TestConfigSecret_IncompleteSelectorFallsBackToInlineConfig(t *testing.T) {
	r := &SeaweedReconciler{}
	inline := "[leveldb2]\nenabled = true\n"

	for name, sel := range map[string]*corev1.SecretKeySelector{
		"missing key":  {LocalObjectReference: corev1.LocalObjectReference{Name: "cfg"}},
		"missing name": {Key: "filer.toml"},
	} {
		t.Run(name, func(t *testing.T) {
			m := newConfigSecretCluster(&seaweedv1.MasterSpec{Replicas: 1}, &seaweedv1.FilerSpec{
				Replicas:     1,
				Config:       &inline,
				ConfigSecret: sel,
			})
			if cm := r.createFilerConfigMap(m); cm == nil {
				t.Fatal("expected the inline config to still produce a ConfigMap")
			}
			pod := &r.createFilerStatefulSet(m).Spec.Template.Spec
			if vol := podVolume(t, pod, "filer-config"); vol.ConfigMap == nil {
				t.Errorf("expected filer-config to come from the ConfigMap, got %+v", vol.VolumeSource)
			}
		})
	}
}

// Optional must reach the volume: with it set, a Secret that has not been
// created yet leaves the mount empty instead of wedging the pod in
// ContainerCreating.
func TestConfigSecret_PropagatesOptional(t *testing.T) {
	r := &SeaweedReconciler{}
	optional := true
	m := newConfigSecretCluster(&seaweedv1.MasterSpec{
		Replicas: 1,
		ConfigSecret: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "cfg"},
			Key:                  "master.toml",
			Optional:             &optional,
		},
	}, nil)

	pod := &r.createMasterStatefulSet(m).Spec.Template.Spec
	vol := podVolume(t, pod, "master-config")
	if vol.Secret == nil || vol.Secret.Optional == nil || !*vol.Secret.Optional {
		t.Errorf("expected optional to be propagated to the volume, got %+v", vol.VolumeSource)
	}
}

// Without either field nothing is mounted at /etc/seaweedfs, so the
// components keep their built-in defaults.
func TestConfigSecret_UnsetMountsNothing(t *testing.T) {
	r := &SeaweedReconciler{}
	m := newConfigSecretCluster(&seaweedv1.MasterSpec{Replicas: 1}, &seaweedv1.FilerSpec{Replicas: 1})

	for _, pod := range []*corev1.PodSpec{
		&r.createMasterStatefulSet(m).Spec.Template.Spec,
		&r.createFilerStatefulSet(m).Spec.Template.Spec,
	} {
		for _, vol := range pod.Volumes {
			if vol.Name == "master-config" || vol.Name == "filer-config" {
				t.Errorf("expected no config volume when neither config nor configSecret is set, got %q", vol.Name)
			}
		}
	}
}
