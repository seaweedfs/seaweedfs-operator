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
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	rbacv1 "k8s.io/api/rbac/v1"
)

// TestHelmOperatorDeploymentSecurity verifies the manager's default hardening.
func TestHelmOperatorDeploymentSecurity(t *testing.T) {
	docs := renderDocs(t, filepath.Join(projectRoot(t), "deploy", "helm"))

	var deployment appsv1.Deployment
	decodeRenderedDocument(t, docs, "Deployment/rbac-test-seaweedfs-operator", &deployment)

	pod := deployment.Spec.Template.Spec
	if pod.SecurityContext == nil ||
		pod.SecurityContext.SeccompProfile == nil ||
		pod.SecurityContext.SeccompProfile.Type != "RuntimeDefault" {
		t.Fatalf("operator pod seccompProfile = %#v, want RuntimeDefault", pod.SecurityContext)
	}

	if len(pod.Containers) != 1 {
		t.Fatalf("operator containers = %d, want 1", len(pod.Containers))
	}
	manager := pod.Containers[0]
	if !containsString(manager.Args, "--health-probe-bind-address=:8081") {
		t.Errorf("operator args %v do not configure the health probe server", manager.Args)
	}

	var webhookPort int32
	var healthPort int32
	for _, port := range manager.Ports {
		switch port.Name {
		case "https":
			webhookPort = port.ContainerPort
		case "health":
			healthPort = port.ContainerPort
		}
	}
	if webhookPort != 9443 {
		t.Errorf("webhook container port = %d, want 9443", webhookPort)
	}
	if healthPort != 8081 {
		t.Errorf("health container port = %d, want 8081", healthPort)
	}
	if manager.LivenessProbe == nil ||
		manager.LivenessProbe.HTTPGet == nil ||
		manager.LivenessProbe.HTTPGet.Path != "/healthz" ||
		manager.LivenessProbe.HTTPGet.Port.StrVal != "health" {
		t.Errorf("operator liveness probe = %#v, want HTTP /healthz on health port", manager.LivenessProbe)
	}
	if manager.ReadinessProbe == nil ||
		manager.ReadinessProbe.HTTPGet == nil ||
		manager.ReadinessProbe.HTTPGet.Path != "/readyz" ||
		manager.ReadinessProbe.HTTPGet.Port.StrVal != "health" {
		t.Errorf("operator readiness probe = %#v, want HTTP /readyz on health port", manager.ReadinessProbe)
	}
}

// TestHelmHealthProbePortIsConfigurable verifies custom ports and rejects
// port-number and port-name collisions.
func TestHelmHealthProbePortIsConfigurable(t *testing.T) {
	chartDir := filepath.Join(projectRoot(t), "deploy", "helm")
	docs := renderDocs(t, chartDir, "--set", "healthProbe.port=18081")

	var deployment appsv1.Deployment
	decodeRenderedDocument(t, docs, "Deployment/rbac-test-seaweedfs-operator", &deployment)
	manager := deployment.Spec.Template.Spec.Containers[0]
	if !containsString(manager.Args, "--health-probe-bind-address=:18081") {
		t.Errorf("operator args %v do not use the configured health probe port", manager.Args)
	}
	var healthPort int32
	for _, port := range manager.Ports {
		if port.Name == "health" {
			healthPort = port.ContainerPort
		}
	}
	if healthPort != 18081 {
		t.Errorf("health container port = %d, want 18081", healthPort)
	}

	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skipf("helm not found in PATH; skipping collision validation: %v", err)
	}
	collisionCases := []struct {
		name        string
		args        []string
		wantMessage string
	}{
		{
			name:        "metrics and health ports",
			args:        []string{"--set", "port.number=8081"},
			wantMessage: "healthProbe.port must differ from port.number",
		},
		{
			name:        "health and webhook ports",
			args:        []string{"--set", "healthProbe.port=9443"},
			wantMessage: "both ports must differ from webhook port 9443",
		},
		{
			name:        "metrics and webhook ports",
			args:        []string{"--set", "port.number=9443"},
			wantMessage: "both ports must differ from webhook port 9443",
		},
		{
			name:        "metrics and health port names",
			args:        []string{"--set", "port.name=health"},
			wantMessage: `port.name must not be "health"`,
		},
	}
	for _, tc := range collisionCases {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			args := append([]string{"template", "rbac-test", chartDir}, tc.args...)
			cmd := exec.CommandContext(ctx, helm, args...)
			output, err := cmd.CombinedOutput()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatalf("helm template timed out after 30s while checking %s", tc.name)
			}
			if err == nil {
				t.Fatalf("helm template accepted invalid configuration with %s", tc.name)
			}
			if !strings.Contains(string(output), tc.wantMessage) {
				t.Fatalf("helm template failed without expected message %q:\n%s", tc.wantMessage, output)
			}
		})
	}

	renderDocs(t, chartDir,
		"--set", "healthProbe.port=9443",
		"--set", "webhook.enabled=false")
	renderDocs(t, chartDir,
		"--set", "port.number=9443",
		"--set", "webhook.enabled=false")
}

// TestWebhookCertificateRBACIsResourceScoped verifies that the certificate
// updater can modify only the release-owned admission configurations.
func TestWebhookCertificateRBACIsResourceScoped(t *testing.T) {
	docs := renderDocs(t, filepath.Join(projectRoot(t), "deploy", "helm"))

	var role rbacv1.ClusterRole
	decodeRenderedDocument(
		t,
		docs,
		"ClusterRole/rbac-test-seaweedfs-operator-update-webhook-certificates",
		&role,
	)

	want := []string{
		"rbac-test-seaweedfs-operator-validating-webhook-configuration",
		"rbac-test-seaweedfs-operator-mutating-webhook-configuration",
	}
	if len(role.Rules) != 1 {
		t.Fatalf("webhook certificate ClusterRole has %d rules, want exactly 1", len(role.Rules))
	}
	rule := role.Rules[0]
	wantResources := []string{
		"validatingwebhookconfigurations",
		"mutatingwebhookconfigurations",
	}
	if !reflect.DeepEqual(rule.Resources, wantResources) {
		t.Fatalf("webhook certificate ClusterRole resources = %v, want %v", rule.Resources, wantResources)
	}
	if !reflect.DeepEqual(rule.ResourceNames, want) {
		t.Fatalf("webhook patch resourceNames = %v, want %v", rule.ResourceNames, want)
	}
}

// decodeRenderedDocument decodes one rendered Helm resource into a typed value.
func decodeRenderedDocument(t *testing.T, docs []map[string]any, key string, target any) {
	t.Helper()
	for _, doc := range docs {
		if docKey(doc) != key {
			continue
		}
		data, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("marshal %s: %v", key, err)
		}
		if err := json.Unmarshal(data, target); err != nil {
			t.Fatalf("decode %s: %v", key, err)
		}
		return
	}
	t.Fatalf("rendered chart has no %s", key)
}

// containsString reports whether values contains want.
func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
