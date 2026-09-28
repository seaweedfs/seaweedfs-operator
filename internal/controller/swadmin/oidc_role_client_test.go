package swadmin

import (
	"context"
	"sync"
	"testing"

	"github.com/seaweedfs/seaweedfs/weed/pb/iam_pb"
)

// stsRecordingIAM records the OIDC provider and role requests it receives.
type stsRecordingIAM struct {
	iam_pb.UnimplementedSeaweedIdentityAccessManagementServer
	mu             sync.Mutex
	putProvider    *iam_pb.PutOIDCProviderRequest
	deleteProvider *iam_pb.DeleteOIDCProviderRequest
	putRole        *iam_pb.PutRoleRequest
	deleteRole     *iam_pb.DeleteRoleRequest
}

func (s *stsRecordingIAM) PutOIDCProvider(_ context.Context, req *iam_pb.PutOIDCProviderRequest) (*iam_pb.PutOIDCProviderResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.putProvider = req
	return &iam_pb.PutOIDCProviderResponse{Arn: "arn:aws:iam:::oidc-provider/oidc.example.org"}, nil
}

func (s *stsRecordingIAM) DeleteOIDCProvider(_ context.Context, req *iam_pb.DeleteOIDCProviderRequest) (*iam_pb.DeleteOIDCProviderResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteProvider = req
	return &iam_pb.DeleteOIDCProviderResponse{}, nil
}

func (s *stsRecordingIAM) PutRole(_ context.Context, req *iam_pb.PutRoleRequest) (*iam_pb.PutRoleResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.putRole = req
	return &iam_pb.PutRoleResponse{RoleArn: "arn:aws:iam::role/" + req.GetRole().GetRoleName()}, nil
}

func (s *stsRecordingIAM) DeleteRole(_ context.Context, req *iam_pb.DeleteRoleRequest) (*iam_pb.DeleteRoleResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteRole = req
	return &iam_pb.DeleteRoleResponse{}, nil
}

func TestIAMClient_PutAndDeleteOIDCProvider(t *testing.T) {
	srv := &stsRecordingIAM{}
	c := startIAMServer(t, srv)
	arn, err := c.PutOIDCProvider(context.Background(), OIDCProvider{
		IssuerURL: "https://oidc.example.org", ClientIDs: []string{"s3"},
		Thumbprints: []string{"9e99a48a9960b14926bb7f3b02e22da2b0ab7280"}, AccountID: "111122223333",
	})
	if err != nil {
		t.Fatalf("PutOIDCProvider: %v", err)
	}
	if arn != "arn:aws:iam:::oidc-provider/oidc.example.org" {
		t.Errorf("arn = %q", arn)
	}
	got := srv.putProvider
	if got.GetIssuerUrl() != "https://oidc.example.org" || len(got.GetClientIds()) != 1 || got.GetClientIds()[0] != "s3" ||
		len(got.GetThumbprints()) != 1 || got.GetAccountId() != "111122223333" {
		t.Errorf("PutOIDCProvider sent %v", got)
	}

	if err := c.DeleteOIDCProvider(context.Background(), "https://oidc.example.org"); err != nil {
		t.Fatalf("DeleteOIDCProvider: %v", err)
	}
	if srv.deleteProvider.GetIssuerUrl() != "https://oidc.example.org" {
		t.Errorf("DeleteOIDCProvider sent %v", srv.deleteProvider)
	}
}

func TestIAMClient_PutAndDeleteRole(t *testing.T) {
	srv := &stsRecordingIAM{}
	c := startIAMServer(t, srv)
	arn, err := c.PutRole(context.Background(), IAMRole{
		Name: "app", TrustPolicy: `{"Version":"2012-10-17"}`, AttachedPolicies: []string{"rw"},
		Description: "app runtime", MaxSessionDuration: 3600,
	})
	if err != nil {
		t.Fatalf("PutRole: %v", err)
	}
	if arn != "arn:aws:iam::role/app" {
		t.Errorf("arn = %q", arn)
	}
	got := srv.putRole.GetRole()
	if got.GetRoleName() != "app" || got.GetTrustPolicy() != `{"Version":"2012-10-17"}` || len(got.GetAttachedPolicies()) != 1 ||
		got.GetDescription() != "app runtime" || got.GetMaxSessionDuration() != 3600 {
		t.Errorf("PutRole sent %v", got)
	}

	if err := c.DeleteRole(context.Background(), "app"); err != nil {
		t.Fatalf("DeleteRole: %v", err)
	}
	if srv.deleteRole.GetRoleName() != "app" {
		t.Errorf("DeleteRole sent %v", srv.deleteRole)
	}
}
