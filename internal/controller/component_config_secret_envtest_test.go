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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

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

// replaceOnGetClient swaps the target ConfigMap for an unowned one with a
// fresh UID immediately after it is read, standing in for another actor
// deleting and recreating it in the window between the reconciler's read and
// its delete. Only the fake client's ResourceVersion precondition is
// simulated in-memory, so the UID precondition needs a real apiserver.
type replaceOnGetClient struct {
	client.Client
	target   string
	replaced bool
}

func (c *replaceOnGetClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := c.Client.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	cm, ok := obj.(*corev1.ConfigMap)
	if !ok || c.replaced || key.Name != c.target {
		return nil
	}
	c.replaced = true
	if err := c.Client.Delete(ctx, cm.DeepCopy()); err != nil {
		return err
	}
	return c.Client.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Data:       map[string]string{"unrelated": "data"},
	})
}

// Pruning reads the ConfigMap, checks that this CR controls it, then deletes
// it. Without a precondition tying those together, an object recreated under
// the same name in between would be deleted on the strength of the previous
// object's owner reference.
func TestPruneOwnedConfigMap_LeavesAReplacementAlone(t *testing.T) {
	_, cli := mustEnvtest(t)
	ctx := context.Background()

	ns := newTestNamespace(t, ctx, cli, "prune-race")
	t.Cleanup(func() {
		_ = cli.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	})

	cr := minimalVolumeSeaweedCR(ns)
	if err := cli.Create(ctx, cr); err != nil {
		t.Fatalf("create Seaweed: %v", err)
	}

	owned := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: cr.Name + "-filer", Namespace: ns},
		Data:       map[string]string{"filer.toml": "[postgres]\npassword = \"hunter2\"\n"},
	}
	if err := controllerutil.SetControllerReference(cr, owned, cli.Scheme()); err != nil {
		t.Fatalf("set owner ref: %v", err)
	}
	if err := cli.Create(ctx, owned); err != nil {
		t.Fatalf("create ConfigMap: %v", err)
	}

	racing := &replaceOnGetClient{Client: cli, target: owned.Name}
	r := &SeaweedReconciler{Client: racing, Log: logf.FromContext(ctx), Scheme: cli.Scheme()}

	// The conflict is the expected outcome, reported as success: the object
	// under that name is no longer ours to remove.
	if err := r.pruneOwnedConfigMap(ctx, cr, owned.Name); err != nil {
		t.Fatalf("expected the replacement to be left alone without an error, got %v", err)
	}
	if !racing.replaced {
		t.Fatal("test did not exercise the race: the ConfigMap was never replaced")
	}

	got := &corev1.ConfigMap{}
	if err := cli.Get(ctx, client.ObjectKey{Namespace: ns, Name: owned.Name}, got); err != nil {
		t.Fatalf("expected the replacement ConfigMap to survive, got %v", err)
	}
	if got.Data["unrelated"] != "data" {
		t.Errorf("expected the replacement to be untouched, got %v", got.Data)
	}
}
