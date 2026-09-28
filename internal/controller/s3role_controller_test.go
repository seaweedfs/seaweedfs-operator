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
	logf "sigs.k8s.io/controller-runtime/pkg/log"

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
