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
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/robfig/cron/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// backupSchedulerInterval is how often the scheduler evaluates cron schedules.
// A schedule fires at most once per tick, so this also bounds how late a
// backup can start relative to its cron time.
const backupSchedulerInterval = 30 * time.Second

// BackupScheduler is a leader-elected Runnable that evaluates every Seaweed
// cluster's spec.backup.schedule and creates SeaweedBackup CRs when due,
// then prunes completed backups beyond each schedule's Keep. The due time is
// derived from the most recent existing SeaweedBackup for the schedule; when a
// schedule has no history yet, the scheduler anchors on the first time it
// observed the schedule (held in memory) so the first run fires at the next
// cron time rather than immediately. Anchors are best-effort: an operator
// restart re-anchors, which at worst delays one run by a single cron period.
type BackupScheduler struct {
	client.Client
	Log      logr.Logger
	Interval time.Duration
	// now is overridable in tests.
	now func() time.Time

	// anchors records, per "ns/cluster/schedule", the first time a schedule
	// with no backup history was observed, so the next-cron-time comparison
	// is stable across ticks instead of re-anchoring to "now" every tick.
	mu      sync.Mutex
	anchors map[string]time.Time
}

// anchorFor returns the stored first-observation time for key, recording now
// as the anchor the first time the key is seen.
func (s *BackupScheduler) anchorFor(key string, now time.Time) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.anchors == nil {
		s.anchors = map[string]time.Time{}
	}
	if t, ok := s.anchors[key]; ok {
		return t
	}
	s.anchors[key] = now
	return now
}

// clearAnchor drops a schedule's anchor once it has real backup history.
func (s *BackupScheduler) clearAnchor(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.anchors, key)
}

// SetupWithManager registers the scheduler with the manager.
func (s *BackupScheduler) SetupWithManager(mgr ctrl.Manager) error {
	if s.Interval == 0 {
		s.Interval = backupSchedulerInterval
	}
	if s.now == nil {
		s.now = time.Now
	}
	return mgr.Add(s)
}

// NeedLeaderElection ensures only the elected leader runs the scheduler, so
// HA deployments do not create duplicate backups.
func (s *BackupScheduler) NeedLeaderElection() bool { return true }

// Start runs the scheduler loop until the context is cancelled.
func (s *BackupScheduler) Start(ctx context.Context) error {
	if s.now == nil {
		s.now = time.Now
	}
	ticker := time.NewTicker(s.Interval)
	defer ticker.Stop()
	s.Log.Info("backup scheduler started", "interval", s.Interval)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.tick(ctx); err != nil {
				s.Log.Error(err, "backup scheduler tick failed")
			}
		}
	}
}

// tick evaluates every cluster's schedules once.
func (s *BackupScheduler) tick(ctx context.Context) error {
	var clusters seaweedv1.SeaweedList
	if err := s.List(ctx, &clusters); err != nil {
		return err
	}
	now := s.now()
	for i := range clusters.Items {
		m := &clusters.Items[i]
		if m.Spec.Backup == nil {
			continue
		}
		for _, sched := range m.Spec.Backup.Schedule {
			if sched.Suspend {
				continue
			}
			if err := s.reconcileSchedule(ctx, m, sched, now); err != nil {
				s.Log.Error(err, "reconcile schedule", "cluster", m.Name, "schedule", sched.Name)
			}
		}
	}
	return nil
}

// reconcileSchedule fires a backup when the cron is due (relative to the most
// recent existing backup) and enforces retention.
func (s *BackupScheduler) reconcileSchedule(ctx context.Context, m *seaweedv1.Seaweed, sched seaweedv1.BackupScheduleSpec, now time.Time) error {
	cronSched, err := cron.ParseStandard(sched.Schedule)
	if err != nil {
		return fmt.Errorf("invalid cron %q: %w", sched.Schedule, err)
	}

	existing, err := s.listScheduleBackups(ctx, m, sched.Name)
	if err != nil {
		return err
	}

	key := m.Namespace + "/" + m.Name + "/" + sched.Name
	lastRun := mostRecentCreation(existing)
	if lastRun.IsZero() {
		// No history yet: anchor on the first observation (persisted across
		// ticks) so the first backup fires at the next cron time, not
		// immediately and not never.
		lastRun = s.anchorFor(key, now)
	} else {
		s.clearAnchor(key)
	}

	if !cronSched.Next(lastRun).After(now) {
		if err := s.createScheduledBackup(ctx, m, sched, now); err != nil {
			return err
		}
		// The new backup is now the schedule's history; drop the anchor.
		s.clearAnchor(key)
	}

	return s.pruneBackups(ctx, existing, sched.Keep)
}

// createScheduledBackup creates one SeaweedBackup for a fired schedule.
func (s *BackupScheduler) createScheduledBackup(ctx context.Context, m *seaweedv1.Seaweed, sched seaweedv1.BackupScheduleSpec, fireTime time.Time) error {
	// Keep the generated name within the 63-char object-name limit even when
	// the cluster + schedule names are long; the timestamp suffix is preserved
	// so successive fires stay distinct and ordered.
	name := boundedName(fmt.Sprintf("%s-%s", m.Name, sched.Name), "-"+fireTime.UTC().Format("20060102150405"))
	backup := &seaweedv1.SeaweedBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: m.Namespace,
			Labels: map[string]string{
				seaweedv1.LabelBackupCluster:  m.Name,
				seaweedv1.LabelBackupSchedule: sched.Name,
			},
		},
		Spec: seaweedv1.SeaweedBackupSpec{
			ClusterName: m.Name,
			StorageName: sched.StorageName,
			FilerPath:   sched.FilerPath,
		},
	}
	if err := s.Create(ctx, backup); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return err
	}
	s.Log.Info("created scheduled backup", "backup", name, "cluster", m.Name, "schedule", sched.Name)
	return nil
}

// pruneBackups keeps the Keep most-recent terminal (Completed/Failed) backups
// and deletes older terminal ones. Keep == 0 retains everything. Non-terminal
// backups are never deleted and — importantly — never counted toward Keep, so a
// burst of still-Running snapshots can't push completed ones out of retention.
func (s *BackupScheduler) pruneBackups(ctx context.Context, backups []seaweedv1.SeaweedBackup, keep int32) error {
	if keep <= 0 {
		return nil
	}
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].CreationTimestamp.After(backups[j].CreationTimestamp.Time)
	})
	terminal := int32(0)
	for i := range backups {
		b := &backups[i]
		if b.Status.Phase != seaweedv1.BackupPhaseCompleted && b.Status.Phase != seaweedv1.BackupPhaseFailed {
			continue
		}
		terminal++
		if terminal <= keep {
			continue
		}
		if err := s.Delete(ctx, b); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		s.Log.Info("pruned backup beyond retention", "backup", b.Name, "keep", keep)
	}
	return nil
}

// listScheduleBackups returns the SeaweedBackups created by one schedule.
func (s *BackupScheduler) listScheduleBackups(ctx context.Context, m *seaweedv1.Seaweed, schedule string) ([]seaweedv1.SeaweedBackup, error) {
	var list seaweedv1.SeaweedBackupList
	if err := s.List(ctx, &list, client.InNamespace(m.Namespace), client.MatchingLabels{
		seaweedv1.LabelBackupCluster:  m.Name,
		seaweedv1.LabelBackupSchedule: schedule,
	}); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// mostRecentCreation returns the newest CreationTimestamp among backups, or the
// zero time when the list is empty.
func mostRecentCreation(backups []seaweedv1.SeaweedBackup) time.Time {
	var latest time.Time
	for i := range backups {
		if t := backups[i].CreationTimestamp.Time; t.After(latest) {
			latest = t
		}
	}
	return latest
}
