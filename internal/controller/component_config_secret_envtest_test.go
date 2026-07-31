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
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// The config/configSecret exclusivity is a CEL rule on the CRD, so only a real
// apiserver can tell us whether it compiles, fits the cost budget, and rejects
// what it should. The controller's precedence rule keeps a pre-existing CR
// working, but users should hear about the ambiguity at apply time.
func TestSeaweedCRD_ConfigAndConfigSecretAreMutuallyExclusive(t *testing.T) {
	_, cli := mustEnvtest(t)
	ctx := context.Background()

	ns := newTestNamespace(t, ctx, cli, "config-secret")
	t.Cleanup(func() {
		_ = cli.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	})

	inline := "[leveldb2]\nenabled = true\n"
	sel := &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: "cfg"},
		Key:                  "toml",
	}

	cases := []struct {
		name       string
		master     *seaweedv1.MasterSpec
		filer      *seaweedv1.FilerSpec
		wantReject bool
	}{
		{
			name:   "master config alone",
			master: &seaweedv1.MasterSpec{Replicas: 1, Config: &inline},
		},
		{
			name:   "master configSecret alone",
			master: &seaweedv1.MasterSpec{Replicas: 1, ConfigSecret: sel},
		},
		{
			name:       "master both",
			master:     &seaweedv1.MasterSpec{Replicas: 1, Config: &inline, ConfigSecret: sel},
			wantReject: true,
		},
		{
			name:   "filer configSecret alone",
			master: &seaweedv1.MasterSpec{Replicas: 1},
			filer:  &seaweedv1.FilerSpec{Replicas: 1, ConfigSecret: sel},
		},
		{
			name:       "filer both",
			master:     &seaweedv1.MasterSpec{Replicas: 1},
			filer:      &seaweedv1.FilerSpec{Replicas: 1, Config: &inline, ConfigSecret: sel},
			wantReject: true,
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr := minimalVolumeSeaweedCR(ns)
			cr.Name = strings.ToLower(strings.ReplaceAll(tc.name, " ", "-"))
			cr.Spec.Master = tc.master
			cr.Spec.Filer = tc.filer

			err := cli.Create(ctx, cr)
			if err == nil {
				t.Cleanup(func() { _ = cli.Delete(context.Background(), cr) })
			}
			switch {
			case tc.wantReject && err == nil:
				t.Fatalf("case %d: expected the apiserver to reject config + configSecret", i)
			case tc.wantReject && !strings.Contains(err.Error(), "mutually exclusive"):
				t.Fatalf("case %d: expected the exclusivity message, got %v", i, err)
			case !tc.wantReject && err != nil:
				t.Fatalf("case %d: expected the CR to be accepted, got %v", i, err)
			}
		})
	}
}
