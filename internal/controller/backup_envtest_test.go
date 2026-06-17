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
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// These tests exercise the apiserver-side CEL admission rules on the backup
// CRDs, which the controller-runtime fake client does not evaluate.

func TestCELStorageTypeMustMatchSubBlock(t *testing.T) {
	_, cli := mustEnvtest(t)
	ctx := context.Background()

	bad := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "cel-bad-", Namespace: "default"},
		Spec: seaweedv1.SeaweedSpec{
			Backup: &seaweedv1.BackupSpec{
				Storages: map[string]seaweedv1.BackupStorageSpec{
					// type s3 but no s3 block — must be rejected.
					"s3": {Type: seaweedv1.BackupStorageS3},
				},
			},
		},
	}
	if err := cli.Create(ctx, bad); err == nil {
		_ = cli.Delete(ctx, bad)
		t.Fatal("expected CEL to reject a storage whose sub-block does not match its type")
	}

	good := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "cel-good-", Namespace: "default"},
		Spec: seaweedv1.SeaweedSpec{
			Backup: &seaweedv1.BackupSpec{
				Storages: map[string]seaweedv1.BackupStorageSpec{
					"pvc": {Type: seaweedv1.BackupStorageFilesystem, Filesystem: &seaweedv1.FilesystemBackupStore{ExistingClaim: "c"}},
				},
				DataMirror: []seaweedv1.BackupMirrorSpec{{StorageName: "pvc"}},
			},
		},
	}
	if err := cli.Create(ctx, good); err != nil {
		t.Fatalf("valid backup spec rejected: %v", err)
	}
	_ = cli.Delete(ctx, good)
}

func TestCELScheduleStorageMustExist(t *testing.T) {
	_, cli := mustEnvtest(t)
	ctx := context.Background()
	bad := &seaweedv1.Seaweed{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "cel-sched-", Namespace: "default"},
		Spec: seaweedv1.SeaweedSpec{
			Backup: &seaweedv1.BackupSpec{
				Storages: map[string]seaweedv1.BackupStorageSpec{
					"pvc": {Type: seaweedv1.BackupStorageFilesystem, Filesystem: &seaweedv1.FilesystemBackupStore{ExistingClaim: "c"}},
				},
				Schedule: []seaweedv1.BackupScheduleSpec{{Name: "n", Schedule: "0 2 * * *", StorageName: "ghost"}},
			},
		},
	}
	if err := cli.Create(ctx, bad); err == nil {
		_ = cli.Delete(ctx, bad)
		t.Fatal("expected CEL to reject a schedule referencing an undefined storage")
	}
}

func TestCELRestoreBackupNameXorSource(t *testing.T) {
	_, cli := mustEnvtest(t)
	ctx := context.Background()

	// Neither set — rejected.
	neither := &seaweedv1.SeaweedRestore{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "cel-rst-", Namespace: "default"},
		Spec:       seaweedv1.SeaweedRestoreSpec{ClusterName: "c1"},
	}
	if err := cli.Create(ctx, neither); err == nil {
		_ = cli.Delete(ctx, neither)
		t.Fatal("expected CEL to reject a restore with neither backupName nor backupSource")
	}

	// Both set — rejected.
	both := &seaweedv1.SeaweedRestore{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "cel-rst-", Namespace: "default"},
		Spec: seaweedv1.SeaweedRestoreSpec{
			ClusterName:  "c1",
			BackupName:   "bk1",
			BackupSource: &seaweedv1.BackupSource{StorageName: "pvc", MetaPath: "x"},
		},
	}
	if err := cli.Create(ctx, both); err == nil {
		_ = cli.Delete(ctx, both)
		t.Fatal("expected CEL to reject a restore with both backupName and backupSource")
	}

	// Exactly one — accepted.
	ok := &seaweedv1.SeaweedRestore{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "cel-rst-", Namespace: "default"},
		Spec:       seaweedv1.SeaweedRestoreSpec{ClusterName: "c1", BackupName: "bk1"},
	}
	if err := cli.Create(ctx, ok); err != nil {
		t.Fatalf("valid restore rejected: %v", err)
	}
	_ = cli.Delete(ctx, ok)
}

func TestCELBackupImmutability(t *testing.T) {
	_, cli := mustEnvtest(t)
	ctx := context.Background()

	bk := &seaweedv1.SeaweedBackup{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "cel-bk-", Namespace: "default"},
		Spec:       seaweedv1.SeaweedBackupSpec{ClusterName: "c1", StorageName: "pvc"},
	}
	if err := cli.Create(ctx, bk); err != nil {
		t.Fatalf("create backup: %v", err)
	}
	defer func() { _ = cli.Delete(ctx, bk) }()

	bk.Spec.ClusterName = "c2"
	if err := cli.Update(ctx, bk); err == nil {
		t.Fatal("expected CEL to reject mutating an immutable clusterName")
	}

	// The default filerPath should be applied.
	var got seaweedv1.SeaweedBackup
	if err := cli.Get(ctx, client.ObjectKeyFromObject(bk), &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.FilerPath != "/" {
		t.Errorf("expected default filerPath '/', got %q", got.Spec.FilerPath)
	}
}
