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

func TestValidateVolume(t *testing.T) {
	t.Run("default PVC-backed volume is fine", func(t *testing.T) {
		sw := baseValid()
		if err := sw.validateVolume(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("PVC-backed volume with zero storage request is rejected", func(t *testing.T) {
		sw := baseValid()
		sw.Spec.Volume.Requests = nil
		err := sw.validateVolume()
		if err == nil {
			t.Fatal("expected rejection for zero storage request, got nil")
		}
		if !strings.Contains(err.Error(), "storage request cannot be zero") {
			t.Fatalf("error does not mention zero storage request: %v", err)
		}
	})

	t.Run("hostPath without storage request is fine (no PVCs)", func(t *testing.T) {
		sw := baseValid()
		sw.Spec.Volume.Requests = nil
		sw.Spec.Volume.HostPath = []VolumeServerHostPath{{Path: "/mnt/disk0"}}
		if err := sw.validateVolume(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("DaemonSet without hostPath is rejected", func(t *testing.T) {
		sw := baseValid()
		sw.Spec.Volume.Kind = VolumeServerDaemonSet
		err := sw.validateVolume()
		if err == nil {
			t.Fatal("expected rejection for DaemonSet without hostPath, got nil")
		}
		if !strings.Contains(err.Error(), "requires spec.volume.hostPath") {
			t.Fatalf("error does not mention hostPath requirement: %v", err)
		}
	})

	t.Run("DaemonSet with hostPath is fine", func(t *testing.T) {
		sw := baseValid()
		sw.Spec.Volume.Kind = VolumeServerDaemonSet
		sw.Spec.Volume.Requests = nil
		sw.Spec.Volume.HostPath = []VolumeServerHostPath{{Path: "/mnt/disk0"}, {Path: "/mnt/disk1"}}
		if err := sw.validateVolume(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("relative hostPath is rejected", func(t *testing.T) {
		sw := baseValid()
		sw.Spec.Volume.HostPath = []VolumeServerHostPath{{Path: "relative/path"}}
		err := sw.validateVolume()
		if err == nil {
			t.Fatal("expected rejection for relative hostPath, got nil")
		}
		if !strings.Contains(err.Error(), "must be an absolute path") {
			t.Fatalf("error does not mention absolute path: %v", err)
		}
	})

	t.Run("duplicate hostPath is rejected", func(t *testing.T) {
		sw := baseValid()
		sw.Spec.Volume.HostPath = []VolumeServerHostPath{{Path: "/mnt/disk0"}, {Path: "/mnt/disk0"}}
		err := sw.validateVolume()
		if err == nil {
			t.Fatal("expected rejection for duplicate hostPath, got nil")
		}
		if !strings.Contains(err.Error(), "duplicated") {
			t.Fatalf("error does not mention duplication: %v", err)
		}
	})

	t.Run("duplicate hostPath differing only by trailing slash is rejected", func(t *testing.T) {
		sw := baseValid()
		sw.Spec.Volume.HostPath = []VolumeServerHostPath{{Path: "/mnt/disk0"}, {Path: "/mnt/disk0/"}}
		err := sw.validateVolume()
		if err == nil {
			t.Fatal("expected rejection for canonically-duplicate hostPath, got nil")
		}
		if !strings.Contains(err.Error(), "duplicated") {
			t.Fatalf("error does not mention duplication: %v", err)
		}
	})

	t.Run("DaemonSet with volumeTopology is rejected", func(t *testing.T) {
		sw := baseValid()
		sw.Spec.Volume.Kind = VolumeServerDaemonSet
		sw.Spec.Volume.HostPath = []VolumeServerHostPath{{Path: "/mnt/disk0"}}
		sw.Spec.VolumeTopology = map[string]*VolumeTopologySpec{"dc1": {Replicas: 1, Rack: "r1", DataCenter: "dc1"}}
		err := sw.validateVolume()
		if err == nil {
			t.Fatal("expected rejection for DaemonSet + volumeTopology, got nil")
		}
		if !strings.Contains(err.Error(), "volumeTopology") {
			t.Fatalf("error does not mention volumeTopology: %v", err)
		}
	})

	t.Run("nil volume is a no-op", func(t *testing.T) {
		sw := baseValid()
		sw.Spec.Volume = nil
		if err := sw.validateVolume(); err != nil {
			t.Fatalf("unexpected error for nil volume: %v", err)
		}
	})
}

func TestVolumeSpecIsDaemonSet(t *testing.T) {
	var nilSpec *VolumeSpec
	if nilSpec.IsDaemonSet() {
		t.Error("nil VolumeSpec must not report DaemonSet")
	}
	if (&VolumeSpec{}).IsDaemonSet() {
		t.Error("empty Kind must default to StatefulSet, not DaemonSet")
	}
	if (&VolumeSpec{Kind: VolumeServerStatefulSet}).IsDaemonSet() {
		t.Error("explicit StatefulSet must not report DaemonSet")
	}
	if !(&VolumeSpec{Kind: VolumeServerDaemonSet}).IsDaemonSet() {
		t.Error("explicit DaemonSet must report DaemonSet")
	}
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

// Every master keeps its raft state under the same subdirectory of -mdir, so a
// claim shared by several of them is never a working configuration.
func TestValidateMasterPersistence(t *testing.T) {
	withPersistence := func(replicas int32, persistence *PersistenceSpec) *Seaweed {
		sw := baseValid()
		sw.Spec.Master.Replicas = replicas
		sw.Spec.Master.Persistence = persistence
		return sw
	}

	t.Run("unset is fine", func(t *testing.T) {
		if err := withPersistence(3, nil).validateMasterPersistence(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("claim templates are fine at any replica count", func(t *testing.T) {
		sw := withPersistence(3, &PersistenceSpec{Enabled: true})
		if err := sw.validateMasterPersistence(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("existing claim is fine for a single master", func(t *testing.T) {
		claim := "my-claim"
		sw := withPersistence(1, &PersistenceSpec{Enabled: true, ExistingClaim: &claim})
		if err := sw.validateMasterPersistence(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("existing claim is rejected for several masters", func(t *testing.T) {
		claim := "my-claim"
		sw := withPersistence(3, &PersistenceSpec{Enabled: true, ExistingClaim: &claim})
		err := sw.validateMasterPersistence()
		if err == nil {
			t.Fatal("expected an error for a claim shared by 3 masters")
		}
		if !strings.Contains(err.Error(), "existingClaim") {
			t.Fatalf("error should name the offending field, got %v", err)
		}
	})

	// Disabled persistence carries no claim, whatever else is set on it.
	t.Run("disabled is fine", func(t *testing.T) {
		claim := "my-claim"
		sw := withPersistence(3, &PersistenceSpec{ExistingClaim: &claim})
		if err := sw.validateMasterPersistence(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestValidateWorker(t *testing.T) {
	newWorker := func() *Seaweed {
		sw := baseValid()
		sw.Spec.Admin = &AdminSpec{}
		sw.Spec.Filer = &FilerSpec{Replicas: 1}
		sw.Spec.Worker = &WorkerSpec{Replicas: 1}
		return sw
	}

	t.Run("worker with admin is fine", func(t *testing.T) {
		if errs := newWorker().validateWorker(); len(errs) != 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
	})

	t.Run("worker without admin is rejected", func(t *testing.T) {
		sw := newWorker()
		sw.Spec.Admin = nil
		if errs := sw.validateWorker(); len(errs) != 1 || !strings.Contains(errs[0].Error(), "spec.admin") {
			t.Fatalf("expected admin requirement error, got %v", errs)
		}
	})

	// The sidecar holds 9328 in the same pod, so the Go worker cannot.
	t.Run("metricsPort 9328 with the sidecar is rejected", func(t *testing.T) {
		sw := newWorker()
		port := int32(WorkerLanceMetricsPort)
		sw.Spec.Worker.MetricsPort = &port
		sw.Spec.Filer.Lance = &LanceConfig{Enabled: true}
		if errs := sw.validateWorker(); len(errs) != 1 || !strings.Contains(errs[0].Error(), "worker-lance") {
			t.Fatalf("expected the port clash error, got %v", errs)
		}
	})

	t.Run("metricsPort 9328 without the sidecar is fine", func(t *testing.T) {
		for _, lance := range []*LanceConfig{nil, {Enabled: false}} {
			sw := newWorker()
			port := int32(WorkerLanceMetricsPort)
			sw.Spec.Worker.MetricsPort = &port
			sw.Spec.Filer.Lance = lance
			if errs := sw.validateWorker(); len(errs) != 0 {
				t.Fatalf("lance %+v: unexpected errors: %v", lance, errs)
			}
		}
	})

	t.Run("another metricsPort is fine", func(t *testing.T) {
		sw := newWorker()
		port := int32(9350)
		sw.Spec.Worker.MetricsPort = &port
		if errs := sw.validateWorker(); len(errs) != 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
	})
}
