package backup

import (
	"fmt"
	"strings"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// GenerateLocalSinkConfig generates configuration for local sink
func GenerateLocalSinkConfig(config *strings.Builder, localConfig *seaweedv1.LocalSinkConfig) {
	config.WriteString("[sink.local]\n")
	config.WriteString("enabled = true\n")
	config.WriteString(fmt.Sprintf("directory = \"%s\"\n", localConfig.Directory))
	config.WriteString(fmt.Sprintf("is_incremental = %t\n\n", localConfig.IsIncremental))
}

// GenerateFilerSinkConfig generates configuration for filer sink
func GenerateFilerSinkConfig(config *strings.Builder, filerConfig *seaweedv1.FilerSinkConfig) {
	config.WriteString("[sink.filer]\n")
	config.WriteString("enabled = true\n")
	config.WriteString(fmt.Sprintf("grpcAddress = \"%s\"\n", filerConfig.GRPCAddress))
	config.WriteString(fmt.Sprintf("directory = \"%s\"\n", filerConfig.Directory))
	config.WriteString(fmt.Sprintf("replication = \"%s\"\n", filerConfig.Replication))
	config.WriteString(fmt.Sprintf("collection = \"%s\"\n", filerConfig.Collection))
	config.WriteString(fmt.Sprintf("ttlSec = %d\n", filerConfig.TTLSec))
	config.WriteString(fmt.Sprintf("is_incremental = %t\n\n", filerConfig.IsIncremental))
}
