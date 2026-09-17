// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

import (
	"strings"

	"github.com/olivaresai/olivares/sdk"
)

// accessevidence.go — the DURABLE aggregates behind the v26.9 access-evidence
// contract: a retained policy artifact, an authority transition, an observed
// action stage and an authorization decision.
//
// # Why these are four entities and not one
//
// Versioned policy, observed activity and the authorization decision are kept
// SEPARATE on purpose (ARCHITECTURE.md and the v26.9 contract). AccessEdge — which
// this file does not touch — is an accumulating projection: its Permitted flag
// means "known to be permitted", its counter grows on every redelivery, and it
// carries neither the revision that was evaluated nor the decision that was
// taken. That is a fine inventory and a poor history. These four relations are
// the history: each row is an immutable fact with its own provenance, and the
// map stays a projection over them rather than becoming a second authority.
//
// # A stored record is DATA
//
// None of these types is a witness, a receipt or a capability, and none of them
// gains authority by being persisted. A stored AuthorizationDecision with
// outcome allow is a historical fact about an evaluator: re-running the effect
// still needs current authority and the evidence-or-refuse law (sdk/evidence.go)
// unchanged. Likewise persistence is not execution — an ActionObservation
// records what a boundary declared it saw, at the stage and confirmation level
// it could actually attest.
//
// # Split between the DTO and the aggregate
//
// The normative CONTENT of each record is the versioned sdk exchange shape,
// stored verbatim so its canonical digest survives the round trip. Everything a
// producer must not assert is on the aggregate instead, assigned by the engine:
// the durable id, the tenant, the arrival instant, the record digest, the ledger
// anchor, and the tenant-scoped ids the store RESOLVED from the content's
// references. A DTO is therefore never a durable aggregate — it is the half a
// producer is allowed to author.

// The access-evidence entity kinds. They are declared here (rather than only in
// the sqlstore catalog) because they are also the audit TargetKind of the ledger
// event each append anchors, and one spelling is what keeps the two agreeing.
const (
	// PolicyArtifactKind is the immutable policy artifact record.
	PolicyArtifactKind Kind = "core.policy_artifact"
	// AuthorityTransitionKind is the append-only authority transition record.
	AuthorityTransitionKind Kind = "core.authority_transition"
	// ActionObservationKind is the append-only observed action stage.
	ActionObservationKind Kind = "core.action_observation"
	// AuthorizationDecisionKind is the append-only authorization decision.
	AuthorizationDecisionKind Kind = "core.authorization_decision"
)

// MaxPolicyArtifactBytes bounds a RETAINED normative artifact. A larger
// document is REFUSED, never truncated: a silently shortened policy would hash
// to something the issuer never published and would re-evaluate to a different
// answer, which is the opposite of evidence. A producer holding a larger
// artifact records it in a protected store and retains the reference, with its
// availability stated (sdk.AvailabilityExternalRetained).
//
// 256 KiB is the same ceiling the governance revision store applies to an
// authored policy document, chosen for the same reason and kept identical so an
// artifact that fits one path fits the other.
const MaxPolicyArtifactBytes = 256 << 10

// AccessEvidenceMeta is the engine-assigned provenance carried by every
// access-evidence record. A producer supplies the envelope half of it
// (sdk.AccessEvidenceEnvelope); the engine supplies the rest.
//
// The tenant is NOT here: it lives in BaseFields, stamped from the scope. A
// payload that could name its own tenant is a payload that could write into
// another one.
type AccessEvidenceMeta struct {
	// SchemaVersion is the sdk contract version the record was produced under.
	SchemaVersion int64
	// ProducerInstance is the registered instance the host attributed the record
	// to — from the effective configuration and authentication, never from the
	// payload.
	ProducerInstance string
	// SourceEventID is the producer's ORIGINAL event id: the value that makes an
	// at-least-once redelivery recognizable as the same fact.
	SourceEventID string
	// EventType is the family's closed event type. Together with
	// (tenant, producer instance, source event id) it is the idempotency
	// identity, unique at the database level.
	EventType string
	// AdapterVersion is the version of the adapter that produced the record.
	AdapterVersion string
	// OccurredAt is when the fact happened, as the producer attests it. A bus
	// redelivery never overwrites it with the redelivery instant.
	OccurredAt Timestamp
	// RecordedAt is when this engine durably recorded it. It is deliberately
	// OUTSIDE the record digest, so a redelivery of the same fact is a duplicate
	// rather than a conflict.
	RecordedAt Timestamp
	// RecordDigest is the canonical, domain-separated digest of {envelope,
	// content} (sdk.RecordDigest). It is what makes "same identity, different
	// content" decidable instead of a silent overwrite.
	RecordDigest string
	// LedgerRef is the hex chain hash of the tamper-evident ledger event this
	// record's append anchored, committed in the SAME transaction as the row.
	// It is a pointer for verification; it is not a receipt and confers nothing.
	LedgerRef string
}

// Envelope rebuilds the sdk envelope this record was digested under, so a
// reader can recompute RecordDigest without re-deriving the rule.
func (m AccessEvidenceMeta) Envelope() sdk.AccessEvidenceEnvelope {
	return sdk.AccessEvidenceEnvelope{
		SchemaVersion:    int(m.SchemaVersion),
		ProducerInstance: m.ProducerInstance,
		SourceEventID:    m.SourceEventID,
		EventType:        m.EventType,
		AdapterVersion:   m.AdapterVersion,
		OccurredAt:       m.OccurredAt.String(),
	}
}

// AccessEvidenceKey is the tenant-scoped idempotency identity of one record.
type AccessEvidenceKey struct {
	// ProducerInstance is the emitting instance.
	ProducerInstance string
	// SourceEventID is the producer's original event id.
	SourceEventID string
	// EventType is the family's closed event type.
	EventType string
}

// Key returns the record's idempotency identity.
func (m AccessEvidenceMeta) Key() AccessEvidenceKey {
	return AccessEvidenceKey{
		ProducerInstance: m.ProducerInstance,
		SourceEventID:    m.SourceEventID,
		EventType:        m.EventType,
	}
}

// PolicyArtifact is one immutable, tenant-scoped policy artifact record: whose
// authority it claims, which engine evaluates it, what it hashes to and —
// explicitly — whether its rules are actually retained here.
//
// AuthorityID inside the content is the issuer the producer NAMES. It is not
// accepted as proof: a producer may only publish the evidence types and scopes
// it is registered for, and validating an issuer's authority over a scope is
// the trust registry of the next increment. Recording an operator's declaration
// about an AWS policy does not make AWS have applied it.
type PolicyArtifact struct {
	BaseFields
	AccessEvidenceMeta
	// Artifact is the normative content, stored verbatim as the producer
	// canonicalized it.
	Artifact sdk.PolicyArtifactContent
}

// Reconstructible reports whether the normative rules travel WITH this record,
// which is the only state that supports semantic re-evaluation from the store
// alone. Integrity comparison is possible in every state; re-evaluation is not.
func (a PolicyArtifact) Reconstructible() bool {
	return a.Artifact.Availability.LocallyReconstructible()
}

// AuthorityTransition is one append-only authority event over an artifact,
// grant or binding.
type AuthorityTransition struct {
	BaseFields
	AccessEvidenceMeta
	// Transition is the normative content.
	Transition sdk.AuthorityTransitionContent
	// SubjectArtifactID is the tenant-scoped artifact row the store RESOLVED
	// from Transition.SubjectRef when the subject is a policy artifact; zero for
	// grant and binding subjects, whose entities belong to later increments.
	// The store resolves it inside the pinned tenant, so a reference to another
	// tenant's artifact is indistinguishable from an absent one and is refused
	// as a missing dependency rather than silently crossing the boundary.
	SubjectArtifactID ID
}

// ActionObservation is one immutable observed stage of one action.
type ActionObservation struct {
	BaseFields
	AccessEvidenceMeta
	// Observation is the normative content.
	Observation sdk.ActionObservationContent
	// QuestionDigest is the canonical digest of Observation.Question, computed
	// by the store so two records about the same question are joinable without
	// re-deriving the normalization.
	QuestionDigest string
	// ParentObservationID and DecisionID are the tenant-scoped rows the store
	// resolved from the content's ParentRef and DecisionRef; zero when absent.
	ParentObservationID ID
	DecisionID          ID
}

// AuthorizationDecision is one immutable record of what an evaluator decided,
// with the inputs it actually used.
type AuthorizationDecision struct {
	BaseFields
	AccessEvidenceMeta
	// Decision is the normative content.
	Decision sdk.AuthorizationDecisionContent
	// QuestionDigest is the canonical digest of Decision.Question.
	QuestionDigest string
}

// AccessEvidenceCompleteness is the store's own verdict on whether a decision's
// recorded inputs suffice to re-evaluate it. It is computed from the RESOLVED
// dependencies, so it is a fact about the retained data rather than a repeat of
// the producer's claim.
//
// It answers only the reconstructibility question. Integrity of the chain and
// provenance of the producer are separate verdicts belonging to the export and
// verification increments, and are deliberately not folded in here: a decision
// whose inputs are all retained is replayable and may still have arrived from an
// unverified issuer.
type AccessEvidenceCompleteness struct {
	// DecisionID is the decision this verdict is about.
	DecisionID ID
	// Claimed is what the producer declared.
	Claimed sdk.ReplayCompleteness
	// Resolved is what the retained data supports: complete only when every
	// REQUIRED policy-artifact dependency resolves inside the tenant AND is
	// locally reconstructible.
	Resolved sdk.ReplayCompleteness
	// MissingRefs are required dependency references that do not resolve to a
	// stored record in this tenant.
	MissingRefs []string
	// UnavailableRefs are required dependencies that resolve but whose normative
	// content is not retained here — a digest alone, or a reference to a
	// protected store. They are the reason a "complete" claim can be false while
	// nothing is missing.
	UnavailableRefs []string
}

// Reconstructible reports whether the retained data supports re-evaluation.
func (c AccessEvidenceCompleteness) Reconstructible() bool {
	return c.Resolved == sdk.ReplayComplete
}

// Overclaimed reports whether the producer claimed more reconstructibility than
// the retained data supports. The store refuses such a record at write time;
// the predicate exists so a reader of already-stored data can say the same
// thing without re-deriving the rule.
func (c AccessEvidenceCompleteness) Overclaimed() bool {
	return c.Claimed == sdk.ReplayComplete && c.Resolved != sdk.ReplayComplete
}

// GovernanceRevisionRef is the reuse of the local policy-revision identity: the
// (surface, revision) pair that the governance module's own append-only
// uniqueness index is keyed on.
//
// It is a PAIR and never half of one. There is deliberately no database foreign
// key: the revision store is a module entity and the engine may not reference a
// module table, so the pair is recorded and validated as a pair and resolved by
// the module that owns it.
type GovernanceRevisionRef struct {
	// Surface is the authoring surface (e.g. "managed-settings", "cedar").
	Surface string
	// Revision is the monotonic revision number, 1-based.
	Revision int64
}

// Declared reports whether either half is set.
func (r GovernanceRevisionRef) Declared() bool {
	return strings.TrimSpace(r.Surface) != "" || r.Revision != 0
}

// Complete reports whether both halves are set and the number is positive.
func (r GovernanceRevisionRef) Complete() bool {
	return strings.TrimSpace(r.Surface) != "" && r.Revision > 0
}
