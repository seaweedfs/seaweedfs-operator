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

// TestBucketLifecyclePolicy_CEL exercises the admission validations against a
// real apiserver: a rule must carry an action, an expiration block must set a
// concrete setting, rule ids are unique, and bucketRef is immutable.
func TestBucketLifecyclePolicy_CEL(t *testing.T) {
	_, cli := mustEnvtest(t)
	ctx := context.Background()
	ns := newTestNamespace(t, ctx, cli, "blp-cel")

	policy := func(name string, rules []seaweedv1.BucketLifecycleRule) *seaweedv1.BucketLifecyclePolicy {
		return &seaweedv1.BucketLifecyclePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: seaweedv1.BucketLifecyclePolicySpec{
				BucketRef: seaweedv1.BucketLifecycleRef{Name: "photos"},
				Rules:     rules,
			},
		}
	}

	tests := []struct {
		name       string
		rules      []seaweedv1.BucketLifecycleRule
		wantReject bool
	}{
		{
			name:  "expiration days is accepted",
			rules: []seaweedv1.BucketLifecycleRule{{ID: "a", Expiration: &seaweedv1.BucketLifecycleExpiration{Days: 30}}},
		},
		{
			name:       "rule without an action is rejected",
			rules:      []seaweedv1.BucketLifecycleRule{{ID: "a", Prefix: "logs/"}},
			wantReject: true,
		},
		{
			name:       "empty expiration is rejected",
			rules:      []seaweedv1.BucketLifecycleRule{{ID: "a", Expiration: &seaweedv1.BucketLifecycleExpiration{}}},
			wantReject: true,
		},
		{
			name: "duplicate rule ids are rejected",
			rules: []seaweedv1.BucketLifecycleRule{
				{ID: "dup", Expiration: &seaweedv1.BucketLifecycleExpiration{Days: 1}},
				{ID: "dup", Expiration: &seaweedv1.BucketLifecycleExpiration{Days: 2}},
			},
			wantReject: true,
		},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := policy("case", tc.rules)
			p.Name = "case-" + string(rune('a'+i))
			err := cli.Create(ctx, p)
			if tc.wantReject {
				if err == nil {
					t.Fatalf("expected rejection, but create succeeded")
				}
				if !apierrors.IsInvalid(err) {
					t.Fatalf("expected an Invalid error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected accept, got %v", err)
			}
			t.Cleanup(func() { _ = cli.Delete(context.Background(), p) })
		})
	}

	t.Run("bucketRef is immutable", func(t *testing.T) {
		p := policy("immutable", []seaweedv1.BucketLifecycleRule{{ID: "a", Expiration: &seaweedv1.BucketLifecycleExpiration{Days: 30}}})
		if err := cli.Create(ctx, p); err != nil {
			t.Fatalf("create: %v", err)
		}
		t.Cleanup(func() { _ = cli.Delete(context.Background(), p) })
		p.Spec.BucketRef.Name = "videos"
		if err := cli.Update(ctx, p); err == nil {
			t.Fatal("expected bucketRef update to be rejected")
		} else if !apierrors.IsInvalid(err) {
			t.Fatalf("expected an Invalid error, got %v", err)
		}
	})
}
