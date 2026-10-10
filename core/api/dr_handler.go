// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"modernc.org/sqlite"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/cron"
	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/envconfig"
	"github.com/olivaresai/olivares/core/store"
)

// DRBackupSnapshot retains the native payload check until the bundle is written.
type DRBackupSnapshot struct {
	Store    dr.StoreSnapshot
	Validate func() error
}

// DRConfig enables the disaster-recovery console surface when set in Options.
// PostgreSQL snapshots are supplied by the native command composition root.
type DRConfig struct {
	DataDir          string
	EngineKind       string
	BackupDir        string
	PostgresSnapshot func(context.Context, string) (DRBackupSnapshot, error)
	// PassphraseFile supplies scheduled backups with their encryption passphrase.
	// An empty path makes the runner refuse the backup.
	PassphraseFile string
	// SealKeys supplies the installation custody required to recover stored content,
	// and the sealer probes a restore checks its keys against. A failure aborts the
	// backup; the signing-only fallback is used only when nil.
	SealKeys func(*dr.KeyCipher) (map[string][]byte, []dr.KeyRef, []dr.SealerProbe, error)
	// Getenv reads the effective sealer overrides used by the engine at boot.
	// Nil uses the process environment for standalone API compositions.
	Getenv func(string) string
	// RegisterSchema is the register func the live store was opened with. Backup and restore
	// scratch verification opens the snapshot with it: the guard edition and epoch derive
	// from the registered tables, so a registry holding only the core tables computes a
	// different edition from the one the snapshot records and refuses every real install.
	RegisterSchema func(store.ExtensionRegistry) error
	// QuiesceStore stops and drains the boot-owned SQLite pool. It must leave
	// serving unavailable until restart, including on failure.
	QuiesceStore func(context.Context) error
}

// drService wraps the DR configuration and in-memory job tracker.
type drService struct {
	cfg  DRConfig
	jobs *drJobTracker
	// Serving stays unavailable once promotion starts; restart reloads custody.
	restartRequired atomic.Bool
	maintenanceJob  string // protected by smu
	// Schedule and restore policy persist in estate settings and reload at boot.
	// smu protects snapshots; scheduleOpMu serializes reloads and updates.
	smu          sync.Mutex
	scheduleOpMu sync.Mutex
	schedule     *drSchedule
	// Pending approvals are process-local; restart requires a new restore request.
	pmu     sync.Mutex
	pending map[string]*pendingRestore
}

// drSchedule holds the current backup schedule configuration and DR policy.
// NextRun is DERIVED from the cron spec on read; the rest round-trips through
// the persisted estate settings (dr_schedule.go).
type drSchedule struct {
	Enabled bool   `json:"enabled"`
	Cron    string `json:"cron"`
	Retain  int    `json:"retain_days"`
	LastRun string `json:"last_run,omitempty"`
	NextRun string `json:"next_run,omitempty"`
	// LastRunStatus/LastRunError record the outcome of the most recent SCHEDULED
	// run ("completed"/"failed"), so the console shows a failing schedule honestly
	// instead of a silent gap in the backup directory.
	LastRunStatus string `json:"last_run_status,omitempty"`
	LastRunError  string `json:"last_run_error,omitempty"`
	// RequireDualControl reports the effective console restore gate. When armed,
	// a distinct administrator account must approve; two accounts need not be
	// two humans. CLI restore uses a declared-operator record, not this gate,
	// so estates requiring approval for every restore must also control host access.
	RequireDualControl bool `json:"require_dual_control_restore"`
	// DisarmAt is the persisted RFC3339 instant when a requested disarm takes effect.
	// Arming is immediate; disarming waits, remains visible and can be countermanded.
	// The server computes the instant; restart must not skip the delay.
	DisarmAt string `json:"dual_control_disarm_effective_at,omitempty"`
	// DisarmBy identifies the stable account requesting a disarm, not its credential.
	// After the delay, the gate still holds against that account and anonymous tokens.
	// It survives a spent DisarmAt and clears only on re-arming; the next disarm
	// records its requester again. Other administrators can restore after the delay.
	DisarmBy string `json:"dual_control_disarm_requested_by,omitempty"`
}

// drDualControlDisarmDelay is the fixed cool-down before a restore-gate disarm.
// Making it configurable would create another way to weaken the same control.
const drDualControlDisarmDelay = time.Hour

// disarmInstant parses a pending disarm. An absent or unreadable instant leaves
// the gate armed while allowing a fresh request to replace the corrupt state.
func (d drSchedule) disarmInstant() (time.Time, bool) {
	if d.DisarmAt == "" {
		return time.Time{}, false
	}
	when, err := time.Parse(time.RFC3339, d.DisarmAt)
	if err != nil {
		return time.Time{}, false
	}
	return when, true
}

// dualControlArmed reports the estate-wide gate at now. An armed gate stays
// armed until a readable disarm with a recorded requester takes effect.
func (d drSchedule) dualControlArmed(now time.Time) bool {
	if !d.RequireDualControl {
		return false
	}
	when, ok := d.disarmInstant()
	if !ok {
		return true
	}
	if d.DisarmBy == "" {
		return true
	}
	return now.Before(when)
}

// disarmPending reports whether a READABLE disarm is recorded and has not yet
// taken effect.
func (d drSchedule) disarmPending(now time.Time) bool {
	when, ok := d.disarmInstant()
	return d.RequireDualControl && ok && now.Before(when)
}

// dualControlHoldsFor includes the estate-wide gate and the disarmer's account.
// An elapsed disarm frees other accounts, never its requester or an anonymous
// credential that cannot be distinguished from the requester. Credentials of
// the same account count as one party.
func (d drSchedule) dualControlHoldsFor(account string, now time.Time) bool {
	if d.dualControlArmed(now) {
		return true
	}
	if d.DisarmBy == "" {
		return false
	}
	return account == "" || account == d.DisarmBy
}

// scheduleSnapshot returns a copy of the current schedule under the lock.
func (ds *drService) scheduleSnapshot() drSchedule {
	ds.smu.Lock()
	defer ds.smu.Unlock()
	return *ds.schedule
}

// setSchedule mutates the schedule under the lock.
func (ds *drService) setSchedule(fn func(*drSchedule)) {
	ds.smu.Lock()
	defer ds.smu.Unlock()
	fn(ds.schedule)
}

// requireDualControlRestore reads the EFFECTIVE dual-control restore gate under
// the lock. It takes the instant because a pending disarm makes the answer
// time-dependent: the stored flag alone is no longer the gate (drSchedule.DisarmAt).
func (ds *drService) requireDualControlRestore(now time.Time) bool {
	ds.smu.Lock()
	defer ds.smu.Unlock()
	return ds.schedule.dualControlArmed(now)
}

// requireDualControlRestoreFor is requireDualControlRestore asked on behalf of a
// ACCOUNT. It is what the restore endpoint consults, because an elapsed disarm
// frees the estate without ever freeing the account that requested it
// (drSchedule.DisarmBy).
func (ds *drService) requireDualControlRestoreFor(account string, now time.Time) bool {
	ds.smu.Lock()
	defer ds.smu.Unlock()
	return ds.schedule.dualControlHoldsFor(account, now)
}

// pendingRestore records both the requester's stable account, used for the
// distinct-approver check, and its credential actor, retained in the audit trail.
type pendingRestore struct {
	RequestID     string `json:"request_id"`
	UploadID      string `json:"upload_id"`
	Initiator     string `json:"initiator"`
	InitiatorUser string `json:"initiator_user,omitempty"`
	CreatedAt     string `json:"created_at"`
}

func newDRService(cfg DRConfig) *drService {
	if cfg.BackupDir == "" {
		cfg.BackupDir = filepath.Join(cfg.DataDir, "backups")
	}
	return &drService{
		cfg:      cfg,
		jobs:     newDRJobTracker(),
		schedule: &drSchedule{},
		pending:  make(map[string]*pendingRestore),
	}
}

// dual-control outcomes for a restore approval.
var (
	errNoPendingRestore = errors.New("no pending restore for this request — it may have expired or already run")
	// Accounts, rather than credentials or humans, are the compared parties.
	errSelfApprove = errors.New("dual-control: a restore must be requested and approved by two DIFFERENT user accounts; a second credential of the same account is the same requester")
	// An anonymous system token cannot be counted as a distinct account.
	errNoStableIdentity = errors.New("dual-control: a restore needs a stable user identity on both sides; a system token cannot request or approve one")
)

// registerPending stores the requester's stable account and credential actor.
func (ds *drService) registerPending(uploadID string, initiator auth.PersonRef, now string) *pendingRestore {
	pr := &pendingRestore{
		RequestID:     "drr_" + generateJobID()[4:],
		UploadID:      uploadID,
		Initiator:     initiator.Actor,
		InitiatorUser: initiator.User,
		CreatedAt:     now,
	}
	ds.pmu.Lock()
	ds.pending[pr.RequestID] = pr
	ds.pmu.Unlock()
	return pr
}

// approvePending requires a matching restore intent and a distinct stable
// approver account. Success consumes the intent; refusal leaves it pending.
// This establishes two accounts, not two humans.
func (ds *drService) approvePending(requestID, uploadID string, approver auth.PersonRef) (*pendingRestore, error) {
	ds.pmu.Lock()
	defer ds.pmu.Unlock()
	pr, ok := ds.pending[requestID]
	if !ok || pr.UploadID != uploadID {
		return nil, errNoPendingRestore
	}
	initiator := auth.PersonRef{User: pr.InitiatorUser, Actor: pr.Initiator}
	// Refuse unattributable parties rather than treating them as different accounts.
	switch ok, verdict := auth.TwoDistinctPeople(initiator, approver, auth.RefuseWhenUndetermined); {
	case ok:
		// two distinct accounts — consume the request
	case verdict == auth.PersonSame:
		return nil, errSelfApprove
	default:
		return nil, errNoStableIdentity
	}
	delete(ds.pending, requestID)
	return pr, nil
}

func (ds *drService) ensureBackupDir() error {
	return os.MkdirAll(ds.cfg.BackupDir, 0o700)
}

// minDRPassphraseLen is the creation floor in runes for encrypted bundles.
// Restore accepts legacy shorter passphrases so existing bundles remain recoverable.
const minDRPassphraseLen = 12

// drPassphraseFloorError returns a client-facing message when the passphrase is
// under the floor, or "" when it passes. Empty stays the caller's own "required"
// error so the two failure modes read distinctly.
func drPassphraseFloorError(passphrase string) string {
	if passphrase != "" && utf8.RuneCountInString(passphrase) < minDRPassphraseLen {
		return fmt.Sprintf("passphrase must be at least %d characters", minDRPassphraseLen)
	}
	return ""
}

// ---------- request/response types ----------

type triggerBackupRequest struct {
	Notes      string `json:"notes"`
	Passphrase string `json:"passphrase"`
}

type triggerBackupResponse struct {
	JobID string `json:"job_id"`
}

type backupListItem struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	SizeBytes   int64  `json:"size_bytes"`
	CreatedAt   string `json:"created_at"`
	EngineKind  string `json:"engine"`
	Version     string `json:"engine_version,omitempty"`
	TenantCount int    `json:"tenant_count"`
	Notes       string `json:"notes,omitempty"`
}

type restoreUploadResponse struct {
	UploadID string       `json:"upload_id"`
	Manifest *dr.Manifest `json:"manifest"`
	Filename string       `json:"filename"`
}

type restoreApplyRequest struct {
	Passphrase string `json:"passphrase"`
}

// restoreApplyResponse is the apply outcome: a job id (single-actor path) OR an
// awaiting-approval request id (dual-control path).
type restoreApplyResponse struct {
	JobID            string `json:"job_id,omitempty"`
	AwaitingApproval bool   `json:"awaiting_approval,omitempty"`
	RequestID        string `json:"request_id,omitempty"`
	Initiator        string `json:"initiator,omitempty"`
}

// restoreApproveRequest is the second approver's confirmation under dual-control.
type restoreApproveRequest struct {
	RequestID  string `json:"request_id"`
	Passphrase string `json:"passphrase"`
}

// drScheduleRequest preserves the gate when RequireDualControl is omitted.
// The server owns the disarm instant and run bookkeeping.
type drScheduleRequest struct {
	Enabled            bool   `json:"enabled"`
	Cron               string `json:"cron"`
	Retain             int    `json:"retain_days"`
	RequireDualControl *bool  `json:"require_dual_control_restore"`

	// Accept read-modify-write round trips, but ignore server-owned fields.
	// A client must not choose an earlier disarm instant or another requester.
	IgnoredDisarmAt      string `json:"dual_control_disarm_effective_at"`
	IgnoredDisarmBy      string `json:"dual_control_disarm_requested_by"`
	IgnoredLastRun       string `json:"last_run"`
	IgnoredNextRun       string `json:"next_run"`
	IgnoredLastRunStatus string `json:"last_run_status"`
	IgnoredLastRunError  string `json:"last_run_error"`
}

// ---------- handlers ----------

var errDRUnavailable = fmt.Errorf("dr service unavailable")

func (s *Server) handleTriggerBackup(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	p := mc.Principal
	if s.drSvc == nil {
		s.writeError(w, r, errDRUnavailable)
		return
	}

	var req triggerBackupRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.badRequest(w, r, RequestBodyErrorMessage(err, "invalid request body"))
		return
	}
	if req.Passphrase == "" {
		s.badRequest(w, r, "passphrase is required")
		return
	}
	if msg := drPassphraseFloorError(req.Passphrase); msg != "" {
		s.badRequest(w, r, msg)
		return
	}

	if err := s.drSvc.ensureBackupDir(); err != nil {
		s.writeError(w, r, fmt.Errorf("backup dir: %w", err))
		return
	}

	job := s.drSvc.jobs.create(drJobBackup, req.Notes)

	// The detached job must survive cancellation when the 202 response completes.
	go s.runBackup(context.WithoutCancel(r.Context()), job.ID, req.Passphrase, req.Notes, p.Actor())

	writeJSON(w, http.StatusAccepted, triggerBackupResponse{JobID: job.ID})
}

func (s *Server) runBackup(ctx context.Context, jobID, passphrase, notes, actor string) {
	svc := s.drSvc
	update := func(phase string, progress int) {
		svc.jobs.update(jobID, func(j *drJob) {
			j.Status = drJobRunning
			j.Phase = phase
			j.Progress = progress
		})
	}

	fail := func(err error) {
		svc.jobs.update(jobID, func(j *drJob) {
			j.Status = drJobFailed
			j.Error = err.Error()
			j.DoneAt = time.Now().UTC().Format(time.RFC3339)
		})
		s.log.Error("dr: backup failed", "job", jobID, "err", err)
	}

	update("preparing", 5)

	cipher, err := dr.NewPassphraseCipher([]byte(passphrase))
	if err != nil {
		fail(err)
		return
	}

	update("scanning_keys", 10)

	dataDir := svc.cfg.DataDir
	var sealedKeys map[string][]byte
	var keyRefs []dr.KeyRef
	var sealerProbes []dr.SealerProbe
	if svc.cfg.SealKeys != nil {
		sealedKeys, keyRefs, sealerProbes, err = svc.cfg.SealKeys(cipher)
		if err != nil {
			fail(fmt.Errorf("seal installation custody: %w", err))
			return
		}
	} else {
		sealedKeys = make(map[string][]byte)
		keyFiles, err := filepath.Glob(filepath.Join(dataDir, "*-signing.key"))
		if err != nil {
			fail(fmt.Errorf("scan keys: %w", err))
			return
		}

		for _, kf := range keyFiles {
			raw, err := os.ReadFile(kf)
			if err != nil {
				fail(fmt.Errorf("read key %s: %w", filepath.Base(kf), err))
				return
			}
			name := filepath.Base(kf)
			bundlePath := "keys/" + name + ".enc"
			sealed, err := cipher.Seal(raw)
			if err != nil {
				fail(fmt.Errorf("seal key %s: %w", name, err))
				return
			}
			sealedKeys[bundlePath] = sealed
			fp, _ := dr.PubFingerprintFromSigningKey(raw)
			role := dr.RoleOther
			if strings.HasPrefix(name, "audit") {
				role = dr.RoleAudit
			} else if strings.HasPrefix(name, "catalog") {
				role = dr.RoleCatalog
			}
			keyRefs = append(keyRefs, dr.KeyRef{
				File: bundlePath, Name: name, Role: role, PubSHA256: fp,
			})
		}
	}

	update("snapshot", 25)

	var snapshotPath string
	var ss dr.StoreSnapshot
	var validateSnapshot func() error
	tipMatch := dr.TipExact

	if svc.cfg.EngineKind == "sqlite" {
		dbPath := filepath.Join(dataDir, "olivares.db")
		snapshotPath = filepath.Join(svc.cfg.BackupDir, fmt.Sprintf("snapshot-%s.db", jobID))
		if err := dr.SnapshotSQLite(ctx, dbPath, snapshotPath); err != nil {
			fail(fmt.Errorf("snapshot: %w", err))
			return
		}
		defer func() { _ = os.Remove(snapshotPath) }()

		hash, size, err := dr.FileSHA256(snapshotPath)
		if err != nil {
			fail(fmt.Errorf("digest snapshot: %w", err))
			return
		}
		ss = dr.StoreSnapshot{
			Method:    dr.MethodVacuumInto,
			File:      "store/olivares.db",
			SizeBytes: size,
			SHA256:    hash,
		}
	} else if svc.cfg.EngineKind == "postgres" && svc.cfg.PostgresSnapshot != nil {
		snapshotPath = filepath.Join(svc.cfg.BackupDir, fmt.Sprintf("snapshot-%s.pgcustom", jobID))
		defer func() { _ = os.Remove(snapshotPath) }()
		snapshot, err := svc.cfg.PostgresSnapshot(ctx, snapshotPath)
		if err != nil {
			fail(fmt.Errorf("snapshot: %w", err))
			return
		}
		if snapshot.Validate == nil {
			fail(fmt.Errorf("PostgreSQL snapshot has no payload verification"))
			return
		}
		ss, validateSnapshot = snapshot.Store, snapshot.Validate
		// The native dump and the manifest read the live store separately.
		tipMatch = dr.TipAdvisory
	} else {
		fail(fmt.Errorf("engine %q not supported for web backup (use CLI for postgres)", svc.cfg.EngineKind))
		return
	}

	update("manifest", 50)

	eventPub := s.signer.PublicKey()
	cpVerifier := audit.NewCheckpointVerifier().AddEd25519(eventPub)
	if svc.cfg.EngineKind == "postgres" {
		cpVerifier, err = s.signer.CheckpointVerifier(ctx)
		if err != nil {
			fail(fmt.Errorf("checkpoint verifier: %w", err))
			return
		}
	}

	manifestStore := s.st
	if svc.cfg.EngineKind == "sqlite" {
		scratchDir, err := os.MkdirTemp(svc.cfg.BackupDir, "manifest-*")
		if err != nil {
			fail(fmt.Errorf("stage snapshot manifest: %w", err))
			return
		}
		defer func() { _ = os.RemoveAll(scratchDir) }()
		scratch, err := openScratchSnapshot(ctx, snapshotPath, filepath.Join(scratchDir, "olivares.db"), s.signer, svc.cfg.RegisterSchema)
		if err != nil {
			fail(fmt.Errorf("open snapshot to build manifest: %w", err))
			return
		}
		defer func() { _ = scratch.Close() }()
		// TipExact describes the bundled snapshot, even while live requests append.
		manifestStore = scratch
	}
	manifest, err := dr.BuildManifest(ctx, manifestStore, eventPub, cpVerifier, dr.BuildOptions{
		EngineKind: svc.cfg.EngineKind,
		Version:    s.version,
		Store:      ss,
		Keys:       keyRefs,
		TipMatch:   tipMatch,
		Now:        time.Now().UTC(),
		Notes:      notes + " [via console by " + actor + "]",
	})
	if err != nil {
		fail(fmt.Errorf("build manifest: %w", err))
		return
	}
	manifest.SealerProbes = sealerProbes

	if svc.cfg.EngineKind == "postgres" {
		for _, tip := range manifest.Tenants {
			if !tip.VerifiedAtBackup {
				fail(fmt.Errorf("backup refused: unverified tenant %s (%s); repair the ledger before creating a backup", tip.Tenant, tip.VerifyReason))
				return
			}
		}
	}

	update("bundle", 70)

	ts := time.Now().UTC().Format("20060102-150405")
	bundleName := fmt.Sprintf("olivares-%s-%s.drbundle", ts, svc.cfg.EngineKind)
	bundlePath := filepath.Join(svc.cfg.BackupDir, bundleName)

	if validateSnapshot != nil {
		if err := validateSnapshot(); err != nil {
			fail(err)
			return
		}
	}
	f, err := os.Create(bundlePath)
	if err != nil {
		fail(fmt.Errorf("create bundle: %w", err))
		return
	}

	if err := dr.WriteAuthenticatedBundle(f, dr.BundleInput{
		Manifest:     manifest,
		KEK:          cipher.Params(),
		SnapshotPath: snapshotPath,
		SealedKeys:   sealedKeys,
	}, cipher); err != nil {
		_ = f.Close()
		_ = os.Remove(bundlePath)
		fail(fmt.Errorf("write bundle: %w", err))
		return
	}
	if err := f.Close(); err != nil {
		fail(fmt.Errorf("close bundle: %w", err))
		return
	}

	update("complete", 100)

	svc.jobs.update(jobID, func(j *drJob) {
		j.Status = drJobCompleted
		j.Phase = "complete"
		j.Progress = 100
		j.BundlePath = bundlePath
		j.BundleID = bundleName
		j.DoneAt = time.Now().UTC().Format(time.RFC3339)
	})

	s.log.Info("dr: backup completed", "job", jobID, "bundle", bundleName, "actor", actor)
}

func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	if s.drSvc == nil {
		s.writeError(w, r, errDRUnavailable)
		return
	}

	dir := s.drSvc.cfg.BackupDir
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, map[string]any{"items": []any{}})
			return
		}
		s.writeError(w, r, err)
		return
	}

	var items JSONArray[backupListItem]
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".drbundle") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		item := backupListItem{
			ID:        e.Name(),
			Filename:  e.Name(),
			SizeBytes: info.Size(),
			CreatedAt: info.ModTime().UTC().Format(time.RFC3339),
		}

		manifest := s.inspectBundle(filepath.Join(dir, e.Name()))
		if manifest != nil {
			item.CreatedAt = manifest.CreatedAt
			item.EngineKind = manifest.EngineKind
			item.Version = manifest.Version
			item.TenantCount = len(manifest.Tenants)
			item.Notes = manifest.Notes
		}
		items = append(items, item)
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt > items[j].CreatedAt
	})

	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) inspectBundle(path string) *dr.Manifest {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	tmp, err := os.MkdirTemp("", "dr-inspect-*")
	if err != nil {
		return nil
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	m, _, err := dr.ExtractBundle(f, tmp)
	if err != nil {
		return nil
	}
	return m
}

func (s *Server) handleGetBackup(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	if s.drSvc == nil {
		s.writeError(w, r, errDRUnavailable)
		return
	}

	id := chi.URLParam(r, "id")
	if id == "" || strings.Contains(id, "/") || strings.Contains(id, "..") {
		s.badRequest(w, r, "invalid backup id")
		return
	}

	bundlePath := filepath.Join(s.drSvc.cfg.BackupDir, id)
	manifest := s.inspectBundle(bundlePath)
	if manifest == nil {
		s.writeError(w, r, fmt.Errorf("not found: %w", errBadRequest))
		return
	}

	info, err := os.Stat(bundlePath)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":         id,
		"filename":   id,
		"size_bytes": info.Size(),
		"manifest":   manifest,
	})
}

func (s *Server) handleDownloadBackup(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	if s.drSvc == nil {
		s.writeError(w, r, errDRUnavailable)
		return
	}

	id := chi.URLParam(r, "id")
	if id == "" || strings.Contains(id, "/") || strings.Contains(id, "..") {
		s.badRequest(w, r, "invalid backup id")
		return
	}

	bundlePath := filepath.Join(s.drSvc.cfg.BackupDir, id)
	f, err := os.Open(bundlePath)
	if err != nil {
		if os.IsNotExist(err) {
			s.writeError(w, r, fmt.Errorf("not found: %w", errBadRequest))
			return
		}
		s.writeError(w, r, err)
		return
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, id))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

func (s *Server) handleDeleteBackup(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	p := mc.Principal
	if s.drSvc == nil {
		s.writeError(w, r, errDRUnavailable)
		return
	}

	id := chi.URLParam(r, "id")
	if id == "" || strings.Contains(id, "/") || strings.Contains(id, "..") {
		s.badRequest(w, r, "invalid backup id")
		return
	}

	bundlePath := filepath.Join(s.drSvc.cfg.BackupDir, id)
	if _, err := os.Stat(bundlePath); os.IsNotExist(err) {
		s.writeError(w, r, fmt.Errorf("not found: %w", errBadRequest))
		return
	}

	if err := os.Remove(bundlePath); err != nil {
		s.writeError(w, r, err)
		return
	}

	s.log.Info("dr: backup deleted", "id", id, "actor", p.Actor())
	w.WriteHeader(http.StatusNoContent)
}

// maxBundleUpload caps the restore upload at 10 GiB.
const maxBundleUpload = 10 << 30

func (s *Server) handleRestoreUpload(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	if s.drSvc == nil {
		s.writeError(w, r, errDRUnavailable)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBundleUpload)

	// A fresh install has never made a backup, so the directory may not exist yet.
	if err := s.drSvc.ensureBackupDir(); err != nil {
		s.writeError(w, r, fmt.Errorf("backup dir: %w", err))
		return
	}
	tmpFile, err := os.CreateTemp(s.drSvc.cfg.BackupDir, "restore-upload-*.drbundle")
	if err != nil {
		s.writeError(w, r, fmt.Errorf("create temp: %w", err))
		return
	}
	tmpPath := tmpFile.Name()

	if _, err := io.Copy(tmpFile, r.Body); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		s.badRequest(w, r, "upload failed: "+err.Error())
		return
	}
	_ = tmpFile.Close()

	manifest := s.inspectBundle(tmpPath)
	if manifest == nil {
		_ = os.Remove(tmpPath)
		s.badRequest(w, r, "invalid or corrupt DR bundle")
		return
	}

	uploadID := filepath.Base(tmpPath)

	writeJSON(w, http.StatusOK, restoreUploadResponse{
		UploadID: uploadID,
		Manifest: manifest,
		Filename: uploadID,
	})
}

func (s *Server) handleRestoreApply(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	p := mc.Principal
	if s.drSvc == nil {
		s.writeError(w, r, errDRUnavailable)
		return
	}

	uploadID := chi.URLParam(r, "id")
	if uploadID == "" || strings.Contains(uploadID, "/") || strings.Contains(uploadID, "..") {
		s.badRequest(w, r, "invalid upload id")
		return
	}

	var req restoreApplyRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.badRequest(w, r, RequestBodyErrorMessage(err, "invalid request body"))
		return
	}

	bundlePath := filepath.Join(s.drSvc.cfg.BackupDir, uploadID)
	if _, err := os.Stat(bundlePath); os.IsNotExist(err) {
		s.badRequest(w, r, "upload not found — re-upload the bundle")
		return
	}

	// A gated restore records an intent; its distinct approver supplies the passphrase.
	// Compare the account before consulting its gate. Delegated credentials cannot
	// pass authzSystem; if that changes, the comparison must use the acted-for account.
	initiator := auth.PersonRefOf(p)
	if s.drSvc.requireDualControlRestoreFor(initiator.User, s.clock.Now().Time().UTC()) {
		if !initiator.Stable() {
			s.forbidden(w, r, errNoStableIdentity.Error())
			return
		}
		pr := s.drSvc.registerPending(uploadID, initiator, time.Now().UTC().Format(time.RFC3339))
		s.log.Info("dr: restore requested — awaiting a second approver (dual-control)",
			"request", pr.RequestID, "initiator", pr.Initiator, "initiator_user", pr.InitiatorUser, "upload", uploadID)
		writeJSON(w, http.StatusAccepted, restoreApplyResponse{AwaitingApproval: true, RequestID: pr.RequestID, Initiator: pr.Initiator})
		return
	}

	if req.Passphrase == "" {
		s.badRequest(w, r, "passphrase is required to decrypt backup keys")
		return
	}
	job := s.drSvc.jobs.create(drJobRestore, "restore "+uploadID)
	s.rememberRestoreReceipt(job.ID, r, p.Actor())
	// The detached job must survive cancellation when the 202 response completes.
	go s.runRestore(context.WithoutCancel(r.Context()), job.ID, bundlePath, req.Passphrase, p.Actor())
	writeJSON(w, http.StatusAccepted, restoreApplyResponse{JobID: job.ID})
}

// handleRestoreApprove requires a distinct stable administrator account and
// its passphrase. Another credential of the requester cannot self-approve.
// CLI restore is outside this console gate.
func (s *Server) handleRestoreApprove(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	p := mc.Principal
	if s.drSvc == nil {
		s.writeError(w, r, errDRUnavailable)
		return
	}

	uploadID := chi.URLParam(r, "id")
	if uploadID == "" || strings.Contains(uploadID, "/") || strings.Contains(uploadID, "..") {
		s.badRequest(w, r, "invalid upload id")
		return
	}
	var req restoreApproveRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.badRequest(w, r, RequestBodyErrorMessage(err, "invalid request body"))
		return
	}
	if req.RequestID == "" {
		s.badRequest(w, r, "request_id is required")
		return
	}
	if req.Passphrase == "" {
		s.badRequest(w, r, "passphrase is required to decrypt backup keys")
		return
	}

	approver := auth.PersonRefOf(p)
	pr, err := s.drSvc.approvePending(req.RequestID, uploadID, approver)
	if errors.Is(err, errSelfApprove) || errors.Is(err, errNoStableIdentity) {
		s.forbidden(w, r, err.Error())
		return
	}
	if err != nil {
		s.badRequest(w, r, err.Error())
		return
	}

	bundlePath := filepath.Join(s.drSvc.cfg.BackupDir, uploadID)
	if _, err := os.Stat(bundlePath); os.IsNotExist(err) {
		s.badRequest(w, r, "upload not found — re-upload the bundle")
		return
	}
	job := s.drSvc.jobs.create(drJobRestore, "restore "+uploadID+" (dual-control: "+pr.Initiator+"→"+p.Actor()+")")
	s.rememberRestoreReceipt(job.ID, r, p.Actor())
	// Record the accounts alongside their credential actors.
	s.log.Info("dr: restore approved under dual-control", "job", job.ID,
		"initiator", pr.Initiator, "initiator_user", pr.InitiatorUser,
		"approver", approver.Actor, "approver_user", approver.User)
	// The detached job must survive cancellation when the 202 response completes.
	go s.runRestore(context.WithoutCancel(r.Context()), job.ID, bundlePath, req.Passphrase, pr.Initiator+"+"+p.Actor())
	writeJSON(w, http.StatusAccepted, restoreApplyResponse{JobID: job.ID})
}

func (s *Server) runRestore(ctx context.Context, jobID, bundlePath, passphrase, actor string) {
	svc := s.drSvc
	update := func(phase string, progress int) {
		svc.jobs.update(jobID, func(j *drJob) {
			j.Status = drJobRunning
			j.Phase = phase
			j.Progress = progress
		})
	}

	promoted := false
	fail := func(err error) {
		svc.jobs.update(jobID, func(j *drJob) {
			j.restartSafe = !promoted
			j.Status = drJobFailed
			j.Error = err.Error()
			j.DoneAt = time.Now().UTC().Format(time.RFC3339)
		})
		s.log.Error("dr: restore failed", "job", jobID, "err", err)
	}

	// Console restore is SQLite-only. Refuse before opening a bundle, taking a
	// filesystem lease, or staging anything in the live data directory.
	if svc.cfg.EngineKind != "sqlite" {
		fail(fmt.Errorf("engine %q not supported for web restore (use CLI for postgres)", svc.cfg.EngineKind))
		return
	}

	// Hold the exclusive restore guard from the first read through key/store
	// replacement. Refuse concurrent boot or restore rather than queueing it.
	guard, gerr := acquireConsoleRestoreGuard(svc.cfg)
	if gerr != nil {
		fail(gerr)
		return
	}
	defer guard.release()
	// A request authorized before maintenance may start after the first job
	// released its guard. Keep that job's custody and recovery advice intact.
	if svc.restartRequired.Load() {
		fail(fmt.Errorf("live store is stopped for an earlier restore; follow that job's recovery or restart instruction before applying another bundle"))
		return
	}

	update("extracting", 10)

	tmpDir, err := os.MkdirTemp("", "dr-restore-*")
	if err != nil {
		fail(err)
		return
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	f, err := os.Open(bundlePath)
	if err != nil {
		fail(err)
		return
	}
	manifest, kdfParams, err := dr.ExtractBundle(f, tmpDir)
	_ = f.Close()
	if err != nil {
		fail(fmt.Errorf("extract bundle: %w", err))
		return
	}
	if err := dr.CheckImportCompatibility(manifest, s.version); err != nil {
		fail(err)
		return
	}

	if manifest.Store.SHA256 != "" {
		snapshotPath := filepath.Join(tmpDir, filepath.FromSlash(manifest.Store.File))
		hash, _, err := dr.FileSHA256(snapshotPath)
		if err != nil {
			fail(fmt.Errorf("verify snapshot digest: %w", err))
			return
		}
		if hash != manifest.Store.SHA256 {
			fail(fmt.Errorf("snapshot digest mismatch: bundle=%s, computed=%s", manifest.Store.SHA256, hash))
			return
		}
	}

	update("decrypting_keys", 30)

	cipher, err := dr.OpenCipher([]byte(passphrase), kdfParams)
	if err != nil {
		fail(fmt.Errorf("open cipher (wrong passphrase?): %w", err))
		return
	}
	if err := dr.VerifyBundleIntegrity(tmpDir, manifest, kdfParams, cipher, false); err != nil {
		fail(fmt.Errorf("verify bundle authentication: %w", err))
		return
	}

	update("verifying_bundle", 40)

	// Verify ledger continuity in scratch before overwriting live keys or data.
	if rep, verr := verifyBundleScratch(ctx, tmpDir, manifest, cipher, svc.cfg.RegisterSchema); verr != nil {
		fail(fmt.Errorf("pre-restore verification: %w", verr))
		return
	} else if !rep.OK {
		fail(fmt.Errorf("bundle is NOT ledger-continuity-safe; live store left untouched: %s", strings.Join(rep.Problems, "; ")))
		return
	}

	dataDir := svc.cfg.DataDir
	stage, err := stageConsoleRestoreKeys(dataDir, tmpDir, manifest, cipher)
	if err != nil {
		fail(err)
		return
	}
	defer func() {
		if err := os.RemoveAll(stage.dir); err != nil {
			s.log.Error("dr: private restore staging cleanup failed", "path", stage.dir, "err", err)
		}
	}()
	same := dr.SameInstallation(dataDir, manifest)

	if svc.cfg.QuiesceStore == nil {
		fail(fmt.Errorf("live store cannot drain safely for console restore; use CLI restore with the engine stopped"))
		return
	}
	svc.beginMaintenance(jobID)
	update("draining_store", 45)
	drainCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = svc.cfg.QuiesceStore(drainCtx)
	cancel()
	if err != nil {
		fail(fmt.Errorf("drain store: %w; live data and keys were not replaced; restart the engine before retrying", err))
		return
	}
	suffix := ".pre-restore-" + jobID
	if err := stage.preserve(ctx, dataDir, suffix); err != nil {
		fail(fmt.Errorf("preserve current state: %w; live data and keys were not replaced; restart the engine before retrying", err))
		return
	}
	preservation := fmt.Sprintf("previous state preserved as *%s in %s (remove once satisfied)", suffix, dataDir)
	svc.jobs.update(jobID, func(j *drJob) { j.Notes = preservation })
	update("restoring_store", 50)
	snapshotPath := filepath.Join(tmpDir, filepath.FromSlash(manifest.Store.File))
	if err := promoteSQLiteSnapshot(ctx, snapshotPath, filepath.Join(dataDir, "olivares.db")); err != nil {
		fail(fmt.Errorf("restore store: %w; the signing keys were not replaced; restart the engine before retrying", err))
		return
	}
	promoted = true
	count, err := stage.promoteKeys(dataDir)
	if err != nil {
		if rollbackErr := stage.rollback(dataDir, count); rollbackErr != nil {
			fail(fmt.Errorf("%w; rollback failed: %w; do not restart until the database and all custody keys are recovered from the preserved state or the complete bundle", err, rollbackErr))
		} else {
			promoted = false
			fail(fmt.Errorf("%w; rolled back to the pre-restore state; restart the engine before retrying", err))
		}
		return
	}
	getenv := svc.cfg.Getenv
	if getenv == nil {
		getenv = envconfig.Get
	}
	custody := dr.RestoreCustodyNote("restore verified and promoted in place", manifest, dataDir, same, getenv)

	update("promoted", 90)

	// The closed store cannot sign any restored event until restart reloads custody.
	s.log.Warn("dr: restore verified in scratch and promoted — restart the engine to load the restored keys and module state",
		"job", jobID, "actor", actor, "engine", manifest.EngineKind)

	svc.jobs.update(jobID, func(j *drJob) {
		j.Status = drJobCompleted
		j.Phase = "restart_required"
		j.Notes = custody + ". " + preservation
		j.Progress = 100
		j.DoneAt = time.Now().UTC().Format(time.RFC3339)
	})
}

// beginMaintenance pins the job whose recovery advice applies until restart.
func (svc *drService) beginMaintenance(jobID string) {
	svc.smu.Lock()
	svc.maintenanceJob = jobID
	svc.smu.Unlock()
	svc.restartRequired.Store(true)
}

// rememberRestoreReceipt grants only this job's progress to the credential that
// already passed system:admin and the restore policy. The grant expires in ten
// minutes or at restart; it cannot authorize another request or any store write.
func (s *Server) rememberRestoreReceipt(jobID string, r *http.Request, actor string) {
	token, _, err := requestCredential(r)
	if err != nil || token == "" {
		return
	}
	hash := sha256.Sum256([]byte(token))
	s.drSvc.jobs.update(jobID, func(j *drJob) {
		j.receiptHash = hash
		j.receiptUntil = time.Now().Add(10 * time.Minute)
		j.receiptActor = actor
	})
}

// restoreMaintenance answers before authentication touches the closed store.
// The restore administrator can reconnect to its read-only job receipt; all
// other requests remain unavailable until custody and module state reload.
func (s *Server) restoreMaintenance(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		svc := s.drSvc
		if svc == nil || !svc.restartRequired.Load() {
			next.ServeHTTP(w, r)
			return
		}
		// Liveness must not restart a pod during promotion or key recovery.
		// These existing handlers never touch the stopped store; readiness does.
		if r.Method == http.MethodGet {
			switch r.URL.Path {
			case "/livez":
				s.handleLivez(w, r, ModuleContext{})
				return
			case "/healthz":
				s.handleHealth(w, r, ModuleContext{})
				return
			}
		}
		svc.smu.Lock()
		jobID := svc.maintenanceJob
		svc.smu.Unlock()
		job, ok := svc.jobs.get(jobID)
		token, cookieAuth, err := requestCredential(r)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if cookieAuth && !validBrowserCSRF(r, token) {
			s.writeError(w, r, errForbidden)
			return
		}
		hash := sha256.Sum256([]byte(token))
		receipt := ok && token != "" && time.Now().Before(job.receiptUntil) && subtle.ConstantTimeCompare(hash[:], job.receiptHash[:]) == 1
		if receipt && r.Method == http.MethodGet {
			w.Header().Set("Cache-Control", "no-store")
			if h := actorHolderFrom(r.Context()); h != nil {
				h.actor = job.receiptActor
			}
			switch r.URL.Path {
			case "/v1/console/dr/jobs":
				writeJSON(w, http.StatusOK, map[string]any{"items": []drJob{job}})
				return
			case "/v1/console/dr/jobs/" + jobID + "/stream":
				s.streamDRJob(w, r, jobID)
				return
			}
		}
		var body errorBody
		body.Error.Code = "restore_restart_required"
		switch {
		case !ok || job.Status == drJobPending || job.Status == drJobRunning:
			body.Error.Message = "Restore is still running with the live store stopped. Wait for its result before restarting."
		case job.Status == drJobFailed && !job.restartSafe:
			body.Error.Message = "Restore requires complete custody recovery. Do not restart until all custody keys from the bundle are restored, including keys not yet attempted; consult the restore job or DR runbook."
		default:
			body.Error.Message = "The live store is stopped. Restart the engine to load the signing keys and module state before continuing."
		}
		w.Header().Set("Retry-After", "30")
		writeJSON(w, http.StatusServiceUnavailable, body)
	})
}

// validRestoredKeyName accepts only a plain file name. The key files are written into the
// data dir after the store is restored, and a name from the bundle's manifest must not pick
// another directory or the store's own files; it is checked before anything changes.
func validRestoredKeyName(name string) error {
	if name == "" || name == "." || name == ".." || name == ".gitignore" || name != filepath.Base(name) || strings.HasPrefix(name, "olivares.db") {
		return fmt.Errorf("bundle key name %q is not a plain file name", name)
	}
	return nil
}

// promoteSQLiteSnapshot restores through SQLite's backup API. SQLite owns the
// destination write transaction and WAL, so active connections cannot replay old
// pages over the snapshot, and a failed copy rolls back without unlinking live files.
func promoteSQLiteSnapshot(ctx context.Context, snapshot, dbPath string) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	// The engine pool is drained; an external SQLite writer can still hold a transaction.
	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return fmt.Errorf("restore busy_timeout: %w", err)
	}
	return conn.Raw(func(raw any) error {
		restorer, ok := raw.(interface {
			NewRestore(string) (*sqlite.Backup, error)
		})
		if !ok {
			return fmt.Errorf("SQLite driver does not support snapshot restore")
		}
		backup, err := restorer.NewRestore(snapshot)
		if err != nil {
			return err
		}
		for more := true; more; {
			if err = ctx.Err(); err != nil {
				break
			}
			more, err = backup.Step(128)
			if err != nil {
				break
			}
		}
		return errors.Join(err, backup.Finish())
	})
}

func (s *Server) handleDRJobStream(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	if s.drSvc == nil {
		s.writeError(w, r, errDRUnavailable)
		return
	}

	jobID := chi.URLParam(r, "id")
	if jobID == "" {
		s.badRequest(w, r, "job id required")
		return
	}

	s.streamDRJob(w, r, jobID)
}

func (s *Server) streamDRJob(w http.ResponseWriter, r *http.Request, jobID string) {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, cancel := s.drSvc.jobs.broker.subscribe(jobID)
	defer cancel()

	if writeFrame(rc, w, ": connected\n\n") != nil {
		return
	}

	if current, ok := s.drSvc.jobs.get(jobID); ok {
		payload, _ := json.Marshal(current)
		if writeFrame(rc, w, fmt.Sprintf("event: job\ndata: %s\n\n", payload)) != nil {
			return
		}
		if current.Status == drJobCompleted || current.Status == drJobFailed {
			return
		}
	}

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case job, ok := <-ch:
			if !ok {
				return
			}
			payload, err := json.Marshal(job)
			if err != nil {
				continue
			}
			if writeFrame(rc, w, fmt.Sprintf("event: job\ndata: %s\n\n", payload)) != nil {
				return
			}
			if job.Status == drJobCompleted || job.Status == drJobFailed {
				return
			}
		case <-ticker.C:
			if writeFrame(rc, w, ": ping\n\n") != nil {
				return
			}
		}
	}
}

func (s *Server) handleGetDRSchedule(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	if s.drSvc == nil {
		s.writeError(w, r, errDRUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, s.drScheduleView())
}

func (s *Server) handlePutDRSchedule(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	p := mc.Principal
	if s.drSvc == nil {
		s.writeError(w, r, errDRUnavailable)
		return
	}

	var req drScheduleRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.badRequest(w, r, RequestBodyErrorMessage(err, "invalid request body"))
		return
	}
	req.Cron = strings.TrimSpace(req.Cron)
	if req.Retain < 0 {
		s.badRequest(w, r, "retain_days must be >= 0")
		return
	}
	// Use the runner's grammar when validating a schedule.
	if req.Cron != "" {
		if _, err := cron.Parse(req.Cron); err != nil {
			s.badRequest(w, r, "invalid cron: "+err.Error())
			return
		}
	}
	if req.Enabled && req.Cron == "" {
		s.badRequest(w, r, "an enabled schedule needs a cron expression")
		return
	}

	// Persist the schedule and restore policy before publishing in-memory state.
	s.drSvc.scheduleOpMu.Lock()
	defer s.drSvc.scheduleOpMu.Unlock()
	now := s.clock.Now().Time().UTC()
	next := s.drSvc.scheduleSnapshot()
	next.Enabled = req.Enabled
	next.Cron = req.Cron
	next.Retain = req.Retain
	dualControlChange := applyDualControlRequest(&next, req.RequireDualControl, now, auth.PersonRefOf(p).User)
	if err := s.saveDRSchedule(r.Context(), next); err != nil {
		s.writeError(w, r, fmt.Errorf("persist DR schedule: %w", err))
		return
	}
	s.drSvc.setSchedule(func(d *drSchedule) { *d = next })

	s.log.Info("dr: schedule updated", "enabled", req.Enabled, "cron", req.Cron,
		"retain_days", req.Retain, "require_dual_control_restore", next.dualControlArmed(now),
		"dual_control_change", dualControlChange, "dual_control_disarm_effective_at", next.DisarmAt,
		"dual_control_disarm_requested_by", next.DisarmBy, "actor", p.Actor())
	writeJSON(w, http.StatusOK, s.drScheduleView())
}

// applyDualControlRequest preserves an omitted gate, arms immediately and
// records delayed disarms. Re-arming cancels a disarm and clears its requester.
// Repeated disarms keep their instant and requester; collapsing a spent instant
// keeps DisarmBy so the disarmer cannot restore by asking again.
func applyDualControlRequest(next *drSchedule, want *bool, now time.Time, account string) string {
	switch {
	case want == nil:
		return "unchanged"
	case *want:
		if next.RequireDualControl && next.DisarmAt == "" && next.DisarmBy == "" {
			return "unchanged"
		}
		canceled := next.disarmPending(now)
		next.RequireDualControl = true
		next.DisarmAt = ""
		next.DisarmBy = ""
		if canceled {
			return "disarm_cancelled"
		}
		return "armed"
	case !next.dualControlArmed(now):
		// Not armed (never was, or a previous disarm already took effect): collapse
		// to the plain off state so the stored config stops carrying a spent instant.
		// DisarmBy stays: it records who freed this estate, and the gate still holds
		// against them until someone re-arms.
		next.RequireDualControl = false
		next.DisarmAt = ""
		return "unchanged"
	case next.disarmPending(now):
		return "disarm_pending" // already scheduled — do not move it, nor whose it is
	default:
		next.DisarmAt = now.Add(drDualControlDisarmDelay).Format(time.RFC3339)
		next.DisarmBy = account
		return "disarm_scheduled"
	}
}

// drScheduleView derives next_run and reports the effective restore gate.
// Pending disarms remain visible; spent instants are omitted from the response.
func (s *Server) drScheduleView() drSchedule {
	now := s.clock.Now().Time().UTC()
	sched := s.drSvc.scheduleSnapshot()
	sched.NextRun = ""
	if sched.Enabled && sched.Cron != "" {
		if spec, err := cron.Parse(sched.Cron); err == nil {
			if next, ok := nextDRCronAfter(spec, s.clock.Now().Time()); ok {
				sched.NextRun = next.UTC().Format(time.RFC3339)
			}
		}
	}
	// Compute both answers before omitting a spent instant; clearing it first
	// would make the stored armed flag hold the gate indefinitely.
	armed, pending := sched.dualControlArmed(now), sched.disarmPending(now)
	if !pending {
		sched.DisarmAt = ""
	}
	sched.RequireDualControl = armed
	return sched
}

// handleListPendingRestores lists restore requests awaiting a second approver, so a
// distinct admin can find and approve one (dual-control).
func (s *Server) handleListPendingRestores(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	if s.drSvc == nil {
		s.writeError(w, r, errDRUnavailable)
		return
	}
	s.drSvc.pmu.Lock()
	items := make([]pendingRestore, 0, len(s.drSvc.pending))
	for _, pr := range s.drSvc.pending {
		items = append(items, *pr)
	}
	s.drSvc.pmu.Unlock()
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt > items[j].CreatedAt })
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleListDRJobs(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	if s.drSvc == nil {
		s.writeError(w, r, errDRUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.drSvc.jobs.list()})
}
