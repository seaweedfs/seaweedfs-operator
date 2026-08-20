package controller

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// ipBindSeaweed sets the same ipBind on every component that takes one.
func ipBindSeaweed(ipBind *string) *seaweedv1.Seaweed {
	return &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1, IPBind: ipBind},
			Volume: &seaweedv1.VolumeSpec{
				Replicas:           1,
				VolumeServerConfig: seaweedv1.VolumeServerConfig{IPBind: ipBind},
			},
			Filer: &seaweedv1.FilerSpec{Replicas: 1, IPBind: ipBind},
		},
	}
}

// ipBindScripts renders every startup script that carries an -ip flag.
func ipBindScripts(m *seaweedv1.Seaweed, topology *seaweedv1.VolumeTopologySpec) map[string]string {
	return map[string]string{
		"master":   buildMasterStartupScript(m),
		"volume":   buildVolumeServerStartupScript(m, []string{"/data"}, "0", "$(POD_NAME).sw-volume-peer.ns"),
		"filer":    buildFilerStartupScript(m),
		"topology": buildVolumeServerStartupScriptWithTopology(m, []string{"/data"}, "dc1", topology),
	}
}

func topologySpec(ipBind *string) *seaweedv1.VolumeTopologySpec {
	return &seaweedv1.VolumeTopologySpec{
		Replicas:           1,
		Rack:               "rack1",
		DataCenter:         "dc1",
		VolumeServerConfig: seaweedv1.VolumeServerConfig{IPBind: ipBind},
	}
}

// Regression test for the cold-start crash: with no ipBind configured, every
// component binds the wildcard instead of the unresolvable peer FQDN in -ip.
func TestIPBindDefaultsToWildcard(t *testing.T) {
	m := ipBindSeaweed(nil)
	for component, script := range ipBindScripts(m, topologySpec(nil)) {
		if !strings.Contains(script, "-ip.bind=0.0.0.0") {
			t.Errorf("%s script must bind to the wildcard by default, got %q", component, script)
		}
	}
}

// fla9 takes the last occurrence of a repeated flag, so -ip.bind must land
// before ExtraArgs for the `extraArgs: [-ip.bind=…]` workaround to still win.
func TestIPBindAfterIP(t *testing.T) {
	m := ipBindSeaweed(nil)
	m.Spec.Master.ExtraArgs = []string{"-ip.bind=10.0.0.9"}
	script := buildMasterStartupScript(m, m.BaseMasterSpec().ExtraArgs()...)

	ipIdx := strings.Index(script, "-ip=")
	bindIdx := strings.Index(script, "-ip.bind=0.0.0.0")
	extraIdx := strings.Index(script, "-ip.bind=10.0.0.9")
	if ipIdx < 0 || bindIdx < 0 || extraIdx < 0 {
		t.Fatalf("expected -ip, the default -ip.bind and the extraArgs override in %q", script)
	}
	if !(ipIdx < bindIdx && bindIdx < extraIdx) {
		t.Errorf("expected -ip < -ip.bind < extraArgs ordering, got %q", script)
	}
}

func TestIPBindExplicitAddress(t *testing.T) {
	m := ipBindSeaweed(ptr.To("127.0.0.1"))
	for component, script := range ipBindScripts(m, topologySpec(ptr.To("127.0.0.1"))) {
		if !strings.Contains(script, "-ip.bind=127.0.0.1") {
			t.Errorf("%s script = %q, want -ip.bind=127.0.0.1", component, script)
		}
		if strings.Contains(script, "-ip.bind=0.0.0.0") {
			t.Errorf("%s script should not keep the default bind, got %q", component, script)
		}
	}
}

// An empty ipBind is the escape hatch back to weed binding to -ip.
func TestIPBindEmptyOmitsFlag(t *testing.T) {
	m := ipBindSeaweed(ptr.To(""))
	for component, script := range ipBindScripts(m, topologySpec(ptr.To(""))) {
		if strings.Contains(script, "-ip.bind") {
			t.Errorf("%s script must omit -ip.bind when ipBind is empty, got %q", component, script)
		}
	}
}

// Topology → spec.volume precedence, as for every other VolumeServerConfig field.
func TestIPBindTopologyFallback(t *testing.T) {
	cases := []struct {
		name   string
		volume *string
		topo   *string
		want   string
	}{
		{name: "neither set falls back to the wildcard", want: "-ip.bind=0.0.0.0"},
		{name: "volume value inherited", volume: ptr.To("10.0.0.1"), want: "-ip.bind=10.0.0.1"},
		{name: "topology overrides volume", volume: ptr.To("10.0.0.1"), topo: ptr.To("10.0.0.2"), want: "-ip.bind=10.0.0.2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := ipBindSeaweed(tc.volume)
			got := buildVolumeServerStartupScriptWithTopology(m, []string{"/data"}, "dc1", topologySpec(tc.topo))
			if !strings.Contains(got, tc.want) {
				t.Errorf("topology script = %q, want %s", got, tc.want)
			}
		})
	}
}

// Topology-only deployments omit spec.volume, so the fallback must be nil-safe.
func TestIPBindTopologyOnlyCluster(t *testing.T) {
	m := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
		Spec: seaweedv1.SeaweedSpec{
			Master: &seaweedv1.MasterSpec{Replicas: 1},
		},
	}
	got := buildVolumeServerStartupScriptWithTopology(m, []string{"/data"}, "dc1", topologySpec(nil))
	if !strings.Contains(got, "-ip.bind=0.0.0.0") {
		t.Errorf("topology-only script = %q, want -ip.bind=0.0.0.0", got)
	}
}

// DaemonSet pods advertise $(POD_IP), but still bind like every other kind.
func TestIPBindDaemonSet(t *testing.T) {
	m := makeHostPathSeaweed(seaweedv1.VolumeServerDaemonSet, []seaweedv1.VolumeServerHostPath{
		{Path: "/mnt/disk0"},
	})

	r := &SeaweedReconciler{}
	ds := r.createVolumeServerDaemonSet(m)

	cmd := volumeContainerCommand(t, ds.Spec.Template.Spec)
	if !strings.Contains(cmd, "-ip.bind=0.0.0.0") {
		t.Errorf("DaemonSet startup = %q, want -ip.bind=0.0.0.0", cmd)
	}
}
