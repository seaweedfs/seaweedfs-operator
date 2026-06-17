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
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/grpc"

	"github.com/seaweedfs/seaweedfs-operator/internal/controller/swadmin"
)

// VolumeAdmin is the small surface the volume reconciler uses to drain a volume
// server before a scale-down removes its pod. The default implementation drives
// `weed shell` through swadmin.SeaweedAdmin; tests inject a fake.
type VolumeAdmin interface {
	// VolumeServerVolumeCounts returns, per volume-server node id
	// (<host>:<port>), the number of volumes and EC shards the master reports
	// it hosts. A server absent from the map is not registered with the master.
	VolumeServerVolumeCounts(ctx context.Context) (map[string]int, error)
	// EvacuateServer moves every volume and EC shard off node (a <host>:<port>
	// id) onto the remaining servers, returning only once the node holds no
	// data. It is safe to call on an already-empty node (a fast no-op) and
	// returns an error if any volume cannot be moved (e.g. no replication-safe
	// destination), so the caller never removes a server that still holds data.
	EvacuateServer(ctx context.Context, node string) error
	io.Closer
}

// VolumeAdminFactory builds a VolumeAdmin for a target cluster's masters.
// grpcDialOption carries the transport credentials for clusters with [grpc]
// mTLS (nil to dial without TLS). Replaceable in tests.
type VolumeAdminFactory func(masters string, grpcDialOption grpc.DialOption, log logr.Logger) (VolumeAdmin, error)

// unlockTimeout bounds the master-connection wait when releasing the shell lock
// after an evacuation, so a defer never hangs on an unresponsive master.
const unlockTimeout = 15 * time.Second

// swadminVolumeAdmin is the default VolumeAdmin, backed by swadmin.SeaweedAdmin.
// mu serializes command execution (which swaps the shared Output writer) and Close.
type swadminVolumeAdmin struct {
	sa  *swadmin.SeaweedAdmin
	log logr.Logger
	mu  sync.Mutex
}

// NewSwadminVolumeAdmin returns a VolumeAdmin that talks to masters over the
// embedded `weed shell`. No filer is needed: every command it issues
// (volume.list, volumeServer.evacuate, lock/unlock) is master-only.
func NewSwadminVolumeAdmin(masters string, grpcDialOption grpc.DialOption, log logr.Logger) (VolumeAdmin, error) {
	sa := swadmin.NewSeaweedAdmin(masters, "", grpcDialOption, io.Discard)
	return &swadminVolumeAdmin{sa: sa, log: log}, nil
}

func (a *swadminVolumeAdmin) VolumeServerVolumeCounts(ctx context.Context) (map[string]int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sa.VolumeServerVolumeCounts(ctx)
}

func (a *swadminVolumeAdmin) EvacuateServer(ctx context.Context, node string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	var buf bytes.Buffer
	a.sa.Output = &buf

	// volumeServer.evacuate -apply refuses to run unless the masters are locked.
	if err := a.sa.ProcessCommand(ctx, "lock"); err != nil {
		return fmt.Errorf("lock masters: %w", err)
	}
	// Release the lock even if ctx is already done; a leaked lock would block
	// every later admin command until the master's lease expires. Use a fresh,
	// short-bounded context so an unresponsive master caps the unlock's
	// master-connection wait instead of stalling this defer.
	defer func() {
		a.sa.Output = io.Discard
		unlockCtx, cancel := context.WithTimeout(context.Background(), unlockTimeout)
		defer cancel()
		_ = a.sa.ProcessCommand(unlockCtx, "unlock")
	}()

	// No -skipNonMoveable: a volume that cannot be moved must fail the
	// evacuation so the caller keeps the server (and its only copy of that
	// data) alive rather than deleting the pod out from under it.
	cmd := fmt.Sprintf("volumeServer.evacuate -node %s -apply", node)
	if err := a.sa.ProcessCommand(ctx, cmd); err != nil {
		return fmt.Errorf("evacuate %s: %w (output: %s)", node, err, strings.TrimSpace(buf.String()))
	}

	a.log.Info("volume server evacuated", "node", node)
	return nil
}

func (a *swadminVolumeAdmin) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sa.Close()
}
