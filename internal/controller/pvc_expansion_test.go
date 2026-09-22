package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func expansionTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("clientgoscheme: %v", err)
	}
	if err := seaweedv1.AddToScheme(scheme); err != nil {
		t.Fatalf("seaweedv1: %v", err)
	}
	return scheme
}

func expansionTestReconciler(t *testing.T, recorder record.EventRecorder, objs ...client.Object) *SeaweedReconciler {
	t.Helper()
	scheme := expansionTestScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &SeaweedReconciler{
		Client:   cli,
		Log:      logf.FromContext(context.Background()),
		Scheme:   scheme,
		Recorder: recorder,
	}
}

func expansionTestSeaweed(namespace string) *seaweedv1.Seaweed {
	return &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "weed", Namespace: namespace},
	}
}

func expansionTestClaimTemplate(name, size, storageClass string) corev1.PersistentVolumeClaim {
	var sc *string
	if storageClass != "" {
		sc = ptr.To(storageClass)
	}
	return corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: sc,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)},
			},
		},
	}
}

func expansionTestStatefulSet(namespace string, replicas int32, templates ...corev1.PersistentVolumeClaim) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "weed-volume", Namespace: namespace},
		Spec: appsv1.StatefulSetSpec{
			Replicas:             ptr.To(replicas),
			VolumeClaimTemplates: templates,
		},
	}
}

func expansionTestPVC(namespace, name, size, storageClass string) *corev1.PersistentVolumeClaim {
	pvc := expansionTestClaimTemplate(name, size, storageClass)
	pvc.Namespace = namespace
	pvc.Name = name
	return &pvc
}

func expansionTestStorageClass(name string, allowExpansion bool) *storagev1.StorageClass {
	return &storagev1.StorageClass{
		ObjectMeta:           metav1.ObjectMeta{Name: name},
		Provisioner:          "test.csi.example.com",
		AllowVolumeExpansion: ptr.To(allowExpansion),
	}
}

func pvcStorageRequest(t *testing.T, cli client.Client, namespace, name string) resource.Quantity {
	t.Helper()
	pvc := &corev1.PersistentVolumeClaim{}
	if err := cli.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, pvc); err != nil {
		t.Fatalf("get PVC %s/%s: %v", namespace, name, err)
	}
	return *pvc.Spec.Resources.Requests.Storage()
}

func nextEvent(t *testing.T, recorder *record.FakeRecorder) string {
	t.Helper()
	select {
	case event := <-recorder.Events:
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("expected an event, got none")
		return ""
	}
}

func noEvent(t *testing.T, recorder *record.FakeRecorder) {
	t.Helper()
	select {
	case event := <-recorder.Events:
		t.Fatalf("expected no event, got %q", event)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestReconcileVolumeClaimTemplates_AtSizeIsNoOp(t *testing.T) {
	recorder := record.NewFakeRecorder(10)
	r := expansionTestReconciler(t, recorder)
	cr := expansionTestSeaweed("default")

	existing := expansionTestStatefulSet("default", 2, expansionTestClaimTemplate("mount0", "100Gi", "expandable"))
	desired := expansionTestStatefulSet("default", 2, expansionTestClaimTemplate("mount0", "100Gi", "expandable"))

	if err := r.reconcileVolumeClaimTemplates(context.Background(), cr, existing, desired); err != nil {
		t.Fatal(err)
	}
	noEvent(t, recorder)
}

func TestReconcileVolumeClaimTemplates_ExpandsPVCsWhenStorageClassAllows(t *testing.T) {
	recorder := record.NewFakeRecorder(10)
	r := expansionTestReconciler(t, recorder,
		expansionTestStorageClass("expandable", true),
		expansionTestPVC("default", "mount0-weed-volume-0", "100Gi", "expandable"),
		expansionTestPVC("default", "mount0-weed-volume-1", "100Gi", "expandable"),
	)
	cr := expansionTestSeaweed("default")

	existing := expansionTestStatefulSet("default", 2, expansionTestClaimTemplate("mount0", "100Gi", "expandable"))
	desired := expansionTestStatefulSet("default", 2, expansionTestClaimTemplate("mount0", "200Gi", "expandable"))

	if err := r.reconcileVolumeClaimTemplates(context.Background(), cr, existing, desired); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"mount0-weed-volume-0", "mount0-weed-volume-1"} {
		if got := pvcStorageRequest(t, r.Client, "default", name); got.Cmp(resource.MustParse("200Gi")) != 0 {
			t.Errorf("PVC %s storage = %s, want 200Gi", name, got.String())
		}
	}
	for i := 0; i < 2; i++ {
		if event := nextEvent(t, recorder); !strings.Contains(event, "VolumeClaimExpanded") {
			t.Errorf("expected VolumeClaimExpanded event, got %q", event)
		}
	}
}

func TestReconcileVolumeClaimTemplates_WarnsWhenStorageClassBlocks(t *testing.T) {
	recorder := record.NewFakeRecorder(10)
	r := expansionTestReconciler(t, recorder,
		expansionTestStorageClass("rigid", false),
		expansionTestPVC("default", "mount0-weed-volume-0", "100Gi", "rigid"),
	)
	cr := expansionTestSeaweed("default")

	existing := expansionTestStatefulSet("default", 1, expansionTestClaimTemplate("mount0", "100Gi", "rigid"))
	desired := expansionTestStatefulSet("default", 1, expansionTestClaimTemplate("mount0", "200Gi", "rigid"))

	if err := r.reconcileVolumeClaimTemplates(context.Background(), cr, existing, desired); err != nil {
		t.Fatal(err)
	}

	if got := pvcStorageRequest(t, r.Client, "default", "mount0-weed-volume-0"); got.Cmp(resource.MustParse("100Gi")) != 0 {
		t.Errorf("PVC storage = %s, want unchanged 100Gi", got.String())
	}
	if event := nextEvent(t, recorder); !strings.Contains(event, "VolumeExpansionNotSupported") || !strings.Contains(event, "Warning") {
		t.Errorf("expected Warning VolumeExpansionNotSupported event, got %q", event)
	}
}

func TestReconcileVolumeClaimTemplates_SkipsMissingPVC(t *testing.T) {
	recorder := record.NewFakeRecorder(10)
	r := expansionTestReconciler(t, recorder,
		expansionTestStorageClass("expandable", true),
	)
	cr := expansionTestSeaweed("default")

	existing := expansionTestStatefulSet("default", 3, expansionTestClaimTemplate("mount0", "100Gi", "expandable"))
	desired := expansionTestStatefulSet("default", 3, expansionTestClaimTemplate("mount0", "200Gi", "expandable"))

	if err := r.reconcileVolumeClaimTemplates(context.Background(), cr, existing, desired); err != nil {
		t.Fatal(err)
	}
	noEvent(t, recorder)
}

func TestReconcileVolumeClaimTemplates_SkipsResizeInProgress(t *testing.T) {
	pvc := expansionTestPVC("default", "mount0-weed-volume-0", "100Gi", "expandable")
	pvc.Status.Conditions = []corev1.PersistentVolumeClaimCondition{{
		Type:   corev1.PersistentVolumeClaimResizing,
		Status: corev1.ConditionTrue,
	}}
	recorder := record.NewFakeRecorder(10)
	r := expansionTestReconciler(t, recorder,
		expansionTestStorageClass("expandable", true),
		pvc,
	)
	cr := expansionTestSeaweed("default")

	existing := expansionTestStatefulSet("default", 1, expansionTestClaimTemplate("mount0", "100Gi", "expandable"))
	desired := expansionTestStatefulSet("default", 1, expansionTestClaimTemplate("mount0", "200Gi", "expandable"))

	if err := r.reconcileVolumeClaimTemplates(context.Background(), cr, existing, desired); err != nil {
		t.Fatal(err)
	}
	if got := pvcStorageRequest(t, r.Client, "default", "mount0-weed-volume-0"); got.Cmp(resource.MustParse("100Gi")) != 0 {
		t.Errorf("PVC storage = %s, want unchanged 100Gi while resize is in progress", got.String())
	}
	noEvent(t, recorder)
}

func TestReconcileVolumeClaimTemplates_SkipsAlreadyRequestedPVC(t *testing.T) {
	recorder := record.NewFakeRecorder(10)
	r := expansionTestReconciler(t, recorder,
		expansionTestPVC("default", "mount0-weed-volume-0", "200Gi", "expandable"),
	)
	cr := expansionTestSeaweed("default")

	existing := expansionTestStatefulSet("default", 1, expansionTestClaimTemplate("mount0", "100Gi", "expandable"))
	desired := expansionTestStatefulSet("default", 1, expansionTestClaimTemplate("mount0", "200Gi", "expandable"))

	if err := r.reconcileVolumeClaimTemplates(context.Background(), cr, existing, desired); err != nil {
		t.Fatal(err)
	}
	noEvent(t, recorder)
}

func TestReconcileVolumeClaimTemplates_DownsizeSilentlyIgnored(t *testing.T) {
	recorder := record.NewFakeRecorder(10)
	r := expansionTestReconciler(t, recorder,
		expansionTestStorageClass("expandable", true),
		expansionTestPVC("default", "mount0-weed-volume-0", "100Gi", "expandable"),
	)
	cr := expansionTestSeaweed("default")

	existing := expansionTestStatefulSet("default", 1, expansionTestClaimTemplate("mount0", "100Gi", "expandable"))
	desired := expansionTestStatefulSet("default", 1, expansionTestClaimTemplate("mount0", "50Gi", "expandable"))

	if err := r.reconcileVolumeClaimTemplates(context.Background(), cr, existing, desired); err != nil {
		t.Fatal(err)
	}
	if got := pvcStorageRequest(t, r.Client, "default", "mount0-weed-volume-0"); got.Cmp(resource.MustParse("100Gi")) != 0 {
		t.Errorf("PVC storage = %s, want unchanged 100Gi", got.String())
	}
	noEvent(t, recorder)
}

func TestReconcileVolumeClaimTemplates_WarnsOnNonStorageDrift(t *testing.T) {
	recorder := record.NewFakeRecorder(10)
	r := expansionTestReconciler(t, recorder,
		expansionTestStorageClass("expandable", true),
		expansionTestPVC("default", "mount0-weed-volume-0", "100Gi", "expandable"),
	)
	cr := expansionTestSeaweed("default")

	existing := expansionTestStatefulSet("default", 1, expansionTestClaimTemplate("mount0", "100Gi", "expandable"))
	desiredTemplate := expansionTestClaimTemplate("mount0", "200Gi", "other-class")
	desired := expansionTestStatefulSet("default", 1, desiredTemplate)

	if err := r.reconcileVolumeClaimTemplates(context.Background(), cr, existing, desired); err != nil {
		t.Fatal(err)
	}
	if event := nextEvent(t, recorder); !strings.Contains(event, "VolumeClaimTemplatesMismatch") {
		t.Errorf("expected VolumeClaimTemplatesMismatch event for non-storage drift, got %q", event)
	}
}

func TestReconcileVolumeClaimTemplates_UsesDefaultStorageClass(t *testing.T) {
	sc := expansionTestStorageClass("cluster-default", true)
	sc.Annotations = map[string]string{"storageclass.kubernetes.io/is-default-class": "true"}
	pvc := expansionTestPVC("default", "mount0-weed-volume-0", "100Gi", "")
	pvc.Spec.StorageClassName = nil
	recorder := record.NewFakeRecorder(10)
	r := expansionTestReconciler(t, recorder, sc, pvc)
	cr := expansionTestSeaweed("default")

	template := expansionTestClaimTemplate("mount0", "100Gi", "")
	desiredTemplate := expansionTestClaimTemplate("mount0", "200Gi", "")
	existing := expansionTestStatefulSet("default", 1, template)
	desired := expansionTestStatefulSet("default", 1, desiredTemplate)

	if err := r.reconcileVolumeClaimTemplates(context.Background(), cr, existing, desired); err != nil {
		t.Fatal(err)
	}
	if got := pvcStorageRequest(t, r.Client, "default", "mount0-weed-volume-0"); got.Cmp(resource.MustParse("200Gi")) != 0 {
		t.Errorf("PVC storage = %s, want 200Gi via the default StorageClass", got.String())
	}
}
