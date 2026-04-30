/*


Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/go-logr/logr"

	"github.com/seaweedfs/seaweedfs-operator/internal/controller/swadmin"
)

// BucketAdmin is the small surface the bucket reconciler uses to drive a
// SeaweedFS cluster. The default implementation wraps swadmin.SeaweedAdmin
// (which embeds `weed shell` directly); tests inject a fake.
//
// Methods are loosely idempotent — most underlying shell commands are safe
// to call repeatedly with the same arguments. The admin layer translates
// well-known error substrings into typed sentinels so the reconciler can
// take structured action.
type BucketAdmin interface {
	BucketExists(ctx context.Context, name string) (bool, error)
	CreateBucket(ctx context.Context, name, owner string, withLock bool) error
	DeleteBucket(ctx context.Context, name string) error
	SetVersioning(ctx context.Context, name, status string) error
	EnableObjectLock(ctx context.Context, name string) error
	SetQuota(ctx context.Context, name string, sizeMiB int64, enforce bool) error
	RemoveQuota(ctx context.Context, name string) error
	SetOwner(ctx context.Context, name, owner string) error
	RemoveOwner(ctx context.Context, name string) error
	// SetAccess applies a comma-joined set of actions for a user. Pass
	// "none" to strip the user's grants on this bucket without deleting
	// the IAM identity itself.
	SetAccess(ctx context.Context, name, user, actions string) error
	// Configure issues a single fs.configure call; args are the flag list
	// minus locationPrefix and -apply (the admin layer adds those).
	Configure(ctx context.Context, prefix string, args []string) error
	// ListCollectionStats fetches per-collection (= per-bucket) usage in
	// a single round trip. The map is keyed by bucket/collection name;
	// buckets that exist on the filer but have no objects yet may be
	// absent from the map.
	ListCollectionStats(ctx context.Context) (map[string]BucketCollectionStats, error)
}

// BucketCollectionStats is the subset of `collection.list` output the
// usage refresher consumes. See command_collection_list.go in OSS for the
// canonical format.
type BucketCollectionStats struct {
	// FileCount is the total number of S3 objects in the bucket.
	FileCount int64
	// SizeBytes is the total stored bytes for the bucket.
	SizeBytes int64
}

// BucketAdminFactory creates a BucketAdmin for the master peers of a target
// Seaweed cluster. Replaceable in tests.
type BucketAdminFactory func(masters string, log logr.Logger) (BucketAdmin, error)

// Sentinel errors returned by BucketAdmin implementations.
var (
	// ErrBucketNotFound indicates the bucket does not exist on the filer.
	ErrBucketNotFound = errors.New("bucket not found")
	// ErrBucketAlreadyExists is returned by CreateBucket when the bucket
	// is already present on the filer.
	ErrBucketAlreadyExists = errors.New("bucket already exists")
	// ErrRetentionBlocksDelete is returned by DeleteBucket when Object
	// Lock retention or legal hold prevents removal.
	ErrRetentionBlocksDelete = errors.New("bucket has objects with active Object Lock retention or legal hold")
	// ErrObjectLockBlocksSuspend is returned by SetVersioning when Object
	// Lock prevents transitioning to Suspended.
	ErrObjectLockBlocksSuspend = errors.New("cannot suspend versioning while Object Lock is enabled")
)

// swadminBucketAdmin is the default BucketAdmin, backed by swadmin.SeaweedAdmin.
//
// Reconcile goroutines (one per Bucket worker, plus the periodic
// usage-stats loop) share a single instance via the reconciler's admin
// cache, so concurrent ProcessCommand calls would race on the embedded
// SeaweedAdmin's Output writer. The mu serializes every command in this
// admin's lifetime — coarse but sufficient: shell commands are short and
// per-bucket reconciles don't fan out further than the worker count.
type swadminBucketAdmin struct {
	sa  *swadmin.SeaweedAdmin
	log logr.Logger
	mu  sync.Mutex
}

// NewSwadminBucketAdmin returns a BucketAdmin that runs `weed shell` commands
// against the given comma-separated master peers list.
func NewSwadminBucketAdmin(masters string, log logr.Logger) (BucketAdmin, error) {
	sa := swadmin.NewSeaweedAdmin(masters, io.Discard)
	return &swadminBucketAdmin{sa: sa, log: log}, nil
}

func (a *swadminBucketAdmin) run(cmd string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var buf bytes.Buffer
	a.sa.Output = &buf
	err := a.sa.ProcessCommand(cmd)
	if err != nil {
		a.log.V(2).Info("swadmin command failed", "cmd", cmd, "stdout", buf.String(), "err", err.Error())
	} else {
		a.log.V(2).Info("swadmin command ok", "cmd", cmd, "stdout", buf.String())
	}
	return buf.String(), err
}

// BucketExists probes for the bucket via `s3.bucket.versioning -name X` with
// no other flags — a read-only call that returns the current state when the
// bucket is present and an explicit "lookup bucket" error when it is not.
func (a *swadminBucketAdmin) BucketExists(ctx context.Context, name string) (bool, error) {
	_, err := a.run(fmt.Sprintf("s3.bucket.versioning -name %s", name))
	if err == nil {
		return true, nil
	}
	if isBucketNotFoundErr(err) {
		return false, nil
	}
	return false, err
}

func (a *swadminBucketAdmin) CreateBucket(ctx context.Context, name, owner string, withLock bool) error {
	parts := []string{"s3.bucket.create", "-name", name}
	if owner != "" {
		parts = append(parts, "-owner", owner)
	}
	if withLock {
		parts = append(parts, "-withLock")
	}
	_, err := a.run(strings.Join(parts, " "))
	if err == nil {
		return nil
	}
	if isAlreadyExistsErr(err) {
		return ErrBucketAlreadyExists
	}
	return err
}

func (a *swadminBucketAdmin) DeleteBucket(ctx context.Context, name string) error {
	_, err := a.run(fmt.Sprintf("s3.bucket.delete -name %s", name))
	if err == nil {
		return nil
	}
	if isRetentionErr(err) {
		return ErrRetentionBlocksDelete
	}
	if isBucketNotFoundErr(err) {
		return ErrBucketNotFound
	}
	return err
}

func (a *swadminBucketAdmin) SetVersioning(ctx context.Context, name, status string) error {
	_, err := a.run(fmt.Sprintf("s3.bucket.versioning -name %s -status %s", name, status))
	if err == nil {
		return nil
	}
	if isObjectLockSuspendErr(err) {
		return ErrObjectLockBlocksSuspend
	}
	return err
}

func (a *swadminBucketAdmin) EnableObjectLock(ctx context.Context, name string) error {
	_, err := a.run(fmt.Sprintf("s3.bucket.lock -name %s -enable", name))
	return err
}

func (a *swadminBucketAdmin) SetQuota(ctx context.Context, name string, sizeMiB int64, enforce bool) error {
	if _, err := a.run(fmt.Sprintf("s3.bucket.quota -name %s -op set -sizeMB %d", name, sizeMiB)); err != nil {
		return err
	}
	op := "enable"
	if !enforce {
		op = "disable"
	}
	_, err := a.run(fmt.Sprintf("s3.bucket.quota -name %s -op %s", name, op))
	return err
}

func (a *swadminBucketAdmin) RemoveQuota(ctx context.Context, name string) error {
	_, err := a.run(fmt.Sprintf("s3.bucket.quota -name %s -op remove", name))
	return err
}

func (a *swadminBucketAdmin) SetOwner(ctx context.Context, name, owner string) error {
	_, err := a.run(fmt.Sprintf("s3.bucket.owner -name %s -owner %s", name, owner))
	return err
}

func (a *swadminBucketAdmin) RemoveOwner(ctx context.Context, name string) error {
	_, err := a.run(fmt.Sprintf("s3.bucket.owner -name %s -delete", name))
	return err
}

func (a *swadminBucketAdmin) SetAccess(ctx context.Context, name, user, actions string) error {
	if actions == "" {
		actions = "none"
	}
	_, err := a.run(fmt.Sprintf("s3.bucket.access -name %s -user %s -access %s", name, user, actions))
	return err
}

func (a *swadminBucketAdmin) Configure(ctx context.Context, prefix string, args []string) error {
	parts := []string{"fs.configure", "-locationPrefix=" + prefix}
	parts = append(parts, args...)
	parts = append(parts, "-apply")
	_, err := a.run(strings.Join(parts, " "))
	return err
}

func (a *swadminBucketAdmin) ListCollectionStats(ctx context.Context) (map[string]BucketCollectionStats, error) {
	out, err := a.run("collection.list")
	if err != nil {
		return nil, err
	}
	return parseCollectionListOutput(out), nil
}

// collectionListLine matches one line of `collection.list` stdout. The
// `Total N collections.` summary line is skipped because it does not match
// the per-collection prefix.
//
// Reference format (tabs between fields):
//
//	collection:"photos"\tvolumeCount:3\tsize:107374182400\tfileCount:12483\tdeletedBytes:0\tdeletion:0
var collectionListLine = regexp.MustCompile(`collection:"([^"]+)"\s+volumeCount:\d+\s+size:(\d+)\s+fileCount:(\d+)`)

func parseCollectionListOutput(out string) map[string]BucketCollectionStats {
	m := map[string]BucketCollectionStats{}
	for _, line := range strings.Split(out, "\n") {
		matches := collectionListLine.FindStringSubmatch(line)
		if len(matches) != 4 {
			continue
		}
		size, err := strconv.ParseInt(matches[2], 10, 64)
		if err != nil {
			continue
		}
		count, err := strconv.ParseInt(matches[3], 10, 64)
		if err != nil {
			continue
		}
		m[matches[1]] = BucketCollectionStats{FileCount: count, SizeBytes: size}
	}
	return m
}

// Error-string classifiers. The shell commands return errors with stable
// substrings which the admin layer maps onto sentinels. They are isolated
// here so future swadmin error-message changes only need updates in one
// place.

// isBucketNotFoundErr matches the three SeaweedFS-specific error prefixes
// emitted when a bucket lookup misses. The generic substring "not found"
// is intentionally NOT matched: connection errors, gRPC stream errors,
// and unrelated filer errors can all carry that phrase, and treating
// them as bucket-missing would mask real problems and trigger spurious
// CreateBucket attempts.
func isBucketNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "lookup bucket") ||
		strings.Contains(msg, "did not find bucket") ||
		strings.Contains(msg, "bucket not found")
}

func isAlreadyExistsErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "file exists")
}

func isRetentionErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Object Lock retention") ||
		strings.Contains(msg, "legal hold")
}

func isObjectLockSuspendErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "cannot suspend versioning") &&
		strings.Contains(err.Error(), "Object Lock is enabled")
}
