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
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/util/yaml"
)

// TestHelmGlobalImageRegistry guards the fix for issue #268: the chart
// declares a `global.imageRegistry` value (the Bitnami-style chart-wide
// registry override used for internal mirrors / air-gapped clusters), but
// the templates rendered images straight from each image's own
// `.registry` and ignored the global value entirely, so setting it had no
// effect. These cases render the chart with `helm template` — exactly what
// `helm install` does — and assert the global override actually reaches the
// rendered image references while staying backward compatible when unset.
func TestHelmGlobalImageRegistry(t *testing.T) {
	root := projectRoot(t)
	chartDir := filepath.Join(root, "deploy", "helm")

	t.Run("override applies to operator and certgen images", func(t *testing.T) {
		images := renderImages(t, chartDir, "--set", "global.imageRegistry=registry.example.com")

		for _, operator := range requireImages(t, images, "seaweedfs-operator") {
			if !strings.HasPrefix(operator, "registry.example.com/seaweedfs-operator:") {
				t.Errorf("operator image %q does not use global.imageRegistry override; want registry.example.com/seaweedfs-operator:<tag>", operator)
			}
		}

		// The chart renders three certgen Jobs (create + patch
		// mutating + patch validating); assert every one honors the
		// override so a single drifting Job can't hide behind another.
		certgens := requireImages(t, images, "certgen")
		if len(certgens) != 3 {
			t.Fatalf("expected 3 rendered certgen containers, got %d (%v)", len(certgens), certgens)
		}
		for _, certgen := range certgens {
			if !strings.HasPrefix(certgen, "registry.example.com/ingress-nginx/kube-webhook-certgen:") {
				t.Errorf("webhook certgen image %q does not use global.imageRegistry override; want registry.example.com/ingress-nginx/kube-webhook-certgen:<tag>", certgen)
			}
		}
	})

	t.Run("default values are backward compatible", func(t *testing.T) {
		images := renderImages(t, chartDir)

		for _, operator := range requireImages(t, images, "seaweedfs-operator") {
			if !strings.HasPrefix(operator, "chrislusf/seaweedfs-operator:") {
				t.Errorf("default operator image %q changed; want chrislusf/seaweedfs-operator:<tag> when global.imageRegistry is unset", operator)
			}
		}

		for _, certgen := range requireImages(t, images, "certgen") {
			if !strings.HasPrefix(certgen, "registry.k8s.io/ingress-nginx/kube-webhook-certgen:") {
				t.Errorf("default certgen image %q changed; want registry.k8s.io/ingress-nginx/kube-webhook-certgen:<tag> when global.imageRegistry is unset", certgen)
			}
		}
	})

	t.Run("per-image registry still works without a global override", func(t *testing.T) {
		images := renderImages(t, chartDir, "--set", "image.registry=myreg.io")

		for _, operator := range requireImages(t, images, "seaweedfs-operator") {
			if !strings.HasPrefix(operator, "myreg.io/seaweedfs-operator:") {
				t.Errorf("per-image registry override not honored: operator image %q; want myreg.io/seaweedfs-operator:<tag>", operator)
			}
		}
	})

	t.Run("nil global renders without error and falls back to per-image registry", func(t *testing.T) {
		// Guards the image helper against a nil/omitted `global` (the
		// subchart case, where Helm may not inject a `global` map, or
		// an explicit `--set global=null`). The helper must fall back
		// to the per-image registry rather than panic on a nil-pointer
		// field access.
		images := renderImages(t, chartDir, "--set", "global=null")

		for _, operator := range requireImages(t, images, "seaweedfs-operator") {
			if !strings.HasPrefix(operator, "chrislusf/seaweedfs-operator:") {
				t.Errorf("operator image %q with nil global; want chrislusf/seaweedfs-operator:<tag>", operator)
			}
		}
	})
}

// renderImages runs `helm template` with the given extra args and returns
// a map of container name -> all images rendered for that name across every
// Deployment and Job. Images are collected into a slice per name (rather
// than a single value) so that multiple resources sharing a container name
// — e.g. the three webhook certgen Jobs — are each recorded instead of
// overwriting one another.
func renderImages(t *testing.T, chartDir string, extraArgs ...string) map[string][]string {
	t.Helper()
	args := append([]string{"template", "image-test", chartDir}, extraArgs...)
	// Bound the subprocess so a stalled `helm template` can't hang CI
	// indefinitely.
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
		// A missing helm binary is a legitimate skip on a dev machine;
		// a render failure is a genuine regression that must fail.
		if errors.Is(err, exec.ErrNotFound) {
			t.Skipf("helm not found in PATH; skipping image registry test: %v", err)
		}
		t.Fatalf("helm template failed: %v\nstderr: %s", err, stderr.String())
	}

	type podResource struct {
		Kind string `json:"kind"`
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Name  string `json:"name"`
						Image string `json:"image"`
					} `json:"containers"`
					InitContainers []struct {
						Name  string `json:"name"`
						Image string `json:"image"`
					} `json:"initContainers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}

	images := map[string][]string{}
	dec := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(stdout.Bytes()), 4096)
	for {
		var res podResource
		if err := dec.Decode(&res); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			// Skip docs that don't shape into a pod-bearing resource.
			continue
		}
		for _, c := range res.Spec.Template.Spec.Containers {
			if c.Name != "" {
				images[c.Name] = append(images[c.Name], c.Image)
			}
		}
		for _, c := range res.Spec.Template.Spec.InitContainers {
			if c.Name != "" {
				images[c.Name] = append(images[c.Name], c.Image)
			}
		}
	}
	return images
}

func requireImages(t *testing.T, images map[string][]string, containerName string) []string {
	t.Helper()
	imgs, ok := images[containerName]
	if !ok {
		t.Fatalf("no container named %q found in rendered chart output; got containers %v", containerName, images)
	}
	return imgs
}
