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
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func driverAt(name string, created time.Time, uid string) *seaweedv1.SeaweedCSIDriver {
	return &seaweedv1.SeaweedCSIDriver{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			UID:               types.UID(uid),
			CreationTimestamp: metav1.NewTime(created),
		},
	}
}

func TestOthersTurn(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)

	older := driverAt("a", t0, "uid-a")
	newer := driverAt("b", t1, "uid-b")

	// The older object wins the shared driverName.
	assert.True(t, othersTurn(older, newer), "older object should win over newer")
	assert.False(t, othersTurn(newer, older), "newer object should yield to older")

	// Same creation time: lexicographically smaller UID wins deterministically.
	sameLow := driverAt("a", t0, "uid-1")
	sameHigh := driverAt("b", t0, "uid-2")
	assert.True(t, othersTurn(sameLow, sameHigh))
	assert.False(t, othersTurn(sameHigh, sameLow))
}

func TestStatusHelpers(t *testing.T) {
	assert.Equal(t, metav1.ConditionTrue, boolToStatus(true))
	assert.Equal(t, metav1.ConditionFalse, boolToStatus(false))
	assert.Equal(t, "Available", reasonAvailable(true))
	assert.Equal(t, "Unavailable", reasonAvailable(false))
}

func TestNamingIsPerInstance(t *testing.T) {
	d := &seaweedv1.SeaweedCSIDriver{ObjectMeta: metav1.ObjectMeta{Name: "prod"}}
	assert.Equal(t, "seaweedfs-csi-prod", csiName(d))
	assert.Equal(t, "seaweedfs-csi-prod-controller", csiControllerName(d))
	assert.Equal(t, "seaweedfs-csi-prod-node", csiNodeName(d))
	assert.Equal(t, "seaweedfs-csi-prod-mount", csiMountName(d))
	// Distinct CRs must not collide on cluster-scoped object names.
	other := &seaweedv1.SeaweedCSIDriver{ObjectMeta: metav1.ObjectMeta{Name: "staging"}}
	assert.NotEqual(t, csiControllerName(d), csiControllerName(other))
}
