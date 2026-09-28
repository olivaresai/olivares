// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// accessevidence.go — the sqlstore half of the v26.9 access-evidence contract
// (store.AccessEvidenceRepo). Four relations, one per normative family, all
// append-only and all carrying the same provenance and idempotency identity.
//
// # Why four relations rather than one
//
// A policy artifact, an authority transition, an observed stage and a decision
// are different facts with different constraints, and merging them into a
// generic "evidence" table would put the vocabulary of each family beyond the
// reach of a CHECK constraint — the one enforcement that survives an
// out-of-band write. Keeping them apart is also what keeps the reads honest:
// nothing joins an observation to a decision unless a producer recorded that
// link.
//
// # Why the tables are descriptors like every other core entity
//
// The descriptors are the single source of truth (catalog.go): a FRESH database
// creates these tables in the v2 "core_entities" migration, and an EXISTING
// database — one migrated by a build that predates them — has each created
// whole, with its guards and indexes, in one transaction by reconcileColumns.
// No hand-authored migration and no new core version is needed, and no existing
// relation is altered: AccessEdge, the evidence journal and the audit chain are
// untouched.
//
// AppendOnly is likewise not decoration. It makes the dialect emit the
// immutability triggers on BOTH engines, puts the relations in
// registry.appendOnlyTables() — which drives the PostgreSQL append-only ACL
// revoke, its verification, and the guard rollout census — and retains their
// rows when a tenant is dropped. A forbidden UPDATE or DELETE therefore fails at
// the engine, not at a repository method a future caller could bypass.

// The access-evidence relation names.
const (
	policyArtifactTable        = "policy_artifacts"
	authorityTransitionTable   = "authority_transitions"
	actionObservationTable     = "action_observations"
	authorizationDecisionTable = "authorization_decisions"
)

// The provenance columns every access-evidence relation carries. They are named
// constants because four descriptors, four codecs and the shared append path
// all have to agree on one spelling.
const (
	colAESchemaVersion = "schema_version"
	colAEProducer      = "producer_instance"
	colAESourceEventID = "source_event_id"
	colAEEventType     = "event_type"
	colAEAdapter       = "adapter_version"
	colAEOccurredAt    = "occurred_at"
	colAERecordedAt    = "recorded_at"
	colAERecordDigest  = "record_digest"
	colAELedgerRef     = "ledger_ref"
	colAEContent       = "content"
)

// accessEvidenceProvenanceFields returns the provenance columns shared by all
// four families, in one place so the four tables cannot drift apart.
//
// producer_instance and record_digest are indexed because both are the axis of
// a real question — "what did this producer send" and "is this exact fact
// already here" — and occurred_at because a reader walks these records by when
// the fact happened, never by when the row arrived.
//
// content is the family's declaration of its content column, whose Go type
// differs per family.
func accessEvidenceProvenanceFields(content *model.ColumnDecl) []model.FieldSpec {
	return []model.FieldSpec{
		field(colAESchemaVersion, model.KindInt, false),
		pdecl(indexedField(colAEProducer, model.KindText, false),
			model.None("the registered producer instance the host attributes the record to: core/model/accessevidence.go:84")),
		pdecl(field(colAESourceEventID, model.KindText, false),
			model.None("the producer's original event id: core/model/accessevidence.go:88, sdk/accessevidence.go:938")),
		pdecl(field(colAEEventType, model.KindText, false),
			model.None("the family's closed event type: sdk/accessevidence.go:623, core/store/accessevidence.go:232")),
		pdecl(field(colAEAdapter, model.KindText, false),
			model.None("the producing adapter's version: core/model/accessevidence.go:95, core/store/accessevidence.go:235")),
		indexedField(colAEOccurredAt, model.KindTimestamp, false),
		field(colAERecordedAt, model.KindTimestamp, false),
		pdecl(indexedField(colAERecordDigest, model.KindText, false),
			model.None("the canonical digest of envelope and content: core/model/accessevidence.go:104, sdk/accessevidence.go:990")),
		pdecl(field(colAELedgerRef, model.KindText, false),
			model.None("the hex chain hash of the anchoring ledger event, a pointer that confers nothing: core/model/accessevidence.go:108")),
		pdecl(field(colAEContent, model.KindJSON, false), content),
	}
}

// accessEvidenceFields prepends the shared provenance columns, with the
// family's content declaration, to a family's own columns.
func accessEvidenceFields(content *model.ColumnDecl, own ...model.FieldSpec) []model.FieldSpec {
	return append(accessEvidenceProvenanceFields(content), own...)
}

// accessEvidenceIngestIndex is the DB-level ground truth of idempotency: one
// record per (tenant, producer instance, source event id, event type).
//
// The event type is part of the key rather than implied by the relation because
// it is what makes the identity globally disjoint: each family's CHECK pins its
// own single value, so the four indexes together behave as one key over all
// access evidence, and a producer that emits an observation and a decision from
// the same source event stores both without either displacing the other.
func accessEvidenceIngestIndex(table string) model.IndexSpec {
	return model.IndexSpec{
		Name:    table + "_ingest_uniq",
		Columns: []string{model.ColTenantID, colAEProducer, colAESourceEventID, colAEEventType},
		Unique:  true,
	}
}

// vocabCheck renders a portable "column IN (...)" CHECK from a closed
// vocabulary, so the constraint is built FROM the Go constants and cannot drift
// from them.
func vocabCheck(column string, values ...string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = "'" + v + "'"
	}
	return fmt.Sprintf("%s IN (%s)", column, strings.Join(quoted, ","))
}

// nullableVocabCheck is vocabCheck for a column whose absence is meaningful: it
// permits NULL and refuses any non-NULL value outside the vocabulary. A CHECK
// that forgot the NULL arm would refuse every row that legitimately has no
// value, so the two arms are written together.
func nullableVocabCheck(column string, values ...string) string {
	return fmt.Sprintf("%s IS NULL OR %s", column, vocabCheck(column, values...))
}

// accessEvidenceDescriptors returns the four relations in creation order.
func accessEvidenceDescriptors() []model.EntityDescriptor {
	return []model.EntityDescriptor{
		policyArtifactDescriptor,
		authorityTransitionDescriptor,
		actionObservationDescriptor,
		authorizationDecisionDescriptor,
	}
}

// policyArtifactDescriptor is the immutable policy artifact relation.
//
// availability is indexed because "which of my retained artifacts can actually
// be re-evaluated" is the question a completeness check asks of every required
// dependency, and it is a filter over exactly this column.
var policyArtifactDescriptor = model.EntityDescriptor{
	Kind:       model.PolicyArtifactKind,
	Table:      policyArtifactTable,
	AppendOnly: true,
	Fields: accessEvidenceFields(pdeclAEArtifactContent,
		pdecl(indexedField("authority_id", model.KindText, false), pdeclNoneAEAuthorityID),
		pdecl(indexedField("surface", model.KindText, false), pdeclNoneAESurface),
		pdecl(field("engine", model.KindText, false), pdeclNoneAEEngine),
		pdecl(indexedField("artifact_digest", model.KindText, false), pdeclNoneAEArtifactDigest),
		pdecl(field("origin", model.KindText, false), pdeclNoneAEOrigin),
		pdecl(indexedField("availability", model.KindText, false), pdeclNoneAEAvailability),
		pdecl(field("governance_surface", model.KindText, true), pdeclNoneAEGovSurface),
		field("governance_revision", model.KindInt, true),
	),
	Indexes: []model.IndexSpec{accessEvidenceIngestIndex(policyArtifactTable)},
	Checks: []string{
		vocabCheck(colAEEventType, sdk.EventTypePolicyArtifact),
		vocabCheck("origin",
			string(sdk.OriginLocalAuthoritative), string(sdk.OriginRemoteAuthenticatedSnapshot),
			string(sdk.OriginOperatorDeclaration), string(sdk.OriginUnverified)),
		vocabCheck("availability",
			string(sdk.AvailabilityRetained), string(sdk.AvailabilityExternalRetained),
			string(sdk.AvailabilityExternalRestricted), string(sdk.AvailabilityAbsent)),
	},
}

// authorityTransitionDescriptor is the append-only authority transition
// relation.
var authorityTransitionDescriptor = model.EntityDescriptor{
	Kind:       model.AuthorityTransitionKind,
	Table:      authorityTransitionTable,
	AppendOnly: true,
	Fields: accessEvidenceFields(pdeclAETransitionContent,
		pdecl(field("subject_kind", model.KindText, false), pdeclNoneAESubjectKind),
		pdecl(indexedField("subject_ref", model.KindText, false), pdeclNoneAESubjectRef),
		pdecl(indexedField("subject_artifact_id", model.KindUUID, true),
			model.None("the artifact row the store resolved inside the tenant, zero for grant and binding subjects: core/model/accessevidence.go:177")),
		pdecl(field("transition", model.KindText, false), pdeclNoneAETransition),
		field("authority_sequence", model.KindInt, true),
		indexedField("effective_at", model.KindTimestamp, false),
		field("effective_until", model.KindTimestamp, true),
		field("known_at", model.KindTimestamp, false),
		pdecl(field("snapshot_completeness", model.KindText, false), pdeclNoneAESnapshot),
	),
	Indexes: []model.IndexSpec{accessEvidenceIngestIndex(authorityTransitionTable)},
	Checks: []string{
		vocabCheck(colAEEventType, sdk.EventTypeAuthorityTransition),
		vocabCheck("subject_kind",
			string(sdk.SubjectPolicyArtifact), string(sdk.SubjectGrant), string(sdk.SubjectBinding)),
		vocabCheck("transition",
			string(sdk.TransitionActivate), string(sdk.TransitionSupersede),
			string(sdk.TransitionDeactivate), string(sdk.TransitionRevoke), string(sdk.TransitionExpire)),
		vocabCheck("snapshot_completeness",
			string(sdk.SnapshotCompleteWithinScope), string(sdk.SnapshotPartial),
			string(sdk.SnapshotUnknown), string(sdk.SnapshotUnavailable)),
	},
}

// actionObservationDescriptor is the append-only observed-stage relation.
//
// The confirmation CHECK is the database half of the rule that a transport
// acknowledgement may not present itself as a durable effect: the column is
// constrained to the three confirmation levels, and the codec additionally
// requires it exactly when the stage is effect_confirmed — a cross-column rule
// a portable CHECK cannot express.
var actionObservationDescriptor = model.EntityDescriptor{
	Kind:       model.ActionObservationKind,
	Table:      actionObservationTable,
	AppendOnly: true,
	Fields: accessEvidenceFields(pdeclAEObservationContent,
		pdecl(indexedField("question_digest", model.KindText, false), pdeclNoneAEQuestionDigest),
		pdecl(indexedField("stage", model.KindText, false), pdeclNoneAEStage),
		pdecl(field("mediation", model.KindText, false), pdeclNoneAEMediation),
		pdecl(field("prevention", model.KindText, false), pdeclNoneAEPrevention),
		pdecl(field("confirmation", model.KindText, true), pdeclNoneAEConfirmation),
		pdecl(indexedField("principal_ref", model.KindText, true), pdeclScanAEEvidence),
		pdecl(indexedField("resource_ref", model.KindText, true), pdeclNoneAEResource),
		pdecl(indexedField("action", model.KindText, false), pdeclNoneAEAction),
		pdecl(indexedField("operation_id", model.KindText, true), pdeclNoneAEOperation),
		pdecl(field("effect_digest", model.KindText, true), pdeclNoneAEEffect),
		pdecl(indexedField("parent_observation_id", model.KindUUID, true), pdeclNoneAEResolvedRow),
		pdecl(indexedField("decision_id", model.KindUUID, true), pdeclNoneAEResolvedRow),
	),
	Indexes: []model.IndexSpec{accessEvidenceIngestIndex(actionObservationTable)},
	Checks: []string{
		vocabCheck(colAEEventType, sdk.EventTypeActionObservation),
		vocabCheck("stage",
			string(sdk.StageRequested), string(sdk.StageAttempted), string(sdk.StageDispatched),
			string(sdk.StageEffectConfirmed), string(sdk.StageFailed), string(sdk.StageNotSent),
			string(sdk.StageBlocked), string(sdk.StageOutcomeUnknown),
			string(sdk.StageResponseReleased), string(sdk.StageResponseWithheld)),
		vocabCheck("mediation",
			string(sdk.MediationOlivaresPEP), string(sdk.MediationExternalObserver)),
		vocabCheck("prevention",
			string(sdk.PreventionCanPrevent), string(sdk.PreventionCannotPrevent), string(sdk.PreventionUnknown)),
		nullableVocabCheck("confirmation",
			string(sdk.ConfirmationReceipt), string(sdk.ConfirmationResponse), string(sdk.ConfirmationDurableEffect)),
	},
}

// authorizationDecisionDescriptor is the append-only decision relation.
var authorizationDecisionDescriptor = model.EntityDescriptor{
	Kind:       model.AuthorizationDecisionKind,
	Table:      authorizationDecisionTable,
	AppendOnly: true,
	Fields: accessEvidenceFields(pdeclAEDecisionContent,
		pdecl(indexedField("question_digest", model.KindText, false), pdeclNoneAEQuestionDigest),
		pdecl(field("purpose", model.KindText, false), pdeclNoneAEPurpose),
		pdecl(indexedField("outcome", model.KindText, false), pdeclNoneAEOutcome),
		pdecl(field("disposition", model.KindText, true), pdeclNoneAEDisposition),
		field("shadow", model.KindBool, false),
		pdecl(indexedField("evaluator", model.KindText, false), pdeclNoneAEEvaluator),
		pdecl(field("replay_completeness", model.KindText, false), pdeclNoneAEReplay),
		pdecl(indexedField("operation_id", model.KindText, true), pdeclNoneAEOperation),
		pdecl(field("effect_digest", model.KindText, true), pdeclNoneAEEffect),
	),
	Indexes: []model.IndexSpec{accessEvidenceIngestIndex(authorizationDecisionTable)},
	Checks: []string{
		vocabCheck(colAEEventType, sdk.EventTypeAuthorizationDecision),
		vocabCheck("purpose",
			string(sdk.PurposeLiveAuthorization), string(sdk.PurposeHistoricalReconstruction),
			string(sdk.PurposeCurrentWhatIf)),
		vocabCheck("outcome",
			string(sdk.AccessOutcomeAllow), string(sdk.AccessOutcomeDeny), string(sdk.AccessOutcomeIndeterminate)),
		nullableVocabCheck("disposition",
			string(sdk.DispositionAllow), string(sdk.DispositionDeny),
			string(sdk.DispositionAsk), string(sdk.DispositionModify)),
		vocabCheck("replay_completeness",
			string(sdk.ReplayComplete), string(sdk.ReplayIncomplete), string(sdk.ReplayUnknown)),
	},
}

// The declarations of what the access-evidence columns and content leaves say
// about principals. Every family is retained evidence: no live producer writes
// these relations yet and no reader turns a stored value into authority; the
// replay reader rebuilds a request evaluated only against the retained artifact
// (modules/governance/replay.go:354-377). Principal-bearing strings are
// therefore evidence, scanned as text because their issuer namespaces vary.
var (
	pdeclScanAEEvidence = model.Scan(model.ClassEvidence)

	pdeclNoneAEAuthorityID    = model.None("the issuer the producer names, not accepted as proof of authority: core/model/accessevidence.go:150, sdk/accessevidence.go:640")
	pdeclNoneAESurface        = model.None("the policy surface label: sdk/accessevidence.go:645")
	pdeclNoneAEEngine         = model.None("the evaluator that consumes the artifact: sdk/accessevidence.go:647, modules/governance/replay.go:173")
	pdeclNoneAEArtifactDigest = model.None("the digest of the artifact bytes, recomputed over retained content: core/store/accessevidence.go:374")
	pdeclNoneAEOrigin         = model.None("a provenance class from the closed vocabulary: core/store/accessevidence.go:334")
	pdeclNoneAEAvailability   = model.None("a reconstructibility state from the closed vocabulary: core/store/accessevidence.go:336")
	pdeclNoneAEGovSurface     = model.None("a local policy-revision surface name: sdk/accessevidence.go:657, core/store/accessevidence.go:291")

	pdeclNoneAESubjectKind = model.None("an authority subject kind limited to policy artifact, grant or binding: core/store/accessevidence.go:411")
	pdeclNoneAESubjectRef  = model.None("the artifact, grant or binding the transition names, never an account: sdk/accessevidence.go:700, core/model/accessevidence.go:177")
	pdeclNoneAETransition  = model.None("an authority event from the closed vocabulary: core/store/accessevidence.go:415")
	pdeclNoneAESnapshot    = model.None("a snapshot completeness from the closed vocabulary: core/store/accessevidence.go:417")
	pdeclNoneAETimestamp   = model.None("canonical UTC timestamp text: sdk/accessevidence.go:718, core/store/accessevidence.go:425, core/store/accessevidence.go:546")
	pdeclNoneAEReasonCode  = model.None("a stable machine-readable reason code, never prose: sdk/accessevidence.go:722, sdk/accessevidence.go:805")
	pdeclNoneAEMapKey      = model.None("the name of a scope, condition, obligation or context entry: sdk/accessevidence.go:666, sdk/accessevidence.go:724, sdk/accessevidence.go:808, sdk/accessevidence.go:533")

	pdeclNoneAEQuestionDigest = model.None("the canonical digest of the question: core/model/accessevidence.go:192, sdk/accessevidence.go:564")
	pdeclNoneAEStage          = model.None("an observed stage from the closed vocabulary: core/store/accessevidence.go:463")
	pdeclNoneAEMediation      = model.None("a mediation label from the closed vocabulary: core/store/accessevidence.go:465")
	pdeclNoneAEPrevention     = model.None("a prevention capability from the closed vocabulary: core/store/accessevidence.go:467")
	pdeclNoneAEConfirmation   = model.None("an effect confirmation level from the closed vocabulary: core/store/accessevidence.go:478")
	pdeclNoneAEResource       = model.None("the resource named inside its origin scope: sdk/accessevidence.go:518, modules/governance/replay.go:374")
	pdeclNoneAEAction         = model.None("the protocol's exact operation, its vocabulary version or a coarse mode: sdk/accessevidence.go:525, core/store/accessevidence.go:260")
	pdeclNoneAEOperation      = model.None("an evidence-journal operation id, a single-use idempotency id: sdk/accessevidence.go:759, sdk/evidence.go:74")
	pdeclNoneAEEffect         = model.None("an opaque digest of the governed effect binding: sdk/accessevidence.go:759, sdk/evidence.go:86")
	pdeclNoneAEResolvedRow    = model.None("an observation or decision row the store resolved inside the tenant: core/model/accessevidence.go:196")
	pdeclNoneAEEvidenceRef    = model.None("a ledger anchor pointer for verification, not a receipt: sdk/accessevidence.go:776, sdk/accessevidence.go:843")

	pdeclNoneAEPurpose     = model.None("a decision purpose from the closed vocabulary: core/store/accessevidence.go:507")
	pdeclNoneAEOutcome     = model.None("a decision outcome from the closed vocabulary: core/store/accessevidence.go:509")
	pdeclNoneAEDisposition = model.None("an effective disposition from the closed vocabulary: core/store/accessevidence.go:511")
	pdeclNoneAEEvaluator   = model.None("the evaluator name or version that decided: core/store/accessevidence.go:515")
	pdeclNoneAEReplay      = model.None("a replay completeness claim from the closed vocabulary: core/store/accessevidence.go:513")
)

// pdeclAEQuestionLeaves classifies every occurrence of the normalized access
// question inside a content document.
var pdeclAEQuestionLeaves = model.TypeLeaves(sdk.AccessQuestion{},
	model.Leaf("principal_kind", model.None("an open principal kind label, read only to shape a replayed request: sdk/accessevidence.go:481, modules/governance/replay.go:357")),
	model.Leaf("principal_ref", pdeclScanAEEvidence),
	model.Leaf("issuer", model.None("the identity issuer that authenticated the principal: sdk/accessevidence.go:486")),
	model.Leaf("credential_class", model.None("a credential kind label, never a credential value: sdk/accessevidence.go:488")),
	model.Leaf("credential_ref", pdeclScanAEEvidence),
	model.Leaf("actor_ref", pdeclScanAEEvidence),
	model.Leaf("subject_ref", pdeclScanAEEvidence),
	model.Leaf("delegation_refs[]", pdeclScanAEEvidence),
	model.Leaf("agent_ref", pdeclNoneAEBindingRef),
	model.Leaf("session_ref", pdeclNoneAEBindingRef),
	model.Leaf("run_ref", pdeclNoneAEBindingRef),
	model.Leaf("profile_ref", pdeclNoneAEBindingRef),
	model.Leaf("environment_ref", pdeclNoneAEBindingRef),
	model.Leaf("workspace_ref", pdeclNoneAEBindingRef),
	model.Leaf("source_instance", pdeclNoneAEOriginScope),
	model.Leaf("endpoint", pdeclNoneAEOriginScope),
	model.Leaf("namespace", pdeclNoneAEOriginScope),
	model.Leaf("resource_kind", pdeclNoneAEResource),
	model.Leaf("resource_ref", pdeclNoneAEResource),
	model.Leaf("resource_attributes_digest", model.None("a digest of the resource attribute snapshot: sdk/accessevidence.go:521")),
	model.Leaf("action", pdeclNoneAEAction),
	model.Leaf("action_vocabulary", pdeclNoneAEAction),
	model.Leaf("mode", pdeclNoneAEAction),
	model.Leaf("context{key}", pdeclNoneAEMapKey),
	model.Leaf("context{}", pdeclScanAEEvidence),
)

// pdeclNoneAEBindingRef and pdeclNoneAEOriginScope are the question's
// non-principal references.
var (
	pdeclNoneAEBindingRef  = model.None("a binding-demonstrated agent, session, run, profile, environment or workspace reference, never an account: sdk/accessevidence.go:501")
	pdeclNoneAEOriginScope = model.None("the origin scope that makes the resource reference canonical: sdk/accessevidence.go:513")
)

// pdeclAEDependencyLeaves classifies every typed input a record names.
var pdeclAEDependencyLeaves = model.TypeLeaves(sdk.AccessDependency{},
	model.Leaf("kind", model.None("a dependency kind label: sdk/accessevidence.go:572, sdk/accessevidence.go:584")),
	model.Leaf("ref", model.None("a tenant-scoped reference to a policy artifact, fact, role, membership or binding input, never an account: sdk/accessevidence.go:574, sdk/accessevidence.go:584")),
	model.Leaf("digest", model.None("a digest binding the input's exact content: sdk/accessevidence.go:576")),
	model.Leaf("version", model.None("the input's own version or sequence: sdk/accessevidence.go:578")),
)

// The content declarations of the four families.
var (
	pdeclAEArtifactContent = model.Nested(sdk.PolicyArtifactContent{}, model.ClassEvidence,
		model.Leaf("authority_id", pdeclNoneAEAuthorityID),
		model.Leaf("surface", pdeclNoneAESurface),
		model.Leaf("engine", pdeclNoneAEEngine),
		model.Leaf("native_version", model.None("the issuer's own version string: sdk/accessevidence.go:649")),
		model.Leaf("artifact_digest", pdeclNoneAEArtifactDigest),
		model.Leaf("digest_algorithm", model.None("the digest algorithm name: core/store/accessevidence.go:370")),
		model.Leaf("governance_surface", pdeclNoneAEGovSurface),
		model.Leaf("origin", pdeclNoneAEOrigin),
		model.Leaf("authorized_scope{key}", pdeclNoneAEMapKey),
		model.Leaf("authorized_scope{}", pdeclScanAEEvidence),
		model.Leaf("availability", pdeclNoneAEAvailability),
		// content is retained policy text, evaluated only by a replay.
		model.Leaf("content", pdeclScanAEEvidence),
		model.Leaf("content_ref", model.None("a durable reference to the protected store that holds the artifact: sdk/accessevidence.go:674")),
		pdeclAEDependencyLeaves,
	)
	pdeclAETransitionContent = model.Nested(sdk.AuthorityTransitionContent{}, model.ClassEvidence,
		model.Leaf("subject_kind", pdeclNoneAESubjectKind),
		model.Leaf("subject_ref", pdeclNoneAESubjectRef),
		model.Leaf("transition", pdeclNoneAETransition),
		model.Leaf("governance_surface", pdeclNoneAEGovSurface),
		model.Leaf("effective_at", pdeclNoneAETimestamp),
		model.Leaf("effective_until", pdeclNoneAETimestamp),
		model.Leaf("known_at", pdeclNoneAETimestamp),
		model.Leaf("reason_code", pdeclNoneAEReasonCode),
		model.Leaf("snapshot_completeness", pdeclNoneAESnapshot),
		model.Leaf("snapshot_scope{key}", pdeclNoneAEMapKey),
		model.Leaf("snapshot_scope{}", pdeclScanAEEvidence),
		model.Leaf("conditions{key}", pdeclNoneAEMapKey),
		model.Leaf("conditions{}", pdeclScanAEEvidence),
	)
	pdeclAEObservationContent = model.Nested(sdk.ActionObservationContent{}, model.ClassEvidence,
		pdeclAEQuestionLeaves,
		model.Leaf("stage", pdeclNoneAEStage),
		model.Leaf("mediation", pdeclNoneAEMediation),
		model.Leaf("prevention", pdeclNoneAEPrevention),
		model.Leaf("prevention_detail{key}", pdeclNoneAEPreventionDetail),
		model.Leaf("prevention_detail{}", pdeclNoneAEPreventionDetail),
		model.Leaf("confirmation", pdeclNoneAEConfirmation),
		model.Leaf("operation_id", pdeclNoneAEOperation),
		model.Leaf("effect_digest", pdeclNoneAEEffect),
		model.Leaf("parent_ref", pdeclNoneAEResolvedRow),
		model.Leaf("decision_ref", pdeclNoneAEResolvedRow),
		model.Leaf("provider_correlation{key}", pdeclNoneAECorrelation),
		model.Leaf("provider_correlation{}", pdeclNoneAECorrelation),
		model.Leaf("result_code", model.None("the protocol's stable result code, never a body: sdk/accessevidence.go:774")),
		model.Leaf("evidence_ref", pdeclNoneAEEvidenceRef),
	)
	pdeclAEDecisionContent = model.Nested(sdk.AuthorizationDecisionContent{}, model.ClassEvidence,
		pdeclAEQuestionLeaves,
		pdeclAEDependencyLeaves,
		model.Leaf("purpose", pdeclNoneAEPurpose),
		model.Leaf("evaluator", pdeclNoneAEEvaluator),
		model.Leaf("evaluator_version", pdeclNoneAEEvaluator),
		model.Leaf("outcome", pdeclNoneAEOutcome),
		model.Leaf("disposition", pdeclNoneAEDisposition),
		model.Leaf("reason_code", pdeclNoneAEReasonCode),
		model.Leaf("obligations{key}", pdeclNoneAEMapKey),
		model.Leaf("obligations{}", pdeclScanAEEvidence),
		model.Leaf("replay_completeness", pdeclNoneAEReplay),
		model.Leaf("valid_from", pdeclNoneAETimestamp),
		model.Leaf("valid_until", pdeclNoneAETimestamp),
		model.Leaf("authorization_point", model.None("where the decision was taken, an enforcement-point label: sdk/accessevidence.go:822")),
		// claim_holder has no producer yet; it may name the holder of a claim.
		model.Leaf("claim_holder", pdeclScanAEEvidence),
		model.Leaf("profile_state", model.None("a surface profile state label: sdk/accessevidence.go:826")),
		model.Leaf("approval_ref", pdeclNoneAEReliedOn),
		model.Leaf("delegation_ref", pdeclNoneAEReliedOn),
		model.Leaf("operation_id", pdeclNoneAEOperation),
		model.Leaf("effect_digest", pdeclNoneAEEffect),
		model.Leaf("evidence_ref", pdeclNoneAEEvidenceRef),
		model.Leaf("policy_version_id", model.None("the retained artifact id or a surface:revision pair: sdk/accessevidence.go:846, modules/governance/replay.go:297")),
		model.Leaf("inputs_digest", model.None("the canonical digest of the inputs: sdk/accessevidence.go:853, sdk/accessevidence.go:883")),
	)
)

// pdeclNoneAEPreventionDetail, pdeclNoneAECorrelation and pdeclNoneAEReliedOn
// are observation and decision references that name no principal.
var (
	pdeclNoneAEPreventionDetail = model.None("the deployment fact behind a prevention claim, a provider version or hook configuration: sdk/accessevidence.go:752")
	pdeclNoneAECorrelation      = model.None("a provider's own correlation ids (a provider session id, a hook event id), never authentication: sdk/accessevidence.go:770")
	pdeclNoneAEReliedOn         = model.None("an approval or delegation record the decision relied on: sdk/accessevidence.go:834")
)

// ---------------------------------------------------------------------------
// Provenance encode/decode
// ---------------------------------------------------------------------------

// encAccessMeta writes the shared provenance columns of one record.
func encAccessMeta(m model.AccessEvidenceMeta, contentJSON string) model.Record {
	return model.Record{
		colAESchemaVersion: m.SchemaVersion,
		colAEProducer:      m.ProducerInstance,
		colAESourceEventID: m.SourceEventID,
		colAEEventType:     m.EventType,
		colAEAdapter:       m.AdapterVersion,
		colAEOccurredAt:    encTS(m.OccurredAt),
		colAERecordedAt:    encTS(m.RecordedAt),
		colAERecordDigest:  m.RecordDigest,
		colAELedgerRef:     m.LedgerRef,
		colAEContent:       contentJSON,
	}
}

// decAccessMeta reads the shared provenance columns back.
func decAccessMeta(r model.Record) (model.AccessEvidenceMeta, error) {
	occurred, err := decTS(r, colAEOccurredAt)
	if err != nil {
		return model.AccessEvidenceMeta{}, err
	}
	recorded, err := decTS(r, colAERecordedAt)
	if err != nil {
		return model.AccessEvidenceMeta{}, err
	}
	return model.AccessEvidenceMeta{
		SchemaVersion:    r.Int(colAESchemaVersion),
		ProducerInstance: r.String(colAEProducer),
		SourceEventID:    r.String(colAESourceEventID),
		EventType:        r.String(colAEEventType),
		AdapterVersion:   r.String(colAEAdapter),
		OccurredAt:       occurred,
		RecordedAt:       recorded,
		RecordDigest:     r.String(colAERecordDigest),
		LedgerRef:        r.String(colAELedgerRef),
	}, nil
}

// decAccessContent unmarshals the stored canonical JSON into a content DTO.
func decAccessContent(r model.Record, dst any) error {
	raw := r.String(colAEContent)
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("%w: record carries no content", store.ErrAccessEvidenceIntegrity)
	}
	if err := json.Unmarshal([]byte(raw), dst); err != nil {
		return fmt.Errorf("%w: record content does not decode: %v", store.ErrAccessEvidenceIntegrity, err)
	}
	return nil
}

// verifyAccessRecord is the decode-time integrity check every family runs. It
// is the read-side counterpart of the write-side validation, and it is total
// rather than field-by-field: recomputing the record digest over the decoded
// envelope and content detects any divergence between the provenance columns,
// the stored content and the digest the row claims.
//
// It also refuses a row with no ledger anchor. Every stored record was appended
// together with its ledger event in one transaction, so a row without an anchor
// is either a forged insert or a partially applied write, and either way it must
// not decode into a usable fact.
func verifyAccessRecord(m model.AccessEvidenceMeta, content sdk.AccessEvidenceContent) error {
	if strings.TrimSpace(m.LedgerRef) == "" {
		return fmt.Errorf("%w: record %s/%s carries no ledger anchor",
			store.ErrAccessEvidenceIntegrity, m.ProducerInstance, m.SourceEventID)
	}
	digest, err := sdk.RecordDigest(m.Envelope(), content)
	if err != nil {
		return fmt.Errorf("%w: record %s/%s: %v",
			store.ErrAccessEvidenceIntegrity, m.ProducerInstance, m.SourceEventID, err)
	}
	if digest != m.RecordDigest {
		return fmt.Errorf("%w: record %s/%s hashes to %s but the row claims %s",
			store.ErrAccessEvidenceIntegrity, m.ProducerInstance, m.SourceEventID, digest, m.RecordDigest)
	}
	return nil
}

// ---------------------------------------------------------------------------
// The repository
// ---------------------------------------------------------------------------

// accessEvidenceRepo implements store.AccessEvidenceRepo over four tenant-pinned
// generic repositories, the scope's shared audit log (so a record and its ledger
// event ride one chain head in one transaction) and the existing evidence
// journal (consulted, never written).
type accessEvidenceRepo struct {
	artifacts    *genericRepo
	transitions  *genericRepo
	observations *genericRepo
	decisions    *genericRepo
	journal      store.EvidenceOperationRepo
	audit        *auditLog
}

func newAccessEvidenceRepo(
	artifacts, transitions, observations, decisions *genericRepo,
	journal store.EvidenceOperationRepo,
	audit *auditLog,
) *accessEvidenceRepo {
	return &accessEvidenceRepo{
		artifacts: artifacts, transitions: transitions,
		observations: observations, decisions: decisions,
		journal: journal, audit: audit,
	}
}

var _ store.AccessEvidenceRepo = (*accessEvidenceRepo)(nil)

// lookupByKey reads the record already stored under an idempotency identity.
// Read-side availability failures are wrapped like the write side, so an
// unreachable backend classifies as unavailable rather than as "no duplicate".
func lookupByKey(ctx context.Context, repo *genericRepo, key model.AccessEvidenceKey) (model.Record, bool, error) {
	recs, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{
			{Column: colAEProducer, Op: model.OpEq, Value: key.ProducerInstance},
			{Column: colAESourceEventID, Op: model.OpEq, Value: key.SourceEventID},
			{Column: colAEEventType, Op: model.OpEq, Value: key.EventType},
		},
		Limit: 1,
	})
	if err != nil {
		return nil, false, wrapUnavailableErr(err)
	}
	if len(recs) == 0 {
		return nil, false, nil
	}
	return recs[0], true, nil
}

// appendOutcome is the shared result of the ingest path, before the family
// decodes its own row.
type appendOutcome struct {
	rec     model.Record
	fresh   bool
	dropped bool
}

// appendRecord is the ONE ingest path all four families share: validate the
// identity, resolve a duplicate, anchor, insert.
//
// The ordering is the discipline, not an implementation detail:
//
//  1. an existing row with the SAME digest is an exact redelivery — return it,
//     append NOTHING and count nothing. At-least-once delivery must not inflate
//     activity;
//  2. an existing row with a DIFFERENT digest is a conflict. Evidence is never
//     overwritten and the recorded row stands;
//  3. otherwise append the ledger event FIRST, inside the caller's transaction.
//     A real append error rolls the whole thing back (deny-closed); a DEGRADE
//     Seq==0 drop stages NO row and reports Dropped, so the loss accounting the
//     store already staged commits (the F9 discipline of sdk/evidence.go) and the
//     fact is treated as unrecorded rather than recorded-without-an-anchor;
//  4. insert the row with the SAME id the ledger event targeted, so the anchor
//     and the record point at each other.
//
// A concurrent duplicate that passes step 1 loses the UNIQUE index at step 4 and
// returns ErrAccessEvidenceRaced: the caller's Mutate rolls the losing
// transaction back whole — its ledger append included — and a re-run resolves at
// step 1 or 2 against the committed winner.
func (r *accessEvidenceRepo) appendRecord(
	ctx context.Context,
	repo *genericRepo,
	kind model.Kind,
	in store.AccessEvidenceAppend,
	content sdk.AccessEvidenceContent,
	own model.Record,
) (appendOutcome, error) {
	if repo.readOnly {
		return appendOutcome{}, store.ErrReadOnly
	}
	digest, err := sdk.RecordDigest(in.Envelope, content)
	if err != nil {
		return appendOutcome{}, fmt.Errorf("%w: %v", store.ErrAccessEvidenceInvalid, err)
	}
	key := model.AccessEvidenceKey{
		ProducerInstance: in.Envelope.ProducerInstance,
		SourceEventID:    in.Envelope.SourceEventID,
		EventType:        in.Envelope.EventType,
	}
	prior, found, err := lookupByKey(ctx, repo, key)
	if err != nil {
		return appendOutcome{}, err
	}
	if found {
		if prior.String(colAERecordDigest) == digest {
			return appendOutcome{rec: prior}, nil // exact redelivery; nothing appended
		}
		return appendOutcome{}, fmt.Errorf(
			"%w: producer %s event %s type %s: recorded %s, delivered %s",
			store.ErrAccessEvidenceConflict, key.ProducerInstance, key.SourceEventID, key.EventType,
			prior.String(colAERecordDigest), digest)
	}

	id := model.NewID()
	ev, err := r.audit.Append(ctx, model.AuditDraft{
		Actor:      in.Actor,
		ActorKind:  in.ActorKind,
		Action:     kind.Name() + ".record",
		TargetKind: kind,
		TargetID:   id,
		Meta: map[string]any{
			"producer_instance": key.ProducerInstance,
			"source_event_id":   key.SourceEventID,
			"event_type":        key.EventType,
			"record_digest":     digest,
		},
	})
	if err != nil {
		return appendOutcome{}, err // block-mode spool-full / write fault ⇒ rollback, deny-closed
	}
	if ev.Seq == 0 {
		return appendOutcome{dropped: true}, nil // commit the gap; the record is NOT stored
	}

	occurred, err := model.ParseTimestamp(in.Envelope.OccurredAt)
	if err != nil {
		return appendOutcome{}, fmt.Errorf("%w: %v", store.ErrAccessEvidenceInvalid, err)
	}
	contentJSON, err := json.Marshal(content)
	if err != nil {
		return appendOutcome{}, fmt.Errorf("%w: content does not encode: %v", store.ErrAccessEvidenceInvalid, err)
	}
	meta := model.AccessEvidenceMeta{
		SchemaVersion:    int64(in.Envelope.SchemaVersion),
		ProducerInstance: key.ProducerInstance,
		SourceEventID:    key.SourceEventID,
		EventType:        key.EventType,
		AdapterVersion:   in.Envelope.AdapterVersion,
		OccurredAt:       occurred,
		RecordedAt:       repo.clock.Now(),
		RecordDigest:     digest,
		LedgerRef:        hex.EncodeToString(ev.Hash),
	}
	rec := encAccessMeta(meta, string(contentJSON))
	for k, v := range own {
		rec[k] = v
	}
	created, err := repo.CreateWithID(ctx, id, rec)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return appendOutcome{}, fmt.Errorf("%w: producer %s event %s type %s",
				store.ErrAccessEvidenceRaced, key.ProducerInstance, key.SourceEventID, key.EventType)
		}
		return appendOutcome{}, err
	}
	return appendOutcome{rec: created, fresh: true}, nil
}

// ---------------------------------------------------------------------------
// Policy artifacts
// ---------------------------------------------------------------------------

// decodePolicyArtifact rebuilds one artifact row and verifies its integrity.
func decodePolicyArtifact(rec model.Record) (model.PolicyArtifact, error) {
	base, err := baseFromRecord(rec)
	if err != nil {
		return model.PolicyArtifact{}, err
	}
	meta, err := decAccessMeta(rec)
	if err != nil {
		return model.PolicyArtifact{}, err
	}
	var content sdk.PolicyArtifactContent
	if err := decAccessContent(rec, &content); err != nil {
		return model.PolicyArtifact{}, err
	}
	if err := verifyAccessRecord(meta, content); err != nil {
		return model.PolicyArtifact{}, err
	}
	return model.PolicyArtifact{BaseFields: base, AccessEvidenceMeta: meta, Artifact: content}, nil
}

// RetainPolicyArtifact records one immutable policy artifact.
func (r *accessEvidenceRepo) RetainPolicyArtifact(
	ctx context.Context, in store.PolicyArtifactAppend,
) (store.AccessEvidenceWrite[model.PolicyArtifact], error) {
	var zero store.AccessEvidenceWrite[model.PolicyArtifact]
	if err := store.ValidatePolicyArtifactAppend(in); err != nil {
		return zero, err
	}
	a := in.Artifact
	own := model.Record{
		"authority_id":        a.AuthorityID,
		"surface":             a.Surface,
		"engine":              a.Engine,
		"artifact_digest":     a.ArtifactDigest,
		"origin":              string(a.Origin),
		"availability":        string(a.Availability),
		"governance_surface":  encOptStr(a.GovernanceSurface),
		"governance_revision": encOptInt(a.GovernanceRevision),
	}
	out, err := r.appendRecord(ctx, r.artifacts, model.PolicyArtifactKind, in.AccessEvidenceAppend, a, own)
	if err != nil || out.dropped {
		return store.AccessEvidenceWrite[model.PolicyArtifact]{Dropped: out.dropped}, err
	}
	rec, err := decodePolicyArtifact(out.rec)
	if err != nil {
		return zero, err
	}
	return store.AccessEvidenceWrite[model.PolicyArtifact]{Record: rec, Fresh: out.fresh}, nil
}

// PolicyArtifact returns one artifact by id.
func (r *accessEvidenceRepo) PolicyArtifact(ctx context.Context, id model.ID) (model.PolicyArtifact, error) {
	rec, err := r.artifacts.Get(ctx, id)
	if err != nil {
		return model.PolicyArtifact{}, wrapUnavailableErr(err)
	}
	return decodePolicyArtifact(rec)
}

// ---------------------------------------------------------------------------
// Authority transitions
// ---------------------------------------------------------------------------

func decodeAuthorityTransition(rec model.Record) (model.AuthorityTransition, error) {
	base, err := baseFromRecord(rec)
	if err != nil {
		return model.AuthorityTransition{}, err
	}
	meta, err := decAccessMeta(rec)
	if err != nil {
		return model.AuthorityTransition{}, err
	}
	var content sdk.AuthorityTransitionContent
	if err := decAccessContent(rec, &content); err != nil {
		return model.AuthorityTransition{}, err
	}
	if err := verifyAccessRecord(meta, content); err != nil {
		return model.AuthorityTransition{}, err
	}
	return model.AuthorityTransition{
		BaseFields:         base,
		AccessEvidenceMeta: meta,
		Transition:         content,
		SubjectArtifactID:  decID(rec, "subject_artifact_id"),
	}, nil
}

// AppendAuthorityTransition records one authority transition.
//
// A policy-artifact subject is RESOLVED inside the pinned tenant before the
// record is written. That resolution is the tenant boundary: a reference to
// another tenant's artifact reads as absent here — the repositories are
// tenant-pinned and RLS backs them on Postgres — so it is refused as a missing
// dependency instead of quietly linking across tenants.
func (r *accessEvidenceRepo) AppendAuthorityTransition(
	ctx context.Context, in store.AuthorityTransitionAppend,
) (store.AccessEvidenceWrite[model.AuthorityTransition], error) {
	var zero store.AccessEvidenceWrite[model.AuthorityTransition]
	if err := store.ValidateAuthorityTransitionAppend(in); err != nil {
		return zero, err
	}
	t := in.Transition
	var artifactID model.ID
	if t.SubjectKind == sdk.SubjectPolicyArtifact {
		resolved, err := r.resolveArtifactRef(ctx, t.SubjectRef, "transition subject")
		if err != nil {
			return zero, err
		}
		artifactID = resolved
	}
	effective, err := model.ParseTimestamp(t.EffectiveAt)
	if err != nil {
		return zero, fmt.Errorf("%w: %v", store.ErrAccessEvidenceInvalid, err)
	}
	known, err := model.ParseTimestamp(t.KnownAt)
	if err != nil {
		return zero, fmt.Errorf("%w: %v", store.ErrAccessEvidenceInvalid, err)
	}
	var until any
	if strings.TrimSpace(t.EffectiveUntil) != "" {
		ts, perr := model.ParseTimestamp(t.EffectiveUntil)
		if perr != nil {
			return zero, fmt.Errorf("%w: %v", store.ErrAccessEvidenceInvalid, perr)
		}
		until = ts.String()
	}
	own := model.Record{
		"subject_kind":          string(t.SubjectKind),
		"subject_ref":           t.SubjectRef,
		"subject_artifact_id":   encOptID(artifactID),
		"transition":            string(t.Transition),
		"authority_sequence":    encOptInt(t.AuthoritySequence),
		"effective_at":          encTS(effective),
		"effective_until":       until,
		"known_at":              encTS(known),
		"snapshot_completeness": string(t.SnapshotCompleteness),
	}
	out, err := r.appendRecord(ctx, r.transitions, model.AuthorityTransitionKind, in.AccessEvidenceAppend, t, own)
	if err != nil || out.dropped {
		return store.AccessEvidenceWrite[model.AuthorityTransition]{Dropped: out.dropped}, err
	}
	rec, err := decodeAuthorityTransition(out.rec)
	if err != nil {
		return zero, err
	}
	return store.AccessEvidenceWrite[model.AuthorityTransition]{Record: rec, Fresh: out.fresh}, nil
}

// AuthorityTransition returns one transition by id.
func (r *accessEvidenceRepo) AuthorityTransition(ctx context.Context, id model.ID) (model.AuthorityTransition, error) {
	rec, err := r.transitions.Get(ctx, id)
	if err != nil {
		return model.AuthorityTransition{}, wrapUnavailableErr(err)
	}
	return decodeAuthorityTransition(rec)
}

// AuthorityTransitionsFor returns one subject's recorded transitions, ordered by
// the instant the ISSUER attests (effective_at) and then by id.
//
// It is a history and not a verdict. Ordering by effective_at deliberately does
// NOT reorder what Olivares knew: known_at travels on every row precisely so a
// late snapshot can complete history without implying the enforcement point had
// it earlier, and deciding what is in force today is the next increment's work
// over the real evaluator's selection.
func (r *accessEvidenceRepo) AuthorityTransitionsFor(ctx context.Context, subjectRef string) ([]model.AuthorityTransition, error) {
	recs, _, err := r.transitions.List(ctx, model.Query{
		Filters: []model.Filter{{Column: "subject_ref", Op: model.OpEq, Value: subjectRef}},
		Sort:    []model.Sort{{Column: "effective_at"}},
		Limit:   maxLimit,
	})
	if err != nil {
		return nil, wrapUnavailableErr(err)
	}
	out := make([]model.AuthorityTransition, 0, len(recs))
	for _, rec := range recs {
		t, derr := decodeAuthorityTransition(rec)
		if derr != nil {
			return nil, derr
		}
		out = append(out, t)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Action observations
// ---------------------------------------------------------------------------

func decodeActionObservation(rec model.Record) (model.ActionObservation, error) {
	base, err := baseFromRecord(rec)
	if err != nil {
		return model.ActionObservation{}, err
	}
	meta, err := decAccessMeta(rec)
	if err != nil {
		return model.ActionObservation{}, err
	}
	var content sdk.ActionObservationContent
	if err := decAccessContent(rec, &content); err != nil {
		return model.ActionObservation{}, err
	}
	if err := verifyAccessRecord(meta, content); err != nil {
		return model.ActionObservation{}, err
	}
	return model.ActionObservation{
		BaseFields:          base,
		AccessEvidenceMeta:  meta,
		Observation:         content,
		QuestionDigest:      rec.String("question_digest"),
		ParentObservationID: decID(rec, "parent_observation_id"),
		DecisionID:          decID(rec, "decision_id"),
	}, nil
}

// AppendActionObservation records one observed stage.
func (r *accessEvidenceRepo) AppendActionObservation(
	ctx context.Context, in store.ActionObservationAppend,
) (store.AccessEvidenceWrite[model.ActionObservation], error) {
	var zero store.AccessEvidenceWrite[model.ActionObservation]
	if err := store.ValidateActionObservationAppend(in); err != nil {
		return zero, err
	}
	o := in.Observation
	questionDigest, err := o.Question.Digest()
	if err != nil {
		return zero, fmt.Errorf("%w: %v", store.ErrAccessEvidenceInvalid, err)
	}
	var parentID, decisionID model.ID
	if strings.TrimSpace(o.ParentRef) != "" {
		parentID, err = r.resolveObservationRef(ctx, o.ParentRef)
		if err != nil {
			return zero, err
		}
	}
	if strings.TrimSpace(o.DecisionRef) != "" {
		decisionID, err = r.resolveDecisionRef(ctx, o.DecisionRef)
		if err != nil {
			return zero, err
		}
	}
	if err := r.requireJournaledOperation(ctx, o.OperationID, o.EffectDigest, "observation"); err != nil {
		return zero, err
	}
	own := model.Record{
		"question_digest":       questionDigest,
		"stage":                 string(o.Stage),
		"mediation":             string(o.Mediation),
		"prevention":            string(o.Prevention),
		"confirmation":          encOptStr(string(o.Confirmation)),
		"principal_ref":         encOptStr(o.Question.PrincipalRef),
		"resource_ref":          encOptStr(o.Question.ResourceRef),
		"action":                o.Question.Action,
		"operation_id":          encOptStr(string(o.OperationID)),
		"effect_digest":         encOptStr(string(o.EffectDigest)),
		"parent_observation_id": encOptID(parentID),
		"decision_id":           encOptID(decisionID),
	}
	out, err := r.appendRecord(ctx, r.observations, model.ActionObservationKind, in.AccessEvidenceAppend, o, own)
	if err != nil || out.dropped {
		return store.AccessEvidenceWrite[model.ActionObservation]{Dropped: out.dropped}, err
	}
	rec, err := decodeActionObservation(out.rec)
	if err != nil {
		return zero, err
	}
	return store.AccessEvidenceWrite[model.ActionObservation]{Record: rec, Fresh: out.fresh}, nil
}

// ActionObservation returns one observation by id.
func (r *accessEvidenceRepo) ActionObservation(ctx context.Context, id model.ID) (model.ActionObservation, error) {
	rec, err := r.observations.Get(ctx, id)
	if err != nil {
		return model.ActionObservation{}, wrapUnavailableErr(err)
	}
	return decodeActionObservation(rec)
}

// ActionObservationsForQuestion returns the separate stages recorded against one
// canonical question, oldest first by the instant the fact occurred.
func (r *accessEvidenceRepo) ActionObservationsForQuestion(ctx context.Context, questionDigest string) ([]model.ActionObservation, error) {
	recs, _, err := r.observations.List(ctx, model.Query{
		Filters: []model.Filter{{Column: "question_digest", Op: model.OpEq, Value: questionDigest}},
		Sort:    []model.Sort{{Column: colAEOccurredAt}},
		Limit:   maxLimit,
	})
	if err != nil {
		return nil, wrapUnavailableErr(err)
	}
	out := make([]model.ActionObservation, 0, len(recs))
	for _, rec := range recs {
		o, derr := decodeActionObservation(rec)
		if derr != nil {
			return nil, derr
		}
		out = append(out, o)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Authorization decisions
// ---------------------------------------------------------------------------

func decodeAuthorizationDecision(rec model.Record) (model.AuthorizationDecision, error) {
	base, err := baseFromRecord(rec)
	if err != nil {
		return model.AuthorizationDecision{}, err
	}
	meta, err := decAccessMeta(rec)
	if err != nil {
		return model.AuthorizationDecision{}, err
	}
	var content sdk.AuthorizationDecisionContent
	if err := decAccessContent(rec, &content); err != nil {
		return model.AuthorizationDecision{}, err
	}
	if err := verifyAccessRecord(meta, content); err != nil {
		return model.AuthorizationDecision{}, err
	}
	return model.AuthorizationDecision{
		BaseFields:         base,
		AccessEvidenceMeta: meta,
		Decision:           content,
		QuestionDigest:     rec.String("question_digest"),
	}, nil
}

// AppendAuthorizationDecision records one decision.
//
// The completeness of the decision's REQUIRED policy-artifact inputs is computed
// from the stored artifacts before the row is written, and a producer claiming
// ReplayComplete over an input that is not locally reconstructible is refused.
// That is the whole point of the availability axis: a digest can be compared,
// and comparing is not re-evaluating.
func (r *accessEvidenceRepo) AppendAuthorizationDecision(
	ctx context.Context, in store.AuthorizationDecisionAppend,
) (store.AccessEvidenceWrite[model.AuthorizationDecision], error) {
	var zero store.AccessEvidenceWrite[model.AuthorizationDecision]
	if err := store.ValidateAuthorizationDecisionAppend(in); err != nil {
		return zero, err
	}
	d := in.Decision
	stamped, stampErr := sdk.StampDecisionReconstructionFields(d)
	if stampErr != nil {
		return zero, fmt.Errorf("%w: %v", store.ErrAccessEvidenceInvalid, stampErr)
	}
	d = stamped
	in.Decision = d
	questionDigest, err := d.Question.Digest()
	if err != nil {
		return zero, fmt.Errorf("%w: %v", store.ErrAccessEvidenceInvalid, err)
	}
	if err := r.requireJournaledOperation(ctx, d.OperationID, d.EffectDigest, "decision"); err != nil {
		return zero, err
	}
	completeness, err := r.resolveDependencies(ctx, d.Inputs)
	if err != nil {
		return zero, err
	}
	completeness.Claimed = d.ReplayCompleteness
	if completeness.Overclaimed() {
		return zero, fmt.Errorf(
			"%w: decision claims %q while required inputs are not locally reconstructible (unavailable: %v)",
			store.ErrAccessEvidenceOverclaim, d.ReplayCompleteness, completeness.UnavailableRefs)
	}
	own := model.Record{
		"question_digest":     questionDigest,
		"purpose":             string(d.Purpose),
		"outcome":             string(d.Outcome),
		"disposition":         encOptStr(string(d.Disposition)),
		"shadow":              d.Shadow,
		"evaluator":           d.Evaluator,
		"replay_completeness": string(d.ReplayCompleteness),
		"operation_id":        encOptStr(string(d.OperationID)),
		"effect_digest":       encOptStr(string(d.EffectDigest)),
	}
	out, err := r.appendRecord(ctx, r.decisions, model.AuthorizationDecisionKind, in.AccessEvidenceAppend, d, own)
	if err != nil || out.dropped {
		return store.AccessEvidenceWrite[model.AuthorizationDecision]{Dropped: out.dropped}, err
	}
	rec, err := decodeAuthorizationDecision(out.rec)
	if err != nil {
		return zero, err
	}
	return store.AccessEvidenceWrite[model.AuthorizationDecision]{Record: rec, Fresh: out.fresh}, nil
}

// AuthorizationDecision returns one decision by id.
func (r *accessEvidenceRepo) AuthorizationDecision(ctx context.Context, id model.ID) (model.AuthorizationDecision, error) {
	rec, err := r.decisions.Get(ctx, id)
	if err != nil {
		return model.AuthorizationDecision{}, wrapUnavailableErr(err)
	}
	return decodeAuthorizationDecision(rec)
}

// AuthorizationDecisionsForQuestion returns the decisions recorded against one
// canonical question, oldest first by the instant the fact occurred.
func (r *accessEvidenceRepo) AuthorizationDecisionsForQuestion(ctx context.Context, questionDigest string) ([]model.AuthorizationDecision, error) {
	recs, _, err := r.decisions.List(ctx, model.Query{
		Filters: []model.Filter{{Column: "question_digest", Op: model.OpEq, Value: questionDigest}},
		Sort:    []model.Sort{{Column: colAEOccurredAt}},
		Limit:   maxLimit,
	})
	if err != nil {
		return nil, wrapUnavailableErr(err)
	}
	out := make([]model.AuthorizationDecision, 0, len(recs))
	for _, rec := range recs {
		d, derr := decodeAuthorizationDecision(rec)
		if derr != nil {
			return nil, derr
		}
		out = append(out, d)
	}
	return out, nil
}

// DecisionCompleteness reports what the RETAINED data supports for one stored
// decision, next to what its producer claimed. The two are reported separately
// on purpose: a claim is a statement by a producer, and the resolved verdict is
// a fact about this store.
func (r *accessEvidenceRepo) DecisionCompleteness(ctx context.Context, id model.ID) (model.AccessEvidenceCompleteness, error) {
	decision, err := r.AuthorizationDecision(ctx, id)
	if err != nil {
		return model.AccessEvidenceCompleteness{}, err
	}
	c, err := r.resolveDependencies(ctx, decision.Decision.Inputs)
	if err != nil {
		return model.AccessEvidenceCompleteness{}, err
	}
	c.DecisionID = id
	c.Claimed = decision.Decision.ReplayCompleteness
	return c, nil
}

// ---------------------------------------------------------------------------
// Reference resolution
// ---------------------------------------------------------------------------

// resolveArtifactRef resolves a policy-artifact reference inside the PINNED
// tenant. A malformed id, an unknown id and another tenant's id are all the same
// answer here — the dependency does not resolve — and that is deliberate: a
// distinguishable "exists but is not yours" would be a cross-tenant existence
// oracle.
func (r *accessEvidenceRepo) resolveArtifactRef(ctx context.Context, ref, what string) (model.ID, error) {
	id, err := model.ParseID(strings.TrimSpace(ref))
	if err != nil {
		return "", fmt.Errorf("%w: %s %q is not a record reference", store.ErrAccessEvidenceDependencyMissing, what, ref)
	}
	if _, err := r.artifacts.Get(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", fmt.Errorf("%w: %s %q", store.ErrAccessEvidenceDependencyMissing, what, ref)
		}
		return "", wrapUnavailableErr(err)
	}
	return id, nil
}

// resolveObservationRef resolves a parent-observation reference in this tenant.
func (r *accessEvidenceRepo) resolveObservationRef(ctx context.Context, ref string) (model.ID, error) {
	id, err := model.ParseID(strings.TrimSpace(ref))
	if err != nil {
		return "", fmt.Errorf("%w: parent observation %q is not a record reference", store.ErrAccessEvidenceDependencyMissing, ref)
	}
	if _, err := r.observations.Get(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", fmt.Errorf("%w: parent observation %q", store.ErrAccessEvidenceDependencyMissing, ref)
		}
		return "", wrapUnavailableErr(err)
	}
	return id, nil
}

// resolveDecisionRef resolves a decision reference in this tenant.
func (r *accessEvidenceRepo) resolveDecisionRef(ctx context.Context, ref string) (model.ID, error) {
	id, err := model.ParseID(strings.TrimSpace(ref))
	if err != nil {
		return "", fmt.Errorf("%w: decision %q is not a record reference", store.ErrAccessEvidenceDependencyMissing, ref)
	}
	if _, err := r.decisions.Get(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", fmt.Errorf("%w: decision %q", store.ErrAccessEvidenceDependencyMissing, ref)
		}
		return "", wrapUnavailableErr(err)
	}
	return id, nil
}

// requireJournaledOperation checks a named operation against the EXISTING
// evidence journal, which stays the single authority on governed effects.
//
// A record may name an operation only if the tenant's journal already holds it
// with the SAME effect digest. A different digest is the journal's own rebind
// condition and is reported with the journal's own sentinel, so a consumer
// classifies it exactly as it would a rebind anywhere else. The journal is only
// READ here: nothing in this file claims, settles or re-dispatches an operation.
func (r *accessEvidenceRepo) requireJournaledOperation(
	ctx context.Context, op sdk.OperationID, digest sdk.EffectDigest, what string,
) error {
	id := strings.TrimSpace(string(op))
	if id == "" {
		return nil
	}
	journaled, err := r.journal.Get(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: %s names operation %q, which this tenant's evidence journal does not hold",
				store.ErrAccessEvidenceDependencyMissing, what, id)
		}
		return err
	}
	if journaled.EffectDigest != strings.TrimSpace(string(digest)) {
		return fmt.Errorf("%w: %s binds operation %q to a different effect", store.ErrEvidenceRebind, what, id)
	}
	return nil
}

// resolveDependencies computes what the retained data supports for a decision's
// inputs.
//
// Only REQUIRED inputs decide the verdict, and only DependencyPolicyArtifact
// inputs are resolved to a stored record: the other kinds name entities whose
// storage belongs to later increments, so treating an unresolved role or
// binding ref as missing would report a defect in this store that is really an
// absence of the increment that owns it. They are recorded and left explicit.
//
// An input that resolves but is not locally reconstructible lands in
// UnavailableRefs rather than MissingRefs, because those are different facts:
// one is "we do not have this", the other is "we have a pointer or a digest,
// which is not the rules".
func (r *accessEvidenceRepo) resolveDependencies(ctx context.Context, deps []sdk.AccessDependency) (model.AccessEvidenceCompleteness, error) {
	out := model.AccessEvidenceCompleteness{Resolved: sdk.ReplayComplete}
	requiredArtifacts := 0
	for _, dep := range deps {
		if !dep.Required {
			continue
		}
		if dep.Kind != sdk.DependencyPolicyArtifact {
			// A required input this increment cannot resolve cannot be asserted
			// complete either: it is unknown, and unknown is not permissive.
			out.Resolved = sdk.ReplayUnknown
			continue
		}
		requiredArtifacts++
		id, err := model.ParseID(strings.TrimSpace(dep.Ref))
		if err != nil {
			out.MissingRefs = append(out.MissingRefs, dep.Ref)
			out.Resolved = sdk.ReplayIncomplete
			continue
		}
		rec, err := r.artifacts.Get(ctx, id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				out.MissingRefs = append(out.MissingRefs, dep.Ref)
				out.Resolved = sdk.ReplayIncomplete
				continue
			}
			return model.AccessEvidenceCompleteness{}, wrapUnavailableErr(err)
		}
		artifact, err := decodePolicyArtifact(rec)
		if err != nil {
			return model.AccessEvidenceCompleteness{}, err
		}
		if !artifact.Reconstructible() {
			out.UnavailableRefs = append(out.UnavailableRefs, dep.Ref)
			out.Resolved = sdk.ReplayIncomplete
		}
	}
	if len(out.MissingRefs) > 0 || len(out.UnavailableRefs) > 0 {
		out.Resolved = sdk.ReplayIncomplete
	}
	if requiredArtifacts == 0 && out.Resolved == sdk.ReplayComplete && len(deps) == 0 {
		// A decision that recorded no inputs at all made no claim this store can
		// verify. Reporting "complete" for it would say the retained data
		// supports a replay when there is nothing retained to replay from.
		out.Resolved = sdk.ReplayUnknown
	}
	return out, nil
}
