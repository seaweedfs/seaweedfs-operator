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
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// TestManagerRoleGrantsFinalizersForEveryWritableCRD is the apiserver-free
// counterpart to TestReconcile_SucceedsWithOnlyManagerRolePermissions, so the
// invariant is still guarded on machines where the envtest binaries are absent
// and envtest-backed tests Skip.
//
// Any seaweed.seaweedfs.com kind the operator can create is a kind it may also
// use as an owner, and owning an object on a cluster running the
// OwnerReferencesPermissionEnforcement admission plugin requires `update` on
// that kind's finalizers subresource.
func TestManagerRoleGrantsFinalizersForEveryWritableCRD(t *testing.T) {
	role := loadManagerClusterRole(t)

	const group = "seaweed.seaweedfs.com"
	writable := map[string]bool{}
	finalizers := map[string]bool{}
	for _, rule := range role.Rules {
		if !slices.Contains(rule.APIGroups, group) {
			continue
		}
		for _, res := range rule.Resources {
			switch {
			case strings.HasSuffix(res, "/finalizers"):
				if slices.Contains(rule.Verbs, "update") || slices.Contains(rule.Verbs, "*") {
					finalizers[strings.TrimSuffix(res, "/finalizers")] = true
				}
			case !strings.Contains(res, "/"):
				if slices.Contains(rule.Verbs, "create") || slices.Contains(rule.Verbs, "*") {
					writable[res] = true
				}
			}
		}
	}
	if len(writable) == 0 {
		t.Fatalf("no writable %s resources found in config/rbac/role.yaml; did the group name change?", group)
	}

	var missing []string
	for res := range writable {
		if !finalizers[res] {
			missing = append(missing, res)
		}
	}
	sort.Strings(missing)
	for _, res := range missing {
		t.Errorf("config/rbac/role.yaml grants create on %s.%s but no update on %s/finalizers: "+
			"objects owned by that kind cannot be created on clusters running the "+
			"OwnerReferencesPermissionEnforcement admission plugin (OpenShift enables it by default). "+
			"Add `// +kubebuilder:rbac:groups=%s,resources=%s/finalizers,verbs=update` to the owning "+
			"controller and re-run `make manifests`.", res, group, res, group, res)
	}
}

// loadManagerClusterRole decodes config/rbac/role.yaml, the controller-gen
// output that both the kustomize overlay and the Helm chart's manager
// ClusterRole are derived from.
func loadManagerClusterRole(t *testing.T) *rbacv1.ClusterRole {
	t.Helper()

	path := filepath.Join(projectRoot(), "config", "rbac", "role.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	role := &rbacv1.ClusterRole{}
	if err := yaml.Unmarshal(raw, role); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	if len(role.Rules) == 0 {
		t.Fatalf("%s declares no rules; did controller-gen output move?", path)
	}
	return role
}
