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

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// TestAdminScriptReconcile_Envtest drives the AdminScript reconciler against a
// real apiserver: it asserts the controller waits while the referenced cluster
// is missing, then renders an owned CronJob running `weed shell` against the
// cluster masters and mirrors the schedule status back. This exercises the
// full round trip (CronJob create, owner reference, status subresource) that
// the fake-client unit tests cannot, and runs in the standard `make test` job.
//
// The referenced cluster intentionally has no spec.admin, so this also guards
// the no-admin code path end-to-end against a live apiserver.
func TestAdminScriptReconcile_Envtest(t *testing.T) {
	_, cli := mustEnvtest(t)
	ctx := context.Background()
	ns := newTestNamespace(t, ctx, cli, "adminscript")

	r := &AdminScriptReconciler{
		Client:   cli,
		Log:      logr.Discard(),
		Scheme:   cli.Scheme(),
		Recorder: record.NewFakeRecorder(100),
	}

	scriptKey := types.NamespacedName{Name: "nightly", Namespace: ns}
	req := reconcile.Request{NamespacedName: scriptKey}
	script := "lock\nvolume.balance -force\nunlock"

	adminScript := &seaweedv1.AdminScript{
		ObjectMeta: metav1.ObjectMeta{Name: scriptKey.Name, Namespace: ns},
		Spec: seaweedv1.AdminScriptSpec{
			ClusterRef: seaweedv1.AdminScriptClusterRef{Name: "cluster"},
			Schedule:   "0 2 * * *",
			Script:     script,
		},
	}
	if err := cli.Create(ctx, adminScript); err != nil {
		t.Fatalf("create AdminScript: %v", err)
	}

	// 1) Cluster missing: the controller should requeue and not create a
	// CronJob, and surface Pending / Ready=False.
	res, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("reconcile (cluster missing): %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatalf("expected a requeue while the cluster is missing, got %+v", res)
	}
	cron := &batchv1.CronJob{}
	if err := cli.Get(ctx, scriptKey, cron); !apierrors.IsNotFound(err) {
		t.Fatalf("expected no CronJob before the cluster exists, got err=%v", err)
	}
	got := &seaweedv1.AdminScript{}
	if err := cli.Get(ctx, scriptKey, got); err != nil {
		t.Fatalf("get AdminScript: %v", err)
	}
	if got.Status.Phase != seaweedv1.AdminScriptPhasePending {
		t.Errorf("phase = %q, want Pending while cluster missing", got.Status.Phase)
	}

	// 2) Create the cluster (master only, no admin) and reconcile again.
	cluster := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: ns},
		Spec: seaweedv1.SeaweedSpec{
			Image:  "chrislusf/seaweedfs:test",
			Master: &seaweedv1.MasterSpec{Replicas: 1},
		},
	}
	if err := cli.Create(ctx, cluster); err != nil {
		t.Fatalf("create Seaweed: %v", err)
	}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile (cluster present): %v", err)
	}

	if err := cli.Get(ctx, scriptKey, cron); err != nil {
		t.Fatalf("CronJob was not created: %v", err)
	}
	if cron.Spec.Schedule != "0 2 * * *" {
		t.Errorf("schedule = %q, want %q", cron.Spec.Schedule, "0 2 * * *")
	}
	if cron.Spec.ConcurrencyPolicy != batchv1.ForbidConcurrent {
		t.Errorf("concurrencyPolicy = %q, want Forbid", cron.Spec.ConcurrencyPolicy)
	}

	// Owned by the AdminScript so it is garbage-collected on delete.
	if len(cron.OwnerReferences) != 1 {
		t.Fatalf("expected exactly one owner reference, got %d", len(cron.OwnerReferences))
	}
	owner := cron.OwnerReferences[0]
	if owner.Kind != "AdminScript" || owner.Name != scriptKey.Name {
		t.Errorf("owner = %s/%s, want AdminScript/%s", owner.Kind, owner.Name, scriptKey.Name)
	}
	if owner.Controller == nil || !*owner.Controller {
		t.Errorf("owner reference is not a controller reference")
	}

	containers := cron.Spec.JobTemplate.Spec.Template.Spec.Containers
	if len(containers) != 1 {
		t.Fatalf("expected exactly one container, got %d", len(containers))
	}
	c := containers[0]
	if len(c.Command) < 6 || c.Command[2] != `printf '%s\n' "$WEED_SHELL_SCRIPT" | "$@"` || c.Command[4] != "weed" {
		t.Fatalf("unexpected container command: %v", c.Command)
	}
	if argv := strings.Join(c.Command[4:], " "); !strings.Contains(argv, "shell -master=cluster-master-0.") {
		t.Errorf("command does not run weed shell against the masters: %q", argv)
	}
	var scriptEnv string
	for _, e := range c.Env {
		if e.Name == "WEED_SHELL_SCRIPT" {
			scriptEnv = e.Value
		}
	}
	if scriptEnv != script {
		t.Errorf("WEED_SHELL_SCRIPT env = %q, want the spec script verbatim", scriptEnv)
	}

	// Status reflects a scheduled CronJob.
	if err := cli.Get(ctx, scriptKey, got); err != nil {
		t.Fatalf("get AdminScript: %v", err)
	}
	if got.Status.Phase != seaweedv1.AdminScriptPhaseActive {
		t.Errorf("phase = %q, want Active", got.Status.Phase)
	}
	if got.Status.CronJobName != scriptKey.Name {
		t.Errorf("status.cronJobName = %q, want %q", got.Status.CronJobName, scriptKey.Name)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, seaweedv1.AdminScriptConditionReady)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Errorf("Ready condition = %+v, want status True", cond)
	}
}
