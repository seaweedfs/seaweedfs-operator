package swadmin

import "testing"

func TestDefaultRetentionUpdate(t *testing.T) {
	str := func(m map[string][]byte, k string) string { return string(m[k]) }

	ext, changed := defaultRetentionUpdate(nil, "GOVERNANCE", 30, 0)
	if !changed {
		t.Fatal("set on empty should report changed")
	}
	if str(ext, objectLockDefaultModeKey) != "GOVERNANCE" || str(ext, objectLockDefaultDaysKey) != "30" {
		t.Fatalf("got %v", ext)
	}
	if _, ok := ext[objectLockDefaultYearsKey]; ok {
		t.Fatal("years key must stay absent when days is set")
	}

	if _, changed = defaultRetentionUpdate(ext, "GOVERNANCE", 30, 0); changed {
		t.Fatal("reapplying the same rule must be a no-op")
	}

	ext, changed = defaultRetentionUpdate(ext, "COMPLIANCE", 0, 2)
	if !changed {
		t.Fatal("mode/period change should report changed")
	}
	if str(ext, objectLockDefaultModeKey) != "COMPLIANCE" || str(ext, objectLockDefaultYearsKey) != "2" {
		t.Fatalf("got %v", ext)
	}
	if _, ok := ext[objectLockDefaultDaysKey]; ok {
		t.Fatal("days key must be dropped when switching to years")
	}

	ext, changed = defaultRetentionUpdate(ext, "", 0, 0)
	if !changed {
		t.Fatal("clear should report changed")
	}
	for _, k := range []string{objectLockDefaultModeKey, objectLockDefaultDaysKey, objectLockDefaultYearsKey} {
		if _, ok := ext[k]; ok {
			t.Fatalf("key %s still present after clear: %v", k, ext)
		}
	}

	if _, changed = defaultRetentionUpdate(ext, "", 0, 0); changed {
		t.Fatal("clearing an already-empty rule must be a no-op")
	}
}
