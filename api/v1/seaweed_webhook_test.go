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
