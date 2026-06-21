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
	"encoding/xml"
	"reflect"

	"github.com/seaweedfs/seaweedfs/weed/s3api/lifecycle_xml"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// The structs below mirror the subset of the S3 BucketLifecycleConfiguration
// XML that this operator emits. They exist separately from lifecycle_xml's own
// types because those carry unexported "set" flags with no constructors, so
// they cannot be marshalled from outside that package.

type lifecycleConfiguration struct {
	XMLName xml.Name        `xml:"LifecycleConfiguration"`
	Rules   []lifecycleRule `xml:"Rule"`
}

type lifecycleRule struct {
	ID                             string                      `xml:"ID,omitempty"`
	Status                         string                      `xml:"Status"`
	Prefix                         string                      `xml:"Prefix"`
	Expiration                     *lifecycleExpiration        `xml:"Expiration,omitempty"`
	NoncurrentVersionExpiration    *lifecycleNoncurrentVersion `xml:"NoncurrentVersionExpiration,omitempty"`
	AbortIncompleteMultipartUpload *lifecycleAbortMultipart    `xml:"AbortIncompleteMultipartUpload,omitempty"`
}

type lifecycleExpiration struct {
	Days                      int32 `xml:"Days,omitempty"`
	ExpiredObjectDeleteMarker bool  `xml:"ExpiredObjectDeleteMarker,omitempty"`
}

type lifecycleNoncurrentVersion struct {
	NoncurrentDays          int32 `xml:"NoncurrentDays"`
	NewerNoncurrentVersions int32 `xml:"NewerNoncurrentVersions,omitempty"`
}

type lifecycleAbortMultipart struct {
	DaysAfterInitiation int32 `xml:"DaysAfterInitiation"`
}

// buildLifecycleXML renders the desired rules into S3 lifecycle configuration
// XML. An empty rule set yields nil, signalling "no configuration".
func buildLifecycleXML(rules []seaweedv1.BucketLifecycleRule) ([]byte, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	cfg := lifecycleConfiguration{Rules: make([]lifecycleRule, 0, len(rules))}
	for i := range rules {
		r := &rules[i]
		out := lifecycleRule{ID: r.ID, Status: string(r.Status), Prefix: r.Prefix}
		if r.Expiration != nil {
			out.Expiration = &lifecycleExpiration{
				Days:                      r.Expiration.Days,
				ExpiredObjectDeleteMarker: r.Expiration.ExpiredObjectDeleteMarker,
			}
		}
		if r.NoncurrentVersionExpiration != nil {
			out.NoncurrentVersionExpiration = &lifecycleNoncurrentVersion{
				NoncurrentDays:          r.NoncurrentVersionExpiration.NoncurrentDays,
				NewerNoncurrentVersions: r.NoncurrentVersionExpiration.NewerNoncurrentVersions,
			}
		}
		if r.AbortIncompleteMultipartUpload != nil {
			out.AbortIncompleteMultipartUpload = &lifecycleAbortMultipart{
				DaysAfterInitiation: r.AbortIncompleteMultipartUpload.DaysAfterInitiation,
			}
		}
		cfg.Rules = append(cfg.Rules, out)
	}
	return xml.Marshal(cfg)
}

// lifecycleConfigEqual reports whether two lifecycle configuration XML blobs
// describe the same rules. Comparison is on the canonical parse so formatting
// differences (and rules written by the S3 gateway) don't trigger needless
// rewrites; an unparsable current config compares unequal so it is rewritten.
func lifecycleConfigEqual(current, desired []byte) bool {
	if len(current) == 0 && len(desired) == 0 {
		return true
	}
	currentRules, err := lifecycle_xml.ParseCanonical(current)
	if err != nil {
		return false
	}
	desiredRules, err := lifecycle_xml.ParseCanonical(desired)
	if err != nil {
		return false
	}
	return reflect.DeepEqual(currentRules, desiredRules)
}
