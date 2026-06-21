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

	"github.com/seaweedfs/seaweedfs/weed/s3api/lifecycle_xml"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func TestBuildLifecycleXMLEmpty(t *testing.T) {
	out, err := buildLifecycleXML(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != nil {
		t.Fatalf("expected nil for empty rules, got %q", out)
	}
}

func TestBuildLifecycleXMLRoundTrip(t *testing.T) {
	rules := []seaweedv1.BucketLifecycleRule{
		{
			ID:         "expire-archived",
			Prefix:     "archived/",
			Status:     seaweedv1.BucketLifecycleRuleEnabled,
			Expiration: &seaweedv1.BucketLifecycleExpiration{Days: 90},
		},
		{
			ID:                             "cleanup-incomplete-uploads",
			Status:                         seaweedv1.BucketLifecycleRuleEnabled,
			AbortIncompleteMultipartUpload: &seaweedv1.BucketLifecycleAbortIncompleteMultipartUpload{DaysAfterInitiation: 7},
		},
		{
			ID:                          "expire-old-versions",
			Status:                      seaweedv1.BucketLifecycleRuleDisabled,
			NoncurrentVersionExpiration: &seaweedv1.BucketLifecycleNoncurrentVersionExpiration{NoncurrentDays: 30, NewerNoncurrentVersions: 3},
		},
	}

	out, err := buildLifecycleXML(rules)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	parsed, err := lifecycle_xml.ParseCanonical(out)
	if err != nil {
		t.Fatalf("parse generated xml: %v", err)
	}
	if len(parsed) != 3 {
		t.Fatalf("expected 3 rules, got %d", len(parsed))
	}

	if parsed[0].ID != "expire-archived" || parsed[0].Status != "Enabled" ||
		parsed[0].Prefix != "archived/" || parsed[0].ExpirationDays != 90 {
		t.Errorf("rule 0 mismatch: %+v", parsed[0])
	}
	if parsed[1].ID != "cleanup-incomplete-uploads" || parsed[1].AbortMPUDaysAfterInitiation != 7 || parsed[1].Prefix != "" {
		t.Errorf("rule 1 mismatch: %+v", parsed[1])
	}
	if parsed[2].Status != "Disabled" || parsed[2].NoncurrentVersionExpirationDays != 30 || parsed[2].NewerNoncurrentVersions != 3 {
		t.Errorf("rule 2 mismatch: %+v", parsed[2])
	}
}

func TestLifecycleConfigEqual(t *testing.T) {
	rules := []seaweedv1.BucketLifecycleRule{{
		ID:         "r1",
		Prefix:     "logs/",
		Status:     seaweedv1.BucketLifecycleRuleEnabled,
		Expiration: &seaweedv1.BucketLifecycleExpiration{Days: 30},
	}}
	desired, err := buildLifecycleXML(rules)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	if !lifecycleConfigEqual(nil, nil) {
		t.Error("empty configs should be equal")
	}
	if lifecycleConfigEqual(nil, desired) {
		t.Error("empty vs non-empty should differ")
	}

	// A semantically identical config with different formatting must compare equal.
	formatted := []byte("<LifecycleConfiguration>\n  <Rule>\n    <ID>r1</ID>\n    <Status>Enabled</Status>\n    <Prefix>logs/</Prefix>\n    <Expiration><Days>30</Days></Expiration>\n  </Rule>\n</LifecycleConfiguration>")
	if !lifecycleConfigEqual(formatted, desired) {
		t.Error("semantically equal configs should compare equal")
	}

	changed, err := buildLifecycleXML([]seaweedv1.BucketLifecycleRule{{
		ID:         "r1",
		Prefix:     "logs/",
		Status:     seaweedv1.BucketLifecycleRuleEnabled,
		Expiration: &seaweedv1.BucketLifecycleExpiration{Days: 60},
	}})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if lifecycleConfigEqual(changed, desired) {
		t.Error("different expiration days should differ")
	}

	// An unparsable current config forces a rewrite (compares unequal).
	if lifecycleConfigEqual([]byte("<not-valid"), desired) {
		t.Error("malformed current config should compare unequal")
	}
}
