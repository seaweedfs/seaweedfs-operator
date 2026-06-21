package swadmin

import (
	"context"
	"fmt"

	"github.com/seaweedfs/seaweedfs/weed/pb/filer_pb"
)

// bucketLifecycleConfigurationXMLKey is the filer entry extended attribute that
// holds a bucket's S3 lifecycle configuration XML. It matches the key the
// SeaweedFS S3 gateway reads and the lifecycle worker loads from.
const bucketLifecycleConfigurationXMLKey = "s3-bucket-lifecycle-configuration-xml"

// GetBucketLifecycle returns the lifecycle configuration XML stored on the
// bucket's filer entry, or nil when none is set.
func (sa *SeaweedAdmin) GetBucketLifecycle(ctx context.Context, bucket string) ([]byte, error) {
	var lifecycleXML []byte
	err := sa.commandEnv.WithFilerClient(false, func(client filer_pb.SeaweedFilerClient) error {
		entry, _, err := lookupBucketEntry(ctx, client, bucket)
		if err != nil {
			return err
		}
		if v, ok := entry.Extended[bucketLifecycleConfigurationXMLKey]; ok && len(v) > 0 {
			lifecycleXML = append([]byte(nil), v...)
		}
		return nil
	})
	return lifecycleXML, err
}

// SetBucketLifecycle stores the lifecycle configuration XML on the bucket's
// filer entry. An empty lifecycleXML removes the configuration.
func (sa *SeaweedAdmin) SetBucketLifecycle(ctx context.Context, bucket string, lifecycleXML []byte) error {
	return sa.commandEnv.WithFilerClient(false, func(client filer_pb.SeaweedFilerClient) error {
		entry, dir, err := lookupBucketEntry(ctx, client, bucket)
		if err != nil {
			return err
		}
		if len(lifecycleXML) == 0 {
			if _, ok := entry.Extended[bucketLifecycleConfigurationXMLKey]; !ok {
				return nil
			}
			delete(entry.Extended, bucketLifecycleConfigurationXMLKey)
		} else {
			if entry.Extended == nil {
				entry.Extended = make(map[string][]byte)
			}
			entry.Extended[bucketLifecycleConfigurationXMLKey] = append([]byte(nil), lifecycleXML...)
		}
		_, err = client.UpdateEntry(ctx, &filer_pb.UpdateEntryRequest{Directory: dir, Entry: entry})
		return err
	})
}

// lookupBucketEntry resolves a bucket's filer entry under the configured
// buckets directory and returns the entry together with that directory.
func lookupBucketEntry(ctx context.Context, client filer_pb.SeaweedFilerClient, bucket string) (*filer_pb.Entry, string, error) {
	cfg, err := client.GetFilerConfiguration(ctx, &filer_pb.GetFilerConfigurationRequest{})
	if err != nil {
		return nil, "", fmt.Errorf("get filer configuration: %w", err)
	}
	dir := cfg.DirBuckets
	resp, err := client.LookupDirectoryEntry(ctx, &filer_pb.LookupDirectoryEntryRequest{Directory: dir, Name: bucket})
	if err != nil {
		return nil, "", fmt.Errorf("lookup bucket %s: %w", bucket, err)
	}
	return resp.Entry, dir, nil
}
