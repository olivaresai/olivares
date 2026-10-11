// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/cron"
	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Backup schedules and restore policy persist in SYSTEM org settings.
// The minute-tick runner uses core/cron and the console's backup path.

// drScheduleSettingsKey holds the persisted schedule JSON in the SYSTEM
// tenant's org settings.
const drScheduleSettingsKey = "dr.schedule"

// drScheduleActor is the audit actor recorded for unattended scheduled runs.
const drScheduleActor = "backup-scheduler"

// persistedDRSchedule is the stored subset of drSchedule: config + last-run
// bookkeeping. NextRun is never stored (derived from the cron on read).
type persistedDRSchedule struct {
	Enabled            bool   `json:"enabled"`
	Cron               string `json:"cron"`
	RetainDays         int    `json:"retain_days"`
	RequireDualControl *bool  `json:"require_dual_control_restore"`
	// Disarming takes effect at the persisted instant, never a process-local timer.
	DualControlDisarmAt string `json:"dual_control_disarm_effective_at,omitempty"`
	// The disarmer remains subject to the gate after the delay and across restarts.
	DualControlDisarmBy string `json:"dual_control_disarm_requested_by,omitempty"`
	LastRun             string `json:"last_run,omitempty"`
	LastRunStatus       string `json:"last_run_status,omitempty"`
	LastRunError        string `json:"last_run_error,omitempty"`
}

// readPersistedDRSchedule reads the stored schedule out of the estate. found is
// false when nothing has been configured yet (no system tenant, or no key), which
// is the zero schedule rather than an error; a present-but-unreadable record IS an
// error, because a corrupt record must never decode to an open gate.
func readPersistedDRSchedule(ctx context.Context, st store.Store) (persistedDRSchedule, bool, error) {
	var (
		raw   string
		found bool
	)
	err := st.View(ctx, model.SystemTenantID, func(sc store.Scope) error {
		org, err := sc.Org(ctx)
		if err != nil {
			return err
		}
		v, ok := org.Settings[drScheduleSettingsKey]
		if !ok {
			return nil
		}
		found = true
		var typeOK bool
		raw, typeOK = v.(string)
		if !typeOK {
			return fmt.Errorf("corrupt %s setting: want JSON string, got %T", drScheduleSettingsKey, v)
		}
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return persistedDRSchedule{}, false, nil // system tenant not provisioned yet
	}
	if err != nil {
		return persistedDRSchedule{}, false, err
	}
	if !found {
		return persistedDRSchedule{}, false, nil
	}
	if strings.TrimSpace(raw) == "" {
		return persistedDRSchedule{}, false, fmt.Errorf("corrupt %s setting: empty JSON string", drScheduleSettingsKey)
	}
	var p persistedDRSchedule
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return persistedDRSchedule{}, false, fmt.Errorf("corrupt %s setting: %w", drScheduleSettingsKey, err)
	}
	return p, true, nil
}

// armedDualControl defaults a missing restore-policy field to armed.
// Console and CLI readers share this fail-closed legacy default.
func (p persistedDRSchedule) armedDualControl() bool {
	if p.RequireDualControl == nil {
		return true
	}
	return *p.RequireDualControl
}

// ReadDualControlRestorePolicy reads the effective console restore gate at now
// for the CLI restore declaration. found is false without a stored schedule.
func ReadDualControlRestorePolicy(ctx context.Context, st store.Store, now time.Time) (armed, found bool, err error) {
	p, found, err := readPersistedDRSchedule(ctx, st)
	if err != nil || !found {
		return false, found, err
	}
	// A disarm requires both its instant and its requester; an incomplete record
	// leaves the gate armed, just as the console reader does.
	sched := drSchedule{
		RequireDualControl: p.armedDualControl(),
		DisarmAt:           p.DualControlDisarmAt,
		DisarmBy:           p.DualControlDisarmBy,
	}
	return sched.dualControlArmed(now), true, nil
}

// loadDRSchedule reloads the persisted schedule. Missing state is unconfigured;
// unreadable state is an error and must not reset the restore gate.
func (s *Server) loadDRSchedule(ctx context.Context) error {
	if s.drSvc == nil {
		return nil
	}
	p, found, err := readPersistedDRSchedule(ctx, s.st)
	if err != nil {
		return err
	}
	if !found {
		s.drSvc.setSchedule(func(d *drSchedule) { *d = drSchedule{} })
		return nil
	}
	requireDualControl := p.armedDualControl()
	s.drSvc.setSchedule(func(d *drSchedule) {
		d.Enabled = p.Enabled
		d.Cron = p.Cron
		d.Retain = p.RetainDays
		d.RequireDualControl = requireDualControl
		d.DisarmAt = p.DualControlDisarmAt
		d.DisarmBy = p.DualControlDisarmBy
		d.LastRun = p.LastRun
		d.LastRunStatus = p.LastRunStatus
		d.LastRunError = p.LastRunError
	})
	return nil
}

// saveDRSchedule persists a schedule state into the SYSTEM tenant's org
// settings — a read-modify-write of the FULL settings map inside one Mutate tx
// (SetOrgSettings REPLACES the map, so sibling keys must ride along).
func (s *Server) saveDRSchedule(ctx context.Context, sched drSchedule) error {
	requireDualControl := sched.RequireDualControl
	p := persistedDRSchedule{
		Enabled:             sched.Enabled,
		Cron:                sched.Cron,
		RetainDays:          sched.Retain,
		RequireDualControl:  &requireDualControl,
		DualControlDisarmAt: sched.DisarmAt,
		DualControlDisarmBy: sched.DisarmBy,
		LastRun:             sched.LastRun,
		LastRunStatus:       sched.LastRunStatus,
		LastRunError:        sched.LastRunError,
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.st.Mutate(ctx, model.SystemTenantID, func(sc store.Scope) error {
		org, err := sc.Org(ctx)
		if err != nil {
			return err
		}
		settings := org.Settings
		if settings == nil {
			settings = map[string]any{}
		}
		settings[drScheduleSettingsKey] = string(raw)
		_, err = sc.SetOrgSettings(ctx, settings)
		return err
	})
}

// ScheduledBackupConfigured reloads the persisted state and reports whether an
// enabled schedule has a cron. It neither evaluates nor claims a due instant.
func (s *Server) ScheduledBackupConfigured(ctx context.Context) (bool, error) {
	if s.drSvc == nil {
		return false, nil
	}
	s.drSvc.scheduleOpMu.Lock()
	defer s.drSvc.scheduleOpMu.Unlock()
	if err := s.loadDRSchedule(ctx); err != nil {
		return false, err
	}
	sched := s.drSvc.scheduleSnapshot()
	return sched.Enabled && strings.TrimSpace(sched.Cron) != "", nil
}

// RunDueScheduledBackup runs a due backup through the console backup path,
// applies retention and records the outcome. Disabled or absent DR is a no-op.
func (s *Server) RunDueScheduledBackup(ctx context.Context, now time.Time) (bool, error) {
	if s.drSvc == nil {
		return false, nil
	}
	s.drSvc.scheduleOpMu.Lock()
	defer s.drSvc.scheduleOpMu.Unlock()

	// A standby may have been running since before the current leader changed the
	// schedule. Refresh from the shared estate on every leader-gated tick so a
	// promotion never evaluates boot-time state or a stale last_run.
	if err := s.loadDRSchedule(ctx); err != nil {
		return false, fmt.Errorf("dr schedule: reload persisted state: %w", err)
	}
	now = now.UTC()
	sched := s.drSvc.scheduleSnapshot()
	if !sched.Enabled || strings.TrimSpace(sched.Cron) == "" {
		return false, nil
	}
	spec, err := cron.Parse(sched.Cron)
	if err != nil {
		// PUT validates the spec, so this only happens on a hand-edited estate:
		// loud, and no silent "schedule on, backups off".
		return false, fmt.Errorf("dr schedule: stored cron %q is invalid: %w", sched.Cron, err)
	}
	var last time.Time
	if sched.LastRun != "" {
		if t, perr := time.Parse(time.RFC3339, sched.LastRun); perr == nil {
			last = t
		}
	}
	if !spec.DueSince(last, now) {
		return false, nil
	}

	// Claim the due instant BEFORE touching the filesystem. If leadership moves
	// during a long backup, the promoted node reloads this last_run and will not
	// fire the same cron instant again. A claim that cannot be persisted fails
	// closed: at-most-once execution is more important than an unrecorded backup.
	claimed := sched
	claimed.LastRun = now.Format(time.RFC3339)
	claimed.LastRunStatus = drJobRunning
	claimed.LastRunError = ""
	if err := s.saveDRSchedule(ctx, claimed); err != nil {
		return false, fmt.Errorf("dr schedule: claim due instant: %w", err)
	}
	s.drSvc.setSchedule(func(d *drSchedule) { *d = claimed })

	passphrase, err := s.drSchedulePassphrase()
	if err != nil {
		s.recordScheduledRun(ctx, now, drJobFailed, err.Error())
		return false, fmt.Errorf("dr schedule: %w", err)
	}
	if err := s.drSvc.ensureBackupDir(); err != nil {
		s.recordScheduledRun(ctx, now, drJobFailed, err.Error())
		return false, fmt.Errorf("dr schedule: backup dir: %w", err)
	}

	job := s.drSvc.jobs.create(drJobBackup, "scheduled backup")
	s.runBackup(ctx, job.ID, passphrase, "scheduled backup", drScheduleActor)
	done, ok := s.drSvc.jobs.get(job.ID)
	if !ok || done.Status != drJobCompleted {
		msg := "job disappeared from tracker"
		if ok && done.Error != "" {
			msg = done.Error
		}
		s.recordScheduledRun(ctx, now, drJobFailed, msg)
		return false, fmt.Errorf("dr schedule: backup failed: %s", msg)
	}

	s.applyScheduleRetention(sched.Retain, done.BundleID, now)
	s.recordScheduledRun(ctx, now, drJobCompleted, "")
	s.log.Info("dr: scheduled backup completed", "job", job.ID, "bundle", done.BundleID, "retain_days", sched.Retain)
	return true, nil
}

// drSchedulePassphrase reads the unattended-backup passphrase from the
// configured file (the same $OLIVARES_DR_PASSPHRASE_FILE the CLI DR commands
// use). No file configured is an error, not a silent skip.
func (s *Server) drSchedulePassphrase() (string, error) {
	path := strings.TrimSpace(s.drSvc.cfg.PassphraseFile)
	if path == "" {
		return "", errors.New("scheduled backups need a passphrase file (set OLIVARES_DR_PASSPHRASE_FILE); none is configured")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read DR passphrase file: %w", err)
	}
	pass := strings.TrimSpace(string(raw))
	if pass == "" {
		return "", fmt.Errorf("DR passphrase file %s is empty", path)
	}
	if msg := drPassphraseFloorError(pass); msg != "" {
		return "", errors.New(msg)
	}
	return pass, nil
}

// recordScheduledRun stores the outcome of a scheduled run (memory + estate).
// A persistence failure is logged, never escalated: the run itself already
// succeeded or failed on its own merits.
func (s *Server) recordScheduledRun(ctx context.Context, now time.Time, status, errMsg string) {
	s.drSvc.setSchedule(func(d *drSchedule) {
		d.LastRun = now.UTC().Format(time.RFC3339)
		d.LastRunStatus = status
		d.LastRunError = errMsg
	})
	if err := s.saveDRSchedule(ctx, s.drSvc.scheduleSnapshot()); err != nil {
		s.log.Warn("dr: could not persist scheduled-run bookkeeping", "err", err)
	}
	if errMsg != "" {
		s.log.Error("dr: scheduled backup failed", "err", errMsg)
	}
}

// applyScheduleRetention prunes bundles older than retainDays in the backup
// directory (core/dr.PlanAge — the flat age policy the console's retain_days
// field promises), never deleting the bundle just written. Best-effort: a
// prune failure never fails the backup that produced a valid bundle.
func (s *Server) applyScheduleRetention(retainDays int, keepName string, now time.Time) {
	if retainDays <= 0 {
		return
	}
	dir := s.drSvc.cfg.BackupDir
	matches, err := filepath.Glob(filepath.Join(dir, "*.drbundle"))
	if err != nil {
		s.log.Warn("dr: retention prune skipped", "err", err)
		return
	}
	metas := make([]dr.BundleMeta, 0, len(matches))
	for _, m := range matches {
		manifest := s.inspectBundle(m)
		if manifest == nil {
			s.log.Warn("dr: retention kept unreadable bundle", "bundle", filepath.Base(m))
			continue
		}
		createdAt, err := time.Parse(time.RFC3339, manifest.CreatedAt)
		if err != nil {
			s.log.Warn("dr: retention kept bundle with invalid created_at", "bundle", filepath.Base(m), "created_at", manifest.CreatedAt, "err", err)
			continue
		}
		metas = append(metas, dr.BundleMeta{Name: filepath.Base(m), CreatedAt: createdAt})
	}
	plan := dr.PlanAge(metas, retainDays, now)
	for _, b := range plan.Delete {
		if b.Name == keepName {
			continue
		}
		if err := os.Remove(filepath.Join(dir, b.Name)); err != nil {
			s.log.Warn("dr: could not prune bundle", "bundle", b.Name, "err", err)
			continue
		}
		s.log.Info("dr: pruned bundle past retention", "bundle", b.Name, "retain_days", retainDays)
	}
}

// nextDRCronAfter scans for the next minute within 366 days; ok=false omits next_run.
func nextDRCronAfter(spec cron.Spec, from time.Time) (time.Time, bool) {
	t := from.UTC().Truncate(time.Minute).Add(time.Minute)
	limit := t.Add(366 * 24 * time.Hour)
	for ; t.Before(limit); t = t.Add(time.Minute) {
		if spec.Matches(t) {
			return t, true
		}
	}
	return time.Time{}, false
}
