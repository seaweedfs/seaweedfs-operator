package swadmin

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

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
	c := NewIAMClient("seaweed-filer.invalid:8888", key)

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
	c := NewIAMClient("seaweed-filer.invalid:8888", nil)
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
// withClient → pb.WithGrpcClient → unary call path.
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
	c := NewIAMClient(net.JoinHostPort("127.0.0.1", strconv.Itoa(httpPort)), key)

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

