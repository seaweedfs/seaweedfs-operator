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
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// stagingRefGrant authorizes IAM CRs in "staging" to reference the test
// Seaweed cluster, mirroring the default grant for "media".
func stagingRefGrant() *seaweedv1.ResourceReferenceGrant {
	return &seaweedv1.ResourceReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "test-allow-staging", Namespace: "seaweedfs"},
		Spec: seaweedv1.ResourceReferenceGrantSpec{
			From: []seaweedv1.ReferenceGrantFrom{
				{Group: groupSeaweed, Kind: kindS3Identity, Namespace: "staging"},
				{Group: groupSeaweed, Kind: kindS3Policy, Namespace: "staging"},
			},
			To: []seaweedv1.ReferenceGrantTo{{Group: groupSeaweed, Kind: kindSeaweed}},
		},
	}
}

func identityAt(namespace, name string, age time.Duration) *seaweedv1.S3Identity {
	return &seaweedv1.S3Identity{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         namespace,
			CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
		},
		Spec: seaweedv1.S3IdentitySpec{SeaweedRef: iamSeaweedRef()},
	}
}

func policyAt(namespace, name string, age time.Duration, doc string) *seaweedv1.S3Policy {
	return &seaweedv1.S3Policy{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         namespace,
			CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
		},
		Spec: seaweedv1.S3PolicySpec{SeaweedRef: iamSeaweedRef(), PolicyDocument: doc},
	}
}

func TestS3Identity_SameNameAcrossNamespaces_OldestWins(t *testing.T) {
	scheme := iamTestScheme(t)
	older := identityAt("media", "myapp", 2*time.Hour)
	newer := identityAt("staging", "myapp", time.Hour)
	cli := iamTestClient(t, scheme, newTestSeaweed(), stagingRefGrant(), older, newer)
	fa := newFakeIAMAdmin()
	r := &S3IdentityReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)

	olderKey := types.NamespacedName{Namespace: "media", Name: "myapp"}
	newerKey := types.NamespacedName{Namespace: "staging", Name: "myapp"}
	reconcileStable(t, r, olderKey, 5)
	reconcileOnce(t, r, newerKey)

	var winner seaweedv1.S3Identity
	if err := cli.Get(context.Background(), olderKey, &winner); err != nil {
		t.Fatalf("get winner: %v", err)
	}
	if winner.Status.Phase != seaweedv1.S3PhaseReady {
		t.Errorf("winner phase = %q, want Ready", winner.Status.Phase)
	}

	var loser seaweedv1.S3Identity
	if err := cli.Get(context.Background(), newerKey, &loser); err != nil {
		t.Fatalf("get loser: %v", err)
	}
	if loser.Status.Phase != seaweedv1.S3PhaseFailed {
		t.Errorf("loser phase = %q, want Failed", loser.Status.Phase)
	}
	cond := meta.FindStatusCondition(loser.Status.Conditions, seaweedv1.S3ConditionReady)
	if cond == nil || cond.Reason != "Conflict" {
		t.Errorf("loser Ready condition = %+v, want reason Conflict", cond)
	}
}

func TestS3Identity_SameNameDifferentClusters_NoConflict(t *testing.T) {
	scheme := iamTestScheme(t)
	second := newTestSeaweed()
	second.Name = "prod2"
	older := identityAt("media", "myapp", 2*time.Hour)
	newer := identityAt("staging", "myapp", time.Hour)
	newer.Spec.SeaweedRef = seaweedv1.SeaweedReference{Name: "prod2", Namespace: "seaweedfs"}
	cli := iamTestClient(t, scheme, newTestSeaweed(), second, stagingRefGrant(), older, newer)
	fa := newFakeIAMAdmin()
	r := &S3IdentityReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)

	reconcileStable(t, r, types.NamespacedName{Namespace: "media", Name: "myapp"}, 5)
	reconcileStable(t, r, types.NamespacedName{Namespace: "staging", Name: "myapp"}, 5)

	var got seaweedv1.S3Identity
	if err := cli.Get(context.Background(), types.NamespacedName{Namespace: "staging", Name: "myapp"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Phase != seaweedv1.S3PhaseReady {
		t.Errorf("phase = %q, want Ready (different clusters do not conflict)", got.Status.Phase)
	}
}

func TestS3Identity_ConflictLoserDelete_LeavesUserIntact(t *testing.T) {
	scheme := iamTestScheme(t)
	older := identityAt("media", "myapp", 2*time.Hour)
	newer := identityAt("staging", "myapp", time.Hour)
	newer.Finalizers = []string{s3IdentityFinalizer}
	cli := iamTestClient(t, scheme, newTestSeaweed(), stagingRefGrant(), older, newer)
	fa := newFakeIAMAdmin()
	fa.seedUser("myapp")
	r := &S3IdentityReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)

	if err := cli.Delete(context.Background(), newer); err != nil {
		t.Fatalf("delete: %v", err)
	}
	reconcileOnce(t, r, types.NamespacedName{Namespace: "staging", Name: "myapp"})

	if _, err := fa.GetUser(context.Background(), "myapp"); err != nil {
		t.Fatalf("user myapp should survive deletion of the conflicting CR, got %v", err)
	}
}

func TestS3Identity_WinnerDelete_PromotesLoser(t *testing.T) {
	scheme := iamTestScheme(t)
	older := identityAt("media", "myapp", 2*time.Hour)
	older.Finalizers = []string{s3IdentityFinalizer}
	newer := identityAt("staging", "myapp", time.Hour)
	cli := iamTestClient(t, scheme, newTestSeaweed(), stagingRefGrant(), older, newer)
	fa := newFakeIAMAdmin()
	fa.seedUser("myapp")
	r := &S3IdentityReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)

	olderKey := types.NamespacedName{Namespace: "media", Name: "myapp"}
	newerKey := types.NamespacedName{Namespace: "staging", Name: "myapp"}
	reconcileOnce(t, r, newerKey)

	if err := cli.Delete(context.Background(), older); err != nil {
		t.Fatalf("delete: %v", err)
	}
	reconcileOnce(t, r, olderKey)

	// The departing winner hands the user over to the surviving claimant.
	if _, err := fa.GetUser(context.Background(), "myapp"); err != nil {
		t.Fatalf("user myapp should be handed over, got %v", err)
	}

	reconcileStable(t, r, newerKey, 5)
	var got seaweedv1.S3Identity
	if err := cli.Get(context.Background(), newerKey, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Phase != seaweedv1.S3PhaseReady {
		t.Errorf("phase = %q, want Ready after winner removal", got.Status.Phase)
	}
}

func TestS3Policy_SameNameAcrossNamespaces_OldestWins(t *testing.T) {
	scheme := iamTestScheme(t)
	older := policyAt("media", "rw", 2*time.Hour, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:*"],"Resource":["arn:aws:s3:::media/*"]}]}`)
	newer := policyAt("staging", "rw", time.Hour, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:*"],"Resource":["arn:aws:s3:::staging/*"]}]}`)
	cli := iamTestClient(t, scheme, newTestSeaweed(), stagingRefGrant(), older, newer)
	fa := newFakeIAMAdmin()
	r := &S3PolicyReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)

	reconcileStable(t, r, types.NamespacedName{Namespace: "media", Name: "rw"}, 5)
	reconcileOnce(t, r, types.NamespacedName{Namespace: "staging", Name: "rw"})

	var loser seaweedv1.S3Policy
	if err := cli.Get(context.Background(), types.NamespacedName{Namespace: "staging", Name: "rw"}, &loser); err != nil {
		t.Fatalf("get: %v", err)
	}
	if loser.Status.Phase != seaweedv1.S3PhaseFailed {
		t.Errorf("loser phase = %q, want Failed", loser.Status.Phase)
	}
	cond := meta.FindStatusCondition(loser.Status.Conditions, seaweedv1.S3ConditionReady)
	if cond == nil || cond.Reason != "Conflict" {
		t.Errorf("loser Ready condition = %+v, want reason Conflict", cond)
	}

	doc, err := fa.GetPolicy(context.Background(), "rw")
	if err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if doc != older.Spec.PolicyDocument {
		t.Errorf("policy document was overwritten by the losing CR")
	}
}

func TestS3Credentials_IdentityRefResolvesResourceName(t *testing.T) {
	scheme := iamTestScheme(t)
	id := identityAt("media", "myapp", time.Hour)
	id.Spec.Name = "myapp-media"
	cred := &seaweedv1.S3Credentials{
		ObjectMeta: metav1.ObjectMeta{Name: "myapp-creds", Namespace: "media"},
		Spec: seaweedv1.S3CredentialsSpec{
			SeaweedRef:  iamSeaweedRef(),
			IdentityRef: seaweedv1.S3IdentityRef{Name: "myapp"},
		},
	}
	cli := iamTestClient(t, scheme, newTestSeaweed(), id, cred)
	fa := newFakeIAMAdmin()
	fa.seedUser("myapp-media")
	r := &S3CredentialsReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)

	key := types.NamespacedName{Namespace: "media", Name: "myapp-creds"}
	reconcileStable(t, r, key, 5)

	var got seaweedv1.S3Credentials
	if err := cli.Get(context.Background(), key, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Phase != seaweedv1.S3PhaseReady {
		t.Fatalf("phase = %q, want Ready", got.Status.Phase)
	}
	if got.Status.IdentityName != "myapp-media" {
		t.Errorf("status.identityName = %q, want myapp-media", got.Status.IdentityName)
	}
	user, err := fa.GetUser(context.Background(), "myapp-media")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if len(user.AccessKeys) != 1 || user.AccessKeys[0] != got.Status.AccessKey {
		t.Errorf("access keys on myapp-media = %v, want [%s]", user.AccessKeys, got.Status.AccessKey)
	}
}

func TestS3Credentials_IdentityRefFallsBackToIAMName(t *testing.T) {
	scheme := iamTestScheme(t)
	cred := &seaweedv1.S3Credentials{
		ObjectMeta: metav1.ObjectMeta{Name: "ext-creds", Namespace: "media"},
		Spec: seaweedv1.S3CredentialsSpec{
			SeaweedRef:  iamSeaweedRef(),
			IdentityRef: seaweedv1.S3IdentityRef{Name: "external"},
		},
	}
	cli := iamTestClient(t, scheme, newTestSeaweed(), cred)
	fa := newFakeIAMAdmin()
	fa.seedUser("external")
	r := &S3CredentialsReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)

	key := types.NamespacedName{Namespace: "media", Name: "ext-creds"}
	reconcileStable(t, r, key, 5)

	user, err := fa.GetUser(context.Background(), "external")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if len(user.AccessKeys) != 1 {
		t.Errorf("access keys on external = %v, want one key", user.AccessKeys)
	}
}

func TestS3Credentials_Delete_CleansUpResolvedIdentity(t *testing.T) {
	scheme := iamTestScheme(t)
	cred := &seaweedv1.S3Credentials{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "myapp-creds",
			Namespace:  "media",
			Finalizers: []string{s3CredentialsFinalizer},
		},
		Spec: seaweedv1.S3CredentialsSpec{
			SeaweedRef:  iamSeaweedRef(),
			IdentityRef: seaweedv1.S3IdentityRef{Name: "myapp"},
		},
		Status: seaweedv1.S3CredentialsStatus{AccessKey: "AKIA123", IdentityName: "myapp-media"},
	}
	cli := iamTestClient(t, scheme, newTestSeaweed(), cred)
	fa := newFakeIAMAdmin()
	fa.seedUser("myapp-media")
	if err := fa.CreateAccessKey(context.Background(), "myapp-media", "AKIA123", "secret"); err != nil {
		t.Fatalf("seed key: %v", err)
	}
	r := &S3CredentialsReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)

	// The S3Identity that resolved "myapp" to "myapp-media" is already gone;
	// status carries the resolved name.
	if err := cli.Delete(context.Background(), cred); err != nil {
		t.Fatalf("delete: %v", err)
	}
	reconcileOnce(t, r, types.NamespacedName{Namespace: "media", Name: "myapp-creds"})

	user, err := fa.GetUser(context.Background(), "myapp-media")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if len(user.AccessKeys) != 0 {
		t.Errorf("access keys = %v, want the provisioned key removed", user.AccessKeys)
	}
}

func TestS3PolicyBinding_RefsResolveResourceNames(t *testing.T) {
	scheme := iamTestScheme(t)
	id := identityAt("media", "myapp", time.Hour)
	id.Spec.Name = "myapp-media"
	pol := policyAt("media", "rw", time.Hour, `{"Version":"2012-10-17","Statement":[]}`)
	pol.Spec.Name = "rw-media"
	binding := &seaweedv1.S3PolicyBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "myapp-rw", Namespace: "media"},
		Spec: seaweedv1.S3PolicyBindingSpec{
			SeaweedRef: iamSeaweedRef(),
			PolicyRef:  seaweedv1.S3PolicyRef{Name: "rw"},
			Subjects:   []seaweedv1.S3Subject{{Kind: seaweedv1.S3SubjectKindIdentity, Name: "myapp"}},
		},
	}
	cli := iamTestClient(t, scheme, newTestSeaweed(), id, pol, binding)
	fa := newFakeIAMAdmin()
	fa.seedUser("myapp-media")
	fa.policies["rw-media"] = pol.Spec.PolicyDocument
	r := &S3PolicyBindingReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)

	key := types.NamespacedName{Namespace: "media", Name: "myapp-rw"}
	reconcileStable(t, r, key, 5)

	var got seaweedv1.S3PolicyBinding
	if err := cli.Get(context.Background(), key, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Phase != seaweedv1.S3PhaseReady {
		t.Fatalf("phase = %q, want Ready", got.Status.Phase)
	}
	if got.Status.PolicyName != "rw-media" {
		t.Errorf("status.policyName = %q, want rw-media", got.Status.PolicyName)
	}
	if len(got.Status.AttachedSubjects) != 1 || got.Status.AttachedSubjects[0] != "myapp-media" {
		t.Errorf("attachedSubjects = %v, want [myapp-media]", got.Status.AttachedSubjects)
	}
	user, err := fa.GetUser(context.Background(), "myapp-media")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if len(user.PolicyNames) != 1 || user.PolicyNames[0] != "rw-media" {
		t.Errorf("policies on myapp-media = %v, want [rw-media]", user.PolicyNames)
	}
}

func TestS3Policy_ConflictLoserDelete_LeavesPolicyIntact(t *testing.T) {
	scheme := iamTestScheme(t)
	older := policyAt("media", "rw", 2*time.Hour, `{"Version":"2012-10-17","Statement":[]}`)
	newer := policyAt("staging", "rw", time.Hour, `{"Version":"2012-10-17","Statement":[]}`)
	newer.Finalizers = []string{s3PolicyFinalizer}
	cli := iamTestClient(t, scheme, newTestSeaweed(), stagingRefGrant(), older, newer)
	fa := newFakeIAMAdmin()
	fa.policies["rw"] = older.Spec.PolicyDocument
	r := &S3PolicyReconciler{Client: cli, Log: logf.FromContext(context.Background()), Scheme: scheme}
	r.AdminFactory = fakeIAMFactory(fa)

	if err := cli.Delete(context.Background(), newer); err != nil {
		t.Fatalf("delete: %v", err)
	}
	reconcileOnce(t, r, types.NamespacedName{Namespace: "staging", Name: "rw"})

	if _, err := fa.GetPolicy(context.Background(), "rw"); err != nil {
		t.Fatalf("policy rw should survive deletion of the conflicting CR, got %v", err)
	}
}
