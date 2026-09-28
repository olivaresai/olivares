// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

const (
	collectionRunKind       model.Kind = "inventory.collection_run"
	collectionSourceKind    model.Kind = "inventory.collection_source"
	collectionScopeKind     model.Kind = "inventory.collection_scope"
	collectionAdmissionKind model.Kind = "inventory.collection_admission"
	collectionRejectionKind model.Kind = "inventory.collection_rejection"
	colCollectionLink                  = "collection_link"
	cRun                               = "run_id"
	cSourceKey                         = "source_key"
	cRevision                          = "source_revision"
	cEnvironment                       = "environment_ref"
	cOrder                             = "run_order"
	cScopeKey                          = "scope_key"
	cContract                          = "scope_contract"
	cFamily                            = "family"
	cRequested                         = "requested_scope"
	cFulfilled                         = "fulfilled_scope"
	cSelectors                         = "selectors"
	cHostStart                         = "host_started_at"
	cProducerStart                     = "producer_started_at"
	cHostFinish                        = "host_finished_at"
	cProducerFinish                    = "producer_finished_at"
	cState                             = "result_state"
	cReason                            = "result_reason"
	cTerminal                          = "terminal_hash"
	cAdmitted                          = "admitted_count"
	cCommitted                         = "committed_count"
	cExpected                          = "expected_count"
	cProjection                        = "projection_state"
	cQualified                         = "qualified_at"
	cRejected                          = "rejection_reason"
	cLastQualified                     = "last_qualified_run"
)

// ErrCollectionRejected is a definite collection contract refusal. Raw facts,
// credentials and provider errors are never included in this diagnostic.
var ErrCollectionRejected = errors.New("inventory: collection persistence rejected")

// WithCollectionCoverage opts the existing module into the runtime recorder port.
func WithCollectionCoverage() Option { return func(m *Module) { m.collectionCoverage = true } }

// InventoryCoverageRecorder is discovered at runtime AddModule. Data is bound
// before Start; no second inventory instance, source roster or scheduler exists.
func (m *Module) InventoryCoverageRecorder() event.InventoryRecorder {
	if !m.collectionCoverage {
		return nil
	}
	return m
}

func coverageSourceKey(run event.InventoryRun) string {
	b, _ := json.Marshal([]any{run.Registration.SourceID, run.Registration.SourceRevision, run.Registration.EnvironmentRef})
	return digest(b)
}
func coverageScopeKey(source string, s sdkmodel.InventoryScope) string {
	return digest([]byte(source + ":" + s.Fingerprint()))
}
func collectionLinkValue(e event.Event) any {
	if e.InventoryMember == nil {
		return nil
	}
	return collectionLink(e)
}

// Immutable linkage compares decoded values independently of JSON key order.
func sameCollectionLink(raw string, link *event.InventoryMember) bool {
	if raw == "" {
		return link == nil
	}
	var stored *event.InventoryMember
	if json.Unmarshal([]byte(raw), &stored) != nil {
		return false
	}
	if stored == nil || link == nil {
		return stored == nil && link == nil
	}
	return *stored == *link
}

func collectionLink(e event.Event) string {
	if e.InventoryMember == nil {
		return ""
	}
	b, _ := json.Marshal(e.InventoryMember)
	return string(b)
}

func (m *Module) collectionMutate(ctx context.Context, run event.InventoryRun, fn func(store.Scope) error) error {
	tenant, ok := tenantOf(run.Tenant)
	if !ok || m.data == nil || run.ID == "" || len(run.ID) > 128 || !run.Registration.Valid() || len(run.Registration.SourceID) > 128 || len(run.Registration.EnvironmentRef) > 128 || run.StartedAt.IsZero() {
		return ErrCollectionRejected
	}
	// The existing tenant Mutate gate serializes Begin/admission/receipt/Finish.
	// No caller holds this transaction while publishing to the bus.
	// In particular, a failed COMMIT acknowledgement may already be durable.
	// Preserve errors.Is and the causal chain; only contract checks reject.
	return m.data.Mutate(ctx, tenant, fn)
}
func collectionFind(ctx context.Context, sc store.Scope, kind model.Kind, filters ...model.Filter) (model.Record, error) {
	repo, err := sc.Ext(kind)
	if err != nil {
		return nil, err
	}
	rows, _, err := repo.List(ctx, model.Query{Filters: filters, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}
func collectionUpdate(ctx context.Context, sc store.Scope, kind model.Kind, r model.Record) error {
	repo, err := sc.Ext(kind)
	if err != nil {
		return err
	}
	_, err = repo.Update(ctx, r)
	return err
}
func collectionCreate(ctx context.Context, sc store.Scope, kind model.Kind, r model.Record) (model.Record, error) {
	repo, err := sc.Ext(kind)
	if err != nil {
		return nil, err
	}
	return repo.Create(ctx, r)
}
func collectionIdentity(row model.Record, run event.InventoryRun) bool {
	return row != nil && row.String(cRun) == run.ID && row.String(cSourceKey) == coverageSourceKey(run) && row.String(cHostStart) == instant(run.StartedAt)
}

// BeginRun commits identity and durable order before Gather can emit a member.
func (m *Module) BeginRun(ctx context.Context, run event.InventoryRun) error {
	return m.collectionMutate(ctx, run, func(sc store.Scope) error {
		old, err := collectionFind(ctx, sc, collectionRunKind, eq(cRun, run.ID))
		if err != nil {
			return err
		}
		if old != nil {
			if collectionIdentity(old, run) {
				return nil
			}
			return ErrCollectionRejected
		}
		key := coverageSourceKey(run)
		head, err := collectionFind(ctx, sc, collectionSourceKind, eq(cSourceKey, key))
		if err != nil {
			return err
		}
		order := int64(1)
		if head == nil {
			head, err = collectionCreate(ctx, sc, collectionSourceKind, model.Record{cSourceKey: key, colSourceID: run.Registration.SourceID, cRevision: run.Registration.SourceRevision, cEnvironment: run.Registration.EnvironmentRef, cOrder: order, cRun: run.ID})
		} else {
			order = head.Int(cOrder) + 1
			if order < 1 {
				return ErrCollectionRejected
			}
			head[cOrder] = order
			head[cRun] = run.ID
			err = collectionUpdate(ctx, sc, collectionSourceKind, head)
		}
		if err != nil {
			return err
		}
		_, err = collectionCreate(ctx, sc, collectionRunKind, model.Record{cRun: run.ID, cSourceKey: key, colSourceID: run.Registration.SourceID, cRevision: run.Registration.SourceRevision, cEnvironment: run.Registration.EnvironmentRef, cOrder: order, cHostStart: instant(run.StartedAt), cState: "unknown", cReason: "missing_report", cAdmitted: int64(0), cCommitted: int64(0), cExpected: int64(0), cProjection: "pending"})
		return err
	})
}

// StartCollection pins one immutable declared scope on the host-owned run.
func (m *Module) StartCollection(ctx context.Context, run event.InventoryRun, start sdkmodel.InventoryCollectionStart) error {
	if !start.Scope.Valid() || start.ObservedAt.IsZero() {
		return ErrCollectionRejected
	}
	return m.collectionMutate(ctx, run, func(sc store.Scope) error {
		row, err := collectionFind(ctx, sc, collectionRunKind, eq(cRun, run.ID))
		if err != nil {
			return err
		}
		if !collectionIdentity(row, run) || row.String(cTerminal) != "" {
			return ErrCollectionRejected
		}
		fp := start.Scope.Fingerprint()
		if row.String(cRequested) != "" {
			if row.String(cRequested) == fp && row.String(cProducerStart) == instant(start.ObservedAt) {
				return nil
			}
			return ErrCollectionRejected
		}
		selectors, _ := json.Marshal(start.Scope.Selectors)
		row[cRequested] = fp
		row[cContract] = start.Scope.Contract
		row[cFamily] = start.Scope.Family
		row[cSelectors] = string(selectors)
		row[cProducerStart] = instant(start.ObservedAt)
		row[cScopeKey] = coverageScopeKey(row.String(cSourceKey), start.Scope)
		return collectionUpdate(ctx, sc, collectionRunKind, row)
	})
}

// AdmitMember durably reserves the actual event identity before enqueueing.
func (m *Module) AdmitMember(ctx context.Context, run event.InventoryRun, eventID string, member event.InventoryMember) error {
	if eventID == "" || len(eventID) > 128 || member.RunID != run.ID || member.Ordinal < 1 || member.Ordinal > sdkmodel.MaxInventoryMembers || member.Digest == "" || !sdkmodel.ValidInventoryFingerprint(member.Digest) {
		return ErrCollectionRejected
	}
	return m.collectionMutate(ctx, run, func(sc store.Scope) error {
		row, err := collectionFind(ctx, sc, collectionRunKind, eq(cRun, run.ID))
		if err != nil {
			return err
		}
		if !collectionIdentity(row, run) || row.String(cRequested) == "" || row.String(cTerminal) != "" {
			return ErrCollectionRejected
		}
		old, err := collectionFind(ctx, sc, collectionAdmissionKind, eq(colEventID, eventID))
		if err != nil {
			return err
		}
		if old != nil {
			if old.String(cRun) == run.ID && old.Int(colMemberOrdinal) == member.Ordinal && old.String(colFactsHash) == member.Digest {
				return nil
			}
			return ErrCollectionRejected
		}
		if member.Ordinal != row.Int(cAdmitted)+1 {
			return ErrCollectionRejected
		}
		if _, err = collectionCreate(ctx, sc, collectionAdmissionKind, model.Record{cRun: run.ID, colEventID: eventID, colMemberOrdinal: member.Ordinal, colFactsHash: member.Digest}); err != nil {
			return err
		}
		row[cAdmitted] = member.Ordinal
		return collectionUpdate(ctx, sc, collectionRunKind, row)
	})
}

// FinishRun stores immutable host-closed evidence; queued members remain pending.
func (m *Module) FinishRun(ctx context.Context, run event.InventoryRun, finish event.InventoryFinish) error {
	if finish.FinishedAt.IsZero() || finish.Expected < 0 || finish.Expected > sdkmodel.MaxInventoryMembers || !sdkmodel.ValidInventoryResult(finish.Report.State, finish.Report.Reason) || !sdkmodel.ValidInventoryFingerprint(finish.Report.RequestedScope) || !sdkmodel.ValidInventoryFingerprint(finish.Report.FulfilledScope) || finish.Report.Count < 0 || finish.Report.Count > sdkmodel.MaxInventoryMembers {
		return ErrCollectionRejected
	}
	rejected := false
	err := m.collectionMutate(ctx, run, func(sc store.Scope) error {
		row, err := collectionFind(ctx, sc, collectionRunKind, eq(cRun, run.ID))
		if err != nil {
			return err
		}
		if !collectionIdentity(row, run) {
			return ErrCollectionRejected
		}
		body, _ := json.Marshal(finish)
		hash := digest(body)
		if prior := row.String(cTerminal); prior != "" {
			if prior == hash {
				return nil
			}
			rejected = true
			return rejectCollection(ctx, sc, row, "", "terminal_conflict", m.clock.Now().Time())
		}
		report := finish.Report
		if report.State == "complete" && (row.String(cRequested) == "" || report.RequestedScope != row.String(cRequested) || report.FulfilledScope != row.String(cRequested) || report.Reason != "exhausted" || report.Count != finish.Expected || finish.Expected != row.Int(cAdmitted) || report.ObservedUntil.IsZero() || coverageProducerEndInvalid(report.ObservedUntil, row.String(cProducerStart))) {
			report.State = "partial"
			report.Reason = "protocol_error"
		}
		row[cTerminal] = hash
		row[cState] = report.State
		row[cReason] = report.Reason
		row[cFulfilled] = report.FulfilledScope
		row[cExpected] = finish.Expected
		row[cHostFinish] = instant(finish.FinishedAt)
		row[cProducerFinish] = instant(report.ObservedUntil)
		return m.qualifyCollection(ctx, sc, row)
	})
	if err != nil {
		return err
	}
	if rejected {
		return ErrCollectionRejected
	}
	return nil
}

// qualifyCollection is called in the SAME tenant transaction by Finish and the
// last committing receipt. Admission identity/digest and ordinal uniqueness are
// checked per member; count equality cannot substitute another event population.
func (m *Module) qualifyCollection(ctx context.Context, sc store.Scope, row model.Record) error {
	row[cProjection] = "pending"
	if row.String(cRejected) != "" {
		row[cProjection] = "failed"
	} else if row.String(cTerminal) != "" && row.Int(cExpected) == row.Int(cAdmitted) && row.Int(cCommitted) == row.Int(cAdmitted) {
		row[cProjection] = "committed"
	}
	if row.String(cState) == "complete" && row.String(cProjection) == "committed" && row.String(cQualified) == "" {
		row[cQualified] = instant(m.clock.Now().Time())
		head, err := collectionFind(ctx, sc, collectionScopeKind, eq(cScopeKey, row.String(cScopeKey)))
		if err != nil {
			return err
		}
		if head == nil {
			_, err = collectionCreate(ctx, sc, collectionScopeKind, model.Record{cScopeKey: row.String(cScopeKey), cLastQualified: row.String(cRun), cOrder: row.Int(cOrder)})
			if err != nil {
				return err
			}
		} else if row.Int(cOrder) > head.Int(cOrder) {
			head[cOrder] = row.Int(cOrder)
			head[cLastQualified] = row.String(cRun)
			if err := collectionUpdate(ctx, sc, collectionScopeKind, head); err != nil {
				return err
			}
		}
	}
	return collectionUpdate(ctx, sc, collectionRunKind, row)
}

func rejectCollection(ctx context.Context, sc store.Scope, row model.Record, eventID, reason string, at time.Time) error {
	runID := ""
	if row != nil {
		runID = row.String(cRun)
		row[cRejected] = reason
		row[cProjection] = "failed"
		if err := collectionUpdate(ctx, sc, collectionRunKind, row); err != nil {
			return err
		}
	}
	key := digest([]byte(runID + ":" + eventID + ":" + reason))
	old, err := collectionFind(ctx, sc, collectionRejectionKind, eq(colReceiptKey, key))
	if err != nil {
		return err
	}
	if old != nil {
		return nil
	}
	_, err = collectionCreate(ctx, sc, collectionRejectionKind, model.Record{colReceiptKey: key, cRun: runID, colEventID: eventID, cReason: reason, colFirstSeen: instant(at)})
	return err
}

// checkCollectionLink runs before materialization. Existing C1 receipt linkage,
// including legacy absence, is immutable even when its v1 facts hash matches.
func (m *Module) checkCollectionLink(ctx context.Context, sc store.Scope, e event.Event, receipt model.Record, at time.Time) (bool, error) {
	if receipt != nil && !sameCollectionLink(receipt.String(colCollectionLink), e.InventoryMember) {
		var row model.Record
		var err error
		target := e.InventoryMember
		if target == nil {
			var previous event.InventoryMember
			if json.Unmarshal([]byte(receipt.String(colCollectionLink)), &previous) == nil {
				target = &previous
			}
		}
		if target != nil {
			row, err = collectionFind(ctx, sc, collectionRunKind, eq(cRun, target.RunID))
			if err != nil {
				return false, err
			}
		}
		return false, rejectCollection(ctx, sc, row, e.ID, "linkage_conflict", at)
	}
	if e.InventoryMember == nil {
		return true, nil
	}
	meta := e.InventoryMember
	row, err := collectionFind(ctx, sc, collectionRunKind, eq(cRun, meta.RunID))
	if err != nil {
		return false, err
	}
	admitted, err := collectionFind(ctx, sc, collectionAdmissionKind, eq(colEventID, e.ID))
	if err != nil {
		return false, err
	}
	valid := row != nil && admitted != nil && e.SourceRegistration != nil && e.SourceRegistration.Valid() &&
		row.String(colSourceID) == e.SourceRegistration.SourceID && row.Int(cRevision) == e.SourceRegistration.SourceRevision && row.String(cEnvironment) == e.SourceRegistration.EnvironmentRef &&
		admitted.String(cRun) == meta.RunID && admitted.Int(colMemberOrdinal) == meta.Ordinal && admitted.String(colFactsHash) == meta.Digest && meta.Digest == event.InventoryMemberDigest(e)
	if !valid {
		return false, rejectCollection(ctx, sc, row, e.ID, "member_conflict", at)
	}
	if receipt != nil && admitted.String(colReceiptID) != "" && admitted.String(colReceiptID) != receipt.String(model.ColID) {
		return false, rejectCollection(ctx, sc, row, e.ID, "receipt_conflict", at)
	}
	return true, nil
}
func (m *Module) commitCollectionMember(ctx context.Context, sc store.Scope, e event.Event, receipt model.Record) error {
	if e.InventoryMember == nil {
		return nil
	}
	member, err := collectionFind(ctx, sc, collectionAdmissionKind, eq(colEventID, e.ID))
	if err != nil {
		return err
	}
	if member == nil {
		return ErrCollectionRejected
	}
	if member.String(colReceiptID) != "" {
		return nil
	}
	member[colReceiptID] = receipt.String(model.ColID)
	member[colFirstSeen] = instant(m.clock.Now().Time())
	if err := collectionUpdate(ctx, sc, collectionAdmissionKind, member); err != nil {
		return err
	}
	row, err := collectionFind(ctx, sc, collectionRunKind, eq(cRun, e.InventoryMember.RunID))
	if err != nil {
		return err
	}
	if row == nil {
		return ErrCollectionRejected
	}
	row[cCommitted] = row.Int(cCommitted) + 1
	return m.qualifyCollection(ctx, sc, row)
}

var (
	pdeclCollectionRunID        = model.None("opaque host run ID; coverage.go:147 compares it to the retained run and qualification pointers, never resolves an account")
	pdeclCollectionFingerprint  = model.None("SHA-256 of versioned source/scope/terminal facts; coverage.go:69 and sdk/model/inventory_coverage.go:56 construct them for equality and indexing")
	pdeclCollectionDigest       = model.None("digest of exact admitted event identity and payload; sdk/event/event.go:296 binds the retained member, never resolves an account")
	pdeclCollectionRejectionKey = model.None("SHA-256 of run/event/reason, compared only to make refusal evidence idempotent: coverage.go:341")
	pdeclCollectionEnvironment  = model.None("opened source environment identity, compared with the host snapshot; coverage.go:387 and sdk/event/event.go:266")
	pdeclCollectionVocabulary   = model.None("closed coverage state, reason or versioned query/family label; sdk/model/inventory_coverage.go:33 and sdk/model/inventory_coverage.go:108 constrain the vocabulary")
	pdeclCollectionSelectors    = model.Nested([]string(nil), model.ClassEvidence, model.Leaf("[]", model.None("explicit Azure subscription IDs restricted to ASCII letters, digits and hyphens by sdk/model/inventory_coverage.go:33; sdk/model/inventory_coverage.go:63 matches resource scope, never an account")))
	pdeclCollectionLink         = model.Nested(event.InventoryMember{}, model.ClassEvidence, model.Leaf("run_id", pdeclCollectionRunID), model.Leaf("digest", pdeclCollectionDigest))
)

func registerCoverageSchema(reg store.ExtensionRegistry) error {
	text := func(name string, nullable bool, decl *model.ColumnDecl) model.FieldSpec {
		return model.FieldSpec{Name: name, Kind: model.KindText, Nullable: nullable, Principal: decl}
	}
	integer := func(name string) model.FieldSpec { return model.FieldSpec{Name: name, Kind: model.KindInt} }
	stamp := func(name string, nullable bool) model.FieldSpec {
		return model.FieldSpec{Name: name, Kind: model.KindTimestamp, Nullable: nullable}
	}
	idDeclarations := map[string]*model.ColumnDecl{
		cRun: pdeclCollectionRunID, cLastQualified: pdeclCollectionRunID,
		cSourceKey: pdeclCollectionFingerprint, cScopeKey: pdeclCollectionFingerprint,
		cRequested: pdeclCollectionFingerprint, cFulfilled: pdeclCollectionFingerprint, cTerminal: pdeclCollectionFingerprint,
		colSourceID: pdeclNoneSourceID, cEnvironment: pdeclCollectionEnvironment,
		colEventID: pdeclNoneEventID, colReceiptID: pdeclNoneReceiptID,
		colReceiptKey: pdeclCollectionRejectionKey, colFactsHash: pdeclCollectionDigest,
	}
	id := func(name string, nullable bool) model.FieldSpec { return text(name, nullable, idDeclarations[name]) }
	vocabulary := func(name string, nullable bool) model.FieldSpec {
		return text(name, nullable, pdeclCollectionVocabulary)
	}
	unique := func(name string, cols ...string) model.IndexSpec {
		return model.IndexSpec{Name: name, Columns: append([]string{model.ColTenantID}, cols...), Unique: true}
	}
	for _, d := range []model.EntityDescriptor{
		{Kind: collectionSourceKind, Table: "inventory_collection_source", Fields: []model.FieldSpec{
			id(cSourceKey, false), id(colSourceID, false), integer(cRevision),
			id(cEnvironment, false), integer(cOrder), id(cRun, false),
		}, Indexes: []model.IndexSpec{unique("inventory_collection_source_uniq", cSourceKey)}},
		{Kind: collectionRunKind, Table: "inventory_collection_run", Fields: []model.FieldSpec{
			id(cRun, false), id(cSourceKey, false), id(colSourceID, false), integer(cRevision),
			id(cEnvironment, false), integer(cOrder), id(cScopeKey, true),
			vocabulary(cContract, true), vocabulary(cFamily, true), id(cRequested, true), id(cFulfilled, true),
			{Name: cSelectors, Kind: model.KindJSON, Nullable: true, Principal: pdeclCollectionSelectors},
			stamp(cHostStart, false), stamp(cProducerStart, true), stamp(cHostFinish, true), stamp(cProducerFinish, true),
			vocabulary(cState, false), vocabulary(cReason, false), id(cTerminal, true),
			integer(cAdmitted), integer(cCommitted), integer(cExpected),
			vocabulary(cProjection, false), stamp(cQualified, true), vocabulary(cRejected, true),
		}, Indexes: []model.IndexSpec{unique("inventory_collection_run_uniq", cRun)}},
		{Kind: collectionScopeKind, Table: "inventory_collection_scope", Fields: []model.FieldSpec{
			id(cScopeKey, false), id(cLastQualified, false), integer(cOrder),
		}, Indexes: []model.IndexSpec{unique("inventory_collection_scope_uniq", cScopeKey)}},
		{Kind: collectionAdmissionKind, Table: "inventory_collection_admission", Fields: []model.FieldSpec{
			id(cRun, false), id(colEventID, false), integer(colMemberOrdinal),
			id(colFactsHash, false), id(colReceiptID, true), stamp(colFirstSeen, true),
		}, Indexes: []model.IndexSpec{
			unique("inventory_collection_event_uniq", colEventID),
			unique("inventory_collection_ordinal_uniq", cRun, colMemberOrdinal),
		}},
		{Kind: collectionRejectionKind, Table: "inventory_collection_rejection", Fields: []model.FieldSpec{
			id(colReceiptKey, false), id(cRun, false), id(colEventID, false),
			vocabulary(cReason, false), stamp(colFirstSeen, false),
		}, Indexes: []model.IndexSpec{unique("inventory_collection_reject_uniq", colReceiptKey)}},
	} {
		if err := reg.Register(d); err != nil {
			return err
		}
	}
	return nil
}

func coverageProducerEndInvalid(end time.Time, start string) bool {
	at, err := time.Parse(time.RFC3339Nano, start)
	return err != nil || end.Before(at)
}
