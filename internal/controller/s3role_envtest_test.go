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
	"fmt"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func envtestRole(ns, name string) *seaweedv1.S3Role {
	return &seaweedv1.S3Role{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: seaweedv1.S3RoleSpec{
			SeaweedRef: seaweedv1.SeaweedReference{Name: "cluster-a"},
			WebIdentity: seaweedv1.S3RoleWebIdentity{
				ProviderRef: seaweedv1.S3OIDCProviderRef{Name: "spire"},
				Subjects:    []string{"spiffe://example.org/ns/app/sa/app"},
			},
		},
	}
}

// The API server, not the controller, refuses an S3Role the IAM service would
// reject, so a bad spec fails at kubectl apply.
func TestS3RoleSchemaRejectsInvalidSpecs(t *testing.T) {
	_, cli := mustEnvtest(t)
	ctx := context.Background()
	ns := newTestNamespace(t, ctx, cli, "s3role-schema")

	valid := envtestRole(ns, "valid")
	valid.Spec.MaxSessionDuration = 43200
	if err := cli.Create(ctx, valid); err != nil {
		t.Fatalf("a valid S3Role was rejected: %v", err)
	}
	if valid.Spec.ReclaimPolicy != seaweedv1.S3ReclaimDelete {
		t.Errorf("reclaimPolicy default = %q, want Delete", valid.Spec.ReclaimPolicy)
	}

	cases := []struct {
		name   string
		mutate func(*seaweedv1.S3Role)
	}{
		{"no subjects", func(r *seaweedv1.S3Role) { r.Spec.WebIdentity.Subjects = nil }},
		{"empty subject", func(r *seaweedv1.S3Role) { r.Spec.WebIdentity.Subjects = []string{""} }},
		{"no provider", func(r *seaweedv1.S3Role) { r.Spec.WebIdentity.ProviderRef.Name = "" }},
		{"session below AWS minimum", func(r *seaweedv1.S3Role) { r.Spec.MaxSessionDuration = 60 }},
		{"session above AWS maximum", func(r *seaweedv1.S3Role) { r.Spec.MaxSessionDuration = 43201 }},
		{"role name with a slash", func(r *seaweedv1.S3Role) { r.Spec.Name = "team/app" }},
		{"subject with a policy variable", func(r *seaweedv1.S3Role) {
			r.Spec.WebIdentity.Subjects = []string{"spiffe://example.org/ns/${aws:username}/sa/app"}
		}},
		{"too many policies", func(r *seaweedv1.S3Role) {
			for i := 0; i < 11; i++ {
				r.Spec.PolicyRefs = append(r.Spec.PolicyRefs, seaweedv1.S3PolicyRef{Name: fmt.Sprintf("p%d", i)})
			}
		}},
		{"too many subjects", func(r *seaweedv1.S3Role) {
			r.Spec.WebIdentity.Subjects = nil
			for i := 0; i < 101; i++ {
				r.Spec.WebIdentity.Subjects = append(r.Spec.WebIdentity.Subjects, fmt.Sprintf("spiffe://example.org/ns/app/sa/app-%d", i))
			}
		}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			role := envtestRole(ns, fmt.Sprintf("invalid-%d", i))
			tc.mutate(role)
			err := cli.Create(ctx, role)
			if err == nil {
				t.Fatal("expected the API server to reject the S3Role")
			}
			if !apierrors.IsInvalid(err) {
				t.Fatalf("expected an Invalid error, got %v", err)
			}
		})
	}
}

func TestS3RoleNameIsImmutableOnceSet(t *testing.T) {
	_, cli := mustEnvtest(t)
	ctx := context.Background()
	ns := newTestNamespace(t, ctx, cli, "s3role-name")

	role := envtestRole(ns, "app")
	role.Spec.Name = "checkout-runtime"
	if err := cli.Create(ctx, role); err != nil {
		t.Fatalf("create: %v", err)
	}
	role.Spec.Name = "renamed"
	if err := cli.Update(ctx, role); err == nil || !apierrors.IsInvalid(err) {
		t.Fatalf("expected the rename to be rejected as Invalid, got %v", err)
	}
}
