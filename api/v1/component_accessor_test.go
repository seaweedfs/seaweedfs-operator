package v1

import "testing"

// Verifies pod.spec.serviceAccountName is rendered per-component when
// ComponentSpec.ServiceAccountName is set, and is omitted otherwise so
// pods continue to fall back to the namespace's default SA.
func TestBuildPodSpecServiceAccountName(t *testing.T) {
	sa := "seaweedfs-master"
	cases := []struct {
		name     string
		set      *string
		expected string
	}{
		{"unset preserves default SA fallback", nil, ""},
		{"explicit value is propagated", &sa, sa},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Seaweed{
				Spec: SeaweedSpec{
					Master: &MasterSpec{
						ComponentSpec: ComponentSpec{ServiceAccountName: tc.set},
						Replicas:      1,
					},
				},
			}
			got := s.BaseMasterSpec().BuildPodSpec().ServiceAccountName
			if got != tc.expected {
				t.Fatalf("ServiceAccountName = %q, want %q", got, tc.expected)
			}
		})
	}
}
