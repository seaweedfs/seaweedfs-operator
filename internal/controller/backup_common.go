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
	"fmt"
	"hash/fnv"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

// backupRequeue is the backoff used when a backup/restore is waiting on an
// external dependency (the cluster, its storage, or the snapshot Job).
const backupRequeue = 15 * time.Second

// fnvHash returns an 8-hex-char FNV-1a hash of s, used to keep generated Job
// names unique after truncation.
func fnvHash(s string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return fmt.Sprintf("%08x", h.Sum32())
}

// boundedName returns base+suffix, but when that would exceed the 63-char
// Kubernetes object-name limit it truncates base and splices in a stable hash
// so distinct sources keep distinct names.
func boundedName(base, suffix string) string {
	const max = 63
	if len(base)+len(suffix) <= max {
		return base + suffix
	}
	h := fnvHash(base)
	keep := max - len(suffix) - len(h) - 1
	if keep < 0 {
		keep = 0
	}
	return base[:keep] + "-" + h + suffix
}

// jobFinished reports whether a Job has reached a terminal condition and, if
// so, whether it succeeded.
func jobFinished(job *batchv1.Job) (done bool, success bool) {
	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			return true, true
		case batchv1.JobFailed:
			return true, false
		}
	}
	return false, false
}
