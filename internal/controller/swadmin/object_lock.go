package swadmin

import (
	"context"
	"strconv"

	"github.com/seaweedfs/seaweedfs/weed/pb/filer_pb"
)

// The filer entry extended attributes holding a bucket's Object Lock default
// retention rule. They match the keys the SeaweedFS S3 gateway reads and
// StoreObjectLockConfigurationInExtended writes
// (s3_constants.ExtObjectLockDefault*Key).
const (
	objectLockDefaultModeKey  = "Lock-Default-Mode"
	objectLockDefaultDaysKey  = "Lock-Default-Days"
	objectLockDefaultYearsKey = "Lock-Default-Years"
)

// SetObjectLockDefaultRetention writes the bucket's Object Lock default
// retention rule to the bucket's filer entry. An empty mode removes the rule
// (mode, days and years attributes are dropped together). The update is
// skipped when the stored rule already matches, so it is safe to call on
// every reconcile. ErrBucketNotFound when the bucket has no filer entry.
func (sa *SeaweedAdmin) SetObjectLockDefaultRetention(ctx context.Context, bucket, mode string, days, years int) error {
	return sa.commandEnv.WithFilerClient(false, func(client filer_pb.SeaweedFilerClient) error {
		entry, dir, err := lookupBucketEntry(ctx, client, bucket)
		if err != nil {
			return err
		}
		ext, changed := defaultRetentionUpdate(entry.Extended, mode, days, years)
		if !changed {
			return nil
		}
		entry.Extended = ext
		_, err = client.UpdateEntry(ctx, &filer_pb.UpdateEntryRequest{Directory: dir, Entry: entry})
		return err
	})
}

// defaultRetentionUpdate rewrites the default-retention extended attributes
// in place to the desired rule — an empty mode removes all three — and
// reports whether anything changed.
func defaultRetentionUpdate(ext map[string][]byte, mode string, days, years int) (map[string][]byte, bool) {
	desired := map[string]string{}
	if mode != "" {
		desired[objectLockDefaultModeKey] = mode
		if days > 0 {
			desired[objectLockDefaultDaysKey] = strconv.Itoa(days)
		}
		if years > 0 {
			desired[objectLockDefaultYearsKey] = strconv.Itoa(years)
		}
	}
	changed := false
	for _, k := range []string{objectLockDefaultModeKey, objectLockDefaultDaysKey, objectLockDefaultYearsKey} {
		want, keep := desired[k]
		have, present := ext[k]
		switch {
		case keep && (!present || string(have) != want):
			if ext == nil {
				ext = make(map[string][]byte)
			}
			ext[k] = []byte(want)
			changed = true
		case !keep && present:
			delete(ext, k)
			changed = true
		}
	}
	return ext, changed
}
