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
			ClusterRef: seaweedv1.BucketClusterRef{Name: "cluster-a"},
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
