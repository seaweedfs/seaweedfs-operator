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
)

// TestReconcileRequeueAfter_ScalesWithReadiness locks in the policy
// that drives the periodic safety-net requeue. It's the only behavior
// difference between this PR's controller and master's — important
// enough to cover with a focused unit test.
func TestReconcileRequeueAfter_ScalesWithReadiness(t *testing.T) {
	if got := reconcileRequeueAfter(false); got != requeueWhileReconciling {
		t.Errorf("not-ready cadence = %s, want %s", got, requeueWhileReconciling)
	}
	if got := reconcileRequeueAfter(true); got != requeueWhenReady {
		t.Errorf("ready cadence = %s, want %s", got, requeueWhenReady)
	}
}

// TestReconcileRequeueAfter_ReadyIsLongerThanReconciling guards the
// invariant that the steady-state interval cannot be tighter than the
// rollout interval — flipping these accidentally would hide the
// readiness signal from the cadence policy.
func TestReconcileRequeueAfter_ReadyIsLongerThanReconciling(t *testing.T) {
	if requeueWhenReady <= requeueWhileReconciling {
		t.Errorf("requeueWhenReady (%s) must be longer than requeueWhileReconciling (%s)",
			requeueWhenReady, requeueWhileReconciling)
	}
}
