// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

// EvidenceOperationState is the durable lifecycle state of one journaled
// external-effect operation (q1, consuming the frozen S5 evidence
// contract in sdk/evidence.go). "claimed" is the only non-terminal state: the
// operation's evidence anchor committed BEFORE its effect, and the effect's
// outcome has not been durably settled yet. The five terminal states are the
// settlement vocabulary of an external effect, mutually exclusive (stage-7
// B-bis split "blocked" so a fetched-then-withheld response is no longer
// claimable as two different truths):
//
//	completed — the effect was emitted and its outcome confirmed;
//	not_sent  — the effect was definitively NOT emitted (refused/failed before
//	            any transport write; retryable only under a NEW operation);
//	unknown   — the dispatch outcome is ambiguous (a transport fault after the
//	            write may or may not have landed). NEVER re-dispatched: the
//	            single-use claim is burned, at-most-once is preserved;
//	blocked   — a policy/authorization decision stopped the effect after the
//	            claim anchored and BEFORE any dispatch: nothing reached the
//	            upstream (for a local effect: the mutation never applied);
//	withheld  — a RESPONSE-RELEASE CHILD operation's terminal state, never a
//	            dispatch parent's: the parent's dispatch was observed (it
//	            settles completed) and a policy/authorization decision then
//	            withheld the observed response from the caller — the round
//	            trip finished, the payload never left governance. The child
//	            operation is derived deterministically from its parent (the
//	            MCP connector's deriveResponseReleaseBinding shape).
//
// The reading rule consumers may rely on: blocked proves nothing reached the
// upstream; completed proves something did — the release disposition of what
// came back lives in the release child, never in the parent's word. A consumer
// that infers "safe to re-attempt under a new operation" may do so from
// not_sent and blocked only — never from completed or withheld, whose dispatch
// already ran.
type EvidenceOperationState string

// The evidence operation states.
const (
	EvidenceOpClaimed   EvidenceOperationState = "claimed"
	EvidenceOpCompleted EvidenceOperationState = "completed"
	EvidenceOpNotSent   EvidenceOperationState = "not_sent"
	EvidenceOpUnknown   EvidenceOperationState = "unknown"
	EvidenceOpBlocked   EvidenceOperationState = "blocked"
	EvidenceOpWithheld  EvidenceOperationState = "withheld"
)

// Valid reports whether s is a known state.
func (s EvidenceOperationState) Valid() bool {
	switch s {
	case EvidenceOpClaimed, EvidenceOpCompleted, EvidenceOpNotSent,
		EvidenceOpUnknown, EvidenceOpBlocked, EvidenceOpWithheld:
		return true
	}
	return false
}

// Terminal reports whether s is a settlement state (valid and not "claimed").
// Only terminal states may be recorded by a settle; "claimed" is written
// exclusively by the claim itself.
func (s EvidenceOperationState) Terminal() bool {
	return s.Valid() && s != EvidenceOpClaimed
}

// EvidenceOperation is one row of the durable evidence operation journal: the
// tenant-scoped, single-use record of ONE governed external effect, written by
// the claim/settle transactions (store.EvidenceOperationRepo). It carries refs
// and digests ONLY — never raw parameters, result bodies, bearer material or
// reason text (docs/SECURITY-HARDENING.md, minimal data): the tamper-evident ledger events the
// refs point at are the evidence, and the digests bind the row to the exact
// effect without disclosing it.
type EvidenceOperation struct {
	BaseFields
	// OperationID is the single-use idempotency identity of exactly one governed
	// effect (sdk.OperationID semantics) — UNIQUE per tenant.
	OperationID string
	// EffectDigest is the opaque, domain-separated digest of the FULL effect
	// binding (sdk.EffectDigest). A replay of OperationID with a different digest
	// is a rebind and is refused.
	EffectDigest string
	// Surface is the PEP surface that claimed the operation (e.g. "mcp.gateway").
	Surface string
	// Action is the governed action verb; the journal appends ".claim"/".settle"
	// to it for the paired ledger events.
	Action string
	// State is the durable lifecycle state.
	State EvidenceOperationState
	// ClaimEvidenceRef is the ledger anchor (hex chain hash) of the claim
	// evidence event committed in the SAME transaction as this row.
	ClaimEvidenceRef string
	// OutcomeEvidenceRef is the ledger anchor of the settlement evidence event;
	// empty while the operation is unsettled.
	OutcomeEvidenceRef string
	// ResultDigest is the opaque digest of the effect's outcome (never the
	// result body); empty until settled, and possibly empty for outcomes that
	// have no result (not_sent, blocked). A withheld settlement normally
	// CARRIES one: a response was observed, and its digest is the durable
	// binding to the exact bytes governance refused to release.
	ResultDigest string
	// DispatchRef is an opaque reference to the dispatch (e.g. an upstream
	// message id) — a ref, never a payload. Empty when nothing was dispatched.
	DispatchRef string
	// LeaderEpoch is the leadership fencing token captured when the claim
	// committed, stamped from the DURABLE fence (store.EpochFencer.FencedEpoch —
	// held lock session + persisted epoch/holder on Postgres). A consumer
	// re-runs store.EvidenceEpochFence immediately before emitting the external
	// effect so a node that lost leadership between claim and dispatch
	// self-fences; the residual check-to-effect window needs receiver-side
	// fencing at the upstream (see EvidenceEpochFence).
	LeaderEpoch uint64
}
