package swadmin

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/seaweedfs/seaweedfs/weed/pb/filer_pb"
	"github.com/seaweedfs/seaweedfs/weed/pb/master_pb"
)

// TestNewSeaweedAdmin_DoesNotPanic guards against regressions like
// https://github.com/seaweedfs/seaweedfs-operator/issues/233, where leaving
// shell.ShellOptions.FilerGroup nil panicked inside shell.NewCommandEnv the
// moment any Bucket reached the reconciler.
func TestNewSeaweedAdmin_DoesNotPanic(t *testing.T) {
	sa := NewSeaweedAdmin("seaweed-master.invalid:9333", "seaweed-filer.invalid:8888", nil, io.Discard)
	t.Cleanup(func() { _ = sa.Close() })
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
	sa := NewSeaweedAdmin("seaweed-master.invalid:9333", "seaweed-filer.invalid:8888", nil, io.Discard)
	t.Cleanup(func() { _ = sa.Close() })
	err := sa.commandEnv.WithFilerClient(false, func(filer_pb.SeaweedFilerClient) error {
		return nil
	})
	if err != nil && strings.Contains(err.Error(), "received empty target") {
		t.Fatalf("regression of issue #237: empty target leaked through despite non-empty filer arg: %v", err)
	}
}

func TestVolumeServerVolumeCounts_Aggregation(t *testing.T) {
	topo := &master_pb.TopologyInfo{
		DataCenterInfos: []*master_pb.DataCenterInfo{{
			RackInfos: []*master_pb.RackInfo{{
				DataNodeInfos: []*master_pb.DataNodeInfo{
					{
						// Two disks, volumes plus an EC shard => counts sum across disks.
						Id: "vol-0:8444",
						DiskInfos: map[string]*master_pb.DiskInfo{
							"hdd": {
								VolumeInfos:  []*master_pb.VolumeInformationMessage{{Id: 1}, {Id: 2}},
								EcShardInfos: []*master_pb.VolumeEcShardInformationMessage{{Id: 3}},
							},
							"ssd": {
								VolumeInfos: []*master_pb.VolumeInformationMessage{{Id: 4}},
							},
						},
					},
					{
						// Registered but empty => present with count 0.
						Id:        "vol-1:8444",
						DiskInfos: map[string]*master_pb.DiskInfo{"hdd": {}},
					},
				},
			}},
		}},
	}

	got := volumeServerVolumeCounts(topo)
	want := map[string]int{"vol-0:8444": 4, "vol-1:8444": 0}
	if len(got) != len(want) {
		t.Fatalf("counts = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("counts[%q] = %d, want %d", k, got[k], v)
		}
	}
}

func TestVolumeServerVolumeCounts_EmptyTopology(t *testing.T) {
	if got := volumeServerVolumeCounts(nil); len(got) != 0 {
		t.Fatalf("counts for nil topology = %v, want empty", got)
	}
}

func TestSeaweedAdmin_ProcessCommand_CanceledWhileWaiting(t *testing.T) {
	sa := NewSeaweedAdmin("seaweed-master.invalid:9333", "", nil, io.Discard)
	t.Cleanup(func() { _ = sa.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err := sa.ProcessCommand(ctx, "volume.list")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ProcessCommand error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("canceled master wait took %v", elapsed)
	}
}
