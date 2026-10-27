package swadmin

import (
	"context"
	"fmt"

	"github.com/seaweedfs/seaweedfs/weed/pb/filer_pb"
	"google.golang.org/grpc"
)

// bucketPolicyMetadataKey is the filer entry extended attribute that holds a
// bucket's S3 bucket policy document. It matches the key the SeaweedFS S3
// gateway reads (s3api.BUCKET_POLICY_METADATA_KEY).
const bucketPolicyMetadataKey = "s3-bucket-policy"

// withFilerClient runs fn against the filer gRPC API on a fresh, short-lived
// connection, sharing the dial + admin auth of withClient.
func (c *IAMClient) withFilerClient(ctx context.Context, fn func(ctx context.Context, client filer_pb.SeaweedFilerClient) error) error {
	conn, err := grpc.NewClient(c.filerGrpcAddress, c.dialOption)
	if err != nil {
		return fmt.Errorf("dial filer %s: %w", c.filerGrpcAddress, err)
	}
	defer conn.Close()

	callCtx, cancel := context.WithTimeout(c.authContext(ctx), iamRequestTimeout)
	defer cancel()
	return fn(callCtx, filer_pb.NewSeaweedFilerClient(conn))
}

// GetBucketPolicy returns the bucket policy JSON stored on the bucket's filer
// entry, or nil when none is set. ErrBucketNotFound when the bucket has no
// filer entry.
func (c *IAMClient) GetBucketPolicy(ctx context.Context, bucket string) ([]byte, error) {
	var policyJSON []byte
	err := c.withFilerClient(ctx, func(ctx context.Context, client filer_pb.SeaweedFilerClient) error {
		entry, _, err := lookupBucketEntry(ctx, client, bucket)
		if err != nil {
			return err
		}
		if v, ok := entry.Extended[bucketPolicyMetadataKey]; ok && len(v) > 0 {
			policyJSON = append([]byte(nil), v...)
		}
		return nil
	})
	return policyJSON, err
}

// SetBucketPolicy stores the bucket policy JSON on the bucket's filer entry.
// An empty policyJSON removes the attribute.
func (c *IAMClient) SetBucketPolicy(ctx context.Context, bucket string, policyJSON []byte) error {
	return c.withFilerClient(ctx, func(ctx context.Context, client filer_pb.SeaweedFilerClient) error {
		// PATCH_EXTENDED is a no-op on a missing entry, so the existence
		// check has to happen here rather than fall out of the write.
		_, dir, err := lookupBucketEntry(ctx, client, bucket)
		if err != nil {
			return err
		}
		return patchBucketExtended(ctx, client, dir, bucket, bucketPolicyMetadataKey, policyJSON)
	})
}
