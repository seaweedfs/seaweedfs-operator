/*


Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func TestVolumeTopologyStatefulSet_ImageVersion(t *testing.T) {
	ptr := func(s string) *string { return &s }
	cases := []struct {
		name          string
		volume        *seaweedv1.VolumeSpec
		groupVersion  *string
		otherVersion  *string
		wantGroup     string
		wantOtherZone string
	}{
		{
			name:          "group version overrides the cluster version",
			groupVersion:  ptr("4.53"),
			wantGroup:     "chrislusf/seaweedfs:4.53",
			wantOtherZone: "chrislusf/seaweedfs:4.52",
		},
		{
			name:          "group version overrides spec.volume's",
			volume:        &seaweedv1.VolumeSpec{VolumeServerConfig: seaweedv1.VolumeServerConfig{ComponentSpec: seaweedv1.ComponentSpec{Version: ptr("4.54")}}},
			groupVersion:  ptr("4.53"),
			wantGroup:     "chrislusf/seaweedfs:4.53",
			wantOtherZone: "chrislusf/seaweedfs:4.54",
		},
		{
			name:          "empty group version keeps the cluster version",
			groupVersion:  ptr(""),
			otherVersion:  ptr("4.53"),
			wantGroup:     "chrislusf/seaweedfs:4.52",
			wantOtherZone: "chrislusf/seaweedfs:4.53",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &seaweedv1.Seaweed{
				ObjectMeta: metav1.ObjectMeta{Name: "sw", Namespace: "ns"},
				Spec: seaweedv1.SeaweedSpec{
					Image:   "chrislusf/seaweedfs",
					Version: "4.52",
					Master:  &seaweedv1.MasterSpec{Replicas: 1},
					Volume:  tc.volume,
					VolumeTopology: map[string]*seaweedv1.VolumeTopologySpec{
						"zone-a": {Replicas: 1, Rack: "rack-a", DataCenter: "dc-a",
							VolumeServerConfig: seaweedv1.VolumeServerConfig{ComponentSpec: seaweedv1.ComponentSpec{Version: tc.groupVersion}}},
						"zone-b": {Replicas: 1, Rack: "rack-b", DataCenter: "dc-b",
							VolumeServerConfig: seaweedv1.VolumeServerConfig{ComponentSpec: seaweedv1.ComponentSpec{Version: tc.otherVersion}}},
					},
				},
			}
			r := &SeaweedReconciler{}
			for zone, want := range map[string]string{"zone-a": tc.wantGroup, "zone-b": tc.wantOtherZone} {
				sts := r.createVolumeServerTopologyStatefulSet(m, zone, m.Spec.VolumeTopology[zone])
				if got := sts.Spec.Template.Spec.Containers[0].Image; got != want {
					t.Errorf("%s image = %q, want %q", zone, got, want)
				}
			}
		})
	}
}
