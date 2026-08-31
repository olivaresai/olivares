// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	payloadHash := runEventPayloadHashWithWorkGeneration(
		in.runRef, seq, in.event, in.fromState, in.toState, in.detail, atTS, in.workGeneration,
	)

	// 1) Seal in the global hash-chained audit ledger (anchored by PayloadHash).
	meta := map[string]any{"run_ref": in.runRef, "event": in.event, "to_state": in.toState}
	if in.workGeneration != nil {
		meta[colEvWorkItemID] = in.workGeneration.itemID.String()
		meta[colEvWorkSID] = in.workGeneration.holderSID
		meta[colEvWorkFence] = in.workGeneration.fence
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
	h := sha256.New()
	parts := []string{
		runRef, strconv.FormatInt(seq, 10), event, from, to, detail, atTS,
	}
	if workGeneration != nil {
		parts = append(parts,
			workGeneration.itemID.String(),
			workGeneration.holderSID,
			strconv.FormatInt(workGeneration.fence, 10),
		)
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
