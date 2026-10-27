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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// Finalizer cleanup uses the referenced cluster as the identity of the remote
// state it owns. If that reference could change, a missing replacement cluster
// could make the controller release its finalizer while state remains active on
// the original cluster.
func TestManagedResourceClusterReferencesAreImmutable(t *testing.T) {
	_, cli := mustEnvtest(t)
	ctx := context.Background()
	ns := newTestNamespace(t, ctx, cli, "cluster-ref-immutable")

	bucket := &seaweedv1.Bucket{
		ObjectMeta: metav1.ObjectMeta{Name: "photos", Namespace: ns},
		Spec: seaweedv1.BucketSpec{
			ClusterRef: &seaweedv1.BucketClusterRef{Name: "cluster-a"},
		},
	}
	identity := &seaweedv1.S3Identity{
		ObjectMeta: metav1.ObjectMeta{Name: "alice", Namespace: ns},
		Spec: seaweedv1.S3IdentitySpec{
			SeaweedRef: seaweedv1.SeaweedReference{Name: "cluster-a"},
		},
	}
	credentials := &seaweedv1.S3Credentials{
		ObjectMeta: metav1.ObjectMeta{Name: "alice-credentials", Namespace: ns},
		Spec: seaweedv1.S3CredentialsSpec{
			SeaweedRef:  seaweedv1.SeaweedReference{Name: "cluster-a"},
			IdentityRef: seaweedv1.S3IdentityRef{Name: "alice"},
		},
	}
	policy := &seaweedv1.S3Policy{
		ObjectMeta: metav1.ObjectMeta{Name: "read", Namespace: ns},
		Spec: seaweedv1.S3PolicySpec{
			SeaweedRef:     seaweedv1.SeaweedReference{Name: "cluster-a"},
			PolicyDocument: `{}`,
		},
	}
	binding := &seaweedv1.S3PolicyBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "read-alice", Namespace: ns},
		Spec: seaweedv1.S3PolicyBindingSpec{
			SeaweedRef: seaweedv1.SeaweedReference{Name: "cluster-a"},
			PolicyRef:  seaweedv1.S3PolicyRef{Name: "read"},
			Subjects: []seaweedv1.S3Subject{{
				Kind: seaweedv1.S3SubjectKindIdentity,
				Name: "alice",
			}},
		},
	}
	provider := &seaweedv1.S3OIDCProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "accounts", Namespace: ns},
		Spec: seaweedv1.S3OIDCProviderSpec{
			SeaweedRef: seaweedv1.SeaweedReference{Name: "cluster-a"},
			IssuerURL:  "https://accounts.example.com",
			ClientIDs:  []string{"client-a"},
		},
	}
	role := &seaweedv1.S3Role{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: ns},
		Spec: seaweedv1.S3RoleSpec{
			SeaweedRef: seaweedv1.SeaweedReference{Name: "cluster-a"},
			WebIdentity: seaweedv1.S3RoleWebIdentity{
				ProviderRef: seaweedv1.S3OIDCProviderRef{Name: "accounts"},
				Subjects:    []string{"spiffe://example.org/ns/app/sa/app"},
			},
		},
	}

	tests := []struct {
		name   string
		object client.Object
		mutate func()
	}{
		{"Bucket", bucket, func() { bucket.Spec.ClusterRef.Name = "cluster-b" }},
		{"S3Identity", identity, func() { identity.Spec.SeaweedRef.Name = "cluster-b" }},
		{"S3Credentials", credentials, func() { credentials.Spec.SeaweedRef.Name = "cluster-b" }},
		{"S3Policy", policy, func() { policy.Spec.SeaweedRef.Name = "cluster-b" }},
		{"S3PolicyBinding", binding, func() { binding.Spec.SeaweedRef.Name = "cluster-b" }},
		{"S3OIDCProvider", provider, func() { provider.Spec.SeaweedRef.Name = "cluster-b" }},
		{"S3Role", role, func() { role.Spec.SeaweedRef.Name = "cluster-b" }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := cli.Create(ctx, tc.object); err != nil {
				t.Fatalf("create: %v", err)
			}
			t.Cleanup(func() { _ = cli.Delete(context.Background(), tc.object) })

			tc.mutate()
			if err := cli.Update(ctx, tc.object); err == nil {
				t.Fatal("expected cluster reference update to be rejected")
			} else if !apierrors.IsInvalid(err) {
				t.Fatalf("expected an Invalid error, got %v", err)
			}
		})
	}
}

// Bucket accepts the reference under either name — clusterRef (the original
// field) or seaweedRef (the name the IAM CRDs use) — but never both, never
// neither, and never swapped after creation.
func TestBucketClusterRefAndSeaweedRefAreExclusive(t *testing.T) {
	_, cli := mustEnvtest(t)
	ctx := context.Background()
	ns := newTestNamespace(t, ctx, cli, "bucket-ref-exclusive")

	newBucket := func(name string, spec seaweedv1.BucketSpec) *seaweedv1.Bucket {
		return &seaweedv1.Bucket{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       spec,
		}
	}

	for _, tc := range []struct {
		name string
		spec seaweedv1.BucketSpec
	}{
		{"neither set", seaweedv1.BucketSpec{}},
		{"both set", seaweedv1.BucketSpec{
			ClusterRef: &seaweedv1.BucketClusterRef{Name: "cluster-a"},
			SeaweedRef: &seaweedv1.SeaweedReference{Name: "cluster-a"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := cli.Create(ctx, newBucket("bad-"+strings.ReplaceAll(tc.name, " ", "-"), tc.spec))
			if err == nil {
				t.Fatal("expected create to be rejected")
			} else if !apierrors.IsInvalid(err) {
				t.Fatalf("expected an Invalid error, got %v", err)
			}
		})
	}

	viaSeaweedRef := newBucket("via-seaweedref", seaweedv1.BucketSpec{
		SeaweedRef: &seaweedv1.SeaweedReference{Name: "cluster-a"},
	})
	if err := cli.Create(ctx, viaSeaweedRef); err != nil {
		t.Fatalf("create via seaweedRef: %v", err)
	}
	t.Cleanup(func() { _ = cli.Delete(context.Background(), viaSeaweedRef) })

	viaClusterRef := newBucket("via-clusterref", seaweedv1.BucketSpec{
		ClusterRef: &seaweedv1.BucketClusterRef{Name: "cluster-a"},
	})
	if err := cli.Create(ctx, viaClusterRef); err != nil {
		t.Fatalf("create via clusterRef: %v", err)
	}
	t.Cleanup(func() { _ = cli.Delete(context.Background(), viaClusterRef) })

	for _, tc := range []struct {
		name   string
		object *seaweedv1.Bucket
		mutate func()
	}{
		{"seaweedRef name", viaSeaweedRef, func() { viaSeaweedRef.Spec.SeaweedRef.Name = "cluster-b" }},
		{"seaweedRef dropped", viaSeaweedRef, func() { viaSeaweedRef.Spec.SeaweedRef = nil }},
		{"clusterRef dropped for seaweedRef", viaClusterRef, func() {
			viaClusterRef.Spec.ClusterRef = nil
			viaClusterRef.Spec.SeaweedRef = &seaweedv1.SeaweedReference{Name: "cluster-a"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orig := tc.object.DeepCopy()
			tc.mutate()
			err := cli.Update(ctx, tc.object)
			*tc.object = *orig
			if err == nil {
				t.Fatal("expected reference update to be rejected")
			} else if !apierrors.IsInvalid(err) {
				t.Fatalf("expected an Invalid error, got %v", err)
			}
		})
	}
}
