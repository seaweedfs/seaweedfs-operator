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
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/internal/controller/label"
)

func newCSIReconciler(t *testing.T, objs ...client.Object) (*SeaweedCSIDriverReconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, seaweedv1.AddToScheme(scheme))
	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&seaweedv1.SeaweedCSIDriver{}).
		Build()
	r := &SeaweedCSIDriverReconciler{
		Client:   cli,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(100),
		Log:      logr.Discard(),
	}
	return r, cli
}

func reconcileN(t *testing.T, r *SeaweedCSIDriverReconciler, ns, name string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
		require.NoErrorf(t, err, "reconcile pass %d", i)
	}
}

func mustExist(t *testing.T, cli client.Client, obj client.Object, ns, name string) {
	t.Helper()
	require.NoErrorf(t, cli.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, obj), "expected %T %s/%s to exist", obj, ns, name)
}

func mustNotExist(t *testing.T, cli client.Client, obj client.Object, ns, name string) {
	t.Helper()
	err := cli.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, obj)
	assert.Truef(t, apierrors.IsNotFound(err), "expected %T %s/%s to be absent, got err=%v", obj, ns, name, err)
}

func reconcileTestDriver(name, ns string) *seaweedv1.SeaweedCSIDriver {
	return &seaweedv1.SeaweedCSIDriver{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: seaweedv1.SeaweedCSIDriverSpec{
			FilerAddress: "filer:8888",
			DriverName:   seaweedv1.DefaultCSIDriverName,
			Image:        seaweedv1.DefaultCSIDriverImage,
		},
	}
}

func TestReconcileCreatesAllObjects(t *testing.T) {
	ctx := context.Background()
	d := reconcileTestDriver("sw", "storage")
	d.Spec.StorageClass = &seaweedv1.CSIStorageClassSpec{}
	r, cli := newCSIReconciler(t, d)

	// First pass installs the finalizer and requeues; second pass reconciles.
	reconcileN(t, r, "storage", "sw", 2)

	var got seaweedv1.SeaweedCSIDriver
	require.NoError(t, cli.Get(ctx, types.NamespacedName{Namespace: "storage", Name: "sw"}, &got))
	assert.True(t, controllerutil.ContainsFinalizer(&got, seaweedCSIDriverFinalizer))
	assert.Equal(t, "filer:8888", got.Status.ResolvedFilerAddress)

	// Workloads.
	mustExist(t, cli, &appsv1.Deployment{}, "storage", "seaweedfs-csi-sw-controller")
	mustExist(t, cli, &appsv1.DaemonSet{}, "storage", "seaweedfs-csi-sw-node")
	mustExist(t, cli, &appsv1.DaemonSet{}, "storage", "seaweedfs-csi-sw-mount")
	// ServiceAccounts.
	mustExist(t, cli, &corev1.ServiceAccount{}, "storage", "seaweedfs-csi-sw-controller")
	mustExist(t, cli, &corev1.ServiceAccount{}, "storage", "seaweedfs-csi-sw-node")
	// Cluster-scoped RBAC.
	mustExist(t, cli, &rbacv1.ClusterRole{}, "", "seaweedfs-csi-sw-controller")
	mustExist(t, cli, &rbacv1.ClusterRole{}, "", "seaweedfs-csi-sw-node")
	mustExist(t, cli, &rbacv1.ClusterRoleBinding{}, "", "seaweedfs-csi-sw-controller")
	mustExist(t, cli, &rbacv1.ClusterRoleBinding{}, "", "seaweedfs-csi-sw-node")
	// Namespaced (owned) leader-election RBAC.
	mustExist(t, cli, &rbacv1.Role{}, "storage", "seaweedfs-csi-sw-controller")
	mustExist(t, cli, &rbacv1.RoleBinding{}, "storage", "seaweedfs-csi-sw-controller")

	var csidrv storagev1.CSIDriver
	require.NoError(t, cli.Get(ctx, types.NamespacedName{Name: seaweedv1.DefaultCSIDriverName}, &csidrv))
	require.NotNil(t, csidrv.Spec.PodInfoOnMount)
	assert.True(t, *csidrv.Spec.PodInfoOnMount)

	var sc storagev1.StorageClass
	require.NoError(t, cli.Get(ctx, types.NamespacedName{Name: seaweedv1.DefaultCSIDriverName}, &sc))
	assert.Equal(t, "sw", sc.Labels[label.InstanceLabelKey])
}

func TestReconcileMountServiceDisabled(t *testing.T) {
	d := reconcileTestDriver("sw", "storage")
	off := false
	d.Spec.MountService.Enabled = &off
	r, cli := newCSIReconciler(t, d)

	reconcileN(t, r, "storage", "sw", 2)

	mustExist(t, cli, &appsv1.DaemonSet{}, "storage", "seaweedfs-csi-sw-node")
	mustNotExist(t, cli, &appsv1.DaemonSet{}, "storage", "seaweedfs-csi-sw-mount")
}

func TestReconcileDriverNameConflictReconcilesNothing(t *testing.T) {
	ctx := context.Background()
	win := reconcileTestDriver("win", "storage")
	win.CreationTimestamp = metav1.NewTime(time.Unix(1000, 0))
	win.UID = "uid-win"
	win.Spec.DriverName = "shared"

	lose := reconcileTestDriver("lose", "storage")
	lose.CreationTimestamp = metav1.NewTime(time.Unix(2000, 0))
	lose.UID = "uid-lose"
	lose.Spec.DriverName = "shared"

	r, cli := newCSIReconciler(t, win, lose)
	reconcileN(t, r, "storage", "lose", 2)

	var got seaweedv1.SeaweedCSIDriver
	require.NoError(t, cli.Get(ctx, types.NamespacedName{Namespace: "storage", Name: "lose"}, &got))
	assert.Equal(t, seaweedv1.CSIDriverPhaseFailed, got.Status.Phase)
	cond := apimeta.FindStatusCondition(got.Status.Conditions, seaweedv1.CSIConditionDriverNameConflict)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)

	// The losing object must not render any workloads.
	mustNotExist(t, cli, &appsv1.Deployment{}, "storage", "seaweedfs-csi-lose-controller")
	mustNotExist(t, cli, &appsv1.DaemonSet{}, "storage", "seaweedfs-csi-lose-node")
}

// TestConflictLoserDeletionKeepsOwnersStorageClass is the regression test for
// the ownership-guarded StorageClass cleanup: a conflict loser sharing a
// driverName must not delete the StorageClass the winner created.
func TestConflictLoserDeletionKeepsOwnersStorageClass(t *testing.T) {
	ctx := context.Background()

	win := reconcileTestDriver("win", "storage")
	win.CreationTimestamp = metav1.NewTime(time.Unix(1000, 0))
	win.UID = "uid-win"
	win.Spec.DriverName = "shared"

	lose := reconcileTestDriver("lose", "storage")
	lose.CreationTimestamp = metav1.NewTime(time.Unix(2000, 0))
	lose.UID = "uid-lose"
	lose.Spec.DriverName = "shared"
	lose.Spec.StorageClass = &seaweedv1.CSIStorageClassSpec{}
	lose.Finalizers = []string{seaweedCSIDriverFinalizer}

	// The StorageClass named after the shared driverName is owned by win.
	sc := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{
		Name:   "shared",
		Labels: map[string]string{label.InstanceLabelKey: "win"},
	}}

	r, cli := newCSIReconciler(t, win, lose, sc)

	// Deleting lose (it has the finalizer) marks it for deletion.
	require.NoError(t, cli.Delete(ctx, lose))
	reconcileN(t, r, "storage", "lose", 1)

	// The winner's StorageClass must survive the loser's cleanup.
	var got storagev1.StorageClass
	require.NoError(t, cli.Get(ctx, types.NamespacedName{Name: "shared"}, &got),
		"loser must not delete the StorageClass owned by the winner")
	assert.Equal(t, "win", got.Labels[label.InstanceLabelKey])

	// The loser itself finished finalizing and is gone.
	mustNotExist(t, cli, &seaweedv1.SeaweedCSIDriver{}, "storage", "lose")
}
