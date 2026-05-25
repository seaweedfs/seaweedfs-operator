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

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func iamTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("clientgoscheme: %v", err)
	}
	if err := seaweedv1.AddToScheme(scheme); err != nil {
		t.Fatalf("seaweedv1: %v", err)
	}
	return scheme
}

func iamTestClient(t *testing.T, scheme *runtime.Scheme, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(
			&seaweedv1.S3Identity{},
			&seaweedv1.S3Credentials{},
			&seaweedv1.S3Policy{},
			&seaweedv1.S3PolicyBinding{},
		).
		Build()
}

func iamSeaweedRef() seaweedv1.SeaweedReference {
	return seaweedv1.SeaweedReference{Name: "prod", Namespace: "seaweedfs"}
}

// reconcileStable hammers Reconcile until the result is neither Requeue nor a
// timed requeue, mirroring reconcileUntilStable for the generic Reconciler
// interface.
func reconcileStable(t *testing.T, r reconcile.Reconciler, key types.NamespacedName, maxSteps int) {
	t.Helper()
	for i := 0; i < maxSteps; i++ {
		res, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
		if err != nil {
			t.Fatalf("reconcile step %d: %v", i, err)
		}
		if !res.Requeue && res.RequeueAfter == 0 {
			return
		}
	}
	t.Fatalf("reconcile did not converge after %d steps", maxSteps)
}

func reconcileOnce(t *testing.T, r reconcile.Reconciler, key types.NamespacedName) reconcile.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	return res
}

func fakeIAMFactory(fa IAMAdmin) IAMAdminFactory {
	return func(_ string, _ logr.Logger) (IAMAdmin, error) { return fa, nil }
}

// --- S3Identity ---

func TestS3Identity_CreatesUser(t *testing.T) {
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
	reconcileStable(t, r, key, 5)

	if _, err := fa.GetUser(context.Background(), "alice"); err != nil {
		t.Fatalf("expected user alice created, got %v", err)
	}
	var got seaweedv1.S3Identity
	if err := cli.Get(context.Background(), key, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Phase != seaweedv1.S3PhaseReady {
		t.Errorf("phase = %q, want Ready", got.Status.Phase)
	}
	if got.Status.IdentityName != "alice" {
		t.Errorf("identityName = %q, want alice", got.Status.IdentityName)
	}
}

func TestS3Identity_UpdatesDisabledState(t *testing.T) {
	scheme := iamTestScheme(t)
	id := &seaweedv1.S3Identity{
		ObjectMeta: metav1.ObjectMeta{Name: "alice", Namespace: "media"},
		Spec:       seaweedv1.S3IdentitySpec{SeaweedRef: iamSeaweedRef(), Disabled: true},
	}
	cli := iamTestClient(t, scheme, newTestSeaweed(), id)
	fa := newFakeIAMAdmin()
	fa.seedUser("alice") // already exists, enabled
	r := &S3IdentityReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)

	key := types.NamespacedName{Namespace: "media", Name: "alice"}
	reconcileStable(t, r, key, 5)

	u, err := fa.GetUser(context.Background(), "alice")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if !u.Disabled {
		t.Errorf("expected user to be disabled after reconcile")
	}
}

func TestS3Identity_Delete_RespectsReclaimPolicy(t *testing.T) {
	for _, tc := range []struct {
		name        string
		policy      seaweedv1.S3ReclaimPolicy
		wantDeleted bool
	}{
		{"delete", seaweedv1.S3ReclaimDelete, true},
		{"retain", seaweedv1.S3ReclaimRetain, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := iamTestScheme(t)
			id := &seaweedv1.S3Identity{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "alice",
					Namespace:  "media",
					Finalizers: []string{s3IdentityFinalizer},
				},
				Spec: seaweedv1.S3IdentitySpec{SeaweedRef: iamSeaweedRef(), ReclaimPolicy: tc.policy},
			}
			cli := iamTestClient(t, scheme, newTestSeaweed(), id)
			fa := newFakeIAMAdmin()
			fa.seedUser("alice")
			r := &S3IdentityReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
			r.AdminFactory = fakeIAMFactory(fa)

			key := types.NamespacedName{Namespace: "media", Name: "alice"}
			if err := cli.Delete(context.Background(), id); err != nil {
				t.Fatalf("delete: %v", err)
			}
			reconcileOnce(t, r, key)

			_, err := fa.GetUser(context.Background(), "alice")
			deleted := err != nil
			if deleted != tc.wantDeleted {
				t.Errorf("user deleted = %v, want %v", deleted, tc.wantDeleted)
			}
		})
	}
}

// --- S3Policy ---

func TestS3Policy_PutsPolicyDocument(t *testing.T) {
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

	doc, err := fa.GetPolicy(context.Background(), "rw")
	if err != nil {
		t.Fatalf("expected policy rw, got %v", err)
	}
	if doc == "" {
		t.Fatal("policy document is empty")
	}
	var got seaweedv1.S3Policy
	if err := cli.Get(context.Background(), key, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.PolicyName != "rw" || got.Status.Phase != seaweedv1.S3PhaseReady {
		t.Errorf("status = %+v", got.Status)
	}
}

// --- S3Credentials ---

func TestS3Credentials_GeneratesAndStores(t *testing.T) {
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

	var secret corev1.Secret
	if err := cli.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: "alice-secret"}, &secret); err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if string(secret.Data[defaultAccessKeyField]) != keys[0] {
		t.Errorf("secret accessKey = %q, want %q", secret.Data[defaultAccessKeyField], keys[0])
	}
	if len(secret.Data[defaultSecretKeyField]) == 0 {
		t.Error("secret secretKey is empty")
	}
	if secret.Annotations[s3CredentialsManagedAnnotation] != "true" {
		t.Error("operator-created secret should carry the managed annotation")
	}

	var got seaweedv1.S3Credentials
	if err := cli.Get(context.Background(), key, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.AccessKey != keys[0] || got.Status.SecretName != "alice-secret" {
		t.Errorf("status = %+v", got.Status)
	}

	// Re-reconcile must be idempotent: no second key gets created.
	reconcileStable(t, r, key, 5)
	if k := fa.userKeys("alice"); len(k) != 1 {
		t.Fatalf("expected reconcile to be idempotent, keys = %v", k)
	}
}

func TestS3Credentials_AdoptsExistingSecret(t *testing.T) {
	scheme := iamTestScheme(t)
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "alice-secret", Namespace: "media"},
		Data: map[string][]byte{
			defaultAccessKeyField: []byte("AKIAADOPTED"),
			defaultSecretKeyField: []byte("supersecret"),
		},
	}
	cred := &seaweedv1.S3Credentials{
		ObjectMeta: metav1.ObjectMeta{Name: "alice-creds", Namespace: "media"},
		Spec: seaweedv1.S3CredentialsSpec{
			SeaweedRef:  iamSeaweedRef(),
			IdentityRef: seaweedv1.S3IdentityRef{Name: "alice"},
			SecretRef:   seaweedv1.S3SecretRef{Name: "alice-secret"},
		},
	}
	cli := iamTestClient(t, scheme, newTestSeaweed(), existing, cred)
	fa := newFakeIAMAdmin()
	fa.seedUser("alice")
	r := &S3CredentialsReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)

	key := types.NamespacedName{Namespace: "media", Name: "alice-creds"}
	reconcileStable(t, r, key, 5)

	if keys := fa.userKeys("alice"); len(keys) != 1 || keys[0] != "AKIAADOPTED" {
		t.Fatalf("expected adopted key AKIAADOPTED, got %v", keys)
	}
	if fa.secretKeys["AKIAADOPTED"] != "supersecret" {
		t.Errorf("adopted secret key mismatch: %q", fa.secretKeys["AKIAADOPTED"])
	}
	var secret corev1.Secret
	if err := cli.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: "alice-secret"}, &secret); err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if secret.Annotations[s3CredentialsManagedAnnotation] == "true" {
		t.Error("pre-existing secret must not be marked managed")
	}
}

func TestS3Credentials_WaitsForIdentity(t *testing.T) {
	scheme := iamTestScheme(t)
	cred := &seaweedv1.S3Credentials{
		ObjectMeta: metav1.ObjectMeta{Name: "alice-creds", Namespace: "media"},
		Spec: seaweedv1.S3CredentialsSpec{
			SeaweedRef:  iamSeaweedRef(),
			IdentityRef: seaweedv1.S3IdentityRef{Name: "ghost"},
			SecretRef:   seaweedv1.S3SecretRef{Name: "alice-secret"},
		},
	}
	cli := iamTestClient(t, scheme, newTestSeaweed(), cred)
	fa := newFakeIAMAdmin()
	r := &S3CredentialsReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)

	key := types.NamespacedName{Namespace: "media", Name: "alice-creds"}
	reconcileOnce(t, r, key) // adds finalizer
	res := reconcileOnce(t, r, key)
	if res.RequeueAfter == 0 {
		t.Fatal("expected requeue while waiting for identity")
	}

	var secret corev1.Secret
	err := cli.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: "alice-secret"}, &secret)
	if !apierrors.IsNotFound(err) {
		t.Errorf("no secret should be created while identity is missing, got err=%v", err)
	}
	var got seaweedv1.S3Credentials
	if err := cli.Get(context.Background(), key, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Phase != seaweedv1.S3PhasePending {
		t.Errorf("phase = %q, want Pending", got.Status.Phase)
	}
}

func TestS3Credentials_Delete_RemovesKeyAndManagedSecret(t *testing.T) {
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
	if len(fa.userKeys("alice")) != 1 {
		t.Fatalf("setup: expected 1 key")
	}

	var live seaweedv1.S3Credentials
	if err := cli.Get(context.Background(), key, &live); err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := cli.Delete(context.Background(), &live); err != nil {
		t.Fatalf("delete: %v", err)
	}
	reconcileOnce(t, r, key)

	if k := fa.userKeys("alice"); len(k) != 0 {
		t.Errorf("expected access key removed, got %v", k)
	}
	var secret corev1.Secret
	err := cli.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: "alice-secret"}, &secret)
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected managed secret deleted, got err=%v", err)
	}
}

func TestS3Credentials_Delete_RetainKeepsKeyAndOrphansSecret(t *testing.T) {
	scheme := iamTestScheme(t)
	cred := &seaweedv1.S3Credentials{
		ObjectMeta: metav1.ObjectMeta{Name: "alice-creds", Namespace: "media"},
		Spec: seaweedv1.S3CredentialsSpec{
			SeaweedRef:    iamSeaweedRef(),
			IdentityRef:   seaweedv1.S3IdentityRef{Name: "alice"},
			SecretRef:     seaweedv1.S3SecretRef{Name: "alice-secret"},
			ReclaimPolicy: seaweedv1.S3ReclaimRetain,
		},
	}
	cli := iamTestClient(t, scheme, newTestSeaweed(), cred)
	fa := newFakeIAMAdmin()
	fa.seedUser("alice")
	r := &S3CredentialsReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)

	key := types.NamespacedName{Namespace: "media", Name: "alice-creds"}
	reconcileStable(t, r, key, 5)

	secretKey := types.NamespacedName{Namespace: "media", Name: "alice-secret"}
	var secret corev1.Secret
	if err := cli.Get(context.Background(), secretKey, &secret); err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if len(secret.OwnerReferences) == 0 {
		t.Fatal("setup: operator-created secret should have an owner reference")
	}

	var live seaweedv1.S3Credentials
	if err := cli.Get(context.Background(), key, &live); err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := cli.Delete(context.Background(), &live); err != nil {
		t.Fatalf("delete: %v", err)
	}
	reconcileOnce(t, r, key)

	if k := fa.userKeys("alice"); len(k) != 1 {
		t.Errorf("Retain should keep the access key, got %v", k)
	}
	if err := cli.Get(context.Background(), secretKey, &secret); err != nil {
		t.Fatalf("secret should survive Retain: %v", err)
	}
	if len(secret.OwnerReferences) != 0 {
		t.Errorf("Retain should orphan the secret (strip owner refs), got %v", secret.OwnerReferences)
	}
}

// --- S3PolicyBinding ---

func TestS3PolicyBinding_AttachesAndDetaches(t *testing.T) {
	scheme := iamTestScheme(t)
	binding := &seaweedv1.S3PolicyBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "rw-bind", Namespace: "media"},
		Spec: seaweedv1.S3PolicyBindingSpec{
			SeaweedRef: iamSeaweedRef(),
			PolicyRef:  seaweedv1.S3PolicyRef{Name: "rw"},
			Subjects: []seaweedv1.S3Subject{
				{Kind: seaweedv1.S3SubjectKindIdentity, Name: "alice"},
				{Kind: seaweedv1.S3SubjectKindIdentity, Name: "bob"},
			},
		},
	}
	cli := iamTestClient(t, scheme, newTestSeaweed(), binding)
	fa := newFakeIAMAdmin()
	fa.seedUser("alice")
	fa.seedUser("bob")
	if err := fa.PutPolicy(context.Background(), "rw", "{}"); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	r := &S3PolicyBindingReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)

	key := types.NamespacedName{Namespace: "media", Name: "rw-bind"}
	reconcileStable(t, r, key, 5)

	if got := fa.userPolicies("alice"); len(got) != 1 || got[0] != "rw" {
		t.Errorf("alice policies = %v, want [rw]", got)
	}
	if got := fa.userPolicies("bob"); len(got) != 1 || got[0] != "rw" {
		t.Errorf("bob policies = %v, want [rw]", got)
	}

	// Remove bob from subjects; reconcile should detach him.
	var live seaweedv1.S3PolicyBinding
	if err := cli.Get(context.Background(), key, &live); err != nil {
		t.Fatalf("get: %v", err)
	}
	live.Spec.Subjects = []seaweedv1.S3Subject{{Kind: seaweedv1.S3SubjectKindIdentity, Name: "alice"}}
	if err := cli.Update(context.Background(), &live); err != nil {
		t.Fatalf("update: %v", err)
	}
	reconcileStable(t, r, key, 5)

	if got := fa.userPolicies("bob"); len(got) != 0 {
		t.Errorf("bob policies after removal = %v, want []", got)
	}
	if got := fa.userPolicies("alice"); len(got) != 1 {
		t.Errorf("alice policies should be unchanged, got %v", got)
	}
}

func TestS3PolicyBinding_WaitsForPolicy(t *testing.T) {
	scheme := iamTestScheme(t)
	binding := &seaweedv1.S3PolicyBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "rw-bind", Namespace: "media"},
		Spec: seaweedv1.S3PolicyBindingSpec{
			SeaweedRef: iamSeaweedRef(),
			PolicyRef:  seaweedv1.S3PolicyRef{Name: "ghost"},
			Subjects:   []seaweedv1.S3Subject{{Kind: seaweedv1.S3SubjectKindIdentity, Name: "alice"}},
		},
	}
	cli := iamTestClient(t, scheme, newTestSeaweed(), binding)
	fa := newFakeIAMAdmin()
	fa.seedUser("alice")
	r := &S3PolicyBindingReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)

	key := types.NamespacedName{Namespace: "media", Name: "rw-bind"}
	reconcileOnce(t, r, key) // finalizer
	res := reconcileOnce(t, r, key)
	if res.RequeueAfter == 0 {
		t.Fatal("expected requeue while waiting for policy")
	}
	if got := fa.userPolicies("alice"); len(got) != 0 {
		t.Errorf("no attach should happen while policy missing, got %v", got)
	}
}

func TestS3PolicyBinding_PartialAttachWhenOneSubjectMissing(t *testing.T) {
	scheme := iamTestScheme(t)
	binding := &seaweedv1.S3PolicyBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "rw-bind", Namespace: "media"},
		Spec: seaweedv1.S3PolicyBindingSpec{
			SeaweedRef: iamSeaweedRef(),
			PolicyRef:  seaweedv1.S3PolicyRef{Name: "rw"},
			Subjects: []seaweedv1.S3Subject{
				{Kind: seaweedv1.S3SubjectKindIdentity, Name: "alice"},
				{Kind: seaweedv1.S3SubjectKindIdentity, Name: "ghost"},
			},
		},
	}
	cli := iamTestClient(t, scheme, newTestSeaweed(), binding)
	fa := newFakeIAMAdmin()
	fa.seedUser("alice") // ghost is intentionally absent
	if err := fa.PutPolicy(context.Background(), "rw", "{}"); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	r := &S3PolicyBindingReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)

	key := types.NamespacedName{Namespace: "media", Name: "rw-bind"}
	reconcileOnce(t, r, key) // finalizer
	res := reconcileOnce(t, r, key)
	if res.RequeueAfter == 0 {
		t.Fatal("expected requeue while one subject is missing")
	}

	// The present subject must still get the policy (no head-of-line blocking).
	if got := fa.userPolicies("alice"); len(got) != 1 || got[0] != "rw" {
		t.Errorf("alice should be attached despite ghost missing, got %v", got)
	}
	var got seaweedv1.S3PolicyBinding
	if err := cli.Get(context.Background(), key, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Phase != seaweedv1.S3PhasePending {
		t.Errorf("phase = %q, want Pending", got.Status.Phase)
	}
	if len(got.Status.AttachedSubjects) != 1 || got.Status.AttachedSubjects[0] != "alice" {
		t.Errorf("attachedSubjects = %v, want [alice]", got.Status.AttachedSubjects)
	}
}

// Ensure the reconcilers satisfy the controller-runtime Reconciler interface.
var (
	_ reconcile.Reconciler = (*S3IdentityReconciler)(nil)
	_ reconcile.Reconciler = (*S3CredentialsReconciler)(nil)
	_ reconcile.Reconciler = (*S3PolicyReconciler)(nil)
	_ reconcile.Reconciler = (*S3PolicyBindingReconciler)(nil)
)
