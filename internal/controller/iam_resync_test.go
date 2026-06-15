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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/internal/controller/swadmin"
)

// simulateFilerRestart drops all IAM state the fake holds, mimicking a filer
// whose ephemeral store is wiped on pod restart while the CRs stay Ready.
func (f *fakeIAMAdmin) simulateFilerRestart() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users = map[string]*swadmin.IAMUser{}
	f.policies = map[string]string{}
	f.providers = map[string]string{}
	f.secretKeys = map[string]string{}
}

// Success schedules a periodic resync, and re-running it re-creates a user the
// filer lost — no spec change.
func TestS3Identity_ResyncReprovisionsUserAfterFilerRestart(t *testing.T) {
	scheme := iamTestScheme(t)
	id := &seaweedv1.S3Identity{
		ObjectMeta: metav1.ObjectMeta{Name: "alice", Namespace: "media"},
		Spec:       seaweedv1.S3IdentitySpec{SeaweedRef: iamSeaweedRef()},
	}
	cli := iamTestClient(t, scheme, newTestSeaweed(), id)
	fa := newFakeIAMAdmin()
	r := &S3IdentityReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)

	key := types.NamespacedName{Namespace: "media", Name: "alice"}
	reconcileOnce(t, r, key) // adds finalizer
	res := reconcileOnce(t, r, key)
	if res.RequeueAfter != iamResyncInterval {
		t.Fatalf("success path RequeueAfter = %v, want periodic resync %v", res.RequeueAfter, iamResyncInterval)
	}
	if _, err := fa.GetUser(context.Background(), "alice"); err != nil {
		t.Fatalf("expected user alice created, got %v", err)
	}

	// Filer loses its IAM state; the CR still says Ready.
	fa.simulateFilerRestart()
	if _, err := fa.GetUser(context.Background(), "alice"); err == nil {
		t.Fatal("precondition: user should be gone after simulated restart")
	}

	// The next resync pass must re-create the user.
	reconcileStable(t, r, key, 5)
	if _, err := fa.GetUser(context.Background(), "alice"); err != nil {
		t.Fatalf("resync did not re-provision user alice: %v", err)
	}
}

// A resync re-registers a lost access key from the key still held in the Secret.
func TestS3Credentials_ResyncReregistersKeyAfterFilerRestart(t *testing.T) {
	scheme := iamTestScheme(t)
	cred := &seaweedv1.S3Credentials{
		ObjectMeta: metav1.ObjectMeta{Name: "alice-creds", Namespace: "media"},
		Spec: seaweedv1.S3CredentialsSpec{
			SeaweedRef:  iamSeaweedRef(),
			IdentityRef: seaweedv1.S3IdentityRef{Name: "alice"},
			SecretRef:   seaweedv1.S3SecretRef{Name: "alice-secret"},
		},
	}
	cli := iamTestClient(t, scheme, newTestSeaweed(), cred)
	fa := newFakeIAMAdmin()
	fa.seedUser("alice")
	r := &S3CredentialsReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)

	key := types.NamespacedName{Namespace: "media", Name: "alice-creds"}
	reconcileStable(t, r, key, 5)
	keys := fa.userKeys("alice")
	if len(keys) != 1 {
		t.Fatalf("expected 1 access key on alice, got %v", keys)
	}
	provisioned := keys[0]

	// Filer restart wipes the user and its keys; the identity controller would
	// re-create the user, so seed it back to isolate the credentials self-heal.
	fa.simulateFilerRestart()
	fa.seedUser("alice")
	if k := fa.userKeys("alice"); len(k) != 0 {
		t.Fatalf("precondition: keys should be gone after restart, got %v", k)
	}

	// Resync must re-register the same key the Secret still holds.
	reconcileStable(t, r, key, 5)
	keys = fa.userKeys("alice")
	if len(keys) != 1 || keys[0] != provisioned {
		t.Fatalf("resync did not re-register key %q, got %v", provisioned, keys)
	}

	// The Secret must be unchanged — no spurious rotation.
	var secret corev1.Secret
	if err := cli.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: "alice-secret"}, &secret); err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if string(secret.Data[defaultAccessKeyField]) != provisioned {
		t.Errorf("secret accessKey changed to %q, want stable %q", secret.Data[defaultAccessKeyField], provisioned)
	}
}

// A resync re-applies a lost policy document.
func TestS3Policy_ResyncReappliesPolicyAfterFilerRestart(t *testing.T) {
	scheme := iamTestScheme(t)
	pol := &seaweedv1.S3Policy{
		ObjectMeta: metav1.ObjectMeta{Name: "rw", Namespace: "media"},
		Spec: seaweedv1.S3PolicySpec{
			SeaweedRef: iamSeaweedRef(),
			Statements: []seaweedv1.S3PolicyStatement{{
				Effect:    seaweedv1.S3PolicyEffectAllow,
				Actions:   []string{"s3:GetObject"},
				Resources: []string{"my-bucket/*"},
			}},
		},
	}
	cli := iamTestClient(t, scheme, newTestSeaweed(), pol)
	fa := newFakeIAMAdmin()
	r := &S3PolicyReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)

	key := types.NamespacedName{Namespace: "media", Name: "rw"}
	reconcileStable(t, r, key, 5)
	if _, err := fa.GetPolicy(context.Background(), "rw"); err != nil {
		t.Fatalf("expected policy rw, got %v", err)
	}

	fa.simulateFilerRestart()
	if _, err := fa.GetPolicy(context.Background(), "rw"); err == nil {
		t.Fatal("precondition: policy should be gone after simulated restart")
	}

	reconcileStable(t, r, key, 5)
	if _, err := fa.GetPolicy(context.Background(), "rw"); err != nil {
		t.Fatalf("resync did not re-apply policy rw: %v", err)
	}
}
