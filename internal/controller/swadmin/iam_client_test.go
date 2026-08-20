package swadmin

import (
	"context"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seaweedfs/seaweedfs/weed/iam"
	"github.com/seaweedfs/seaweedfs/weed/pb/iam_pb"
	"github.com/seaweedfs/seaweedfs/weed/security"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// TestIAMClient_AuthContext_AppendsBearerWhenKeySet guards against regressing
// https://github.com/seaweedfs/seaweedfs-operator/issues/257: the operator
// always renders jwt.filer_signing.key into security.toml when spec.Filer is
// defined, so the filer's IAM gRPC service requires an admin Bearer token on
// every call; before this fix the IAMClient sent none and every IAM CR
// failed with "Unauthenticated desc = missing authorization metadata".
func TestIAMClient_AuthContext_AppendsBearerWhenKeySet(t *testing.T) {
	key := []byte("test-jwt-signing-key")
	c := NewIAMClient("seaweed-filer.invalid:8888", key, nil)

	ctx := c.authContext(context.Background())
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		t.Fatal("expected outgoing metadata")
	}
	authHeaders := md.Get("authorization")
	if len(authHeaders) != 1 {
		t.Fatalf("expected exactly one authorization header, got %v", authHeaders)
	}
	if !strings.HasPrefix(authHeaders[0], "Bearer ") {
		t.Fatalf("expected Bearer scheme, got %q", authHeaders[0])
	}
	token := strings.TrimPrefix(authHeaders[0], "Bearer ")
	if token == "" {
		t.Fatal("Bearer token is empty")
	}
	// Confirm the token round-trips against the same checkAdminAuth path the
	// upstream filer uses (security.DecodeJwt with SeaweedFilerAdminClaims).
	parsed, err := security.DecodeJwt(security.SigningKey(key), security.EncodedJwt(token), &security.SeaweedFilerAdminClaims{})
	if err != nil {
		t.Fatalf("DecodeJwt returned error: %v", err)
	}
	if parsed == nil || !parsed.Valid {
		t.Fatal("parsed token is not valid")
	}
}

// TestIAMClient_AuthContext_NoMetadataWhenKeyEmpty pins the unauthenticated
// path so clusters whose security.toml has no jwt.filer_signing.key keep
// working — matching the filer's checkAdminAuth no-op branch.
func TestIAMClient_AuthContext_NoMetadataWhenKeyEmpty(t *testing.T) {
	c := NewIAMClient("seaweed-filer.invalid:8888", nil, nil)
	ctx := c.authContext(context.Background())
	md, ok := metadata.FromOutgoingContext(ctx)
	if ok && len(md.Get("authorization")) > 0 {
		t.Fatalf("expected no authorization metadata, got %v", md.Get("authorization"))
	}
}

// TestIAMClient_GetUser_SendsBearer end-to-end-confirms the Bearer header
// reaches the filer's IAM gRPC server by spinning up an in-process gRPC
// server that asserts the incoming metadata. This is the regression test the
// previous fake-IAMAdmin tests could never catch: it exercises the actual
// withClient -> gRPC unary call path.
func TestIAMClient_GetUser_SendsBearer(t *testing.T) {
	key := []byte("test-jwt-signing-key")

	srv := grpc.NewServer()
	t.Cleanup(srv.Stop)
	rec := &authRecordingIAM{adminSigningKey: security.SigningKey(key)}
	iam_pb.RegisterSeaweedIdentityAccessManagementServer(srv, rec)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()

	// pb.ServerAddress treats host:HTTPPort and derives grpc as HTTPPort+10000.
	// The in-process server listens on whatever ephemeral port net.Listen
	// picked; subtract 10000 so IAMClient ends up dialing back to it.
	grpcPort := lis.Addr().(*net.TCPAddr).Port
	if grpcPort < 10001 {
		t.Skipf("ephemeral gRPC port %d too low to map to a valid HTTP port", grpcPort)
	}
	httpPort := grpcPort - 10000
	c := NewIAMClient(net.JoinHostPort("127.0.0.1", strconv.Itoa(httpPort)), key, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.GetUser(ctx, "alice"); err != nil {
		t.Fatalf("GetUser: %v", err)
	}

	if !rec.sawAuth {
		t.Fatal("filer never saw an authorization header")
	}
	if rec.authErr != nil {
		t.Fatalf("filer rejected the token: %v", rec.authErr)
	}
}

// TestIAMClient_GetUser_DialsFreshConnectionPerCall asserts each call dials a
// new transport: seaweedfs' shared gRPC cache could otherwise pin the operator
// to a filer that changed IP, so two calls must produce two accepted conns.
func TestIAMClient_GetUser_DialsFreshConnectionPerCall(t *testing.T) {
	key := []byte("test-jwt-signing-key")

	srv := grpc.NewServer()
	t.Cleanup(srv.Stop)
	rec := &authRecordingIAM{adminSigningKey: security.SigningKey(key)}
	iam_pb.RegisterSeaweedIdentityAccessManagementServer(srv, rec)

	baseListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	lis := &countingListener{Listener: baseListener}
	go func() { _ = srv.Serve(lis) }()

	grpcPort := baseListener.Addr().(*net.TCPAddr).Port
	if grpcPort < 10001 {
		t.Skipf("ephemeral gRPC port %d too low to map to a valid HTTP port", grpcPort)
	}
	httpPort := grpcPort - 10000
	c := NewIAMClient(net.JoinHostPort("127.0.0.1", strconv.Itoa(httpPort)), key, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		if _, err := c.GetUser(ctx, "alice"); err != nil {
			t.Fatalf("GetUser call %d: %v", i+1, err)
		}
	}

	if got := lis.accepts.Load(); got < 2 {
		t.Fatalf("accepted connections = %d, want at least 2 fresh connections", got)
	}
}

type countingListener struct {
	net.Listener
	accepts atomic.Int32
}

func (l *countingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		l.accepts.Add(1)
	}
	return conn, err
}

// authRecordingIAM is a minimal IAM gRPC server stand-in that records whether
// each request carried an authorization Bearer header and whether that header
// validates against adminSigningKey using the same DecodeJwt path the real
// filer uses. Only GetUser is exercised — that's enough to assert the auth
// plumbing.
type authRecordingIAM struct {
	iam_pb.UnimplementedSeaweedIdentityAccessManagementServer
	adminSigningKey security.SigningKey
	sawAuth         bool
	authErr         error
}

func (s *authRecordingIAM) GetUser(ctx context.Context, req *iam_pb.GetUserRequest) (*iam_pb.GetUserResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	hdr := md.Get("authorization")
	if len(hdr) == 0 {
		s.authErr = status.Error(codes.Unauthenticated, "missing authorization metadata")
		return nil, s.authErr
	}
	s.sawAuth = true
	parts := strings.Fields(hdr[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		s.authErr = status.Error(codes.Unauthenticated, "authorization header must use Bearer scheme")
		return nil, s.authErr
	}
	parsed, err := security.DecodeJwt(s.adminSigningKey, security.EncodedJwt(parts[1]), &security.SeaweedFilerAdminClaims{})
	if err != nil || parsed == nil || !parsed.Valid {
		s.authErr = status.Error(codes.Unauthenticated, "invalid admin token")
		return nil, s.authErr
	}
	return &iam_pb.GetUserResponse{Identity: &iam_pb.Identity{Name: req.Username}}, nil
}

// identityStoreIAM is a minimal in-memory IAM gRPC server that serves and
// stores one identity, enough to exercise the read-modify-write path
// UpdateAccessKey relies on. Messages cross the wire, so the served and
// stored identities are never aliased with the client's copy.
type identityStoreIAM struct {
	iam_pb.UnimplementedSeaweedIdentityAccessManagementServer
	mu       sync.Mutex
	identity *iam_pb.Identity
	updates  int

	// When gate is non-nil every GetUser blocks on it, and the first call
	// closes entered — enough to pin a read-modify-write open while another
	// call races it.
	gate    chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (s *identityStoreIAM) GetUser(_ context.Context, req *iam_pb.GetUserRequest) (*iam_pb.GetUserResponse, error) {
	if s.gate != nil {
		s.once.Do(func() { close(s.entered) })
		<-s.gate
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.identity == nil || s.identity.Name != req.Username {
		return nil, status.Errorf(codes.NotFound, "user %s not found", req.Username)
	}
	// Copy: the response is marshalled after this handler returns the lock.
	return &iam_pb.GetUserResponse{Identity: cloneIdentity(s.identity)}, nil
}

func (s *identityStoreIAM) UpdateUser(_ context.Context, req *iam_pb.UpdateUserRequest) (*iam_pb.UpdateUserResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.identity == nil || s.identity.Name != req.Username {
		return nil, status.Errorf(codes.NotFound, "user %s not found", req.Username)
	}
	s.updates++
	s.identity = req.Identity
	return &iam_pb.UpdateUserResponse{}, nil
}

func (s *identityStoreIAM) DeleteAccessKey(_ context.Context, req *iam_pb.DeleteAccessKeyRequest) (*iam_pb.DeleteAccessKeyResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.identity == nil || s.identity.Name != req.Username {
		return nil, status.Errorf(codes.NotFound, "user %s not found", req.Username)
	}
	kept := s.identity.Credentials[:0]
	for _, cred := range s.identity.Credentials {
		if cred.AccessKey != req.AccessKey {
			kept = append(kept, cred)
		}
	}
	s.identity.Credentials = kept
	return &iam_pb.DeleteAccessKeyResponse{}, nil
}

func (s *identityStoreIAM) accessKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.identity.Credentials))
	for _, cred := range s.identity.Credentials {
		keys = append(keys, cred.AccessKey)
	}
	return keys
}

func cloneIdentity(id *iam_pb.Identity) *iam_pb.Identity {
	out := &iam_pb.Identity{
		Name:        id.Name,
		Disabled:    id.Disabled,
		Account:     id.Account,
		PolicyNames: append([]string(nil), id.PolicyNames...),
	}
	for _, cred := range id.Credentials {
		out.Credentials = append(out.Credentials, &iam_pb.Credential{
			AccessKey: cred.AccessKey,
			SecretKey: cred.SecretKey,
			Status:    cred.Status,
		})
	}
	return out
}

// startIAMServer serves impl on an ephemeral port and returns a client dialing it.
func startIAMServer(t *testing.T, impl iam_pb.SeaweedIdentityAccessManagementServer) *IAMClient {
	t.Helper()
	srv := grpc.NewServer()
	t.Cleanup(srv.Stop)
	iam_pb.RegisterSeaweedIdentityAccessManagementServer(srv, impl)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()

	// pb.ServerAddress derives the gRPC port as HTTPPort+10000, so hand the
	// client the listening port minus 10000.
	grpcPort := lis.Addr().(*net.TCPAddr).Port
	if grpcPort < 10001 {
		t.Skipf("ephemeral gRPC port %d too low to map to a valid HTTP port", grpcPort)
	}
	return NewIAMClient(net.JoinHostPort("127.0.0.1", strconv.Itoa(grpcPort-10000)), nil, nil)
}

// UpdateAccessKey must rewrite only the secret key of the named access key,
// leaving the identity's other credentials and its policies intact — the IAM
// service has no update-credential RPC, so this goes through UpdateUser.
func TestIAMClient_UpdateAccessKey_RewritesSecretKeyInPlace(t *testing.T) {
	store := &identityStoreIAM{identity: &iam_pb.Identity{
		Name: "alice",
		Credentials: []*iam_pb.Credential{
			{AccessKey: "AKIAONE", SecretKey: "sk-one", Status: iam.AccessKeyStatusActive},
			{AccessKey: "AKIATWO", SecretKey: "sk-two", Status: iam.AccessKeyStatusActive},
		},
		PolicyNames: []string{"rw"},
	}}
	c := startIAMServer(t, store)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.UpdateAccessKey(ctx, "alice", "AKIAONE", "sk-rotated"); err != nil {
		t.Fatalf("UpdateAccessKey: %v", err)
	}

	user, err := c.GetUser(ctx, "alice")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got, _ := user.Credential("AKIAONE"); got.SecretKey != "sk-rotated" {
		t.Errorf("AKIAONE secret key = %q, want %q", got.SecretKey, "sk-rotated")
	}
	if got, _ := user.Credential("AKIATWO"); got.SecretKey != "sk-two" {
		t.Errorf("AKIATWO secret key = %q, want it untouched", got.SecretKey)
	}
	if len(user.PolicyNames) != 1 || user.PolicyNames[0] != "rw" {
		t.Errorf("policy names = %v, want [rw] preserved", user.PolicyNames)
	}
	// Fields the domain struct drops must survive the rewrite too.
	if got := store.identity.Credentials[0].Status; got != iam.AccessKeyStatusActive {
		t.Errorf("AKIAONE status = %q, want %q preserved", got, iam.AccessKeyStatusActive)
	}
}

// An access key the identity does not hold is reported as NotFound, so the
// caller can tell a drifted secret key from a key that was never registered.
func TestIAMClient_UpdateAccessKey_UnknownKeyIsNotFound(t *testing.T) {
	store := &identityStoreIAM{identity: &iam_pb.Identity{
		Name:        "alice",
		Credentials: []*iam_pb.Credential{{AccessKey: "AKIAONE", SecretKey: "sk-one"}},
	}}
	c := startIAMServer(t, store)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := c.UpdateAccessKey(ctx, "alice", "AKIAGONE", "sk-rotated")
	if status.Code(err) != codes.NotFound {
		t.Fatalf("UpdateAccessKey error = %v, want codes.NotFound", err)
	}
	if store.updates != 0 {
		t.Errorf("UpdateUser calls = %d, want none", store.updates)
	}
}

// A credential delete that lands mid-rewrite must not be undone: UpdateAccessKey
// writes the whole identity it read, so DeleteAccessKey has to wait for it
// rather than be resurrected by the stale copy.
func TestIAMClient_UpdateAccessKey_SerializesWithDelete(t *testing.T) {
	store := &identityStoreIAM{
		identity: &iam_pb.Identity{
			Name: "alice",
			Credentials: []*iam_pb.Credential{
				{AccessKey: "AKIAONE", SecretKey: "sk-one"},
				{AccessKey: "AKIATWO", SecretKey: "sk-two"},
			},
		},
		gate:    make(chan struct{}),
		entered: make(chan struct{}),
	}
	c := startIAMServer(t, store)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	updateDone := make(chan error, 1)
	go func() { updateDone <- c.UpdateAccessKey(ctx, "alice", "AKIAONE", "sk-rotated") }()
	select {
	case <-store.entered:
	case <-ctx.Done():
		t.Fatal("UpdateAccessKey never reached GetUser")
	}

	// The delete now races the rewrite; it must block on the per-user lock.
	deleteDone := make(chan error, 1)
	go func() { deleteDone <- c.DeleteAccessKey(ctx, "alice", "AKIATWO") }()
	select {
	case err := <-deleteDone:
		t.Fatalf("DeleteAccessKey ran during the rewrite (err=%v); it must wait for the lock", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(store.gate)
	if err := <-updateDone; err != nil {
		t.Fatalf("UpdateAccessKey: %v", err)
	}
	if err := <-deleteDone; err != nil {
		t.Fatalf("DeleteAccessKey: %v", err)
	}

	if keys := store.accessKeys(); len(keys) != 1 || keys[0] != "AKIAONE" {
		t.Fatalf("access keys = %v, want only AKIAONE — the revoked key must stay revoked", keys)
	}
}
