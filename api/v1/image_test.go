package v1

import "testing"

func TestApplyVersion(t *testing.T) {
	cases := []struct {
		name    string
		image   string
		version string
		want    string
	}{
		{"no version leaves image alone", "chrislusf/seaweedfs:3.59", "", "chrislusf/seaweedfs:3.59"},
		{"replaces existing tag", "chrislusf/seaweedfs:3.59", "3.60", "chrislusf/seaweedfs:3.60"},
		{"appends to untagged image", "chrislusf/seaweedfs", "3.60", "chrislusf/seaweedfs:3.60"},
		{"bare repository name", "seaweedfs", "3.60", "seaweedfs:3.60"},
		{"registry port is not a tag", "registry:5000/seaweedfs", "3.60", "registry:5000/seaweedfs:3.60"},
		{"registry port with tag", "registry:5000/seaweedfs:3.59", "3.60", "registry:5000/seaweedfs:3.60"},
		{"digest pin is preserved", "chrislusf/seaweedfs@sha256:abc123", "3.60", "chrislusf/seaweedfs@sha256:abc123"},
		{"digest pin on ported registry", "registry:5000/sw@sha256:abc", "3.60", "registry:5000/sw@sha256:abc"},
		{"nested path", "ghcr.io/org/team/seaweedfs:v1", "v2", "ghcr.io/org/team/seaweedfs:v2"},
		{"empty image", "", "3.60", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := applyVersion(tc.image, tc.version); got != tc.want {
				t.Errorf("applyVersion(%q, %q) = %q want %q", tc.image, tc.version, got, tc.want)
			}
		})
	}
}

// The per-component version must win over the cluster version, and both must
// fall back to leaving spec.image untouched.
func TestComponentAccessorImage(t *testing.T) {
	componentVersion := "3.99"
	s := &Seaweed{
		Spec: SeaweedSpec{
			Image:   "chrislusf/seaweedfs:3.59",
			Version: "3.60",
			Master:  &MasterSpec{},
			Filer: &FilerSpec{
				ComponentSpec: ComponentSpec{Version: &componentVersion},
			},
		},
	}

	if got, want := s.BaseMasterSpec().Image(), "chrislusf/seaweedfs:3.60"; got != want {
		t.Errorf("master image = %q want %q (cluster version)", got, want)
	}
	if got, want := s.BaseFilerSpec().Image(), "chrislusf/seaweedfs:3.99"; got != want {
		t.Errorf("filer image = %q want %q (component override)", got, want)
	}
	if got, want := s.ClusterImage(), "chrislusf/seaweedfs:3.60"; got != want {
		t.Errorf("cluster image = %q want %q", got, want)
	}

	// An empty component version must not blank out the cluster version.
	empty := ""
	s.Spec.Filer.ComponentSpec.Version = &empty
	if got, want := s.BaseFilerSpec().Image(), "chrislusf/seaweedfs:3.60"; got != want {
		t.Errorf("filer image with empty override = %q want %q", got, want)
	}

	// With no versions at all the image is used verbatim.
	s.Spec.Version = ""
	s.Spec.Filer.ComponentSpec.Version = nil
	if got, want := s.BaseFilerSpec().Image(), "chrislusf/seaweedfs:3.59"; got != want {
		t.Errorf("filer image with no version = %q want %q", got, want)
	}
}
