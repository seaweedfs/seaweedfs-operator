package admin

import (
	"context"
	"fmt"
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

	s.log.Info("Getting volume information")

	// Collect volume information by collection
	err := s.WithMasterClient(func(client master_pb.SeaweedClient) error {
		s.log.V(1).Info("Getting volume list from master")

		resp, err := client.VolumeList(context.Background(), &master_pb.VolumeListRequest{})

		s.log.V(2).Info("Got volume list response", "resp", resp)

		if err != nil {
			s.log.Error(err, "Failed to get volume list")
			return err
		}

		if resp.TopologyInfo != nil {
			s.log.V(2).Info("Processing topology info")
			for _, dc := range resp.TopologyInfo.DataCenterInfos {
				for _, rack := range dc.RackInfos {
					for _, node := range rack.DataNodeInfos {
						for _, diskInfo := range node.DiskInfos {
							for _, volInfo := range diskInfo.VolumeInfos {
								s.log.V(3).Info("Processing volume", "collection", volInfo.Collection, "size", volInfo.Size, "fileCount", volInfo.FileCount)
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

		s.log.V(1).Info("Completed volume information collection", "collections", len(collectionMap))

		return nil
	})

	if err != nil {
		s.log.Error(err, "Failed to get volume information")
		return nil, fmt.Errorf("failed to get volume information: %w", err)
	}

	// Get filer configuration to determine FilerGroup
	var filerGroup string

	s.log.V(1).Info("Getting filer configuration")

	err = s.WithFilerClient(func(client filer_pb.SeaweedFilerClient) error {
		configResp, err := client.GetFilerConfiguration(context.Background(), &filer_pb.GetFilerConfigurationRequest{})
		if err != nil {
			s.log.Error(err, "Failed to get filer configuration")
			// Continue without filer group
			return nil
		}

		filerGroup = configResp.FilerGroup
		s.log.V(1).Info("Got filer group", "filerGroup", filerGroup)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to get filer configuration: %w", err)
	}

	s.log.V(1).Info("Starting bucket listing process")

	// Now list buckets from the filer and match with collection data
	err = s.WithFilerClient(func(client filer_pb.SeaweedFilerClient) error {
		s.log.V(1).Info("Listing buckets from filer")
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

		s.log.V(2).Info("Got bucket listing stream")

		for {
			resp, err := stream.Recv()

			if err != nil {
				if err.Error() == "EOF" {
					break
				}
				return err
			}

			s.log.V(3).Info("Processing bucket entry", "name", resp.Entry.Name, "isDirectory", resp.Entry.IsDirectory)

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

	s.log.V(1).Info("Successfully retrieved S3 buckets", "count", len(buckets))
	return buckets, nil
}

//#endregion

//#region GetBucketDetails

// GetBucketDetails retrieves detailed information about a specific bucket
func (s *AdminServer) GetBucketDetails(bucketName string) (*BucketDetails, error) {
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

		// List objects in bucket (recursively)
		return s.listBucketObjects(client, bucketPath, "", details)
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
	return s.WithFilerClient(func(client filer_pb.SeaweedFilerClient) error {
		// Create bucket directory
		_, err := client.CreateEntry(context.Background(), &filer_pb.CreateEntryRequest{
			Directory: "/buckets",
			Entry: &filer_pb.Entry{
				Name: bucketName,
				Attributes: &filer_pb.FuseAttributes{
					Mtime:    time.Now().Unix(),
					Crtime:   time.Now().Unix(),
					FileMode: uint32(0755 | 040000), // Directory mode
				},
				Quota: quotaBytes,
			},
		})
		if err != nil {
			return fmt.Errorf("failed to create bucket: %w", err)
		}

		return nil
	})
}

//#endregion

//#region CreateS3BucketWithObjectLock

// CreateS3BucketWithObjectLock creates a new S3 bucket with object lock configuration
func (s *AdminServer) CreateS3BucketWithObjectLock(bucketName string, quotaBytes int64, quotaEnabled, versioningEnabled, objectLockEnabled bool, objectLockMode string, objectLockDuration int32) error {
	return s.WithFilerClient(func(client filer_pb.SeaweedFilerClient) error {
		// Object lock requires versioning to be enabled
		if objectLockEnabled && !versioningEnabled {
			versioningEnabled = true
			s.log.V(1).Info("Object lock enabled, forcing versioning to be enabled", "bucketName", bucketName)
		}

		// Create bucket directory
		entry := &filer_pb.Entry{
			Name: bucketName,
			Attributes: &filer_pb.FuseAttributes{
				Mtime:    time.Now().Unix(),
				Crtime:   time.Now().Unix(),
				FileMode: uint32(0755 | 040000), // Directory mode
			},
			Quota: quotaBytes,
		}

		// Set up extended attributes for versioning and object lock
		if versioningEnabled || objectLockEnabled {
			entry.Extended = make(map[string][]byte)
		}

		// Configure versioning
		if versioningEnabled {
			s.log.V(1).Info("Enabling versioning for bucket", "bucketName", bucketName)
			if err := s3api.StoreVersioningInExtended(entry, true); err != nil {
				return fmt.Errorf("failed to configure versioning: %w", err)
			}
		}

		// Configure object lock
		if objectLockEnabled {
			s.log.V(1).Info("Configuring object lock for bucket", "bucketName", bucketName, "mode", objectLockMode, "duration", objectLockDuration)

			// Create object lock configuration
			objectLockConfig := s3api.CreateObjectLockConfigurationFromParams(true, objectLockMode, objectLockDuration)

			if err := s3api.StoreObjectLockConfigurationInExtended(entry, objectLockConfig); err != nil {
				return fmt.Errorf("failed to configure object lock: %w", err)
			}
		}

		// Create the bucket
		_, err := client.CreateEntry(context.Background(), &filer_pb.CreateEntryRequest{
			Directory: "/buckets",
			Entry:     entry,
		})
		if err != nil {
			return fmt.Errorf("failed to create bucket: %w", err)
		}

		s.log.V(1).Info("Successfully created bucket with object lock configuration",
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
