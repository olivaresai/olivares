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
func accessEvidenceProvenanceFields() []model.FieldSpec {
	return []model.FieldSpec{
		field(colAESchemaVersion, model.KindInt, false),
		indexedField(colAEProducer, model.KindText, false),
		field(colAESourceEventID, model.KindText, false),
		field(colAEEventType, model.KindText, false),
		field(colAEAdapter, model.KindText, false),
		indexedField(colAEOccurredAt, model.KindTimestamp, false),
		field(colAERecordedAt, model.KindTimestamp, false),
		indexedField(colAERecordDigest, model.KindText, false),
		field(colAELedgerRef, model.KindText, false),
		field(colAEContent, model.KindJSON, false),
	}
}

// accessEvidenceFields prepends the shared provenance columns to a family's own
// columns.
func accessEvidenceFields(own ...model.FieldSpec) []model.FieldSpec {
	return append(accessEvidenceProvenanceFields(), own...)
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
	Fields: accessEvidenceFields(
		indexedField("authority_id", model.KindText, false),
		indexedField("surface", model.KindText, false),
		field("engine", model.KindText, false),
		indexedField("artifact_digest", model.KindText, false),
		field("origin", model.KindText, false),
		indexedField("availability", model.KindText, false),
		field("governance_surface", model.KindText, true),
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
	Fields: accessEvidenceFields(
		field("subject_kind", model.KindText, false),
		indexedField("subject_ref", model.KindText, false),
		indexedField("subject_artifact_id", model.KindUUID, true),
		field("transition", model.KindText, false),
		field("authority_sequence", model.KindInt, true),
		indexedField("effective_at", model.KindTimestamp, false),
		field("effective_until", model.KindTimestamp, true),
		field("known_at", model.KindTimestamp, false),
		field("snapshot_completeness", model.KindText, false),
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
	Fields: accessEvidenceFields(
		indexedField("question_digest", model.KindText, false),
		indexedField("stage", model.KindText, false),
		field("mediation", model.KindText, false),
		field("prevention", model.KindText, false),
		field("confirmation", model.KindText, true),
		indexedField("principal_ref", model.KindText, true),
		indexedField("resource_ref", model.KindText, true),
		indexedField("action", model.KindText, false),
		indexedField("operation_id", model.KindText, true),
		field("effect_digest", model.KindText, true),
		indexedField("parent_observation_id", model.KindUUID, true),
		indexedField("decision_id", model.KindUUID, true),
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
	Fields: accessEvidenceFields(
		indexedField("question_digest", model.KindText, false),
		field("purpose", model.KindText, false),
		indexedField("outcome", model.KindText, false),
		field("disposition", model.KindText, true),
		field("shadow", model.KindBool, false),
		indexedField("evaluator", model.KindText, false),
		field("replay_completeness", model.KindText, false),
		indexedField("operation_id", model.KindText, true),
		field("effect_digest", model.KindText, true),
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
