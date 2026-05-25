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
	"reflect"
	"testing"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func TestBuildPolicyDocument_FromStatements(t *testing.T) {
	spec := &seaweedv1.S3PolicySpec{
		Statements: []seaweedv1.S3PolicyStatement{
			{
				Sid:       "list",
				Effect:    seaweedv1.S3PolicyEffectAllow,
				Actions:   []string{"s3:ListBucket"},
				Resources: []string{"my-bucket"},
			},
			{
				Effect:    seaweedv1.S3PolicyEffectAllow,
				Actions:   []string{"*", "s3:GetObject"},
				Resources: []string{"my-bucket/uploads/*", "arn:aws:s3:::other", "*"},
			},
		},
	}

	out, err := buildPolicyDocument(spec)
	if err != nil {
		t.Fatalf("buildPolicyDocument: %v", err)
	}

	var doc policyDocument
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if doc.Version != policyDocumentVersion {
		t.Errorf("version = %q, want %q", doc.Version, policyDocumentVersion)
	}
	if len(doc.Statement) != 2 {
		t.Fatalf("got %d statements, want 2", len(doc.Statement))
	}

	st0 := doc.Statement[0]
	if st0.Sid != "list" || st0.Effect != "Allow" {
		t.Errorf("statement0 sid/effect = %q/%q", st0.Sid, st0.Effect)
	}
	if !reflect.DeepEqual(st0.Resource, []string{"arn:aws:s3:::my-bucket"}) {
		t.Errorf("statement0 resources = %v", st0.Resource)
	}

	st1 := doc.Statement[1]
	wantActions := []string{"s3:*", "s3:GetObject"}
	if !reflect.DeepEqual(st1.Action, wantActions) {
		t.Errorf("statement1 actions = %v, want %v", st1.Action, wantActions)
	}
	wantResources := []string{"arn:aws:s3:::my-bucket/uploads/*", "arn:aws:s3:::other", "*"}
	if !reflect.DeepEqual(st1.Resource, wantResources) {
		t.Errorf("statement1 resources = %v, want %v", st1.Resource, wantResources)
	}
}

func TestBuildPolicyDocument_RawDocumentPassthrough(t *testing.T) {
	raw := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*"}]}`
	spec := &seaweedv1.S3PolicySpec{PolicyDocument: raw}

	out, err := buildPolicyDocument(spec)
	if err != nil {
		t.Fatalf("buildPolicyDocument: %v", err)
	}
	if out != raw {
		t.Errorf("raw document was not passed through verbatim:\n got %q\nwant %q", out, raw)
	}
}

func TestBuildPolicyDocument_InvalidRawJSON(t *testing.T) {
	spec := &seaweedv1.S3PolicySpec{PolicyDocument: "{not json"}
	if _, err := buildPolicyDocument(spec); err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestBuildPolicyDocument_Empty(t *testing.T) {
	if _, err := buildPolicyDocument(&seaweedv1.S3PolicySpec{}); err == nil {
		t.Fatal("expected error for empty policy, got nil")
	}
}

func TestBuildPolicyDocument_StatementMissingActions(t *testing.T) {
	spec := &seaweedv1.S3PolicySpec{
		Statements: []seaweedv1.S3PolicyStatement{
			{Effect: seaweedv1.S3PolicyEffectAllow, Resources: []string{"my-bucket"}},
		},
	}
	if _, err := buildPolicyDocument(spec); err == nil {
		t.Fatal("expected error for statement with no actions, got nil")
	}
}

func TestBuildPolicyDocument_StatementMissingResources(t *testing.T) {
	spec := &seaweedv1.S3PolicySpec{
		Statements: []seaweedv1.S3PolicyStatement{
			{Effect: seaweedv1.S3PolicyEffectAllow, Actions: []string{"s3:GetObject"}},
		},
	}
	if _, err := buildPolicyDocument(spec); err == nil {
		t.Fatal("expected error for statement with no resources, got nil")
	}
}
