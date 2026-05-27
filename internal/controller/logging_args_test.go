package controller

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// TestWeedPreambleDefault confirms the historical -logtostderr=true default
// is preserved when no LoggingArgs are set anywhere — existing CRs must not
// silently change their log destination after upgrading the operator.
func TestWeedPreambleDefault(t *testing.T) {
	m := &seaweedv1.Seaweed{ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"}}
	cmd := weedPreamble(m, nil, "master")
	got := strings.Join(cmd, " ")
	if got != "weed -logtostderr=true master" {
		t.Fatalf("default preamble = %q, want %q", got, "weed -logtostderr=true master")
	}
}

// TestWeedPreambleOverridesDefault verifies that supplying LoggingArgs
// drops the default -logtostderr=true entirely, so users get exactly the
// flags they asked for (no duplicate-flag conflicts in fla9).
func TestWeedPreambleOverridesDefault(t *testing.T) {
	m := &seaweedv1.Seaweed{ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"}}
	cmd := weedPreamble(m, []string{"-logJson", "-v=2"}, "master")
	got := strings.Join(cmd, " ")
	if got != "weed -logJson -v=2 master" {
		t.Fatalf("override preamble = %q, want %q", got, "weed -logJson -v=2 master")
	}
}

// TestWeedPreambleOrdering pins down the position of logging args relative
// to -config_dir and the subcommand: logging flags come right after `weed`,
// -config_dir follows, and the subcommand always closes the prefix. fla9
// rejects any of these flags placed after the subcommand, so the order is
// load-bearing.
func TestWeedPreambleOrdering(t *testing.T) {
	enabled := true
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			TLS: &seaweedv1.TLSSpec{Enabled: enabled},
		},
	}
	cmd := weedPreamble(m, []string{"-logJson"}, "filer")
	got := strings.Join(cmd, " ")
	want := "weed -logJson -config_dir=/etc/sw-security filer"
	if got != want {
		t.Fatalf("preamble = %q, want %q", got, want)
	}
}

// TestLoggingArgsClusterFallback covers the cluster→component inheritance
// path on the accessor: a component with no LoggingArgs inherits the
// cluster-level slice verbatim.
func TestLoggingArgsClusterFallback(t *testing.T) {
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			LoggingArgs: []string{"-logJson", "-v=1"},
			Master:      &seaweedv1.MasterSpec{Replicas: 1},
		},
	}
	got := m.BaseMasterSpec().LoggingArgs()
	want := []string{"-logJson", "-v=1"}
	if !equalSlices(got, want) {
		t.Fatalf("master logging args = %v, want %v (should inherit cluster)", got, want)
	}
}

// TestLoggingArgsComponentOverridesCluster: when both cluster and component
// set LoggingArgs, the component fully replaces the cluster value (no
// per-flag merge), matching how Tolerations and ExtraArgs work.
func TestLoggingArgsComponentOverridesCluster(t *testing.T) {
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			LoggingArgs: []string{"-logJson"},
			Master: &seaweedv1.MasterSpec{
				Replicas: 1,
				ComponentSpec: seaweedv1.ComponentSpec{
					LoggingArgs: []string{"-v=4"},
				},
			},
		},
	}
	got := m.BaseMasterSpec().LoggingArgs()
	want := []string{"-v=4"}
	if !equalSlices(got, want) {
		t.Fatalf("master logging args = %v, want %v (component should win)", got, want)
	}
}

// TestBuildMasterStartupScriptWithLoggingArgs is an integration check: the
// fully assembled master command must place the user's logging flags before
// the `master` subcommand and after `weed`.
func TestBuildMasterStartupScriptWithLoggingArgs(t *testing.T) {
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			LoggingArgs: []string{"-logJson"},
			Master:      &seaweedv1.MasterSpec{Replicas: 1},
		},
	}
	got := buildMasterStartupScript(m, m.BaseMasterSpec().ExtraArgs()...)
	if !strings.HasPrefix(got, "weed -logJson master ") {
		t.Fatalf("expected master script to start with 'weed -logJson master ', got %q", got)
	}
	if strings.Contains(got, "-logtostderr=true") {
		t.Fatalf("default -logtostderr=true should be dropped when LoggingArgs set, got %q", got)
	}
}

// TestTopologyLoggingArgsFallback verifies the 3-tier precedence on
// topology pods: topology > spec.volume > cluster. The flat-volume spec is
// allowed to be nil here because topology-only deployments omit it.
func TestTopologyLoggingArgsFallback(t *testing.T) {
	cases := []struct {
		name    string
		cluster []string
		volume  *seaweedv1.VolumeSpec
		topo    *seaweedv1.VolumeTopologySpec
		want    []string
	}{
		{
			name:    "cluster only",
			cluster: []string{"-logJson"},
			topo:    &seaweedv1.VolumeTopologySpec{Rack: "r", DataCenter: "d"},
			want:    []string{"-logJson"},
		},
		{
			name:    "volume overrides cluster",
			cluster: []string{"-logJson"},
			volume: &seaweedv1.VolumeSpec{
				VolumeServerConfig: seaweedv1.VolumeServerConfig{
					ComponentSpec: seaweedv1.ComponentSpec{LoggingArgs: []string{"-v=2"}},
				},
			},
			topo: &seaweedv1.VolumeTopologySpec{Rack: "r", DataCenter: "d"},
			want: []string{"-v=2"},
		},
		{
			name:    "topology overrides volume and cluster",
			cluster: []string{"-logJson"},
			volume: &seaweedv1.VolumeSpec{
				VolumeServerConfig: seaweedv1.VolumeServerConfig{
					ComponentSpec: seaweedv1.ComponentSpec{LoggingArgs: []string{"-v=2"}},
				},
			},
			topo: &seaweedv1.VolumeTopologySpec{
				VolumeServerConfig: seaweedv1.VolumeServerConfig{
					ComponentSpec: seaweedv1.ComponentSpec{LoggingArgs: []string{"-v=4"}},
				},
				Rack:       "r",
				DataCenter: "d",
			},
			want: []string{"-v=4"},
		},
		{
			name: "nothing set returns nil",
			topo: &seaweedv1.VolumeTopologySpec{Rack: "r", DataCenter: "d"},
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
				Spec: seaweedv1.SeaweedSpec{
					LoggingArgs: tc.cluster,
					Volume:      tc.volume,
				},
			}
			got := topologyLoggingArgs(m, tc.topo)
			if !equalSlices(got, tc.want) {
				t.Fatalf("topologyLoggingArgs = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestComponentLoggingArgsAllSpecs is a smoke test covering every
// ComponentSpec embedding so a future regression that wires LoggingArgs
// into a new spec but forgets to plumb it through is caught here.
func TestComponentLoggingArgsAllSpecs(t *testing.T) {
	args := []string{"-logJson"}
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			LoggingArgs: args,
			Master:      &seaweedv1.MasterSpec{Replicas: 1},
			Volume:      &seaweedv1.VolumeSpec{},
			Filer:       &seaweedv1.FilerSpec{Replicas: 1},
			Admin:       &seaweedv1.AdminSpec{},
			Worker:      &seaweedv1.WorkerSpec{Replicas: 1},
			S3:          &seaweedv1.S3GatewaySpec{Replicas: 1},
			SFTP:        &seaweedv1.SFTPSpec{Replicas: 1},
		},
	}
	accessors := map[string]func() []string{
		"master": func() []string { return m.BaseMasterSpec().LoggingArgs() },
		"volume": func() []string { return m.BaseVolumeSpec().LoggingArgs() },
		"filer":  func() []string { return m.BaseFilerSpec().LoggingArgs() },
		"admin":  func() []string { return m.BaseAdminSpec().LoggingArgs() },
		"worker": func() []string { return m.BaseWorkerSpec().LoggingArgs() },
		"s3":     func() []string { return m.BaseS3Spec().LoggingArgs() },
		"sftp":   func() []string { return m.BaseSFTPSpec().LoggingArgs() },
	}
	for name, fn := range accessors {
		if got := fn(); !equalSlices(got, args) {
			t.Errorf("%s LoggingArgs = %v, want %v", name, got, args)
		}
	}
}

// adminStartupScript intentionally `exec`s weed, so the logging prefix is
// preceded by `exec ` rather than starting the string. This test guards the
// previously-pinned admin invariant against accidental drift.
func TestBuildAdminStartupScriptWithLoggingArgs(t *testing.T) {
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			LoggingArgs: []string{"-logJson"},
			Master:      &seaweedv1.MasterSpec{Replicas: 1},
			Admin: &seaweedv1.AdminSpec{
				CredentialsSecret: &corev1.LocalObjectReference{Name: "creds"},
			},
		},
	}
	got := buildAdminStartupScript(m)
	// `-config_dir` is always present for admin (see securityConfigNeeded),
	// so the assertion locks in the logging-arg ordering rather than the
	// exact full prefix.
	if !strings.Contains(got, "exec weed -logJson -config_dir=") {
		t.Fatalf("expected admin script to place -logJson directly after weed, got %q", got)
	}
	if strings.Contains(got, "-logtostderr=true") {
		t.Fatalf("default -logtostderr=true should be dropped, got %q", got)
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
