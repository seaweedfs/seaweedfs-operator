package swadmin

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/seaweedfs/seaweedfs/weed/pb"
	"github.com/seaweedfs/seaweedfs/weed/shell"
	"github.com/seaweedfs/seaweedfs/weed/util/fla9"
)

func init() {
	// seaweedfs internals log via weed/glog, which defaults to writing
	// log files under /tmp. The operator pod runs with a read-only root
	// filesystem, so glog repeatedly prints "cannot create log: ... read-only
	// file system" to stderr the moment any seaweedfs code logs. glog
	// registers its flags via the seaweedfs-internal fla9 package (NOT
	// the stdlib flag package), so this has to go through fla9.
	if f := fla9.Lookup("logtostderr"); f != nil {
		_ = f.Value.Set("true")
	}
}

type SeaweedAdmin struct {
	commandReg *regexp.Regexp
	commandEnv *shell.CommandEnv
	Output     io.Writer
	cancel     context.CancelFunc
	closeOnce  sync.Once
}

const masterConnectionTimeout = 30 * time.Second

// NewSeaweedAdmin builds a SeaweedAdmin that mirrors `weed shell`. filer is
// required for s3.bucket.* / fs.* callers; master-only callers (volume.list,
// volume.balance) may pass "". dialOption carries the transport credentials
// for clusters with [grpc] mTLS (see ClientTLSDialOption); pass nil to dial
// without TLS.
func NewSeaweedAdmin(masters, filer string, dialOption grpc.DialOption, output io.Writer) *SeaweedAdmin {
	if dialOption == nil {
		dialOption = grpc.WithTransportCredentials(insecure.NewCredentials())
	}
	var shellOptions shell.ShellOptions
	shellOptions.GrpcDialOption = dialOption
	shellOptions.Masters = &masters
	shellOptions.FilerAddress = pb.ServerAddress(filer)
	// shell.NewCommandEnv unconditionally dereferences FilerGroup; leaving
	// it nil panics the reconciler the moment any Bucket is processed.
	emptyFilerGroup := ""
	shellOptions.FilerGroup = &emptyFilerGroup

	commandEnv := shell.NewCommandEnv(&shellOptions)
	reg, _ := regexp.Compile(`'.*?'|".*?"|\S+`)

	ctx, cancel := context.WithCancel(context.Background())
	go commandEnv.MasterClient.KeepConnectedToMaster(ctx)

	return &SeaweedAdmin{
		commandEnv: commandEnv,
		commandReg: reg,
		Output:     output,
		cancel:     cancel,
	}
}

// Close stops the background master connection loop.
func (sa *SeaweedAdmin) Close() error {
	sa.closeOnce.Do(sa.cancel)
	return nil
}

// ProcessCommands runs semicolon-separated commands in order.
func (sa *SeaweedAdmin) ProcessCommands(ctx context.Context, cmds string) error {
	for _, c := range strings.Split(cmds, ";") {
		if err := sa.ProcessCommand(ctx, c); err != nil {
			return err
		}
	}
	return nil
}

// ProcessCommand runs one shell command, capping the wait for a master
// connection at masterConnectionTimeout and observing ctx.
func (sa *SeaweedAdmin) ProcessCommand(ctx context.Context, cmd string) error {
	cmds := sa.commandReg.FindAllString(cmd, -1)
	if len(cmds) == 0 {
		return nil
	}

	args := make([]string, len(cmds[1:]))

	for i := range args {
		args[i] = strings.Trim(string(cmds[1+i]), "\"'")
	}

	for _, c := range shell.Commands {
		if c.Name() == cmds[0] || c.Name() == "fs."+cmds[0] {
			waitCtx, cancel := context.WithTimeout(ctx, masterConnectionTimeout)
			defer cancel()
			// Trust the resolved master, not waitCtx.Err(): a connection that
			// lands as the deadline fires must not read as a timeout.
			if sa.commandEnv.MasterClient.GetMaster(waitCtx) == "" {
				return fmt.Errorf("wait for master connection: %w", waitCtx.Err())
			}
			return c.Do(args, sa.commandEnv, sa.Output)
		}
	}

	return fmt.Errorf("unknown command: %v", cmd)

}
