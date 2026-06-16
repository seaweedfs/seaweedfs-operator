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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func newSchedulerClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := seaweedv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&seaweedv1.SeaweedBackup{}).
		Build()
}

func schedulerWith(cli client.Client, now time.Time) *BackupScheduler {
	return &BackupScheduler{Client: cli, Log: logf.Log, now: func() time.Time { return now }}
}

func backupWithSchedule(name, cluster, schedule string, created time.Time, phase seaweedv1.BackupPhase) *seaweedv1.SeaweedBackup {
	return &seaweedv1.SeaweedBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "ns1",
			CreationTimestamp: metav1.NewTime(created),
			Labels: map[string]string{
				seaweedv1.LabelBackupCluster:  cluster,
				seaweedv1.LabelBackupSchedule: schedule,
			},
		},
		Spec:   seaweedv1.SeaweedBackupSpec{ClusterName: cluster, StorageName: "pvc"},
		Status: seaweedv1.SeaweedBackupStatus{Phase: phase},
	}
}

func TestSchedulerFiresWhenDue(t *testing.T) {
	now := time.Date(2026, 6, 16, 2, 5, 0, 0, time.UTC)
	sched := seaweedv1.BackupScheduleSpec{Name: "nightly", Schedule: "* * * * *", StorageName: "pvc"}
	// A prior backup two minutes ago means the every-minute cron is overdue.
	prior := backupWithSchedule("c1-nightly-old", "c1", "nightly", now.Add(-2*time.Minute), seaweedv1.BackupPhaseCompleted)
	cli := newSchedulerClient(t, prior)
	s := schedulerWith(cli, now)

	m := &seaweedv1.Seaweed{ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "ns1"}}
	if err := s.reconcileSchedule(context.Background(), m, sched, now); err != nil {
		t.Fatalf("reconcileSchedule: %v", err)
	}

	var list seaweedv1.SeaweedBackupList
	if err := cli.List(context.Background(), &list, client.InNamespace("ns1")); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("expected a new backup to be created, got %d total", len(list.Items))
	}
}

func TestSchedulerDoesNotFireOnFirstObservation(t *testing.T) {
	now := time.Date(2026, 6, 16, 2, 5, 0, 0, time.UTC)
	sched := seaweedv1.BackupScheduleSpec{Name: "nightly", Schedule: "0 2 * * *", StorageName: "pvc"}
	cli := newSchedulerClient(t)
	s := schedulerWith(cli, now)

	m := &seaweedv1.Seaweed{ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "ns1"}}
	if err := s.reconcileSchedule(context.Background(), m, sched, now); err != nil {
		t.Fatalf("reconcileSchedule: %v", err)
	}

	var list seaweedv1.SeaweedBackupList
	if err := cli.List(context.Background(), &list, client.InNamespace("ns1")); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("expected no backup on first observation, got %d", len(list.Items))
	}
}

func TestSchedulerInvalidCron(t *testing.T) {
	cli := newSchedulerClient(t)
	s := schedulerWith(cli, time.Now())
	m := &seaweedv1.Seaweed{ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "ns1"}}
	err := s.reconcileSchedule(context.Background(), m, seaweedv1.BackupScheduleSpec{Name: "x", Schedule: "not-a-cron", StorageName: "pvc"}, time.Now())
	if err == nil {
		t.Fatal("expected error for invalid cron")
	}
}

func TestPruneBackupsRetainsMostRecentCompleted(t *testing.T) {
	base := time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)
	var backups []seaweedv1.SeaweedBackup
	objs := []client.Object{}
	for i := 0; i < 5; i++ {
		b := backupWithSchedule(
			"c1-nightly-"+string(rune('a'+i)), "c1", "nightly",
			base.Add(time.Duration(i)*time.Hour), seaweedv1.BackupPhaseCompleted,
		)
		backups = append(backups, *b)
		objs = append(objs, b)
	}
	cli := newSchedulerClient(t, objs...)
	s := schedulerWith(cli, base)

	if err := s.pruneBackups(context.Background(), backups, 2); err != nil {
		t.Fatalf("pruneBackups: %v", err)
	}

	var list seaweedv1.SeaweedBackupList
	if err := cli.List(context.Background(), &list, client.InNamespace("ns1")); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("expected 2 retained, got %d", len(list.Items))
	}
	// The two newest (hours 3 and 4) must survive.
	survivors := map[string]bool{}
	for _, b := range list.Items {
		survivors[b.Name] = true
	}
	if !survivors["c1-nightly-d"] || !survivors["c1-nightly-e"] {
		t.Errorf("wrong survivors: %v", survivors)
	}
}

func TestPruneBackupsSkipsRunning(t *testing.T) {
	base := time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)
	running := backupWithSchedule("c1-nightly-run", "c1", "nightly", base, seaweedv1.BackupPhaseRunning)
	old1 := backupWithSchedule("c1-nightly-old1", "c1", "nightly", base.Add(time.Hour), seaweedv1.BackupPhaseCompleted)
	old2 := backupWithSchedule("c1-nightly-old2", "c1", "nightly", base.Add(2*time.Hour), seaweedv1.BackupPhaseCompleted)
	cli := newSchedulerClient(t, running, old1, old2)
	s := schedulerWith(cli, base)

	// Keep=1: only the newest completed is retained, but the Running one is
	// never deleted even though it sorts oldest.
	if err := s.pruneBackups(context.Background(), []seaweedv1.SeaweedBackup{*running, *old1, *old2}, 1); err != nil {
		t.Fatalf("pruneBackups: %v", err)
	}
	var list seaweedv1.SeaweedBackupList
	if err := cli.List(context.Background(), &list, client.InNamespace("ns1")); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, b := range list.Items {
		got[b.Name] = true
	}
	if !got["c1-nightly-run"] {
		t.Errorf("running backup must not be pruned: %v", got)
	}
	if !got["c1-nightly-old2"] {
		t.Errorf("newest completed must be retained: %v", got)
	}
}
