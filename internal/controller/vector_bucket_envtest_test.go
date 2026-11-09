package controller

import (
	"fmt"
	"testing"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestVectorBucketAdmission(t *testing.T) {
	_, cli := mustEnvtest(t)
	ctx := t.Context()
	ns := newTestNamespace(t, ctx, cli, "vector-bucket")
	bucket := func(name string) *seaweedv1.Bucket {
		return &seaweedv1.Bucket{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: seaweedv1.BucketSpec{Type: "Vector", SeaweedRef: &seaweedv1.SeaweedReference{Name: "cluster"}}}
	}
	valid := bucket("valid")
	if err := cli.Create(ctx, valid); err != nil {
		t.Fatal(err)
	}
	valid.Spec.Type = "Object"
	if err := cli.Update(ctx, valid); !apierrors.IsInvalid(err) {
		t.Fatalf("bucket type mutation accepted: %v", err)
	}
	for i, mutate := range []func(*seaweedv1.Bucket){
		func(b *seaweedv1.Bucket) { b.Spec.Versioning = seaweedv1.VersioningEnabled },
		func(b *seaweedv1.Bucket) { b.Spec.Owner = "user" },
		func(b *seaweedv1.Bucket) { b.Spec.AnonymousRead = true },
		func(b *seaweedv1.Bucket) { b.Spec.Placement = &seaweedv1.BucketPlacement{Replication: "001"} },
	} {
		b := bucket(fmt.Sprintf("invalid-%d", i))
		mutate(b)
		if err := cli.Create(ctx, b); !apierrors.IsInvalid(err) {
			t.Fatalf("object setting accepted: %v", err)
		}
	}
	ordinary := bucket("ordinary")
	ordinary.Spec.Type = ""
	if err := cli.Create(ctx, ordinary); err != nil {
		t.Fatal(err)
	}
	if ordinary.Spec.Type != "Object" {
		t.Fatalf("wrong default: %s", ordinary.Spec.Type)
	}
}
