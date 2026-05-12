/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1

import "testing"

// TestComponentAccessorLabels pins the cluster+component label merge for
// issue #243: component-level labels override cluster-level keys, and both
// flow through unchanged when one side is empty.
func TestComponentAccessorLabels(t *testing.T) {
	t.Run("merges cluster and component labels with component winning", func(t *testing.T) {
		s := &Seaweed{
			Spec: SeaweedSpec{
				Labels: map[string]string{"team": "platform", "env": "prod"},
				Filer: &FilerSpec{
					ComponentSpec: ComponentSpec{
						Labels: map[string]string{"team": "storage", "backup": "true"},
					},
				},
			},
		}
		got := s.BaseFilerSpec().Labels()
		want := map[string]string{
			"team":   "storage", // component overrides cluster
			"env":    "prod",    // cluster passes through
			"backup": "true",    // component-only
		}
		if len(got) != len(want) {
			t.Fatalf("len = %d, want %d (got %v)", len(got), len(want), got)
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("got[%q] = %q, want %q", k, got[k], v)
			}
		}
	})

	t.Run("empty inputs return an empty map (never nil)", func(t *testing.T) {
		s := &Seaweed{Spec: SeaweedSpec{Filer: &FilerSpec{}}}
		got := s.BaseFilerSpec().Labels()
		if got == nil {
			t.Fatal("Labels() returned nil; expected empty map")
		}
		if len(got) != 0 {
			t.Errorf("len = %d, want 0", len(got))
		}
	})
}
