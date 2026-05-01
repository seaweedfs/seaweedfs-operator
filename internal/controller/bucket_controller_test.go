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
	"sort"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// fakeBucketAdmin records calls in order and returns configurable per-method
// errors. Default behavior treats "BucketExists" as false and every other
// call as success.
type fakeBucketAdmin struct {
	calls []string

	existsResp    map[string]bool
	createErr     error
	deleteErr     error
	versioningErr error
	lockErr       error
	quotaErr      error
	ownerErr      error
	accessErr     error
	configureErr  error

	collectionStats map[string]BucketCollectionStats
	collectionErr   error
}

func newFakeAdmin() *fakeBucketAdmin {
	return &fakeBucketAdmin{existsResp: map[string]bool{}}
}

func (f *fakeBucketAdmin) record(call string) { f.calls = append(f.calls, call) }

func (f *fakeBucketAdmin) BucketExists(_ context.Context, name string) (bool, error) {
	f.record("Exists:" + name)
	return f.existsResp[name], nil
}
func (f *fakeBucketAdmin) CreateBucket(_ context.Context, name, owner string, withLock bool) error {
	f.record("Create:" + name + ":owner=" + owner + ":lock=" + boolStr(withLock))
	if f.createErr == nil {
		f.existsResp[name] = true
	}
	return f.createErr
}
func (f *fakeBucketAdmin) DeleteBucket(_ context.Context, name string) error {
	f.record("Delete:" + name)
	if f.deleteErr == nil {
		delete(f.existsResp, name)
	}
	return f.deleteErr
}
func (f *fakeBucketAdmin) SetVersioning(_ context.Context, name, status string) error {
	f.record("Versioning:" + name + ":" + status)
	return f.versioningErr
}
func (f *fakeBucketAdmin) EnableObjectLock(_ context.Context, name string) error {
	f.record("Lock:" + name)
	return f.lockErr
}
func (f *fakeBucketAdmin) SetQuota(_ context.Context, name string, sizeMiB int64, enforce bool) error {
	f.record("Quota:" + name + ":" + intStr(sizeMiB) + ":enforce=" + boolStr(enforce))
	return f.quotaErr
}
func (f *fakeBucketAdmin) RemoveQuota(_ context.Context, name string) error {
	f.record("RemoveQuota:" + name)
	return f.quotaErr
}
func (f *fakeBucketAdmin) SetOwner(_ context.Context, name, owner string) error {
	f.record("Owner:" + name + ":" + owner)
	return f.ownerErr
}
func (f *fakeBucketAdmin) RemoveOwner(_ context.Context, name string) error {
	f.record("RemoveOwner:" + name)
	return f.ownerErr
}
func (f *fakeBucketAdmin) SetAccess(_ context.Context, name, user, actions string) error {
	f.record("Access:" + name + ":" + user + ":" + actions)
	return f.accessErr
}
func (f *fakeBucketAdmin) Configure(_ context.Context, prefix string, args []string) error {
	f.record("Configure:" + prefix + ":" + strings.Join(args, ","))
	return f.configureErr
}
func (f *fakeBucketAdmin) ListCollectionStats(_ context.Context) (map[string]BucketCollectionStats, error) {
	f.record("ListCollectionStats")
	if f.collectionErr != nil {
		return nil, f.collectionErr
	}
	return f.collectionStats, nil
}

func boolStr(b bool) string {
	if b {
		return "t"
	}
	return "f"
}
func intStr(i int64) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = digits[i%10]
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// testReconciler builds a BucketReconciler whose AdminFactory always returns
// the supplied fake. The fake client is pre-loaded with the given objects
// and the bucket status subresource is registered.
func testReconciler(t *testing.T, fa *fakeBucketAdmin, objs ...client.Object) (*BucketReconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("clientgoscheme: %v", err)
	}
	if err := seaweedv1.AddToScheme(scheme); err != nil {
		t.Fatalf("seaweedv1: %v", err)
	}
	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&seaweedv1.Bucket{}).
		Build()
	r := &BucketReconciler{
		Client: cli,
		Log:    logf.FromContext(context.Background()),
		Scheme: scheme,
		AdminFactory: func(_ string, _ logr.Logger) (BucketAdmin, error) {
			return fa, nil
		},
	}
	return r, cli
}

// reconcileUntilStable hammers Reconcile until the returned Result is
// neither Requeue:true nor Err. Returns an error if the loop doesn't
// converge in maxSteps iterations.
func reconcileUntilStable(t *testing.T, r *BucketReconciler, key types.NamespacedName, maxSteps int) {
	t.Helper()
	for i := 0; i < maxSteps; i++ {
		res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
		if err != nil {
			t.Fatalf("reconcile step %d: %v", i, err)
		}
		if !res.Requeue && res.RequeueAfter == 0 {
			return
		}
	}
	t.Fatalf("reconcile did not converge after %d steps", maxSteps)
}

func newTestSeaweed() *seaweedv1.Seaweed {
	return &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "seaweedfs"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 3},
		},
	}
}

func newTestBucket(name string) *seaweedv1.Bucket {
	return &seaweedv1.Bucket{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "media",
		},
		Spec: seaweedv1.BucketSpec{
			ClusterRef: seaweedv1.BucketClusterRef{
				Name:      "prod",
				Namespace: "seaweedfs",
			},
			ReclaimPolicy: seaweedv1.BucketReclaimRetain,
			Versioning:    seaweedv1.VersioningOff,
		},
	}
}

func TestReconcile_HappyPath_AddsFinalizerThenCreates(t *testing.T) {
	bucket := newTestBucket("photos")
	bucket.Spec.Versioning = seaweedv1.VersioningEnabled
	bucket.Spec.Owner = "media-team"
	bucket.Spec.Quota = &seaweedv1.BucketQuota{
		Size:    resource.MustParse("100Gi"),
		Enforce: true,
	}
	bucket.Spec.Access = []seaweedv1.BucketAccessGrant{
		{User: "uploader", Actions: []seaweedv1.BucketAccessAction{seaweedv1.BucketAccessRead, seaweedv1.BucketAccessWrite}},
	}
	bucket.Spec.Placement = &seaweedv1.BucketPlacement{
		Replication: "001",
		DiskType:    "ssd",
	}

	fa := newFakeAdmin()
	r, cli := testReconciler(t, fa, newTestSeaweed(), bucket)

	key := types.NamespacedName{Namespace: bucket.Namespace, Name: bucket.Name}
	reconcileUntilStable(t, r, key, 5)

	got := &seaweedv1.Bucket{}
	if err := cli.Get(context.Background(), key, got); err != nil {
		t.Fatalf("get bucket: %v", err)
	}

	if !controllerutil.ContainsFinalizer(got, BucketFinalizer) {
		t.Errorf("expected finalizer to be set")
	}
	if got.Status.Phase != seaweedv1.BucketPhaseReady {
		t.Errorf("phase=%q want Ready; conditions=%+v", got.Status.Phase, got.Status.Conditions)
	}
	if got.Status.BucketName != "photos" {
		t.Errorf("status.bucketName=%q want photos", got.Status.BucketName)
	}
	if got.Status.OwnerIdentity != "media-team" {
		t.Errorf("status.owner=%q", got.Status.OwnerIdentity)
	}
	if got.Status.Quota == nil || got.Status.Quota.SizeBytes != 100*1024*1024*1024 {
		t.Errorf("status.quota.sizeBytes=%v want %d", got.Status.Quota, 100*1024*1024*1024)
	}

	// Sanity-check the call sequence — order matters for create-then-config.
	gotCalls := strings.Join(fa.calls, "\n")
	for _, want := range []string{
		"Exists:photos",
		"Create:photos:owner=media-team:lock=f",
		"Versioning:photos:Enabled",
		"Quota:photos:102400:enforce=t",
		"Owner:photos:media-team",
		"Access:photos:uploader:Read,Write",
		"Configure:/buckets/photos/:-replication=001,-disk=ssd",
	} {
		if !strings.Contains(gotCalls, want) {
			t.Errorf("missing expected call %q\nactual calls:\n%s", want, gotCalls)
		}
	}
}

func TestReconcile_ExistingBucketRefusesAdoption(t *testing.T) {
	bucket := newTestBucket("photos")
	fa := newFakeAdmin()
	fa.existsResp["photos"] = true // pre-existing on the filer
	r, cli := testReconciler(t, fa, newTestSeaweed(), bucket)

	key := types.NamespacedName{Namespace: bucket.Namespace, Name: bucket.Name}
	// First reconcile adds finalizer; second sees the pre-existing bucket
	// and refuses adoption.
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	// failPhase returns an error so the manager requeues. Allow either
	// non-zero RequeueAfter or non-nil err (both happen here).
	if err == nil && res.RequeueAfter == 0 {
		t.Fatalf("expected adoption refusal, got success")
	}

	got := &seaweedv1.Bucket{}
	if err := cli.Get(context.Background(), key, got); err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	if got.Status.Phase != seaweedv1.BucketPhaseFailed {
		t.Errorf("phase=%q want Failed", got.Status.Phase)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, seaweedv1.BucketConditionBucketAlreadyExists)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Errorf("expected BucketAlreadyExists condition true, got %+v", cond)
	}
	// Adoption refusal should NOT have called Create.
	for _, c := range fa.calls {
		if strings.HasPrefix(c, "Create:") {
			t.Errorf("unexpected Create call: %s", c)
		}
	}
}

func TestReconcile_RenameRefused(t *testing.T) {
	bucket := newTestBucket("photos")
	bucket.Spec.Name = "renamed"
	bucket.Status.BucketName = "photos"
	bucket.Finalizers = []string{BucketFinalizer}

	fa := newFakeAdmin()
	r, cli := testReconciler(t, fa, newTestSeaweed(), bucket)
	key := types.NamespacedName{Namespace: bucket.Namespace, Name: bucket.Name}

	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	if err == nil && res.RequeueAfter == 0 {
		t.Fatalf("expected rename to fail")
	}

	got := &seaweedv1.Bucket{}
	if err := cli.Get(context.Background(), key, got); err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, seaweedv1.BucketConditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "BucketRenameNotSupported" {
		t.Errorf("expected Ready=False reason=BucketRenameNotSupported, got %+v", cond)
	}
}

func TestReconcile_ClusterRefMissingRequeues(t *testing.T) {
	bucket := newTestBucket("photos")
	bucket.Spec.ClusterRef.Name = "missing"

	fa := newFakeAdmin()
	r, cli := testReconciler(t, fa, bucket) // no Seaweed CR
	key := types.NamespacedName{Namespace: bucket.Namespace, Name: bucket.Name}

	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("expected RequeueAfter > 0 for missing clusterRef")
	}

	got := &seaweedv1.Bucket{}
	if err := cli.Get(context.Background(), key, got); err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, seaweedv1.BucketConditionClusterReachable)
	if cond == nil || cond.Status != metav1.ConditionFalse {
		t.Errorf("expected ClusterReachable=False, got %+v", cond)
	}
}

func TestReconcile_RetainOnDelete_RemovesFinalizer(t *testing.T) {
	now := metav1.Now()
	bucket := newTestBucket("photos")
	bucket.Spec.ReclaimPolicy = seaweedv1.BucketReclaimRetain
	bucket.Status.BucketName = "photos"
	bucket.Finalizers = []string{BucketFinalizer}
	bucket.DeletionTimestamp = &now

	fa := newFakeAdmin()
	fa.existsResp["photos"] = true
	r, cli := testReconciler(t, fa, newTestSeaweed(), bucket)
	key := types.NamespacedName{Namespace: bucket.Namespace, Name: bucket.Name}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// Bucket should have been deleted from the API (finalizer gone, no
	// other holders) — fake client immediately reaps.
	got := &seaweedv1.Bucket{}
	err := cli.Get(context.Background(), key, got)
	if err == nil {
		t.Errorf("expected bucket to be deleted after finalizer removed; still present: %+v", got)
	} else if !apierrors.IsNotFound(err) {
		t.Errorf("unexpected error getting deleted bucket: %v", err)
	}

	for _, c := range fa.calls {
		if strings.HasPrefix(c, "Delete:") {
			t.Errorf("Retain policy must not call DeleteBucket, got: %s", c)
		}
	}
}

func TestReconcile_DeleteOnDelete_CallsDeleteBucket(t *testing.T) {
	now := metav1.Now()
	bucket := newTestBucket("photos")
	bucket.Spec.ReclaimPolicy = seaweedv1.BucketReclaimDelete
	bucket.Status.BucketName = "photos"
	bucket.Finalizers = []string{BucketFinalizer}
	bucket.DeletionTimestamp = &now

	fa := newFakeAdmin()
	fa.existsResp["photos"] = true
	r, _ := testReconciler(t, fa, newTestSeaweed(), bucket)
	key := types.NamespacedName{Namespace: bucket.Namespace, Name: bucket.Name}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, ok := fa.existsResp["photos"]; ok {
		t.Errorf("expected bucket to be deleted from fake admin; still present")
	}
	found := false
	for _, c := range fa.calls {
		if c == "Delete:photos" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected Delete:photos call; got %v", fa.calls)
	}
}

func TestReconcile_DeleteBlockedByRetention_KeepsFinalizer(t *testing.T) {
	now := metav1.Now()
	bucket := newTestBucket("photos")
	bucket.Spec.ReclaimPolicy = seaweedv1.BucketReclaimDelete
	bucket.Status.BucketName = "photos"
	bucket.Finalizers = []string{BucketFinalizer}
	bucket.DeletionTimestamp = &now

	fa := newFakeAdmin()
	fa.existsResp["photos"] = true
	fa.deleteErr = ErrRetentionBlocksDelete
	r, cli := testReconciler(t, fa, newTestSeaweed(), bucket)
	key := types.NamespacedName{Namespace: bucket.Namespace, Name: bucket.Name}

	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("expected RequeueAfter > 0 when retention blocks deletion")
	}

	got := &seaweedv1.Bucket{}
	if err := cli.Get(context.Background(), key, got); err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	if !controllerutil.ContainsFinalizer(got, BucketFinalizer) {
		t.Errorf("finalizer must NOT be removed while retention blocks delete")
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, seaweedv1.BucketConditionDeleteBlockedByRetention)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Errorf("expected DeleteBlockedByRetention=True, got %+v", cond)
	}
}

func TestReconcile_AccessRevokesRemovedUsers(t *testing.T) {
	bucket := newTestBucket("photos")
	// Existing applied list has 3 users; spec keeps only 1.
	if bucket.Annotations == nil {
		bucket.Annotations = map[string]string{}
	}
	bucket.Annotations[AnnotationAppliedAccess] = "alice,bob,carol"
	bucket.Status.BucketName = "photos"
	bucket.Finalizers = []string{BucketFinalizer}
	bucket.Spec.Access = []seaweedv1.BucketAccessGrant{
		{User: "alice", Actions: []seaweedv1.BucketAccessAction{seaweedv1.BucketAccessRead}},
	}

	fa := newFakeAdmin()
	fa.existsResp["photos"] = true
	r, cli := testReconciler(t, fa, newTestSeaweed(), bucket)
	key := types.NamespacedName{Namespace: bucket.Namespace, Name: bucket.Name}

	reconcileUntilStable(t, r, key, 5)

	// alice keeps Read; bob and carol get revoked to none.
	wantSet := map[string]bool{
		"Access:photos:alice:Read": false,
		"Access:photos:bob:none":   false,
		"Access:photos:carol:none": false,
	}
	for _, c := range fa.calls {
		if _, ok := wantSet[c]; ok {
			wantSet[c] = true
		}
	}
	for k, seen := range wantSet {
		if !seen {
			t.Errorf("missing access call %q; calls=%v", k, fa.calls)
		}
	}

	got := &seaweedv1.Bucket{}
	if err := cli.Get(context.Background(), key, got); err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	if got.Annotations[AnnotationAppliedAccess] != "alice" {
		t.Errorf("applied-access annotation = %q want %q",
			got.Annotations[AnnotationAppliedAccess], "alice")
	}
}

func TestQuantityToMiB(t *testing.T) {
	cases := map[string]struct {
		q       string
		want    int64
		wantErr bool
	}{
		"100Gi exactly":   {"100Gi", 100 * 1024, false},
		"1Mi exactly":     {"1Mi", 1, false},
		"512Ki rounds up": {"512Ki", 1, false},
		"zero":            {"0", 0, false},
		"negative":        {"-1", 0, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			q := resource.MustParse(tc.q)
			got, err := quantityToMiB(q)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestJoinActions(t *testing.T) {
	cases := map[string]struct {
		in   []seaweedv1.BucketAccessAction
		want string
	}{
		"empty":  {nil, "none"},
		"single": {[]seaweedv1.BucketAccessAction{seaweedv1.BucketAccessRead}, "Read"},
		"sorted": {[]seaweedv1.BucketAccessAction{seaweedv1.BucketAccessWrite, seaweedv1.BucketAccessRead}, "Read,Write"},
		"all":    {[]seaweedv1.BucketAccessAction{seaweedv1.BucketAccessAdmin, seaweedv1.BucketAccessRead, seaweedv1.BucketAccessTagging, seaweedv1.BucketAccessList, seaweedv1.BucketAccessWrite}, "Admin,List,Read,Tagging,Write"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := joinActions(tc.in); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestPlacementArgs(t *testing.T) {
	cases := map[string]struct {
		in   *seaweedv1.BucketPlacement
		want []string
	}{
		"nil":   {nil, nil},
		"empty": {&seaweedv1.BucketPlacement{}, nil},
		"full": {
			&seaweedv1.BucketPlacement{
				Replication:       "001",
				DiskType:          "ssd",
				TTL:               "30d",
				Fsync:             true,
				WORM:              true,
				ReadOnly:          true,
				DataCenter:        "dc1",
				Rack:              "rack-a",
				DataNode:          "node1",
				VolumeGrowthCount: ptrInt32(4),
			},
			[]string{
				"-replication=001",
				"-disk=ssd",
				"-ttl=30d",
				"-fsync=true",
				"-worm=true",
				"-readOnly=true",
				"-dataCenter=dc1",
				"-rack=rack-a",
				"-dataNode=node1",
				"-volumeGrowthCount=4",
			},
		},
		"explicit volumeGrowthCount zero is emitted": {
			&seaweedv1.BucketPlacement{VolumeGrowthCount: ptrInt32(0)},
			[]string{"-volumeGrowthCount=0"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := placementArgs(tc.in)
			if !equalStringSlices(got, tc.want) {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func ptrInt32(v int32) *int32 { return &v }

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := append([]string(nil), a...)
	bc := append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}

func TestReadAppliedAccessAnnotation(t *testing.T) {
	cases := map[string]struct {
		ann  string
		want []string
	}{
		"missing":       {"", nil},
		"single":        {"alice", []string{"alice"}},
		"multi":         {"alice,bob,carol", []string{"alice", "bob", "carol"}},
		"with spaces":   {" alice , bob ", []string{"alice", "bob"}},
		"empty entries": {"alice,,bob,", []string{"alice", "bob"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			b := &seaweedv1.Bucket{}
			if tc.ann != "" {
				b.Annotations = map[string]string{AnnotationAppliedAccess: tc.ann}
			}
			got := readAppliedAccessAnnotation(b)
			if !equalStringSlices(got, tc.want) {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}
