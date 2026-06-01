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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// --- grant matching (pure) ---

func TestGrantAllows(t *testing.T) {
	credsFrom := referent{Group: groupSeaweed, Kind: kindS3Credentials, Namespace: "app"}
	secretTo := referent{Group: groupCore, Kind: kindSecret, Namespace: "secrets", Name: "shared"}

	grant := func(from []seaweedv1.ReferenceGrantFrom, to []seaweedv1.ReferenceGrantTo) *seaweedv1.ResourceReferenceGrantSpec {
		return &seaweedv1.ResourceReferenceGrantSpec{From: from, To: to}
	}
	from := func(g, k, ns string) seaweedv1.ReferenceGrantFrom {
		return seaweedv1.ReferenceGrantFrom{Group: g, Kind: k, Namespace: ns}
	}
	to := func(g, k, n string) seaweedv1.ReferenceGrantTo {
		return seaweedv1.ReferenceGrantTo{Group: g, Kind: k, Name: n}
	}

	tests := []struct {
		name string
		spec *seaweedv1.ResourceReferenceGrantSpec
		want bool
	}{
		{
			name: "wildcard name matches",
			spec: grant(
				[]seaweedv1.ReferenceGrantFrom{from(groupSeaweed, kindS3Credentials, "app")},
				[]seaweedv1.ReferenceGrantTo{to(groupCore, kindSecret, "")}),
			want: true,
		},
		{
			name: "pinned name matches",
			spec: grant(
				[]seaweedv1.ReferenceGrantFrom{from(groupSeaweed, kindS3Credentials, "app")},
				[]seaweedv1.ReferenceGrantTo{to(groupCore, kindSecret, "shared")}),
			want: true,
		},
		{
			name: "pinned name mismatch denied",
			spec: grant(
				[]seaweedv1.ReferenceGrantFrom{from(groupSeaweed, kindS3Credentials, "app")},
				[]seaweedv1.ReferenceGrantTo{to(groupCore, kindSecret, "other")}),
			want: false,
		},
		{
			name: "wrong source namespace denied",
			spec: grant(
				[]seaweedv1.ReferenceGrantFrom{from(groupSeaweed, kindS3Credentials, "evil")},
				[]seaweedv1.ReferenceGrantTo{to(groupCore, kindSecret, "")}),
			want: false,
		},
		{
			name: "wrong source kind denied",
			spec: grant(
				[]seaweedv1.ReferenceGrantFrom{from(groupSeaweed, kindS3Identity, "app")},
				[]seaweedv1.ReferenceGrantTo{to(groupCore, kindSecret, "")}),
			want: false,
		},
		{
			name: "wrong target group denied (seaweed vs core)",
			spec: grant(
				[]seaweedv1.ReferenceGrantFrom{from(groupSeaweed, kindS3Credentials, "app")},
				[]seaweedv1.ReferenceGrantTo{to(groupSeaweed, kindSecret, "")}),
			want: false,
		},
		{
			name: "one of several from/to entries matches",
			spec: grant(
				[]seaweedv1.ReferenceGrantFrom{
					from(groupSeaweed, kindS3Identity, "app"),
					from(groupSeaweed, kindS3Credentials, "app"),
				},
				[]seaweedv1.ReferenceGrantTo{
					to(groupSeaweed, kindSeaweed, ""),
					to(groupCore, kindSecret, "shared"),
				}),
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := grantAllows(tc.spec, credsFrom, secretTo); got != tc.want {
				t.Errorf("grantAllows = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReferenceGrantPermits(t *testing.T) {
	scheme := iamTestScheme(t)
	grant := &seaweedv1.ResourceReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "g", Namespace: "secrets"},
		Spec: seaweedv1.ResourceReferenceGrantSpec{
			From: []seaweedv1.ReferenceGrantFrom{{Group: groupSeaweed, Kind: kindS3Credentials, Namespace: "app"}},
			To:   []seaweedv1.ReferenceGrantTo{{Group: groupCore, Kind: kindSecret}},
		},
	}
	cli := iamTestClientNoGrants(t, scheme, grant)
	ctx := context.Background()

	// Same-namespace reference is permitted without consulting any grant.
	ok, err := referenceGrantPermits(ctx, cli,
		referent{Group: groupCore, Kind: kindSecret, Namespace: "app"},
		referent{Group: groupCore, Kind: kindSecret, Namespace: "app", Name: "x"})
	if err != nil || !ok {
		t.Fatalf("same-namespace should be permitted: ok=%v err=%v", ok, err)
	}

	// Cross-namespace reference permitted by the grant.
	if ok, err := secretRefPermitted(ctx, cli, "secrets", "shared", "app"); err != nil || !ok {
		t.Fatalf("expected permitted, ok=%v err=%v", ok, err)
	}

	// Cross-namespace reference from an un-granted source namespace is denied.
	if ok, err := secretRefPermitted(ctx, cli, "secrets", "shared", "other"); err != nil || ok {
		t.Fatalf("expected denied for un-granted source, ok=%v err=%v", ok, err)
	}
}

// --- enforcement: deny then converge ---

func TestS3Identity_CrossNamespaceSeaweedRef_DeniedThenGranted(t *testing.T) {
	scheme := iamTestScheme(t)
	id := &seaweedv1.S3Identity{
		ObjectMeta: metav1.ObjectMeta{Name: "alice", Namespace: "media"},
		Spec:       seaweedv1.S3IdentitySpec{SeaweedRef: iamSeaweedRef()},
	}
	// No grant seeded: the cross-namespace seaweedRef (media -> seaweedfs) is denied.
	cli := iamTestClientNoGrants(t, scheme, newTestSeaweed(), id)
	fa := newFakeIAMAdmin()
	r := &S3IdentityReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)
	key := types.NamespacedName{Namespace: "media", Name: "alice"}

	res := reconcileOnce(t, r, key)
	if res.RequeueAfter == 0 {
		t.Fatal("expected requeue while the seaweedRef is not granted")
	}

	if _, err := fa.GetUser(context.Background(), "alice"); err == nil {
		t.Fatal("no IAM user should be created before the reference is granted")
	}
	var got seaweedv1.S3Identity
	if err := cli.Get(context.Background(), key, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Phase != seaweedv1.S3PhasePending {
		t.Errorf("phase = %q, want Pending", got.Status.Phase)
	}
	if c := meta.FindStatusCondition(got.Status.Conditions, seaweedv1.S3ConditionReferenceGranted); c == nil || c.Status != metav1.ConditionFalse {
		t.Errorf("expected ReferenceGranted=False, got %+v", got.Status.Conditions)
	}
	// Nothing was provisioned, so no finalizer should be attached yet.
	if len(got.Finalizers) != 0 {
		t.Errorf("no finalizer should be added before the grant exists, got %v", got.Finalizers)
	}

	// Publish a name-pinned grant; the identity now converges to Ready.
	grant := &seaweedv1.ResourceReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "allow-media", Namespace: "seaweedfs"},
		Spec: seaweedv1.ResourceReferenceGrantSpec{
			From: []seaweedv1.ReferenceGrantFrom{{Group: groupSeaweed, Kind: kindS3Identity, Namespace: "media"}},
			To:   []seaweedv1.ReferenceGrantTo{{Group: groupSeaweed, Kind: kindSeaweed, Name: "prod"}},
		},
	}
	if err := cli.Create(context.Background(), grant); err != nil {
		t.Fatalf("create grant: %v", err)
	}
	reconcileStable(t, r, key, 5)

	if _, err := fa.GetUser(context.Background(), "alice"); err != nil {
		t.Fatalf("expected user created after grant, got %v", err)
	}
	if err := cli.Get(context.Background(), key, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Phase != seaweedv1.S3PhaseReady {
		t.Errorf("phase = %q, want Ready after grant", got.Status.Phase)
	}
	// The stale ReferenceGranted=False must be cleared once the grant is honored.
	if c := meta.FindStatusCondition(got.Status.Conditions, seaweedv1.S3ConditionReferenceGranted); c != nil {
		t.Errorf("ReferenceGranted condition should be cleared after the grant, got %+v", c)
	}
}

func TestS3Credentials_CrossNamespaceSecretRef_DeniedWithoutGrant(t *testing.T) {
	scheme := iamTestScheme(t)
	// Permit the seaweedRef but not the secretRef, isolating the secret check.
	seaweedGrant := &seaweedv1.ResourceReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "allow-cluster", Namespace: "seaweedfs"},
		Spec: seaweedv1.ResourceReferenceGrantSpec{
			From: []seaweedv1.ReferenceGrantFrom{{Group: groupSeaweed, Kind: kindS3Credentials, Namespace: "media"}},
			To:   []seaweedv1.ReferenceGrantTo{{Group: groupSeaweed, Kind: kindSeaweed}},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: "secrets"},
		Data: map[string][]byte{
			defaultAccessKeyField: []byte("AKIAGRANTLESS"),
			defaultSecretKeyField: []byte("sk"),
		},
	}
	cred := &seaweedv1.S3Credentials{
		ObjectMeta: metav1.ObjectMeta{Name: "alice-creds", Namespace: "media"},
		Spec: seaweedv1.S3CredentialsSpec{
			SeaweedRef:  iamSeaweedRef(),
			IdentityRef: seaweedv1.S3IdentityRef{Name: "alice"},
			SecretRef:   seaweedv1.S3SecretRef{Name: "shared", Namespace: "secrets"},
		},
	}
	cli := iamTestClientNoGrants(t, scheme, newTestSeaweed(), seaweedGrant, secret, cred)
	fa := newFakeIAMAdmin()
	fa.seedUser("alice")
	r := &S3CredentialsReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)
	key := types.NamespacedName{Namespace: "media", Name: "alice-creds"}

	reconcileOnce(t, r, key) // adds finalizer (seaweedRef is granted)
	res := reconcileOnce(t, r, key)
	if res.RequeueAfter == 0 {
		t.Fatal("expected requeue while the secretRef is not granted")
	}
	if keys := fa.userKeys("alice"); len(keys) != 0 {
		t.Errorf("no IAM key should be provisioned while the secretRef is denied, got %v", keys)
	}
	var got seaweedv1.S3Credentials
	if err := cli.Get(context.Background(), key, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Phase != seaweedv1.S3PhasePending {
		t.Errorf("phase = %q, want Pending", got.Status.Phase)
	}
	if c := meta.FindStatusCondition(got.Status.Conditions, seaweedv1.S3ConditionReferenceGranted); c == nil || c.Status != metav1.ConditionFalse {
		t.Errorf("expected ReferenceGranted=False, got %+v", got.Status.Conditions)
	}
}

func TestBucket_CrossNamespaceClusterRef_DeniedWithoutGrant(t *testing.T) {
	bucket := newTestBucket("photos")
	fa := newFakeAdmin()
	r, cli := testReconcilerNoGrants(t, fa, newTestSeaweed(), bucket)
	key := types.NamespacedName{Namespace: bucket.Namespace, Name: bucket.Name}

	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatal("expected requeue while the clusterRef is not granted")
	}
	if len(fa.calls) != 0 {
		t.Errorf("no filer calls should happen while the clusterRef is denied, got %v", fa.calls)
	}
	var got seaweedv1.Bucket
	if err := cli.Get(context.Background(), key, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Phase != seaweedv1.BucketPhasePending {
		t.Errorf("phase = %q, want Pending", got.Status.Phase)
	}
	if c := meta.FindStatusCondition(got.Status.Conditions, seaweedv1.BucketConditionClusterRefForbidden); c == nil || c.Status != metav1.ConditionTrue {
		t.Errorf("expected ClusterRefForbidden=True, got %+v", got.Status.Conditions)
	}
}

// TestS3Identity_Deletion_NotBlockedByMissingGrant pins that revoking (or never
// granting) a cross-namespace reference can never strand a finalizer: deletion
// still cleans up the provisioned IAM object.
func TestS3Identity_Deletion_NotBlockedByMissingGrant(t *testing.T) {
	scheme := iamTestScheme(t)
	id := &seaweedv1.S3Identity{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "alice",
			Namespace:  "media",
			Finalizers: []string{s3IdentityFinalizer},
		},
		Spec: seaweedv1.S3IdentitySpec{SeaweedRef: iamSeaweedRef()},
	}
	// No grant: the create/update path would be denied, but deletion must not be.
	cli := iamTestClientNoGrants(t, scheme, newTestSeaweed(), id)
	fa := newFakeIAMAdmin()
	fa.seedUser("alice")
	r := &S3IdentityReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)
	key := types.NamespacedName{Namespace: "media", Name: "alice"}

	var live seaweedv1.S3Identity
	if err := cli.Get(context.Background(), key, &live); err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := cli.Delete(context.Background(), &live); err != nil {
		t.Fatalf("delete: %v", err)
	}
	reconcileOnce(t, r, key)

	if _, err := fa.GetUser(context.Background(), "alice"); err == nil {
		t.Error("IAM user should be deleted on deletion even without a grant")
	}
	var after seaweedv1.S3Identity
	if err := cli.Get(context.Background(), key, &after); err == nil && len(after.Finalizers) != 0 {
		t.Errorf("finalizer should be cleared on deletion, got %v", after.Finalizers)
	}
}

func TestS3Policy_CrossNamespaceSeaweedRef_DeniedWithoutGrant(t *testing.T) {
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
	cli := iamTestClientNoGrants(t, scheme, newTestSeaweed(), pol)
	fa := newFakeIAMAdmin()
	r := &S3PolicyReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)
	key := types.NamespacedName{Namespace: "media", Name: "rw"}

	res := reconcileOnce(t, r, key)
	if res.RequeueAfter == 0 {
		t.Fatal("expected requeue while the seaweedRef is not granted")
	}
	if _, err := fa.GetPolicy(context.Background(), "rw"); err == nil {
		t.Error("no policy should be provisioned before the reference is granted")
	}
	var got seaweedv1.S3Policy
	if err := cli.Get(context.Background(), key, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Phase != seaweedv1.S3PhasePending {
		t.Errorf("phase = %q, want Pending", got.Status.Phase)
	}
	if c := meta.FindStatusCondition(got.Status.Conditions, seaweedv1.S3ConditionReferenceGranted); c == nil || c.Status != metav1.ConditionFalse {
		t.Errorf("expected ReferenceGranted=False, got %+v", got.Status.Conditions)
	}
}

func TestS3PolicyBinding_CrossNamespaceSeaweedRef_DeniedWithoutGrant(t *testing.T) {
	scheme := iamTestScheme(t)
	binding := &seaweedv1.S3PolicyBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "rw-bind", Namespace: "media"},
		Spec: seaweedv1.S3PolicyBindingSpec{
			SeaweedRef: iamSeaweedRef(),
			PolicyRef:  seaweedv1.S3PolicyRef{Name: "rw"},
			Subjects:   []seaweedv1.S3Subject{{Kind: seaweedv1.S3SubjectKindIdentity, Name: "alice"}},
		},
	}
	cli := iamTestClientNoGrants(t, scheme, newTestSeaweed(), binding)
	fa := newFakeIAMAdmin()
	fa.seedUser("alice")
	if err := fa.PutPolicy(context.Background(), "rw", "{}"); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	r := &S3PolicyBindingReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)
	key := types.NamespacedName{Namespace: "media", Name: "rw-bind"}

	res := reconcileOnce(t, r, key)
	if res.RequeueAfter == 0 {
		t.Fatal("expected requeue while the seaweedRef is not granted")
	}
	if got := fa.userPolicies("alice"); len(got) != 0 {
		t.Errorf("no policy should be attached before the reference is granted, got %v", got)
	}
	var got seaweedv1.S3PolicyBinding
	if err := cli.Get(context.Background(), key, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Phase != seaweedv1.S3PhasePending {
		t.Errorf("phase = %q, want Pending", got.Status.Phase)
	}
	if c := meta.FindStatusCondition(got.Status.Conditions, seaweedv1.S3ConditionReferenceGranted); c == nil || c.Status != metav1.ConditionFalse {
		t.Errorf("expected ReferenceGranted=False, got %+v", got.Status.Conditions)
	}
}

// assertDeletionNotBlocked deletes live and reconciles once with no grant,
// asserting the finalizer is cleared. fresh is an empty object for the re-read.
func assertDeletionNotBlocked(t *testing.T, cli client.Client, r reconcile.Reconciler, key types.NamespacedName, live, fresh client.Object) {
	t.Helper()
	if err := cli.Get(context.Background(), key, live); err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := cli.Delete(context.Background(), live); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile during deletion: %v", err)
	}
	if err := cli.Get(context.Background(), key, fresh); err == nil && len(fresh.GetFinalizers()) != 0 {
		t.Errorf("finalizer should be cleared on deletion without a grant, got %v", fresh.GetFinalizers())
	}
}

func TestS3Credentials_Deletion_NotBlockedByMissingGrant(t *testing.T) {
	scheme := iamTestScheme(t)
	// Cross-namespace seaweedRef AND secretRef, neither granted: deletion must
	// still clear the finalizer.
	cred := &seaweedv1.S3Credentials{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "alice-creds",
			Namespace:  "media",
			Finalizers: []string{s3CredentialsFinalizer},
		},
		Spec: seaweedv1.S3CredentialsSpec{
			SeaweedRef:  iamSeaweedRef(),
			IdentityRef: seaweedv1.S3IdentityRef{Name: "alice"},
			SecretRef:   seaweedv1.S3SecretRef{Name: "shared", Namespace: "secrets"},
		},
	}
	cli := iamTestClientNoGrants(t, scheme, newTestSeaweed(), cred)
	fa := newFakeIAMAdmin()
	fa.seedUser("alice")
	r := &S3CredentialsReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)
	key := types.NamespacedName{Namespace: "media", Name: "alice-creds"}

	assertDeletionNotBlocked(t, cli, r, key, &seaweedv1.S3Credentials{}, &seaweedv1.S3Credentials{})
}

func TestS3Policy_Deletion_NotBlockedByMissingGrant(t *testing.T) {
	scheme := iamTestScheme(t)
	pol := &seaweedv1.S3Policy{
		ObjectMeta: metav1.ObjectMeta{Name: "rw", Namespace: "media", Finalizers: []string{s3PolicyFinalizer}},
		Spec: seaweedv1.S3PolicySpec{
			SeaweedRef: iamSeaweedRef(),
			Statements: []seaweedv1.S3PolicyStatement{{
				Effect:    seaweedv1.S3PolicyEffectAllow,
				Actions:   []string{"s3:GetObject"},
				Resources: []string{"b/*"},
			}},
		},
	}
	cli := iamTestClientNoGrants(t, scheme, newTestSeaweed(), pol)
	fa := newFakeIAMAdmin()
	if err := fa.PutPolicy(context.Background(), "rw", "{}"); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	r := &S3PolicyReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)
	key := types.NamespacedName{Namespace: "media", Name: "rw"}

	assertDeletionNotBlocked(t, cli, r, key, &seaweedv1.S3Policy{}, &seaweedv1.S3Policy{})
}

func TestS3PolicyBinding_Deletion_NotBlockedByMissingGrant(t *testing.T) {
	scheme := iamTestScheme(t)
	binding := &seaweedv1.S3PolicyBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "rw-bind", Namespace: "media", Finalizers: []string{s3PolicyBindingFinalizer}},
		Spec: seaweedv1.S3PolicyBindingSpec{
			SeaweedRef: iamSeaweedRef(),
			PolicyRef:  seaweedv1.S3PolicyRef{Name: "rw"},
			Subjects:   []seaweedv1.S3Subject{{Kind: seaweedv1.S3SubjectKindIdentity, Name: "alice"}},
		},
	}
	cli := iamTestClientNoGrants(t, scheme, newTestSeaweed(), binding)
	fa := newFakeIAMAdmin()
	if err := fa.PutPolicy(context.Background(), "rw", "{}"); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	r := &S3PolicyBindingReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)
	key := types.NamespacedName{Namespace: "media", Name: "rw-bind"}

	assertDeletionNotBlocked(t, cli, r, key, &seaweedv1.S3PolicyBinding{}, &seaweedv1.S3PolicyBinding{})
}

func TestBucket_Deletion_NotBlockedByMissingGrant(t *testing.T) {
	bucket := newTestBucket("photos")
	bucket.Finalizers = []string{BucketFinalizer}
	fa := newFakeAdmin()
	r, cli := testReconcilerNoGrants(t, fa, newTestSeaweed(), bucket)
	key := types.NamespacedName{Namespace: bucket.Namespace, Name: bucket.Name}

	assertDeletionNotBlocked(t, cli, r, key, &seaweedv1.Bucket{}, &seaweedv1.Bucket{})
}
