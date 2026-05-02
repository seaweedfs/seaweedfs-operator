package swadmin

import (
	"context"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"

	"google.golang.org/grpc/credentials/insecure"

	"github.com/seaweedfs/seaweedfs/weed/shell"
	"google.golang.org/grpc"
)

func init() {
	// seaweedfs internals log via weed/glog, which defaults to writing
	// log files under /tmp. The operator pod runs with a read-only root
	// filesystem, so glog repeatedly prints "cannot create log: ... read-only
	// file system" to stderr the moment any seaweedfs code logs. Force
	// stderr-only output as the operator default. flag.Set before
	// flag.Parse only changes the default — operators can still override
	// via -logtostderr=false on the manager command line.
	if f := flag.Lookup("logtostderr"); f != nil {
		_ = f.Value.Set("true")
	}
}

type SeaweedAdmin struct {
	commandReg *regexp.Regexp
	commandEnv *shell.CommandEnv
	Output     io.Writer
}

func NewSeaweedAdmin(masters string, output io.Writer) *SeaweedAdmin {
	var shellOptions shell.ShellOptions
	shellOptions.GrpcDialOption = grpc.WithTransportCredentials(insecure.NewCredentials())
	shellOptions.Masters = &masters
	// shell.NewCommandEnv unconditionally dereferences FilerGroup; leaving
	// it nil panics the reconciler the moment any Bucket is processed.
	// Match the `weed shell` default of an empty filer group.
	emptyFilerGroup := ""
	shellOptions.FilerGroup = &emptyFilerGroup

	commandEnv := shell.NewCommandEnv(&shellOptions)
	reg, _ := regexp.Compile(`'.*?'|".*?"|\S+`)

	go commandEnv.MasterClient.KeepConnectedToMaster(context.Background())

	return &SeaweedAdmin{
		commandEnv: commandEnv,
		commandReg: reg,
		Output:     output,
	}
}

// ProcessCommands cmds can be semi-colon separated commands
func (sa *SeaweedAdmin) ProcessCommands(cmds string) error {
	for _, c := range strings.Split(cmds, ";") {
		if err := sa.ProcessCommand(c); err != nil {
			return err
		}
	}
	return nil
}

func (sa *SeaweedAdmin) ProcessCommand(cmd string) error {
	sa.commandEnv.MasterClient.WaitUntilConnected(context.Background())
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
			return c.Do(args, sa.commandEnv, sa.Output)
		}
	}

	return fmt.Errorf("unknown command: %v", cmd)

}
