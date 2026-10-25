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
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/internal/controller/swadmin"
)

const (
	testIssuer = "https://oidc.example.org"
	testSVID   = "spiffe://example.org/ns/app/sa/app"
)

func readyTestProvider(name string, ref seaweedv1.SeaweedReference) *seaweedv1.S3OIDCProvider {
	return &seaweedv1.S3OIDCProvider{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media"},
		Spec: seaweedv1.S3OIDCProviderSpec{
			SeaweedRef: ref, IssuerURL: testIssuer, ClientIDs: []string{"s3"},
		},
		Status: seaweedv1.S3OIDCProviderStatus{
			Phase: seaweedv1.S3PhaseReady, ProviderArn: "arn:aws:iam:::oidc-provider/oidc.example.org",
		},
	}
}

func testRole(name string, mutate ...func(*seaweedv1.S3Role)) *seaweedv1.S3Role {
	role := &seaweedv1.S3Role{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media"},
		Spec: seaweedv1.S3RoleSpec{
			SeaweedRef: iamSeaweedRef(),
			WebIdentity: seaweedv1.S3RoleWebIdentity{
				ProviderRef: seaweedv1.S3OIDCProviderRef{Name: "spire"},
				Subjects:    []string{testSVID},
			},
		},
	}
	for _, m := range mutate {
		m(role)
	}
	return role
}

func newRoleReconciler(t *testing.T, fa *fakeIAMAdmin, objs ...client.Object) (*S3RoleReconciler, client.Client) {
	t.Helper()
	scheme := iamTestScheme(t)
	cli := iamTestClient(t, scheme, append([]client.Object{newTestSeaweed()}, objs...)...)
	r := &S3RoleReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)
	return r, cli
}

func getRole(t *testing.T, cli client.Client, name string) *seaweedv1.S3Role {
	t.Helper()
	var got seaweedv1.S3Role
	if err := cli.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: name}, &got); err != nil {
		t.Fatalf("get S3Role: %v", err)
	}
	return &got
}

func readyReason(role *seaweedv1.S3Role) string {
	c := meta.FindStatusCondition(role.Status.Conditions, seaweedv1.S3ConditionReady)
	if c == nil {
		return ""
	}
	return c.Reason
}

func TestS3Role_PutsTrustPolicyAndResolvedPolicies(t *testing.T) {
	fa := newFakeIAMAdmin()
	fa.policies["media-rw"] = `{}`
	pol := &seaweedv1.S3Policy{
		ObjectMeta: metav1.ObjectMeta{Name: "rw", Namespace: "media"},
		Spec:       seaweedv1.S3PolicySpec{SeaweedRef: iamSeaweedRef(), Name: "media-rw", PolicyDocument: `{}`},
	}
	role := testRole("app", func(r *seaweedv1.S3Role) {
		r.Spec.PolicyRefs = []seaweedv1.S3PolicyRef{{Name: "rw"}}
		r.Spec.WebIdentity.Subjects = []string{testSVID, "spiffe://example.org/ns/app/sa/batch", testSVID}
		r.Spec.MaxSessionDuration = 3600
	})
	r, cli := newRoleReconciler(t, fa, readyTestProvider("spire", iamSeaweedRef()), pol, role)

	reconcileStable(t, r, types.NamespacedName{Namespace: "media", Name: "app"}, 5)

	put, ok := fa.roles["app"]
	if !ok {
		t.Fatalf("PutRole not called; calls=%v", fa.calls)
	}
	if strings.Join(put.AttachedPolicies, ",") != "media-rw" {
		t.Errorf("attached = %v, want the S3Policy's IAM name media-rw", put.AttachedPolicies)
	}
	if put.MaxSessionDuration != 3600 {
		t.Errorf("maxSessionDuration = %d", put.MaxSessionDuration)
	}
	want := `{"Statement":[{"Action":["sts:AssumeRoleWithWebIdentity"],"Condition":{"StringEquals":{"oidc:sub":["spiffe://example.org/ns/app/sa/app","spiffe://example.org/ns/app/sa/batch"]}},"Effect":"Allow","Principal":{"Federated":"https://oidc.example.org"}}],"Version":"2012-10-17"}`
	if put.TrustPolicy != want {
		t.Errorf("trust policy:\n got %s\nwant %s", put.TrustPolicy, want)
	}
	got := getRole(t, cli, "app")
	if got.Status.Phase != seaweedv1.S3PhaseReady || got.Status.RoleArn != "arn:aws:iam::role/app" || got.Status.RoleName != "app" {
		t.Errorf("status = %+v", got.Status)
	}
}

func TestS3Role_WaitsForItsProvider(t *testing.T) {
	notReady := readyTestProvider("spire", iamSeaweedRef())
	notReady.Status = seaweedv1.S3OIDCProviderStatus{Phase: seaweedv1.S3PhasePending}
	otherCluster := readyTestProvider("spire", seaweedv1.SeaweedReference{Name: "other", Namespace: "seaweedfs"})

	cases := []struct {
		name     string
		provider client.Object
		reason   string
	}{
		{"missing", nil, "ProviderMissing"},
		{"not registered", notReady, "ProviderNotReady"},
		{"other cluster", otherCluster, "ProviderClusterMismatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fa := newFakeIAMAdmin()
			objs := []client.Object{testRole("app")}
			if tc.provider != nil {
				objs = append(objs, tc.provider)
			}
			r, cli := newRoleReconciler(t, fa, objs...)
			key := types.NamespacedName{Namespace: "media", Name: "app"}
			reconcileOnce(t, r, key) // adds the finalizer
			res := reconcileOnce(t, r, key)
			if res.RequeueAfter != requeueAfterTransient {
				t.Errorf("requeueAfter = %v, want transient backoff", res.RequeueAfter)
			}
			got := getRole(t, cli, "app")
			if got.Status.Phase != seaweedv1.S3PhasePending || readyReason(got) != tc.reason {
				t.Errorf("phase=%s reason=%s, want Pending/%s", got.Status.Phase, readyReason(got), tc.reason)
			}
			if _, put := fa.roles["app"]; put {
				t.Error("PutRole was called before the provider was usable")
			}
		})
	}
}

func TestS3Role_WaitsForItsPolicies(t *testing.T) {
	fa := newFakeIAMAdmin()
	role := testRole("app", func(r *seaweedv1.S3Role) { r.Spec.PolicyRefs = []seaweedv1.S3PolicyRef{{Name: "not-yet"}} })
	r, cli := newRoleReconciler(t, fa, readyTestProvider("spire", iamSeaweedRef()), role)
	key := types.NamespacedName{Namespace: "media", Name: "app"}
	reconcileOnce(t, r, key)
	reconcileOnce(t, r, key)

	got := getRole(t, cli, "app")
	if got.Status.Phase != seaweedv1.S3PhasePending || readyReason(got) != "PolicyMissing" {
		t.Errorf("phase=%s reason=%s, want Pending/PolicyMissing", got.Status.Phase, readyReason(got))
	}
	if _, put := fa.roles["app"]; put {
		t.Error("PutRole was called before its policy existed")
	}

	fa.policies["not-yet"] = `{}`
	reconcileStable(t, r, key, 5)
	if _, put := fa.roles["app"]; !put {
		t.Error("PutRole was not called once the policy existed")
	}
}

func TestS3Role_DeleteRespectsReclaimPolicy(t *testing.T) {
	for _, tc := range []struct {
		reclaim    seaweedv1.S3ReclaimPolicy
		wantExists bool
	}{
		{seaweedv1.S3ReclaimDelete, false},
		{seaweedv1.S3ReclaimRetain, true},
	} {
		t.Run(string(tc.reclaim), func(t *testing.T) {
			fa := newFakeIAMAdmin()
			role := testRole("app", func(r *seaweedv1.S3Role) { r.Spec.ReclaimPolicy = tc.reclaim })
			r, cli := newRoleReconciler(t, fa, readyTestProvider("spire", iamSeaweedRef()), role)
			key := types.NamespacedName{Namespace: "media", Name: "app"}
			reconcileStable(t, r, key, 5)

			if err := cli.Delete(context.Background(), getRole(t, cli, "app")); err != nil {
				t.Fatalf("delete: %v", err)
			}
			reconcileOnce(t, r, key)
			if _, exists := fa.roles["app"]; exists != tc.wantExists {
				t.Errorf("role exists after delete = %v, want %v", exists, tc.wantExists)
			}
		})
	}
}

// A role that never reached PutRole has nothing to delete, so its deletion
// must not call DeleteRole — the name may belong to someone else.
func TestS3Role_DeleteBeforeProvisioningLeavesTheNameAlone(t *testing.T) {
	fa := newFakeIAMAdmin()
	fa.roles["app"] = &swadmin.IAMRole{Name: "app"}
	r, cli := newRoleReconciler(t, fa, testRole("app")) // no provider: never provisioned
	key := types.NamespacedName{Namespace: "media", Name: "app"}
	reconcileOnce(t, r, key)
	reconcileOnce(t, r, key)

	if err := cli.Delete(context.Background(), getRole(t, cli, "app")); err != nil {
		t.Fatalf("delete: %v", err)
	}
	reconcileOnce(t, r, key)
	if _, exists := fa.roles["app"]; !exists {
		t.Error("deleting an unprovisioned S3Role deleted a role it never created")
	}
}

// A status write that fails after PutRole must not leave the role orphaned:
// deleting the CR before a successful retry still deletes the role.
func TestS3Role_StatusWriteFailureAfterPutStillDeletesTheRole(t *testing.T) {
	fa := newFakeIAMAdmin()
	scheme := iamTestScheme(t)
	failed := false
	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(append(defaultTestRefGrants(), newTestSeaweed(), readyTestProvider("spire", iamSeaweedRef()), testRole("app"))...).
		WithStatusSubresource(&seaweedv1.S3OIDCProvider{}, &seaweedv1.S3Role{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if _, put := fa.roles["app"]; put && !failed {
					failed = true
					return errors.New("injected status write failure")
				}
				return c.SubResource(sub).Update(ctx, obj, opts...)
			},
		}).
		Build()
	r := &S3RoleReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)
	key := types.NamespacedName{Namespace: "media", Name: "app"}

	reconcileOnce(t, r, key) // finalizer
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err == nil {
		t.Fatal("expected the injected status write failure")
	}
	if _, put := fa.roles["app"]; !put || !failed {
		t.Fatalf("setup: PutRole called=%v, failure injected=%v", put, failed)
	}

	if err := cli.Delete(context.Background(), getRole(t, cli, "app")); err != nil {
		t.Fatalf("delete: %v", err)
	}
	reconcileOnce(t, r, key)
	if _, exists := fa.roles["app"]; exists {
		t.Error("the role outlived its S3Role: deletion skipped DeleteRole after a failed status write")
	}
}

const bob = "spiffe://example.org/ns/app/sa/bob"

// provisionedRole provisions a role trusting testSVID with policy "base" and
// returns it ready to be re-specced.
func provisionedRole(t *testing.T, objs ...client.Object) (*fakeIAMAdmin, *S3RoleReconciler, client.Client, types.NamespacedName) {
	t.Helper()
	fa := newFakeIAMAdmin()
	fa.policies["base"] = `{}`
	role := testRole("app", func(r *seaweedv1.S3Role) { r.Spec.PolicyRefs = []seaweedv1.S3PolicyRef{{Name: "base"}} })
	r, cli := newRoleReconciler(t, fa, append([]client.Object{readyTestProvider("spire", iamSeaweedRef()), role}, objs...)...)
	key := types.NamespacedName{Namespace: "media", Name: "app"}
	reconcileStable(t, r, key, 5)
	if _, exists := fa.roles["app"]; !exists {
		t.Fatal("setup: role not provisioned")
	}
	return fa, r, cli, key
}

func respec(t *testing.T, cli client.Client, mutate func(*seaweedv1.S3Role)) {
	t.Helper()
	got := getRole(t, cli, "app")
	mutate(got)
	got.Generation++ // the API server bumps it on a spec change; the fake client does not
	if err := cli.Update(context.Background(), got); err != nil {
		t.Fatalf("update: %v", err)
	}
}

func wantPending(t *testing.T, cli client.Client, reason string) {
	t.Helper()
	if got := getRole(t, cli, "app"); got.Status.Phase != seaweedv1.S3PhasePending || readyReason(got) != reason {
		t.Errorf("phase=%s reason=%s, want Pending/%s", got.Status.Phase, readyReason(got), reason)
	}
}

// A spec change that cannot be applied in full withdraws the role until it
// can: applying part of it could widen access (a new subject without a policy
// that denies) or keep a removed grant (a dropped subject or policy). Once the
// missing policy exists the role is recreated from the whole spec.
func TestS3Role_AChangedSpecWithAMissingPolicyWithdrawsTheRole(t *testing.T) {
	for name, mutate := range map[string]func(*seaweedv1.S3Role){
		"subject swapped": func(r *seaweedv1.S3Role) {
			r.Spec.WebIdentity.Subjects = []string{bob}
			r.Spec.PolicyRefs = append(r.Spec.PolicyRefs, seaweedv1.S3PolicyRef{Name: "not-yet"})
		},
		"policy replaced": func(r *seaweedv1.S3Role) {
			r.Spec.PolicyRefs = []seaweedv1.S3PolicyRef{{Name: "not-yet"}}
		},
		"provider switched": func(r *seaweedv1.S3Role) {
			r.Spec.WebIdentity.ProviderRef.Name = "other"
			r.Spec.PolicyRefs = append(r.Spec.PolicyRefs, seaweedv1.S3PolicyRef{Name: "not-yet"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			other := readyTestProvider("other", iamSeaweedRef())
			other.Spec.IssuerURL = "https://other.example.org"
			fa, r, cli, key := provisionedRole(t, other)
			respec(t, cli, mutate)
			reconcileOnce(t, r, key)

			if _, exists := fa.roles["app"]; exists {
				t.Errorf("the role stayed live with a spec that could not be applied: %+v", fa.roles["app"])
			}
			wantPending(t, cli, "PolicyMissing")

			fa.policies["not-yet"] = `{}`
			reconcileStable(t, r, key, 5)
			want := getRole(t, cli, "app")
			live, exists := fa.roles["app"]
			if !exists {
				t.Fatal("the role was not recreated once its policies existed")
			}
			issuer := testIssuer
			if want.Spec.WebIdentity.ProviderRef.Name == "other" {
				issuer = other.Spec.IssuerURL
			}
			wantTrust, _ := buildWebIdentityTrustPolicy(issuer, want.Spec.WebIdentity.Subjects)
			if live.TrustPolicy != wantTrust || strings.Join(live.AttachedPolicies, ",") != strings.Join(want.Status.AttachedPolicies, ",") {
				t.Errorf("recreated role = %+v, want the whole new spec", live)
			}
			if want.Status.Phase != seaweedv1.S3PhaseReady {
				t.Errorf("phase = %s once every policy exists, want Ready", want.Status.Phase)
			}
		})
	}
}

// Switching to a provider that is not Ready yet must not leave the old issuer
// trusted: the role is withdrawn until the new provider is registered.
func TestS3Role_SwitchingToAProviderThatIsNotReadyWithdrawsTheRole(t *testing.T) {
	pending := readyTestProvider("other", iamSeaweedRef())
	pending.Spec.IssuerURL = "https://other.example.org"
	pending.Status = seaweedv1.S3OIDCProviderStatus{}
	fa, r, cli, key := provisionedRole(t, pending)
	respec(t, cli, func(r *seaweedv1.S3Role) { r.Spec.WebIdentity.ProviderRef.Name = "other" })
	reconcileOnce(t, r, key)

	if _, exists := fa.roles["app"]; exists {
		t.Errorf("the role still trusts the removed issuer: %+v", fa.roles["app"])
	}
	wantPending(t, cli, "ProviderNotReady")
}

// A dependency lost without a spec change leaves the role alone: it still
// matches its spec, and a provider or policy flapping must not delete it.
func TestS3Role_ADependencyLostWithoutASpecChangeLeavesTheRole(t *testing.T) {
	fa, r, cli, key := provisionedRole(t)
	var provider seaweedv1.S3OIDCProvider
	if err := cli.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: "spire"}, &provider); err != nil {
		t.Fatal(err)
	}
	provider.Status.Phase = seaweedv1.S3PhaseFailed
	if err := cli.Status().Update(context.Background(), &provider); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, key)

	if _, exists := fa.roles["app"]; !exists {
		t.Error("a provider going NotReady with no spec change deleted the role")
	}
	wantPending(t, cli, "ProviderNotReady")
}

// Deleting the owner deletes the role even when another S3Role claims its
// name: handing it over live would keep the owner's subjects and policies
// granted until the claimant applies its own spec, and forever if it never
// can. A claimant that can apply its spec recreates the role from it.
func TestS3Role_DeletingTheOwnerDeletesTheRoleEvenWithAClaimant(t *testing.T) {
	for name, tc := range map[string]struct {
		claimantPolicy string
		wantRecreated  bool
	}{
		"claimant cannot apply its spec": {"not-yet", false},
		"claimant can apply its spec":    {"base", true},
	} {
		t.Run(name, func(t *testing.T) {
			fa := newFakeIAMAdmin()
			fa.policies["base"] = `{}`
			owner := testRole("owner", func(r *seaweedv1.S3Role) {
				r.Spec.Name = "shared"
				r.Spec.PolicyRefs = []seaweedv1.S3PolicyRef{{Name: "base"}}
				r.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
			})
			claimant := testRole("claimant", func(r *seaweedv1.S3Role) {
				r.Spec.Name = "shared"
				r.Spec.WebIdentity.Subjects = []string{bob}
				r.Spec.PolicyRefs = []seaweedv1.S3PolicyRef{{Name: tc.claimantPolicy}}
				r.CreationTimestamp = metav1.NewTime(time.Now())
			})
			r, cli := newRoleReconciler(t, fa, readyTestProvider("spire", iamSeaweedRef()), owner, claimant)
			ownerKey := types.NamespacedName{Namespace: "media", Name: "owner"}
			claimantKey := types.NamespacedName{Namespace: "media", Name: "claimant"}
			reconcileStable(t, r, ownerKey, 5)
			reconcileOnce(t, r, claimantKey)
			reconcileOnce(t, r, claimantKey)
			if _, exists := fa.roles["shared"]; !exists {
				t.Fatal("setup: the owner's role was not provisioned")
			}

			if err := cli.Delete(context.Background(), getRole(t, cli, "owner")); err != nil {
				t.Fatalf("delete: %v", err)
			}
			reconcileOnce(t, r, ownerKey)
			if live, exists := fa.roles["shared"]; exists && strings.Contains(live.TrustPolicy, testSVID) {
				t.Fatalf("the deleted owner's subject is still trusted: %s", live.TrustPolicy)
			}

			reconcileOnce(t, r, claimantKey)
			reconcileOnce(t, r, claimantKey)
			live, exists := fa.roles["shared"]
			if exists != tc.wantRecreated {
				t.Fatalf("role exists after the claimant reconciled = %v, want %v", exists, tc.wantRecreated)
			}
			if exists && (strings.Contains(live.TrustPolicy, testSVID) || !strings.Contains(live.TrustPolicy, bob)) {
				t.Errorf("recreated trust = %s, want the claimant's subject only", live.TrustPolicy)
			}
		})
	}
}

// An older S3Role setting spec.name onto a provisioned role's name conflicts
// instead of displacing the owner, so the owner can still narrow its role.
func TestS3Role_AnOlderClaimantCannotDisplaceTheProvisionedOwner(t *testing.T) {
	fa := newFakeIAMAdmin()
	owner := testRole("owner", func(r *seaweedv1.S3Role) {
		r.Spec.Name = "shared"
		r.Spec.WebIdentity.Subjects = []string{testSVID, bob}
		r.CreationTimestamp = metav1.NewTime(time.Now())
	})
	older := testRole("older", func(r *seaweedv1.S3Role) { r.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour)) })
	r, cli := newRoleReconciler(t, fa, readyTestProvider("spire", iamSeaweedRef()), owner, older)
	ownerKey := types.NamespacedName{Namespace: "media", Name: "owner"}
	olderKey := types.NamespacedName{Namespace: "media", Name: "older"}
	reconcileStable(t, r, ownerKey, 5)

	got := getRole(t, cli, "older")
	got.Spec.Name = "shared"
	if err := cli.Update(context.Background(), got); err != nil {
		t.Fatalf("update: %v", err)
	}
	reconcileOnce(t, r, olderKey)
	reconcileOnce(t, r, olderKey)
	if got := getRole(t, cli, "older"); readyReason(got) != "Conflict" {
		t.Errorf("older claimant reason = %s, want Conflict", readyReason(got))
	}

	got = getRole(t, cli, "owner")
	got.Spec.WebIdentity.Subjects = []string{testSVID}
	got.Generation++
	if err := cli.Update(context.Background(), got); err != nil {
		t.Fatalf("update: %v", err)
	}
	reconcileStable(t, r, ownerKey, 5)
	if strings.Contains(fa.roles["shared"].TrustPolicy, bob) {
		t.Errorf("the owner could not revoke a subject after an older CR claimed its name: %s", fa.roles["shared"].TrustPolicy)
	}
}

// A refused rename is a spec change that cannot be applied, so the role it
// provisioned is withdrawn: a subject removed in the same edit must not stay
// live.
func TestS3Role_ARefusedRenameWithdrawsTheRole(t *testing.T) {
	fa, r, cli, key := provisionedRole(t)
	respec(t, cli, func(r *seaweedv1.S3Role) {
		r.Spec.Name = "renamed"
		r.Spec.WebIdentity.Subjects = []string{bob}
	})
	reconcileOnce(t, r, key)

	if _, exists := fa.roles["app"]; exists {
		t.Errorf("the role stayed live with a refused rename: %+v", fa.roles["app"])
	}
	if _, exists := fa.roles["renamed"]; exists {
		t.Error("a refused rename created a role under the new name")
	}
	if got := getRole(t, cli, "app"); got.Status.Phase != seaweedv1.S3PhaseFailed || readyReason(got) != "RoleRenameNotSupported" {
		t.Errorf("phase=%s reason=%s, want Failed/RoleRenameNotSupported", got.Status.Phase, readyReason(got))
	}
}

// A spec change while the cross-namespace grant is missing cannot be applied,
// so the role is withdrawn rather than left trusting a removed subject.
func TestS3Role_AChangedSpecWithoutItsReferenceGrantWithdrawsTheRole(t *testing.T) {
	fa, r, cli, key := provisionedRole(t)
	var grant seaweedv1.ResourceReferenceGrant
	if err := cli.Get(context.Background(), types.NamespacedName{Namespace: "seaweedfs", Name: "test-allow-media"}, &grant); err != nil {
		t.Fatal(err)
	}
	if err := cli.Delete(context.Background(), &grant); err != nil {
		t.Fatal(err)
	}
	respec(t, cli, func(r *seaweedv1.S3Role) { r.Spec.WebIdentity.Subjects = []string{bob} })
	reconcileOnce(t, r, key)

	if _, exists := fa.roles["app"]; exists {
		t.Errorf("the role stayed live with a spec that could not be applied: %+v", fa.roles["app"])
	}
}

// The owner keeps its name until its cleanup ends. If DeleteRole fails, a
// claimant must not create its role in the meantime, or the owner's retry
// would delete that replacement.
func TestS3Role_AnOwnerBeingDeletedHoldsItsNameUntilItsCleanupEnds(t *testing.T) {
	fa := newFakeIAMAdmin()
	owner := testRole("owner", func(r *seaweedv1.S3Role) {
		r.Spec.Name = "shared"
		r.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
	})
	claimant := testRole("claimant", func(r *seaweedv1.S3Role) {
		r.Spec.Name = "shared"
		r.Spec.WebIdentity.Subjects = []string{bob}
		r.CreationTimestamp = metav1.NewTime(time.Now())
	})
	r, cli := newRoleReconciler(t, fa, readyTestProvider("spire", iamSeaweedRef()), owner, claimant)
	ownerKey := types.NamespacedName{Namespace: "media", Name: "owner"}
	claimantKey := types.NamespacedName{Namespace: "media", Name: "claimant"}
	reconcileStable(t, r, ownerKey, 5)

	fa.deleteRoleErr = errors.New("filer unavailable")
	if err := cli.Delete(context.Background(), getRole(t, cli, "owner")); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: ownerKey}); err == nil {
		t.Fatal("setup: expected the owner's DeleteRole to fail")
	}
	reconcileOnce(t, r, claimantKey)
	reconcileOnce(t, r, claimantKey)
	if readyReason(getRole(t, cli, "claimant")) != "Conflict" {
		t.Errorf("claimant reason = %s while the owner's cleanup is pending, want Conflict", readyReason(getRole(t, cli, "claimant")))
	}
	if live := fa.roles["shared"]; live != nil && strings.Contains(live.TrustPolicy, bob) {
		t.Fatal("the claimant created its role while the owner's cleanup was pending")
	}

	fa.deleteRoleErr = nil
	reconcileOnce(t, r, ownerKey)
	reconcileStable(t, r, claimantKey, 5)
	live, exists := fa.roles["shared"]
	if !exists || !strings.Contains(live.TrustPolicy, bob) || strings.Contains(live.TrustPolicy, testSVID) {
		t.Errorf("after the owner's cleanup, role = %+v, want the claimant's", live)
	}
}

// A CR whose rename was refused still owns the role it provisioned, so a
// claimant of that name conflicts instead of creating a role both would write.
func TestS3Role_AnOwnerWithARefusedRenameStillHoldsItsName(t *testing.T) {
	fa, r, cli, key := provisionedRole(t)
	respec(t, cli, func(r *seaweedv1.S3Role) { r.Spec.Name = "renamed" })
	reconcileOnce(t, r, key)

	claimant := testRole("claimant", func(r *seaweedv1.S3Role) {
		r.Spec.Name = "app"
		r.Spec.WebIdentity.Subjects = []string{bob}
	})
	if err := cli.Create(context.Background(), claimant); err != nil {
		t.Fatal(err)
	}
	claimantKey := types.NamespacedName{Namespace: "media", Name: "claimant"}
	reconcileOnce(t, r, claimantKey)
	reconcileOnce(t, r, claimantKey)
	if readyReason(getRole(t, cli, "claimant")) != "Conflict" {
		t.Errorf("claimant reason = %s, want Conflict with the owner of the refused rename", readyReason(getRole(t, cli, "claimant")))
	}
	if _, exists := fa.roles["app"]; exists {
		t.Error("the claimant created the role the owner still holds")
	}
}

// A rename onto a name another CR holds conflicts, and is still a change that
// cannot be applied: the role this CR provisioned is withdrawn, not left with
// a subject the same edit removed.
func TestS3Role_ARenameOntoAHeldNameWithdrawsTheRole(t *testing.T) {
	other := testRole("other", func(r *seaweedv1.S3Role) { r.Spec.WebIdentity.Subjects = []string{bob} })
	fa, r, cli, key := provisionedRole(t, other)
	otherKey := types.NamespacedName{Namespace: "media", Name: "other"}
	reconcileStable(t, r, otherKey, 5)

	respec(t, cli, func(r *seaweedv1.S3Role) {
		r.Spec.Name = "other"
		r.Spec.WebIdentity.Subjects = []string{bob}
	})
	reconcileOnce(t, r, key)

	if readyReason(getRole(t, cli, "app")) != "Conflict" {
		t.Errorf("reason = %s, want Conflict", readyReason(getRole(t, cli, "app")))
	}
	if _, exists := fa.roles["app"]; exists {
		t.Errorf("the role kept its removed subject while the rename conflicted: %+v", fa.roles["app"])
	}
	if _, exists := fa.roles["other"]; !exists {
		t.Error("the conflicting rename touched the other CR's role")
	}
}

// A CR not permitted to reference the cluster can never provision, so it does
// not hold the name against a CR that is.
func TestS3Role_AForbiddenCRDoesNotHoldTheName(t *testing.T) {
	fa := newFakeIAMAdmin()
	rogue := testRole("rogue", func(r *seaweedv1.S3Role) {
		r.Namespace = "rogue" // no ResourceReferenceGrant admits this namespace
		r.Spec.Name = "shared"
		r.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
	})
	granted := testRole("granted", func(r *seaweedv1.S3Role) {
		r.Spec.Name = "shared"
		r.CreationTimestamp = metav1.NewTime(time.Now())
	})
	r, cli := newRoleReconciler(t, fa, readyTestProvider("spire", iamSeaweedRef()), rogue, granted)
	reconcileStable(t, r, types.NamespacedName{Namespace: "media", Name: "granted"}, 5)

	if got := getRole(t, cli, "granted"); got.Status.Phase != seaweedv1.S3PhaseReady {
		t.Errorf("granted CR phase=%s reason=%s, want Ready despite an older forbidden claimant", got.Status.Phase, readyReason(got))
	}
	if _, exists := fa.roles["shared"]; !exists {
		t.Error("the granted CR did not provision the name")
	}
}

// twoHolders returns an elder and a younger S3Role that both recorded the
// role name "shared" (a race on informer lag), with the live role carrying the
// younger's subject, as if it wrote last.
func twoHolders(t *testing.T) (*fakeIAMAdmin, *S3RoleReconciler, client.Client) {
	t.Helper()
	fa := newFakeIAMAdmin()
	youngerTrust, _ := buildWebIdentityTrustPolicy(testIssuer, []string{bob})
	fa.roles["shared"] = &swadmin.IAMRole{Name: "shared", TrustPolicy: youngerTrust}
	held := seaweedv1.S3RoleStatus{RoleName: "shared", RoleArn: "arn:aws:iam::role/shared", ObservedGeneration: 1}
	elder := testRole("elder", func(r *seaweedv1.S3Role) {
		r.Spec.Name = "shared"
		r.Generation = 1
		r.Status = held
		r.Finalizers = []string{s3RoleFinalizer}
		r.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
	})
	younger := testRole("younger", func(r *seaweedv1.S3Role) {
		r.Spec.Name = "shared"
		r.Spec.WebIdentity.Subjects = []string{bob}
		r.Generation = 1
		r.Status = held
		r.Finalizers = []string{s3RoleFinalizer}
		r.CreationTimestamp = metav1.NewTime(time.Now())
	})
	r, cli := newRoleReconciler(t, fa, readyTestProvider("spire", iamSeaweedRef()), elder, younger)
	return fa, r, cli
}

// A name two S3Roles both provisioned is disputed: the role is deleted and
// neither writes it while both exist, so neither CR's subjects stay granted
// under the other's claim.
func TestS3Role_ANameTwoCRsProvisionedIsDisputed(t *testing.T) {
	fa, r, cli := twoHolders(t)
	for _, n := range []string{"younger", "elder"} {
		reconcileOnce(t, r, types.NamespacedName{Namespace: "media", Name: n})
		if got := getRole(t, cli, n); readyReason(got) != "Disputed" {
			t.Errorf("%s reason = %s, want Disputed", n, readyReason(got))
		}
	}
	if _, exists := fa.roles["shared"]; exists {
		t.Errorf("a disputed role stayed live: %+v", fa.roles["shared"])
	}
	if strings.Contains(strings.Join(fa.calls, " "), "PutRole:shared") {
		t.Error("a disputing CR wrote the role")
	}
}

// Deleting one of two holders deletes the role like any owner's deletion; the
// other then recreates it from its own spec.
func TestS3Role_DeletingOneOfTwoHoldersLetsTheOtherRecreateTheRole(t *testing.T) {
	fa, r, cli := twoHolders(t)
	if err := cli.Delete(context.Background(), getRole(t, cli, "younger")); err != nil {
		t.Fatalf("delete: %v", err)
	}
	reconcileOnce(t, r, types.NamespacedName{Namespace: "media", Name: "younger"})
	if live, exists := fa.roles["shared"]; exists && strings.Contains(live.TrustPolicy, bob) {
		t.Fatalf("the deleted holder's subject is still trusted: %s", live.TrustPolicy)
	}

	reconcileStable(t, r, types.NamespacedName{Namespace: "media", Name: "elder"}, 5)
	live, exists := fa.roles["shared"]
	if !exists || strings.Contains(live.TrustPolicy, bob) || !strings.Contains(live.TrustPolicy, testSVID) {
		t.Errorf("after the other holder was deleted, role = %+v, want the elder's spec", live)
	}
}

func TestS3Role_NameConflictFailsTheLaterClaimant(t *testing.T) {
	fa := newFakeIAMAdmin()
	older := testRole("first", func(r *seaweedv1.S3Role) {
		r.Spec.Name = "shared"
		r.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
	})
	newer := testRole("second", func(r *seaweedv1.S3Role) {
		r.Spec.Name = "shared"
		r.CreationTimestamp = metav1.NewTime(time.Now())
	})
	r, cli := newRoleReconciler(t, fa, readyTestProvider("spire", iamSeaweedRef()), older, newer)
	reconcileOnce(t, r, types.NamespacedName{Namespace: "media", Name: "second"})

	got := getRole(t, cli, "second")
	if got.Status.Phase != seaweedv1.S3PhaseFailed || readyReason(got) != "Conflict" {
		t.Errorf("phase=%s reason=%s, want Failed/Conflict", got.Status.Phase, readyReason(got))
	}
}

func TestS3Role_RenameIsRefused(t *testing.T) {
	fa := newFakeIAMAdmin()
	role := testRole("app")
	r, cli := newRoleReconciler(t, fa, readyTestProvider("spire", iamSeaweedRef()), role)
	key := types.NamespacedName{Namespace: "media", Name: "app"}
	reconcileStable(t, r, key, 5)

	got := getRole(t, cli, "app")
	got.Spec.Name = "renamed"
	if err := cli.Update(context.Background(), got); err != nil {
		t.Fatalf("update: %v", err)
	}
	reconcileOnce(t, r, key)
	got = getRole(t, cli, "app")
	if readyReason(got) != "RoleRenameNotSupported" {
		t.Errorf("reason = %s, want RoleRenameNotSupported", readyReason(got))
	}
	if _, put := fa.roles["renamed"]; put {
		t.Error("a refused rename still created the new role")
	}
}

func TestBuildWebIdentityTrustPolicy(t *testing.T) {
	doc, err := buildWebIdentityTrustPolicy(testIssuer, []string{"b", "a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Statement []struct {
			Principal map[string]string
			Condition map[string]map[string][]string
		}
	}
	if err := json.Unmarshal([]byte(doc), &parsed); err != nil {
		t.Fatal(err)
	}
	st := parsed.Statement[0]
	if st.Principal["Federated"] != testIssuer {
		t.Errorf("Federated = %q; SeaweedFS matches an IAM-managed provider by issuer URL", st.Principal["Federated"])
	}
	if got := st.Condition["StringEquals"]["oidc:sub"]; strings.Join(got, ",") != "a,b" {
		t.Errorf("oidc:sub = %v, want sorted and deduplicated", got)
	}
	if _, err := buildWebIdentityTrustPolicy(testIssuer, nil); err == nil {
		t.Error("a trust policy without subjects must be refused")
	}
}

func TestMapIAMError_UnimplementedIsUnsupported(t *testing.T) {
	err := mapIAMError(status.Error(codes.Unimplemented, "unknown method PutRole"))
	if !errors.Is(err, ErrIAMUnsupported) {
		t.Errorf("Unimplemented mapped to %v, want ErrIAMUnsupported", err)
	}
}
