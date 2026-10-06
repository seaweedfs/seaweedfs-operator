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
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

// Checks the Deployment rendered by the default overlay applied by make deploy.
func TestManagerDeploymentSecurityContext(t *testing.T) {
	kustomizer := krusty.MakeKustomizer(krusty.MakeDefaultOptions())
	resources, err := kustomizer.Run(filesys.MakeFsOnDisk(), "../default")
	if err != nil {
		t.Fatalf("build default Kustomize overlay: %v", err)
	}
	data, err := resources.AsYaml()
	if err != nil {
		t.Fatalf("serialize default Kustomize overlay: %v", err)
	}

	var deployment appsv1.Deployment
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
		break
	}
	if deployment.Name == "" {
		t.Fatal("manager manifest has no Deployment")
	}

	pod := deployment.Spec.Template.Spec
	assertPodSecurityContext(t, pod.SecurityContext)
	if len(pod.Containers) != 1 {
		t.Fatalf("manager Deployment has %d containers, want 1", len(pod.Containers))
	}
	assertContainerSecurityContext(t, pod.Containers[0].SecurityContext)
}

func assertPodSecurityContext(t *testing.T, securityContext *corev1.PodSecurityContext) {
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

func assertContainerSecurityContext(t *testing.T, securityContext *corev1.SecurityContext) {
	t.Helper()
	if securityContext == nil {
		t.Fatal("manager container securityContext is nil")
	}
	if securityContext.AllowPrivilegeEscalation == nil || *securityContext.AllowPrivilegeEscalation {
		t.Errorf("manager container allowPrivilegeEscalation = %v, want false", securityContext.AllowPrivilegeEscalation)
	}
	if securityContext.ReadOnlyRootFilesystem == nil || !*securityContext.ReadOnlyRootFilesystem {
		t.Errorf("manager container readOnlyRootFilesystem = %v, want true", securityContext.ReadOnlyRootFilesystem)
	}
	if securityContext.RunAsNonRoot == nil || !*securityContext.RunAsNonRoot {
		t.Errorf("manager container runAsNonRoot = %v, want true", securityContext.RunAsNonRoot)
	}
	if securityContext.Capabilities == nil ||
		!slices.Contains(securityContext.Capabilities.Drop, corev1.Capability("ALL")) {
		t.Errorf("manager container capabilities.drop = %v, want ALL", securityContext.Capabilities)
	}
}
