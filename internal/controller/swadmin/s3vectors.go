package swadmin

import (
	"context"

	"github.com/seaweedfs/seaweedfs/weed/s3api/s3_constants"
	"github.com/seaweedfs/seaweedfs/weed/s3api/s3vectors"
)

func (sa *SeaweedAdmin) ExecuteS3VectorsOperation(ctx context.Context, operation string, req, resp interface{}) error {
	server := s3vectors.NewServer(sa.commandEnv, nil, nil)
	return server.Execute(ctx, operation, req, resp, s3_constants.AccountAdminId)
}
