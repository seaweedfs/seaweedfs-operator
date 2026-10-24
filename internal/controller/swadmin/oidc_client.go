package swadmin

import (
	"context"

	"github.com/seaweedfs/seaweedfs/weed/pb/iam_pb"
)

// OIDCProvider is the subset of a trusted OpenID Connect provider the operator
// reconciles. It is a plain domain struct so transport types stay confined to
// this package. IssuerURL is the provider's stable identity; the IAM service
// derives the provider ARN from it and AccountID (empty = global, usable by
// roles in any account, which is what SeaweedFS's default role ARNs need).
type OIDCProvider struct {
	IssuerURL   string
	ClientIDs   []string
	Thumbprints []string
	AccountID   string
}

// PutOIDCProvider registers or updates a trusted OIDC provider and returns its
// ARN. The filer writes it where S3 servers running with a filer-typed
// "oidcProviderStore" read, and their metadata subscription applies it without
// a restart.
func (c *IAMClient) PutOIDCProvider(ctx context.Context, provider OIDCProvider) (string, error) {
	var arn string
	err := c.withClient(ctx, func(ctx context.Context, client iam_pb.SeaweedIdentityAccessManagementClient) error {
		resp, err := client.PutOIDCProvider(ctx, &iam_pb.PutOIDCProviderRequest{
			IssuerUrl:   provider.IssuerURL,
			ClientIds:   provider.ClientIDs,
			Thumbprints: provider.Thumbprints,
			AccountId:   provider.AccountID,
		})
		if err != nil {
			return err
		}
		arn = resp.GetArn()
		return nil
	})
	return arn, err
}

// DeleteOIDCProvider removes the OIDC provider identified by issuer URL. The
// service treats a missing provider as deleted.
func (c *IAMClient) DeleteOIDCProvider(ctx context.Context, issuerURL string) error {
	return c.withClient(ctx, func(ctx context.Context, client iam_pb.SeaweedIdentityAccessManagementClient) error {
		_, err := client.DeleteOIDCProvider(ctx, &iam_pb.DeleteOIDCProviderRequest{IssuerUrl: issuerURL})
		return err
	})
}
