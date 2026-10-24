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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// TestBucketNotification_CEL exercises the admission validations against a
// real apiserver: exactly one destination, non-empty secret references, and
// unique rule names within a notification.
func TestBucketNotification_CEL(t *testing.T) {
	_, cli := mustEnvtest(t)
	ctx := context.Background()
	ns := newTestNamespace(t, ctx, cli, "bn-cel")

	notification := func(name string, rules []seaweedv1.BucketNotificationRule) *seaweedv1.BucketNotification {
		return &seaweedv1.BucketNotification{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: seaweedv1.BucketNotificationSpec{
				BucketRef: seaweedv1.BucketNotificationBucketRef{Name: "photos"},
				Rules:     rules,
			},
		}
	}
	kafkaRule := func(name string) seaweedv1.BucketNotificationRule {
		return seaweedv1.BucketNotificationRule{
			Name: name,
			Destination: seaweedv1.BucketNotificationDestination{
				Kafka: &seaweedv1.BucketNotificationKafkaDestination{
					Brokers: []string{"kafka:9092"},
					Topic:   "events",
				},
			},
		}
	}

	tests := []struct {
		name       string
		rules      []seaweedv1.BucketNotificationRule
		wantReject bool
	}{
		{
			name:  "kafka destination is accepted",
			rules: []seaweedv1.BucketNotificationRule{kafkaRule("a")},
		},
		{
			name: "empty destination is rejected",
			rules: []seaweedv1.BucketNotificationRule{{
				Name:        "a",
				Destination: seaweedv1.BucketNotificationDestination{},
			}},
			wantReject: true,
		},
		{
			name: "two destinations are rejected",
			rules: []seaweedv1.BucketNotificationRule{{
				Name: "a",
				Destination: seaweedv1.BucketNotificationDestination{
					Kafka:   &seaweedv1.BucketNotificationKafkaDestination{Brokers: []string{"kafka:9092"}, Topic: "events"},
					Webhook: &seaweedv1.BucketNotificationWebhookDestination{Endpoint: "https://example.com/hook"},
				},
			}},
			wantReject: true,
		},
		{
			name: "empty credentials secret name is rejected",
			rules: []seaweedv1.BucketNotificationRule{func() seaweedv1.BucketNotificationRule {
				r := kafkaRule("a")
				r.Destination.Kafka.CredentialsSecretRef = &corev1.LocalObjectReference{}
				return r
			}()},
			wantReject: true,
		},
		{
			name: "empty bearer token secret name is rejected",
			rules: []seaweedv1.BucketNotificationRule{{
				Name: "a",
				Destination: seaweedv1.BucketNotificationDestination{
					Webhook: &seaweedv1.BucketNotificationWebhookDestination{
						Endpoint:             "https://example.com/hook",
						BearerTokenSecretRef: &corev1.SecretKeySelector{Key: "token"},
					},
				},
			}},
			wantReject: true,
		},
		{
			name: "duplicate rule names are rejected",
			rules: []seaweedv1.BucketNotificationRule{
				kafkaRule("dup"),
				kafkaRule("dup"),
			},
			wantReject: true,
		},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n := notification("case", tc.rules)
			n.Name = "case-" + string(rune('a'+i))
			err := cli.Create(ctx, n)
			if tc.wantReject {
				if err == nil {
					t.Fatal("expected rejection, but create succeeded")
				}
				if !apierrors.IsInvalid(err) {
					t.Fatalf("expected an Invalid error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected accept, got %v", err)
			}
			t.Cleanup(func() { _ = cli.Delete(context.Background(), n) })
		})
	}
}
