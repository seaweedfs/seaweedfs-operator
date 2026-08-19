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

package helm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/util/yaml"
)

// rbacKinds are every kind rbac.create is expected to gate. A chart that
// renders any of these while rbac.create is false breaks the restricted
// installs the flag exists for: the cluster admin provisions RBAC
// out-of-band and the release must not fight them for ownership.
var rbacKinds = map[string]bool{
	"Role":               true,
	"RoleBinding":        true,
	"ClusterRole":        true,
	"ClusterRoleBinding": true,
}

// TestHelmRBACCreate covers both sides of the rbac.create switch. The
// disabled cases are the regression guard: RBAC lives in several
// templates (the manager role, leader election, the end-user
// editor/viewer roles, and the webhook certificate hook job), so a new
// RBAC resource added later without the conditional silently reintroduces
// the resources this flag promises to suppress.
func TestHelmRBACCreate(t *testing.T) {
	chartDir := filepath.Join(projectRoot(t), "deploy", "helm")

	t.Run("disabled renders no RBAC", func(t *testing.T) {
		// Default webhook config, i.e. the certgen hook job path, which
		// carries RBAC of its own outside templates/rbac.
		assertNoRBAC(t, renderDocs(t, chartDir, "--set", "rbac.create=false"))
	})

	t.Run("disabled renders no RBAC with cert-manager", func(t *testing.T) {
		// The cert-manager path skips the hook job; assert the other
		// templates stay gated regardless of which webhook mode is on.
		assertNoRBAC(t, renderDocs(t, chartDir,
			"--set", "rbac.create=false",
			"--set", "webhook.certManager.enabled=true"))
	})

	t.Run("enabled by default renders every RBAC resource", func(t *testing.T) {
		docs := renderDocs(t, chartDir)

		want := []struct{ kind, name string }{
			{"ClusterRole", "rbac-test-seaweedfs-operator-manager-role"},
			{"ClusterRoleBinding", "rbac-test-seaweedfs-operator-manager-rolebinding"},
			{"Role", "rbac-test-seaweedfs-operator-leader-election-role"},
			{"RoleBinding", "rbac-test-seaweedfs-operator-leader-election-rolebinding"},
			{"ClusterRole", "seaweed-editor-role"},
			{"ClusterRole", "seaweed-viewer-role"},
			{"Role", "rbac-test-seaweedfs-operator-update-webhook-certificates"},
			{"RoleBinding", "rbac-test-seaweedfs-operator-update-webhook-certificates"},
			{"ClusterRole", "rbac-test-seaweedfs-operator-update-webhook-certificates"},
			{"ClusterRoleBinding", "rbac-test-seaweedfs-operator-update-webhook-certificates"},
		}
		rendered := map[string]bool{}
		for _, doc := range docs {
			rendered[docKey(doc)] = true
		}
		for _, w := range want {
			if key := w.kind + "/" + w.name; !rendered[key] {
				t.Errorf("default values no longer render %s; rbac.create defaults to true and must stay backward compatible", key)
			}
		}
	})

	t.Run("enabled renders complete manifests", func(t *testing.T) {
		// A guard on `{{- if ... -}}`: the trailing trim marker eats the
		// newline after the conditional and glues the following line onto
		// the preceding one, which silently comments out apiVersion when
		// the template opens with a YAML comment.
		for _, doc := range renderDocs(t, chartDir) {
			if apiVersion, _ := doc["apiVersion"].(string); apiVersion == "" {
				t.Errorf("rendered document %q has no apiVersion; check the whitespace trim markers on its template conditional", docKey(doc))
			}
		}
	})
}

func assertNoRBAC(t *testing.T, docs []map[string]any) {
	t.Helper()
	for _, doc := range docs {
		if kind, _ := doc["kind"].(string); rbacKinds[kind] {
			t.Errorf("rbac.create=false still renders %s; it must be gated on .Values.rbac.create", docKey(doc))
		}
	}
}

// docKey identifies a rendered document for test failure messages.
func docKey(doc map[string]any) string {
	kind, _ := doc["kind"].(string)
	metadata, _ := doc["metadata"].(map[string]any)
	name, _ := metadata["name"].(string)
	return fmt.Sprintf("%s/%s", kind, name)
}

// renderDocs runs `helm template` and returns every non-empty rendered
// document as a generic map, so assertions can inspect fields (like a
// missing apiVersion) that typed decoding would quietly drop.
func renderDocs(t *testing.T, chartDir string, extraArgs ...string) []map[string]any {
	t.Helper()
	args := append([]string{"template", "rbac-test", chartDir}, extraArgs...)
	// Timeout so a stalled render can't hang CI.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "helm", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatalf("helm template timed out after 30s\nstderr: %s", stderr.String())
		}
		// Missing helm binary: skip on a dev machine. Render error: fail.
		if errors.Is(err, exec.ErrNotFound) {
			t.Skipf("helm not found in PATH; skipping RBAC toggle test: %v", err)
		}
		t.Fatalf("helm template %v failed: %v\nstderr: %s", extraArgs, err, stderr.String())
	}

	var docs []map[string]any
	dec := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(stdout.Bytes()), 4096)
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("helm template %v produced undecodable YAML: %v", extraArgs, err)
		}
		if len(doc) > 0 {
			docs = append(docs, doc)
		}
	}
	if len(docs) == 0 {
		t.Fatalf("helm template %v rendered no documents", extraArgs)
	}
	return docs
}
