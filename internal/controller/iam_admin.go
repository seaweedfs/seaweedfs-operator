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
	"context"
	"errors"

	"github.com/go-logr/logr"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/seaweedfs/seaweedfs-operator/internal/controller/swadmin"
)

// IAMAdmin is the surface the S3 IAM reconcilers (S3Identity, S3Credentials,
// S3Policy, S3PolicyBinding) use to drive a Seaweed cluster's embedded IAM
// service. The default implementation wraps swadmin.IAMClient (the filer IAM
// gRPC API); tests inject a fake.
//
// Lookups (GetUser, GetPolicy) return ErrIAMNotFound when the object is
// absent. Deletes are idempotent — deleting an object that is already gone
// returns nil so finalizers converge.
type IAMAdmin interface {
	// GetUser returns the identity, or ErrIAMNotFound if it does not exist.
	GetUser(ctx context.Context, name string) (*swadmin.IAMUser, error)
	// CreateUser creates an identity with no credentials. Returns
	// ErrIAMUserAlreadyExists if the name is taken.
	CreateUser(ctx context.Context, name, displayName, email string, disabled bool) error
	// SetUserState updates only the account attributes and disabled flag,
	// preserving credentials and attached policies.
	SetUserState(ctx context.Context, name, displayName, email string, disabled bool) error
	// DeleteUser removes an identity and its credentials. Idempotent.
	DeleteUser(ctx context.Context, name string) error

	// CreateAccessKey registers an explicit credential pair on an identity.
	CreateAccessKey(ctx context.Context, user, accessKey, secretKey string) error
	// DeleteAccessKey removes a credential pair. Idempotent.
	DeleteAccessKey(ctx context.Context, user, accessKey string) error

	// PutPolicy creates or replaces a named policy from an AWS-style JSON
	// document.
	PutPolicy(ctx context.Context, name, document string) error
	// GetPolicy returns the stored JSON document, or ErrIAMNotFound.
	GetPolicy(ctx context.Context, name string) (string, error)
	// DeletePolicy removes a named policy. Idempotent.
	DeletePolicy(ctx context.Context, name string) error

	// AttachPolicy attaches a policy to an identity (no-op if already
	// attached).
	AttachPolicy(ctx context.Context, user, policy string) error
	// DetachPolicy detaches a policy from an identity (no-op if not
	// attached).
	DetachPolicy(ctx context.Context, user, policy string) error

	// PutOIDCProvider registers or updates a trusted OIDC identity provider
	// (issuer URL + client IDs + optional TLS thumbprints) and returns its
	// ARN. Idempotent on the issuer URL.
	PutOIDCProvider(ctx context.Context, provider swadmin.OIDCProvider) (string, error)
	// DeleteOIDCProvider removes the OIDC provider identified by issuer URL.
	// Idempotent.
	DeleteOIDCProvider(ctx context.Context, issuerURL string) error
}

// IAMAdminFactory creates an IAMAdmin for the IAM service on a target filer.
// adminSigningKey is jwt.filer_signing.key from the cluster's security.toml
// (nil/empty when the cluster does not require admin Bearer auth).
// grpcDialOption carries the transport credentials for clusters with [grpc]
// mTLS (nil to dial without TLS). Replaceable in tests.
type IAMAdminFactory func(filer string, adminSigningKey []byte, grpcDialOption grpc.DialOption, log logr.Logger) (IAMAdmin, error)

// Sentinel errors returned by IAMAdmin implementations.
var (
	// ErrIAMNotFound indicates the requested IAM object (user or policy)
	// does not exist.
	ErrIAMNotFound = errors.New("iam object not found")
	// ErrIAMUserAlreadyExists is returned by CreateUser when the name is
	// already taken.
	ErrIAMUserAlreadyExists = errors.New("iam user already exists")
)

// swadminIAMAdmin is the default IAMAdmin, backed by swadmin.IAMClient. The
// client opens a short-lived gRPC connection per call, so no locking or
// caching is required at this layer.
type swadminIAMAdmin struct {
	c   *swadmin.IAMClient
	log logr.Logger
}

// NewSwadminIAMAdmin returns an IAMAdmin that talks to the filer IAM gRPC API.
// adminSigningKey is forwarded to the underlying IAMClient so it can sign
// admin Bearer tokens; pass nil/empty when the cluster's security.toml does
// not configure jwt.filer_signing.key. grpcDialOption is forwarded as the
// transport credentials; pass nil when the cluster's gRPC ports run without
// TLS.
func NewSwadminIAMAdmin(filer string, adminSigningKey []byte, grpcDialOption grpc.DialOption, log logr.Logger) (IAMAdmin, error) {
	return &swadminIAMAdmin{c: swadmin.NewIAMClient(filer, adminSigningKey, grpcDialOption), log: log}, nil
}

func (a *swadminIAMAdmin) GetUser(ctx context.Context, name string) (*swadmin.IAMUser, error) {
	u, err := a.c.GetUser(ctx, name)
	if err != nil {
		return nil, mapIAMError(err)
	}
	return u, nil
}

func (a *swadminIAMAdmin) CreateUser(ctx context.Context, name, displayName, email string, disabled bool) error {
	return mapIAMError(a.c.CreateUser(ctx, name, displayName, email, disabled))
}

func (a *swadminIAMAdmin) SetUserState(ctx context.Context, name, displayName, email string, disabled bool) error {
	return mapIAMError(a.c.SetUserState(ctx, name, displayName, email, disabled))
}

func (a *swadminIAMAdmin) DeleteUser(ctx context.Context, name string) error {
	err := mapIAMError(a.c.DeleteUser(ctx, name))
	if errors.Is(err, ErrIAMNotFound) {
		return nil
	}
	return err
}

func (a *swadminIAMAdmin) CreateAccessKey(ctx context.Context, user, accessKey, secretKey string) error {
	return mapIAMError(a.c.CreateAccessKey(ctx, user, accessKey, secretKey))
}

func (a *swadminIAMAdmin) DeleteAccessKey(ctx context.Context, user, accessKey string) error {
	err := mapIAMError(a.c.DeleteAccessKey(ctx, user, accessKey))
	if errors.Is(err, ErrIAMNotFound) {
		return nil
	}
	return err
}

func (a *swadminIAMAdmin) PutPolicy(ctx context.Context, name, document string) error {
	return mapIAMError(a.c.PutPolicy(ctx, name, document))
}

func (a *swadminIAMAdmin) GetPolicy(ctx context.Context, name string) (string, error) {
	content, err := a.c.GetPolicy(ctx, name)
	if err != nil {
		return "", mapIAMError(err)
	}
	return content, nil
}

// DeletePolicy removes a policy idempotently. The IAM service does not
// reliably report a missing policy as codes.NotFound on delete, so we probe
// with GetPolicy first and treat an absent policy as already deleted.
func (a *swadminIAMAdmin) DeletePolicy(ctx context.Context, name string) error {
	if _, err := a.GetPolicy(ctx, name); err != nil {
		if errors.Is(err, ErrIAMNotFound) {
			return nil
		}
		return err
	}
	return mapIAMError(a.c.DeletePolicy(ctx, name))
}

func (a *swadminIAMAdmin) AttachPolicy(ctx context.Context, user, policy string) error {
	return mapIAMError(a.c.AttachPolicy(ctx, user, policy))
}

func (a *swadminIAMAdmin) DetachPolicy(ctx context.Context, user, policy string) error {
	return mapIAMError(a.c.DetachPolicy(ctx, user, policy))
}

func (a *swadminIAMAdmin) PutOIDCProvider(ctx context.Context, provider swadmin.OIDCProvider) (string, error) {
	arn, err := a.c.PutOIDCProvider(ctx, provider)
	if err != nil {
		return "", mapIAMError(err)
	}
	return arn, nil
}

func (a *swadminIAMAdmin) DeleteOIDCProvider(ctx context.Context, issuerURL string) error {
	err := mapIAMError(a.c.DeleteOIDCProvider(ctx, issuerURL))
	if errors.Is(err, ErrIAMNotFound) {
		return nil
	}
	return err
}

// mapIAMError translates IAM gRPC status codes into the package sentinels the
// reconcilers branch on. Non-status errors and other codes pass through.
func mapIAMError(err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.NotFound:
		return ErrIAMNotFound
	case codes.AlreadyExists:
		return ErrIAMUserAlreadyExists
	default:
		return err
	}
}
