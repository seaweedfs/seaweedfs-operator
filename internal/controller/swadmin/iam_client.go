package swadmin

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/seaweedfs/seaweedfs/weed/iam"
	"github.com/seaweedfs/seaweedfs/weed/pb"
	"github.com/seaweedfs/seaweedfs/weed/pb/iam_pb"
	"github.com/seaweedfs/seaweedfs/weed/security"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// iamRequestTimeout caps every IAM gRPC call so a reconcile can't hang on an
// unresponsive filer. Mirrors the shell's withIamClient timeout.
const iamRequestTimeout = 30 * time.Second

// keyedMutex hands out a distinct mutex per string key. It is used to
// serialize read-modify-write updates to a single IAM identity.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// lock acquires the mutex for key and returns its unlock func.
func (k *keyedMutex) lock(key string) func() {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = make(map[string]*sync.Mutex)
	}
	m, ok := k.locks[key]
	if !ok {
		m = &sync.Mutex{}
		k.locks[key] = m
	}
	k.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// iamUserLocks serializes the GetUser→mutate→UpdateUser sequences in
// SetUserState / AttachPolicy / DetachPolicy. The SeaweedFS IAM service has no
// ETag/versioning on identities, so without this lock two concurrent
// reconciles touching the same user (e.g. an S3Identity disabling it while an
// S3PolicyBinding attaches a policy) could clobber each other's write. The map
// is package-global because each reconciler holds its own IAMClient, so the
// lock has to live above any single client instance. Keyed by
// filer-address + user, it does not guard against changes made outside the
// operator process — that would require server-side optimistic concurrency.
var iamUserLocks = &keyedMutex{}

// IAMClient talks to a Seaweed cluster's embedded IAM service — the IAM gRPC
// API served on the filer's gRPC port. It mirrors `weed shell`'s s3.user.* /
// s3.accesskey.* / s3.policy* commands but skips the shell layer (and its
// file-based policy I/O and stderr-only secret printing) so the operator can
// run with a read-only root filesystem and capture every result.
//
// Like the bucket admin's master connection, this dials without TLS. When
// adminSigningKey is non-empty (the operator reads it from the rendered
// security.toml ConfigMap) every RPC is stamped with a freshly minted admin
// Bearer token in the "authorization" metadata — matching the upstream
// `weed shell` IAM client. When the key is empty the calls are
// unauthenticated, which the filer accepts when jwt.filer_signing.key is not
// set in its security.toml.
type IAMClient struct {
	filerGrpcAddress string
	dialOption       grpc.DialOption
	adminSigningKey  security.SigningKey
}

// NewIAMClient builds an IAMClient for the given filer. filer is the filer's
// HTTP host:port (as returned by getFilerAddress); seaweedfs derives the gRPC
// port from it internally. adminSigningKey is the jwt.filer_signing.key from
// the cluster's security.toml; pass nil/empty when the cluster does not
// require admin Bearer auth.
func NewIAMClient(filer string, adminSigningKey []byte) *IAMClient {
	return &IAMClient{
		filerGrpcAddress: pb.ServerAddress(filer).ToGrpcAddress(),
		dialOption:       grpc.WithTransportCredentials(insecure.NewCredentials()),
		adminSigningKey:  security.SigningKey(adminSigningKey),
	}
}

// withClient opens a short-lived connection to the filer IAM service, applies
// the request timeout, attaches an admin Bearer token when one is configured,
// and invokes fn. Errors are returned verbatim so callers can classify gRPC
// status codes.
func (c *IAMClient) withClient(ctx context.Context, fn func(ctx context.Context, client iam_pb.SeaweedIdentityAccessManagementClient) error) error {
	return pb.WithGrpcClient(false, 0, func(conn *grpc.ClientConn) error {
		callCtx, cancel := context.WithTimeout(c.authContext(ctx), iamRequestTimeout)
		defer cancel()
		return fn(callCtx, iam_pb.NewSeaweedIdentityAccessManagementClient(conn))
	}, c.filerGrpcAddress, false, c.dialOption)
}

// authContext returns ctx with an admin Bearer token appended to the outgoing
// metadata when adminSigningKey is configured; otherwise ctx is returned
// unchanged. The token is minted per call (claims carry no per-request data
// but jwt/v5 stamps a fresh iat/exp) — cheap enough at IAM-CR-reconcile rate.
// Mirrors weed/shell/command_s3_iam_client.go:iamAdminAuthContext.
func (c *IAMClient) authContext(ctx context.Context) context.Context {
	if len(c.adminSigningKey) == 0 {
		return ctx
	}
	// expiresAfterSec=0 → no exp claim. The filer's checkAdminAuth only
	// validates the signature + RegisteredClaims; not pinning an expiry here
	// keeps the operator's IAM reconciles working without a clock-skew
	// allowance against the filer pod.
	token := security.GenJwtForFilerAdmin(c.adminSigningKey, 0)
	if token == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+string(token))
}

// lockUser serializes read-modify-write updates to a single identity on this
// client's filer. Use as `defer c.lockUser(name)()`.
func (c *IAMClient) lockUser(name string) func() {
	return iamUserLocks.lock(c.filerGrpcAddress + "\x00" + name)
}

// IAMUser is the subset of an IAM identity the operator reconciles. It is a
// plain domain struct so the iam_pb types stay confined to this package.
type IAMUser struct {
	Name        string
	Disabled    bool
	DisplayName string
	Email       string
	PolicyNames []string
	AccessKeys  []string
}

// GetUser fetches an identity. The raw gRPC error (codes.NotFound when the
// user is absent) is returned for the caller to classify.
func (c *IAMClient) GetUser(ctx context.Context, name string) (*IAMUser, error) {
	var out *IAMUser
	err := c.withClient(ctx, func(ctx context.Context, client iam_pb.SeaweedIdentityAccessManagementClient) error {
		resp, err := client.GetUser(ctx, &iam_pb.GetUserRequest{Username: name})
		if err != nil {
			return err
		}
		out = identityToUser(resp.GetIdentity())
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CreateUser creates an identity with no credentials and the given account
// attributes / disabled state.
func (c *IAMClient) CreateUser(ctx context.Context, name, displayName, email string, disabled bool) error {
	return c.withClient(ctx, func(ctx context.Context, client iam_pb.SeaweedIdentityAccessManagementClient) error {
		_, err := client.CreateUser(ctx, &iam_pb.CreateUserRequest{
			Identity: &iam_pb.Identity{
				Name:     name,
				Disabled: disabled,
				Account:  accountOrNil(displayName, email),
			},
		})
		return err
	})
}

// SetUserState updates only the account attributes and disabled flag of an
// existing identity, preserving its credentials and attached policies. It
// reads the current identity, mutates the two managed fields, and writes it
// back via UpdateUser.
func (c *IAMClient) SetUserState(ctx context.Context, name, displayName, email string, disabled bool) error {
	defer c.lockUser(name)()
	return c.withClient(ctx, func(ctx context.Context, client iam_pb.SeaweedIdentityAccessManagementClient) error {
		resp, err := client.GetUser(ctx, &iam_pb.GetUserRequest{Username: name})
		if err != nil {
			return err
		}
		id := resp.GetIdentity()
		if id == nil {
			return fmt.Errorf("user %q returned empty identity", name)
		}
		id.Disabled = disabled
		id.Account = accountOrNil(displayName, email)
		_, err = client.UpdateUser(ctx, &iam_pb.UpdateUserRequest{Username: name, Identity: id})
		return err
	})
}

// DeleteUser removes an identity and all of its credentials.
func (c *IAMClient) DeleteUser(ctx context.Context, name string) error {
	return c.withClient(ctx, func(ctx context.Context, client iam_pb.SeaweedIdentityAccessManagementClient) error {
		_, err := client.DeleteUser(ctx, &iam_pb.DeleteUserRequest{Username: name})
		return err
	})
}

// CreateAccessKey registers an explicit credential pair on an existing
// identity. The caller is responsible for not creating a duplicate access key
// (the IAM service reports that as a generic error, not AlreadyExists).
func (c *IAMClient) CreateAccessKey(ctx context.Context, user, accessKey, secretKey string) error {
	return c.withClient(ctx, func(ctx context.Context, client iam_pb.SeaweedIdentityAccessManagementClient) error {
		_, err := client.CreateAccessKey(ctx, &iam_pb.CreateAccessKeyRequest{
			Username: user,
			Credential: &iam_pb.Credential{
				AccessKey: accessKey,
				SecretKey: secretKey,
				Status:    iam.AccessKeyStatusActive,
			},
		})
		return err
	})
}

// DeleteAccessKey removes a credential pair from an identity.
func (c *IAMClient) DeleteAccessKey(ctx context.Context, user, accessKey string) error {
	return c.withClient(ctx, func(ctx context.Context, client iam_pb.SeaweedIdentityAccessManagementClient) error {
		_, err := client.DeleteAccessKey(ctx, &iam_pb.DeleteAccessKeyRequest{
			Username:  user,
			AccessKey: accessKey,
		})
		return err
	})
}

// AttachPolicy adds policy to an identity's policy list (no-op if already
// attached), preserving the rest of the identity.
func (c *IAMClient) AttachPolicy(ctx context.Context, user, policy string) error {
	defer c.lockUser(user)()
	return c.withClient(ctx, func(ctx context.Context, client iam_pb.SeaweedIdentityAccessManagementClient) error {
		resp, err := client.GetUser(ctx, &iam_pb.GetUserRequest{Username: user})
		if err != nil {
			return err
		}
		id := resp.GetIdentity()
		if id == nil {
			return fmt.Errorf("user %q returned empty identity", user)
		}
		for _, p := range id.PolicyNames {
			if p == policy {
				return nil
			}
		}
		id.PolicyNames = append(id.PolicyNames, policy)
		_, err = client.UpdateUser(ctx, &iam_pb.UpdateUserRequest{Username: user, Identity: id})
		return err
	})
}

// DetachPolicy removes policy from an identity's policy list (no-op if not
// attached), preserving the rest of the identity.
func (c *IAMClient) DetachPolicy(ctx context.Context, user, policy string) error {
	defer c.lockUser(user)()
	return c.withClient(ctx, func(ctx context.Context, client iam_pb.SeaweedIdentityAccessManagementClient) error {
		resp, err := client.GetUser(ctx, &iam_pb.GetUserRequest{Username: user})
		if err != nil {
			return err
		}
		id := resp.GetIdentity()
		if id == nil {
			return fmt.Errorf("user %q returned empty identity", user)
		}
		kept := make([]string, 0, len(id.PolicyNames))
		found := false
		for _, p := range id.PolicyNames {
			if p == policy {
				found = true
				continue
			}
			kept = append(kept, p)
		}
		if !found {
			return nil
		}
		id.PolicyNames = kept
		_, err = client.UpdateUser(ctx, &iam_pb.UpdateUserRequest{Username: user, Identity: id})
		return err
	})
}

// SetBucketAccess grants user the comma-separated actions on bucket, mirroring
// `weed shell s3.bucket.access`. Bucket grants are stored on the identity as
// "Action:bucket" entries; passing "none" (or an empty string) strips the
// user's grants on this bucket without deleting the identity or its other
// grants. The user is auto-created when absent. Going through IAMClient (rather
// than the shell) means the call carries the admin Bearer token, which the
// filer's IAM service requires once jwt.filer_signing.key is set.
func (c *IAMClient) SetBucketAccess(ctx context.Context, bucket, user, actions string) error {
	defer c.lockUser(user)()
	return c.withClient(ctx, func(ctx context.Context, client iam_pb.SeaweedIdentityAccessManagementClient) error {
		resp, err := client.GetUser(ctx, &iam_pb.GetUserRequest{Username: user})
		isNewUser := false
		if err != nil {
			if st, ok := status.FromError(err); !ok || st.Code() != codes.NotFound {
				return err
			}
			isNewUser = true
		}
		id := resp.GetIdentity()
		if isNewUser || id == nil {
			id = &iam_pb.Identity{Name: user}
			isNewUser = true
		}
		setBucketActions(id, bucket, actions)
		if isNewUser {
			_, err = client.CreateUser(ctx, &iam_pb.CreateUserRequest{Identity: id})
			return err
		}
		_, err = client.UpdateUser(ctx, &iam_pb.UpdateUserRequest{Username: user, Identity: id})
		return err
	})
}

// setBucketActions rewrites id's bucket-scoped actions: it drops existing
// "X:bucket" entries and appends one per requested action, leaving grants on
// other buckets untouched. An empty or "none" actions string only strips.
// Mirrors weed/shell updateBucketActions; the CRD enum constrains actions to
// the canonical set so no casing fixup is needed here.
func setBucketActions(id *iam_pb.Identity, bucket, actions string) {
	suffix := ":" + bucket
	kept := make([]string, 0, len(id.Actions))
	for _, a := range id.Actions {
		if !strings.HasSuffix(a, suffix) {
			kept = append(kept, a)
		}
	}
	if actions != "" && !strings.EqualFold(actions, "none") {
		for _, a := range strings.Split(actions, ",") {
			if a = strings.TrimSpace(a); a != "" {
				kept = append(kept, a+suffix)
			}
		}
	}
	id.Actions = kept
}

// PutPolicy creates or replaces a named policy with the given AWS-style JSON
// document.
func (c *IAMClient) PutPolicy(ctx context.Context, name, document string) error {
	return c.withClient(ctx, func(ctx context.Context, client iam_pb.SeaweedIdentityAccessManagementClient) error {
		_, err := client.PutPolicy(ctx, &iam_pb.PutPolicyRequest{Name: name, Content: document})
		return err
	})
}

// GetPolicy returns the stored JSON document for a policy. The raw gRPC error
// (codes.NotFound when absent) is returned for the caller to classify.
func (c *IAMClient) GetPolicy(ctx context.Context, name string) (string, error) {
	var content string
	err := c.withClient(ctx, func(ctx context.Context, client iam_pb.SeaweedIdentityAccessManagementClient) error {
		resp, err := client.GetPolicy(ctx, &iam_pb.GetPolicyRequest{Name: name})
		if err != nil {
			return err
		}
		content = resp.GetContent()
		return nil
	})
	if err != nil {
		return "", err
	}
	return content, nil
}

// DeletePolicy removes a named policy.
func (c *IAMClient) DeletePolicy(ctx context.Context, name string) error {
	return c.withClient(ctx, func(ctx context.Context, client iam_pb.SeaweedIdentityAccessManagementClient) error {
		_, err := client.DeletePolicy(ctx, &iam_pb.DeletePolicyRequest{Name: name})
		return err
	})
}

// GenerateKeyPair returns a fresh random access key id and secret access key,
// using the same generators the IAM service uses for auto-provisioned keys.
func GenerateKeyPair() (accessKey, secretKey string, err error) {
	accessKey, err = iam.GenerateRandomString(iam.AccessKeyIdLength, iam.CharsetUpper)
	if err != nil {
		return "", "", fmt.Errorf("generate access key: %w", err)
	}
	secretKey, err = iam.GenerateSecretAccessKey()
	if err != nil {
		return "", "", fmt.Errorf("generate secret key: %w", err)
	}
	return accessKey, secretKey, nil
}

func identityToUser(id *iam_pb.Identity) *IAMUser {
	if id == nil {
		return nil
	}
	u := &IAMUser{
		Name:        id.Name,
		Disabled:    id.Disabled,
		PolicyNames: append([]string(nil), id.PolicyNames...),
	}
	if id.Account != nil {
		u.DisplayName = id.Account.DisplayName
		u.Email = id.Account.EmailAddress
	}
	for _, cred := range id.Credentials {
		u.AccessKeys = append(u.AccessKeys, cred.AccessKey)
	}
	return u
}

func accountOrNil(displayName, email string) *iam_pb.Account {
	if displayName == "" && email == "" {
		return nil
	}
	return &iam_pb.Account{DisplayName: displayName, EmailAddress: email}
}
