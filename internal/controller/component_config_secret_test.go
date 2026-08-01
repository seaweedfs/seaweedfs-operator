package controller

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

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

// Moving a config into a Secret is pointless if the ConfigMap holding the old
// plaintext copy survives: it stays readable to anyone with `get configmap` in
// the namespace, and owner-reference GC would only collect it when the whole
// Seaweed CR is deleted.
func TestConfigSecret_PrunesConfigMapLeftByInlineConfig(t *testing.T) {
	inline := "[postgres]\npassword = \"hunter2\"\n"
	before := newConfigSecretCluster(
		&seaweedv1.MasterSpec{Replicas: 1, Config: &inline},
		&seaweedv1.FilerSpec{Replicas: 1, Config: &inline},
	)
	before.UID = "test-uid"

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("clientgoscheme: %v", err)
	}
	if err := seaweedv1.AddToScheme(scheme); err != nil {
		t.Fatalf("seaweedv1: %v", err)
	}

	// Stand up the ConfigMaps the inline config would have produced, owned by
	// the CR exactly as the reconciler creates them.
	masterCM := (&SeaweedReconciler{}).createMasterConfigMap(before)
	filerCM := (&SeaweedReconciler{}).createFilerConfigMap(before)
	for _, cm := range []*corev1.ConfigMap{masterCM, filerCM} {
		if err := controllerutil.SetControllerReference(before, cm, scheme); err != nil {
			t.Fatalf("set owner ref: %v", err)
		}
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).
		WithRuntimeObjects(before, masterCM, filerCM).Build()
	r := &SeaweedReconciler{Client: cli, Scheme: scheme, Log: logr.Discard()}

	// The user swaps both components over to a Secret and drops the inline
	// config, which is the only shape the CRD accepts.
	after := newConfigSecretCluster(
		&seaweedv1.MasterSpec{Replicas: 1, ConfigSecret: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "cfg"}, Key: "master.toml",
		}},
		&seaweedv1.FilerSpec{Replicas: 1, ConfigSecret: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "cfg"}, Key: "filer.toml",
		}},
	)
	after.UID = before.UID

	ctx := context.Background()
	if _, _, err := r.ensureMasterConfigMap(ctx, after); err != nil {
		t.Fatalf("ensureMasterConfigMap: %v", err)
	}
	if _, _, err := r.ensureFilerConfigMap(ctx, after); err != nil {
		t.Fatalf("ensureFilerConfigMap: %v", err)
	}

	// Names come from the objects actually created, so a rename in the
	// generator fails this loudly instead of leaving it checking the absence
	// of a ConfigMap nothing ever created.
	for _, name := range []string{masterCM.Name, filerCM.Name} {
		err := cli.Get(ctx, client.ObjectKey{Namespace: "ns", Name: name}, &corev1.ConfigMap{})
		if !apierrors.IsNotFound(err) {
			t.Errorf("expected ConfigMap %q to be pruned, got err %v", name, err)
		}
	}
}

// The generated name is not reserved on a cluster the operator has never
// written it on, so an object it does not control must survive — pruning is
// garbage collection of our own leftovers, not a claim on the name.
func TestConfigSecret_PruneLeavesUnownedConfigMapAlone(t *testing.T) {
	m := newConfigSecretCluster(&seaweedv1.MasterSpec{Replicas: 1}, &seaweedv1.FilerSpec{Replicas: 1})
	m.UID = "test-uid"

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("clientgoscheme: %v", err)
	}
	if err := seaweedv1.AddToScheme(scheme); err != nil {
		t.Fatalf("seaweedv1: %v", err)
	}

	// Take the name from the generator so the foreign object keeps colliding
	// with the one the reconciler manages even if that name changes.
	inline := "[leveldb2]\nenabled = true\n"
	generated := newConfigSecretCluster(nil, &seaweedv1.FilerSpec{Replicas: 1, Config: &inline})
	filerName := (&SeaweedReconciler{}).createFilerConfigMap(generated).Name

	foreign := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: filerName, Namespace: "ns"},
		Data:       map[string]string{"unrelated": "data"},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(m, foreign).Build()
	r := &SeaweedReconciler{Client: cli, Scheme: scheme, Log: logr.Discard()}

	if _, _, err := r.ensureFilerConfigMap(context.Background(), m); err != nil {
		t.Fatalf("ensureFilerConfigMap: %v", err)
	}

	got := &corev1.ConfigMap{}
	if err := cli.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: filerName}, got); err != nil {
		t.Fatalf("expected the unowned ConfigMap to survive, got %v", err)
	}
	if got.Data["unrelated"] != "data" {
		t.Errorf("expected the unowned ConfigMap to be untouched, got %v", got.Data)
	}
}
