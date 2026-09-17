// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The correction batch the independent RETURN required. Each case exists because the
// first delivery asserted something weaker than it claimed.

// Frozen fixture identifiers. The previous vectors used model.NewID(), so they were
// recomputed on every run and could not have caught a change in the layout — they only
// checked that two implementations agreed today. These are literals, and so are the
// digests they must produce.
const (
	frozenRunRef   = "run-frozen-1"
	frozenSeq      = int64(7)
	frozenEvent    = "stopped"
	frozenFrom     = "running"
	frozenTo       = "stopped"
	frozenDetail   = "exit 0"
	frozenAt       = "2026-08-10T10:05:00Z"
	frozenItemID   = "01920000-0000-7000-8000-0000000000aa"
	frozenSID      = "sid-frozen"
	frozenFence    = int64(42)
	frozenLaunchID = "01920000-0000-7000-8000-0000000000bb"

	frozenSevenHex    = "45aa08ae2d571fec9aedcad0ddfe805989c08376e265cb05455669cb3c94bc24"
	frozenTenHex      = "d8da005f598e1d63db687cfed59a67b205ef0b26dda6ddc1cd3381f88ae06c5e"
	frozenFourteenHex = "e5b6b94601d35bcdf0251e75883c56f48a7be67d62c577f3591be020eb82451e"
	frozenWorkHex     = "b0e5a55cf8676789eaeadbc9f467b2a8bde8080e3652f6367084e0d8b68e4757"
	frozenLegacyHex   = "9406dad16c7a1aa203ff32cbcc6045a642d5542de191be96d045d38ed5321835"
)

// R4: literal vectors. If the layout ever changes, these fail; a self-comparing pair
// would not.
func TestTerminalEvidenceFrozenVectors(t *testing.T) {
	work := &runtimeWorkGeneration{
		itemID: model.ID(frozenItemID), holderSID: frozenSID, fence: frozenFence,
	}
	evidence := &runtimeTerminalEvidence{
		observation: obsProcessExitObserved, launchID: model.ID(frozenLaunchID),
	}
	legacy := &runtimeTerminalEvidence{observation: obsHandleLostUnconfirmed}

	for _, tc := range []struct {
		name string
		got  [32]byte
		want string
	}{
		{"seven", runEventPayloadHash(frozenRunRef, frozenSeq, frozenEvent, frozenFrom,
			frozenTo, frozenDetail, frozenAt), frozenSevenHex},
		{"ten", runEventPayloadHashWithWorkGeneration(frozenRunRef, frozenSeq, frozenEvent,
			frozenFrom, frozenTo, frozenDetail, frozenAt, work), frozenTenHex},
		{"fourteen", runEventPayloadHashWithTerminalEvidence(frozenRunRef, frozenSeq,
			frozenEvent, frozenFrom, frozenTo, frozenDetail, frozenAt, nil, evidence),
			frozenFourteenHex},
		{"fourteen with work", runEventPayloadHashWithTerminalEvidence(frozenRunRef,
			frozenSeq, frozenEvent, frozenFrom, frozenTo, frozenDetail, frozenAt, work,
			evidence), frozenWorkHex},
		{"fourteen legacy unbound", runEventPayloadHashWithTerminalEvidence(frozenRunRef,
			frozenSeq, frozenEvent, frozenFrom, frozenTo, frozenDetail, frozenAt, nil,
			legacy), frozenLegacyHex},
	} {
		if got := hex.EncodeToString(tc.got[:]); got != tc.want {
			t.Errorf("%s digest:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}
}

// verifiedAuditAnchor reads the exact event the projection's audit_seq names, through
// the store's OWN verified-anchor capability (store.VerifiedAuditAnchorReader,
// core/store/audit.go:197-209, implemented at sqlstore/audit.go:750-821). That reader
// loads the event and its predecessor in one snapshot, recomputes both canonical
// hashes, verifies the link, and returns the STORED CANONICAL METADATA.
//
// This is why the earlier conditional assertion is gone. AuditEvent.Meta is nil on
// every read path by contract (core/model/audit.go:40-46) and stays nil here; the
// canonical string is the authoritative form, and it is available without weakening
// the audit store or adding a production reader. A missing capability, a missing
// anchor, or metadata that will not decode is a hard failure, not a skip.
func verifiedAuditAnchor(
	t *testing.T, st store.Store, tenant model.TenantID, seq int64,
) (model.AuditEvent, map[string]any) {
	t.Helper()
	if seq == 0 {
		t.Fatal("audit_seq is 0: the projection has no accepted anchor. The strict " +
			"fixture requires one; the degraded policy is a separate, unchanged case")
	}
	var (
		event     model.AuditEvent
		canonical string
		found     bool
	)
	// Reported through the error, not t.Fatal: a Goexit from inside a store
	// transaction callback would unwind the transaction from under the store.
	errNoAnchorCapability := errors.New(
		"this audit log does not implement store.VerifiedAuditAnchorReader; the " +
			"canonical metadata assertion cannot be made and must not be skipped")
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		reader, ok := sc.Audit().(store.VerifiedAuditAnchorReader)
		if !ok {
			return errNoAnchorCapability
		}
		var err error
		event, canonical, found, err = reader.ReadVerifiedAuditAnchor(context.Background(), seq)
		return err
	}); err != nil {
		t.Fatalf("read verified audit anchor at seq %d: %v", seq, err)
	}
	if !found {
		t.Fatalf("no verified audit anchor at seq %d", seq)
	}
	if event.Seq != seq {
		t.Fatalf("the verified anchor is seq %d, want %d", event.Seq, seq)
	}
	if event.Meta != nil {
		t.Fatal("AuditEvent.Meta must stay nil on a read path; the canonical string is " +
			"the authoritative form")
	}
	if canonical == "" {
		t.Fatal("the verified anchor carries no canonical metadata")
	}
	meta := map[string]any{}
	if err := json.Unmarshal([]byte(canonical), &meta); err != nil {
		t.Fatalf("canonical metadata does not decode: %v", err)
	}
	return event, meta
}

// metaString reads one required canonical metadata field, failing when it is absent.
func metaString(t *testing.T, meta map[string]any, key string) string {
	t.Helper()
	raw, ok := meta[key]
	if !ok {
		t.Fatalf("canonical metadata has no %q", key)
	}
	text, ok := raw.(string)
	if !ok {
		t.Fatalf("canonical metadata %q is %T, want string", key, raw)
	}
	return text
}

// R4: the audit entry the projection points at must carry the SAME bytes and the same
// canonical id and observation in its metadata — and none of the Wait error text.
func TestTerminalEvidenceAuditAnchorMatchesTheProjection(t *testing.T) {
	fr := &fakeRunner{}
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ref, launchID := launchedRun(t, m, tenant)
	fr.lastProc().finish(0)
	waitFor(t, "terminal state", func() bool {
		d, _ := m.getRun(context.Background(), tenant, ref)
		return d.State == stateStopped
	})

	ev, _ := terminalEventOf(t, m, st, tenant, ref)
	want := independentPayloadHash(
		"olv.sessions.run_event.terminal.v1",
		ref, strconv.FormatInt(ev.Seq, 10), ev.Event, ev.FromState, ev.ToState, ev.Detail, ev.At,
		"0", "", "", "",
		launchID, obsProcessExitObserved,
	)
	if ev.PayloadHash != want {
		t.Fatalf("projection payload_hash:\n got %s\nwant %s", ev.PayloadHash, want)
	}
	audit, meta := verifiedAuditAnchor(t, st, tenant, ev.AuditSeq)
	wantBytes, err := hex.DecodeString(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(audit.PayloadHash, wantBytes) {
		t.Fatalf("audit PayloadHash:\n got %x\nwant %s", audit.PayloadHash, want)
	}
	// The canonical metadata, asserted exactly. No conditional, no "commitment is
	// nonempty" stand-in: these are the values the append path committed to.
	if got := metaString(t, meta, colEvRetiredLaunchID); got != launchID {
		t.Fatalf("canonical metadata retired id = %q, want %q", got, launchID)
	}
	if got := metaString(t, meta, colEvTerminalObservation); got != obsProcessExitObserved {
		t.Fatalf("canonical metadata observation = %q, want %q",
			got, obsProcessExitObserved)
	}
	if got := metaString(t, meta, "run_ref"); got != ref {
		t.Fatalf("canonical metadata run_ref = %q, want %q", got, ref)
	}
	if audit.Action != "sessions.run."+ev.Event {
		t.Fatalf("audit action = %q, want %q", audit.Action, "sessions.run."+ev.Event)
	}
}

// R4: the exact preserved outcome for an unrequested (-1, error) Wait, and the error
// text absent from the audit metadata as well as from the row.
func TestTerminalEvidenceWaitErrorKeepsTheExactFailedOutcome(t *testing.T) {
	marker := "boom-marker-7c31"
	wr := &waitErrRunner{err: errors.New(marker)}
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(wr), WithCredentialSource(staticCred()))
	ref, launchID := launchedRun(t, m, tenant)

	wr.last().finish()
	waitFor(t, "terminal state", func() bool {
		d, _ := m.getRun(context.Background(), tenant, ref)
		return d.State == stateFailed || d.State == stateStopped
	})

	ev, rec := terminalEventOf(t, m, st, tenant, ref)
	// An unrequested exit of -1 is a failure, not a stop. That is the existing
	// behavior and P1 does not move it.
	if rec.String(colState) != stateFailed {
		t.Fatalf("state = %q, want %q", rec.String(colState), stateFailed)
	}
	if ev.Event != "failed" || ev.ToState != stateFailed {
		t.Fatalf("event/to_state = %q/%q, want failed/failed", ev.Event, ev.ToState)
	}
	if rec.Int(colExitCode) != -1 {
		t.Fatalf("exit_code = %d, want -1", rec.Int(colExitCode))
	}
	if ev.Detail != "exit -1" {
		t.Fatalf("detail = %q, want %q", ev.Detail, "exit -1")
	}
	if ev.TerminalObservation != obsProcessWaitUnverified {
		t.Fatalf("observation = %q", ev.TerminalObservation)
	}
	if ev.RetiredRuntimeLaunchID != launchID {
		t.Fatalf("retired id = %q, want %q", ev.RetiredRuntimeLaunchID, launchID)
	}
	audit, meta := verifiedAuditAnchor(t, st, tenant, ev.AuditSeq)
	if len(audit.PayloadHash) != 32 {
		t.Fatalf("audit PayloadHash is %d bytes", len(audit.PayloadHash))
	}
	// The STORED canonical metadata, not a map that is nil by contract. Every value
	// is scanned for the marker, and the observation is asserted positively so the
	// scan cannot pass by finding nothing at all.
	for key, value := range meta {
		text, ok := value.(string)
		if !ok {
			continue
		}
		if strings.Contains(text, marker) {
			t.Fatalf("the Wait error leaked into canonical metadata %q: %q", key, text)
		}
	}
	if got := metaString(t, meta, colEvTerminalObservation); got != obsProcessWaitUnverified {
		t.Fatalf("canonical metadata observation = %q, want %q",
			got, obsProcessWaitUnverified)
	}
	if got := metaString(t, meta, colEvRetiredLaunchID); got != launchID {
		t.Fatalf("canonical metadata retired id = %q, want %q", got, launchID)
	}
	if strings.Contains(ev.Detail+rec.String(colReason), marker) {
		t.Fatal("the Wait error leaked into a stored field")
	}
}

// conflictOnceData forces exactly one store.ErrConflict, so `transition`'s single
// retry is really taken. Test-only wrapping of the module data seam; no production
// change and no core-store change.
type conflictOnceData struct {
	api.ModuleData
	fired    bool
	attempts int
}

func (d *conflictOnceData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	d.attempts++
	if !d.fired {
		d.fired = true
		// Run the attempt for real, then roll it back with a conflict, exactly as a
		// lost CAS would: the closure's work must not survive.
		_ = d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
			_ = fn(sc)
			return store.ErrConflict
		})
		return store.ErrConflict
	}
	return d.ModuleData.Mutate(ctx, tenant, fn)
}

// R3: only the winning attempt survives, and it carries the generation it actually
// read inside that attempt.
func TestTerminalEvidenceForcedRetryKeepsOnlyTheWinningAttempt(t *testing.T) {
	fr := &fakeRunner{}
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	ref, launchID := launchedRun(t, m, tenant)
	before := listRunEvents(t, st, tenant, ref)

	forced := &conflictOnceData{ModuleData: m.data}
	m.UseData(forced)
	t.Cleanup(func() { m.UseData(forced.ModuleData) })

	if _, err := m.transition(ctx, tenant, ref, transitionInput{
		event: "stopped", toState: stateStopped, detail: "exit 0",
		actor: "user:u1", actorKind: model.ActorUser,
		terminalObservation: obsProcessExitObserved,
		mutate:              func(rec model.Record) { rec[colRuntimeLaunchID] = nil },
	}); err != nil {
		t.Fatalf("transition: %v", err)
	}
	if forced.attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (one conflict, one win)", forced.attempts)
	}

	m.UseData(forced.ModuleData)
	after := listRunEvents(t, st, tenant, ref)
	if len(after) != len(before)+1 {
		t.Fatalf("events %d -> %d: the losing attempt was retained", len(before), len(after))
	}
	last := after[len(after)-1]
	if last.TerminalObservation != obsProcessExitObserved || last.RetiredRuntimeLaunchID != launchID {
		t.Fatalf("the surviving event carries the wrong generation: %+v", last)
	}
	if last.Seq != before[len(before)-1].Seq+1 {
		t.Fatalf("sequence %d does not follow %d", last.Seq, before[len(before)-1].Seq)
	}
	rec, err := m.loadRun(ctx, tenant, ref)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Int(colLastEventSeq) != last.Seq {
		t.Fatalf("last_event_seq = %d, want %d", rec.Int(colLastEventSeq), last.Seq)
	}
}

// R3: the append boundary itself, reached with values a producer would never build,
// inside a transaction that also mutates the run row. Nothing may survive.
func TestTerminalEvidenceAppendBoundaryRejectsAndRollsBack(t *testing.T) {
	for _, tc := range []struct {
		name     string
		evidence *runtimeTerminalEvidence
		event    string
		toState  string
	}{
		{"malformed uuid", &runtimeTerminalEvidence{
			observation: obsProcessExitObserved, launchID: model.ID("not-a-uuid")},
			"stopped", stateStopped},
		{"process observation without an id", &runtimeTerminalEvidence{
			observation: obsProcessExitObserved}, "stopped", stateStopped},
		{"id without a kind", &runtimeTerminalEvidence{
			launchID: model.ID(frozenLaunchID)}, "stopped", stateStopped},
		{"unknown kind", &runtimeTerminalEvidence{
			observation: "process_probably_died", launchID: model.ID(frozenLaunchID)},
			"stopped", stateStopped},
		{"complete evidence on a non-terminal pair", &runtimeTerminalEvidence{
			observation: obsProcessExitObserved, launchID: model.ID(frozenLaunchID)},
			"stopping", stateRunning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeRunner{}
			m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
			ctx := context.Background()
			ref, _ := launchedRun(t, m, tenant)

			beforeRun, err := m.loadRun(ctx, tenant, ref)
			if err != nil {
				t.Fatal(err)
			}
			beforeEvents := listRunEvents(t, st, tenant, ref)
			beforeAudit := auditHead(t, st, tenant)

			err = m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
				repo, rerr := sc.Ext(runKind)
				if rerr != nil {
					return rerr
				}
				rec, rerr := findRunRec(ctx, repo, ref)
				if rerr != nil {
					return rerr
				}
				// A real state mutation in the same transaction, so the rollback has
				// something to undo.
				rec[colState] = tc.toState
				rec[colReason] = "boundary probe"
				if _, uerr := repo.Update(ctx, rec); uerr != nil {
					return uerr
				}
				_, aerr := appendRunEvent(ctx, sc, runEventInput{
					runID: model.ID(rec.String(model.ColID)), runRef: ref,
					event: tc.event, fromState: beforeRun.String(colState), toState: tc.toState,
					detail: "boundary probe", actor: "user:u1", actorKind: model.ActorUser,
					at: m.now(), terminalEvidence: tc.evidence,
				})
				return aerr
			})
			if !errors.Is(err, errInvalidTerminalEvidence) {
				t.Fatalf("err = %v, want errInvalidTerminalEvidence", err)
			}

			afterRun, err := m.loadRun(ctx, tenant, ref)
			if err != nil {
				t.Fatal(err)
			}
			if afterRun.String(colState) != beforeRun.String(colState) ||
				afterRun.String(colReason) != beforeRun.String(colReason) {
				t.Fatalf("the run row survived a rolled-back transaction: %q/%q",
					afterRun.String(colState), afterRun.String(colReason))
			}
			if got := listRunEvents(t, st, tenant, ref); len(got) != len(beforeEvents) {
				t.Fatalf("projection grew by %d", len(got)-len(beforeEvents))
			}
			if got := auditHead(t, st, tenant); got != beforeAudit {
				t.Fatalf("the audit chain advanced %d -> %d on a refused append",
					beforeAudit, got)
			}
		})
	}
}

func auditHead(t *testing.T, st store.Store, tenant model.TenantID) int64 {
	t.Helper()
	var seq int64
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		head, ok, err := sc.Audit().Head(context.Background())
		if err != nil || !ok {
			return err
		}
		seq = head.Seq
		return nil
	}); err != nil {
		t.Fatalf("audit head: %v", err)
	}
	return seq
}
