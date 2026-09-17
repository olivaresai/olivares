// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
)

// accessevidence.go — the engine-side contract of the v26.9 access-evidence
// store: retain a policy artifact, append an authority transition, append an
// observed action stage, append an authorization decision, and read the exact
// separate records back with their dependency/completeness status.
//
// # What this seam is for, and what it refuses to be
//
// It is a NARROW, typed ingest for producers the host has already authenticated
// and registered. It is deliberately NOT a generic document endpoint: there is
// no "accept this JSON as a decision" path, the tenant is stamped from the
// scope rather than read from a payload, and the producer instance is the one
// the host attributed — a payload cannot promote itself to a policy authority
// by writing `origin: local_authoritative`, because origin is a recorded claim
// and the trust registry that validates issuers is the next increment's work.
//
// Nothing here returns an sdk.EvidenceReceipt, an authorization witness or any
// capability, and that absence is pinned by a test rather than by this comment.
// A stored decision is DATA: an effect still needs current authority and the
// unchanged evidence-or-refuse law (sdk/evidence.go). Persisting an observation
// is likewise not proof that anything executed — it records what a boundary
// declared it saw, bounded by what that boundary could actually attest.
//
// # Relationship to the existing journal
//
// This is not a second journal. The durable EvidenceOperation journal
// (evidenceops.go) remains the single authority on governed effects: an
// observation or decision that names an operation must name one the tenant's
// journal already holds, with the SAME effect digest, or it is refused with the
// journal's own ErrEvidenceRebind. The access-evidence tables reference that
// identity; they never create, settle or re-dispatch it, and historical replay
// over them executes nothing.

// Sentinel errors of the access-evidence store. Callers match them with
// errors.Is. Like the journal's sentinels they are refusals of the caller's
// request, not anchoring faults.
var (
	// ErrAccessEvidenceInvalid is an incomplete or self-contradictory record: a
	// caller bug. It is returned BEFORE any store I/O, so a malformed record can
	// never leave a partial trace.
	ErrAccessEvidenceInvalid = errors.New("access evidence input invalid")
	// ErrAccessEvidenceConflict is the same idempotency identity
	// (tenant, producer instance, source event id, event type) carrying
	// DIFFERENT content. Evidence is never silently overwritten: the conflict is
	// reported and the recorded row stands.
	ErrAccessEvidenceConflict = errors.New("access evidence identity conflict (same key, different content)")
	// ErrAccessEvidenceRaced is a concurrent duplicate that lost the unique
	// index. The losing transaction rolls back whole — its ledger append
	// included — and the driver re-reads the committed winner, which resolves to
	// an exact duplicate or to a conflict.
	ErrAccessEvidenceRaced = errors.New("access evidence append raced; retry")
	// ErrAccessEvidenceDependencyMissing is a reference that does not resolve to
	// a record in the PINNED tenant. A reference to another tenant's record is
	// indistinguishable from an absent one by construction, so a crossed-tenant
	// reference lands here rather than crossing the boundary.
	ErrAccessEvidenceDependencyMissing = errors.New("access evidence dependency does not resolve in this tenant")
	// ErrAccessEvidenceOverclaim is a producer declaring more reconstructibility
	// than the retained data supports: replay_completeness "complete" while a
	// REQUIRED policy-artifact dependency is not locally reconstructible. A
	// digest alone does not demonstrate reconstructibility, and the store
	// refuses to record the claim that it does.
	ErrAccessEvidenceOverclaim = errors.New("access evidence replay completeness overclaimed")
	// ErrAccessEvidenceIntegrity is a stored row that contradicts the contract
	// (a record without its ledger anchor, a digest that no longer matches its
	// content). It surfaces on read as well as write: a corrupt row must not
	// decode into a usable fact.
	ErrAccessEvidenceIntegrity = errors.New("access evidence integrity violation")
)

// AccessEvidenceAppend is the provenance half every append carries: the
// producer's envelope plus the ledger attribution of the append itself.
//
// The host fills ProducerInstance from the effective configuration and
// authentication before calling. Actor/ActorKind attribute the ledger event and
// are required for the same reason the journal requires them: an evidence event
// nobody is attributed to is not evidence.
type AccessEvidenceAppend struct {
	// Envelope is the producer's declared identity for this record.
	Envelope sdk.AccessEvidenceEnvelope
	// Actor and ActorKind attribute the ledger event (model.AuditDraft).
	Actor     string
	ActorKind string
}

// PolicyArtifactAppend retains one immutable policy artifact.
type PolicyArtifactAppend struct {
	AccessEvidenceAppend
	// Artifact is the normative content.
	Artifact sdk.PolicyArtifactContent
}

// AuthorityTransitionAppend appends one authority transition.
type AuthorityTransitionAppend struct {
	AccessEvidenceAppend
	// Transition is the normative content.
	Transition sdk.AuthorityTransitionContent
}

// ActionObservationAppend appends one observed action stage.
type ActionObservationAppend struct {
	AccessEvidenceAppend
	// Observation is the normative content.
	Observation sdk.ActionObservationContent
}

// AuthorizationDecisionAppend appends one authorization decision.
type AuthorizationDecisionAppend struct {
	AccessEvidenceAppend
	// Decision is the normative content.
	Decision sdk.AuthorizationDecisionContent
}

// AccessEvidenceWrite is the in-transaction outcome of one append.
//
// The three fields are mutually informative and none is redundant: Fresh says
// THIS call recorded the fact, a non-Fresh non-Dropped result is an exact
// duplicate whose recorded row is returned unchanged, and Dropped is the
// DEGRADE evidence drop in which nothing but the loss accounting was staged.
type AccessEvidenceWrite[T any] struct {
	// Record is the stored row: freshly created, or the recorded row on an
	// exact duplicate. Zero when Dropped.
	Record T
	// Fresh is true when this call created the record.
	Fresh bool
	// Dropped is true on a DEGRADE-mode Seq==0 ledger drop: the loss accounting
	// is staged and NOTHING else is. The caller MUST return nil so the
	// transaction commits (the F9 discipline of sdk/evidence.go), then treat the
	// fact as unrecorded — never as recorded-without-an-anchor.
	Dropped bool
}

// AccessEvidenceRepo is the tenant-pinned access-evidence store, reached from
// Scope.AccessEvidence() like the journal from Scope.EvidenceOperations().
//
// Every append is an IN-TRANSACTION primitive: it appends its ledger event and
// stages its row in the caller's open transaction, so the record and its anchor
// commit atomically or not at all. A rollback therefore leaves no partially
// recorded evidence, which is what makes the commit/rollback acceptance mean
// something.
//
// Idempotency is resolved transactionally and backed by a UNIQUE index on
// (tenant_id, producer_instance, source_event_id, event_type): an identical
// redelivery returns the recorded row and appends nothing (it does not count as
// a second activity), the same identity with different content is
// ErrAccessEvidenceConflict, and a concurrent duplicate that slips past the
// in-transaction read loses the index and is ErrAccessEvidenceRaced.
//
// The reads return the EXACT separate records. They never fuse an observation
// with a decision, never derive a decision from an observation, and never
// answer "would this be permitted now" — that projection belongs to the
// access-map module, over these rows, and remains a projection.
type AccessEvidenceRepo interface {
	// RetainPolicyArtifact records one immutable policy artifact.
	RetainPolicyArtifact(ctx context.Context, in PolicyArtifactAppend) (AccessEvidenceWrite[model.PolicyArtifact], error)
	// AppendAuthorityTransition records one authority transition. A
	// policy-artifact subject must resolve inside the pinned tenant.
	AppendAuthorityTransition(ctx context.Context, in AuthorityTransitionAppend) (AccessEvidenceWrite[model.AuthorityTransition], error)
	// AppendActionObservation records one observed stage. Parent and decision
	// references must resolve inside the pinned tenant; a named operation must
	// exist in the tenant's evidence journal with the same effect digest.
	AppendActionObservation(ctx context.Context, in ActionObservationAppend) (AccessEvidenceWrite[model.ActionObservation], error)
	// AppendAuthorizationDecision records one decision. Required policy-artifact
	// inputs must resolve inside the pinned tenant, and a "complete" replay
	// claim is verified against their actual availability.
	AppendAuthorizationDecision(ctx context.Context, in AuthorizationDecisionAppend) (AccessEvidenceWrite[model.AuthorizationDecision], error)

	// PolicyArtifact returns one artifact by id, or ErrNotFound.
	PolicyArtifact(ctx context.Context, id model.ID) (model.PolicyArtifact, error)
	// AuthorityTransition returns one transition by id, or ErrNotFound.
	AuthorityTransition(ctx context.Context, id model.ID) (model.AuthorityTransition, error)
	// ActionObservation returns one observation by id, or ErrNotFound.
	ActionObservation(ctx context.Context, id model.ID) (model.ActionObservation, error)
	// AuthorizationDecision returns one decision by id, or ErrNotFound.
	AuthorizationDecision(ctx context.Context, id model.ID) (model.AuthorizationDecision, error)

	// AuthorityTransitionsFor returns the transitions recorded for one subject
	// reference, oldest first by the store's ordering. It is the authority
	// HISTORY of that subject and is never collapsed into a current verdict:
	// selecting what is in force today is the next increment's work, and it must
	// consume the real evaluator's selection rather than this list.
	AuthorityTransitionsFor(ctx context.Context, subjectRef string) ([]model.AuthorityTransition, error)
	// ActionObservationsForQuestion returns the observations recorded against
	// one canonical question digest — the separate stages of one action, not a
	// merged verdict about it.
	ActionObservationsForQuestion(ctx context.Context, questionDigest string) ([]model.ActionObservation, error)
	// DecisionCompleteness reports what the RETAINED data supports for one
	// decision, independently of what its producer claimed.
	DecisionCompleteness(ctx context.Context, id model.ID) (model.AccessEvidenceCompleteness, error)
}

// blankAccessField reports an empty or whitespace-only required field, with the
// same TrimSpace semantics the sdk applies to binding identities.
func blankAccessField(s string) bool { return strings.TrimSpace(s) == "" }

// validateAccessAppend checks the provenance half shared by all four families.
// It runs before any store I/O.
//
// An unknown schema version is REFUSED rather than accepted and reinterpreted.
// That is the deny-closed reading of "no permissive unknowns": a record written
// under a contract this build does not implement would be stored with fields
// this build cannot validate, and would then be read back as if it had been.
func validateAccessAppend(a AccessEvidenceAppend, content sdk.AccessEvidenceContent) error {
	switch {
	case content == nil:
		return fmt.Errorf("%w: append: content required", ErrAccessEvidenceInvalid)
	case !a.Envelope.Valid():
		return fmt.Errorf("%w: append: envelope must name producer instance, source event id, event type and occurrence instant",
			ErrAccessEvidenceInvalid)
	case a.Envelope.SchemaVersion != sdk.AccessEvidenceSchemaVersion:
		return fmt.Errorf("%w: append: schema version %d is not the version this engine implements (%d)",
			ErrAccessEvidenceInvalid, a.Envelope.SchemaVersion, sdk.AccessEvidenceSchemaVersion)
	case a.Envelope.EventType != content.AccessEvidenceEventType():
		return fmt.Errorf("%w: append: envelope event type %q does not match content family %q",
			ErrAccessEvidenceInvalid, a.Envelope.EventType, content.AccessEvidenceEventType())
	case blankAccessField(a.Envelope.AdapterVersion):
		return fmt.Errorf("%w: append: adapter version required", ErrAccessEvidenceInvalid)
	case blankAccessField(a.Actor) || blankAccessField(a.ActorKind):
		return fmt.Errorf("%w: append: actor attribution required", ErrAccessEvidenceInvalid)
	}
	if _, err := model.ParseTimestamp(a.Envelope.OccurredAt); err != nil {
		return fmt.Errorf("%w: append: occurrence instant is not canonical timestamp text: %v",
			ErrAccessEvidenceInvalid, err)
	}
	return nil
}

// validateAccessQuestion checks the normalized question shared by observations
// and decisions.
func validateAccessQuestion(q sdk.AccessQuestion) error {
	if q.SchemaVersion != sdk.AccessEvidenceSchemaVersion {
		return fmt.Errorf("%w: question: schema version %d is not the version this engine implements (%d)",
			ErrAccessEvidenceInvalid, q.SchemaVersion, sdk.AccessEvidenceSchemaVersion)
	}
	if !q.Valid() {
		return fmt.Errorf("%w: question: an actor and an exact action are required", ErrAccessEvidenceInvalid)
	}
	// The exact verb and its vocabulary travel together: an action recorded
	// without saying which vocabulary it belongs to cannot be compared to a
	// policy's verb later, and "read" would silently stand in for it.
	if blankAccessField(q.ActionVocabulary) {
		return fmt.Errorf("%w: question: the action vocabulary of %q is required", ErrAccessEvidenceInvalid, q.Action)
	}
	// A resource is a reference INSIDE a scope. Naming a resource without its
	// origin scope is how two same-named tables on two servers become one.
	if !blankAccessField(q.ResourceRef) && blankAccessField(q.ResourceKind) {
		return fmt.Errorf("%w: question: resource %q has no resource kind", ErrAccessEvidenceInvalid, q.ResourceRef)
	}
	if !blankAccessField(q.ResourceRef) && blankAccessField(q.SourceInstance) {
		return fmt.Errorf("%w: question: resource %q has no source instance; two identically named resources on two systems are not one resource",
			ErrAccessEvidenceInvalid, q.ResourceRef)
	}
	return nil
}

// validateAccessBinding checks that an operation identity is a complete PAIR.
// Half a binding cannot be checked against the journal and cannot bind anything,
// so it is refused rather than stored as a dangling reference.
func validateAccessBinding(op sdk.OperationID, digest sdk.EffectDigest, what string) error {
	hasOp := !blankAccessField(string(op))
	hasDigest := !blankAccessField(string(digest))
	if hasOp != hasDigest {
		return fmt.Errorf("%w: %s: operation id and effect digest travel together (got operation=%t digest=%t)",
			ErrAccessEvidenceInvalid, what, hasOp, hasDigest)
	}
	return nil
}

// validateGovernanceRevisionPair checks the reuse of the local revision
// identity: both halves or neither, and a positive revision number.
func validateGovernanceRevisionPair(surface string, revision int64, what string) error {
	ref := model.GovernanceRevisionRef{Surface: surface, Revision: revision}
	if ref.Declared() && !ref.Complete() {
		return fmt.Errorf("%w: %s: governance revision identity is the (surface, revision) pair; got surface=%q revision=%d",
			ErrAccessEvidenceInvalid, what, surface, revision)
	}
	return nil
}

// containsInlinePolicySecret is the minimal-data backstop on a RETAINED
// artifact: a policy document must never carry a live provider credential. It
// is the same guardrail the governance revision store applies, kept identical so
// a document refused on one path is refused on the other. It is a guardrail, not
// a scanner.
func containsInlinePolicySecret(content string) bool {
	return strings.Contains(content, "sk-ant-")
}

// ValidatePolicyArtifactAppend checks that in names a complete, self-consistent
// artifact. The cross-field rules are the point of the function:
//
//   - availability decides which of Content/ContentRef may be present, so
//     "retained" cannot mean an empty body and "absent" cannot smuggle one;
//   - a RETAINED artifact must hash to its declared digest under a digest
//     algorithm this engine can recompute — otherwise "retained" would be an
//     unverified claim about bytes nobody checked;
//   - a declared size must match the retained bytes, so a truncated body cannot
//     present itself as the whole document;
//   - an oversized artifact is REFUSED, never truncated.
func ValidatePolicyArtifactAppend(in PolicyArtifactAppend) error {
	if err := validateAccessAppend(in.AccessEvidenceAppend, in.Artifact); err != nil {
		return err
	}
	a := in.Artifact
	switch {
	case a.SchemaVersion != sdk.AccessEvidenceSchemaVersion:
		return fmt.Errorf("%w: artifact: schema version %d is not the version this engine implements (%d)",
			ErrAccessEvidenceInvalid, a.SchemaVersion, sdk.AccessEvidenceSchemaVersion)
	case blankAccessField(a.AuthorityID):
		return fmt.Errorf("%w: artifact: authority id required", ErrAccessEvidenceInvalid)
	case blankAccessField(a.Surface):
		return fmt.Errorf("%w: artifact: surface required", ErrAccessEvidenceInvalid)
	case blankAccessField(a.Engine):
		return fmt.Errorf("%w: artifact: evaluating engine required", ErrAccessEvidenceInvalid)
	case !a.Origin.Valid():
		return fmt.Errorf("%w: artifact: origin %q is not a known provenance class", ErrAccessEvidenceInvalid, a.Origin)
	case !a.Availability.Valid():
		return fmt.Errorf("%w: artifact: availability %q is not a known state", ErrAccessEvidenceInvalid, a.Availability)
	case blankAccessField(a.ArtifactDigest):
		return fmt.Errorf("%w: artifact: artifact digest required", ErrAccessEvidenceInvalid)
	case blankAccessField(a.DigestAlgorithm):
		return fmt.Errorf("%w: artifact: digest algorithm required", ErrAccessEvidenceInvalid)
	}
	if err := validateGovernanceRevisionPair(a.GovernanceSurface, a.GovernanceRevision, "artifact"); err != nil {
		return err
	}
	for _, d := range a.Dependencies {
		if !d.Valid() {
			return fmt.Errorf("%w: artifact: dependency %+v names neither a kind nor a ref", ErrAccessEvidenceInvalid, d)
		}
	}

	hasContent := a.Content != ""
	hasRef := !blankAccessField(a.ContentRef)
	switch a.Availability {
	case sdk.AvailabilityRetained:
		if !hasContent {
			return fmt.Errorf("%w: artifact: availability %q requires the normative content", ErrAccessEvidenceInvalid, a.Availability)
		}
		if hasRef {
			return fmt.Errorf("%w: artifact: availability %q must not also carry an external reference", ErrAccessEvidenceInvalid, a.Availability)
		}
		if len(a.Content) > model.MaxPolicyArtifactBytes {
			return fmt.Errorf("%w: artifact: retained content is %d bytes, over the %d-byte ceiling; record it in a protected store and retain the reference rather than storing a shortened document",
				ErrAccessEvidenceInvalid, len(a.Content), model.MaxPolicyArtifactBytes)
		}
		if a.ContentBytes != 0 && a.ContentBytes != int64(len(a.Content)) {
			return fmt.Errorf("%w: artifact: declared size %d does not match the %d retained bytes",
				ErrAccessEvidenceInvalid, a.ContentBytes, len(a.Content))
		}
		if a.DigestAlgorithm != sdk.ArtifactDigestAlgorithm {
			return fmt.Errorf("%w: artifact: a retained artifact must declare digest algorithm %q so the engine can recompute it; got %q",
				ErrAccessEvidenceInvalid, sdk.ArtifactDigestAlgorithm, a.DigestAlgorithm)
		}
		if got := sdk.ArtifactContentDigest([]byte(a.Content)); got != a.ArtifactDigest {
			return fmt.Errorf("%w: artifact: retained content hashes to %s, not the declared %s",
				ErrAccessEvidenceInvalid, got, a.ArtifactDigest)
		}
		if containsInlinePolicySecret(a.Content) {
			return fmt.Errorf("%w: artifact: retained content carries an inline provider credential", ErrAccessEvidenceInvalid)
		}
	case sdk.AvailabilityExternalRetained, sdk.AvailabilityExternalRestricted:
		if !hasRef {
			return fmt.Errorf("%w: artifact: availability %q requires the durable reference to the protected store",
				ErrAccessEvidenceInvalid, a.Availability)
		}
		if hasContent {
			return fmt.Errorf("%w: artifact: availability %q must not retain the content here", ErrAccessEvidenceInvalid, a.Availability)
		}
	case sdk.AvailabilityAbsent:
		if hasContent || hasRef {
			return fmt.Errorf("%w: artifact: availability %q means neither content nor reference is held", ErrAccessEvidenceInvalid, a.Availability)
		}
	}
	return nil
}

// ValidateAuthorityTransitionAppend checks one authority transition. The two
// temporal coordinates are both required and are not interchangeable:
// EffectiveAt is the state of the world the ISSUER attests, KnownAt bounds what
// Olivares had received. Keeping them apart is what lets a late snapshot
// complete history without claiming the enforcement point knew it earlier.
func ValidateAuthorityTransitionAppend(in AuthorityTransitionAppend) error {
	if err := validateAccessAppend(in.AccessEvidenceAppend, in.Transition); err != nil {
		return err
	}
	t := in.Transition
	switch {
	case t.SchemaVersion != sdk.AccessEvidenceSchemaVersion:
		return fmt.Errorf("%w: transition: schema version %d is not the version this engine implements (%d)",
			ErrAccessEvidenceInvalid, t.SchemaVersion, sdk.AccessEvidenceSchemaVersion)
	case !t.SubjectKind.Valid():
		return fmt.Errorf("%w: transition: subject kind %q is not known", ErrAccessEvidenceInvalid, t.SubjectKind)
	case blankAccessField(t.SubjectRef):
		return fmt.Errorf("%w: transition: subject reference required", ErrAccessEvidenceInvalid)
	case !t.Transition.Valid():
		return fmt.Errorf("%w: transition: %q is not a known authority transition", ErrAccessEvidenceInvalid, t.Transition)
	case !t.SnapshotCompleteness.Valid():
		return fmt.Errorf("%w: transition: snapshot completeness %q is not known", ErrAccessEvidenceInvalid, t.SnapshotCompleteness)
	case blankAccessField(t.ReasonCode):
		return fmt.Errorf("%w: transition: a stable reason code is required", ErrAccessEvidenceInvalid)
	}
	if err := validateGovernanceRevisionPair(t.GovernanceSurface, t.GovernanceRevision, "transition"); err != nil {
		return err
	}
	effective, err := model.ParseTimestamp(t.EffectiveAt)
	if err != nil {
		return fmt.Errorf("%w: transition: effective instant is not canonical timestamp text: %v", ErrAccessEvidenceInvalid, err)
	}
	if _, err := model.ParseTimestamp(t.KnownAt); err != nil {
		return fmt.Errorf("%w: transition: known instant is not canonical timestamp text: %v", ErrAccessEvidenceInvalid, err)
	}
	if !blankAccessField(t.EffectiveUntil) {
		until, err := model.ParseTimestamp(t.EffectiveUntil)
		if err != nil {
			return fmt.Errorf("%w: transition: effective-until instant is not canonical timestamp text: %v", ErrAccessEvidenceInvalid, err)
		}
		if until.Time().Before(effective.Time()) {
			return fmt.Errorf("%w: transition: effective window ends (%s) before it starts (%s)",
				ErrAccessEvidenceInvalid, t.EffectiveUntil, t.EffectiveAt)
		}
	}
	return nil
}

// ValidateActionObservationAppend checks one observed stage. Two rules carry
// the contract's weight:
//
//   - an EXTERNAL observer may not claim it could have prevented the stage. It
//     was not in the path; recording otherwise would turn a collector into a
//     enforcement point on paper.
//   - only a confirmed effect may carry a confirmation level, and it MUST carry
//     one. Without it a transport acknowledgement reads as a durable change to
//     the resource, which is precisely the overclaim being prevented.
func ValidateActionObservationAppend(in ActionObservationAppend) error {
	if err := validateAccessAppend(in.AccessEvidenceAppend, in.Observation); err != nil {
		return err
	}
	o := in.Observation
	switch {
	case o.SchemaVersion != sdk.AccessEvidenceSchemaVersion:
		return fmt.Errorf("%w: observation: schema version %d is not the version this engine implements (%d)",
			ErrAccessEvidenceInvalid, o.SchemaVersion, sdk.AccessEvidenceSchemaVersion)
	case !o.Stage.Valid():
		return fmt.Errorf("%w: observation: stage %q is not known", ErrAccessEvidenceInvalid, o.Stage)
	case !o.Mediation.Valid():
		return fmt.Errorf("%w: observation: mediation %q is not known", ErrAccessEvidenceInvalid, o.Mediation)
	case !o.Prevention.Valid():
		return fmt.Errorf("%w: observation: prevention capability %q is not known", ErrAccessEvidenceInvalid, o.Prevention)
	}
	if err := validateAccessQuestion(o.Question); err != nil {
		return err
	}
	if o.Mediation == sdk.MediationExternalObserver && o.Prevention == sdk.PreventionCanPrevent {
		return fmt.Errorf("%w: observation: an external observer was not in the path and cannot claim it could prevent the stage",
			ErrAccessEvidenceInvalid)
	}
	if o.Stage == sdk.StageEffectConfirmed {
		if !o.Confirmation.Valid() {
			return fmt.Errorf("%w: observation: a confirmed effect must state what the protocol confirmed (receipt, response or durable effect)",
				ErrAccessEvidenceInvalid)
		}
	} else if o.Confirmation != "" {
		return fmt.Errorf("%w: observation: stage %q confirms nothing and must not carry confirmation %q",
			ErrAccessEvidenceInvalid, o.Stage, o.Confirmation)
	}
	return validateAccessBinding(o.OperationID, o.EffectDigest, "observation")
}

// ValidateAuthorizationDecisionAppend checks one decision. Two rules keep a
// record from claiming an authority it never had:
//
//   - a reconstruction or a what-if may NOT bind a journaled operation. It did
//     not govern the effect; presenting it as the original enforcement point's
//     decision is how history gets rewritten.
//   - a SHADOW decision has no effective disposition. In observe mode nothing
//     was applied, and a recorded disposition would read as a block that never
//     happened.
func ValidateAuthorizationDecisionAppend(in AuthorizationDecisionAppend) error {
	if err := validateAccessAppend(in.AccessEvidenceAppend, in.Decision); err != nil {
		return err
	}
	d := in.Decision
	switch {
	case d.SchemaVersion != sdk.AccessEvidenceSchemaVersion:
		return fmt.Errorf("%w: decision: schema version %d is not the version this engine implements (%d)",
			ErrAccessEvidenceInvalid, d.SchemaVersion, sdk.AccessEvidenceSchemaVersion)
	case !d.Purpose.Valid():
		return fmt.Errorf("%w: decision: purpose %q is not known", ErrAccessEvidenceInvalid, d.Purpose)
	case !d.Outcome.Valid():
		return fmt.Errorf("%w: decision: outcome %q is not known", ErrAccessEvidenceInvalid, d.Outcome)
	case d.Disposition != "" && !d.Disposition.Valid():
		return fmt.Errorf("%w: decision: disposition %q is not known", ErrAccessEvidenceInvalid, d.Disposition)
	case !d.ReplayCompleteness.Valid():
		return fmt.Errorf("%w: decision: replay completeness %q is not known", ErrAccessEvidenceInvalid, d.ReplayCompleteness)
	case blankAccessField(d.Evaluator) || blankAccessField(d.EvaluatorVersion):
		return fmt.Errorf("%w: decision: evaluator and its version are required", ErrAccessEvidenceInvalid)
	case blankAccessField(d.ReasonCode):
		return fmt.Errorf("%w: decision: a stable reason code is required", ErrAccessEvidenceInvalid)
	case blankAccessField(d.AuthorizationPoint):
		return fmt.Errorf("%w: decision: the authorization point is required; the limits of a decision are the limits of its enforcement point",
			ErrAccessEvidenceInvalid)
	}
	if err := validateAccessQuestion(d.Question); err != nil {
		return err
	}
	if d.Shadow && d.Disposition != "" {
		return fmt.Errorf("%w: decision: a shadow evaluation applied nothing and must not record an effective disposition (%q)",
			ErrAccessEvidenceInvalid, d.Disposition)
	}
	if err := validateAccessBinding(d.OperationID, d.EffectDigest, "decision"); err != nil {
		return err
	}
	if d.Purpose != sdk.PurposeLiveAuthorization && !blankAccessField(string(d.OperationID)) {
		return fmt.Errorf("%w: decision: purpose %q did not govern operation %q and must not bind it",
			ErrAccessEvidenceInvalid, d.Purpose, d.OperationID)
	}
	for _, dep := range d.Inputs {
		if !dep.Valid() {
			return fmt.Errorf("%w: decision: input %+v names neither a kind nor a ref", ErrAccessEvidenceInvalid, dep)
		}
	}
	if !blankAccessField(d.ValidFrom) || !blankAccessField(d.ValidUntil) {
		if blankAccessField(d.ValidFrom) || blankAccessField(d.ValidUntil) {
			return fmt.Errorf("%w: decision: a validity window needs both ends", ErrAccessEvidenceInvalid)
		}
		from, err := model.ParseTimestamp(d.ValidFrom)
		if err != nil {
			return fmt.Errorf("%w: decision: validity start is not canonical timestamp text: %v", ErrAccessEvidenceInvalid, err)
		}
		until, err := model.ParseTimestamp(d.ValidUntil)
		if err != nil {
			return fmt.Errorf("%w: decision: validity end is not canonical timestamp text: %v", ErrAccessEvidenceInvalid, err)
		}
		if until.Time().Before(from.Time()) {
			return fmt.Errorf("%w: decision: validity window ends (%s) before it starts (%s)",
				ErrAccessEvidenceInvalid, d.ValidUntil, d.ValidFrom)
		}
	}
	return nil
}
