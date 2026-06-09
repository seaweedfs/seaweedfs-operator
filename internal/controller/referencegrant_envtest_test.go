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

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// TestResourceReferenceGrant_FromOneOfCEL exercises the spec.from[] one-of CEL
// rule against a real apiserver. It pins that exactly one of namespace /
// namespaceSelector is accepted — and in particular guards the escaping of
// "namespace" (a CEL reserved word that must be written self.__namespace__),
// which a fake client cannot validate.
func TestResourceReferenceGrant_FromOneOfCEL(t *testing.T) {
	_, cli := mustEnvtest(t)
	ctx := context.Background()

	ns := newTestNamespace(t, ctx, cli, "refgrant-cel")
	t.Cleanup(func() {
		_ = cli.Delete(context.Background(), &seaweedv1.ResourceReferenceGrant{ObjectMeta: metav1.ObjectMeta{Name: "g", Namespace: ns}})
	})

	to := []seaweedv1.ReferenceGrantTo{{Group: groupSeaweed, Kind: kindSeaweed}}
	grant := func(from seaweedv1.ReferenceGrantFrom) *seaweedv1.ResourceReferenceGrant {
		return &seaweedv1.ResourceReferenceGrant{
			ObjectMeta: metav1.ObjectMeta{Name: "g", Namespace: ns},
			Spec:       seaweedv1.ResourceReferenceGrantSpec{From: []seaweedv1.ReferenceGrantFrom{from}, To: to},
		}
	}

	tests := []struct {
		name       string
		from       seaweedv1.ReferenceGrantFrom
		wantReject bool
	}{
		{
			name: "namespace only is accepted",
			from: seaweedv1.ReferenceGrantFrom{Group: groupSeaweed, Kind: kindBucket, Namespace: "app"},
		},
		{
			name: "namespaceSelector only is accepted",
			from: seaweedv1.ReferenceGrantFrom{Group: groupSeaweed, Kind: kindBucket, NamespaceSelector: &metav1.LabelSelector{}},
		},
		{
			name:       "both set is rejected",
			from:       seaweedv1.ReferenceGrantFrom{Group: groupSeaweed, Kind: kindBucket, Namespace: "app", NamespaceSelector: &metav1.LabelSelector{}},
			wantReject: true,
		},
		{
			name:       "neither set is rejected",
			from:       seaweedv1.ReferenceGrantFrom{Group: groupSeaweed, Kind: kindBucket},
			wantReject: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := grant(tc.from)
			err := cli.Create(ctx, g)
			if tc.wantReject {
				if !apierrors.IsInvalid(err) {
					t.Fatalf("expected an Invalid (CEL) error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected the grant to be accepted, got %v", err)
			}
			if err := cli.Delete(ctx, g); err != nil {
				t.Fatalf("cleanup delete: %v", err)
			}
		})
	}
}
