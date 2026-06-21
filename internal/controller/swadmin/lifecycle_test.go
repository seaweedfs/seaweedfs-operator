package swadmin

import (
	"sort"
	"testing"
)

func TestStaleTTLPrefixes(t *testing.T) {
	ttls := map[string]string{
		"/buckets/photos/archived": "90d", // day TTL under the bucket -> stale
		"/buckets/photos/logs":     "30d", // day TTL under the bucket -> stale
		"/buckets/photos/hot":      "1h",  // hour TTL -> not a legacy lifecycle entry
		"/buckets/photos/none":     "",    // no TTL -> keep
		"/buckets/other/x":         "30d", // different bucket -> keep
	}
	got := staleTTLPrefixes(ttls, "/buckets/photos/")
	sort.Strings(got)
	want := []string{"/buckets/photos/archived", "/buckets/photos/logs"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
