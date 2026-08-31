// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The dual-control restore gate was NOMINAL: Made the two-humans comparison
// real, and the gate was still defeated without ever facing it — by the same
// admin, through the same endpoint, with the same permission. PUT the schedule
// with require_dual_control_restore:false and the very next apply runs. Two
// requests, one human, and the disarm PERSISTED across a restart.
//
// The property pinned here is NOT "disarming needs two people" — that is a
// permanent lockout in exactly the disaster the control exists for (the second
// admin unreachable, and the estate can then neither restore nor disarm, ever).
// It is: STRENGTHENING IS IMMEDIATE, WEAKENING IS DELAYED. Arming holds at once;
// a disarm is recorded, stays visible, and does not take effect until the
// cool-down elapses — during which the gate still holds and any admin can
// countermand it by re-arming.

// movableClock is a Clock a test advances by hand, so the cool-down is exercised
// by moving time rather than by sleeping through it.
type movableClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *movableClock) Now() model.Timestamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	return model.NewTimestamp(c.now)
}

func (c *movableClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// drHarnessAt builds a DR harness over a FILE store (so a second harness on the
// same store is a real restart, not a fresh estate) with a clock the test drives.
func drHarnessAt(t *testing.T, start time.Time) (*harness, string, store.Store, string, *movableClock) {
	t.Helper()
	clk := &movableClock{now: start}
	dir := t.TempDir()
	st := openDRStore(t, dir)
	h := newDRHarnessAt(t, dir, st, clk)
	return h, filepath.Join(dir, "backups"), st, dir, clk
}

// newDRHarnessAt is newDRHarness with an injected clock.
func newDRHarnessAt(t *testing.T, dir string, st store.Store, clk model.Clock) *harness {
	t.Helper()
	return newHarnessOpts(t, func(o *api.Options) {
		o.Store = st
		o.Authenticator = auth.NewAuthenticator(st, nil)
		o.DR = &api.DRConfig{DataDir: dir, EngineKind: "sqlite"}
		o.Clock = clk
	})
}

// putSchedule PUTs a schedule body and returns the response.
func putSchedule(t *testing.T, h *harness, admin string, body map[string]any) resp {
	t.Helper()
	return h.do("PUT", "/v1/console/dr/schedule", admin, body, nil)
}

// TestDRDualControlDisarmIsNotImmediate is the reproduction of bypass 1: ONE
// admin turns the two-person gate off and restores alone, through the ordinary
// endpoints, in the same sitting.
func TestDRDualControlDisarmIsNotImmediate(t *testing.T) {
	start := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	h, dir, st, estateDir, clk := drHarnessAt(t, start)
	admin := h.adminLogin()
	enableDualControl(t, h, admin)
	stageUpload(t, dir, "upload-1")

	// Under the armed gate the restore is HELD for a second approver.
	r := h.do("POST", "/v1/console/dr/restore/upload-1/apply", admin, map[string]any{}, nil)
	if r.code != http.StatusAccepted || r.body["awaiting_approval"] != true {
		t.Fatalf("armed gate did not hold the restore: %d %s", r.code, r.raw)
	}

	// The SAME admin now asks to disarm. The request is accepted and RECORDED —
	// but it must not take effect yet, and the response must say so rather than
	// report a gate that is off.
	r = putSchedule(t, h, admin, map[string]any{
		"enabled": false, "cron": "", "retain_days": 7,
		"require_dual_control_restore": false,
	})
	if r.code != http.StatusOK {
		t.Fatalf("disarm request = %d %s, want 200", r.code, r.raw)
	}
	if r.body["require_dual_control_restore"] != true {
		t.Fatalf("BYPASS: one admin disarmed the two-person restore gate in one request: %s", r.raw)
	}
	effAt, _ := r.body["dual_control_disarm_effective_at"].(string)
	if effAt == "" {
		t.Fatalf("a pending disarm must be VISIBLE with the instant it takes effect: %s", r.raw)
	}
	when, err := time.Parse(time.RFC3339, effAt)
	if err != nil {
		t.Fatalf("dual_control_disarm_effective_at %q is not RFC3339: %v", effAt, err)
	}
	if !when.After(start) {
		t.Fatalf("disarm effective at %s is not in the future of %s", when, start)
	}

	// The whole point: the restore is STILL held while the disarm is pending.
	r = h.do("POST", "/v1/console/dr/restore/upload-1/apply", admin, map[string]any{
		"passphrase": "correct horse battery staple",
	}, nil)
	if r.code != http.StatusAccepted || r.body["awaiting_approval"] != true {
		t.Fatalf("BYPASS: the restore ran while the disarm was still pending: %d %s", r.code, r.raw)
	}
	if r.body["job_id"] != nil {
		t.Fatalf("BYPASS: a job started under a pending disarm: %s", r.raw)
	}

	// A RESTART must not skip the cool-down: the pending disarm is part of the
	// persisted config, and the gate is computed from it, not from a timer that
	// died with the process. A second server over the same estate is that restart.
	restarted := newDRHarnessAt(t, estateDir, st, clk)
	r = restarted.do("GET", "/v1/console/dr/schedule", admin, nil, nil)
	if r.body["require_dual_control_restore"] != true {
		t.Fatalf("a restart cleared the armed gate / skipped the cool-down: %s", r.raw)
	}
	if r.body["dual_control_disarm_effective_at"] != effAt {
		t.Fatalf("the pending disarm did not survive a restart: want %q, got %s", effAt, r.raw)
	}

	// Once the cool-down elapses the disarm takes effect for the ESTATE — the gate
	// reads off, and a different admin restores without a second approver. What it
	// never does is authorize the restore of the person who asked for it; that is
	// TestDRDualControlADisarmNeverAuthorisesTheDisarmersOwnRestore, and it is why
	// this test stops here rather than restoring as `admin`.
	clk.advance(2 * time.Hour)
	r = h.do("GET", "/v1/console/dr/schedule", admin, nil, nil)
	if r.body["require_dual_control_restore"] != false {
		t.Fatalf("the disarm never took effect after the cool-down: %s", r.raw)
	}
}

// TestDRDualControlADisarmNeverAuthorisesTheDisarmersOwnRestore is the second half
// of bypass 1, and it is the half the delay did not close.
//
// The version of this suite that shipped in asserted the OPPOSITE at this
// point: after the cool-down it required the SAME admin's solo restore to return a
// job_id. That made the contract "one person with patience", and an external
// contrast said so with the measurement in hand — a delay is not a two-person
// control, it is a one-person control with a wait.
//
// The rule now is that weakening a control never becomes a path to the destructive
// act FOR THE PERSON WHO WEAKENED IT. Waiting buys them nothing at all, so there is
// nothing to wait for.
//
// It is not the two-people-to-disarm rule that was rejected in and the
// rejection still stands: THAT would be a permanent lockout, because with the gate
// armed and the second admin gone the estate could neither restore nor disarm. Here
// the estate is freed the moment the cool-down passes — for everyone except the one
// person whose own act freed it (see the companion test). A genuinely solo operator
// still has the documented host path, `olivares dr restore`, which carries its own
// declared-operator record (cmd/olivares/dr_declaration.go).
func TestDRDualControlADisarmNeverAuthorisesTheDisarmersOwnRestore(t *testing.T) {
	start := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	h, dir, _, _, clk := drHarnessAt(t, start)
	admin := h.adminLogin()
	enableDualControl(t, h, admin)
	stageUpload(t, dir, "upload-1")

	putSchedule(t, h, admin, map[string]any{
		"enabled": false, "cron": "", "retain_days": 7,
		"require_dual_control_restore": false,
	})
	clk.advance(2 * time.Hour)

	// The estate-wide gate is off…
	r := h.do("GET", "/v1/console/dr/schedule", admin, nil, nil)
	if r.body["require_dual_control_restore"] != false {
		t.Fatalf("the disarm never took effect after the cool-down: %s", r.raw)
	}
	// …and it still holds against the person who turned it off.
	r = h.do("POST", "/v1/console/dr/restore/upload-1/apply", admin, map[string]any{
		"passphrase": "correct horse battery staple",
	}, nil)
	if r.body["job_id"] != nil {
		t.Fatalf("BYPASS: one admin disarmed the two-person gate and restored alone by WAITING: %s", r.raw)
	}
	if r.code != http.StatusAccepted || r.body["awaiting_approval"] != true {
		t.Fatalf("the disarmer's own restore must be held for a second approver: %d %s", r.code, r.raw)
	}

	// And the obvious way round it: ask again, so the spent instant is collapsed
	// away and the stored config forgets there ever was a disarm.
	putSchedule(t, h, admin, map[string]any{
		"enabled": false, "cron": "", "retain_days": 7,
		"require_dual_control_restore": false,
	})
	r = h.do("POST", "/v1/console/dr/restore/upload-1/apply", admin, map[string]any{
		"passphrase": "correct horse battery staple",
	}, nil)
	if r.body["job_id"] != nil {
		t.Fatalf("BYPASS: re-asking for the disarm erased who asked for it: %s", r.raw)
	}
}

// TestDRDualControlTheProvenanceFOLLOWSWhoeverAsksLast is the exit from the rule,
// and writing it corrected a claim I had put in a comment without measuring it.
//
// The comment said DisarmBy "is cleared by RE-ARMING", and left the impression that
// re-arming was an escape hatch the disarmer could use on themselves. It is not:
// re-arming does clear the record, and the very next disarm re-records WHOEVER ASKS
// — so an admin who re-arms and disarms again is simply the disarmer again. The
// exit is a SECOND PERSON asking, which is the whole point of a two-person control.
//
// So the property is: the record follows the person who asked LAST. When a
// different admin requests the disarm, the first one is free and the second one is
// held. Without that, "the disarmer is held" would drift into a permanent lockout
// of whoever touched the switch first, which is a defect and not a control.
func TestDRDualControlTheProvenanceFOLLOWSWhoeverAsksLast(t *testing.T) {
	start := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	h, dir, _, _, clk := drHarnessAt(t, start)
	first := h.adminLogin()
	enableDualControl(t, h, first)
	stageUpload(t, dir, "upload-1")
	stageUpload(t, dir, "upload-2")

	r := h.do("POST", "/v1/users", first, map[string]any{
		"email": "second@x.io", "password": "supersecret2", "superadmin": true,
	}, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("create second superadmin = %d %s", r.code, r.raw)
	}
	r = h.do("POST", "/v1/auth/login", "", map[string]any{"email": "second@x.io", "password": "supersecret2"}, nil)
	if r.code != http.StatusOK {
		t.Fatalf("login second admin = %d %s", r.code, r.raw)
	}
	second, _ := r.body["token"].(string)

	disarm := map[string]any{
		"enabled": false, "cron": "", "retain_days": 7,
		"require_dual_control_restore": false,
	}
	putSchedule(t, h, first, disarm)
	clk.advance(2 * time.Hour)

	// Re-arm, then let the OTHER admin take it down. Re-arming alone must clear the
	// record — otherwise the second disarm could not be attributed to its requester.
	r = putSchedule(t, h, second, map[string]any{
		"enabled": false, "cron": "", "retain_days": 7,
		"require_dual_control_restore": true,
	})
	if r.body["dual_control_disarm_requested_by"] != nil {
		t.Fatalf("re-arming did not clear who had asked: %s", r.raw)
	}
	putSchedule(t, h, second, disarm)
	clk.advance(2 * time.Hour)

	// The first admin took no part in THIS disarm, so nothing holds them now.
	r = h.do("POST", "/v1/console/dr/restore/upload-1/apply", first, map[string]any{
		"passphrase": "correct horse battery staple",
	}, nil)
	if r.code != http.StatusAccepted || r.body["job_id"] == nil {
		t.Fatalf("LOCKOUT: an admin was held for a disarm somebody else requested: %d %s", r.code, r.raw)
	}
	// And the one who did ask is the one now held.
	r = h.do("POST", "/v1/console/dr/restore/upload-2/apply", second, map[string]any{
		"passphrase": "correct horse battery staple",
	}, nil)
	if r.body["job_id"] != nil {
		t.Fatalf("BYPASS: the admin who requested THIS disarm restored alone by waiting: %s", r.raw)
	}
}

// TestDRDualControlWhoDisarmedSurvivesARestart pins the persistence of the
// provenance separately from the instant's, because the two fail differently and
// only one of them is loud.
//
// A restart that forgot the INSTANT would either skip the wait or lose the request,
// and the schedule would visibly disagree with itself. A restart that forgot WHO
// asked looks completely normal — the gate reads off, exactly as it should — and
// silently hands the disarmer the bypass the whole design exists to close. So the
// restart is exercised the way the estate does it: a second server over the same
// store, with the state re-read from it.
func TestDRDualControlWhoDisarmedSurvivesARestart(t *testing.T) {
	start := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	h, dir, st, estateDir, clk := drHarnessAt(t, start)
	admin := h.adminLogin()
	enableDualControl(t, h, admin)
	stageUpload(t, dir, "upload-1")

	putSchedule(t, h, admin, map[string]any{
		"enabled": false, "cron": "", "retain_days": 7,
		"require_dual_control_restore": false,
	})
	clk.advance(2 * time.Hour)

	restarted := newDRHarnessAt(t, estateDir, st, clk)
	r := restarted.do("GET", "/v1/console/dr/schedule", admin, nil, nil)
	if r.body["require_dual_control_restore"] != false {
		t.Fatalf("the elapsed disarm did not survive the restart: %s", r.raw)
	}
	if r.body["dual_control_disarm_requested_by"] == nil {
		t.Fatalf("the restart forgot WHO asked for the disarm: %s", r.raw)
	}
	r = restarted.do("POST", "/v1/console/dr/restore/upload-1/apply", admin, map[string]any{
		"passphrase": "correct horse battery staple",
	}, nil)
	if r.body["job_id"] != nil {
		t.Fatalf("BYPASS: a restart freed the person who disarmed the gate: %s", r.raw)
	}
}

// TestDRDualControlADisarmDoesFreeADIFFERENTAdmin is the anti-lockout half, and
// it is what makes the rule above a two-person control rather than a brick: the
// disarm really does take effect for the estate. A second admin — who took no part
// in it — restores alone once the cool-down has passed.
//
// Without this, "the disarmer is still held" would be indistinguishable from "the
// disarm does nothing", and a mutant that simply ignored the elapsed instant would
// pass the test above.
func TestDRDualControlADisarmDoesFreeADIFFERENTAdmin(t *testing.T) {
	start := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	h, dir, _, _, clk := drHarnessAt(t, start)
	admin := h.adminLogin()
	enableDualControl(t, h, admin)
	stageUpload(t, dir, "upload-1")

	r := h.do("POST", "/v1/users", admin, map[string]any{
		"email": "second@x.io", "password": "supersecret2", "superadmin": true,
	}, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("create second superadmin = %d %s", r.code, r.raw)
	}
	r = h.do("POST", "/v1/auth/login", "", map[string]any{"email": "second@x.io", "password": "supersecret2"}, nil)
	if r.code != http.StatusOK {
		t.Fatalf("login second admin = %d %s", r.code, r.raw)
	}
	second, _ := r.body["token"].(string)

	putSchedule(t, h, admin, map[string]any{
		"enabled": false, "cron": "", "retain_days": 7,
		"require_dual_control_restore": false,
	})
	clk.advance(2 * time.Hour)

	r = h.do("POST", "/v1/console/dr/restore/upload-1/apply", second, map[string]any{
		"passphrase": "correct horse battery staple",
	}, nil)
	if r.code != http.StatusAccepted || r.body["job_id"] == nil {
		t.Fatalf("LOCKOUT: an admin who did not disarm anything was still held after the cool-down: %d %s", r.code, r.raw)
	}
}

// TestDRDualControlRearmIsImmediateAndCancelsAPendingDisarm is the other half of
// "strengthen now, weaken later": re-arming must not wait for anything, and it
// must cancel a disarm in flight — that is how a second admin who notices the
// pending disarm countermands it.
func TestDRDualControlRearmIsImmediateAndCancelsAPendingDisarm(t *testing.T) {
	start := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	h, _, _, _, clk := drHarnessAt(t, start)
	admin := h.adminLogin()
	enableDualControl(t, h, admin)

	r := putSchedule(t, h, admin, map[string]any{
		"enabled": false, "cron": "", "retain_days": 7,
		"require_dual_control_restore": false,
	})
	if r.body["dual_control_disarm_effective_at"] == nil {
		t.Fatalf("no pending disarm to cancel: %s", r.raw)
	}

	// Re-arm: immediate, and the pending disarm is gone.
	r = putSchedule(t, h, admin, map[string]any{
		"enabled": false, "cron": "", "retain_days": 7,
		"require_dual_control_restore": true,
	})
	if r.code != http.StatusOK || r.body["require_dual_control_restore"] != true {
		t.Fatalf("re-arm must take effect at once: %d %s", r.code, r.raw)
	}
	if r.body["dual_control_disarm_effective_at"] != nil {
		t.Fatalf("re-arming must cancel the pending disarm, got %s", r.raw)
	}

	// And the cancellation is real: past the original cool-down the gate holds.
	clk.advance(48 * time.Hour)
	r = h.do("GET", "/v1/console/dr/schedule", admin, nil, nil)
	if r.body["require_dual_control_restore"] != true {
		t.Fatalf("a canceled disarm fired anyway: %s", r.raw)
	}
}

// TestDRDualControlRepeatedDisarmDoesNotShortenTheCooldown closes the obvious way
// around a delay: ask again. Re-requesting must be idempotent — it must not
// restart the clock in EITHER direction, and above all must not bring the
// effective instant closer.
func TestDRDualControlRepeatedDisarmDoesNotShortenTheCooldown(t *testing.T) {
	start := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	h, _, _, _, clk := drHarnessAt(t, start)
	admin := h.adminLogin()
	enableDualControl(t, h, admin)

	body := map[string]any{
		"enabled": false, "cron": "", "retain_days": 7,
		"require_dual_control_restore": false,
	}
	first, _ := putSchedule(t, h, admin, body).body["dual_control_disarm_effective_at"].(string)
	if first == "" {
		t.Fatalf("no disarm scheduled")
	}
	clk.advance(30 * time.Minute)
	again, _ := putSchedule(t, h, admin, body).body["dual_control_disarm_effective_at"].(string)
	if again != first {
		t.Fatalf("re-requesting the disarm moved the effective instant: %q -> %q", first, again)
	}
	// Still armed 30 minutes in, whatever was asked in between.
	r := h.do("GET", "/v1/console/dr/schedule", admin, nil, nil)
	if r.body["require_dual_control_restore"] != true {
		t.Fatalf("gate opened early: %s", r.raw)
	}
}

// TestDRScheduleRoundTripCannotNameTheDisarmInstant is the security lens turned on
// the fix itself: the shortest way past a delay is to choose when it ends.
//
// It also pins the round-trip the narrower request type would otherwise have
// broken. decodeJSON sets DisallowUnknownFields, and the previous request type was
// the STORED struct, so a client that read the schedule and PUT the whole object
// back used to work; a type that simply dropped the server-owned fields would 400
// on every such client. Accepted and ignored is the only shape that is both.
func TestDRScheduleRoundTripCannotNameTheDisarmInstant(t *testing.T) {
	start := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	h, _, _, _, clk := drHarnessAt(t, start)
	admin := h.adminLogin()
	enableDualControl(t, h, admin)

	// Ask to disarm, then read the whole object back — the round-trip a client does.
	pending, _ := putSchedule(t, h, admin, map[string]any{
		"enabled": false, "cron": "", "retain_days": 7,
		"require_dual_control_restore": false,
	}).body["dual_control_disarm_effective_at"].(string)
	if pending == "" {
		t.Fatal("no pending disarm to attack")
	}
	got := h.do("GET", "/v1/console/dr/schedule", admin, nil, nil)
	body := map[string]any{}
	for k, v := range got.body {
		body[k] = v
	}
	// The attack: put the instant in the PAST while echoing everything else back.
	body["dual_control_disarm_effective_at"] = "2020-01-01T00:00:00Z"
	body["require_dual_control_restore"] = false

	r := putSchedule(t, h, admin, body)
	if r.code != http.StatusOK {
		t.Fatalf("a full-object round-trip must still be accepted, got %d %s", r.code, r.raw)
	}
	if r.body["require_dual_control_restore"] != true {
		t.Fatalf("BYPASS: a client named its own disarm instant and the gate opened: %s", r.raw)
	}
	if r.body["dual_control_disarm_effective_at"] != pending {
		t.Fatalf("a client moved the pending instant: want %q, got %s", pending, r.raw)
	}
	// And the clock the SERVER set is the one that governs.
	clk.advance(30 * time.Minute)
	if h.do("GET", "/v1/console/dr/schedule", admin, nil, nil).
		body["require_dual_control_restore"] != true {
		t.Fatal("the gate opened before the server's own instant")
	}
}

// TestDRDualControlCorruptDisarmInstantFailsClosedWithoutLockingOut is the
// failure mode found by re-reading the fix rather than by a test: an unreadable
// stored instant (hand-edited or corrupt estate) must keep the gate ARMED, and
// must still be replaceable by a fresh disarm request.
//
// Treating it as a pending disarm instead would arm the gate FOREVER — no request
// could schedule an instant because one would already look scheduled — which is
// the permanent lockout the delayed design exists to avoid. Fail-closed must not
// become fail-shut.
func TestDRDualControlCorruptDisarmInstantFailsClosedWithoutLockingOut(t *testing.T) {
	start := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	st := openDRStore(t, dir)
	clk := &movableClock{now: start}
	h := newDRHarnessAt(t, dir, st, clk)
	admin := h.adminLogin()
	enableDualControl(t, h, admin)

	// Corrupt the stored instant behind the API's back, as a hand-edit would.
	seedCorruptDisarmInstant(t, st, "not-a-timestamp")
	restarted := newDRHarnessAt(t, dir, st, clk)

	r := restarted.do("GET", "/v1/console/dr/schedule", admin, nil, nil)
	if r.body["require_dual_control_restore"] != true {
		t.Fatalf("an unreadable disarm instant opened the gate: %s", r.raw)
	}
	if r.body["dual_control_disarm_effective_at"] != nil {
		t.Fatalf("an unreadable instant was reported as a real pending disarm: %s", r.raw)
	}

	// A fresh disarm request must schedule a REAL instant over the garbage.
	r = putSchedule(t, restarted, admin, map[string]any{
		"enabled": false, "cron": "", "retain_days": 7,
		"require_dual_control_restore": false,
	})
	effAt, _ := r.body["dual_control_disarm_effective_at"].(string)
	if effAt == "" {
		t.Fatalf("LOCKOUT: a corrupt instant made the gate impossible to disarm at all: %s", r.raw)
	}
	if _, err := time.Parse(time.RFC3339, effAt); err != nil {
		t.Fatalf("the replacement instant is not RFC3339 either: %q", effAt)
	}
	clk.advance(2 * time.Hour)
	r = restarted.do("GET", "/v1/console/dr/schedule", admin, nil, nil)
	if r.body["require_dual_control_restore"] != false {
		t.Fatalf("the replacement disarm never took effect: %s", r.raw)
	}
}

// seedCorruptDisarmInstant rewrites the persisted schedule's disarm instant to a
// value the API never writes, simulating a hand-edited estate.
func seedCorruptDisarmInstant(t *testing.T, st store.Store, bad string) {
	t.Helper()
	ctx := t.Context()
	if err := st.Mutate(ctx, model.SystemTenantID, func(sc store.Scope) error {
		org, err := sc.Org(ctx)
		if err != nil {
			return err
		}
		raw, _ := org.Settings["dr.schedule"].(string)
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return err
		}
		m["dual_control_disarm_effective_at"] = bad
		out, err := json.Marshal(m)
		if err != nil {
			return err
		}
		settings := org.Settings
		settings["dr.schedule"] = string(out)
		_, err = sc.SetOrgSettings(ctx, settings)
		return err
	}); err != nil {
		t.Fatalf("seed corrupt disarm instant: %v", err)
	}
}

// TestDRScheduleEditDoesNotDisarmByOmission pins a second, quieter way the gate
// came off: the handler decoded the request into the SAME struct it stores, so a
// PUT that simply does not mention require_dual_control_restore decoded Go's
// zero value — false — and disarmed a gate the caller never asked about. An
// absent field must mean "leave it alone".
func TestDRScheduleEditDoesNotDisarmByOmission(t *testing.T) {
	start := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	h, _, _, _, clk := drHarnessAt(t, start)
	admin := h.adminLogin()
	enableDualControl(t, h, admin)

	// An ordinary retention edit, with no mention of the gate.
	r := putSchedule(t, h, admin, map[string]any{
		"enabled": true, "cron": "0 3 * * *", "retain_days": 30,
	})
	if r.code != http.StatusOK {
		t.Fatalf("schedule edit = %d %s", r.code, r.raw)
	}
	if r.body["require_dual_control_restore"] != true {
		t.Fatalf("an edit that never mentioned the gate turned it off: %s", r.raw)
	}
	if r.body["dual_control_disarm_effective_at"] != nil {
		t.Fatalf("an edit that never mentioned the gate started a disarm clock: %s", r.raw)
	}
	if r.body["retain_days"] != float64(30) {
		t.Fatalf("the edit itself did not apply: %s", r.raw)
	}
	// And it stays armed once the cool-down window would have passed.
	clk.advance(48 * time.Hour)
	r = h.do("GET", "/v1/console/dr/schedule", admin, nil, nil)
	if r.body["require_dual_control_restore"] != true {
		t.Fatalf("a phantom disarm fired later: %s", r.raw)
	}
}
