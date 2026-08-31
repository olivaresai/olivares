// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
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
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/dr"
)

// DRConfig configures the disaster-recovery console surface. When set in
// Options, the console backup/restore and schedule endpoints are available. nil
// leaves them answering 501 (an embedder/test that did not opt in).
type DRConfig struct {
	DataDir    string
	EngineKind string
	BackupDir  string
	// PassphraseFile is the path of the file holding the backup passphrase used
	// by SCHEDULED backups (the same $OLIVARES_DR_PASSPHRASE_FILE the CLI DR
	// commands read — an unattended cron run has no admin to type one). Empty
	// leaves the schedule runner refusing to run, loudly, until it is set: a
	// backup that cannot seal its keys must not pretend to have run.
	PassphraseFile string
}

// drService wraps the DR configuration and in-memory job tracker.
type drService struct {
	cfg  DRConfig
	jobs *drJobTracker
	// schedule is the current backup schedule + DR policy. Unlike the job tracker
	// it is PERSISTED: the config — including the RequireDualControl
	// security gate — is stored in the estate (dr_schedule.go) and reloaded at
	// boot, so a restart never silently resets it. Guarded by smu: the console
	// handlers and the background schedule runner touch it concurrently.
	smu          sync.Mutex
	scheduleOpMu sync.Mutex
	schedule     *drSchedule
	// pending holds restore requests awaiting a second approver (dual-control). It is
	// in-memory like the job tracker (the design); a process restart clears
	// pending requests, which is safe — an unconsumed restore intent simply has to
	// be re-requested.
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
	// RequireDualControl gates CONSOLE restore behind a second, DISTINCT
	// administrator (dual-control). Default false so a solo operator can still
	// restore; when true the initiator requests and a different admin approves.
	// Restore is the most destructive console operation, so this is the control an
	// estate with more than one admin should enable.
	//
	// What it does NOT reach, said here because the console's own copy promised
	// otherwise: `olivares dr restore` on the host. That path has no session and no
	// principal, and this very file sends Postgres estates to it (runRestore refuses
	// any non-sqlite engine with "use CLI for postgres"), so on the default
	// production engine this gate covers no restore at all. The command-line path
	// carries its own control — a declared operator and reason, sealed into the
	// restored ledger (cmd/olivares/dr_declaration.go) — which is a
	// declaration, not a second party. An estate that needs two approvers on EVERY
	// restore needs host access controlled as well; this flag cannot do it.
	//
	// On the wire this field reports the gate's EFFECTIVE state — what actually gates
	// a restore right now — which is not always the stored bool: see DisarmAt.
	RequireDualControl bool `json:"require_dual_control_restore"`
	// DisarmAt is the instant a REQUESTED disarm of RequireDualControl takes effect
	// (RFC3339, empty when none is pending). It exists because the gate used to
	// protect the restore and not itself: one admin PUT the flag false and the very
	// next apply ran — two requests, one account, no second party anywhere.
	//
	// The rule it encodes is "strengthen now, weaken later". Arming is immediate;
	// disarming is recorded, stays visible, and does not take effect until this
	// instant passes, during which the gate still holds and ANY admin can countermand
	// it by re-arming. It is persisted (dr_schedule.go) and the gate is COMPUTED from
	// it — never a timer, which a restart would skip.
	//
	// The alternative — requiring two parties to disarm — was rejected with its
	// reason: it is a permanent lockout in exactly the disaster the control exists
	// for. With the gate armed and the second admin unreachable, the estate could
	// then neither restore NOR disarm, ever. A delay refuses the measured attack
	// (two requests, one account, seconds apart) without ever trapping a solo operator.
	DisarmAt string `json:"dual_control_disarm_effective_at,omitempty"`
	// DisarmBy is the STABLE ACCOUNT that asked for the disarm — not the credential.
	// It is what makes the delay a control rather than a wait.
	//
	// "Strengthen now, weaken later" closed the two-requests-in-one-sitting bypass
	// and left the same bypass with an hour in it: one admin turned the gate off,
	// waited, and restored alone. A delay is not a two-person control; it is a
	// one-person control with patience, and the suite's own test required exactly
	// that solo restore, so the behavior was contract rather than oversight.
	//
	// The rule this field carries is: WEAKENING A CONTROL IS NEVER A PATH TO THE ACT
	// IT PROTECTS, FOR THE ACCOUNT THAT WEAKENED IT. Once the cool-down passes the gate
	// is off for the estate — any other admin restores unencumbered — and it keeps
	// holding against whoever requested the disarm, so waiting buys them nothing.
	//
	// It deliberately SURVIVES the collapse of a spent DisarmAt: forgetting who
	// asked would make "ask again after it takes effect" a one-request bypass.
	//
	// It is cleared by RE-ARMING — and then the next disarm re-records WHOEVER ASKS,
	// so re-arming is not an escape hatch the disarmer can use on themselves: an
	// admin who re-arms and disarms again is simply the disarmer again. The exit is a
	// SECOND ACCOUNT requesting the disarm, which is the two-party control doing its
	// job rather than a hole in it. Measured in
	// TestDRDualControlTheProvenanceFOLLOWSWhoeverAsksLast, which is where that got
	// corrected — the first draft of this comment claimed re-arming was the exit.
	//
	// This is NOT the two-parties-to-disarm rule rejected with the delay, and that
	// rejection stands: requiring two would be a permanent lockout with the second
	// admin gone, since the estate could then neither restore nor disarm. Here the
	// estate is never locked: the disarm frees everyone else at once, and a
	// genuinely solo operator still has `olivares dr restore` on the host, which
	// carries its own declared-operator record (cmd/olivares/dr_declaration.go).
	//
	// What it does not reach, said plainly: a superadmin who can CREATE a second
	// superadmin can always manufacture a second ACCOUNT — which is why nothing in
	// this file promises two HUMANS. That is a property of
	// every dual control in this product, not of this field — and minting an admin
	// is a loud, recorded act, which flipping a boolean and waiting was not.
	DisarmBy string `json:"dual_control_disarm_requested_by,omitempty"`
}

// drDualControlDisarmDelay is how long a requested weakening of the dual-control
// restore gate waits before it takes effect.
//
// It is a constant rather than a setting on purpose: a configurable window is
// itself a control, so shortening it would be a weakening that would need this
// same delay — one more surface, protecting nothing that the constant does not.
// One hour is the smallest value that does the job it has: it must outlast the
// sitting in which a restore is attempted (the measured bypass was seconds), and
// it must be long enough for a notification to reach a second admin who can
// countermand it. It costs nothing to an estate with two admins available — two
// administrators who agree never need to disarm, because they can simply perform the
// restore under the control they would be disarming.
const drDualControlDisarmDelay = time.Hour

// disarmInstant parses DisarmAt. ok is false when there is no READABLE pending
// instant — either none was recorded, or the stored value cannot be parsed (a
// hand-edited or corrupt estate).
//
// Both callers need that distinction rather than just "armed or not", and getting
// it from one place is what keeps an unreadable instant from becoming a LOCKOUT:
// it must leave the gate armed (fail closed) while still being replaceable by a
// fresh disarm request. Treating it as a pending disarm instead would arm the gate
// permanently — no request could ever schedule an instant, because one would
// already appear to be scheduled — which is precisely the trap that the delay
// design exists to avoid.
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

// dualControlArmed reports whether the restore gate ACTUALLY holds at instant now.
// A pending disarm leaves the gate armed until its instant passes; an unreadable
// DisarmAt is no disarm at all, so corrupt state fails CLOSED.
// A disarm with NO REQUESTER on record is not a disarm either. There is nobody to
// hold it against, so honoring it would open the estate for everyone including
// whoever wrote it — and a stored record with the gate armed, an elapsed instant
// and an empty DisarmBy is exactly what a hand-edit, or an estate written before
// the provenance existed, produces (the contrast's C-03). It fails closed
// like an unreadable instant does, and like that one it stays replaceable: a fresh
// disarm request records a requester and schedules a real instant, so fail-closed
// never becomes fail-shut.
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

// dualControlHoldsFor reports whether the restore gate holds against THIS account.
//
// It is the estate-wide answer OR the per-account one: an elapsed disarm frees the
// estate but never its own requester (drSchedule.DisarmBy). account is the stable
// user, so a second credential of the same account is the same account — the
// correction, applied to the disarm as well as to the approval.
//
// A caller with NO stable account — a standalone superadmin API token, whose
// model.APIToken.UserID is zero — is held too, and that is the whole point rather
// than a detail. The comparison is over an account, so an anonymous credential
// matches no DisarmBy; the admin who disarmed had only to come back through a
// token of theirs and the gate read off. That is lesson (one account holds a
// session AND their own tokens) arriving at the disarm instead of at the approval,
// and the answer is the same: a party the estate cannot ATTRIBUTE cannot be told
// apart from the one it is holding, so it is refused rather than compared.
//
// It costs nothing to an estate that never armed the gate: with no disarm on
// record there is nothing to consume, and an anonymous token restores exactly as
// before. And it is not a lockout — the refusal is by name (errNoStableIdentity),
// and any nameable admin who did not disarm restores unencumbered.
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

// pendingRestore is a restore intent awaiting a second approver. It carries BOTH
// identities of the requester: the credential actor string (Initiator, e.g.
// "token:<id>") and the stable user ACCOUNT behind it (InitiatorUser). The account is
// what the dual-control comparison uses; the actor stays so the trail still names which
// credential was used. Recording only the actor made the trail show two actors without
// revealing they were one account (breakglass.go does the same).
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
	// errSelfApprove names the unit the gate actually compares: two DIFFERENT user
	// ACCOUNTS. It deliberately stops short of "two people". The engine cannot
	// verify that — one human may hold both accounts and two humans may share one
	// (core/auth/person.go, "WHAT THIS FILE DECIDES") — and this text is read by an
	// operator who is deciding whether they are protected. It promised "two DIFFERENT
	// people" until following PersonSame's doc, which invited exactly that.
	errSelfApprove = errors.New("dual-control: a restore must be requested and approved by two DIFFERENT user accounts; a second credential of the same account is the same requester")
	// errNoStableIdentity refuses a party that no user ACCOUNT stands behind. Dual
	// control counts accounts, and a credential with no user (model.APIToken.UserID
	// zero — "a standalone system token", core/model/auth.go) cannot be counted as
	// one of the two. Comparing its credential string instead would certify a
	// second party that nobody can name (mirroring killswitch.go).
	errNoStableIdentity = errors.New("dual-control: a restore needs a stable user identity on both sides; a system token cannot request or approve one")
)

// registerPending records a restore intent awaiting a second approver. It stores BOTH
// halves of the requester's identity (auth.PersonRef): the stable user ACCOUNT, which is
// what the distinct-approver rule compares, and the credential actor, which is what the
// trail must show. Storing only the actor is why the comparison could not be made by
// account.
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

// approvePending consumes a pending restore under dual-control: it must exist, match
// the upload, and be approved by a DIFFERENT USER ACCOUNT than the one that requested it
// (the structural separation check). The comparison is over the stable user
// identity, NOT the credential actor string: one account holds a session and its own
// tokens, so an actor-string check counted one account as two. A party with no
// user behind it is refused outright rather than compared. On success the pending
// entry is removed and returned; a refused approval LEAVES the entry in place, so a
// genuine second account can still approve.
//
// It does NOT establish two humans, and no caller may say it does: the requester can
// create the approving account and choose its password (this file already says so on
// drSchedule.DisarmBy — "a superadmin who can CREATE a second superadmin can always
// manufacture a second person"). Moved that admission out of one field's comment
// and into the text the operator reads.
func (ds *drService) approvePending(requestID, uploadID string, approver auth.PersonRef) (*pendingRestore, error) {
	ds.pmu.Lock()
	defer ds.pmu.Unlock()
	pr, ok := ds.pending[requestID]
	if !ok || pr.UploadID != uploadID {
		return nil, errNoPendingRestore
	}
	initiator := auth.PersonRef{User: pr.InitiatorUser, Actor: pr.Initiator}
	// ONE call, THREE outcomes. The two-step form this replaces — a Stable()
	// floor, then a same-account comparison — is correct and lets a caller forget the
	// floor, which is the silent pass the whole class is made of: an unattributable
	// party compares unequal to every real account, so skipping the floor ADMITS it.
	// RefuseWhenUndetermined because this gate's promise to the operator is two
	// separate administrator accounts.
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

// minDRPassphraseLen is the floor (in runes) for a passphrase that encrypts a
// NEW DR bundle. The bundle carries the estate's signing keys and the
// passphrase is its ONLY protection at rest — accepting 1 character made the
// encryption theater. 12 characters is the NIST SP 800-63B-4 §3.1.1.2 floor for
// memorized secrets chosen by the operator. Restore deliberately does not apply
// this creation policy: legacy bundles encrypted with a shorter passphrase must
// remain recoverable.
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

// drScheduleRequest is the PUT body. It is deliberately NOT drSchedule, for two
// reasons that were both live defects:
//
//   - RequireDualControl is a *bool so ABSENT is distinguishable from false.
//     Decoding into the stored struct made every edit that omitted the field a
//     silent disarm of the estate's two-person restore gate.
//   - it has no DisarmAt. A client that could name the instant a disarm takes
//     effect could name one in the past, which is the delay removed by the same
//     request that is subject to it. The instant is computed here, never accepted.
type drScheduleRequest struct {
	Enabled            bool   `json:"enabled"`
	Cron               string `json:"cron"`
	Retain             int    `json:"retain_days"`
	RequireDualControl *bool  `json:"require_dual_control_restore"`

	// The rest are SERVER-OWNED and ignored. They are declared only so a client
	// that reads the schedule, edits one field and PUTs the whole object back
	// still works: decodeJSON sets DisallowUnknownFields, so a type that merely
	// omitted them would 400 on every round-trip — which the previous type, being
	// the stored struct itself, accepted. Narrowing the writable set must not
	// narrow the ACCEPTED set.
	//
	// Ignoring is the point for DisarmAt in particular: a client that could name
	// the instant could name one already past, which is the delay deleted by the
	// same request it is supposed to delay. Measured: a PUT carrying
	// "dual_control_disarm_effective_at":"2020-01-01T00:00:00Z" leaves the gate
	// armed and the pending instant untouched.
	IgnoredDisarmAt      string `json:"dual_control_disarm_effective_at"`
	IgnoredDisarmBy      string `json:"dual_control_disarm_requested_by"`
	IgnoredLastRun       string `json:"last_run"`
	IgnoredNextRun       string `json:"next_run"`
	IgnoredLastRunStatus string `json:"last_run_status"`
	IgnoredLastRunError  string `json:"last_run_error"`
}

// ---------- handlers ----------

var errDRUnavailable = fmt.Errorf("dr service unavailable")

func (s *Server) handleTriggerBackup(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authzSystem(w, r, "system:admin")
	if !ok {
		return
	}
	if s.drSvc == nil {
		s.writeError(w, r, errDRUnavailable)
		return
	}

	var req triggerBackupRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.badRequest(w, r, "invalid request body")
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
	keyFiles, err := filepath.Glob(filepath.Join(dataDir, "*-signing.key"))
	if err != nil {
		fail(fmt.Errorf("scan keys: %w", err))
		return
	}

	sealedKeys := make(map[string][]byte)
	var keyRefs []dr.KeyRef
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

	update("snapshot", 25)

	var snapshotPath string
	var ss dr.StoreSnapshot

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
	} else {
		fail(fmt.Errorf("engine %q not supported for web backup (use CLI for postgres)", svc.cfg.EngineKind))
		return
	}

	update("manifest", 50)

	eventPub := s.signer.PublicKey()
	cpVerifier := audit.NewCheckpointVerifier().AddEd25519(eventPub)

	manifest, err := dr.BuildManifest(ctx, s.st, eventPub, cpVerifier, dr.BuildOptions{
		EngineKind: svc.cfg.EngineKind,
		Version:    s.version,
		Store:      ss,
		Keys:       keyRefs,
		TipMatch:   dr.TipExact,
		Now:        time.Now().UTC(),
		Notes:      notes + " [via console by " + actor + "]",
	})
	if err != nil {
		fail(fmt.Errorf("build manifest: %w", err))
		return
	}

	update("bundle", 70)

	ts := time.Now().UTC().Format("20060102-150405")
	bundleName := fmt.Sprintf("olivares-%s-%s.drbundle", ts, svc.cfg.EngineKind)
	bundlePath := filepath.Join(svc.cfg.BackupDir, bundleName)

	f, err := os.Create(bundlePath)
	if err != nil {
		fail(fmt.Errorf("create bundle: %w", err))
		return
	}

	if err := dr.WriteBundle(f, dr.BundleInput{
		Manifest:     manifest,
		KEK:          cipher.Params(),
		SnapshotPath: snapshotPath,
		SealedKeys:   sealedKeys,
	}); err != nil {
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

func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
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

	var items []backupListItem
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

func (s *Server) handleGetBackup(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
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

func (s *Server) handleDownloadBackup(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
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

func (s *Server) handleDeleteBackup(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authzSystem(w, r, "system:admin")
	if !ok {
		return
	}
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

func (s *Server) handleRestoreUpload(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
	if s.drSvc == nil {
		s.writeError(w, r, errDRUnavailable)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBundleUpload)

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

func (s *Server) handleRestoreApply(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authzSystem(w, r, "system:admin")
	if !ok {
		return
	}
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
		s.badRequest(w, r, "invalid request body")
		return
	}

	bundlePath := filepath.Join(s.drSvc.cfg.BackupDir, uploadID)
	if _, err := os.Stat(bundlePath); os.IsNotExist(err) {
		s.badRequest(w, r, "upload not found — re-upload the bundle")
		return
	}

	// Dual-control: register the restore INTENT and require a distinct second admin
	// to approve (with the passphrase) before it runs. The passphrase is not accepted
	// or held here — the approver supplies it, so no secret sits in memory awaiting a
	// second approver.
	//
	// NOTE on delegation: PersonRefOf reads Principal.UserID, i.e. the credential's
	// OWNER. A token-exchanged credential also carries ActAsUserID (the account whose
	// authority it exercises, core/auth/tokenexchange.go:210-211). That is not a hole
	// today because ExchangeToken refuses a superadmin subject and always mints
	// IsSuperadmin=false, so a delegated credential can never pass authzSystem here.
	// If that ever changes, "Bob acting for Alice" would count as Bob and could
	// approve Alice's own restore — this rule would then need the acted-for user.
	//
	// The question is asked FOR THIS ACCOUNT, not for the estate: an elapsed disarm
	// frees everyone except the account that asked for it, so waiting out the
	// cool-down is not a way past the gate (drSchedule.DisarmBy). Resolving
	// the account therefore has to happen BEFORE the gate is consulted.
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
	// The detached job must survive cancellation when the 202 response completes.
	go s.runRestore(context.WithoutCancel(r.Context()), job.ID, bundlePath, req.Passphrase, p.Actor())
	writeJSON(w, http.StatusAccepted, restoreApplyResponse{JobID: job.ID})
}

// handleRestoreApprove is the dual-control second step: a DIFFERENT USER ACCOUNT holding
// the system role approves a pending restore and supplies the passphrase. The requester
// cannot self-approve through ANY credential of that account — the check is over the
// stable user, so minting a token for oneself does not manufacture a second approver
// (corrected in).
//
// That makes two ACCOUNTS real FOR THIS ENDPOINT — not two humans, which this engine
// cannot verify (core/auth/person.go). Two other
// ways past it were measured and closed elsewhere rather than here, and neither was
// a flaw in this comparison: the gate could be switched off by the same admin in the
// next request (now delayed, see drSchedule.DisarmAt), and the command-line restore
// never reaches this code at all (see RequireDualControl).
func (s *Server) handleRestoreApprove(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authzSystem(w, r, "system:admin")
	if !ok {
		return
	}
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
		s.badRequest(w, r, "invalid request body")
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
	// Log the ACCOUNTS alongside the credentials: two actor strings alone never
	// revealed whether two accounts were involved.
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

	fail := func(err error) {
		svc.jobs.update(jobID, func(j *drJob) {
			j.Status = drJobFailed
			j.Error = err.Error()
			j.DoneAt = time.Now().UTC().Format(time.RFC3339)
		})
		s.log.Error("dr: restore failed", "job", jobID, "err", err)
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

	update("verifying_bundle", 40)

	// Prove the bundle restores to a continuity-safe ledger in a SCRATCH directory
	// BEFORE overwriting anything live. A corrupt, tampered, wrong-key or incomplete
	// bundle is refused HERE, with the live data dir untouched — the console never
	// promotes a restore it has not verified (the most destructive console op).
	if rep, verr := verifyBundleScratch(ctx, tmpDir, manifest, cipher); verr != nil {
		fail(fmt.Errorf("pre-restore verification: %w", verr))
		return
	} else if !rep.OK {
		fail(fmt.Errorf("bundle is NOT ledger-continuity-safe; live store left untouched: %s", strings.Join(rep.Problems, "; ")))
		return
	}

	dataDir := svc.cfg.DataDir
	for _, kr := range manifest.Keys {
		encPath := filepath.Join(tmpDir, filepath.FromSlash(kr.File))
		sealed, err := os.ReadFile(encPath)
		if err != nil {
			fail(fmt.Errorf("read sealed key %s: %w", kr.Name, err))
			return
		}
		plain, err := cipher.Open(sealed)
		if err != nil {
			fail(fmt.Errorf("decrypt key %s (wrong passphrase?): %w", kr.Name, err))
			return
		}
		dstPath := filepath.Join(dataDir, kr.Name)
		if err := os.WriteFile(dstPath, plain, 0o600); err != nil {
			fail(fmt.Errorf("write key %s: %w", kr.Name, err))
			return
		}
	}

	update("restoring_store", 50)

	if svc.cfg.EngineKind == "sqlite" {
		snapshotPath := filepath.Join(tmpDir, filepath.FromSlash(manifest.Store.File))
		dbPath := filepath.Join(dataDir, "olivares.db")
		if err := dr.CopyFile(snapshotPath, dbPath); err != nil {
			fail(fmt.Errorf("restore store: %w", err))
			return
		}
	} else {
		fail(fmt.Errorf("engine %q not supported for web restore (use CLI for postgres)", svc.cfg.EngineKind))
		return
	}

	update("promoted", 90)

	// The bundle was proven continuity-safe in scratch before promotion; the RUNNING
	// engine still holds the old store open, so a restart is required to load the
	// restored one (the live swap is not hot-reloaded by design).
	s.log.Warn("dr: restore verified in scratch and promoted — restart the engine to load the restored store",
		"job", jobID, "actor", actor, "engine", manifest.EngineKind)

	svc.jobs.update(jobID, func(j *drJob) {
		j.Status = drJobCompleted
		j.Phase = "complete"
		j.Progress = 100
		j.DoneAt = time.Now().UTC().Format(time.RFC3339)
	})
}

func (s *Server) handleDRJobStream(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
	if s.drSvc == nil {
		s.writeError(w, r, errDRUnavailable)
		return
	}

	jobID := chi.URLParam(r, "id")
	if jobID == "" {
		s.badRequest(w, r, "job id required")
		return
	}

	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
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

func (s *Server) handleGetDRSchedule(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
	if s.drSvc == nil {
		s.writeError(w, r, errDRUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, s.drScheduleView())
}

func (s *Server) handlePutDRSchedule(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authzSystem(w, r, "system:admin")
	if !ok {
		return
	}
	if s.drSvc == nil {
		s.writeError(w, r, errDRUnavailable)
		return
	}

	var req drScheduleRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.badRequest(w, r, "invalid request body")
		return
	}
	req.Cron = strings.TrimSpace(req.Cron)
	if req.Retain < 0 {
		s.badRequest(w, r, "retain_days must be >= 0")
		return
	}
	// Validate the cron spec up front — an enabled schedule with a spec the
	// runner cannot parse would be configuration without execution.
	if req.Cron != "" {
		if _, err := parseDRCron(req.Cron); err != nil {
			s.badRequest(w, r, "invalid cron: "+err.Error())
			return
		}
	}
	if req.Enabled && req.Cron == "" {
		s.badRequest(w, r, "an enabled schedule needs a cron expression")
		return
	}

	// Persist FIRST (fail closed): if the estate cannot store the config — the
	// dual-control restore gate included — the in-memory state must not start
	// telling the console a lie that a restart would expose.
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

// applyDualControlRequest folds a PUT's dual-control intent into next under the
// "strengthen now, weaken later" rule, and reports what it did (for the trail).
//
// The three intents are deliberately distinct, and the third is the one that used
// to be a bypass in its own right:
//
//   - want == nil — the caller did not mention the gate. Leave it, AND leave any
//     pending disarm, completely alone. The handler used to decode the request into
//     the same struct it stores, so a retention edit that never named the gate
//     decoded Go's zero value (false) and disarmed it.
//   - *want — arm. Immediate, and it CANCELS a disarm in flight: that is how a
//     second admin who sees the pending disarm countermands it. Arming is also what
//     SPENDS the provenance, so DisarmBy is cleared here and nowhere else.
//   - !*want — disarm. If the gate is not actually armed there is nothing to
//     protect, so the state simply normalises to off. If it IS armed, the request
//     is RECORDED — with the instant it takes effect and WHO asked — and the gate
//     keeps holding until then. Re-asking is idempotent: an existing pending
//     instant is never moved, so repeating the request cannot bring it closer (nor
//     push it away), and it cannot change whose disarm it is either.
//
// account is the requester's stable user. The one place it must NOT be forgotten is
// the collapse of a spent instant: that branch clears DisarmAt because the config
// should stop carrying a fired timer, and clearing DisarmBy alongside it would make
// "wait for the disarm, then ask once more" a complete bypass of the rule the field
// exists for.
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

// drScheduleView is the wire shape of the schedule: the stored config plus the
// DERIVED next-run instant (never persisted — it is a function of the cron).
//
// It also reports the dual-control gate as the console must read it — its
// EFFECTIVE state, not the stored bool. While a disarm is pending the gate is
// still armed, and the response says so AND carries the instant it stops being
// armed; once that instant has passed the gate reads off and the spent instant is
// dropped. Reporting the stored bool instead would tell an operator the gate was
// off while their restores were still being held for a second approver.
func (s *Server) drScheduleView() drSchedule {
	now := s.clock.Now().Time().UTC()
	sched := s.drSvc.scheduleSnapshot()
	sched.NextRun = ""
	if sched.Enabled && sched.Cron != "" {
		if spec, err := parseDRCron(sched.Cron); err == nil {
			if next, ok := spec.nextAfter(s.clock.Now().Time()); ok {
				sched.NextRun = next.UTC().Format(time.RFC3339)
			}
		}
	}
	// Order matters: both answers are functions of DisarmAt, so compute them BEFORE
	// clearing a spent instant. Clearing first makes a stored `RequireDualControl:
	// true` with an ELAPSED disarm read as armed forever — the cool-down would never
	// end and a solo operator would be locked out by the very field meant to prevent
	// that.
	armed, pending := sched.dualControlArmed(now), sched.disarmPending(now)
	if !pending {
		sched.DisarmAt = ""
	}
	sched.RequireDualControl = armed
	return sched
}

// handleListPendingRestores lists restore requests awaiting a second approver, so a
// distinct admin can find and approve one (dual-control).
func (s *Server) handleListPendingRestores(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
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

func (s *Server) handleListDRJobs(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
	if s.drSvc == nil {
		s.writeError(w, r, errDRUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.drSvc.jobs.list()})
}
