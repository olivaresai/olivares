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
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// dr_schedule.go makes the console backup schedule REAL. Before it, the
// PUT /v1/console/dr/schedule handler assigned an in-memory struct nobody read:
// no runner consumed Cron/Enabled/Retain, LastRun/NextRun were never written,
// and the RequireDualControl restore gate silently reset on every restart. Now:
//
//   - the config persists in the estate (the SYSTEM tenant's org settings, the
//     same read-modify-write pattern the audit archive bookkeeping uses) and is
//     reloaded at boot;
//   - RunDueScheduledBackup — driven by the composition root's minute-tick pump
//     (cmd/olivares drschedulepump) — evaluates the cron spec and runs a due
//     backup through the EXACT path the console trigger uses (runBackup), then
//     applies the retain_days retention (core/dr.PlanAge) and records the run.
//
// The cron matcher mirrors the deliberately small 5-field matcher of the
// report schedules (modules/reporting/cron.go): "*", "*/n", a number, or a
// comma list, UTC, minute granularity. It is re-stated here because /core must
// not import /modules (the layering frontier scripts/check-boundary.sh pins).

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
	// DualControlDisarmAt persists a REQUESTED-but-not-yet-effective disarm of the
	// restore gate (drSchedule.DisarmAt). It has to survive a restart for the same
	// reason the gate itself does: the cool-down is what makes disarming a control
	// something other than a second request by the same person, and a restart that
	// forgot it would either skip the wait or lose the request entirely. The gate is
	// COMPUTED from this instant, never driven by a process-local timer.
	DualControlDisarmAt string `json:"dual_control_disarm_effective_at,omitempty"`
	// DualControlDisarmBy persists WHO asked for the disarm (drSchedule.DisarmBy).
	// It has to survive a restart more urgently than the instant does: the instant
	// only delays the weakening, while this is what stops the person who asked for
	// it from being freed by it, so a restart that forgot it would hand them the
	// bypass the delay was built to close.
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

// armedDualControl applies the fail-closed default for the destructive restore
// gate. The setting and this field shipped together, so a record without it is
// legacy/hand-edited/corrupt: fail closed instead of silently decoding Go's
// zero-value false. It lives here, once, because BOTH readers need the same
// answer — the server at boot and the CLI restore, which asks the estate whether
// a two-person control was in force (cmd/olivares/dr_declaration.go).
func (p persistedDRSchedule) armedDualControl() bool {
	if p.RequireDualControl == nil {
		return true
	}
	return *p.RequireDualControl
}

// ReadDualControlRestorePolicy reports whether an estate's console dual-control
// restore gate is ARMED at instant now, read straight from the persisted estate.
//
// It is exported for the CLI restore, which replaces an estate from OUTSIDE the
// console and therefore outside that gate: it has to be able to say, in the record
// it seals, whether the estate it just replaced required two people. Reading it
// through this function rather than re-deriving it keeps the settings key, the
// pending-disarm rule and above all the fail-closed legacy default in one place.
// found is false when the estate has no schedule configured at all.
func ReadDualControlRestorePolicy(ctx context.Context, st store.Store, now time.Time) (armed, found bool, err error) {
	p, found, err := readPersistedDRSchedule(ctx, st)
	if err != nil || !found {
		return false, found, err
	}
	// DisarmBy is NOT optional here, and leaving it out is not a cosmetic omission:
	// dualControlArmed short-circuits to `true` the moment DisarmBy is empty
	// (dr_handler.go:208-210), which is the right fail-closed rule for a schedule that
	// records an instant but not a person. Copying only DisarmAt therefore made this
	// reader answer ARMED for EVERY estate with a disarm on record — including one whose
	// disarm took effect hours ago — while the console, which builds the full schedule,
	// answered the opposite. Two readers of one state, disagreeing.
	//
	// The direction is fail-closed at the gate, so nothing was opened. What it broke is
	// the RECORD: this function's only caller seals its answer into the CLI restore
	// declaration (cmd/olivares/dr_declaration.go), so a disarmed estate was being
	// attested as having required two people. An evidence artifact that says the safe
	// thing when the truth is the other one is worse than no artifact.
	sched := drSchedule{
		RequireDualControl: p.armedDualControl(),
		DisarmAt:           p.DualControlDisarmAt,
		DisarmBy:           p.DualControlDisarmBy,
	}
	return sched.dualControlArmed(now), true, nil
}

// loadDRSchedule seeds the in-memory schedule from the estate at boot. A
// missing system org or an absent key is the zero schedule (nothing configured
// yet); any other failure is an error — starting with a silently-reset
// dual-control gate is exactly the defect this file removes.
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

// RunDueScheduledBackup evaluates the persisted backup schedule as of now and,
// when a cron instant is due, runs ONE backup through the same path the console
// trigger uses (job tracker + runBackup), applies the retain_days retention and
// persists the run outcome. It returns whether a backup ran. Callers (the
// composition root's pump) invoke it once per tick; with no DR surface or no
// enabled schedule it is a cheap no-op.
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
	spec, err := parseDRCron(sched.Cron)
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
	if !spec.dueSince(last, now) {
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

// ---------- 5-field cron matcher (UTC, minute granularity) ----------

// drCronSpec is a parsed 5-field cron expression (minute hour day-of-month
// month day-of-week).
type drCronSpec struct {
	minute, hour, dom, month, dow drCronField
}

type drCronField struct {
	any  bool
	step int   // 0 = no step; otherwise "*/step"
	set  []int // explicit values (empty when any/step)
}

type drCronBound struct {
	name     string
	min, max int
}

var drCronBounds = [5]drCronBound{
	{"minute", 0, 59},
	{"hour", 0, 23},
	{"day-of-month", 1, 31},
	{"month", 1, 12},
	{"day-of-week", 0, 6}, // 0 = Sunday
}

// parseDRCron parses and validates a 5-field cron expression. Supported syntax
// per field: "*", "*/n", a number, or a comma list of numbers — a backup
// schedule is "daily at 02:00", not a process supervisor.
func parseDRCron(spec string) (drCronSpec, error) {
	fields := strings.Fields(strings.TrimSpace(spec))
	if len(fields) != 5 {
		return drCronSpec{}, fmt.Errorf("want 5 fields (minute hour day-of-month month day-of-week), got %d", len(fields))
	}
	var parsed [5]drCronField
	for i, f := range fields {
		cf, err := parseDRCronField(f, drCronBounds[i])
		if err != nil {
			return drCronSpec{}, err
		}
		parsed[i] = cf
	}
	return drCronSpec{minute: parsed[0], hour: parsed[1], dom: parsed[2], month: parsed[3], dow: parsed[4]}, nil
}

func parseDRCronField(f string, b drCronBound) (drCronField, error) {
	if f == "*" {
		return drCronField{any: true}, nil
	}
	if rest, ok := strings.CutPrefix(f, "*/"); ok {
		n, err := strconv.Atoi(rest)
		if err != nil || n <= 0 || n > b.max {
			return drCronField{}, fmt.Errorf("%s: invalid step %q", b.name, f)
		}
		return drCronField{step: n}, nil
	}
	parts := strings.Split(f, ",")
	set := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < b.min || n > b.max {
			return drCronField{}, fmt.Errorf("%s: value %q out of range [%d, %d]", b.name, p, b.min, b.max)
		}
		set = append(set, n)
	}
	return drCronField{set: set}, nil
}

func (f drCronField) matchesValue(v int) bool {
	if f.any {
		return true
	}
	if f.step > 0 {
		return v%f.step == 0
	}
	for _, n := range f.set {
		if n == v {
			return true
		}
	}
	return false
}

// matches reports whether the instant (truncated to the minute, UTC) satisfies
// the spec. Day-of-month and day-of-week combine with OR when BOTH are
// restricted (the traditional cron rule); otherwise the restricted one applies.
func (sp drCronSpec) matches(t time.Time) bool {
	t = t.UTC()
	if !sp.minute.matchesValue(t.Minute()) || !sp.hour.matchesValue(t.Hour()) || !sp.month.matchesValue(int(t.Month())) {
		return false
	}
	domOK := sp.dom.matchesValue(t.Day())
	dowOK := sp.dow.matchesValue(int(t.Weekday()))
	switch {
	case sp.dom.any && sp.dow.any:
		return true
	case sp.dom.any:
		return dowOK
	case sp.dow.any:
		return domOK
	default:
		return domOK || dowOK
	}
}

// dueSince reports whether the spec has a matching instant AFTER last and at or
// before now. A zero last means "never ran": due iff a matching instant exists
// in the 24h lookback window (a fresh schedule must not replay history). The
// scan is minute-granular and bounded to 31 days — beyond that a due schedule
// fires on the next matching instant instead of replaying an outage backlog.
func (sp drCronSpec) dueSince(last, now time.Time) bool {
	now = now.UTC().Truncate(time.Minute)
	start := last.UTC().Truncate(time.Minute).Add(time.Minute)
	if last.IsZero() {
		start = now.Add(-24 * time.Hour)
	}
	if floor := now.Add(-31 * 24 * time.Hour); start.Before(floor) {
		start = floor
	}
	for t := start; !t.After(now); t = t.Add(time.Minute) {
		if sp.matches(t) {
			return true
		}
	}
	return false
}

// nextAfter returns the first matching instant strictly after from, scanning at
// minute granularity up to 366 days out (ok=false past that — a spec that never
// fires within a year has no honest next_run to show).
func (sp drCronSpec) nextAfter(from time.Time) (time.Time, bool) {
	t := from.UTC().Truncate(time.Minute).Add(time.Minute)
	limit := t.Add(366 * 24 * time.Hour)
	for ; t.Before(limit); t = t.Add(time.Minute) {
		if sp.matches(t) {
			return t, true
		}
	}
	return time.Time{}, false
}
