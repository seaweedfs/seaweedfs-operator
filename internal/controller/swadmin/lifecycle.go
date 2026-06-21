package swadmin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/seaweedfs/seaweedfs/weed/filer"
	"github.com/seaweedfs/seaweedfs/weed/pb/filer_pb"
)

// bucketLifecycleConfigurationXMLKey is the filer entry extended attribute that
// holds a bucket's S3 lifecycle configuration XML. It matches the key the
// SeaweedFS S3 gateway reads and the lifecycle worker loads from.
const bucketLifecycleConfigurationXMLKey = "s3-bucket-lifecycle-configuration-xml"

// ErrBucketNotFound is returned when the bucket has no filer entry.
var ErrBucketNotFound = errors.New("bucket not found")

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
		// Drop any legacy day-TTL filer.conf entries for this bucket so they
		// can't keep expiring objects outside the declared rules, matching the
		// SeaweedFS S3 Put/DeleteBucketLifecycle handlers.
		if err := clearLegacyBucketTTLs(ctx, client, dir, bucket); err != nil {
			return err
		}
		switch {
		case len(lifecycleXML) == 0:
			if _, ok := entry.Extended[bucketLifecycleConfigurationXMLKey]; !ok {
				return nil
			}
			delete(entry.Extended, bucketLifecycleConfigurationXMLKey)
		default:
			if entry.Extended == nil {
				entry.Extended = make(map[string][]byte)
			}
			entry.Extended[bucketLifecycleConfigurationXMLKey] = append([]byte(nil), lifecycleXML...)
		}
		_, err = client.UpdateEntry(ctx, &filer_pb.UpdateEntryRequest{Directory: dir, Entry: entry})
		return err
	})
}

// clearLegacyBucketTTLs removes day-TTL filer.conf location entries under the
// bucket. Older SeaweedFS lifecycle handling stamped expiration as a per-path
// TTL in filer.conf; a stale entry would expire objects independently of the
// lifecycle XML this operator manages.
func clearLegacyBucketTTLs(ctx context.Context, client filer_pb.SeaweedFilerClient, bucketsDir, bucket string) error {
	content, err := filer.ReadInsideFiler(ctx, client, filer.DirectoryEtcSeaweedFS, filer.FilerConfName)
	if err != nil {
		if errors.Is(err, filer_pb.ErrNotFound) || strings.Contains(err.Error(), filer_pb.ErrNotFound.Error()) {
			return nil
		}
		return fmt.Errorf("read filer.conf: %w", err)
	}
	fc := filer.NewFilerConf()
	if err := fc.LoadFromBytes(content); err != nil {
		return fmt.Errorf("parse filer.conf: %w", err)
	}
	prefixes := staleTTLPrefixes(fc.GetCollectionTtls(bucket), bucketsDir+"/"+bucket+"/")
	if len(prefixes) == 0 {
		return nil
	}
	for _, prefix := range prefixes {
		fc.DeleteLocationConf(prefix)
	}
	var buf bytes.Buffer
	if err := fc.ToText(&buf); err != nil {
		return fmt.Errorf("serialize filer.conf: %w", err)
	}
	return filer.SaveInsideFiler(ctx, client, filer.DirectoryEtcSeaweedFS, filer.FilerConfName, buf.Bytes())
}

// staleTTLPrefixes returns the location prefixes under bucketPrefix whose TTL is
// expressed in days — the legacy lifecycle form this controller supersedes.
func staleTTLPrefixes(ttls map[string]string, bucketPrefix string) []string {
	var out []string
	for prefix, ttl := range ttls {
		if strings.HasPrefix(prefix, bucketPrefix) && strings.HasSuffix(ttl, "d") {
			out = append(out, prefix)
		}
	}
	return out
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
		// Distinguish a genuinely missing bucket from transport errors so the
		// caller can treat the former as "already gone".
		if strings.Contains(err.Error(), filer_pb.ErrNotFound.Error()) {
			return nil, "", ErrBucketNotFound
		}
		return nil, "", fmt.Errorf("lookup bucket %s: %w", bucket, err)
	}
	if resp.Entry == nil {
		return nil, "", ErrBucketNotFound
	}
	return resp.Entry, dir, nil
}
