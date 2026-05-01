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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func TestParseCollectionListOutput(t *testing.T) {
	out := strings.Join([]string{
		`collection:"photos"	volumeCount:3	size:107374182400	fileCount:12483	deletedBytes:0	deletion:0`,
		`collection:"docs"	volumeCount:1	size:524288	fileCount:42	deletedBytes:0	deletion:0`,
		`collection:"empty"	volumeCount:1	size:0	fileCount:0	deletedBytes:0	deletion:0`,
		`Total 3 collections.`,
	}, "\n")
	got := parseCollectionListOutput(out)

	want := map[string]BucketCollectionStats{
		"photos": {FileCount: 12483, SizeBytes: 107374182400},
		"docs":   {FileCount: 42, SizeBytes: 524288},
		"empty":  {FileCount: 0, SizeBytes: 0},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %#v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("entry %q = %+v, want %+v", k, got[k], v)
		}
	}
}

func TestParseCollectionListOutput_IgnoresMalformedLines(t *testing.T) {
	out := strings.Join([]string{
		`some unrelated banner`,
		`collection:"photos"	volumeCount:3	size:notanumber	fileCount:42	deletedBytes:0	deletion:0`,
		`collection:"good"	volumeCount:1	size:100	fileCount:5	deletedBytes:0	deletion:0`,
	}, "\n")
	got := parseCollectionListOutput(out)
	if _, ok := got["photos"]; ok {
		t.Errorf("expected malformed photos line to be skipped; got %+v", got["photos"])
	}
	if got["good"] != (BucketCollectionStats{FileCount: 5, SizeBytes: 100}) {
		t.Errorf("good entry = %+v", got["good"])
	}
}

// Locks in the regex's tolerance for added fields between size/fileCount —
// the parser must keep matching if a future SeaweedFS release inserts new
// metadata columns into `collection.list` output.
func TestParseCollectionListOutput_ToleratesNewFields(t *testing.T) {
	out := `collection:"photos"	volumeCount:3	newFieldA:42	size:107374182400	newFieldB:abc	fileCount:12483	deletedBytes:0	deletion:0`
	got := parseCollectionListOutput(out)
	want := BucketCollectionStats{FileCount: 12483, SizeBytes: 107374182400}
	if got["photos"] != want {
		t.Errorf("got %+v want %+v", got["photos"], want)
	}
}

func TestUsageEqual(t *testing.T) {
	a := &seaweedv1.BucketUsage{ObjectCount: 1, SizeBytes: 2}
	b := &seaweedv1.BucketUsage{ObjectCount: 1, SizeBytes: 2}
	if !usageEqual(a, b) {
		t.Errorf("identical usage should be equal")
	}
	c := &seaweedv1.BucketUsage{ObjectCount: 1, SizeBytes: 3}
	if usageEqual(a, c) {
		t.Errorf("differing SizeBytes should not be equal")
	}
	if !usageEqual(nil, nil) {
		t.Errorf("nil/nil should be equal")
	}
	if usageEqual(nil, a) {
		t.Errorf("nil/non-nil should not be equal")
	}
}

func TestRefreshAllUsage_PopulatesStatus(t *testing.T) {
	bucket := newTestBucket("photos")
	bucket.Status.BucketName = "photos"
	bucket.Finalizers = []string{BucketFinalizer}

	fa := newFakeAdmin()
	fa.collectionStats = map[string]BucketCollectionStats{
		"photos": {FileCount: 100, SizeBytes: 1024 * 1024 * 1024},
	}
	r, cli := testReconciler(t, fa, newTestSeaweed(), bucket)

	r.refreshAllUsage(context.Background(), logf.FromContext(context.Background()))

	got := &seaweedv1.Bucket{}
	if err := cli.Get(context.Background(), types.NamespacedName{Namespace: bucket.Namespace, Name: bucket.Name}, got); err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	if got.Status.Usage == nil {
		t.Fatalf("expected Status.Usage to be populated")
	}
	if got.Status.Usage.ObjectCount != 100 {
		t.Errorf("ObjectCount=%d want 100", got.Status.Usage.ObjectCount)
	}
	if got.Status.Usage.SizeBytes != 1024*1024*1024 {
		t.Errorf("SizeBytes=%d want %d", got.Status.Usage.SizeBytes, 1024*1024*1024)
	}
	if got.Status.Usage.LastUpdated == nil {
		t.Errorf("LastUpdated should be set")
	}

	gotCalls := strings.Join(fa.calls, "\n")
	if !strings.Contains(gotCalls, "ListCollectionStats") {
		t.Errorf("expected ListCollectionStats call; got %v", fa.calls)
	}
}

func TestRefreshAllUsage_SkipsBucketsWithoutStatusName(t *testing.T) {
	pending := newTestBucket("pending")
	// Status.BucketName intentionally empty — bucket not yet provisioned
	// by the main reconcile loop.
	pending.Finalizers = []string{BucketFinalizer}

	fa := newFakeAdmin()
	fa.collectionStats = map[string]BucketCollectionStats{
		"pending": {FileCount: 99, SizeBytes: 99}, // present on filer
	}
	r, cli := testReconciler(t, fa, newTestSeaweed(), pending)

	r.refreshAllUsage(context.Background(), logf.FromContext(context.Background()))

	got := &seaweedv1.Bucket{}
	if err := cli.Get(context.Background(), types.NamespacedName{Namespace: pending.Namespace, Name: pending.Name}, got); err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	if got.Status.Usage != nil {
		t.Errorf("expected Status.Usage to remain nil for unprovisioned bucket; got %+v", got.Status.Usage)
	}
	for _, c := range fa.calls {
		if c == "ListCollectionStats" {
			t.Errorf("expected zero ListCollectionStats calls when no buckets are eligible; got %v", fa.calls)
		}
	}
}

func TestRefreshAllUsage_NoStatsForBucketYieldsZero(t *testing.T) {
	bucket := newTestBucket("photos")
	bucket.Status.BucketName = "photos"
	bucket.Finalizers = []string{BucketFinalizer}

	fa := newFakeAdmin()
	fa.collectionStats = map[string]BucketCollectionStats{
		// "photos" intentionally absent — bucket exists but is empty.
		"unrelated": {FileCount: 1, SizeBytes: 1},
	}
	r, cli := testReconciler(t, fa, newTestSeaweed(), bucket)

	r.refreshAllUsage(context.Background(), logf.FromContext(context.Background()))

	got := &seaweedv1.Bucket{}
	if err := cli.Get(context.Background(), types.NamespacedName{Namespace: bucket.Namespace, Name: bucket.Name}, got); err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	if got.Status.Usage == nil || got.Status.Usage.ObjectCount != 0 || got.Status.Usage.SizeBytes != 0 {
		t.Errorf("expected zero usage for empty bucket, got %+v", got.Status.Usage)
	}
}

func TestRefreshAllUsage_SkipsAPIWriteWhenUsageUnchanged(t *testing.T) {
	bucket := newTestBucket("photos")
	bucket.Status.BucketName = "photos"
	bucket.Finalizers = []string{BucketFinalizer}

	// Use an unmistakably-old LastUpdated so we can tell whether the
	// refresh re-wrote it. metav1.Time round-trips at second granularity
	// in the fake client, so pick a fixed second past.
	originalUpdate := metav1.NewTime(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	bucket.Status.Usage = &seaweedv1.BucketUsage{
		ObjectCount: 100,
		SizeBytes:   1024,
		LastUpdated: &originalUpdate,
	}

	fa := newFakeAdmin()
	fa.collectionStats = map[string]BucketCollectionStats{
		"photos": {FileCount: 100, SizeBytes: 1024},
	}
	r, cli := testReconciler(t, fa, newTestSeaweed(), bucket)

	r.refreshAllUsage(context.Background(), logf.FromContext(context.Background()))

	got := &seaweedv1.Bucket{}
	if err := cli.Get(context.Background(), types.NamespacedName{Namespace: bucket.Namespace, Name: bucket.Name}, got); err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	if got.Status.Usage.LastUpdated == nil || !got.Status.Usage.LastUpdated.Time.Equal(originalUpdate.Time) {
		t.Errorf("LastUpdated bumped despite identical counts: was=%v now=%v",
			originalUpdate.Time, got.Status.Usage.LastUpdated)
	}
}
