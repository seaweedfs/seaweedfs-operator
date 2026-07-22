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
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// writeCountingClient wraps a client.Client and tallies write calls plus the
// 4xx-class responses the apiserver returns for them — the same signal
// rest_client_requests_total exposes to API-client error-rate alerting.
// Reads pass through uncounted: in production they are served from the
// informer cache and never reach the apiserver.
type writeCountingClient struct {
	client.Client
	creates             int
	updates             int
	deletes             int
	createAlreadyExists int // 409
	deleteNotFound      int // 404
}

func (c *writeCountingClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	c.creates++
	err := c.Client.Create(ctx, obj, opts...)
	if apierrors.IsAlreadyExists(err) {
		c.createAlreadyExists++
	}
	return err
}

func (c *writeCountingClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	c.updates++
	return c.Client.Update(ctx, obj, opts...)
}

func (c *writeCountingClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	c.deletes++
	err := c.Client.Delete(ctx, obj, opts...)
	if apierrors.IsNotFound(err) {
		c.deleteNotFound++
	}
	return err
}

func (c *writeCountingClient) reset() {
	c.creates, c.updates, c.deletes = 0, 0, 0
	c.createAlreadyExists, c.deleteNotFound = 0, 0
}

// TestReconcile_ConvergedClusterEmitsNoFailedWrites drives the full Seaweed
// reconcile loop against a real apiserver until it converges, then asserts
// that a steady-state pass issues no Create answered with 409 AlreadyExists
// and no Delete answered with 404 NotFound. A converged pass should probe by
// read (a cache hit in production) and stay write-silent, so client
// error-rate alerting sees near-zero 4xx from the operator.
func TestReconcile_ConvergedClusterEmitsNoFailedWrites(t *testing.T) {
	_, cli := mustEnvtest(t)
	ctx := context.Background()

	ns := newTestNamespace(t, ctx, cli, "steady")
	t.Cleanup(func() {
		_ = cli.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	})

	// Master + volume + filer, no S3/SFTP: the absent optional gateways make
	// every pass run the teardown prune, which is where the 404s came from.
	// ConcurrentStart skips the master pod-readiness wait — envtest runs no
	// kubelets, so no pod would ever report Running.
	concurrentStart := true
	diskCount := int32(1)
	cr := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "steady", Namespace: ns},
		Spec: seaweedv1.SeaweedSpec{
			Image:                 "chrislusf/seaweedfs:latest",
			VolumeServerDiskCount: &diskCount,
			Master: &seaweedv1.MasterSpec{
				Replicas:        1,
				ConcurrentStart: &concurrentStart,
			},
			Volume: &seaweedv1.VolumeSpec{
				Replicas: 1,
				VolumeServerConfig: seaweedv1.VolumeServerConfig{
					ResourceRequirements: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
					},
				},
			},
			Filer: &seaweedv1.FilerSpec{Replicas: 1},
		},
	}
	if err := cli.Create(ctx, cr); err != nil {
		t.Fatalf("create Seaweed CR: %v", err)
	}

	counting := &writeCountingClient{Client: cli}
	r := &SeaweedReconciler{
		Client: counting,
		Log:    logf.FromContext(ctx),
		Scheme: cli.Scheme(),
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: ns}}

	// Converge: the first pass creates every owned object, the following
	// passes stamp last-applied annotations via the merge path.
	for i := 0; i < 3; i++ {
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatalf("convergence reconcile %d: %v", i+1, err)
		}
	}

	counting.reset()
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("steady-state reconcile: %v", err)
	}

	if counting.createAlreadyExists != 0 {
		t.Errorf("steady-state reconcile issued %d Create calls answered with 409 AlreadyExists, want 0", counting.createAlreadyExists)
	}
	if counting.deleteNotFound != 0 {
		t.Errorf("steady-state reconcile issued %d Delete calls answered with 404 NotFound, want 0", counting.deleteNotFound)
	}
	if counting.creates != 0 {
		t.Errorf("steady-state reconcile issued %d Create calls, want 0: every owned object already exists", counting.creates)
	}
	if counting.deletes != 0 {
		t.Errorf("steady-state reconcile issued %d Delete calls, want 0: absent components should be probed by read", counting.deletes)
	}
}

// TestCreateOrUpdate_ReadsBeforeWriting pins the helper's write behavior at
// the API level: a missing object costs exactly one Create, an unchanged
// object costs no write at all, and a drifted object costs exactly one
// Update that lands.
func TestCreateOrUpdate_ReadsBeforeWriting(t *testing.T) {
	_, cli := mustEnvtest(t)
	ctx := context.Background()

	ns := newTestNamespace(t, ctx, cli, "createorupdate")
	t.Cleanup(func() {
		_ = cli.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	})

	counting := &writeCountingClient{Client: cli}
	r := &SeaweedReconciler{
		Client: counting,
		Log:    logf.FromContext(ctx),
		Scheme: cli.Scheme(),
	}

	cm := func(v string) *corev1.ConfigMap {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "cou", Namespace: ns},
			Data:       map[string]string{"k": v},
		}
	}

	if _, err := r.CreateOrUpdateConfigMap(cm("v1")); err != nil {
		t.Fatalf("initial pass: %v", err)
	}
	if counting.creates != 1 || counting.updates != 0 {
		t.Errorf("missing object: got %d creates / %d updates, want 1 / 0", counting.creates, counting.updates)
	}

	counting.reset()
	if _, err := r.CreateOrUpdateConfigMap(cm("v1")); err != nil {
		t.Fatalf("no-op pass: %v", err)
	}
	if counting.creates != 0 || counting.createAlreadyExists != 0 || counting.updates != 0 {
		t.Errorf("unchanged object: got %d creates (%d conflicted) / %d updates, want no writes",
			counting.creates, counting.createAlreadyExists, counting.updates)
	}

	counting.reset()
	if _, err := r.CreateOrUpdateConfigMap(cm("v2")); err != nil {
		t.Fatalf("drift pass: %v", err)
	}
	if counting.creates != 0 || counting.updates != 1 {
		t.Errorf("drifted object: got %d creates / %d updates, want 0 / 1", counting.creates, counting.updates)
	}
	var got corev1.ConfigMap
	if err := cli.Get(ctx, types.NamespacedName{Name: "cou", Namespace: ns}, &got); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if got.Data["k"] != "v2" {
		t.Errorf("update did not land: Data[k]=%q, want %q", got.Data["k"], "v2")
	}
}
