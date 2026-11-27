package controller

import (
	"reflect"
	"testing"

	"k8s.io/utils/ptr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func TestComponentServiceAccountsSkipsRetiredFlatVolume(t *testing.T) {
	cr := &seaweedv1.Seaweed{Spec: seaweedv1.SeaweedSpec{
		Volume: &seaweedv1.VolumeSpec{VolumeServerConfig: seaweedv1.VolumeServerConfig{
			ComponentSpec: seaweedv1.ComponentSpec{ServiceAccountName: ptr.To("flat")},
		}},
		VolumeTopology: map[string]*seaweedv1.VolumeTopologySpec{
			"rack1": {Replicas: 1, VolumeServerConfig: seaweedv1.VolumeServerConfig{
				ComponentSpec: seaweedv1.ComponentSpec{ServiceAccountName: ptr.To("rack")},
			}},
		},
	}}
	want := []componentServiceAccount{{component: "volume/rack1", name: "rack"}}
	if got := componentServiceAccounts(cr); !reflect.DeepEqual(got, want) {
		t.Fatalf("retired flat volume: got %v, want %v", got, want)
	}

	cr.Spec.Volume.Replicas = 1
	want = []componentServiceAccount{{component: "volume", name: "flat"}, {component: "volume/rack1", name: "rack"}}
	if got := componentServiceAccounts(cr); !reflect.DeepEqual(got, want) {
		t.Fatalf("flat volume next to groups: got %v, want %v", got, want)
	}
}
