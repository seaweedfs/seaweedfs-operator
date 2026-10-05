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

package manager

import (
	"bytes"
	"io"
	"os"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func TestManagerDeploymentSecurityContext(t *testing.T) {
	data, err := os.ReadFile("manager.yaml")
	if err != nil {
		t.Fatalf("read manager manifest: %v", err)
	}

	var deployment appsv1.Deployment
	found := false
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	for {
		var candidate appsv1.Deployment
		if err := decoder.Decode(&candidate); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("decode manager Deployment: %v", err)
		}
		if candidate.Kind != "Deployment" {
			continue
		}
		deployment = candidate
		found = true
		break
	}
	if !found {
		t.Fatal("manager manifest has no Deployment")
	}

	pod := deployment.Spec.Template.Spec
	assertManagerPodSecurityContext(t, pod.SecurityContext)
	if len(pod.Containers) != 1 {
		t.Fatalf("manager Deployment has %d containers, want 1", len(pod.Containers))
	}
	assertManagerContainerSecurityContext(t, pod.Containers[0].SecurityContext)
}

func assertManagerPodSecurityContext(t *testing.T, securityContext *corev1.PodSecurityContext) {
	t.Helper()
	if securityContext == nil {
		t.Fatal("manager pod securityContext is nil")
	}
	if securityContext.RunAsNonRoot == nil || !*securityContext.RunAsNonRoot {
		t.Errorf("manager pod runAsNonRoot = %v, want true", securityContext.RunAsNonRoot)
	}
	if securityContext.RunAsUser == nil || *securityContext.RunAsUser != 65532 {
		t.Errorf("manager pod runAsUser = %v, want 65532", securityContext.RunAsUser)
	}
	if securityContext.FSGroup == nil || *securityContext.FSGroup != 65532 {
		t.Errorf("manager pod fsGroup = %v, want 65532", securityContext.FSGroup)
	}
	if securityContext.SeccompProfile == nil ||
		securityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("manager pod seccompProfile = %#v, want RuntimeDefault", securityContext.SeccompProfile)
	}
}

func assertManagerContainerSecurityContext(t *testing.T, securityContext *corev1.SecurityContext) {
	t.Helper()
	if securityContext == nil {
		t.Fatal("manager container securityContext is nil")
	}
	if securityContext.AllowPrivilegeEscalation == nil || *securityContext.AllowPrivilegeEscalation {
		t.Errorf(
			"manager container allowPrivilegeEscalation = %v, want false",
			securityContext.AllowPrivilegeEscalation,
		)
	}
	if securityContext.ReadOnlyRootFilesystem == nil || !*securityContext.ReadOnlyRootFilesystem {
		t.Errorf(
			"manager container readOnlyRootFilesystem = %v, want true",
			securityContext.ReadOnlyRootFilesystem,
		)
	}
	if securityContext.RunAsNonRoot == nil || !*securityContext.RunAsNonRoot {
		t.Errorf("manager container runAsNonRoot = %v, want true", securityContext.RunAsNonRoot)
	}
	if !containsCapability(securityContext.Capabilities, corev1.Capability("ALL")) {
		t.Errorf(
			"manager container capabilities.drop = %v, want ALL",
			securityContext.Capabilities,
		)
	}
}

func containsCapability(capabilities *corev1.Capabilities, want corev1.Capability) bool {
	if capabilities == nil {
		return false
	}
	for _, capability := range capabilities.Drop {
		if capability == want {
			return true
		}
	}
	return false
}
