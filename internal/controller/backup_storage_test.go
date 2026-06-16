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
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func ptrBool(b bool) *bool { return &b }

func TestRenderReplicationTomlS3(t *testing.T) {
	st := seaweedv1.BackupStorageSpec{
		Type: seaweedv1.BackupStorageS3,
		S3: &seaweedv1.S3BackupStore{
			Bucket:         "my-bucket",
			Region:         "eu-west-1",
			Endpoint:       "https://minio.example.com",
			Directory:      "/backups",
			ForcePathStyle: ptrBool(true),
		},
	}
	creds := map[string][]byte{
		seaweedv1.BackupSecretKeyAWSAccessKeyID:     []byte("AKIA"),
		seaweedv1.BackupSecretKeyAWSSecretAccessKey: []byte("secret"),
	}
	out, err := renderReplicationToml("s3store", st, "cluster1", creds)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"[sink.s3]",
		"enabled = true",
		`aws_access_key_id = "AKIA"`,
		`aws_secret_access_key = "secret"`,
		`region = "eu-west-1"`,
		`bucket = "my-bucket"`,
		`directory = "/backups/cluster1/data"`,
		`endpoint = "https://minio.example.com"`,
		"s3_force_path_style = true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered toml missing %q:\n%s", want, out)
		}
	}
}

func TestRenderReplicationTomlGCS(t *testing.T) {
	st := seaweedv1.BackupStorageSpec{
		Type: seaweedv1.BackupStorageGCS,
		GCS:  &seaweedv1.GCSBackupStore{Bucket: "gcs-bucket", Directory: "/"},
	}
	out, err := renderReplicationToml("gcsstore", st, "c1", nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "[sink.google_cloud_storage]") {
		t.Errorf("missing gcs sink:\n%s", out)
	}
	if !strings.Contains(out, `google_application_credentials = "/etc/seaweedfs/gcs.json"`) {
		t.Errorf("gcs creds path not pointed at mounted key:\n%s", out)
	}
	if !strings.Contains(out, `directory = "/c1/data"`) {
		t.Errorf("gcs directory wrong:\n%s", out)
	}
}

func TestRenderReplicationTomlAzureAndB2(t *testing.T) {
	az := seaweedv1.BackupStorageSpec{
		Type:  seaweedv1.BackupStorageAzure,
		Azure: &seaweedv1.AzureBackupStore{AccountName: "acct", Container: "c", Directory: "/"},
	}
	out, err := renderReplicationToml("az", az, "c1", map[string][]byte{seaweedv1.BackupSecretKeyAzureAccountKey: []byte("k")})
	if err != nil {
		t.Fatalf("azure render: %v", err)
	}
	if !strings.Contains(out, "[sink.azure]") || !strings.Contains(out, `account_name = "acct"`) || !strings.Contains(out, `account_key = "k"`) {
		t.Errorf("azure sink wrong:\n%s", out)
	}

	b2 := seaweedv1.BackupStorageSpec{
		Type: seaweedv1.BackupStorageB2,
		B2:   &seaweedv1.B2BackupStore{Bucket: "b", Region: "us-west", Directory: "/"},
	}
	out, err = renderReplicationToml("b2", b2, "c1", map[string][]byte{
		seaweedv1.BackupSecretKeyB2AccountID: []byte("id"),
		seaweedv1.BackupSecretKeyB2AppKey:    []byte("appkey"),
	})
	if err != nil {
		t.Fatalf("b2 render: %v", err)
	}
	if !strings.Contains(out, "[sink.backblaze]") || !strings.Contains(out, `b2_account_id = "id"`) || !strings.Contains(out, `b2_master_application_key = "appkey"`) {
		t.Errorf("b2 sink wrong:\n%s", out)
	}
}

func TestRenderReplicationTomlFilesystem(t *testing.T) {
	st := seaweedv1.BackupStorageSpec{
		Type:       seaweedv1.BackupStorageFilesystem,
		Filesystem: &seaweedv1.FilesystemBackupStore{ExistingClaim: "pvc", MountPath: "/backup"},
	}
	out, err := renderReplicationToml("fs", st, "c1", nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "[sink.local]") || !strings.Contains(out, `directory = "/backup/c1/data"`) {
		t.Errorf("local sink wrong:\n%s", out)
	}
}

func TestRenderReplicationTomlMissingSubBlock(t *testing.T) {
	st := seaweedv1.BackupStorageSpec{Type: seaweedv1.BackupStorageS3}
	if _, err := renderReplicationToml("s", st, "c", nil); err == nil {
		t.Fatal("expected error when s3 block is missing")
	}
}

func TestTomlStringEscaping(t *testing.T) {
	got := tomlString(`a"b\c`)
	if got != `"a\"b\\c"` {
		t.Errorf("tomlString escaping = %s", got)
	}
}

func TestBoundedName(t *testing.T) {
	short := boundedName("my-backup", "-bkp")
	if short != "my-backup-bkp" {
		t.Errorf("short bounded name = %q", short)
	}
	long := strings.Repeat("a", 80)
	got := boundedName(long, "-bkp")
	if len(got) > 63 {
		t.Errorf("bounded name too long: %d (%q)", len(got), got)
	}
	if !strings.HasSuffix(got, "-bkp") {
		t.Errorf("bounded name lost suffix: %q", got)
	}
	// Distinct inputs keep distinct names.
	other := boundedName(strings.Repeat("a", 79)+"b", "-bkp")
	if other == got {
		t.Errorf("distinct inputs collided: %q", got)
	}
}

func TestMetaPaths(t *testing.T) {
	if got := metaRelPath("c1", "bk1"); got != "c1/bk1/filer.meta.gz" {
		t.Errorf("metaRelPath = %q", got)
	}
	if got := reservedFilerMetaPath("bk1"); got != "/.seaweedfs-operator/backups/bk1/filer.meta.gz" {
		t.Errorf("reservedFilerMetaPath = %q", got)
	}
}

func TestMetaLoadStatement(t *testing.T) {
	if got := metaLoadStatement("/scratch/x.gz", "/"); got != "fs.meta.load /scratch/x.gz" {
		t.Errorf("root load = %q", got)
	}
	if got := metaLoadStatement("/scratch/x.gz", "/buckets"); got != "fs.meta.load -dirPrefix=/buckets /scratch/x.gz" {
		t.Errorf("subtree load = %q", got)
	}
}

func testSeaweedForBackup() *seaweedv1.Seaweed {
	return &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "ns1"},
		Spec: seaweedv1.SeaweedSpec{
			Image:  "chrislusf/seaweedfs:test",
			Master: &seaweedv1.MasterSpec{Replicas: 1},
		},
	}
}

func TestSnapshotScriptFilesystem(t *testing.T) {
	m := testSeaweedForBackup()
	st := seaweedv1.BackupStorageSpec{
		Type:       seaweedv1.BackupStorageFilesystem,
		Filesystem: &seaweedv1.FilesystemBackupStore{ExistingClaim: "pvc", MountPath: "/backup"},
	}
	script, dest := snapshotScript(m, st, "c1", "bk1", "/")
	if dest != "/backup/c1/bk1/filer.meta.gz" {
		t.Errorf("destination = %q", dest)
	}
	for _, want := range []string{
		"set -euo pipefail",
		"fs.meta.save -o /backup/c1/bk1/filer.meta.gz /",
		"weed", "shell", "-master=", "-filer=",
		"test -s /backup/c1/bk1/filer.meta.gz",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("filesystem snapshot script missing %q:\n%s", want, script)
		}
	}
}

func TestSnapshotScriptObjectStore(t *testing.T) {
	m := testSeaweedForBackup()
	st := seaweedv1.BackupStorageSpec{
		Type: seaweedv1.BackupStorageS3,
		S3:   &seaweedv1.S3BackupStore{Bucket: "b", Directory: "/"},
	}
	script, dest := snapshotScript(m, st, "c1", "bk1", "/")
	if dest != "/.seaweedfs-operator/backups/bk1/filer.meta.gz" {
		t.Errorf("destination = %q", dest)
	}
	for _, want := range []string{
		"fs.meta.save -o /scratch/filer.meta.gz /",
		"filer.copy /scratch/filer.meta.gz http://c1-filer.ns1:8888/.seaweedfs-operator/backups/bk1/",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("object-store snapshot script missing %q:\n%s", want, script)
		}
	}
}

func TestRestoreScript(t *testing.T) {
	m := testSeaweedForBackup()
	fs := restoreScript(m, "/backup/c1/bk1/filer.meta.gz", "", "/")
	if !strings.Contains(fs, "fs.meta.load /backup/c1/bk1/filer.meta.gz") {
		t.Errorf("filesystem restore script wrong:\n%s", fs)
	}
	obj := restoreScript(m, "", "http://c1-filer.ns1:8888/.seaweedfs-operator/backups/bk1/filer.meta.gz", "/")
	if !strings.Contains(obj, "filer.cat -o /scratch/restore.meta.gz http://c1-filer.ns1:8888/") {
		t.Errorf("object-store restore script wrong:\n%s", obj)
	}
	if !strings.Contains(obj, "fs.meta.load /scratch/restore.meta.gz") {
		t.Errorf("object-store restore missing load:\n%s", obj)
	}
}
