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
	"encoding/json"
	"fmt"
	"strings"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// policyDocumentVersion is the AWS IAM policy language version stamped on
// documents the operator assembles from structured statements.
const policyDocumentVersion = "2012-10-17"

// s3ARNPrefix is the SeaweedFS/AWS S3 resource ARN prefix. Bucket-relative
// resource shorthand is expanded against it.
const s3ARNPrefix = "arn:aws:s3:::"

// policyDocument / policyStatement mirror the subset of the AWS policy
// document JSON that the SeaweedFS policy engine consumes. Actions and
// resources are emitted as arrays, which the engine's StringOrStringSlice
// parser accepts.
type policyDocument struct {
	Version   string            `json:"Version"`
	Statement []policyStatement `json:"Statement"`
}

type policyStatement struct {
	Sid      string   `json:"Sid,omitempty"`
	Effect   string   `json:"Effect"`
	Action   []string `json:"Action"`
	Resource []string `json:"Resource"`
}

// buildPolicyDocument resolves an S3PolicySpec to the JSON document string the
// IAM PutPolicy API expects. When spec.policyDocument is set it is returned
// verbatim (after a validity check); otherwise the structured statements are
// assembled, expanding bucket-relative resource shorthand into S3 ARNs.
func buildPolicyDocument(spec *seaweedv1.S3PolicySpec) (string, error) {
	if strings.TrimSpace(spec.PolicyDocument) != "" {
		// Round-trip to confirm it is valid JSON before we hand it to the
		// IAM service, so an obvious typo fails on this CR rather than
		// silently producing an unusable policy.
		var probe map[string]any
		if err := json.Unmarshal([]byte(spec.PolicyDocument), &probe); err != nil {
			return "", fmt.Errorf("policyDocument is not valid JSON: %w", err)
		}
		return spec.PolicyDocument, nil
	}

	if len(spec.Statements) == 0 {
		return "", fmt.Errorf("policy has neither statements nor policyDocument")
	}

	doc := policyDocument{Version: policyDocumentVersion}
	for i, st := range spec.Statements {
		if len(st.Actions) == 0 {
			return "", fmt.Errorf("statement %d has no actions", i)
		}
		if len(st.Resources) == 0 {
			return "", fmt.Errorf("statement %d has no resources", i)
		}
		doc.Statement = append(doc.Statement, policyStatement{
			Sid:      st.Sid,
			Effect:   string(st.Effect),
			Action:   normalizeActions(st.Actions),
			Resource: expandResources(st.Resources),
		})
	}

	out, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("marshal policy document: %w", err)
	}
	return string(out), nil
}

// normalizeActions accepts the bare wildcard "*" as a friendly alias for the
// S3 service wildcard "s3:*"; every other action is passed through unchanged
// so callers can use any valid S3 action string.
func normalizeActions(actions []string) []string {
	out := make([]string, 0, len(actions))
	for _, a := range actions {
		if a == "*" {
			a = "s3:*"
		}
		out = append(out, a)
	}
	return out
}

// expandResources turns each resource entry into a full S3 ARN. Entries that
// already start with "arn:" are kept verbatim, the bare wildcard "*" is passed
// through (it matches all resources), and everything else is treated as a
// bucket-relative path and prefixed with arn:aws:s3:::.
func expandResources(resources []string) []string {
	out := make([]string, 0, len(resources))
	for _, r := range resources {
		switch {
		case r == "*":
			out = append(out, "*")
		case strings.HasPrefix(r, "arn:"):
			out = append(out, r)
		default:
			out = append(out, s3ARNPrefix+r)
		}
	}
	return out
}
