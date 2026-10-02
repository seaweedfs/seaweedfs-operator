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
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	rbacv1 "k8s.io/api/rbac/v1"
)

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
		"rbac-test-seaweedfs-operator-mutating-webhook-configuration",
		"rbac-test-seaweedfs-operator-validating-webhook-configuration",
	}
	for _, rule := range role.Rules {
		if reflect.DeepEqual(rule.Resources, []string{
			"validatingwebhookconfigurations",
			"mutatingwebhookconfigurations",
		}) {
			if !reflect.DeepEqual(rule.ResourceNames, want) {
				t.Fatalf("webhook patch resourceNames = %v, want %v", rule.ResourceNames, want)
			}
			return
		}
	}
	t.Fatal("webhook certificate ClusterRole has no admission-registration rule")
}

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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
