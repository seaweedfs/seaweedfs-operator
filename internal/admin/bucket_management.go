package admin

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/seaweedfs/seaweedfs/weed/pb/filer_pb"
	"github.com/seaweedfs/seaweedfs/weed/pb/master_pb"
	"github.com/seaweedfs/seaweedfs/weed/s3api"
)

//#region GetS3Buckets

// GetS3Buckets retrieves all Object Store buckets from the filer and collects size/object data from collections
func (s *AdminServer) GetS3Buckets() ([]S3Bucket, error) {
	var buckets []S3Bucket

	// Build a map of collection name to collection data
	collectionMap := make(map[string]struct {
		Size      int64
		FileCount int64
	})

	s.log.Debug("getting volume information")

	// Collect volume information by collection
	err := s.WithMasterClient(func(client master_pb.SeaweedClient) error {
		s.log.Debugw("getting volume list from master")

		resp, err := client.VolumeList(context.Background(), &master_pb.VolumeListRequest{})

		s.log.Debugw("got volume list response", "resp", resp)

		if err != nil {
			s.log.Error(err, "Failed to get volume list")
			return err
		}

		if resp.TopologyInfo != nil {
			s.log.Debugw("processing topology info")
			for _, dc := range resp.TopologyInfo.DataCenterInfos {
				for _, rack := range dc.RackInfos {
					for _, node := range rack.DataNodeInfos {
						for _, diskInfo := range node.DiskInfos {
							for _, volInfo := range diskInfo.VolumeInfos {
								s.log.Debugw("processing volume", "collection", volInfo.Collection, "size", volInfo.Size, "fileCount", volInfo.FileCount)
								collection := volInfo.Collection
								if collection == "" {
									collection = "default"
								}

								if _, exists := collectionMap[collection]; !exists {
									collectionMap[collection] = struct {
										Size      int64
										FileCount int64
									}{}
								}

								data := collectionMap[collection]
								data.Size += int64(volInfo.Size)
								data.FileCount += int64(volInfo.FileCount)
								collectionMap[collection] = data
							}
						}
					}
				}
			}
		}

		s.log.Debugw("Completed volume information collection", "collections", len(collectionMap))

		return nil
	})

	if err != nil {
		s.log.Error(err, "Failed to get volume information")
		return nil, fmt.Errorf("failed to get volume information: %w", err)
	}

	// Get filer configuration to determine FilerGroup
	var filerGroup string

	s.log.Debugw("getting filer configuration")

	err = s.WithFilerClient(func(client filer_pb.SeaweedFilerClient) error {
		configResp, err := client.GetFilerConfiguration(context.Background(), &filer_pb.GetFilerConfigurationRequest{})
		if err != nil {
			s.log.Error(err, "Failed to get filer configuration")
			// Continue without filer group
			return nil
		}

		filerGroup = configResp.FilerGroup
		s.log.Debugw("got filer group", "filerGroup", filerGroup)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to get filer configuration: %w", err)
	}

	s.log.Debugw("Starting bucket listing process")

	// Now list buckets from the filer and match with collection data
	err = s.WithFilerClient(func(client filer_pb.SeaweedFilerClient) error {
		s.log.Debugw("Listing buckets from filer")
		// List buckets by looking at the /buckets directory
		stream, err := client.ListEntries(context.Background(), &filer_pb.ListEntriesRequest{
			Directory:          "/buckets",
			Prefix:             "",
			StartFromFileName:  "",
			InclusiveStartFrom: false,
			Limit:              1000,
		})
		if err != nil {
			return err
		}

		s.log.Debugw("got bucket listing stream")

		for {
			resp, err := stream.Recv()

			if err != nil {
				if err.Error() == "EOF" {
					break
				}
				return err
			}

			s.log.Debugw("processing bucket entry", "name", resp.Entry.Name, "isDirectory", resp.Entry.IsDirectory)

			if resp.Entry.IsDirectory {
				bucketName := resp.Entry.Name

				// Determine collection name for this bucket
				var collectionName string
				if filerGroup != "" {
					collectionName = fmt.Sprintf("%s_%s", filerGroup, bucketName)
				} else {
					collectionName = bucketName
				}

				// Get size and object count from collection data
				var size int64
				var objectCount int64
				if collectionData, exists := collectionMap[collectionName]; exists {
					size = collectionData.Size
					objectCount = collectionData.FileCount
				}

				// Get quota information from entry
				quota := resp.Entry.Quota
				quotaEnabled := quota > 0
				if quota < 0 {
					// Negative quota means disabled
					quota = -quota
					quotaEnabled = false
				}

				// Get versioning and object lock information from extended attributes
				versioningEnabled := false
				objectLockEnabled := false
				objectLockMode := ""
				var objectLockDuration int32 = 0

				if resp.Entry.Extended != nil {
					// Use shared utility to extract versioning information
					versioningEnabled = extractVersioningFromEntry(resp.Entry)

					// Use shared utility to extract Object Lock information
					objectLockEnabled, objectLockMode, objectLockDuration = extractObjectLockInfoFromEntry(resp.Entry)
				}

				bucket := S3Bucket{
					Name:               bucketName,
					CreatedAt:          time.Unix(resp.Entry.Attributes.Crtime, 0),
					Size:               size,
					ObjectCount:        objectCount,
					LastModified:       time.Unix(resp.Entry.Attributes.Mtime, 0),
					Quota:              quota,
					QuotaEnabled:       quotaEnabled,
					VersioningEnabled:  versioningEnabled,
					ObjectLockEnabled:  objectLockEnabled,
					ObjectLockMode:     objectLockMode,
					ObjectLockDuration: objectLockDuration,
				}
				buckets = append(buckets, bucket)
			}
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to list Object Store buckets: %w", err)
	}

	s.log.Debugw("successfully retrieved S3 buckets", "count", len(buckets))
	return buckets, nil
}

//#endregion

//#region GetBucketDetails

// GetBucketDetails retrieves detailed information about a specific bucket
func (s *AdminServer) GetBucketDetails(bucketName string, includeObjects bool) (*BucketDetails, error) {
	bucketPath := fmt.Sprintf("/buckets/%s", bucketName)

	details := &BucketDetails{
		Bucket: S3Bucket{
			Name: bucketName,
		},
		Objects:   []S3Object{},
		UpdatedAt: time.Now(),
	}

	err := s.WithFilerClient(func(client filer_pb.SeaweedFilerClient) error {
		// Get bucket info
		bucketResp, err := client.LookupDirectoryEntry(context.Background(), &filer_pb.LookupDirectoryEntryRequest{
			Directory: "/buckets",
			Name:      bucketName,
		})
		if err != nil {
			return fmt.Errorf("bucket not found: %w", err)
		}

		details.Bucket.CreatedAt = time.Unix(bucketResp.Entry.Attributes.Crtime, 0)
		details.Bucket.LastModified = time.Unix(bucketResp.Entry.Attributes.Mtime, 0)

		// Get quota information from entry
		quota := bucketResp.Entry.Quota
		quotaEnabled := quota > 0
		if quota < 0 {
			// Negative quota means disabled
			quota = -quota
			quotaEnabled = false
		}
		details.Bucket.Quota = quota
		details.Bucket.QuotaEnabled = quotaEnabled

		// Get versioning and object lock information from extended attributes
		versioningEnabled := false
		objectLockEnabled := false
		objectLockMode := ""
		var objectLockDuration int32 = 0

		if bucketResp.Entry.Extended != nil {
			// Use shared utility to extract versioning information
			versioningEnabled = extractVersioningFromEntry(bucketResp.Entry)

			// Use shared utility to extract Object Lock information
			objectLockEnabled, objectLockMode, objectLockDuration = extractObjectLockInfoFromEntry(bucketResp.Entry)
		}

		details.Bucket.VersioningEnabled = versioningEnabled
		details.Bucket.ObjectLockEnabled = objectLockEnabled
		details.Bucket.ObjectLockMode = objectLockMode
		details.Bucket.ObjectLockDuration = objectLockDuration

		if includeObjects {
			// List objects in bucket (recursively)
			return s.listBucketObjects(client, bucketPath, "", details)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return details, nil
}

//#endregion

//#region listBucketObjects

// listBucketObjects recursively lists all objects in a bucket
func (s *AdminServer) listBucketObjects(client filer_pb.SeaweedFilerClient, directory, prefix string, details *BucketDetails) error {
	stream, err := client.ListEntries(context.Background(), &filer_pb.ListEntriesRequest{
		Directory:          directory,
		Prefix:             prefix,
		StartFromFileName:  "",
		InclusiveStartFrom: false,
		Limit:              1000,
	})
	if err != nil {
		return err
	}

	for {
		resp, err := stream.Recv()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return err
		}

		entry := resp.Entry
		if entry.IsDirectory {
			// Recursively list subdirectories
			subDir := fmt.Sprintf("%s/%s", directory, entry.Name)
			err := s.listBucketObjects(client, subDir, "", details)
			if err != nil {
				return err
			}
		} else {
			// Add file object
			objectKey := entry.Name
			if directory != fmt.Sprintf("/buckets/%s", details.Bucket.Name) {
				// Remove bucket prefix to get relative path
				relativePath := directory[len(fmt.Sprintf("/buckets/%s", details.Bucket.Name))+1:]
				objectKey = fmt.Sprintf("%s/%s", relativePath, entry.Name)
			}

			obj := S3Object{
				Key:          objectKey,
				Size:         int64(entry.Attributes.FileSize),
				LastModified: time.Unix(entry.Attributes.Mtime, 0),
				ETag:         "", // Could be calculated from chunks if needed
				StorageClass: "STANDARD",
			}

			details.Objects = append(details.Objects, obj)
			details.TotalSize += obj.Size
			details.TotalCount++
		}
	}

	// Update bucket totals
	details.Bucket.Size = details.TotalSize
	details.Bucket.ObjectCount = details.TotalCount

	return nil
}

//#endregion

//#region CreateS3Bucket

// CreateS3Bucket creates a new S3 bucket
func (s *AdminServer) CreateS3Bucket(bucketName string) error {
	return s.CreateS3BucketWithQuota(bucketName, 0, false)
}

//#endregion

//#region CreateS3BucketWithQuota

// CreateS3BucketWithQuota creates a new S3 bucket with optional quota
func (s *AdminServer) CreateS3BucketWithQuota(bucketName string, quotaBytes int64, quotaEnabled bool) error {
	return s.CreateS3BucketWithObjectLock(bucketName, quotaBytes, quotaEnabled, false, false, "", 0)
}

//#endregion

//#region CreateS3BucketWithObjectLock

// CreateS3BucketWithObjectLock creates a new S3 bucket with object lock configuration
func (s *AdminServer) CreateS3BucketWithObjectLock(bucketName string, quotaBytes int64, quotaEnabled, versioningEnabled, objectLockEnabled bool, objectLockMode string, objectLockDuration int32) error {
	return s.WithFilerClient(func(client filer_pb.SeaweedFilerClient) error {
		// Object lock requires versioning to be enabled
		if objectLockEnabled && !versioningEnabled {
			versioningEnabled = true
			s.log.Debugw("Object lock enabled, forcing versioning to be enabled", "bucketName", bucketName)
		}

		if len(bucketName) > 63 {
			return fmt.Errorf("bucket name must be less than 63 characters")
		} else if len(bucketName) < 3 {
			return fmt.Errorf("bucket name must be at least 3 characters")
		}

		// First ensure /buckets directory exists
		_, err := client.CreateEntry(context.Background(), &filer_pb.CreateEntryRequest{
			Directory: "/",
			Entry: &filer_pb.Entry{
				Name:        "buckets",
				IsDirectory: true,
				Attributes: &filer_pb.FuseAttributes{
					FileMode: uint32(0755 | os.ModeDir), // Directory mode
					Uid:      uint32(1000),
					Gid:      uint32(1000),
					Crtime:   time.Now().Unix(),
					Mtime:    time.Now().Unix(),
					TtlSec:   0,
				},
			},
		})

		// Ignore error if directory already exists
		if err != nil && !strings.Contains(err.Error(), "already exists") && !strings.Contains(err.Error(), "existing entry") {
			return fmt.Errorf("failed to create /buckets directory: %w", err)
		}

		// Check if bucket already exists
		_, err = client.LookupDirectoryEntry(context.Background(), &filer_pb.LookupDirectoryEntryRequest{
			Directory: "/buckets",
			Name:      bucketName,
		})

		if err == nil {
			return fmt.Errorf("bucket %s already exists", bucketName)
		}

		// Determine quota value (negative if disabled)
		var quota int64
		if quotaEnabled && quotaBytes > 0 {
			quota = quotaBytes
		} else if !quotaEnabled && quotaBytes > 0 {
			quota = -quotaBytes
		} else {
			quota = 0
		}

		// Prepare bucket attributes with versioning and object lock metadata
		attributes := &filer_pb.FuseAttributes{
			FileMode: uint32(0755 | os.ModeDir), // Directory mode
			Uid:      filer_pb.OS_UID,
			Gid:      filer_pb.OS_GID,
			Crtime:   time.Now().Unix(),
			Mtime:    time.Now().Unix(),
			TtlSec:   0,
		}

		// Create extended attributes map for versioning
		extended := make(map[string][]byte)

		// Create bucket entry
		bucketEntry := &filer_pb.Entry{
			Name:        bucketName,
			IsDirectory: true,
			Attributes:  attributes,
			Extended:    extended,
			Quota:       quota,
		}

		// Handle versioning using shared utilities
		if err := s3api.StoreVersioningInExtended(bucketEntry, versioningEnabled); err != nil {
			return fmt.Errorf("failed to store versioning configuration: %w", err)
		}

		// Handle Object Lock configuration using shared utilities
		if objectLockEnabled {
			s.log.Debugw("Configuring object lock for bucket", "bucketName", bucketName, "mode", objectLockMode, "duration", objectLockDuration)

			if objectLockMode != "GOVERNANCE" && objectLockMode != "COMPLIANCE" {
				return fmt.Errorf("invalid object lock mode: %s", objectLockMode)
			}

			// Validate Object Lock parameters
			if err := s3api.ValidateObjectLockParameters(objectLockEnabled, objectLockMode, objectLockDuration); err != nil {
				return fmt.Errorf("invalid Object Lock parameters: %w", err)
			}

			// Create Object Lock configuration using shared utility
			objectLockConfig := s3api.CreateObjectLockConfigurationFromParams(objectLockEnabled, objectLockMode, objectLockDuration)

			// Store Object Lock configuration in extended attributes using shared utility
			if err := s3api.StoreObjectLockConfigurationInExtended(bucketEntry, objectLockConfig); err != nil {
				return fmt.Errorf("failed to store Object Lock configuration: %w", err)
			}
		}

		// Create bucket directory under /buckets
		_, err = client.CreateEntry(context.Background(), &filer_pb.CreateEntryRequest{
			Directory: "/buckets",
			Entry:     bucketEntry,
		})
		if err != nil {
			return fmt.Errorf("failed to create bucket directory: %w", err)
		}

		s.log.Debugw("Successfully created bucket with object lock configuration",
			"bucketName", bucketName,
			"versioningEnabled", versioningEnabled,
			"objectLockEnabled", objectLockEnabled)

		return nil
	})
}

//#endregion

//#region DeleteS3Bucket

// DeleteS3Bucket deletes an S3 bucket and all its contents
func (s *AdminServer) DeleteS3Bucket(bucketName string) error {
	return s.WithFilerClient(func(client filer_pb.SeaweedFilerClient) error {
		// Delete bucket directory recursively
		_, err := client.DeleteEntry(context.Background(), &filer_pb.DeleteEntryRequest{
			Directory:            "/buckets",
			Name:                 bucketName,
			IsDeleteData:         true,
			IsRecursive:          true,
			IgnoreRecursiveError: false,
		})

		if err != nil {
			return fmt.Errorf("failed to delete bucket: %w", err)
		}

		return nil
	})
}

//#endregion

//#region extractObjectLockInfoFromEntry

// Function to extract Object Lock information from bucket entry using shared utilities
func extractObjectLockInfoFromEntry(entry *filer_pb.Entry) (bool, string, int32) {
	// Try to load Object Lock configuration using shared utility
	if config, found := s3api.LoadObjectLockConfigurationFromExtended(entry); found {
		return s3api.ExtractObjectLockInfoFromConfig(config)
	}

	return false, "", 0
}

//#endregion

//#region extractVersioningFromEntry

// Function to extract versioning information from bucket entry using shared utilities
func extractVersioningFromEntry(entry *filer_pb.Entry) bool {
	enabled, _ := s3api.LoadVersioningFromExtended(entry)
	return enabled
}

//#endregion
