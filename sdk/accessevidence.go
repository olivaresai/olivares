// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sdk

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// accessevidence.go — the versioned EXCHANGE contract for reconstructible
// authority, activity and authorization decisions (v26.9 increment A).
//
// It is the wire half of the storage contract: the four content shapes a
// producer hands the engine, the closed vocabularies they are built from, and
// the canonical digest every party recomputes over them. Like the rest of this
// SDK it is Apache-2.0 and zero-dependency, so a producer, an adapter, or a
// LATER independent verifier can build against it without the AGPL engine.
//
// # What this file is NOT
//
// These are DTOs, not durable aggregates. A content value carries no identity,
// no tenant, no ledger anchor and no authority: the engine assigns the durable
// id, stamps the tenant and the producer from the effective configuration and
// authentication, and anchors the record. A producer therefore cannot assert
// its own tenant, cannot assert that it is a policy authority, and cannot mint
// an authorization by handing over JSON.
//
// A stored record is DATA. Nothing here is an EvidenceReceipt, a witness or a
// capability, and no function in this file returns one: an allow decision that
// was recorded is a historical fact about an evaluator, never a green light for
// an effect. The evidence-or-refuse law (evidence.go) is unchanged and remains
// the only thing that authorizes an effect.
//
// # Separation the vocabularies exist to keep
//
// Four properties are recorded separately and never collapsed, because each is
// evidence of something different: the AUTHENTICITY of the producer, the
// AUTHORITY of the policy issuer, the CONFIDENCE in the attribution, and the
// COVERAGE of the observation. Likewise an intent (`requested`) is not a
// dispatch, a dispatch is not a confirmed effect, and a confirmed effect states
// WHAT the protocol actually confirmed (EffectConfirmation) rather than
// implying a durable mutation the transport never proved.

// AccessEvidenceSchemaVersion is the version of the shapes in this file. It is
// stamped on every record so a reader knows which contract produced it; a
// record whose version a reader does not implement is refused, never guessed.
const AccessEvidenceSchemaVersion = 1

// The domain separators for the canonical digests. They are versioned and
// length-prefixed into the preimage so a value hashed for one purpose can never
// be replayed as another, and so a scheme change is a new domain rather than a
// silent reinterpretation of old bytes.
const (
	domainAccessQuestion = "olivares.access-evidence.question.v1"
	domainAccessRecord   = "olivares.access-evidence.record.v1"
	domainAccessArtifact = "olivares.access-evidence.artifact.v1"
)

// ArtifactDigestAlgorithm names the digest scheme ArtifactContentDigest
// implements. A RETAINED artifact must declare it, because retaining the bytes
// is only worth something if the engine can recompute the digest over them; an
// artifact held elsewhere may declare its issuer's own scheme verbatim.
const ArtifactDigestAlgorithm = "olivares.access-evidence.artifact.v1-sha256"

// AccessEvidenceTimeLayout is the canonical instant format for every normative
// timestamp in this contract: fixed-width RFC 3339 in UTC with nine fractional
// digits. It is fixed-width for four-digit years, so lexical order equals
// chronological order and the digested text equals the stored text — which is
// why these fields are strings rather than time.Time. It is byte-identical to
// the engine's own storage layout, and the engine pins that equality with a test
// rather than a comment.
const AccessEvidenceTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// FormatEvidenceTime renders t in the canonical layout, in UTC. A producer that
// formats instants any other way produces a record whose digest cannot be
// reproduced after storage.
func FormatEvidenceTime(t time.Time) string {
	return t.UTC().Format(AccessEvidenceTimeLayout)
}

// ParseEvidenceTime parses canonical instant text. It exists so a consumer
// validates rather than guesses: text that is not the canonical form is an
// error, never a best-effort reading.
func ParseEvidenceTime(s string) (time.Time, error) {
	t, err := time.Parse(AccessEvidenceTimeLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("access evidence: parse instant %q: %w", s, err)
	}
	return t.UTC(), nil
}

// ---------------------------------------------------------------------------
// Closed vocabularies
// ---------------------------------------------------------------------------

// ArtifactOrigin says HOW Olivares came to hold a policy artifact. It is the
// provenance axis, and it is deliberately not a trust score: an
// operator_declaration about a remote cloud policy is a declaration about that
// policy, never proof that the cloud enforced it.
type ArtifactOrigin string

// The artifact origins.
const (
	// OriginLocalAuthoritative: Olivares itself is the authority for this
	// artifact (a governance revision it stores and evaluates).
	OriginLocalAuthoritative ArtifactOrigin = "local_authoritative"
	// OriginRemoteAuthenticatedSnapshot: an authenticated snapshot from the
	// system that owns the policy. Authenticity of the transfer, not proof that
	// the remote system applied it to the access in question.
	OriginRemoteAuthenticatedSnapshot ArtifactOrigin = "remote_authenticated_snapshot"
	// OriginOperatorDeclaration: an operator asserted this artifact's content.
	OriginOperatorDeclaration ArtifactOrigin = "operator_declaration"
	// OriginUnverified: received without verifiable provenance.
	OriginUnverified ArtifactOrigin = "unverified"
)

// Valid reports whether o is a known origin. Unknown is never permissive.
func (o ArtifactOrigin) Valid() bool {
	switch o {
	case OriginLocalAuthoritative, OriginRemoteAuthenticatedSnapshot,
		OriginOperatorDeclaration, OriginUnverified:
		return true
	}
	return false
}

// ArtifactAvailability is the EXPLICIT reconstructibility state of a policy
// artifact's normative content. It exists because a digest proves integrity by
// comparison and proves nothing about being able to re-evaluate: only
// AvailabilityRetained means "the rules are here".
type ArtifactAvailability string

// The artifact availability states.
const (
	// AvailabilityRetained: the normative artifact itself is retained with the
	// record. This is the ONLY state that demonstrates local reconstructibility.
	AvailabilityRetained ArtifactAvailability = "retained"
	// AvailabilityExternalRetained: the artifact lives in a protected store and
	// the durable reference is recorded and declared reachable.
	AvailabilityExternalRetained ArtifactAvailability = "external_retained"
	// AvailabilityExternalRestricted: the reference is recorded; the content is
	// restricted and is NOT available to the reader of this record.
	AvailabilityExternalRestricted ArtifactAvailability = "external_restricted"
	// AvailabilityAbsent: neither content nor reference — the digest alone. The
	// record can be compared for integrity and cannot be re-evaluated.
	AvailabilityAbsent ArtifactAvailability = "absent"
)

// Valid reports whether a is a known availability state.
func (a ArtifactAvailability) Valid() bool {
	switch a {
	case AvailabilityRetained, AvailabilityExternalRetained,
		AvailabilityExternalRestricted, AvailabilityAbsent:
		return true
	}
	return false
}

// LocallyReconstructible reports whether the normative content travels WITH the
// record. Only AvailabilityRetained does; every other state means a second
// system must be consulted, or the rules are gone.
func (a ArtifactAvailability) LocallyReconstructible() bool { return a == AvailabilityRetained }

// AuthoritySubjectKind names WHAT an authority transition is about.
type AuthoritySubjectKind string

// The authority subject kinds.
const (
	// SubjectPolicyArtifact: a retained policy artifact record.
	SubjectPolicyArtifact AuthoritySubjectKind = "policy_artifact"
	// SubjectGrant: a grant with its own id, effect and conditions.
	SubjectGrant AuthoritySubjectKind = "grant"
	// SubjectBinding: a source/scope/credential binding.
	SubjectBinding AuthoritySubjectKind = "binding"
)

// Valid reports whether k is a known subject kind.
func (k AuthoritySubjectKind) Valid() bool {
	switch k {
	case SubjectPolicyArtifact, SubjectGrant, SubjectBinding:
		return true
	}
	return false
}

// AuthorityTransitionType is the append-only authority event. Withdrawal is a
// first-class member: an authority history that can only add is not a history.
type AuthorityTransitionType string

// The authority transitions.
const (
	// TransitionActivate makes the subject effective.
	TransitionActivate AuthorityTransitionType = "activate"
	// TransitionSupersede replaces the subject with a newer one.
	TransitionSupersede AuthorityTransitionType = "supersede"
	// TransitionDeactivate stops the subject being selected, without asserting
	// that the issuer revoked it.
	TransitionDeactivate AuthorityTransitionType = "deactivate"
	// TransitionRevoke is the issuer's withdrawal of the subject.
	TransitionRevoke AuthorityTransitionType = "revoke"
	// TransitionExpire records a known expiry being reached.
	TransitionExpire AuthorityTransitionType = "expire"
)

// Valid reports whether t is a known transition.
func (t AuthorityTransitionType) Valid() bool {
	switch t {
	case TransitionActivate, TransitionSupersede, TransitionDeactivate,
		TransitionRevoke, TransitionExpire:
		return true
	}
	return false
}

// SnapshotCompleteness declares what the SOURCE of a transition could see. It
// is what stops an absence in a partial page from being read as a revocation.
type SnapshotCompleteness string

// The snapshot completeness states.
const (
	// SnapshotCompleteWithinScope: the protocol proved a complete snapshot for
	// the declared scope and generation.
	SnapshotCompleteWithinScope SnapshotCompleteness = "complete_within_declared_scope"
	// SnapshotPartial: a page, a filtered view or an interrupted poll.
	SnapshotPartial SnapshotCompleteness = "partial"
	// SnapshotUnknown: completeness could not be determined.
	SnapshotUnknown SnapshotCompleteness = "unknown"
	// SnapshotUnavailable: the source could not be read at all.
	SnapshotUnavailable SnapshotCompleteness = "unavailable"
)

// Valid reports whether s is a known completeness state.
func (s SnapshotCompleteness) Valid() bool {
	switch s {
	case SnapshotCompleteWithinScope, SnapshotPartial, SnapshotUnknown, SnapshotUnavailable:
		return true
	}
	return false
}

// ObservationStage is the normalized stage of one observed action. It does NOT
// replace the normative EvidenceOperation states (evidenceop journal): it
// records what a boundary SAW, in the vocabulary the journal already
// distinguishes, so an attempt, a dispatch and a confirmed effect stay three
// different facts.
type ObservationStage string

// The observation stages.
const (
	// StageRequested: the request arrived (or a pre-event hook fired).
	StageRequested ObservationStage = "requested"
	// StageAttempted: the actor tried to proceed past the request.
	StageAttempted ObservationStage = "attempted"
	// StageDispatched: the write/delivery to the next actor is attested by that
	// boundary. It is not proof the next actor applied anything.
	StageDispatched ObservationStage = "dispatched"
	// StageEffectConfirmed: a confirmation the protocol allows. It MUST say what
	// was confirmed (EffectConfirmation).
	StageEffectConfirmed ObservationStage = "effect_confirmed"
	// StageFailed: the action failed.
	StageFailed ObservationStage = "failed"
	// StageNotSent: definitively not emitted (journal 'not_sent').
	StageNotSent ObservationStage = "not_sent"
	// StageBlocked: a policy/authorization decision stopped it before dispatch
	// (journal 'blocked'): zero accesses consumed.
	StageBlocked ObservationStage = "blocked"
	// StageOutcomeUnknown: ambiguous outcome (journal 'unknown'). The absence of
	// a post-event is neither success nor failure.
	StageOutcomeUnknown ObservationStage = "outcome_unknown"
	// StageResponseReleased: the response was delivered to the caller.
	StageResponseReleased ObservationStage = "response_released"
	// StageResponseWithheld: the response was observed and withheld (journal
	// 'withheld'). It does not unmake the parent effect that already ran.
	StageResponseWithheld ObservationStage = "response_withheld"
)

// Valid reports whether s is a known stage.
func (s ObservationStage) Valid() bool {
	switch s {
	case StageRequested, StageAttempted, StageDispatched, StageEffectConfirmed,
		StageFailed, StageNotSent, StageBlocked, StageOutcomeUnknown,
		StageResponseReleased, StageResponseWithheld:
		return true
	}
	return false
}

// ObservationMediation says whether Olivares sat in the path of this stage.
type ObservationMediation string

// The mediation kinds.
const (
	// MediationOlivaresPEP: an Olivares enforcement point observed the stage.
	MediationOlivaresPEP ObservationMediation = "olivares_pep"
	// MediationExternalObserver: the fact was collected from outside the path.
	MediationExternalObserver ObservationMediation = "external_observer"
)

// Valid reports whether m is a known mediation.
func (m ObservationMediation) Valid() bool {
	return m == MediationOlivaresPEP || m == MediationExternalObserver
}

// PreventionCapability is the CONCRETE ability to stop this stage, recorded per
// observation because it is a property of the deployment, not of the product.
type PreventionCapability string

// The prevention capabilities.
const (
	// PreventionCanPrevent: this boundary could have stopped the stage.
	PreventionCanPrevent PreventionCapability = "can_prevent"
	// PreventionCannotPrevent: it could only watch.
	PreventionCannotPrevent PreventionCapability = "cannot_prevent"
	// PreventionUnknown: the capability is not established.
	PreventionUnknown PreventionCapability = "unknown"
)

// Valid reports whether p is a known capability.
func (p PreventionCapability) Valid() bool {
	switch p {
	case PreventionCanPrevent, PreventionCannotPrevent, PreventionUnknown:
		return true
	}
	return false
}

// EffectConfirmation says WHAT a confirmation proves. Without it, a transport
// acknowledgement reads as a durable change to the resource, which is the
// overclaim this axis exists to prevent.
type EffectConfirmation string

// The effect confirmation levels.
const (
	// ConfirmationReceipt: the next actor acknowledged RECEIPT only.
	ConfirmationReceipt EffectConfirmation = "receipt"
	// ConfirmationResponse: a response came back; the resource change is not
	// separately attested.
	ConfirmationResponse EffectConfirmation = "response"
	// ConfirmationDurableEffect: the protocol attests a durable effect on the
	// resource itself.
	ConfirmationDurableEffect EffectConfirmation = "durable_effect"
)

// Valid reports whether c is a known confirmation level.
func (c EffectConfirmation) Valid() bool {
	switch c {
	case ConfirmationReceipt, ConfirmationResponse, ConfirmationDurableEffect:
		return true
	}
	return false
}

// DecisionPurpose separates "what the enforcement point decided then" from "what
// would be decided now". Collapsing them is how an old allow becomes a current
// authorization.
type DecisionPurpose string

// The decision purposes.
const (
	// PurposeLiveAuthorization: the decision an enforcement point actually took
	// for a real action.
	PurposeLiveAuthorization DecisionPurpose = "live_authorization"
	// PurposeHistoricalReconstruction: a later re-evaluation of a past question.
	// It is a new decision with its own inputs and never speaks for the original.
	PurposeHistoricalReconstruction DecisionPurpose = "historical_reconstruction"
	// PurposeCurrentWhatIf: "would this be permitted now?".
	PurposeCurrentWhatIf DecisionPurpose = "current_what_if"
)

// Valid reports whether p is a known purpose.
func (p DecisionPurpose) Valid() bool {
	switch p {
	case PurposeLiveAuthorization, PurposeHistoricalReconstruction, PurposeCurrentWhatIf:
		return true
	}
	return false
}

// AccessDecisionOutcome is what the evaluator concluded. Indeterminate is explicit so
// an unreachable policy is never dressed up as a denial by policy.
type AccessDecisionOutcome string

// The decision outcomes.
const (
	// AccessOutcomeAllow: the evaluator permitted the exact question.
	AccessOutcomeAllow AccessDecisionOutcome = "allow"
	// AccessOutcomeDeny: the evaluator refused it.
	AccessOutcomeDeny AccessDecisionOutcome = "deny"
	// AccessOutcomeIndeterminate: the evaluator could not conclude (policy
	// unreachable, inputs missing, evaluator error).
	AccessOutcomeIndeterminate AccessDecisionOutcome = "indeterminate"
)

// Valid reports whether o is a known outcome.
func (o AccessDecisionOutcome) Valid() bool {
	switch o {
	case AccessOutcomeAllow, AccessOutcomeDeny, AccessOutcomeIndeterminate:
		return true
	}
	return false
}

// DecisionDisposition is the EFFECTIVE disposition applied at the enforcement
// point, which some protocols express more richly than allow/deny.
type DecisionDisposition string

// The decision dispositions.
const (
	DispositionAllow  DecisionDisposition = "allow"
	DispositionDeny   DecisionDisposition = "deny"
	DispositionAsk    DecisionDisposition = "ask"
	DispositionModify DecisionDisposition = "modify"
)

// Valid reports whether d is a known disposition.
func (d DecisionDisposition) Valid() bool {
	switch d {
	case DispositionAllow, DispositionDeny, DispositionAsk, DispositionModify:
		return true
	}
	return false
}

// ReplayCompleteness is the producer's CLAIM about whether the recorded inputs
// suffice to re-evaluate the decision. The engine verifies a "complete" claim
// against the actual availability of the required dependencies and refuses an
// overclaim, so this field cannot be used to promise reconstruction that the
// retained data does not support.
type ReplayCompleteness string

// The replay completeness claims.
const (
	// ReplayComplete: every required input is retained and reconstructible.
	ReplayComplete ReplayCompleteness = "complete"
	// ReplayIncomplete: at least one required input is not retained.
	ReplayIncomplete ReplayCompleteness = "incomplete"
	// ReplayUnknown: the producer cannot say.
	ReplayUnknown ReplayCompleteness = "unknown"
)

// Valid reports whether r is a known claim.
func (r ReplayCompleteness) Valid() bool {
	switch r {
	case ReplayComplete, ReplayIncomplete, ReplayUnknown:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// The normalized question
// ---------------------------------------------------------------------------

// AccessQuestion is the normalized "who tried to do what, to which resource, in
// which scope" shared by observations and decisions. Every field is an
// identifier, a reference or a version — never a bearer value, a secret, a SQL
// body, a prompt, a file or a result (minimal data).
//
// Field semantics that carry weight:
//
//   - Actor and Subject are separate. When a request is delegated they differ,
//     and collapsing them attributes the act to the wrong party.
//   - Resource identity is the pair (ResourceKind, ResourceRef) INSIDE a scope
//     (SourceInstance/Endpoint/Namespace). Two tables called public.x on two
//     PostgreSQL servers are two resources; sharing a name does not merge them.
//   - Action is the protocol's exact verb with its vocabulary version.
//     AccessMode is a SEPARATE, coarser classification and never replaces it.
//   - Optional fields distinguish "does not apply" from "not resolved" by being
//     empty and being documented as such: nothing here is invented from a hash.
type AccessQuestion struct {
	// SchemaVersion is AccessEvidenceSchemaVersion at production time.
	SchemaVersion int `json:"schema_version"`

	// PrincipalKind classifies the authenticated principal (e.g. "user",
	// "service", "external"). Empty means unresolved, never "anonymous".
	PrincipalKind string `json:"principal_kind,omitempty"`
	// PrincipalRef is the principal's reference in its issuer's namespace.
	PrincipalRef string `json:"principal_ref,omitempty"`
	// Issuer is the identity issuer that authenticated the principal.
	Issuer string `json:"issuer,omitempty"`
	// CredentialClass names the KIND of credential (e.g. "oidc", "api_token").
	// Never a credential value.
	CredentialClass string `json:"credential_class,omitempty"`
	// CredentialRef is a non-secret reference/version of the credential.
	CredentialRef string `json:"credential_ref,omitempty"`
	// ActorRef is the requesting actor; SubjectRef is the effective subject when
	// the two differ (delegation). Leave SubjectRef empty when they are the same.
	ActorRef   string `json:"actor_ref,omitempty"`
	SubjectRef string `json:"subject_ref,omitempty"`
	// DelegationRefs are the demonstrated links of a delegation chain, in order.
	// A link is recorded only when a binding demonstrates it.
	DelegationRefs []string `json:"delegation_refs,omitempty"`

	// AgentRef, SessionRef, RunRef, ProfileRef, EnvironmentRef and WorkspaceRef
	// are recorded ONLY when demonstrated by a binding, with the binding's own
	// reference/version. A provider's session id is a correlation value and is
	// never treated as Olivares authentication: it belongs in
	// ActionObservationContent.ProviderCorrelation, not here.
	AgentRef       string `json:"agent_ref,omitempty"`
	SessionRef     string `json:"session_ref,omitempty"`
	RunRef         string `json:"run_ref,omitempty"`
	ProfileRef     string `json:"profile_ref,omitempty"`
	EnvironmentRef string `json:"environment_ref,omitempty"`
	WorkspaceRef   string `json:"workspace_ref,omitempty"`

	// SourceInstance, Endpoint and Namespace are the origin scope that makes the
	// resource reference canonical.
	SourceInstance string `json:"source_instance,omitempty"`
	Endpoint       string `json:"endpoint,omitempty"`
	Namespace      string `json:"namespace,omitempty"`
	// ResourceKind and ResourceRef name the resource inside that scope.
	ResourceKind string `json:"resource_kind,omitempty"`
	ResourceRef  string `json:"resource_ref,omitempty"`
	// ResourceAttributesDigest is the digest of the resource attribute snapshot
	// used to authorize, when the evaluator consumed one.
	ResourceAttributesDigest string `json:"resource_attributes_digest,omitempty"`

	// Action is the protocol's exact operation (an IAM verb, an MCP method, a
	// K3 act). ActionVocabulary versions that vocabulary.
	Action           string `json:"action,omitempty"`
	ActionVocabulary string `json:"action_vocabulary,omitempty"`
	// Mode is the coarse read/write classification when the adapter knows it. It
	// is auxiliary: "read" never substitutes for the exact verb above.
	Mode string `json:"mode,omitempty"`

	// Context is the minimal typed evaluation context: memberships, roles,
	// constraints, target scope, clock and versioned facts, as non-secret
	// string values. Never bearer material, secret values, SQL, prompts or
	// results.
	Context map[string]string `json:"context,omitempty"`
}

// Valid reports whether the question names something concrete enough to be
// evidence: a schema version, an actor and an action. A question that names no
// actor and no action describes nothing and is refused rather than stored as a
// shape that later reads as "everyone did everything".
func (q AccessQuestion) Valid() bool {
	return q.SchemaVersion > 0 &&
		strings.TrimSpace(q.ActorRef) != "" &&
		strings.TrimSpace(q.Action) != ""
}

// EffectiveSubject returns SubjectRef when delegation made it differ, else
// ActorRef. It exists so a reader never has to re-derive the rule.
func (q AccessQuestion) EffectiveSubject() string {
	if strings.TrimSpace(q.SubjectRef) != "" {
		return q.SubjectRef
	}
	return q.ActorRef
}

// Digest returns the canonical, domain-separated digest of the question. Two
// producers that normalized the same question produce the same digest, and a
// digest computed before storage equals one recomputed after a round trip
// because every field is a string.
func (q AccessQuestion) Digest() (string, error) {
	return canonicalDigest(domainAccessQuestion, q)
}

// AccessDependency is one typed input a decision consumed: an artifact, a fact,
// a role/membership version, a binding. Ref is resolvable inside the tenant;
// Digest binds the exact value; Required says the decision cannot be replayed
// without it.
type AccessDependency struct {
	// Kind classifies the input (see the DependencyKind constants).
	Kind string `json:"kind"`
	// Ref is the tenant-scoped reference to the input.
	Ref string `json:"ref"`
	// Digest binds the exact content, when the producer computed one.
	Digest string `json:"digest,omitempty"`
	// Version is the input's own version/sequence, when it has one.
	Version string `json:"version,omitempty"`
	// Required marks an input without which the decision cannot be re-evaluated.
	Required bool `json:"required"`
}

// The dependency kinds. DependencyPolicyArtifact is the only one the engine
// resolves to a stored record; the others are recorded refs whose resolution
// belongs to the increments that own those entities.
const (
	// DependencyPolicyArtifact refs a stored policy artifact record by id.
	DependencyPolicyArtifact = "policy_artifact"
	// DependencyFact refs a versioned fact the evaluator consumed.
	DependencyFact = "fact"
	// DependencyRole refs a role/permission set version.
	DependencyRole = "role"
	// DependencyMembership refs a membership version.
	DependencyMembership = "membership"
	// DependencyBinding refs a source/credential/session binding version.
	DependencyBinding = "binding"
)

// Valid reports whether the dependency names something resolvable.
func (d AccessDependency) Valid() bool {
	return strings.TrimSpace(d.Kind) != "" && strings.TrimSpace(d.Ref) != ""
}

// ---------------------------------------------------------------------------
// The four content shapes
// ---------------------------------------------------------------------------

// AccessEvidenceContent is the sealed sum of the content shapes a producer may
// submit. It is sealed with an unexported marker so no third party can
// introduce a family the engine does not have storage and constraints for.
type AccessEvidenceContent interface {
	// AccessEvidenceEventType returns the closed event type of this family. It
	// is part of the idempotency identity and of the record digest.
	AccessEvidenceEventType() string
	isAccessEvidenceContent()
}

// The closed event types, one per family. They are disjoint across families on
// purpose: the deduplication identity (producer, source event, type) is then
// globally unique even though each family is stored in its own relation.
const (
	EventTypePolicyArtifact        = "policy_artifact.retained"
	EventTypeAuthorityTransition   = "authority_transition.recorded"
	EventTypeActionObservation     = "action_observation.recorded"
	EventTypeAuthorizationDecision = "authorization_decision.recorded"
)

// PolicyArtifactContent is one immutable policy artifact as its producer
// presents it: WHOSE it is, WHICH engine evaluates it, WHAT it hashes to, and —
// explicitly — whether its rules are actually here.
//
// Retaining the artifact is what makes semantic re-evaluation possible later;
// retaining only a digest supports integrity comparison and nothing more, which
// is why Availability is a required, first-class field rather than an inference
// from which columns happen to be empty.
type PolicyArtifactContent struct {
	// SchemaVersion is AccessEvidenceSchemaVersion at production time.
	SchemaVersion int `json:"schema_version"`
	// AuthorityID identifies the issuer that holds authority over this artifact.
	// The engine does not accept it as PROOF of authority: a registered producer
	// may only publish within the scope it is registered for, and validating the
	// issuer's authority is the trust-registry work of the next increment.
	AuthorityID string `json:"authority_id"`
	// Surface is the policy surface (e.g. "managed-settings", "cedar", "aws.iam").
	Surface string `json:"surface"`
	// Engine identifies the evaluator that consumes this artifact.
	Engine string `json:"engine"`
	// NativeVersion is the issuer's own version string, verbatim.
	NativeVersion string `json:"native_version,omitempty"`
	// ArtifactDigest is the digest of the normative artifact bytes.
	// DigestAlgorithm names how it was computed. When Availability is
	// AvailabilityRetained the engine RECOMPUTES it over Content and refuses a
	// mismatch, so a retained artifact cannot silently be a different document.
	ArtifactDigest  string `json:"artifact_digest"`
	DigestAlgorithm string `json:"digest_algorithm"`
	// GovernanceSurface and GovernanceRevision reuse the local policy-revision
	// identity (the module's own (surface, revision) uniqueness key) when the
	// artifact IS a local revision. They travel as a pair: one without the other
	// is refused.
	GovernanceSurface  string `json:"governance_surface,omitempty"`
	GovernanceRevision int64  `json:"governance_revision,omitempty"`
	// Origin is the provenance class.
	Origin ArtifactOrigin `json:"origin"`
	// AuthorizedScope records the scope the issuer is authorized over, as
	// non-secret strings (e.g. account, namespace, surface family).
	AuthorizedScope map[string]string `json:"authorized_scope,omitempty"`
	// Availability is the explicit reconstructibility state.
	Availability ArtifactAvailability `json:"availability"`
	// Content is the normative artifact, present only for AvailabilityRetained.
	// It is a POLICY document, never a credential; an inline provider key is
	// refused by the engine.
	Content string `json:"content,omitempty"`
	// ContentRef is the durable reference to the protected store that holds the
	// artifact, present only for the external availabilities.
	ContentRef string `json:"content_ref,omitempty"`
	// ContentBytes is the declared size of the normative artifact. For a
	// retained artifact the engine checks it against the retained bytes, so a
	// truncated body cannot present itself as the whole document.
	ContentBytes int64 `json:"content_bytes,omitempty"`
	// Dependencies are the inputs this artifact's evaluation needs beyond
	// itself (imported policy sets, referenced facts).
	Dependencies []AccessDependency `json:"dependencies,omitempty"`
}

// AccessEvidenceEventType implements AccessEvidenceContent.
func (PolicyArtifactContent) AccessEvidenceEventType() string { return EventTypePolicyArtifact }
func (PolicyArtifactContent) isAccessEvidenceContent()        {}

// AuthorityTransitionContent is one append-only authority event: which subject,
// which revision activated it, when the issuer says it took effect
// (EffectiveAt) and when Olivares learned it (KnownAt).
//
// The two coordinates are separate because a late snapshot may complete history
// without implying the enforcement point knew it earlier, and because an absence
// in a partial snapshot must never read as a revocation — which is what
// SnapshotCompleteness records.
type AuthorityTransitionContent struct {
	SchemaVersion int `json:"schema_version"`
	// SubjectKind and SubjectRef name the exact artifact, grant or binding.
	// For SubjectPolicyArtifact, SubjectRef is the stored artifact's id and the
	// engine resolves it inside the pinned tenant.
	SubjectKind AuthoritySubjectKind `json:"subject_kind"`
	SubjectRef  string               `json:"subject_ref"`
	// Transition is the authority event.
	Transition AuthorityTransitionType `json:"transition"`
	// GovernanceSurface/GovernanceRevision reuse the local revision identity of
	// the revision that carries this transition, when there is one.
	GovernanceSurface  string `json:"governance_surface,omitempty"`
	GovernanceRevision int64  `json:"governance_revision,omitempty"`
	// AuthoritySequence is the issuer's own version/sequence for this subject,
	// used to order transitions from the same authority. It is NOT the policy
	// document's revision.
	AuthoritySequence int64 `json:"authority_sequence,omitempty"`
	// EffectiveAt is the instant the issuer attests, EffectiveUntil the end of
	// the attested window when one exists — never invented for a subject that
	// has no expiry. KnownAt bounds what Olivares had received.
	// All three are canonical UTC timestamp text.
	EffectiveAt    string `json:"effective_at"`
	EffectiveUntil string `json:"effective_until,omitempty"`
	KnownAt        string `json:"known_at"`
	// ReasonCode is a stable machine-readable reason, never prose.
	ReasonCode string `json:"reason_code"`
	// SnapshotCompleteness declares what the source could see; SnapshotScope
	// records the exact scope it covered.
	SnapshotCompleteness SnapshotCompleteness `json:"snapshot_completeness"`
	SnapshotScope        map[string]string    `json:"snapshot_scope,omitempty"`
	// Conditions are the grant's own conditions when the subject is a grant.
	Conditions map[string]string `json:"conditions,omitempty"`
}

// AccessEvidenceEventType implements AccessEvidenceContent.
func (AuthorityTransitionContent) AccessEvidenceEventType() string {
	return EventTypeAuthorityTransition
}
func (AuthorityTransitionContent) isAccessEvidenceContent() {}

// ActionObservationContent is one observed stage of one action. Intent,
// dispatch, effect and response release are separate records linked by
// ParentRef, never one row whose meaning shifts.
type ActionObservationContent struct {
	SchemaVersion int `json:"schema_version"`
	// Question is the normalized question this stage is about.
	Question AccessQuestion `json:"question"`
	// Stage is the normalized stage.
	Stage ObservationStage `json:"stage"`
	// Mediation and Prevention record whether Olivares was in the path and what
	// this boundary could actually have stopped. An external observer may not
	// claim PreventionCanPrevent.
	Mediation  ObservationMediation `json:"mediation"`
	Prevention PreventionCapability `json:"prevention"`
	// PreventionDetail records the deployment fact behind Prevention (a provider
	// version, a hook configuration). Non-secret strings only.
	PreventionDetail map[string]string `json:"prevention_detail,omitempty"`
	// Confirmation is REQUIRED for StageEffectConfirmed and must be empty for
	// every other stage: a stage that confirms nothing may not carry a
	// confirmation level, and a confirmed effect must say what was confirmed.
	Confirmation EffectConfirmation `json:"confirmation,omitempty"`
	// OperationID and EffectDigest reuse the existing evidence journal identity
	// when this stage belongs to a journaled operation. The engine checks the
	// tenant's journal row exists and its binding matches; it never creates one.
	OperationID  OperationID  `json:"operation_id,omitempty"`
	EffectDigest EffectDigest `json:"effect_digest,omitempty"`
	// ParentRef is the parent observation's stored id (a release child pointing
	// at its dispatch parent, for instance).
	ParentRef string `json:"parent_ref,omitempty"`
	// DecisionRef is the stored authorization decision this stage was governed
	// by, when there is one. Its absence is recorded as absence, never as allow.
	DecisionRef string `json:"decision_ref,omitempty"`
	// ProviderCorrelation holds the provider's own identifiers for correlation
	// (a provider session id, a hook event id). They are correlation values and
	// never authentication.
	ProviderCorrelation map[string]string `json:"provider_correlation,omitempty"`
	// ResultCode is the protocol's stable result/status code, never a body.
	ResultCode string `json:"result_code,omitempty"`
	// EvidenceRef is the existing ledger anchor of the origin evidence for this
	// stage, when the producer already anchored one. It is a POINTER for
	// verification; it is not, and never becomes, a receipt.
	EvidenceRef string `json:"evidence_ref,omitempty"`
}

// AccessEvidenceEventType implements AccessEvidenceContent.
func (ActionObservationContent) AccessEvidenceEventType() string { return EventTypeActionObservation }
func (ActionObservationContent) isAccessEvidenceContent()        {}

// AuthorizationDecisionContent is what an evaluator actually decided, with the
// inputs it actually used. It is never derived backwards from an edge, and a
// later reconstruction is a different record with its own purpose and inputs.
type AuthorizationDecisionContent struct {
	SchemaVersion int `json:"schema_version"`
	// Question is the exact question that was evaluated.
	Question AccessQuestion `json:"question"`
	// Purpose separates a live decision from a reconstruction or a what-if.
	Purpose DecisionPurpose `json:"purpose"`
	// Evaluator and EvaluatorVersion identify what decided.
	Evaluator        string `json:"evaluator"`
	EvaluatorVersion string `json:"evaluator_version"`
	// Outcome is the evaluator's conclusion; Disposition is the effective
	// disposition applied at the enforcement point when the protocol has one. A
	// shadow decision has NO effective disposition and must leave it empty.
	Outcome     AccessDecisionOutcome `json:"outcome"`
	Disposition DecisionDisposition   `json:"disposition,omitempty"`
	// Shadow marks an observe-mode evaluation that did not enforce.
	Shadow bool `json:"shadow"`
	// ReasonCode is a stable machine-readable reason. An unreachable policy and
	// a policy denial are different codes and different outcomes.
	ReasonCode string `json:"reason_code"`
	// Obligations are the obligations the decision attached, as non-secret
	// strings.
	Obligations map[string]string `json:"obligations,omitempty"`
	// Inputs are the typed dependencies the evaluator consumed. Every
	// DependencyPolicyArtifact ref is resolved inside the pinned tenant.
	Inputs []AccessDependency `json:"inputs,omitempty"`
	// ReplayCompleteness is the producer's claim about re-evaluability. The
	// engine verifies a ReplayComplete claim against the availability of every
	// required policy-artifact input and refuses an overclaim.
	ReplayCompleteness ReplayCompleteness `json:"replay_completeness"`
	// ValidFrom/ValidUntil bound the decision's validity window, in canonical
	// UTC timestamp text. Empty means the protocol declares none.
	ValidFrom  string `json:"valid_from,omitempty"`
	ValidUntil string `json:"valid_until,omitempty"`
	// AuthorizationPoint names WHERE the decision was taken (e.g.
	// "mcp.gateway.preflight"), because the limits of a decision are the limits
	// of its enforcement point.
	AuthorizationPoint string `json:"authorization_point"`
	// AuthorityEpoch, ClaimHolder, ClaimFence, ClaimGeneration and ProfileState
	// are recorded only by surfaces that use them; empty means "does not apply
	// on this surface", not "unknown".
	AuthorityEpoch  int64  `json:"authority_epoch,omitempty"`
	ClaimHolder     string `json:"claim_holder,omitempty"`
	ClaimFence      int64  `json:"claim_fence,omitempty"`
	ClaimGeneration int64  `json:"claim_generation,omitempty"`
	ProfileState    string `json:"profile_state,omitempty"`
	// ApprovalRef and DelegationRef point at an approval or delegation the
	// decision relied on.
	ApprovalRef   string `json:"approval_ref,omitempty"`
	DelegationRef string `json:"delegation_ref,omitempty"`
	// OperationID and EffectDigest bind the decision to the journaled operation
	// it governed. A reconstruction may NOT carry them: it did not govern
	// anything.
	OperationID  OperationID  `json:"operation_id,omitempty"`
	EffectDigest EffectDigest `json:"effect_digest,omitempty"`
	// EvidenceRef is the ledger anchor the producer already holds for this
	// decision, a pointer for verification and not a receipt.
	EvidenceRef string `json:"evidence_ref,omitempty"`
}

// AccessEvidenceEventType implements AccessEvidenceContent.
func (AuthorizationDecisionContent) AccessEvidenceEventType() string {
	return EventTypeAuthorizationDecision
}
func (AuthorizationDecisionContent) isAccessEvidenceContent() {}

// ---------------------------------------------------------------------------
// Envelope and canonical digests
// ---------------------------------------------------------------------------

// AccessEvidenceEnvelope is the provenance every record carries: which producer
// instance emitted it, under which original event id and event type, with which
// adapter version, and when the fact occurred.
//
// The tenant is deliberately ABSENT. The host stamps and verifies the tenant
// from the effective configuration and authentication; a payload that could
// name its own tenant would be a payload that could write into another one.
//
// RecordedAt is likewise absent: the arrival instant is store-assigned, and
// including it would make an at-least-once redelivery of the same fact hash
// differently and read as a conflicting record.
type AccessEvidenceEnvelope struct {
	// SchemaVersion is AccessEvidenceSchemaVersion at production time.
	SchemaVersion int `json:"schema_version"`
	// ProducerInstance is the registered instance that emitted the record.
	ProducerInstance string `json:"producer_instance"`
	// SourceEventID is the producer's ORIGINAL event id — the thing that makes a
	// redelivery recognizable. A source without a stable id declares its
	// deduplication limited rather than having one guessed for it.
	SourceEventID string `json:"source_event_id"`
	// EventType is the family's closed event type; it must equal the content's
	// AccessEvidenceEventType.
	EventType string `json:"event_type"`
	// AdapterVersion is the version of the adapter that produced the record.
	AdapterVersion string `json:"adapter_version"`
	// OccurredAt is when the fact happened, in canonical UTC timestamp text. The
	// bus never substitutes a redelivery instant for it.
	OccurredAt string `json:"occurred_at"`
}

// Valid reports whether the envelope names a complete, deduplicable identity.
func (e AccessEvidenceEnvelope) Valid() bool {
	return e.SchemaVersion > 0 &&
		strings.TrimSpace(e.ProducerInstance) != "" &&
		strings.TrimSpace(e.SourceEventID) != "" &&
		strings.TrimSpace(e.EventType) != "" &&
		strings.TrimSpace(e.OccurredAt) != ""
}

// accessRecordPreimage is the exact structure hashed by RecordDigest. It is a
// named type rather than an inline literal so the hashed shape is a declared,
// reviewable contract instead of a side effect of a call site.
type accessRecordPreimage struct {
	Envelope AccessEvidenceEnvelope `json:"envelope"`
	Content  AccessEvidenceContent  `json:"content"`
}

// RecordDigest returns the canonical, domain-separated digest of one record:
// its envelope (minus the store-assigned arrival instant) and its content.
//
// It is what makes idempotency decidable. A redelivery of the same fact
// produces the same digest and is a duplicate; the same identity with different
// bytes produces a different digest and is a conflict, never a silent
// overwrite of evidence.
//
// It refuses an envelope whose EventType disagrees with the content's family:
// the identity tuple would then name a family the bytes are not from.
func RecordDigest(env AccessEvidenceEnvelope, content AccessEvidenceContent) (string, error) {
	if content == nil {
		return "", fmt.Errorf("access evidence: record digest needs content")
	}
	if !env.Valid() {
		return "", fmt.Errorf("access evidence: record digest needs a complete envelope")
	}
	if env.EventType != content.AccessEvidenceEventType() {
		return "", fmt.Errorf("access evidence: envelope event type %q does not match content family %q",
			env.EventType, content.AccessEvidenceEventType())
	}
	return canonicalDigest(domainAccessRecord, accessRecordPreimage{Envelope: env, Content: content})
}

// ArtifactContentDigest returns the canonical digest of a policy artifact's
// normative bytes. It is domain-separated from the record digest so an artifact
// body can never be replayed as a record, and it is computed over the exact
// stored bytes so the check survives storage.
func ArtifactContentDigest(content []byte) string {
	h := sha256.New()
	writeLengthPrefixed(h, []byte(domainAccessArtifact))
	writeLengthPrefixed(h, content)
	return hex.EncodeToString(h.Sum(nil))
}

// canonicalDigest hashes a domain separator and the canonical JSON encoding of
// v. The domain is LENGTH-PREFIXED, so no domain can be confused with the start
// of a payload, and the payload is length-prefixed for the same reason.
//
// Canonical JSON is exact here rather than approximately stable: every field of
// every shape in this file is a string, an int, a bool, a slice of those, or a
// map[string]string — Go's encoder emits struct fields in declaration order and
// map keys sorted, and there are no floats whose formatting could vary. HTML
// escaping is disabled so a reference containing <, > or & hashes as itself.
func canonicalDigest(domain string, v any) (string, error) {
	payload, err := canonicalJSON(v)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	writeLengthPrefixed(h, []byte(domain))
	writeLengthPrefixed(h, payload)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// canonicalJSON encodes v deterministically, without HTML escaping and without
// the trailing newline json.Encoder appends.
func canonicalJSON(v any) ([]byte, error) {
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("access evidence: canonical encoding: %w", err)
	}
	return []byte(strings.TrimSuffix(sb.String(), "\n")), nil
}

// writeLengthPrefixed writes u32be(len(b)) followed by b, closing the
// "concatenate two fields to forge a third" ambiguity.
func writeLengthPrefixed(h interface{ Write([]byte) (int, error) }, b []byte) {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(b)))
	_, _ = h.Write(n[:])
	_, _ = h.Write(b)
}
