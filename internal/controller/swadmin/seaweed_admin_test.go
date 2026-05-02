package swadmin

import (
	"io"
	"testing"
)

// TestNewSeaweedAdmin_DoesNotPanic guards against regressions like
// https://github.com/seaweedfs/seaweedfs-operator/issues/233, where leaving
// shell.ShellOptions.FilerGroup nil panicked inside shell.NewCommandEnv the
// moment any Bucket reached the reconciler.
func TestNewSeaweedAdmin_DoesNotPanic(t *testing.T) {
	sa := NewSeaweedAdmin("seaweed-master.invalid:9333", io.Discard)
	if sa == nil {
		t.Fatal("NewSeaweedAdmin returned nil")
	}
	if sa.commandEnv == nil {
		t.Fatal("commandEnv was not initialized")
	}
}
