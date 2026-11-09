package controller

import (
	"context"
	"errors"
	"fmt"

	"github.com/seaweedfs/seaweedfs/weed/s3api/s3vectors"
)

type vectorBucketAdmin struct {
	BucketAdmin
	execute func(context.Context, string, interface{}, interface{}) error
}

func (a *swadminBucketAdmin) ExecuteS3VectorsOperation(ctx context.Context, op string, req, resp interface{}) error {
	return a.sa.ExecuteS3VectorsOperation(ctx, op, req, resp)
}

func (a *vectorBucketAdmin) BucketExists(ctx context.Context, name string) (bool, error) {
	var resp s3vectors.GetVectorBucketResponse
	err := a.execute(ctx, "GetVectorBucket", &s3vectors.GetVectorBucketRequest{VectorBucketName: name}, &resp)
	if err == nil {
		return true, nil
	}
	var e *s3vectors.Error
	if errors.As(err, &e) && e.Code == "NotFoundException" {
		exists, lookupErr := a.BucketAdmin.BucketExists(ctx, name)
		if lookupErr != nil {
			return false, lookupErr
		}
		if exists {
			return false, fmt.Errorf("bucket %q exists with a different format", name)
		}
		return false, nil
	}
	return false, err
}

func (a *vectorBucketAdmin) CreateBucket(ctx context.Context, name, owner string, withLock bool) error {
	var resp s3vectors.CreateVectorBucketResponse
	err := a.execute(ctx, "CreateVectorBucket", &s3vectors.CreateVectorBucketRequest{VectorBucketName: name}, &resp)
	var e *s3vectors.Error
	if errors.As(err, &e) && e.Code == "ConflictException" {
		exists, lookupErr := a.BucketExists(ctx, name)
		if lookupErr != nil {
			return lookupErr
		}
		if exists {
			return ErrBucketAlreadyExists
		}
	}
	return err
}

func (a *vectorBucketAdmin) DeleteBucket(ctx context.Context, name string) error {
	err := a.execute(ctx, "DeleteVectorBucket", &s3vectors.DeleteVectorBucketRequest{VectorBucketName: name}, nil)
	var e *s3vectors.Error
	if errors.As(err, &e) && e.Code == "NotFoundException" {
		return ErrBucketNotFound
	}
	return err
}
