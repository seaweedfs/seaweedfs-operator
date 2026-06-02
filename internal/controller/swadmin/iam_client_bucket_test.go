package swadmin

import (
	"context"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seaweedfs/seaweedfs/weed/pb/iam_pb"
	"github.com/seaweedfs/seaweedfs/weed/security"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// TestIAMClient_SetBucketAccess_SendsBearer reproduces
// https://github.com/seaweedfs/seaweedfs-operator/issues/265 end to end:
// granting bucket access hits the filer's IAM gRPC service, which rejects
// unauthenticated calls once jwt.filer_signing.key is set. With the key wired,
// SetBucketAccess presents a valid admin Bearer token and the grant lands. The
// previous bucket controller sent none, so every access reconcile failed and
// Bucket CRDs stuck in Failed with reason AccessFailed.
func TestIAMClient_SetBucketAccess_SendsBearer(t *testing.T) {
	key := []byte("test-jwt-signing-key")
	srv := newBucketAccessIAM(key)
	filer := startBucketAccessIAM(t, srv)

	c := NewIAMClient(filer, key)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.SetBucketAccess(ctx, "photos", "uploader", "Read,Write"); err != nil {
		t.Fatalf("SetBucketAccess: %v", err)
	}
	if !srv.authSeen {
		t.Fatal("filer IAM service never saw a valid Bearer token")
	}
	assertActions(t, srv.actions("uploader"), "Read:photos", "Write:photos")
}

// TestIAMClient_SetBucketAccess_NoKeyUnauthenticated pins the failing path the
// fix repairs: with no signing key the call carries no Bearer token and the
// filer rejects it — the AccessFailed symptom on secured clusters.
func TestIAMClient_SetBucketAccess_NoKeyUnauthenticated(t *testing.T) {
	srv := newBucketAccessIAM([]byte("test-jwt-signing-key"))
	filer := startBucketAccessIAM(t, srv)

	c := NewIAMClient(filer, nil) // no signing key wired
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := c.SetBucketAccess(ctx, "photos", "uploader", "Read,Write")
	if err == nil {
		t.Fatal("expected SetBucketAccess to fail without a signing key")
	}
	if st, _ := status.FromError(err); st.Code() != codes.Unauthenticated {
		t.Fatalf("expected codes.Unauthenticated, got: %v", err)
	}
	if srv.authSeen {
		t.Fatal("filer should not have seen a valid Bearer token")
	}
}

// TestIAMClient_SetBucketAccess_PreservesOtherGrants confirms the
// read-modify-write only rewrites the target bucket's actions: grants on other
// buckets and attached policies survive a set, and "none" strips just this
// bucket. Mirrors `weed shell s3.bucket.access`.
func TestIAMClient_SetBucketAccess_PreservesOtherGrants(t *testing.T) {
	key := []byte("test-jwt-signing-key")
	srv := newBucketAccessIAM(key, &iam_pb.Identity{
		Name:        "uploader",
		Actions:     []string{"Read:other", "Admin:photos"},
		PolicyNames: []string{"p1"},
	})
	filer := startBucketAccessIAM(t, srv)

	c := NewIAMClient(filer, key)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.SetBucketAccess(ctx, "photos", "uploader", "List"); err != nil {
		t.Fatalf("SetBucketAccess set: %v", err)
	}
	assertActions(t, srv.actions("uploader"), "Read:other", "List:photos")

	if err := c.SetBucketAccess(ctx, "photos", "uploader", "none"); err != nil {
		t.Fatalf("SetBucketAccess none: %v", err)
	}
	assertActions(t, srv.actions("uploader"), "Read:other")
	if got := srv.policies("uploader"); len(got) != 1 || got[0] != "p1" {
		t.Fatalf("attached policies clobbered: %v", got)
	}
}

func assertActions(t *testing.T, got []string, want ...string) {
	t.Helper()
	w := make(map[string]bool, len(want))
	for _, a := range want {
		w[a] = true
	}
	if len(got) != len(w) {
		t.Fatalf("actions = %v, want %v", got, want)
	}
	for _, a := range got {
		if !w[a] {
			t.Fatalf("unexpected action %q in %v", a, got)
		}
	}
}

// startBucketAccessIAM serves srv on an ephemeral port and returns the filer
// HTTP host:port the operator passes to NewIAMClient (seaweedfs derives the
// gRPC port as +10000).
func startBucketAccessIAM(t *testing.T, srv *bucketAccessIAM) string {
	t.Helper()
	gsrv := grpc.NewServer()
	iam_pb.RegisterSeaweedIdentityAccessManagementServer(gsrv, srv)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(gsrv.Stop)
	go func() { _ = gsrv.Serve(lis) }()
	grpcPort := lis.Addr().(*net.TCPAddr).Port
	if grpcPort < 10001 {
		t.Skipf("ephemeral gRPC port %d too low to map to a valid HTTP port", grpcPort)
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(grpcPort-10000))
}

// bucketAccessIAM is an in-process IAM gRPC server that rejects any call
// lacking a valid admin Bearer token (mirroring the filer's checkAdminAuth when
// jwt.filer_signing.key is set) and keeps identities in memory, so the
// GetUser→mutate→Create/Update sequence in SetBucketAccess can be asserted.
type bucketAccessIAM struct {
	iam_pb.UnimplementedSeaweedIdentityAccessManagementServer
	key security.SigningKey

	mu       sync.Mutex
	users    map[string]*iam_pb.Identity
	authSeen bool
	authErr  error
}

func newBucketAccessIAM(key []byte, seed ...*iam_pb.Identity) *bucketAccessIAM {
	s := &bucketAccessIAM{key: security.SigningKey(key), users: map[string]*iam_pb.Identity{}}
	for _, id := range seed {
		s.users[id.Name] = id
	}
	return s
}

func (s *bucketAccessIAM) auth(ctx context.Context) error {
	md, _ := metadata.FromIncomingContext(ctx)
	hdr := md.Get("authorization")
	if len(hdr) == 0 {
		s.authErr = status.Error(codes.Unauthenticated, "missing authorization metadata")
		return s.authErr
	}
	parts := strings.Fields(hdr[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		s.authErr = status.Error(codes.Unauthenticated, "authorization header must use Bearer scheme")
		return s.authErr
	}
	parsed, err := security.DecodeJwt(s.key, security.EncodedJwt(parts[1]), &security.SeaweedFilerAdminClaims{})
	if err != nil || parsed == nil || !parsed.Valid {
		s.authErr = status.Error(codes.Unauthenticated, "invalid admin token")
		return s.authErr
	}
	s.authSeen = true
	return nil
}

func (s *bucketAccessIAM) GetUser(ctx context.Context, req *iam_pb.GetUserRequest) (*iam_pb.GetUserResponse, error) {
	if err := s.auth(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.users[req.Username]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "user %s not found", req.Username)
	}
	return &iam_pb.GetUserResponse{Identity: id}, nil
}

func (s *bucketAccessIAM) CreateUser(ctx context.Context, req *iam_pb.CreateUserRequest) (*iam_pb.CreateUserResponse, error) {
	if err := s.auth(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[req.Identity.Name] = req.Identity
	return &iam_pb.CreateUserResponse{}, nil
}

func (s *bucketAccessIAM) UpdateUser(ctx context.Context, req *iam_pb.UpdateUserRequest) (*iam_pb.UpdateUserResponse, error) {
	if err := s.auth(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[req.Username] = req.Identity
	return &iam_pb.UpdateUserResponse{}, nil
}

func (s *bucketAccessIAM) actions(user string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.users[user]; ok {
		return append([]string(nil), id.Actions...)
	}
	return nil
}

func (s *bucketAccessIAM) policies(user string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.users[user]; ok {
		return append([]string(nil), id.PolicyNames...)
	}
	return nil
}
