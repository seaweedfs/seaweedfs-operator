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
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func s3HTTPSTestCR(s3 *seaweedv1.S3GatewaySpec) *seaweedv1.Seaweed {
	return &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1},
			Filer:  &seaweedv1.FilerSpec{Replicas: 1},
			S3:     s3,
		},
	}
}

func TestS3HTTPS_DisabledByDefault(t *testing.T) {
	m := s3HTTPSTestCR(&seaweedv1.S3GatewaySpec{Replicas: 1})
	r := &SeaweedReconciler{}

	for _, p := range r.buildS3Service(m).Spec.Ports {
		if p.Name == "s3-https" {
			t.Fatalf("Service should not expose s3-https without httpsPort/tlsSecret: %+v", p)
		}
	}
	dep := r.buildS3Deployment(m)
	c := dep.Spec.Template.Spec.Containers[0]
	for _, p := range c.Ports {
		if p.Name == "s3-https" {
			t.Fatalf("container should not expose s3-https: %+v", p)
		}
	}
	for _, v := range dep.Spec.Template.Spec.Volumes {
		if v.Name == s3HTTPSVolumeName {
			t.Fatalf("unexpected HTTPS TLS volume: %+v", v)
		}
	}
	script := c.Command[len(c.Command)-1]
	for _, flag := range []string{"-port.https", "-cert.file", "-key.file"} {
		if strings.Contains(script, flag) {
			t.Errorf("startup script should not contain %s: %s", flag, script)
		}
	}
}

// The CRD rejects either field alone; the builders still must not mount an
// unnamed Secret or dereference a nil TLSSecret if one reaches them.
func TestS3HTTPS_RequiresBothPortAndSecret(t *testing.T) {
	port := int32(8334)
	cases := map[string]*seaweedv1.S3GatewaySpec{
		"port only":         {Replicas: 1, HTTPSPort: &port},
		"secret only":       {Replicas: 1, TLSSecret: &corev1.LocalObjectReference{Name: "s3-tls"}},
		"empty secret name": {Replicas: 1, HTTPSPort: &port, TLSSecret: &corev1.LocalObjectReference{}},
	}
	for name, s3 := range cases {
		t.Run(name, func(t *testing.T) {
			if got := s3HTTPSPort(s3HTTPSTestCR(s3)); got != 0 {
				t.Errorf("s3HTTPSPort = %d, want 0", got)
			}
		})
	}
}

func TestS3HTTPS_Enabled(t *testing.T) {
	port := int32(8334)
	m := s3HTTPSTestCR(&seaweedv1.S3GatewaySpec{
		Replicas:  1,
		HTTPSPort: &port,
		TLSSecret: &corev1.LocalObjectReference{Name: "s3-tls"},
	})
	r := &SeaweedReconciler{}

	svcPorts := map[string]corev1.ServicePort{}
	for _, p := range r.buildS3Service(m).Spec.Ports {
		svcPorts[p.Name] = p
	}
	if _, ok := svcPorts["s3-http"]; !ok {
		t.Error("Service must keep the s3-http port alongside HTTPS")
	}
	https, ok := svcPorts["s3-https"]
	if !ok {
		t.Fatal("Service is missing the s3-https port")
	}
	if https.Port != port || https.TargetPort.IntValue() != int(port) {
		t.Errorf("s3-https port = %d -> %s, want %d -> %d", https.Port, https.TargetPort.String(), port, port)
	}

	dep := r.buildS3Deployment(m)
	pod := dep.Spec.Template.Spec
	c := pod.Containers[0]

	var gotContainerPort bool
	for _, p := range c.Ports {
		if p.Name == "s3-https" && p.ContainerPort == port {
			gotContainerPort = true
		}
	}
	if !gotContainerPort {
		t.Errorf("container ports %+v missing s3-https:%d", c.Ports, port)
	}

	var vol *corev1.Volume
	for i := range pod.Volumes {
		if pod.Volumes[i].Name == s3HTTPSVolumeName {
			vol = &pod.Volumes[i]
		}
	}
	if vol == nil || vol.Secret == nil || vol.Secret.SecretName != "s3-tls" {
		t.Fatalf("expected a Secret volume for s3-tls, got %+v", vol)
	}
	// Projecting exactly these keys makes kubelet fail the mount when one is
	// missing, instead of starting a pod whose TLS handshakes hang.
	wantItems := []corev1.KeyToPath{{Key: "tls.crt", Path: "tls.crt"}, {Key: "tls.key", Path: "tls.key"}}
	if !reflect.DeepEqual(vol.Secret.Items, wantItems) {
		t.Errorf("Secret volume items = %+v, want %+v", vol.Secret.Items, wantItems)
	}
	var gotMount bool
	for _, vm := range c.VolumeMounts {
		if vm.Name == s3HTTPSVolumeName && vm.MountPath == s3HTTPSTLSMountPath && vm.ReadOnly {
			gotMount = true
		}
	}
	if !gotMount {
		t.Errorf("container mounts %+v missing read-only %s at %s", c.VolumeMounts, s3HTTPSVolumeName, s3HTTPSTLSMountPath)
	}

	script := c.Command[len(c.Command)-1]
	for _, want := range []string{
		"-port=8333",
		"-port.https=8334",
		"-cert.file=/etc/sw-s3-tls/tls.crt",
		"-key.file=/etc/sw-s3-tls/tls.key",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("startup script missing %q: %s", want, script)
		}
	}

	if strings.Contains(script, "-port.grpc") {
		t.Errorf("startup script should leave the gRPC port to weed's default: %s", script)
	}

	// HTTP keeps serving, so the probes stay on the plain HTTP port.
	for name, probe := range map[string]*corev1.Probe{"readiness": c.ReadinessProbe, "liveness": c.LivenessProbe} {
		if got := probe.HTTPGet.Port.IntValue(); got != int(s3EffectivePort(m)) {
			t.Errorf("%s probe port = %d, want HTTP port %d", name, got, s3EffectivePort(m))
		}
		if probe.HTTPGet.Scheme != corev1.URISchemeHTTP {
			t.Errorf("%s probe scheme = %q, want HTTP", name, probe.HTTPGet.Scheme)
		}
	}
}

func TestS3HTTPS_HTTPSOnly(t *testing.T) {
	port := int32(8334)
	m := s3HTTPSTestCR(&seaweedv1.S3GatewaySpec{
		Replicas:  1,
		HTTPSPort: &port,
		HTTPSOnly: true,
		TLSSecret: &corev1.LocalObjectReference{Name: "s3-tls"},
	})
	r := &SeaweedReconciler{}

	svc := r.buildS3Service(m)
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Name != "s3-https" || svc.Spec.Ports[0].Port != port {
		t.Errorf("Service ports = %+v, want only s3-https:%d", svc.Spec.Ports, port)
	}

	c := r.buildS3Deployment(m).Spec.Template.Spec.Containers[0]
	if len(c.Ports) != 1 || c.Ports[0].Name != "s3-https" || c.Ports[0].ContainerPort != port {
		t.Errorf("container ports = %+v, want only s3-https:%d", c.Ports, port)
	}

	for name, probe := range map[string]*corev1.Probe{"readiness": c.ReadinessProbe, "liveness": c.LivenessProbe} {
		if got := probe.HTTPGet.Port.IntValue(); got != int(port) {
			t.Errorf("%s probe port = %d, want HTTPS port %d", name, got, port)
		}
		if probe.HTTPGet.Scheme != corev1.URISchemeHTTPS {
			t.Errorf("%s probe scheme = %q, want HTTPS", name, probe.HTTPGet.Scheme)
		}
	}

	// weed s3 serves TLS on -port when -port.https is unset; -port.grpc keeps
	// gRPC on 18333 rather than following -port to 18334.
	script := c.Command[len(c.Command)-1]
	for _, want := range []string{
		"-port=8334",
		"-port.grpc=18333",
		"-cert.file=/etc/sw-s3-tls/tls.crt",
		"-key.file=/etc/sw-s3-tls/tls.key",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("startup script missing %q: %s", want, script)
		}
	}
	for _, unwanted := range []string{"-port=8333", "-port.https"} {
		if strings.Contains(script, unwanted) {
			t.Errorf("startup script should not contain %q: %s", unwanted, script)
		}
	}
}

// httpsOnly without a usable HTTPS config falls back to plain HTTP rather
// than rendering a gateway with no listener.
func TestS3HTTPS_HTTPSOnlyIgnoredWithoutHTTPS(t *testing.T) {
	m := s3HTTPSTestCR(&seaweedv1.S3GatewaySpec{Replicas: 1, HTTPSOnly: true})
	if s3HTTPSOnly(m) {
		t.Fatal("s3HTTPSOnly = true without httpsPort/tlsSecret")
	}
	svc := (&SeaweedReconciler{}).buildS3Service(m)
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Name != "s3-http" {
		t.Errorf("Service ports = %+v, want only s3-http", svc.Spec.Ports)
	}
}

// The HTTPS certificate is user-supplied and separate from the spec.tls
// mTLS material, so both mounts coexist without sharing a path.
func TestS3HTTPS_CoexistsWithComponentTLS(t *testing.T) {
	certManagerCRDMu.Lock()
	prev := certManagerCRDAvailable
	certManagerCRDAvailable = true
	certManagerCRDMu.Unlock()
	defer func() {
		certManagerCRDMu.Lock()
		certManagerCRDAvailable = prev
		certManagerCRDMu.Unlock()
	}()

	port := int32(8334)
	m := s3HTTPSTestCR(&seaweedv1.S3GatewaySpec{
		Replicas:  1,
		HTTPSPort: &port,
		TLSSecret: &corev1.LocalObjectReference{Name: "public-s3-tls"},
	})
	m.Spec.TLS = &seaweedv1.TLSSpec{Enabled: true}

	pod := (&SeaweedReconciler{}).buildS3Deployment(m).Spec.Template.Spec
	secrets := map[string]string{}
	for _, v := range pod.Volumes {
		if v.Secret != nil {
			secrets[v.Name] = v.Secret.SecretName
		}
	}
	if secrets[s3HTTPSVolumeName] != "public-s3-tls" {
		t.Errorf("HTTPS volume secret = %q, want public-s3-tls", secrets[s3HTTPSVolumeName])
	}
	if secrets[tlsVolumeName] != TLSServerSecretName(m) {
		t.Errorf("mTLS volume secret = %q, want %q", secrets[tlsVolumeName], TLSServerSecretName(m))
	}
}

// Both rules are CEL on the CRD, so only a real apiserver can confirm they
// compile and reject what they should.
func TestSeaweedCRD_S3HTTPSValidation(t *testing.T) {
	_, cli := mustEnvtest(t)
	ctx := context.Background()

	ns := newTestNamespace(t, ctx, cli, "s3-https")
	t.Cleanup(func() {
		_ = cli.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	})

	i32 := func(v int32) *int32 { return &v }
	secret := &corev1.LocalObjectReference{Name: "s3-tls"}

	cases := []struct {
		name       string
		s3         *seaweedv1.S3GatewaySpec
		hostSuffix string
		wantErr    string
	}{
		{name: "http only", s3: &seaweedv1.S3GatewaySpec{Replicas: 1}},
		{name: "https", s3: &seaweedv1.S3GatewaySpec{Replicas: 1, HTTPSPort: i32(8334), TLSSecret: secret}},
		{name: "https custom port", s3: &seaweedv1.S3GatewaySpec{Replicas: 1, Port: i32(9000), HTTPSPort: i32(8333), TLSSecret: secret}},
		{
			name:    "port without secret",
			s3:      &seaweedv1.S3GatewaySpec{Replicas: 1, HTTPSPort: i32(8334)},
			wantErr: "must be set together",
		},
		{
			name:    "secret without port",
			s3:      &seaweedv1.S3GatewaySpec{Replicas: 1, TLSSecret: secret},
			wantErr: "must be set together",
		},
		{
			name:    "empty secret name",
			s3:      &seaweedv1.S3GatewaySpec{Replicas: 1, HTTPSPort: i32(8334), TLSSecret: &corev1.LocalObjectReference{}},
			wantErr: "must be set together",
		},
		{
			name:    "same as default port",
			s3:      &seaweedv1.S3GatewaySpec{Replicas: 1, HTTPSPort: i32(8333), TLSSecret: secret},
			wantErr: "must differ from port",
		},
		{
			name:    "same as custom port",
			s3:      &seaweedv1.S3GatewaySpec{Replicas: 1, Port: i32(9000), HTTPSPort: i32(9000), TLSSecret: secret},
			wantErr: "must differ from port",
		},
		{
			name:    "same as default grpc port",
			s3:      &seaweedv1.S3GatewaySpec{Replicas: 1, HTTPSPort: i32(18333), TLSSecret: secret},
			wantErr: "must not collide",
		},
		{
			name:    "same as custom grpc port",
			s3:      &seaweedv1.S3GatewaySpec{Replicas: 1, Port: i32(9000), HTTPSPort: i32(19000), TLSSecret: secret},
			wantErr: "must not collide",
		},
		{
			name:    "same as iceberg port",
			s3:      &seaweedv1.S3GatewaySpec{Replicas: 1, HTTPSPort: i32(8181), TLSSecret: secret},
			wantErr: "must not collide",
		},
		{
			name:    "same as lance port",
			s3:      &seaweedv1.S3GatewaySpec{Replicas: 1, HTTPSPort: i32(9101), TLSSecret: secret},
			wantErr: "must not collide",
		},
		{
			name:    "same as metrics port",
			s3:      &seaweedv1.S3GatewaySpec{Replicas: 1, MetricsPort: i32(9327), HTTPSPort: i32(9327), TLSSecret: secret},
			wantErr: "must not collide",
		},
		{
			name: "https only",
			s3:   &seaweedv1.S3GatewaySpec{Replicas: 1, HTTPSPort: i32(8334), HTTPSOnly: true, TLSSecret: secret},
		},
		{
			name:    "https only without https",
			s3:      &seaweedv1.S3GatewaySpec{Replicas: 1, HTTPSOnly: true},
			wantErr: "httpsOnly requires httpsPort",
		},
		{
			name: "https only with ingress",
			s3: &seaweedv1.S3GatewaySpec{
				Replicas: 1, HTTPSPort: i32(8334), HTTPSOnly: true, TLSSecret: secret,
				Ingress: &seaweedv1.IngressSpec{Enabled: true, Host: "s3.example.com"},
			},
			wantErr: "not supported with ingress",
		},
		{
			name:       "https only with host suffix",
			s3:         &seaweedv1.S3GatewaySpec{Replicas: 1, HTTPSPort: i32(8334), HTTPSOnly: true, TLSSecret: secret},
			hostSuffix: "example.com",
			wantErr:    "not supported with spec.hostSuffix",
		},
		{
			name:       "https with host suffix",
			s3:         &seaweedv1.S3GatewaySpec{Replicas: 1, HTTPSPort: i32(8334), TLSSecret: secret},
			hostSuffix: "example.com",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr := minimalVolumeSeaweedCR(ns)
			cr.Name = strings.ReplaceAll(tc.name, " ", "-")
			cr.Spec.Filer = &seaweedv1.FilerSpec{Replicas: 1}
			cr.Spec.S3 = tc.s3
			if tc.hostSuffix != "" {
				cr.Spec.HostSuffix = &tc.hostSuffix
			}

			err := cli.Create(ctx, cr)
			if err == nil {
				t.Cleanup(func() { _ = cli.Delete(context.Background(), cr) })
			}
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("expected the CR to be accepted, got %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("expected the apiserver to reject the CR with %q", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("expected %q, got %v", tc.wantErr, err)
			}
		})
	}
}
