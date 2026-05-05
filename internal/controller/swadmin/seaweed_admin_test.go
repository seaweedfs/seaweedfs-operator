package swadmin

import (
	"io"
	"strings"
	"testing"

	"github.com/seaweedfs/seaweedfs/weed/pb/filer_pb"
)

// TestNewSeaweedAdmin_DoesNotPanic guards against regressions like
// https://github.com/seaweedfs/seaweedfs-operator/issues/233, where leaving
// shell.ShellOptions.FilerGroup nil panicked inside shell.NewCommandEnv the
// moment any Bucket reached the reconciler.
func TestNewSeaweedAdmin_DoesNotPanic(t *testing.T) {
	sa := NewSeaweedAdmin("seaweed-master.invalid:9333", "seaweed-filer.invalid:8888", io.Discard)
	if sa == nil {
		t.Fatal("NewSeaweedAdmin returned nil")
	}
	if sa.commandEnv == nil {
		t.Fatal("commandEnv was not initialized")
	}
}

// TestNewSeaweedAdmin_FilerAddressWired guards against regressing
// https://github.com/seaweedfs/seaweedfs-operator/issues/237, where
// ShellOptions.FilerAddress was never set so every s3.bucket.* command
// dialed gRPC with an empty target.
func TestNewSeaweedAdmin_FilerAddressWired(t *testing.T) {
	sa := NewSeaweedAdmin("seaweed-master.invalid:9333", "seaweed-filer.invalid:8888", io.Discard)
	err := sa.commandEnv.WithFilerClient(false, func(filer_pb.SeaweedFilerClient) error {
		return nil
	})
	if err != nil && strings.Contains(err.Error(), "received empty target") {
		t.Fatalf("regression of issue #237: empty target leaked through despite non-empty filer arg: %v", err)
	}
}
