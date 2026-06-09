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

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// TestResourceReferenceGrant_NamespaceXorSelector_EnforcedByApiserver pins that
// the from-entry CEL rule both compiles and enforces "exactly one of namespace
// or namespaceSelector" on a real apiserver. "namespace" is a CEL reserved word
// and must be escaped as "__namespace__"; an unescaped rule fails CRD
// installation outright — a failure the fake-client unit tests cannot catch.
func TestResourceReferenceGrant_NamespaceXorSelector_EnforcedByApiserver(t *testing.T) {
	_, cli := mustEnvtest(t)
	ctx := context.Background()

	ns := newTestNamespace(t, ctx, cli, "refgrant-cel")
	t.Cleanup(func() {
		_ = cli.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	})

	to := []seaweedv1.ReferenceGrantTo{{Group: groupSeaweed, Kind: kindSeaweed}}
	grant := func(name string, from seaweedv1.ReferenceGrantFrom) *seaweedv1.ResourceReferenceGrant {
		return &seaweedv1.ResourceReferenceGrant{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       seaweedv1.ResourceReferenceGrantSpec{From: []seaweedv1.ReferenceGrantFrom{from}, To: to},
		}
	}

	tests := []struct {
		name      string
		from      seaweedv1.ReferenceGrantFrom
		wantError bool
	}{
		{
			name: "namespace-only-accepted",
			from: seaweedv1.ReferenceGrantFrom{Group: groupSeaweed, Kind: kindBucket, Namespace: "app"},
		},
		{
			name: "selector-only-accepted",
			from: seaweedv1.ReferenceGrantFrom{Group: groupSeaweed, Kind: kindBucket, NamespaceSelector: &metav1.LabelSelector{}},
		},
		{
			name:      "both-set-rejected",
			from:      seaweedv1.ReferenceGrantFrom{Group: groupSeaweed, Kind: kindBucket, Namespace: "app", NamespaceSelector: &metav1.LabelSelector{}},
			wantError: true,
		},
		{
			name:      "neither-set-rejected",
			from:      seaweedv1.ReferenceGrantFrom{Group: groupSeaweed, Kind: kindBucket},
			wantError: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := cli.Create(ctx, grant("g-"+tc.name, tc.from))
			if tc.wantError {
				if err == nil {
					t.Fatal("apiserver accepted a grant the CEL rule should reject")
				}
				if !strings.Contains(err.Error(), "exactly one of namespace or namespaceSelector") {
					t.Errorf("expected the CEL validation message, got: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("apiserver rejected a valid grant: %v", err)
			}
		})
	}
}
