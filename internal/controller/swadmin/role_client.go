package swadmin

import (
	"context"

	"github.com/seaweedfs/seaweedfs/weed/pb/iam_pb"
)

// IAMRole is the subset of an STS role the operator reconciles. TrustPolicy is
// the JSON trust policy document; AttachedPolicies are IAM policy names.
type IAMRole struct {
	Name               string
	TrustPolicy        string
	AttachedPolicies   []string
	Description        string
	MaxSessionDuration int64
}

// PutRole creates or replaces a role and returns its ARN. The filer writes it
// where S3 servers running with a filer-typed "roleStore" read; their metadata
// subscription drops their cached copy, so a changed trust policy (including a
// revoked subject) takes effect on every S3 server at once.
func (c *IAMClient) PutRole(ctx context.Context, role IAMRole) (string, error) {
	var arn string
	err := c.withClient(ctx, func(ctx context.Context, client iam_pb.SeaweedIdentityAccessManagementClient) error {
		resp, err := client.PutRole(ctx, &iam_pb.PutRoleRequest{Role: &iam_pb.Role{
			RoleName:           role.Name,
			TrustPolicy:        role.TrustPolicy,
			AttachedPolicies:   role.AttachedPolicies,
			Description:        role.Description,
			MaxSessionDuration: role.MaxSessionDuration,
		}})
		if err != nil {
			return err
		}
		arn = resp.GetRoleArn()
		return nil
	})
	return arn, err
}

// DeleteRole removes a role. The service treats a missing role as deleted.
func (c *IAMClient) DeleteRole(ctx context.Context, name string) error {
	return c.withClient(ctx, func(ctx context.Context, client iam_pb.SeaweedIdentityAccessManagementClient) error {
		_, err := client.DeleteRole(ctx, &iam_pb.DeleteRoleRequest{RoleName: name})
		return err
	})
}
