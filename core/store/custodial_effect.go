// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"errors"
	"strings"

	"github.com/olivaresai/olivares/core/model"
)

// custodial_effect.go (P2 / W1) — the engine-owned custodial effect capability.
//
// A custodial effect is ONE journal operation whose claim row is inserted
// BEFORE its ledger event is appended, over a relation the ENGINE fixes rather
// than the caller names. It exists because the managed Stop of a run must burn
// a single-use operation identity even when the evidence append is dropped by a
// degrade-mode spool: the generic EvidenceOperationRepo.Claim appends first and
// therefore leaves no durable trace of a dropped claim, which would let the
// same identity be presented again as if nothing had happened.
//
// What the engine owns, and the caller therefore cannot choose:
//
//   - the SURFACE (ManagedStopSurface) and the ACTION (ManagedStopAction);
//   - the target RELATION — the run and claim entities, their key, generation,
//     lineage and lease-fence columns (the sqlstore relation check);
//   - the CLAIM ROW the touch updates: it is reached from the run proven
//     visible in this transaction, never from a caller-supplied row identity,
//     so a valid-looking foreign claim cannot be substituted;
//   - the EffectDigest, which binds the caller's semantic digest to the
//     PERSISTED lineage of the target;
//   - the leader EPOCH, taken from the store's own elector.
//
// The capability is OPTIONAL on an ordinary Scope: a store that cannot satisfy
// the relation, the schema transition or the durable fence simply does not
// expose a usable binding, and its readiness says which of the three is
// missing. It is never a partial capability: an unavailable engine refuses at
// Bind, before any lock, read or write.
//
// The confined forwarding wrapper (workspace_custodial_effect_ports.go) hands
// its OWN boundary to the binding. The boundary field is unexported, so no
// module can widen, supply or remove it, and the engine — not the caller —
// applies it to the target read.

// ManagedStopSurface is the ONLY custodial surface. It is a constant rather
// than a registration: adding a surface is a ratified contract change, not
// something a module can do by declaring a descriptor.
const ManagedStopSurface = "sessions.managed_stop"

// ManagedStopAction is the governed action verb of the surface. The journal
// appends ".claim"/".settle" to it for the paired ledger events, exactly like
// the generic journal does.
const ManagedStopAction = "sessions.run.stop"

// ManagedStopOperationPrefix partitions the managed-stop journal identities
// away from every other consumer's within a tenant. A bound operation ID is
// this prefix followed by the client operation ID.
const ManagedStopOperationPrefix = ManagedStopSurface + ".v1:"

// ManagedStopReservedPrefix is the wider reservation the GENERIC journal
// producers refuse. It covers ManagedStopOperationPrefix and every future
// version of it, so no generic Claim or Settle — in-tree or through a module —
// can write, replay or settle a managed-stop identity. Readers are deliberately
// untouched: a reserved row still decodes, still reports its state, and a
// refused row still denies closed through its blank claim anchor.
const ManagedStopReservedPrefix = ManagedStopSurface + "."

// ReservedCustodialOperationID reports whether an operation identity belongs to
// a custodial surface and is therefore writable only through the custodial
// handle. It uses the journal's own TrimSpace semantics so a leading space
// cannot smuggle a reserved identity past the generic validators.
func ReservedCustodialOperationID(operationID string) bool {
	return strings.HasPrefix(strings.TrimSpace(operationID), ManagedStopReservedPrefix)
}

// CustodialMode selects which of the handle's operations the binding admits.
// The mode is part of the binding because the pre-Bind checks differ: a Claim
// compares the target generation and qualifies the claim row, a Settle does
// neither (the generation may already be cleared), and a Lookup writes nothing
// and neither restricts nor seals its scope.
type CustodialMode uint8

// The three binding modes.
const (
	// CustodialLookup decodes the bound row and nothing else. It is admitted in
	// a View as well as a Mutate.
	CustodialLookup CustodialMode = iota + 1
	// CustodialClaim admits TouchClaim once and Claim once, in that order.
	CustodialClaim
	// CustodialSettle admits Settle once, in its own transaction.
	CustodialSettle
)

// Valid reports whether m is one of the three modes.
func (m CustodialMode) Valid() bool {
	return m == CustodialLookup || m == CustodialClaim || m == CustodialSettle
}

// String renders the mode for diagnostics.
func (m CustodialMode) String() string {
	switch m {
	case CustodialLookup:
		return "lookup"
	case CustodialClaim:
		return "claim"
	case CustodialSettle:
		return "settle"
	default:
		return "invalid"
	}
}

// CustodialEffectBinding names ONE operation over the engine's fixed relation.
//
// It carries no surface, no target kind, no key column and no claim row
// identity: all five are engine constants, which is what makes substitution
// impossible rather than merely discouraged. RunLaunchID is COMPARED in Claim
// mode and is a digest input in every mode, so a settle presents the same
// generation the claim was bound to even after the live row has cleared it.
type CustodialEffectBinding struct {
	// Mode selects the admitted operations (see CustodialMode).
	Mode CustodialMode
	// OperationID is the journal identity: ManagedStopOperationPrefix followed
	// by the client operation ID.
	OperationID string
	// SemanticDigest is the CALLER's digest of the request semantics. The engine
	// treats it as an opaque string and binds it into its own EffectDigest; it
	// never interprets or recomputes it.
	SemanticDigest string
	// RunRef is the run's natural key within the tenant.
	RunRef string
	// RunLaunchID is the expected runtime launch generation.
	RunLaunchID model.ID
	// ClaimVersion is the version of the claim row the caller observed. Claim
	// mode requires the persisted row to still carry exactly this version; any
	// other value — including a value that is accurate for a DIFFERENT claim
	// row — is a mismatch, because the caller names no row.
	ClaimVersion int64
	// Actor and ActorKind attribute the claim and settlement ledger events.
	Actor, ActorKind string

	// boundary is the confinement the FORWARDING wrapper installs. It is
	// unexported: a module cannot supply, widen or clear it, and an unconfined
	// engine scope leaves it nil, which reads exactly as "this scope has no
	// confinement of its own".
	boundary *workspaceBoundary
}

// confine returns b carrying exactly boundary. It is the only way a boundary
// enters a binding, and it is unexported to package store.
func (b CustodialEffectBinding) confine(boundary workspaceBoundary) CustodialEffectBinding {
	b.boundary = &boundary
	return b
}

// Confined reports whether the binding carries a workspace confinement.
func (b CustodialEffectBinding) Confined() bool { return b.boundary != nil }

// ConfinedWorkspaceID returns the confined workspace, or the zero id when the
// binding carries no confinement. It is a fact for diagnostics and equality
// checks; the predicate itself comes from WorkspaceConstraint.
func (b CustodialEffectBinding) ConfinedWorkspaceID() model.ID {
	if b.boundary == nil {
		return ""
	}
	return b.boundary.id
}

// WorkspaceConstraint returns the forced lineage predicate for the target
// descriptor, mirroring BoundedReadOptions.ExtensionConstraint: confined=false
// means the scope has no boundary and the caller reads unfiltered; a confined
// binding over a descriptor that declares no lineage receives
// ErrWorkspaceLineageRequired and opens no repository.
func (b CustodialEffectBinding) WorkspaceConstraint(
	desc model.EntityDescriptor,
) (filter model.Filter, confined bool, err error) {
	if b.boundary == nil {
		return model.Filter{}, false, nil
	}
	spec := desc.WorkspaceLineage
	if !spec.Declared() {
		return model.Filter{}, true, denied(string(desc.Kind))
	}
	return b.boundary.filterFor(spec), true, nil
}

// WorkspaceOwns reports whether a PERSISTED lineage value is inside the
// binding's boundary. ok=false means the stored value is unreadable for the
// declared encoding, which is a fault and never an unset value: the engine
// denies such a row rather than admitting it. An unconfined binding owns every
// value it can read.
func (b CustodialEffectBinding) WorkspaceOwns(
	desc model.EntityDescriptor, stored string,
) (inside, ok bool) {
	if b.boundary == nil {
		return true, true
	}
	return b.boundary.owns(desc.WorkspaceLineage, stored)
}

// CustodialEffectHandle is one bound operation. It is valid only inside the
// transaction whose Scope produced it and is used sequentially, like every
// other repository the Scope hands out.
//
// Ordering within CustodialClaim is fixed: TouchClaim at most once, then Claim
// exactly once. Claim and Settle SEAL the scope when they return — with a
// result OR an error — so nothing else can be written after the operation's
// identity has been decided.
type CustodialEffectHandle interface {
	// Lookup decodes the bound row. ok=false means the operation has no row
	// yet. It writes nothing and is admitted in every mode.
	Lookup(ctx context.Context) (model.EvidenceOperation, bool, error)
	// TouchClaim advances the qualified claim row's version and updated_at, and
	// assigns no other column. Claim mode only, at most once, after Bind and
	// before Claim. Zero rows affected is ErrConflict and poisons.
	TouchClaim(ctx context.Context) error
	// Claim performs the custodial order: lookup, provisional refused insert,
	// one ledger append, then the positive update under a version CAS with a
	// pre-commit epoch recheck. Claim mode only, once.
	Claim(ctx context.Context) (CustodialClaimResult, error)
	// Settle records the terminal outcome of a claimed operation. Settle mode
	// only, once, in its own transaction.
	Settle(ctx context.Context, s CustodialSettlement) (CustodialSettleResult, error)
}

// CustodialEffectClaimer is the OPTIONAL Scope capability that binds a
// custodial effect. A Scope that does not expose it cannot reach the surface at
// all; a Scope that does still refuses at Bind when the engine is not ready.
type CustodialEffectClaimer interface {
	BindCustodialEffect(ctx context.Context, b CustodialEffectBinding) (CustodialEffectHandle, error)
}

// CustodialClaimOutcome is what one Claim decided.
type CustodialClaimOutcome uint8

// The five claim outcomes.
const (
	// CustodialFreshAnchored: this call inserted the row, appended its claim
	// evidence and promoted the row to claimed. It is the ONLY outcome that
	// authorizes the effect.
	CustodialFreshAnchored CustodialClaimOutcome = iota + 1
	// CustodialRefusedFresh: the append was dropped by a degrade-mode spool
	// (Seq==0). The provisional refused row and the loss accounting COMMIT, the
	// identity is burned, and nothing is dispatched.
	CustodialRefusedFresh
	// CustodialReplayClaimed: an earlier call with the same digest claimed it.
	CustodialReplayClaimed
	// CustodialReplaySettled: an earlier call claimed and settled it.
	CustodialReplaySettled
	// CustodialReplayRefused: an earlier call burned it without claiming.
	CustodialReplayRefused
)

// String renders the outcome for diagnostics and records.
func (o CustodialClaimOutcome) String() string {
	switch o {
	case CustodialFreshAnchored:
		return "fresh_anchored"
	case CustodialRefusedFresh:
		return "refused_fresh"
	case CustodialReplayClaimed:
		return "replay_claimed"
	case CustodialReplaySettled:
		return "replay_settled"
	case CustodialReplayRefused:
		return "replay_refused"
	default:
		return "invalid"
	}
}

// Fresh reports whether THIS call decided the operation. Both fresh outcomes
// are fresh decisions; only CustodialFreshAnchored authorizes an effect.
func (o CustodialClaimOutcome) Fresh() bool {
	return o == CustodialFreshAnchored || o == CustodialRefusedFresh
}

// CustodialClaimResult is the in-transaction outcome of a Claim.
type CustodialClaimResult struct {
	// Outcome is what was decided.
	Outcome CustodialClaimOutcome
	// Op is the journal row. It is the refused row on CustodialRefusedFresh and
	// the recorded row on every replay; it is the zero value only on an error.
	Op model.EvidenceOperation
}

// CustodialSettlement is the terminal outcome of one claimed custodial
// operation. The operation identity and its digest come from the binding, not
// from this value: a settlement cannot name another operation.
type CustodialSettlement struct {
	// State is the terminal settlement state (model.EvidenceOperationState
	// Terminal). refused is not a settlement and is refused here.
	State model.EvidenceOperationState
	// ResultDigest is the opaque digest of the outcome; it may be empty.
	ResultDigest string
	// DispatchRef is the opaque dispatch reference; it may be empty. It is part
	// of settlement identity, exactly as in the generic journal.
	DispatchRef string
}

// CustodialSettleResult is the in-transaction outcome of a Settle.
type CustodialSettleResult struct {
	// Op is the settled row, or the recorded row on an idempotent re-settle.
	Op model.EvidenceOperation
	// Fresh is true when this call recorded the settlement.
	Fresh bool
	// Dropped is true on a degrade-mode Seq==0 drop of the outcome event: the
	// row deliberately STAYS claimed, which is the safe ambiguous shape, since a
	// claimed operation is never re-dispatched.
	Dropped bool
}

// CustodialUnready names WHY the engine cannot bind a custodial effect. The
// three reasons are distinct because their remedies are: a schema transition, a
// module relation, and leadership wiring are three different incidents.
type CustodialUnready string

// The unavailability reasons.
const (
	// CustodyUnreadySchema: the core evidence journal has not crossed the
	// controlled transition that admits the refused state, so the provisional
	// insert could not be written.
	CustodyUnreadySchema CustodialUnready = "schema_unsupported"
	// CustodyUnreadyRelation: the registered run/claim relation is absent or
	// does not match the engine's fixed shape. This is the state of an ordinary
	// build until the run descriptor declares its authorization lineage.
	CustodyUnreadyRelation CustodialUnready = "relation_invalid"
	// CustodyUnreadyLeader: the store's elector does not implement the durable
	// fence, so no epoch can be stamped. Fail closed; the in-memory pair is
	// never a fallback.
	CustodyUnreadyLeader CustodialUnready = "leader_unwired"
)

// CustodialReadiness is the store-level answer to "can a managed Stop bind at
// all?". Detail carries the exact deviation for the operator; it is a
// diagnostic string, never part of a caller's control flow.
type CustodialReadiness struct {
	Ready  bool
	Reason CustodialUnready
	Detail string
}

// CustodialEffectReadiness is the OPTIONAL Store capability that answers the
// readiness question without opening a transaction, so a module can report
// unavailable at boot instead of discovering it on the first request.
type CustodialEffectReadiness interface {
	ManagedStopReadiness(ctx context.Context) CustodialReadiness
}

// Custodial effect sentinels. Callers match them with errors.Is.
//
// POISON is a property of the SCOPE, not of the error: every sentinel below
// that the contract calls poisoned also marks the transaction so a callback
// that swallows it still cannot commit. The error's identity is for classifying
// the refusal, never for deciding whether the transaction may proceed.
var (
	// ErrCustodyUnavailable is the bind-time refusal when the engine cannot
	// offer the capability at all. It wraps nothing about the caller's request;
	// CustodialReadiness names the missing half.
	ErrCustodyUnavailable = errors.New("custodial effect capability unavailable")
	// ErrCustodySchemaUnsupported is ErrCustodyUnavailable's schema half: the
	// evidence journal has not crossed the controlled refused-state transition.
	ErrCustodySchemaUnsupported = errors.New("custodial effect schema transition not completed")
	// ErrCustodyRelationInvalid is ErrCustodyUnavailable's relation half: the
	// registered run/claim descriptors are absent or do not match the engine's
	// fixed relation. No module registration can make them match by renaming.
	ErrCustodyRelationInvalid = errors.New("custodial effect relation invalid")
	// ErrCustodyLeaderUnwired is ErrCustodyUnavailable's fencing half: the
	// elector does not implement EpochFencer, so no epoch can be stamped.
	ErrCustodyLeaderUnwired = errors.New("custodial effect leader fence unwired")
	// ErrCustodyLeaderUnavailable means the durable fence could not be taken
	// right now. The bind refuses; nothing is written.
	ErrCustodyLeaderUnavailable = errors.New("custodial effect leader fence unavailable")
	// ErrCustodyBindingInvalid is an incomplete or malformed binding — a caller
	// bug, refused before any I/O.
	ErrCustodyBindingInvalid = errors.New("custodial effect binding invalid")
	// ErrCustodyAlreadyBound is a SECOND binding in one transaction. The
	// custodial order is defined for exactly one operation per transaction, so
	// the second attempt refuses and poisons.
	ErrCustodyAlreadyBound = errors.New("custodial effect already bound in this transaction")
	// ErrCustodyAlreadyClaimed is a second TouchClaim, Claim or Settle on one
	// handle. It poisons: a caller that reached it has lost track of what it
	// already decided.
	ErrCustodyAlreadyClaimed = errors.New("custodial effect operation already performed")
	// ErrCustodyHandleStale covers two refusals, and nothing is written for
	// either: a handle used after its transaction finished, and an operation
	// invoked on a binding whose MODE does not admit it — TouchClaim or Claim on
	// a Lookup or Settle binding, Settle on a Lookup or Claim binding.
	//
	// Repeating an operation the SAME binding does admit is NOT this error: a
	// second TouchClaim, Claim or Settle is ErrCustodyAlreadyClaimed, and it
	// poisons.
	ErrCustodyHandleStale = errors.New("custodial effect handle is stale")
	// ErrCustodyTargetConcealed means the run is absent, belongs to another
	// tenant, or is outside this scope's confinement. The three are deliberately
	// indistinguishable: telling them apart would be an existence probe.
	ErrCustodyTargetConcealed = errors.New("custodial effect target concealed")
	// ErrCustodyTargetGeneration means the run's persisted launch generation is
	// not the one the binding expects: the target was superseded. It poisons.
	ErrCustodyTargetGeneration = errors.New("custodial effect target generation superseded")
	// ErrCustodyClaimMismatch means the run's admission claim does not qualify:
	// no row, more than one row, a different holder or fence, a non-active
	// state, a lapsed lease, or a version other than the observed one. It
	// poisons. A version that is accurate for another row is a mismatch too,
	// because the caller names no row.
	ErrCustodyClaimMismatch = errors.New("custodial effect claim qualification failed")
	// ErrCustodyWriteSet means a write that is not an engine authority touch was
	// recorded before the binding, or a non-custody write was attempted while
	// the scope was restricted. It poisons.
	ErrCustodyWriteSet = errors.New("custodial effect write set violation")
	// ErrCustodySealed means a write, lock, append or journal call was attempted
	// after the operation was decided. It poisons.
	ErrCustodySealed = errors.New("custodial effect scope is sealed")
	// ErrCustodyEpochChanged means the stamped epoch no longer matches: the
	// cluster moved under this transaction (Claim, pre-commit) or the row was
	// claimed under another leader (Settle). Claim poisons; Settle refuses with
	// no write and does not poison, so the row stays claimed and settleable by
	// an explicit later takeover.
	ErrCustodyEpochChanged = errors.New("custodial effect epoch changed")
)
