package swadmin

import (
	"context"
	"errors"
)

// OIDCProvider is the subset of a trusted OpenID Connect provider the operator
// reconciles. It is a plain domain struct so transport types stay confined to
// this package. IssuerURL is the provider's stable identity; the IAM service
// derives the provider ARN from it and the cluster account ID.
type OIDCProvider struct {
	IssuerURL   string
	ClientIDs   []string
	Thumbprints []string
}

// ErrOIDCNotWired marks the OIDC client methods as not yet connected to a
// transport. See the package note below: the embedded IAM service exposes OIDC
// provider management only over the AWS-style HTTP IAM API today, not over the
// filer IAM gRPC service this client uses for users/policies. Wiring requires
// one of the transports described in PutOIDCProvider.
var ErrOIDCNotWired = errors.New("swadmin: OIDC provider management transport not wired (see oidc_client.go)")

// PutOIDCProvider registers or updates a trusted OIDC provider and returns its
// ARN.
//
// Unlike users and policies, OIDC providers are NOT reachable over the
// SeaweedIdentityAccessManagement gRPC service this client dials — that service
// has no OIDC RPCs. There are three ways to wire this; pick one:
//
//  1. (recommended) Add OIDC RPCs to SeaweedIdentityAccessManagement in
//     seaweedfs (weed/pb/iam.proto): PutOIDCProvider / GetOIDCProvider /
//     DeleteOIDCProvider / ListOIDCProviders, backed by the filer's
//     NewIamGrpcServer routing into IAMManager.{Create,Delete}OIDCProvider.
//     Then this method is the same withClient(...) pattern as PutPolicy and the
//     operator keeps its single filer-gRPC transport + admin Bearer auth.
//
//  2. Call the embedded IAM HTTP API's CreateOpenIDConnectProvider /
//     DeleteOpenIDConnectProvider actions (form-encoded, SigV4-signed) against
//     the S3 endpoint. Works against today's server with no change, but needs
//     admin S3 access keys and a second transport/target distinct from the
//     filer gRPC the other IAM CRDs use.
//
//  3. Write the OIDCProviderRecord JSON directly under
//     /etc/iam/oidc-providers/ via the SeaweedFiler gRPC (the S3 server's
//     onOIDCProviderChange subscription picks it up live). Reachable today over
//     the existing transport, but couples the operator to the store's internal
//     record format and ARN derivation.
func (c *IAMClient) PutOIDCProvider(ctx context.Context, provider OIDCProvider) (string, error) {
	// Intended shape once option (1) lands in seaweedfs:
	//
	//	var arn string
	//	err := c.withClient(ctx, func(ctx context.Context, client iam_pb.SeaweedIdentityAccessManagementClient) error {
	//		resp, err := client.PutOIDCProvider(ctx, &iam_pb.PutOIDCProviderRequest{
	//			IssuerUrl:   provider.IssuerURL,
	//			ClientIds:   provider.ClientIDs,
	//			Thumbprints: provider.Thumbprints,
	//		})
	//		if err != nil {
	//			return err
	//		}
	//		arn = resp.GetArn()
	//		return nil
	//	})
	//	return arn, err
	return "", ErrOIDCNotWired
}

// DeleteOIDCProvider removes the OIDC provider identified by issuer URL.
// See PutOIDCProvider for the transport options that need wiring.
func (c *IAMClient) DeleteOIDCProvider(ctx context.Context, issuerURL string) error {
	// Intended shape once option (1) lands in seaweedfs:
	//
	//	return c.withClient(ctx, func(ctx context.Context, client iam_pb.SeaweedIdentityAccessManagementClient) error {
	//		_, err := client.DeleteOIDCProvider(ctx, &iam_pb.DeleteOIDCProviderRequest{IssuerUrl: issuerURL})
	//		return err
	//	})
	return ErrOIDCNotWired
}
