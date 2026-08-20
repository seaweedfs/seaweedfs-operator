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

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// TestReconcile_SucceedsWithOnlyManagerRolePermissions drives a full Seaweed
// reconcile with a client that holds exactly the permissions
// config/rbac/role.yaml grants — no more — against an apiserver running the
// OwnerReferencesPermissionEnforcement admission plugin.
//
// Every owned object the reconciler writes carries a controller reference with
// blockOwnerDeletion: true (controllerutil.SetControllerReference always sets
// it), and that plugin rejects such a write unless the writer can update the
// owner's finalizers subresource. So a missing `<kind>/finalizers` grant turns
// every reconcile into an immediate hard failure:
//
//	secrets "x-security-config" is forbidden: cannot set blockOwnerDeletion if
//	an ownerReference refers to a resource you can't set finalizers on: , <nil>
//
// Clusters that leave the plugin disabled (kind, k3s, upstream defaults) never
// see it, so the gap only surfaces on OpenShift and other strict-RBAC installs.
// Enforcing the real ClusterRole here is what makes that environment-specific
// failure reproducible in CI.
func TestReconcile_SucceedsWithOnlyManagerRolePermissions(t *testing.T) {
	cfg, admin := mustEnvtest(t)
	ctx := context.Background()

	ns := newTestNamespace(t, ctx, admin, "managerrbac")
	t.Cleanup(func() {
		_ = admin.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	})

	limited := managerRoleClient(t, ctx, cfg, admin, ns)

	// ConcurrentStart skips the master pod-readiness wait — envtest runs no
	// kubelets, so no pod would ever report Running.
	concurrentStart := true
	diskCount := int32(1)
	cr := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "rbac", Namespace: ns},
		Spec: seaweedv1.SeaweedSpec{
			Image:                 "chrislusf/seaweedfs:latest",
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
			// The security Secret only exists when something asks for it.
			SecurityConfig: &seaweedv1.SecurityConfigSpec{
				JWTSigning: &seaweedv1.JWTSigningSpec{FilerWrite: true},
			},
		},
	}
	if err := admin.Create(ctx, cr); err != nil {
		t.Fatalf("create Seaweed CR: %v", err)
	}

	r := &SeaweedReconciler{
		Client: limited,
		Log:    logf.FromContext(ctx),
		Scheme: limited.Scheme(),
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: ns}}

	// Convergence takes several passes, each returning early after the
	// component it just created, so run enough of them to reach the tail of the
	// loop and exercise every owned-object write.
	for i := 0; i < 5; i++ {
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatalf("reconcile pass %d failed with only the permissions config/rbac/role.yaml grants: %v\n"+
				"If this is a blockOwnerDeletion error, the manager ClusterRole is missing an "+
				"`update` grant on the owner kind's /finalizers subresource — add the "+
				"kubebuilder:rbac marker to the owning controller and re-run `make manifests`.", i+1, err)
		}
	}

	// The security Secret is the first owned object every reconcile writes, and
	// the one the bug report tripped over, so assert it actually landed instead
	// of trusting a nil error alone.
	var secret corev1.Secret
	if err := admin.Get(ctx, types.NamespacedName{Name: cr.Name + "-security-config", Namespace: ns}, &secret); err != nil {
		t.Fatalf("owned security Secret was not created: %v", err)
	}
	if len(secret.OwnerReferences) != 1 || secret.OwnerReferences[0].BlockOwnerDeletion == nil || !*secret.OwnerReferences[0].BlockOwnerDeletion {
		t.Fatalf("security Secret should carry a blockOwnerDeletion controller reference, got %+v", secret.OwnerReferences)
	}
}

// managerRoleClient returns a client authenticated as the operator's
// ServiceAccount, authorized by a ClusterRole built from the live
// config/rbac/role.yaml. Reading the rules off disk rather than restating them
// keeps the test honest: it always asserts against the permissions the
// kustomize overlay and the Helm chart actually ship.
func managerRoleClient(t *testing.T, ctx context.Context, cfg *rest.Config, admin client.Client, ns string) client.Client {
	t.Helper()

	role := loadManagerClusterRole(t)
	role.Name = "manager-role-" + ns

	const saName = "seaweedfs-operator-controller-manager"
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: ns}}
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "manager-rolebinding-" + ns},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     role.Name,
		},
		Subjects: []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: saName, Namespace: ns}},
	}
	for _, obj := range []client.Object{role, sa, binding} {
		if err := admin.Create(ctx, obj); err != nil {
			t.Fatalf("create %T %s: %v", obj, obj.GetName(), err)
		}
	}
	// The namespace cleanup takes the ServiceAccount with it; the ClusterRole
	// and its binding are cluster-scoped and have to go explicitly.
	t.Cleanup(func() {
		bg := context.Background()
		_ = admin.Delete(bg, binding)
		_ = admin.Delete(bg, role)
	})

	// Impersonation, rather than a minted ServiceAccount token, so the test
	// does not depend on the TokenRequest API or on the ServiceAccount
	// admission plugin (envtest disables the latter).
	limitedCfg := rest.CopyConfig(cfg)
	limitedCfg.Impersonate = rest.ImpersonationConfig{
		UserName: fmt.Sprintf("system:serviceaccount:%s:%s", ns, saName),
	}
	cli, err := client.New(limitedCfg, client.Options{Scheme: admin.Scheme()})
	if err != nil {
		t.Fatalf("build impersonating client: %v", err)
	}
	return cli
}
