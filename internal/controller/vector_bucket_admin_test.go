package controller

import (
	"context"
	"testing"

	"github.com/seaweedfs/seaweedfs/weed/s3api/s3_constants"
	"github.com/seaweedfs/seaweedfs/weed/s3api/s3tables"
	"github.com/seaweedfs/seaweedfs/weed/s3api/s3tables/s3tablestest"
	"github.com/seaweedfs/seaweedfs/weed/s3api/s3vectors"
)

func TestVectorBucketCatalog(t *testing.T) {
	fs := s3tablestest.Start(t)
	client := s3tables.NewManagerClient(fs.Client)
	server := s3vectors.NewServer(client, nil, nil)
	a := &vectorBucketAdmin{BucketAdmin: &fakeBucketAdmin{}, execute: func(ctx context.Context, op string, req, resp interface{}) error {
		return server.Execute(ctx, op, req, resp, s3_constants.AccountAdminId)
	}}
	ctx := context.Background()
	if exists, err := a.BucketExists(ctx, "missing"); err != nil || exists {
		t.Fatalf("missing: %v %v", exists, err)
	}
	if err := s3tables.NewManager().Execute(ctx, client, "CreateTableBucket", &s3tables.CreateTableBucketRequest{Name: "warehouse", Format: s3tables.FormatIceberg}, nil, s3_constants.AccountAdminId); err != nil {
		t.Fatal(err)
	}
	a.BucketAdmin = &fakeBucketAdmin{existsResp: map[string]bool{"warehouse": true}}
	if _, err := a.BucketExists(ctx, "warehouse"); err == nil {
		t.Fatal("adopted Iceberg bucket as vector")
	}
	if err := a.CreateBucket(ctx, "warehouse", "", false); err == nil || err == ErrBucketAlreadyExists {
		t.Fatalf("format conflict incorrectly adoptable: %v", err)
	}

	if err := a.CreateBucket(ctx, "embeddings", "", false); err != nil {
		t.Fatal(err)
	}
	if exists, err := a.BucketExists(ctx, "embeddings"); err != nil || !exists {
		t.Fatalf("exists=%v err=%v", exists, err)
	}
	if err := a.CreateBucket(ctx, "embeddings", "", false); err != ErrBucketAlreadyExists {
		t.Fatalf("race error: %v", err)
	}
	if err := a.DeleteBucket(ctx, "embeddings"); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteBucket(ctx, "embeddings"); err != ErrBucketNotFound {
		t.Fatalf("missing delete: %v", err)
	}
}
