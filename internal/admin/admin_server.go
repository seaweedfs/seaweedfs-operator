package admin

import (
	"context"
	"fmt"
	"time"

	"github.com/seaweedfs/seaweedfs/weed/cluster"
	"github.com/seaweedfs/seaweedfs/weed/credential"
	_ "github.com/seaweedfs/seaweedfs/weed/credential/filer_etc"
	"github.com/seaweedfs/seaweedfs/weed/operation"
	"github.com/seaweedfs/seaweedfs/weed/pb"
	"github.com/seaweedfs/seaweedfs/weed/pb/filer_pb"
	"github.com/seaweedfs/seaweedfs/weed/pb/master_pb"
	"github.com/seaweedfs/seaweedfs/weed/pb/volume_server_pb"
	"github.com/seaweedfs/seaweedfs/weed/security"
	"github.com/seaweedfs/seaweedfs/weed/util"
	"github.com/seaweedfs/seaweedfs/weed/wdclient"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// AdminServer provides a simplified interface for SeaweedFS admin operations
type AdminServer struct {
	masterClient    *wdclient.MasterClient
	grpcDialOption  grpc.DialOption
	cacheExpiration time.Duration

	// Filer discovery and caching
	cachedFilers         []string
	lastFilerUpdate      time.Time
	filerCacheExpiration time.Duration

	// Credential management
	credentialManager *credential.CredentialManager

	// Logger
	log *zap.SugaredLogger
}

// NewAdminServer creates a new AdminServer instance
func NewAdminServer(masters string, log *zap.SugaredLogger) *AdminServer {
	grpcDialOption := security.LoadClientTLS(util.GetViper(), "grpc.client")

	// Create master client with multiple master support
	masterClient := wdclient.NewMasterClient(
		grpcDialOption,
		"",      // filerGroup - not needed for admin
		"admin", // clientType
		"",      // clientHost - not needed for admin
		"",      // dataCenter - not needed for admin
		"",      // rack - not needed for admin
		*pb.ServerAddresses(masters).ToServiceDiscovery(),
	)

	// Start master client connection process
	ctx := context.Background()
	go masterClient.KeepConnectedToMaster(ctx)

	server := &AdminServer{
		masterClient:         masterClient,
		grpcDialOption:       grpcDialOption,
		cacheExpiration:      10 * time.Second,
		filerCacheExpiration: 30 * time.Second, // Cache filers for 30 seconds
		log:                  log,
	}

	// Note: Credential manager initialization is deferred until first use
	// to ensure filer address is available

	return server
}

// WithMasterClient executes a function with a master client connection
func (s *AdminServer) WithMasterClient(f func(client master_pb.SeaweedClient) error) error {
	return s.masterClient.WithClient(false, f)
}

// WithFilerClient executes a function with a filer client connection
func (s *AdminServer) WithFilerClient(f func(client filer_pb.SeaweedFilerClient) error) error {
	filerAddr := s.GetFilerAddress()
	if filerAddr == "" {
		return fmt.Errorf("no filer available")
	}

	return pb.WithGrpcFilerClient(false, 0, pb.ServerAddress(filerAddr), s.grpcDialOption, func(client filer_pb.SeaweedFilerClient) error {
		return f(client)
	})
}

// WithVolumeServerClient executes a function with a volume server client connection
func (s *AdminServer) WithVolumeServerClient(address pb.ServerAddress, f func(client volume_server_pb.VolumeServerClient) error) error {
	return operation.WithVolumeServerClient(false, address, s.grpcDialOption, func(client volume_server_pb.VolumeServerClient) error {
		return f(client)
	})
}

// GetFilerAddress returns a filer address, discovering from masters if needed
func (s *AdminServer) GetFilerAddress() string {
	// Discover filers from masters
	filers := s.getDiscoveredFilers()
	if len(filers) > 0 {
		return filers[0] // Return the first available filer
	}

	return ""
}

// getDiscoveredFilers returns cached filers or discovers them from masters
func (s *AdminServer) getDiscoveredFilers() []string {
	// Check if cache is still valid
	if time.Since(s.lastFilerUpdate) < s.filerCacheExpiration && len(s.cachedFilers) > 0 {
		return s.cachedFilers
	}

	// Discover filers from masters
	var filers []string
	err := s.WithMasterClient(func(client master_pb.SeaweedClient) error {
		resp, err := client.ListClusterNodes(context.Background(), &master_pb.ListClusterNodesRequest{
			ClientType: cluster.FilerType,
		})
		if err != nil {
			return err
		}

		for _, node := range resp.ClusterNodes {
			filers = append(filers, node.Address)
		}

		return nil
	})

	if err != nil {
		currentMaster := s.masterClient.GetMaster(context.Background())
		s.log.Debugw("Failed to discover filers from master", "master", currentMaster, "error", err)
		// Return cached filers even if expired, better than nothing
		return s.cachedFilers
	}

	// Update cache
	s.cachedFilers = filers
	s.lastFilerUpdate = time.Now()

	return filers
}

// GetAllFilers returns all discovered filers
func (s *AdminServer) GetAllFilers() []string {
	return s.getDiscoveredFilers()
}

// ensureCredentialManager initializes the credential manager if not already done
func (s *AdminServer) ensureCredentialManager() error {
	if s.credentialManager != nil {
		return nil
	}

	// Create credential manager with default store (empty string uses default)
	credentialManager, err := credential.NewCredentialManagerWithDefaults("")
	if err != nil {
		return fmt.Errorf("failed to initialize credential manager: %w", err)
	}

	// For stores that need filer address function, configure them
	// This follows the upstream implementation pattern
	if store := credentialManager.GetStore(); store != nil {
		if filerFuncSetter, ok := store.(interface {
			SetFilerAddressFunc(func() pb.ServerAddress, grpc.DialOption)
		}); ok {
			// Configure the filer address function to dynamically return the current active filer
			// This function will be called each time credentials need to be loaded/saved,
			// so it will automatically use whatever filer is currently available (HA-aware)
			filerFuncSetter.SetFilerAddressFunc(func() pb.ServerAddress {
				return pb.ServerAddress(s.GetFilerAddress())
			}, s.grpcDialOption)
			s.log.Debugw("Credential store configured with dynamic filer address function", "store", store.GetName())
		} else {
			s.log.Debugw("Credential store does not support filer address function", "store", store.GetName())
			// This is not an error - some stores may not need filer address
		}
	}

	s.credentialManager = credentialManager
	s.log.Debugw("Initialized credential manager", "store", credentialManager.GetStore().GetName())

	return nil
}
