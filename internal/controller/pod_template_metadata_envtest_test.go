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
	"context"
	"fmt"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

const (
	restartedAtAnnotation = "kubectl.kubernetes.io/restartedAt"
	restartedAt           = "2026-09-03T12:23:43Z"
)

// rolloutRestart stamps a workload's pod template the way kubectl rollout
// restart does: a strategic merge patch adding the restartedAt annotation.
func rolloutRestart(t *testing.T, ctx context.Context, cli client.Client, obj client.Object) {
	t.Helper()
	patch := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{%q:%q}}}}}`, restartedAtAnnotation, restartedAt)
	if err := cli.Patch(ctx, obj, client.RawPatch(types.StrategicMergePatchType, []byte(patch))); err != nil {
		t.Fatalf("rollout restart %s: %v", obj.GetName(), err)
	}
}

func podTemplateOf(t *testing.T, obj client.Object) corev1.PodTemplateSpec {
	t.Helper()
	switch o := obj.(type) {
	case *appsv1.StatefulSet:
		return o.Spec.Template
	case *appsv1.Deployment:
		return o.Spec.Template
	case *appsv1.DaemonSet:
		return o.Spec.Template
	}
	t.Fatalf("unexpected workload type %T", obj)
	return corev1.PodTemplateSpec{}
}

// assertTemplateAnnotations re-reads obj and checks that the restart
// annotation stamped by another controller survived the reconcile while the
// operator's own annotation was brought up to date.
func assertTemplateAnnotations(t *testing.T, ctx context.Context, cli client.Client, obj client.Object, checksum string) {
	t.Helper()
	if err := cli.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
		t.Fatalf("get %s: %v", obj.GetName(), err)
	}
	got := podTemplateOf(t, obj).Annotations
	if got[restartedAtAnnotation] != restartedAt {
		t.Errorf("%s: template annotation %s = %q, want %q", obj.GetName(), restartedAtAnnotation, got[restartedAtAnnotation], restartedAt)
	}
	if got["checksum/config"] != checksum {
		t.Errorf("%s: template annotation checksum/config = %q, want %q", obj.GetName(), got["checksum/config"], checksum)
	}
}

// TestReconcile_PreservesPodTemplateAnnotationsFromOtherControllers converges
// a cluster against a real apiserver, stamps every managed workload's pod
// template the way kubectl rollout restart (and Reloader, and the Vault
// Secrets Operator) does, and bumps an operator-owned annotation on the CR.
// The next reconcile must keep the foreign key and apply its own. Reverting
// the template mid-roll left StatefulSets half rolled, with ordinal 0 never
// restarted.
func TestReconcile_PreservesPodTemplateAnnotationsFromOtherControllers(t *testing.T) {
	_, cli := mustEnvtest(t)
	ctx := context.Background()

	ns := newTestNamespace(t, ctx, cli, "restart")
	t.Cleanup(func() {
		_ = cli.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	})

	concurrentStart := true
	diskCount := int32(1)
	cr := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "restart", Namespace: ns},
		Spec: seaweedv1.SeaweedSpec{
			Image:                 "chrislusf/seaweedfs:latest",
			Annotations:           map[string]string{"checksum/config": "v1"},
			VolumeServerDiskCount: &diskCount,
			Master: &seaweedv1.MasterSpec{
				Replicas:        1,
				ConcurrentStart: &concurrentStart,
			},
			Volume: &seaweedv1.VolumeSpec{
				Replicas: 1,
				VolumeServerConfig: seaweedv1.VolumeServerConfig{
					ResourceRequirements: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
					},
				},
			},
			Filer: &seaweedv1.FilerSpec{Replicas: 1},
			S3:    &seaweedv1.S3GatewaySpec{Replicas: 1},
		},
	}
	if err := cli.Create(ctx, cr); err != nil {
		t.Fatalf("create Seaweed CR: %v", err)
	}

	r := &SeaweedReconciler{Client: cli, Log: logf.FromContext(ctx), Scheme: cli.Scheme()}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: ns}}
	for i := 0; i < 3; i++ {
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatalf("convergence reconcile %d: %v", i+1, err)
		}
	}

	workloads := []client.Object{
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "restart-master", Namespace: ns}},
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "restart-volume", Namespace: ns}},
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "restart-filer", Namespace: ns}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "restart-s3", Namespace: ns}},
	}
	for _, w := range workloads {
		rolloutRestart(t, ctx, cli, w)
	}

	if err := cli.Get(ctx, req.NamespacedName, cr); err != nil {
		t.Fatalf("re-read CR: %v", err)
	}
	cr.Spec.Annotations["checksum/config"] = "v2"
	if err := cli.Update(ctx, cr); err != nil {
		t.Fatalf("update CR: %v", err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile after restart: %v", err)
	}

	for _, w := range workloads {
		assertTemplateAnnotations(t, ctx, cli, w, "v2")
	}
}

// TestEnsureVolumeServerDaemonSet_PreservesPodTemplateAnnotationsFromOtherControllers
// covers the hostPath DaemonSet, which is reconciled outside the StatefulSet path.
func TestEnsureVolumeServerDaemonSet_PreservesPodTemplateAnnotationsFromOtherControllers(t *testing.T) {
	_, cli := mustEnvtest(t)
	ctx := context.Background()

	ns := newTestNamespace(t, ctx, cli, "restart-ds")
	t.Cleanup(func() {
		_ = cli.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	})

	cr := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "restart", Namespace: ns},
		Spec: seaweedv1.SeaweedSpec{
			Image:       "chrislusf/seaweedfs:latest",
			Annotations: map[string]string{"checksum/config": "v1"},
			Master:      &seaweedv1.MasterSpec{Replicas: 1},
			Volume: &seaweedv1.VolumeSpec{
				Replicas: 1,
				Kind:     seaweedv1.VolumeServerDaemonSet,
				HostPath: []seaweedv1.VolumeServerHostPath{{Path: "/mnt/disk0"}},
			},
		},
	}
	if err := cli.Create(ctx, cr); err != nil {
		t.Fatalf("create Seaweed CR: %v", err)
	}

	r := &SeaweedReconciler{Client: cli, Log: logf.FromContext(ctx), Scheme: cli.Scheme()}
	if _, _, err := r.ensureVolumeServerDaemonSet(ctx, cr); err != nil {
		t.Fatalf("ensure DaemonSet: %v", err)
	}

	ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "restart-volume", Namespace: ns}}
	rolloutRestart(t, ctx, cli, ds)

	cr.Spec.Annotations["checksum/config"] = "v2"
	if _, _, err := r.ensureVolumeServerDaemonSet(ctx, cr); err != nil {
		t.Fatalf("ensure DaemonSet after restart: %v", err)
	}
	assertTemplateAnnotations(t, ctx, cli, ds, "v2")
}

// TestCreateOrUpdateDeployment_AddsAnnotationsToBareTemplate pins the
// Deployment path for a template that was created without annotations: the
// apiserver hands it back with a nil map, and the merge must allocate rather
// than panic when the CR later gains an annotation.
func TestCreateOrUpdateDeployment_AddsAnnotationsToBareTemplate(t *testing.T) {
	_, cli := mustEnvtest(t)
	ctx := context.Background()

	ns := newTestNamespace(t, ctx, cli, "bare-template")
	t.Cleanup(func() {
		_ = cli.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	})

	r := &SeaweedReconciler{Client: cli, Log: logf.FromContext(ctx), Scheme: cli.Scheme()}
	deploy := func(annotations map[string]string) *appsv1.Deployment {
		labels := map[string]string{"app": "bare"}
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "bare", Namespace: ns},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: labels},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: annotations},
					Spec: corev1.PodSpec{Containers: []corev1.Container{{
						Name:  "app",
						Image: "busybox",
					}}},
				},
			},
		}
	}

	if _, err := r.CreateOrUpdateDeployment(deploy(nil)); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := r.CreateOrUpdateDeployment(deploy(map[string]string{"checksum/config": "v1"})); err != nil {
		t.Fatalf("update: %v", err)
	}

	var got appsv1.Deployment
	if err := cli.Get(ctx, types.NamespacedName{Name: "bare", Namespace: ns}, &got); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if got.Spec.Template.Annotations["checksum/config"] != "v1" {
		t.Errorf("template annotation checksum/config = %q, want v1", got.Spec.Template.Annotations["checksum/config"])
	}
}
