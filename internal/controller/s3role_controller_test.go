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

	"github.com/seaweedfs/seaweedfs/weed/iam/policy"
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

const (
	bob   = "spiffe://example.org/ns/app/sa/bob"
	carol = "spiffe://example.org/ns/app/sa/carol"
)

// provisionThenRespec provisions a role trusting subjects with policy "base",
// then changes its subjects and adds a policy that does not exist yet.
func provisionThenRespec(t *testing.T, subjects, respec []string) (*fakeIAMAdmin, *S3RoleReconciler, client.Client, types.NamespacedName) {
	t.Helper()
	fa := newFakeIAMAdmin()
	fa.policies["base"] = `{}`
	role := testRole("app", func(r *seaweedv1.S3Role) {
		r.Spec.WebIdentity.Subjects = subjects
		r.Spec.PolicyRefs = []seaweedv1.S3PolicyRef{{Name: "base"}}
		r.Spec.MaxSessionDuration = 3600
	})
	r, cli := newRoleReconciler(t, fa, readyTestProvider("spire", iamSeaweedRef()), role)
	key := types.NamespacedName{Namespace: "media", Name: "app"}
	reconcileStable(t, r, key, 5)

	got := getRole(t, cli, "app")
	got.Spec.WebIdentity.Subjects = respec
	got.Spec.PolicyRefs = []seaweedv1.S3PolicyRef{{Name: "base"}, {Name: "not-yet"}}
	got.Spec.MaxSessionDuration = 43200
	if err := cli.Update(context.Background(), got); err != nil {
		t.Fatalf("update: %v", err)
	}
	return fa, r, cli, key
}

func wantPolicyMissing(t *testing.T, cli client.Client) {
	t.Helper()
	if got := getRole(t, cli, "app"); got.Status.Phase != seaweedv1.S3PhasePending || readyReason(got) != "PolicyMissing" {
		t.Errorf("phase=%s reason=%s, want Pending/PolicyMissing", got.Status.Phase, readyReason(got))
	}
}

// While a newly referenced policy does not exist yet, a removed subject is
// revoked at once, but nothing is added: a new subject, the new policy (which
// may deny) and the longer session all wait, so the live role never admits
// more than it did.
func TestS3Role_RevokesARemovedSubjectButGrantsNothingWhileAPolicyIsMissing(t *testing.T) {
	fa, r, cli, key := provisionThenRespec(t, []string{testSVID, carol}, []string{carol, bob})
	reconcileOnce(t, r, key)

	live := fa.roles["app"]
	issuer, trusted, ok := parseWebIdentityTrustPolicy(live.TrustPolicy)
	if !ok || issuer != testIssuer || strings.Join(trusted, ",") != carol {
		t.Errorf("trust = %s, want only %s (the removed subject revoked, the new one not added)", live.TrustPolicy, carol)
	}
	if strings.Join(live.AttachedPolicies, ",") != "base" || live.MaxSessionDuration != 3600 {
		t.Errorf("attached=%v maxSession=%d, want the live role's base/3600 unchanged", live.AttachedPolicies, live.MaxSessionDuration)
	}
	wantPolicyMissing(t, cli)

	fa.policies["not-yet"] = `{}`
	reconcileStable(t, r, key, 5)
	live = fa.roles["app"]
	if _, trusted, _ := parseWebIdentityTrustPolicy(live.TrustPolicy); strings.Join(trusted, ",") != bob+","+carol {
		t.Errorf("trust = %s once the policy exists, want bob and carol", live.TrustPolicy)
	}
	if strings.Join(live.AttachedPolicies, ",") != "base,not-yet" || live.MaxSessionDuration != 43200 {
		t.Errorf("attached=%v maxSession=%d once the policy exists, want base,not-yet/43200", live.AttachedPolicies, live.MaxSessionDuration)
	}
	if got := getRole(t, cli, "app"); got.Status.Phase != seaweedv1.S3PhaseReady {
		t.Errorf("phase = %s once every policy exists, want Ready", got.Status.Phase)
	}
}

// With no subject removed there is nothing to revoke, so the live role is not
// written at all until the missing policy exists.
func TestS3Role_LeavesTheRoleAloneWhenNothingIsRevokedAndAPolicyIsMissing(t *testing.T) {
	fa, r, cli, key := provisionThenRespec(t, []string{testSVID}, []string{testSVID, bob})
	puts := strings.Count(strings.Join(fa.calls, " "), "PutRole:")
	reconcileOnce(t, r, key)

	if got := strings.Count(strings.Join(fa.calls, " "), "PutRole:"); got != puts {
		t.Errorf("PutRole called %d more time(s) with nothing to revoke", got-puts)
	}
	wantPolicyMissing(t, cli)
}

// When every subject the role trusts is removed, no trust policy can express
// the narrowed role, so it is deleted until the missing policy exists, then
// recreated from the spec.
func TestS3Role_DeletesTheRoleWhileAPolicyIsMissingIfEverySubjectIsRemoved(t *testing.T) {
	fa, r, cli, key := provisionThenRespec(t, []string{testSVID}, []string{bob})
	reconcileOnce(t, r, key)

	if _, exists := fa.roles["app"]; exists {
		t.Error("the role still trusts its only, removed, subject")
	}
	wantPolicyMissing(t, cli)

	fa.policies["not-yet"] = `{}`
	reconcileStable(t, r, key, 5)
	live, exists := fa.roles["app"]
	if !exists {
		t.Fatal("the role was not recreated once its policies existed")
	}
	if _, trusted, _ := parseWebIdentityTrustPolicy(live.TrustPolicy); strings.Join(trusted, ",") != bob {
		t.Errorf("recreated trust = %s, want only bob", live.TrustPolicy)
	}
}

// The filer stores a role's trust policy as a policy.PolicyDocument and
// GetRole re-encodes that, so what the operator reads back is not the string
// it wrote. Round-trip through SeaweedFS's own type and parse the result.
func TestParseWebIdentityTrustPolicy_AsSeaweedFSReturnsIt(t *testing.T) {
	for _, subjects := range [][]string{{bob}, {bob, carol}} {
		written, err := buildWebIdentityTrustPolicy(testIssuer, subjects)
		if err != nil {
			t.Fatal(err)
		}
		var stored policy.PolicyDocument
		if err := json.Unmarshal([]byte(written), &stored); err != nil {
			t.Fatalf("SeaweedFS cannot decode the trust policy the operator writes: %v", err)
		}
		returned, err := json.Marshal(&stored)
		if err != nil {
			t.Fatal(err)
		}
		issuer, got, ok := parseWebIdentityTrustPolicy(string(returned))
		if !ok || issuer != testIssuer || strings.Join(got, ",") != strings.Join(subjects, ",") {
			t.Errorf("parse(%s) = %q %v %v, want %q %v", returned, issuer, got, ok, testIssuer, subjects)
		}
	}
}

func TestParseWebIdentityTrustPolicy(t *testing.T) {
	written, err := buildWebIdentityTrustPolicy(testIssuer, []string{bob, carol})
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		doc  string
		want bool
	}{
		"as written": {written, true},
		// SeaweedFS re-encodes the stored document: Resource null, one-item lists as bare strings.
		"as stored":        {`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Federated":"` + testIssuer + `"},"Action":"sts:AssumeRoleWithWebIdentity","Resource":null,"Condition":{"StringEquals":{"oidc:sub":"` + bob + `"}}}]}`, true},
		"deny":             {strings.Replace(written, `"Allow"`, `"Deny"`, 1), false},
		"second statement": {strings.Replace(written, `}],"Version"`, `},{"Effect":"Allow","Principal":{"Federated":"x"},"Action":["sts:AssumeRoleWithWebIdentity"]}],"Version"`, 1), false},
		"extra condition":  {strings.Replace(written, `"StringEquals":`, `"StringLike":{"oidc:aud":"*"},"StringEquals":`, 1), false},
		"other action":     {strings.Replace(written, `sts:AssumeRoleWithWebIdentity`, `sts:AssumeRole`, 1), false},
		"not json":         {`{`, false},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, ok := parseWebIdentityTrustPolicy(tc.doc); ok != tc.want {
				t.Errorf("ok = %v, want %v for %s", ok, tc.want, tc.doc)
			}
		})
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
