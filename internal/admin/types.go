package admin

import (
	"time"

	"github.com/seaweedfs/seaweedfs/weed/pb/master_pb"
)

// Core cluster topology structures
type ClusterTopology struct {
	Masters       []MasterNode   `json:"masters"`
	DataCenters   []DataCenter   `json:"datacenters"`
	VolumeServers []VolumeServer `json:"volume_servers"`
	TotalVolumes  int            `json:"total_volumes"`
	TotalFiles    int64          `json:"total_files"`
	TotalSize     int64          `json:"total_size"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

type MasterNode struct {
	Address  string `json:"address"`
	IsLeader bool   `json:"is_leader"`
}

type DataCenter struct {
	ID    string `json:"id"`
	Racks []Rack `json:"racks"`
}

type Rack struct {
	ID    string         `json:"id"`
	Nodes []VolumeServer `json:"nodes"`
}

type VolumeServer struct {
	ID            string    `json:"id"`
	Address       string    `json:"address"`
	DataCenter    string    `json:"datacenter"`
	Rack          string    `json:"rack"`
	PublicURL     string    `json:"public_url"`
	Volumes       int       `json:"volumes"`
	MaxVolumes    int       `json:"max_volumes"`
	DiskUsage     int64     `json:"disk_usage"`
	DiskCapacity  int64     `json:"disk_capacity"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
}

// S3 Bucket management structures
type S3Bucket struct {
	Name               string    `json:"name"`
	CreatedAt          time.Time `json:"created_at"`
	Size               int64     `json:"size"`
	ObjectCount        int64     `json:"object_count"`
	LastModified       time.Time `json:"last_modified"`
	Quota              int64     `json:"quota"`                // Quota in bytes, 0 means no quota
	QuotaEnabled       bool      `json:"quota_enabled"`        // Whether quota is enabled
	VersioningEnabled  bool      `json:"versioning_enabled"`   // Whether versioning is enabled
	ObjectLockEnabled  bool      `json:"object_lock_enabled"`  // Whether object lock is enabled
	ObjectLockMode     string    `json:"object_lock_mode"`     // Object lock mode: "GOVERNANCE" or "COMPLIANCE"
	ObjectLockDuration int32     `json:"object_lock_duration"` // Default retention duration in days
}

type S3Object struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	LastModified time.Time `json:"last_modified"`
	ETag         string    `json:"etag"`
	StorageClass string    `json:"storage_class"`
}

type BucketDetails struct {
	Bucket     S3Bucket   `json:"bucket"`
	Objects    []S3Object `json:"objects"`
	TotalSize  int64      `json:"total_size"`
	TotalCount int64      `json:"total_count"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// Volume management structures
type VolumeWithTopology struct {
	*master_pb.VolumeInformationMessage
	Server     string `json:"server"`
	DataCenter string `json:"datacenter"`
	Rack       string `json:"rack"`
}
