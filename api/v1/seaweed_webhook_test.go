/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// baseValid returns a Seaweed CR that satisfies the webhook's required
// fields — any test wanting to assert a specific failure just mutates
// the fields it cares about so the test only exercises one concern.
func baseValid() *Seaweed {
	return &Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "n"},
		Spec: SeaweedSpec{
			Master: &MasterSpec{Replicas: 1},
			Volume: &VolumeSpec{
				Replicas: 1,
				VolumeServerConfig: VolumeServerConfig{
					ResourceRequirements: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
					},
				},
			},
		},
	}
}

func TestValidateS3Exclusivity(t *testing.T) {
	t.Run("neither set is fine", func(t *testing.T) {
		sw := baseValid()
		if err := sw.validateS3Exclusivity(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("only standalone is fine", func(t *testing.T) {
		sw := baseValid()
		sw.Spec.S3 = &S3GatewaySpec{Replicas: 1}
		if err := sw.validateS3Exclusivity(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("only embedded is fine", func(t *testing.T) {
		sw := baseValid()
		sw.Spec.Filer = &FilerSpec{Replicas: 1, S3: &S3Config{Enabled: true}}
		if err := sw.validateS3Exclusivity(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("both set is rejected", func(t *testing.T) {
		sw := baseValid()
		sw.Spec.Filer = &FilerSpec{Replicas: 1, S3: &S3Config{Enabled: true}}
		sw.Spec.S3 = &S3GatewaySpec{Replicas: 1}
		err := sw.validateS3Exclusivity()
		if err == nil {
			t.Fatal("expected rejection, got nil")
		}
		if !strings.Contains(err.Error(), "cannot both be set") {
			t.Fatalf("error does not mention mutual exclusion: %v", err)
		}
	})

	t.Run("embedded disabled is treated as unset", func(t *testing.T) {
		sw := baseValid()
		sw.Spec.Filer = &FilerSpec{Replicas: 1, S3: &S3Config{Enabled: false}}
		sw.Spec.S3 = &S3GatewaySpec{Replicas: 1}
		if err := sw.validateS3Exclusivity(); err != nil {
			t.Fatalf("unexpected error when embedded is disabled: %v", err)
		}
	})
}

func TestS3DeprecationWarnings(t *testing.T) {
	t.Run("embedded enabled emits deprecation warning", func(t *testing.T) {
		sw := baseValid()
		sw.Spec.Filer = &FilerSpec{Replicas: 1, S3: &S3Config{Enabled: true}}
		warnings := sw.s3DeprecationWarnings()
		if len(warnings) != 1 {
			t.Fatalf("expected 1 warning, got %d: %v", len(warnings), warnings)
		}
		if !strings.Contains(warnings[0], "deprecated") {
			t.Fatalf("warning does not mention deprecation: %v", warnings[0])
		}
	})

	t.Run("no filer no warning", func(t *testing.T) {
		sw := baseValid()
		if w := sw.s3DeprecationWarnings(); len(w) != 0 {
			t.Fatalf("expected no warnings, got %v", w)
		}
	})

	t.Run("embedded disabled no warning", func(t *testing.T) {
		sw := baseValid()
		sw.Spec.Filer = &FilerSpec{Replicas: 1, S3: &S3Config{Enabled: false}}
		if w := sw.s3DeprecationWarnings(); len(w) != 0 {
			t.Fatalf("expected no warnings, got %v", w)
		}
	})
}

func strptr(s string) *string { return &s }

func TestValidateBackup(t *testing.T) {
	t.Run("nil backup is fine", func(t *testing.T) {
		sw := baseValid()
		if errs := sw.validateBackup(); len(errs) != 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
	})

	t.Run("filesystem needs no credentials", func(t *testing.T) {
		sw := baseValid()
		sw.Spec.Backup = &BackupSpec{Storages: map[string]BackupStorageSpec{
			"pvc": {Type: BackupStorageFilesystem, Filesystem: &FilesystemBackupStore{ExistingClaim: "c"}},
		}}
		if errs := sw.validateBackup(); len(errs) != 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
	})

	t.Run("azure requires credentialsSecret", func(t *testing.T) {
		sw := baseValid()
		sw.Spec.Backup = &BackupSpec{Storages: map[string]BackupStorageSpec{
			"az": {Type: BackupStorageAzure, Azure: &AzureBackupStore{AccountName: "a", Container: "c"}},
		}}
		errs := sw.validateBackup()
		if len(errs) == 0 || !strings.Contains(errs[0].Error(), "credentialsSecret") {
			t.Fatalf("expected credentialsSecret error, got %v", errs)
		}
	})

	t.Run("b2 with credentials is fine", func(t *testing.T) {
		sw := baseValid()
		sw.Spec.Backup = &BackupSpec{Storages: map[string]BackupStorageSpec{
			"b2": {Type: BackupStorageB2, B2: &B2BackupStore{Bucket: "b"}, CredentialsSecret: strptr("creds")},
		}}
		if errs := sw.validateBackup(); len(errs) != 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
	})

	t.Run("invalid storage name is rejected", func(t *testing.T) {
		sw := baseValid()
		sw.Spec.Backup = &BackupSpec{Storages: map[string]BackupStorageSpec{
			"Bad_Name": {Type: BackupStorageFilesystem, Filesystem: &FilesystemBackupStore{ExistingClaim: "c"}},
		}}
		errs := sw.validateBackup()
		if len(errs) == 0 || !strings.Contains(errs[0].Error(), "RFC1123") {
			t.Fatalf("expected RFC1123 name error, got %v", errs)
		}
	})
}
