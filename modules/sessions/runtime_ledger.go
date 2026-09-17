// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The lifecycle ledger (pattern). Each transition is:
//   1. SEALED in the core audit chain via sc.Audit().Append — hash-chained,
//      per-event-signed, immutable/WORM — anchored by a PayloadHash (the strongest
//      tamper-evidence in the product, reused rather than reinvented), and
//   2. PROJECTED into the append-only sessions.run_event entity — queryable per
//      session (the observe surface consume), linked to the global
//      chain by audit_seq.
// Both writes happen in the SAME transaction as the state mutation (the caller's
// ModuleData.Mutate): the ledger is the system of record, so if the seal fails
// the whole transition rolls back — it is NOT best-effort (contrast the observe
// overlay's best-effort stream-open audit).

// errInvalidTerminalEvidence refuses half-evidence. It is returned from inside the
// caller's transaction, so nothing commits: not the row update, not the sequence,
// not the audit append, not the projection.
var errInvalidTerminalEvidence = errors.New("sessions: invalid terminal evidence")

// runEventInput is one lifecycle transition to record.
type runEventInput struct {
	runID     model.ID // the sessions.run row id (the audit target)
	runRef    string
	event     string // created|launched|stopping|stopped|failed|resumed|cleaned
	fromState string
	toState   string
	detail    string // short, non-sensitive
	actor     string
	actorKind string
	at        time.Time
	// workGeneration is present only for K2 fenced runtime-control outcomes.
	workGeneration *runtimeWorkGeneration
	// terminalEvidence is present only on a terminal transition that retires a
	// runtime generation (P1). It is built inside the transaction, from the row as
	// it stood before the mutation cleared the launch id.
	terminalEvidence *runtimeTerminalEvidence
}

// The three observations P1 may record. They are separate values because the
// lifecycle state and the process fact are separate facts: a run row can be
// terminal while nothing confirmed the process died.
const (
	// obsProcessExitObserved: the owned Process.Wait returned a nil error, which
	// under the Process contract (runtime_ports.go:200-201) means that process
	// exited. It says nothing about unrelated descendants or remote hosts.
	obsProcessExitObserved = "process_exit_observed"
	// obsProcessWaitUnverified: Wait returned an error. The terminal state is still
	// recorded, but collection was not confirmed. The error TEXT is never stored.
	obsProcessWaitUnverified = "process_wait_unverified"
	// obsHandleLostUnconfirmed: orphan recovery made the row terminal after losing
	// the handle. reconcileTerminal says so itself and P1 keeps that distinction.
	obsHandleLostUnconfirmed = "handle_lost_unconfirmed"
)

// runtimeTerminalEvidence is one complete observation of a retired generation.
// launchID is the canonical parsed model.ID; it is zero only for a legacy orphan
// recovery whose row never carried a launch reservation.
type runtimeTerminalEvidence struct {
	observation string
	launchID    model.ID
}

// validate refuses half-evidence before either ledger is touched. It runs inside
// the caller's transaction, so a refusal rolls the whole transition back.
//
// THE PARSE HAPPENS HERE, not only in the producer. model.ID is a string type and
// KindUUID is TEXT in both dialects, so nothing below this line would reject
// model.ID("not-a-uuid"): the repo would pass it straight through to SQL and the
// hash would commit to it. A validator that only asks whether the id is EMPTY is
// not validating an id. The canonical parsed form is what the hash, the audit
// metadata and the projection all use, so a valid but non-canonical spelling is
// normalized here rather than stored three different ways.
func (e *runtimeTerminalEvidence) validate() error {
	if !e.launchID.IsZero() {
		parsed, err := model.ParseID(e.launchID.String())
		if err != nil {
			return fmt.Errorf("%w: retired launch id is not a canonical uuid: %w",
				errInvalidTerminalEvidence, err)
		}
		e.launchID = parsed
	}
	switch e.observation {
	case obsProcessExitObserved, obsProcessWaitUnverified:
		if e.launchID.IsZero() {
			return fmt.Errorf("%w: %s requires the retired runtime launch id",
				errInvalidTerminalEvidence, e.observation)
		}
	case obsHandleLostUnconfirmed:
		// An unbound legacy recovery is the ONLY case allowed to omit the id, and it
		// is stored as NULL rather than a zero UUID.
	case "":
		return fmt.Errorf("%w: an observation is required", errInvalidTerminalEvidence)
	default:
		return fmt.Errorf("%w: unknown observation %q",
			errInvalidTerminalEvidence, e.observation)
	}
	return nil
}

// appendRunEvent seals one transition in both ledgers within the caller's
// transaction and returns the per-session sequence number assigned to it (the
// run row stores it as last_event_seq, an O(1) anchor for per-session reads).
func appendRunEvent(ctx context.Context, sc store.Scope, in runEventInput) (int64, error) {
	repo, err := sc.Ext(runEventKind)
	if err != nil {
		return 0, err
	}
	// Next per-session sequence (0-based).
	prior, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{eq(colEvRunRef, in.runRef)},
		Sort:    []model.Sort{{Column: colEvSeq, Desc: true}},
		Limit:   1,
	})
	if err != nil {
		return 0, err
	}
	seq := int64(0)
	if len(prior) > 0 {
		seq = prior[0].Int(colEvSeq) + 1
	}
	atTS := model.NewTimestamp(in.at).String()
	if in.terminalEvidence != nil {
		// Complete evidence is admitted only on a transition that actually retires a
		// generation. `transition` checks this too; the append boundary is the one
		// every writer passes through, so it enforces it as well.
		if !terminalLifecycle(in.event, in.toState) {
			return 0, fmt.Errorf("%w: %s/%s does not retire a generation",
				errInvalidTerminalEvidence, in.event, in.toState)
		}
		if err := in.terminalEvidence.validate(); err != nil {
			return 0, err
		}
	}
	payloadHash := runEventPayloadHashWithTerminalEvidence(
		in.runRef, seq, in.event, in.fromState, in.toState, in.detail, atTS,
		in.workGeneration, in.terminalEvidence,
	)

	// 1) Seal in the global hash-chained audit ledger (anchored by PayloadHash).
	meta := map[string]any{"run_ref": in.runRef, "event": in.event, "to_state": in.toState}
	if in.workGeneration != nil {
		meta[colEvWorkItemID] = in.workGeneration.itemID.String()
		meta[colEvWorkSID] = in.workGeneration.holderSID
		meta[colEvWorkFence] = in.workGeneration.fence
	}
	if in.terminalEvidence != nil {
		meta[colEvTerminalObservation] = in.terminalEvidence.observation
		if !in.terminalEvidence.launchID.IsZero() {
			meta[colEvRetiredLaunchID] = in.terminalEvidence.launchID.String()
		}
	}
	ev, err := sc.Audit().Append(ctx, model.AuditDraft{
		Actor:       orSystem(in.actor),
		ActorKind:   orSystemKind(in.actorKind),
		Action:      "sessions.run." + in.event,
		TargetKind:  runKind,
		TargetID:    in.runID,
		PayloadHash: payloadHash[:],
		// Meta is non-sensitive: run_ref is an opaque UUID we mint (never a subject
		// id / PII — safe in an immutable WORM ledger, docs/SECURITY-HARDENING.md).
		Meta: meta,
	})
	if err != nil {
		return 0, err
	}

	// 2) Project into the queryable per-session append-only ledger.
	row := model.Record{
		colEvRunRef:      in.runRef,
		colEvSeq:         seq,
		colEvAt:          atTS,
		colEvEvent:       in.event,
		colEvPayloadHash: hex.EncodeToString(payloadHash[:]),
		// Seq 0 = evidence dropped under the degrade spool policy (honest zero).
		colEvAuditSeq: ev.Seq,
	}
	setIf(row, colEvFromState, in.fromState)
	setIf(row, colEvToState, in.toState)
	setIf(row, colEvDetail, in.detail)
	setIf(row, colEvActor, in.actor)
	setIf(row, colEvActorKind, in.actorKind)
	if in.workGeneration != nil {
		row[colEvWorkItemID] = in.workGeneration.itemID.String()
		row[colEvWorkSID] = in.workGeneration.holderSID
		row[colEvWorkFence] = in.workGeneration.fence
	}
	if in.terminalEvidence != nil {
		row[colEvTerminalObservation] = in.terminalEvidence.observation
		if !in.terminalEvidence.launchID.IsZero() {
			// The canonical parsed id, the same string the hash and the audit meta
			// committed to. A zero id stays NULL; it is never an empty UUID.
			row[colEvRetiredLaunchID] = in.terminalEvidence.launchID.String()
		}
	}
	if _, err := repo.Create(ctx, row); err != nil {
		return 0, err
	}
	return seq, nil
}

// runEventPayloadHash is the SHA-256 of the canonical, non-sensitive transition
// (the ledger anchor). It commits to the lifecycle facts only — never any
// transcript, prompt, env value or secret.
func runEventPayloadHash(runRef string, seq int64, event, from, to, detail, atTS string) [32]byte {
	return runEventPayloadHashWithWorkGeneration(runRef, seq, event, from, to, detail, atTS, nil)
}

func runEventPayloadHashWithWorkGeneration(
	runRef string,
	seq int64,
	event, from, to, detail, atTS string,
	workGeneration *runtimeWorkGeneration,
) [32]byte {
	return runEventPayloadHashWithTerminalEvidence(
		runRef, seq, event, from, to, detail, atTS, workGeneration, nil,
	)
}

// terminalEvidenceHashVersion is slot 1 of the fourteen-string encoding. It is the
// reason a P1 event can never be confused with a historical seven- or ten-string
// one whatever a caller puts in the other fields: the version is a fixed literal
// this package alone emits, and slot 9 states work presence explicitly instead of
// leaving it to be inferred from how many strings arrived.
const terminalEvidenceHashVersion = "olv.sessions.run_event.terminal.v1"

// runEventPayloadHashWithTerminalEvidence is the ledger anchor.
//
// WITHOUT terminal evidence it is byte-for-byte the original function: seven base
// strings, then the three work strings only when that group is present. Historical
// digests are therefore never rewritten, and the seven-argument helper above still
// produces exactly what it always did.
//
// WITH terminal evidence it is the complete fourteen-string list, every slot always
// present. Both new fields and any concurrent work generation are bound.
func runEventPayloadHashWithTerminalEvidence(
	runRef string,
	seq int64,
	event, from, to, detail, atTS string,
	workGeneration *runtimeWorkGeneration,
	evidence *runtimeTerminalEvidence,
) [32]byte {
	h := sha256.New()
	var parts []string
	if evidence == nil {
		parts = []string{
			runRef, strconv.FormatInt(seq, 10), event, from, to, detail, atTS,
		}
		if workGeneration != nil {
			parts = append(parts,
				workGeneration.itemID.String(),
				workGeneration.holderSID,
				strconv.FormatInt(workGeneration.fence, 10),
			)
		}
	} else {
		workPresent, workItem, workSID, workFence := "0", "", "", ""
		if workGeneration != nil {
			workPresent = "1"
			workItem = workGeneration.itemID.String()
			workSID = workGeneration.holderSID
			workFence = strconv.FormatInt(workGeneration.fence, 10)
		}
		retired := ""
		if !evidence.launchID.IsZero() {
			retired = evidence.launchID.String()
		}
		parts = []string{
			terminalEvidenceHashVersion,
			runRef, strconv.FormatInt(seq, 10), event, from, to, detail, atTS,
			workPresent, workItem, workSID, workFence,
			retired, evidence.observation,
		}
	}
	for _, part := range parts {
		// Length-prefix each field so concatenation is unambiguous (canonical).
		_, _ = h.Write([]byte(strconv.Itoa(len(part))))
		_, _ = h.Write([]byte{':'})
		_, _ = h.Write([]byte(part))
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

func orSystem(actor string) string {
	if actor == "" {
		return model.ActorSystem
	}
	return actor
}

func orSystemKind(kind string) string {
	if kind == "" {
		return model.ActorSystem
	}
	return kind
}
