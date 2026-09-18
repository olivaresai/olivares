// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

// The attempt lifecycle — the three internal operations of this cut and their
// private helpers: the durable activation frontier (Begin), the bounded legacy
// import, the exact lookup, and the guard the legacy reservation wrappers consult.
//
// Private T0 admission lives in attempt.go and reuses this reader. Operational
// activation, T1–T4, callers, endpoints and scheduling remain release prerequisites;
// the normal module retains no verifier and cannot admit an attempt.
//
// Two boundaries are load-bearing and are stated once here rather than repeated at
// every call site:
//
//   - THE PHYSICAL WRITER LOCK IS THE EXISTING ONE. Every mutation below takes
//     lockFinOpsWriter — the same alertWriterLockKeyPrefix+tenant key the cost
//     ingestion, the alert writer and the reservation ledger already take. The
//     contract's logical name for it is not a second namespace, and no scope is
//     unwrapped to find the capability.
//   - AUTHORITY IS ASKED PER CALL, AND A READ IS NOT A GRANT. Every operation puts
//     a closed check to the configured verifier before it looks anything up, and a
//     lookup or a replay writes no audit and returns no permission.

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Audit actions of this cut. Both are privileged recovery writes appended in the
// SAME transaction as their effect, so a rolled-back operation leaves no audit and
// a committed one can never be unaudited.
const (
	auditActionBeginActivation = "finops.lifecycle_scope.begin"
	auditActionImport          = "finops.attempt.imported"
)

// -----------------------------------------------------------------------------
// Capability preconditions
// -----------------------------------------------------------------------------

// attemptLock takes THE existing FinOps writer lock and classifies its failure.
//
// The type assertion happens here only to tell "this scope cannot serialize"
// (capability_unavailable — a supported-scope problem the caller must fix) from
// "the lock could not be taken" (store_unavailable). The lock itself is taken by
// lockFinOpsWriter, so there is exactly one physical identity in the module and
// this function cannot drift from it.
func attemptLock(ctx context.Context, sc store.Scope) error {
	if _, ok := sc.(store.TransactionLocker); !ok {
		return attemptErr(errCodeCapabilityUnavailable, nil)
	}
	if err := lockFinOpsWriter(ctx, sc); err != nil {
		return storeErr(err)
	}
	return nil
}

// attemptNow reads the DATABASE clock. The application clock is never a fallback:
// a frontier instant or a review comparison decided on a skewed process clock
// would be a fact about that process, not about the ledger.
func attemptNow(ctx context.Context, sc store.Scope) (model.Timestamp, error) {
	clock, ok := sc.(store.TransactionClock)
	if !ok {
		return model.Timestamp{}, attemptErr(errCodeCapabilityUnavailable, nil)
	}
	now, err := clock.TransactionNow(ctx)
	if err != nil {
		return model.Timestamp{}, storeErr(err)
	}
	return now, nil
}

// businessTenant refuses the zero and system tenants before anything is opened.
func businessTenant(t model.TenantID) error {
	if t.IsZero() || t.IsSystem() {
		return attemptErr(errCodeInvalidAttempt, nil)
	}
	return nil
}

// mutateAttempt runs one lifecycle transaction and classifies its outcome.
//
// The distinction it exists to make: an error the CALLBACK returned rolled the
// transaction back and keeps its own classification, while an error that arrives
// after the callback completed proves nothing about the commit. The second case is
// write_outcome_unknown with MayHaveCommitted — never a success, never a rollback,
// and never a retry that would import twice.
func (m *Module) mutateAttempt(ctx context.Context, tenant model.TenantID, fn func(sc store.Scope) error) error {
	completed := false
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		completed = false
		if ferr := fn(sc); ferr != nil {
			return ferr
		}
		completed = true
		return nil
	})
	switch {
	case err == nil:
		return nil
	case completed:
		// The callback finished and the transaction did not acknowledge. Resolve it
		// by identity (the operation's own deterministic ref), not by repeating it.
		return attemptErr(errCodeWriteOutcomeUnknown, err)
	default:
		return err
	}
}

// -----------------------------------------------------------------------------
// Repositories and row access
// -----------------------------------------------------------------------------

func attemptRepoOf(sc store.Scope) (store.GenericRepo, error) {
	repo, err := sc.Ext(attemptKind)
	if err != nil {
		return nil, storeErr(err)
	}
	return repo, nil
}

func lifecycleRepoOf(sc store.Scope) (store.GenericRepo, error) {
	repo, err := sc.Ext(lifecycleScopeKind)
	if err != nil {
		return nil, storeErr(err)
	}
	return repo, nil
}

func reservationRepoOf(sc store.Scope) (store.GenericRepo, error) {
	repo, err := sc.Ext(budgetReservationKind)
	if err != nil {
		return nil, storeErr(err)
	}
	return repo, nil
}

// scanRows pages an entity with the same bounded, cursor-checked loop the
// reservation ledger uses, so there is one set of paging rules in the module.
func scanRows(ctx context.Context, repo store.GenericRepo, filters []model.Filter) ([]model.Record, reservationScanState, error) {
	return scanReservationsTyped(ctx, repo, filters)
}

// readLifecycleScope returns this tenant's activation boundary.
//
// found is TRUE or FALSE only after a SUCCESSFUL read. A store failure returns an
// error and leaves found meaningless: "I could not look" is not "there is no row",
// and the difference is the whole guarantee — an unavailable lookup that answered
// "inactive" would hand v1 admission to a legacy path at exactly the moment the
// ledger is least trustworthy.
func readLifecycleScope(ctx context.Context, sc store.Scope) (LifecycleScopeView, bool, error) {
	repo, err := lifecycleRepoOf(sc)
	if err != nil {
		return LifecycleScopeView{}, false, err
	}
	// The tenant has at most one row (unique index). Two would be corruption, so the
	// query asks for two in order to SEE that rather than take the first.
	rows, page, err := repo.List(ctx, model.Query{Limit: 2})
	if err != nil {
		return LifecycleScopeView{}, false, storeErr(err)
	}
	if len(rows) == 0 {
		if page.HasMore {
			// An empty page promising more rows is not an established absence.
			return LifecycleScopeView{}, false, attemptErr(errCodeLedgerIncomplete, nil)
		}
		return LifecycleScopeView{}, false, nil
	}
	if len(rows) > 1 {
		return LifecycleScopeView{}, false, attemptErr(errCodeLedgerIndeterminate, nil)
	}
	view, err := decodeLifecycleScope(sc.Tenant(), rows[0])
	if err != nil {
		return LifecycleScopeView{}, false, err
	}
	return view, true, nil
}

// decodeLifecycleScope validates and decodes one frontier row. A corrupt row is an
// INTEGRITY error, never an absence: absence has to be observed, not inferred from
// something the reader could not parse.
func decodeLifecycleScope(tenant model.TenantID, rec model.Record) (LifecycleScopeView, error) {
	if tenantCellOf(rec, tenant) != tenantCellMatches {
		return LifecycleScopeView{}, attemptErr(errCodeLedgerIndeterminate, nil)
	}
	state, ok := textCell(rec, colScopeState)
	if !ok || (state != lifecycleQuiescing && state != lifecycleActive) {
		return LifecycleScopeView{}, attemptErr(errCodeLedgerIndeterminate, nil)
	}
	frontierAtText, ok := textCell(rec, colScopeFrontierAt)
	if !ok {
		return LifecycleScopeView{}, attemptErr(errCodeLedgerIndeterminate, nil)
	}
	frontierAt, err := model.ParseTimestamp(frontierAtText)
	if err != nil {
		return LifecycleScopeView{}, attemptErr(errCodeLedgerIndeterminate, err)
	}
	body, ok := textCell(rec, colScopeFrontier)
	if !ok {
		return LifecycleScopeView{}, attemptErr(errCodeLedgerIndeterminate, nil)
	}
	var doc jsonFrontier
	if err := strictUnmarshal(body, &doc); err != nil {
		return LifecycleScopeView{}, attemptErr(errCodeLedgerIndeterminate, err)
	}
	evidence, err := decodeEvidenceRefs(doc.Evidence)
	if err != nil {
		return LifecycleScopeView{}, attemptErr(errCodeLedgerIndeterminate, err)
	}
	id, err := model.ParseID(rec.String(model.ColID))
	if err != nil {
		return LifecycleScopeView{}, attemptErr(errCodeLedgerIndeterminate, err)
	}
	version, ok := int64Cell(rec, model.ColVersion)
	if !ok {
		return LifecycleScopeView{}, attemptErr(errCodeLedgerIndeterminate, nil)
	}
	view := LifecycleScopeView{
		ID: id, Version: version, State: state, FrontierAt: frontierAt,
		FrontierDigest:           Digest(doc.FrontierDigest),
		PendingGroupCount:        int64(doc.PendingGroupCount),
		PendingGroupDigest:       Digest(doc.PendingGroupDigest),
		HistoricalTerminalDigest: Digest(doc.HistoricalTerminalDigest),
		Evidence:                 evidence,
	}
	if !validDigest(view.FrontierDigest) || !validDigest(view.PendingGroupDigest) ||
		!validDigest(view.HistoricalTerminalDigest) {
		return LifecycleScopeView{}, attemptErr(errCodeLedgerIndeterminate, nil)
	}
	// THE CENSUS PROVES ITSELF (R4). Checking the hex shape of three digests said
	// nothing about the set they cover: editing one pending entry's group digest —
	// the edit that lets an altered group pass the import membership check — left
	// every stored digest looking perfectly well formed. Both derivable commitments
	// are recomputed here, so a boundary that no longer commits to its own census is
	// refused rather than carried.
	if err := validateFrontierCommitments(tenant, frontierAt, doc, evidence); err != nil {
		return LifecycleScopeView{}, err
	}
	if activated, ok := textCell(rec, colScopeActivated); ok {
		ts, perr := model.ParseTimestamp(activated)
		if perr != nil {
			return LifecycleScopeView{}, attemptErr(errCodeLedgerIndeterminate, perr)
		}
		view.ActivatedAt = &ts
	}
	// An activated_at without the active state (or the reverse) is a contradiction
	// this reader refuses rather than resolves.
	if (view.ActivatedAt != nil) != (state == lifecycleActive) {
		return LifecycleScopeView{}, attemptErr(errCodeLedgerIndeterminate, nil)
	}
	return view, nil
}

// frontierDocumentOf returns the stored census document of a frontier row, for the
// two readers that need the pending set itself rather than its aggregate.
func frontierDocumentOf(rec model.Record) (jsonFrontier, error) {
	body, ok := textCell(rec, colScopeFrontier)
	if !ok {
		return jsonFrontier{}, attemptErr(errCodeLedgerIndeterminate, nil)
	}
	var doc jsonFrontier
	if err := strictUnmarshal(body, &doc); err != nil {
		return jsonFrontier{}, attemptErr(errCodeLedgerIndeterminate, err)
	}
	return doc, nil
}

// lifecycleScopeRow returns the tenant's single frontier row (not just its view),
// for the paths that must read the census and stamp an evidence reference to it.
func lifecycleScopeRow(ctx context.Context, sc store.Scope) (model.Record, bool, error) {
	repo, err := lifecycleRepoOf(sc)
	if err != nil {
		return nil, false, err
	}
	rows, page, err := repo.List(ctx, model.Query{Limit: 2})
	if err != nil {
		return nil, false, storeErr(err)
	}
	if len(rows) == 0 {
		if page.HasMore {
			return nil, false, attemptErr(errCodeLedgerIncomplete, nil)
		}
		return nil, false, nil
	}
	if len(rows) > 1 {
		return nil, false, attemptErr(errCodeLedgerIndeterminate, nil)
	}
	return rows[0], true, nil
}

// -----------------------------------------------------------------------------
// The legacy reservation guard
// -----------------------------------------------------------------------------

// guardLegacyReservationMutation is the check every covered legacy reservation
// wrapper makes, inside its own transaction and under the writer lock it has
// already taken.
//
// It is an INTERNAL, CONFINED lookup through the scope the wrapper already holds —
// not a recovery entry point, and deliberately NOT the query path of GetAttempt:
//
//   - it requires NO attempt evidence verifier. A tenant whose scope is confirmed
//     inactive must keep working exactly as it did on a deployment that configures
//     no verifier at all, and making the guard depend on one would silently disable
//     every legacy reservation in the product the day this code ships.
//   - it establishes ABSENCE only from a successful read. An unavailable store, an
//     unreadable row or a corrupt row is UNKNOWN, and unknown is refused. It never
//     degrades to "inactive", which is the one answer that would let a covered
//     wrapper create an obligation across a committed frontier.
//
// nil means: this tenant has no activation frontier, confirmed, so the legacy
// behavior adjudicated for it is preserved unchanged.
func guardLegacyReservationMutation(ctx context.Context, sc store.Scope) error {
	_, found, err := readLifecycleScope(ctx, sc)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	return attemptErr(errCodeLifecycleAPIRequired, nil)
}

// -----------------------------------------------------------------------------
// BeginLifecycleActivation
// -----------------------------------------------------------------------------

// BeginLifecycleActivation commits this tenant's durable activation frontier: a
// quiescing boundary, the complete original set of pending legacy reservation
// groups and the frozen digest of the groups that were ALREADY terminal.
//
// From the commit, the covered legacy wrappers of this binary refuse to create or
// terminalize a reservation obligation for this tenant. That is the entire effect.
// It admits nothing, grants nothing, dispatches nothing, and it does NOT activate:
// ActivateLifecycleScope, the v1 admission path and the callers that would use them
// are separate, still-required work.
//
// The census is all-or-nothing. An incomplete scan, a group that mixes pending and
// terminal rows, a group that mixes legacy and v1 children, a malformed linkage or
// a frontier document over its declared byte bound each REFUSE the whole
// operation. A partially recorded boundary would be a census that is partly true,
// which is worse than none.
func (m *Module) BeginLifecycleActivation(
	ctx context.Context, tenant model.TenantID, req LifecycleActivationRequest,
) (LifecycleScopeView, error) {
	if m.data == nil {
		return LifecycleScopeView{}, attemptErr(errCodeCapabilityUnavailable, nil)
	}
	if err := businessTenant(tenant); err != nil {
		return LifecycleScopeView{}, err
	}
	if req.ExpectedVersion < 0 || !validEvidenceRefs(req.Evidence) {
		return LifecycleScopeView{}, attemptErr(errCodeInvalidAttempt, nil)
	}
	// Quiescence and covered-caller readiness are EVIDENCED, never assumed. An
	// empty evidence set is refused here so the verifier is never asked to judge a
	// transition nobody claimed anything about.
	if len(req.Evidence) == 0 {
		return LifecycleScopeView{}, attemptErr(errCodeLifecycleActivation, nil)
	}
	if m.attemptVerifier == nil {
		return LifecycleScopeView{}, attemptErr(errCodeCapabilityUnavailable, nil)
	}

	var view LifecycleScopeView
	err := m.mutateAttempt(ctx, tenant, func(sc store.Scope) error {
		if err := attemptLock(ctx, sc); err != nil {
			return err
		}
		now, err := attemptNow(ctx, sc)
		if err != nil {
			return err
		}
		// Current authority for the lookup, BEFORE the lookup. A component that lost
		// recovery access to this tenant learns nothing about whether a row exists.
		if _, err := m.verifyAttempt(ctx, sc, queryCheck("")); err != nil {
			return err
		}
		existing, found, err := readLifecycleScope(ctx, sc)
		if err != nil {
			return err
		}
		if found {
			// EXACT REPLAY IS CHECKED BEFORE THE OCC. The stored original request
			// bytes are compared to this request's; equal means this is the same Begin
			// arriving again (a lost acknowledgement, a retry) and the answer is the
			// CURRENT view — which may already be active. It is never rewound, and no
			// audit or grant is produced.
			row, _, rerr := lifecycleScopeRow(ctx, sc)
			if rerr != nil {
				return rerr
			}
			doc, derr := frontierDocumentOf(row)
			if derr != nil {
				return derr
			}
			stored, aerr := decodeActivationRequest(doc.OriginalRequest)
			if aerr != nil {
				return attemptErr(errCodeLedgerIndeterminate, aerr)
			}
			if !bytesEqual(activationRequestBytes(tenant, stored), activationRequestBytes(tenant, req)) {
				return attemptErr(errCodeAttemptIdentityConflict, nil)
			}
			view = existing
			return nil
		}
		// A new transition. ExpectedVersion is zero exactly when no row exists.
		if req.ExpectedVersion != 0 {
			return attemptErr(errCodeStaleAttempt, nil)
		}
		pending, terminal, err := censusLegacyGroups(ctx, sc)
		if err != nil {
			return err
		}
		sortPendingGroups(pending)
		sortPendingGroups(terminal)
		pendingDigest := groupSetDigest(frontierTagPending, tenant, pending)
		terminalDigest := groupSetDigest(frontierTagTerminal, tenant, terminal)
		frontier := frontierDigestOf(tenant, now, int64(len(pending)), pendingDigest, terminalDigest, req.Evidence)

		// A COMPLETE ENUMERATION THAT FOUND NOTHING IS A COLLECTION, NOT AN ABSENCE.
		// censusLegacyGroups appends, so it hands back a nil slice for a tenant with
		// no pending legacy group; json.Marshal writes nil as `null`; and this
		// module's codec refuses a null ANYWHERE (attempt_schema.go). So the boundary
		// committed and no reader — the replay, the covered wrappers, the import —
		// could ever decode it again, while Begin's in-memory return said it had
		// worked. The empty census is spelled here, at the single producer of the
		// document, and NOT by teaching the reader to accept a null: that would make
		// "censused and empty" and "absent" the same word for every future writer,
		// which is precisely the distinction the frontier exists to carry.
		//
		// Only the stored shape moves. nil and an empty slice are indistinguishable
		// to sortPendingGroups, to len and to groupSetDigest, so every digest above —
		// and every digest a reader recomputes — is byte-identical either way.
		pendingGroups := pending
		if pendingGroups == nil {
			pendingGroups = []jsonPendingGroup{}
		}

		doc := jsonFrontier{
			FrontierDigest:           string(frontier),
			FrontierAt:               now.String(),
			PendingGroups:            pendingGroups,
			PendingGroupCount:        jsonInt(len(pending)),
			PendingGroupDigest:       string(pendingDigest),
			HistoricalTerminalDigest: string(terminalDigest),
			HistoricalTerminalCount:  jsonInt(len(terminal)),
			OriginalRequest:          encodeActivationRequest(req),
			Evidence:                 encodeEvidenceRefs(req.Evidence),
		}
		body, err := marshalBounded(doc, maxFrontierPayloadBytes, "activation frontier")
		if err != nil {
			// Over the declared bound. The whole Begin fails: there is deliberately no
			// chunking, because a frontier written in parts is a boundary that is only
			// partly committed.
			return attemptErr(errCodeLedgerIncomplete, err)
		}

		// The transition's own evidence, verified before any write. This is a
		// DIFFERENT check from the query above: current access to read is not
		// permission to cross a boundary.
		actor, err := m.verifyAttempt(ctx, sc, EvidenceCheck{
			Operation:      opBeginActivation,
			ProposedDigest: scopeProposalDigest(tenant, lifecycleQuiescing, frontier, req),
			Evidence:       req.Evidence,
		})
		if err != nil {
			return err
		}

		repo, err := lifecycleRepoOf(sc)
		if err != nil {
			return err
		}
		created, err := repo.Create(ctx, model.Record{
			colScopeState:      lifecycleQuiescing,
			colScopeFrontierAt: now.String(),
			colScopeFrontier:   body,
		})
		if err != nil {
			return mapAttemptWriteErr(err)
		}
		id, perr := model.ParseID(created.String(model.ColID))
		if perr != nil {
			return attemptErr(errCodeLedgerIndeterminate, perr)
		}
		if err := appendAttemptAudit(ctx, sc, actor, model.AuditDraft{
			Action:     auditActionBeginActivation,
			TargetKind: lifecycleScopeKind,
			TargetID:   id,
			Meta: map[string]any{
				"state":                      lifecycleQuiescing,
				"frontier_digest":            string(frontier),
				"pending_group_count":        fmt.Sprintf("%d", len(pending)),
				"pending_group_digest":       string(pendingDigest),
				"historical_terminal_digest": string(terminalDigest),
			},
		}); err != nil {
			return err
		}
		version, ok := int64Cell(created, model.ColVersion)
		if !ok {
			return attemptErr(errCodeLedgerIndeterminate, nil)
		}
		view = LifecycleScopeView{
			ID: id, Version: version, State: lifecycleQuiescing, FrontierAt: now,
			FrontierDigest: frontier, PendingGroupCount: int64(len(pending)),
			PendingGroupDigest: pendingDigest, HistoricalTerminalDigest: terminalDigest,
			Evidence: req.Evidence,
		}
		return nil
	})
	if err != nil {
		return LifecycleScopeView{}, err
	}
	return view, nil
}

// appendAttemptAudit appends the operation's audit and REQUIRES a nonzero receipt.
// The explicit degrade policy of the ledger returns a zero event with a nil error;
// for a privileged recovery write that is not "audited", so it is turned into an
// error that rolls the whole transaction back.
func appendAttemptAudit(ctx context.Context, sc store.Scope, actor VerifiedAttemptActor, d model.AuditDraft) error {
	d.Actor = actor.Actor
	d.ActorKind = actor.ActorKind
	ev, err := sc.Audit().Append(ctx, d)
	if err != nil {
		return storeErr(err)
	}
	if ev.Seq == 0 {
		return attemptErr(errCodeEvidenceNotAnchored, nil)
	}
	return nil
}

// mapAttemptWriteErr classifies a write failure. store.ErrConflict keeps its
// identity for errors.Is; it is NEVER absorbed as a successful replay, because a
// unique-index violation inside a PostgreSQL transaction has already aborted it.
func mapAttemptWriteErr(err error) error {
	switch {
	case errors.Is(err, store.ErrConflict):
		return attemptErr(errCodeStaleAttempt, err)
	default:
		return storeErr(err)
	}
}

// bytesEqual compares two canonical streams.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// -----------------------------------------------------------------------------
// The census
// -----------------------------------------------------------------------------

// reservationLinkage is how a reservation row declares which ledger owns it.
type reservationLinkage int

const (
	// linkageLegacy: BOTH lifecycle columns are absent/NULL.
	linkageLegacy reservationLinkage = iota
	// linkageV1: a valid attempt ref AND the known lifecycle version.
	linkageV1
	// linkageMalformed: anything else — an empty ref, an unknown version, one
	// column set without the other. It is NOT legacy. Reading it as legacy would
	// hand a v1 obligation to a branch that expires holds on a TTL.
	linkageMalformed
)

// linkageOf classifies one reservation row.
func linkageOf(r model.Record) (reservationLinkage, AttemptRef) {
	refCell, hasRef := r[colResvAttemptRef]
	verCell, hasVer := r[colResvLifecycleVersion]
	refNull := !hasRef || refCell == nil
	verNull := !hasVer || verCell == nil
	if refNull && verNull {
		return linkageLegacy, ""
	}
	if refNull != verNull {
		return linkageMalformed, ""
	}
	ref, ok := textCell(r, colResvAttemptRef)
	if !ok || !validAttemptRef(AttemptRef(ref)) {
		return linkageMalformed, ""
	}
	version, ok := int64Cell(r, colResvLifecycleVersion)
	if !ok || version != lifecycleLinkageVersion {
		return linkageMalformed, ""
	}
	return linkageV1, AttemptRef(ref)
}

// pendingChildState reports whether a legacy row still holds an obligation.
// expired counts as PENDING: the money was never settled, and an import must
// explicitly re-hold it rather than treat a lapsed TTL as a resolution.
func pendingChildState(state string) (pending bool, known bool) {
	switch state {
	case resvStateActive, resvStateExpired:
		return true, true
	case resvStateCommitted, resvStateReleased:
		return false, true
	}
	return false, false
}

// censusLegacyGroups enumerates EVERY reservation group of the tenant and splits
// the legacy ones into the pending frontier set and the frozen terminal baseline.
//
// What blocks, rather than quietly shrinking the census:
//
//   - an incomplete scan (page cap, unusable or cycling cursor);
//   - a row whose tenant, handle, amount or state cannot be read exactly;
//   - a group mixing pending and terminal rows — a partly settled obligation is
//     not history and is not a clean hold;
//   - a group mixing legacy and v1 children, or carrying a malformed linkage;
//   - an existing v1 group whose parent is absent, owns another handle, or whose
//     manifest does not cover exactly this group's children.
func censusLegacyGroups(ctx context.Context, sc store.Scope) (pending, terminal []jsonPendingGroup, err error) {
	repo, err := reservationRepoOf(sc)
	if err != nil {
		return nil, nil, err
	}
	rows, scan, err := scanRows(ctx, repo, nil)
	if err != nil {
		return nil, nil, storeErr(err)
	}
	if !scan.complete() {
		return nil, nil, attemptErr(errCodeLedgerIncomplete, nil)
	}
	parents, err := readAllAttempts(ctx, sc)
	if err != nil {
		return nil, nil, err
	}

	tenant := sc.Tenant()
	// coveredParents records the handles a v1 group was found and validated for, so
	// the parent-side pass below can name the parents nothing covered.
	coveredParents := map[model.ID]bool{}
	type group struct {
		rows   []model.Record
		legacy int
		v1     int
		refs   map[AttemptRef]bool
	}
	groups := map[string]*group{}
	order := make([]string, 0, len(rows))
	for _, r := range rows {
		if tenantCellOf(r, tenant) != tenantCellMatches {
			return nil, nil, attemptErr(errCodeLedgerIndeterminate, nil)
		}
		handle, ok := textCell(r, colResvHandle)
		if !ok || handle == "" {
			return nil, nil, attemptErr(errCodeLedgerIndeterminate, nil)
		}
		g, seen := groups[handle]
		if !seen {
			g = &group{refs: map[AttemptRef]bool{}}
			groups[handle] = g
			order = append(order, handle)
		}
		g.rows = append(g.rows, r)
		switch link, ref := linkageOf(r); link {
		case linkageLegacy:
			g.legacy++
		case linkageV1:
			g.v1++
			g.refs[ref] = true
		default:
			return nil, nil, attemptErr(errCodeLedgerIndeterminate, nil)
		}
	}

	for _, handle := range order {
		g := groups[handle]
		if g.legacy > 0 && g.v1 > 0 {
			// A partially linked group belongs to neither ledger and must not
			// disappear from either set.
			return nil, nil, attemptErr(errCodeLegacyUnresolved, nil)
		}
		handleID, perr := model.ParseID(handle)
		if perr != nil {
			return nil, nil, attemptErr(errCodeLedgerIndeterminate, perr)
		}
		if g.v1 > 0 {
			// Existing v1 groups (including imports taken before this Begin) are
			// validated separately and are NOT re-imported or matched against their
			// current child versions.
			if err := validateExistingV1Group(tenant, handleID, g.refs, g.rows, parents); err != nil {
				return nil, nil, err
			}
			coveredParents[handleID] = true
			continue
		}
		snapshots := make([]jsonChildRow, 0, len(g.rows))
		pendingRows, terminalRows := 0, 0
		for _, r := range g.rows {
			snap, serr := childRowSnapshot(r)
			if serr != nil {
				return nil, nil, attemptErr(errCodeLedgerIndeterminate, serr)
			}
			if snap.Amount < 0 {
				return nil, nil, attemptErr(errCodeLedgerIndeterminate, nil)
			}
			isPending, known := pendingChildState(snap.State)
			if !known {
				return nil, nil, attemptErr(errCodeLedgerIndeterminate, nil)
			}
			if isPending {
				pendingRows++
			} else {
				terminalRows++
			}
			snapshots = append(snapshots, snap)
		}
		if pendingRows > 0 && terminalRows > 0 {
			return nil, nil, attemptErr(errCodeLegacyUnresolved, nil)
		}
		sortChildRows(snapshots)
		entry := jsonPendingGroup{
			Handle:      handle,
			GroupDigest: string(groupDigest(tenant, handleID, snapshots)),
			ChildCount:  jsonInt(len(snapshots)),
		}
		if pendingRows > 0 {
			pending = append(pending, entry)
		} else {
			terminal = append(terminal, entry)
		}
	}

	// THE OTHER DIRECTION (R3). Everything above walks the reservation rows and
	// judges the groups it SEES. A held parent whose children have all disappeared
	// owns no rows, so it was never visited — and Begin certified a boundary over a
	// ledger it had not reconciled. The hold reader already refused that state; the
	// census, which is what a later activation is judged against, did not.
	//
	// Versions are deliberately NOT compared here: the import itself advances them,
	// and so does every legitimate v1 write. What must hold is that each parent's
	// group exists and is coherent.
	for _, parent := range parents {
		if coveredParents[parent.Handle] {
			continue
		}
		if parent.Phase == phasePrepared && len(parent.Targets) == 0 {
			if err := validateAttemptGroup(tenant, parent, nil, true); err != nil {
				return nil, nil, err
			}
			continue
		}
		return nil, nil, attemptErr(errCodeLegacyUnresolved, nil)
	}
	return pending, terminal, nil
}

// validateExistingV1Group checks an already-imported group against its parent.
//
// CARDINALITY IS NOT COHERENCE (R3). The first cut compared the handle, the owner
// reference and the set of ids, and stopped there — so a group whose live amount no
// longer matched the immutable original, or whose child had been settled out from
// under a held parent, or whose child had been re-pointed at another policy, was
// certified and frozen into the boundary. Every fact a hold decision rests on is
// checked here, against the parent's own manifest and its own immutable snapshot.
//
// Versions are deliberately NOT compared: the import itself advances them, and so
// does every legitimate v1 write. Treating that movement as corruption would block
// activation on ordinary work.
func validateExistingV1Group(
	tenant model.TenantID, handle model.ID, refs map[AttemptRef]bool,
	rows []model.Record, parents map[AttemptRef]AttemptView,
) error {
	blocked := func() error { return attemptErr(errCodeLegacyUnresolved, nil) }
	if len(refs) != 1 {
		// One reservation group is owned by exactly one parent.
		return blocked()
	}
	var ref AttemptRef
	for r := range refs {
		ref = r
	}
	parent, ok := parents[ref]
	if !ok || parent.Handle != handle {
		return blocked()
	}
	if err := validateAttemptGroup(tenant, parent, rows, true); err != nil {
		return blocked()
	}
	return nil
}

// validateAttemptGroup is the complete group reader shared by lookup/replay,
// census and holds. On partial scans it checks every observed row; absence and
// the reservation commitment are decidable only after the child scan completes.
// It never resolves mutable policy, entity or directory rows.
func validateAttemptGroup(tenant model.TenantID, parent AttemptView, rows []model.Record, complete bool) error {
	blocked := func() error { return attemptErr(errCodeLedgerIndeterminate, nil) }
	targets := make(map[model.ID]TargetSnapshot, len(parent.Targets))
	for _, t := range parent.Targets {
		if _, duplicate := targets[t.ChildID]; duplicate {
			return blocked()
		}
		targets[t.ChildID] = t
	}
	original := map[model.ID]model.Record{}
	if parent.LegacyImport != nil {
		for _, o := range parent.LegacyImport.OriginalChildren {
			id, err := model.ParseID(o.String(model.ColID))
			if err != nil {
				return blocked()
			}
			original[id] = o
		}
	}
	if complete && len(targets) != len(rows) {
		return blocked()
	}
	seen := map[model.ID]model.Record{}
	live := map[model.ID]model.Record{}
	for _, r := range rows {
		if tenantCellOf(r, tenant) != tenantCellMatches {
			return blocked()
		}
		id, err := model.ParseID(r.String(model.ColID))
		if err != nil {
			return blocked()
		}
		target, claimed := targets[id]
		if !claimed {
			return blocked()
		}
		if previous, duplicate := seen[id]; duplicate {
			if !complete && reflect.DeepEqual(previous, r) {
				continue
			}
			return blocked()
		}
		seen[id] = r
		live[id] = r
		link, ref := linkageOf(r)
		if link != linkageV1 || ref != parent.AttemptRef {
			return blocked()
		}
		// A HELD parent's children are held. A child that left the active state
		// under one is a settlement the lifecycle never performed.
		if !textCellEquals(r, colResvState, resvStateActive) {
			return blocked()
		}
		// The live attribution must be the manifest's attribution.
		if !textCellEquals(r, colResvPolicyRef, target.PolicyID.String()) ||
			!textCellEquals(r, colResvScopeKey, target.ScopeKey) ||
			!textCellEquals(r, colResvPeriodStart, target.PeriodStart.String()) ||
			!textCellEquals(r, colResvHandle, parent.Handle.String()) {
			return blocked()
		}
		amount, ok := int64Cell(r, colResvAmount)
		if !ok || amount < 0 {
			return blocked()
		}
		// AND THE WHOLE OF IT MUST BE THE HISTORY THE IMPORT PRESERVED — policy kind,
		// period and the nullable dimension included, not only the four fields the
		// manifest happens to carry. THE PARENT ITSELF IS NOT MONEY: every figure
		// compared here comes from a child row or from the immutable child snapshot,
		// never from the parent, which carries none.
		if orig, recorded := original[id]; recorded {
			if col := liveChildContradictsOriginal(r, orig); col != "" {
				return blocked()
			}
		}
		if parent.Phase == phasePrepared {
			seq, seqOK := int64Cell(r, colResvSeq)
			actual, actualOK := int64Cell(r, colResvActual)
			version, versionOK := int64Cell(r, model.ColVersion)
			want := parent.Binding.Estimate.AmountMicroUSD
			if target.Action == "unlimited" {
				want = 0
			}
			if !canonicalAttemptID(model.ID(r.String(model.ColID)), true) ||
				!textCellEquals(r, colResvPolicyKind, target.PolicyKind) || !textCellEquals(r, colResvDimension, target.Dimension) ||
				!textCellEquals(r, colResvPeriod, target.Period) || !textCellEquals(r, colResvExpiresAt, parent.ReviewAfter.String()) ||
				!seqOK || seq < 1 || !actualOK || actual != 0 || amount != want || !versionOK || version < 1 || !r.IsNull(colResvSettledAt) {
				return blocked()
			}
		}
	}
	if complete && parent.Phase == phasePrepared {
		amounts, seqs := make([]int64, len(parent.Targets)), make([]int64, len(parent.Targets))
		for i, tg := range parent.Targets {
			amounts[i], _ = int64Cell(live[tg.ChildID], colResvAmount)
			seqs[i], _ = int64Cell(live[tg.ChildID], colResvSeq)
		}
		if reservationDigestOf(tenant, parent.AttemptRef, parent.Handle, parent.BindingDigest, nil,
			parent.AccountingBasis, parent.AccountingAt, parent.Targets, amounts, seqs) != parent.ReservationDigest {
			return blocked()
		}
	}
	return nil
}

// liveChildContradictsOriginal compares a LIVE reservation row against the
// immutable original row the import preserved, and reports the first historical
// fact they disagree on.
//
// It exists because "policy_ref, dim_key, period_start, handle, amount" was not the
// whole of a child's identity. The row also carries its POLICY KIND, its PERIOD and
// a NULLABLE DIMENSION, and those are history too: a child that was a monthly
// budget target is not a total one, and a dimension history never recorded is not a
// dimension it recorded as empty. No operation in this cut can authorize a
// transition on any of them, so a disagreement is a contradiction wherever it is
// seen — in Begin's census and in the hold reader alike, which is why there is one
// function rather than two lists that can drift.
//
// What it does NOT compare, deliberately: the child VERSION, which the import
// itself advances and every legitimate v1 write moves; and nothing at all is read
// from the policy as it stands today.
//
// It returns "" when the live row is faithful to its original.
func liveChildContradictsOriginal(live, orig model.Record) string {
	text := func(col string) (string, string, bool) {
		l, lok := textCell(live, col)
		o, ook := textCell(orig, col)
		if lok != ook {
			return l, o, false
		}
		return l, o, l == o
	}
	for _, col := range []string{
		colResvPolicyRef,
		colResvPolicyKind,
		colResvScopeKey,
		colResvPeriod,
		colResvPeriodStart,
		colResvHandle,
	} {
		if _, _, same := text(col); !same {
			return col
		}
	}
	// The nullable one, compared on PRESENCE first. An absent cell and a present
	// empty string are two different historical facts and the snapshot keeps them
	// apart; a comparison that read both as "" would erase exactly that.
	if _, _, same := text(colResvDimension); !same {
		return colResvDimension
	}
	liveAmount, lok := int64Cell(live, colResvAmount)
	origAmount, ook := int64Cell(orig, colResvAmount)
	if !lok || !ook || liveAmount != origAmount {
		return colResvAmount
	}
	return ""
}

// readAllAttempts reads the tenant's complete attempt parent set, indexed by ref.
// It is bounded by the same paging rules as every other scan; an incomplete
// enumeration refuses.
func readAllAttempts(ctx context.Context, sc store.Scope) (map[AttemptRef]AttemptView, error) {
	repo, err := attemptRepoOf(sc)
	if err != nil {
		return nil, err
	}
	rows, scan, err := scanRows(ctx, repo, nil)
	if err != nil {
		return nil, storeErr(err)
	}
	if !scan.complete() {
		return nil, attemptErr(errCodeLedgerIncomplete, nil)
	}
	out := make(map[AttemptRef]AttemptView, len(rows))
	for _, r := range rows {
		view, derr := decodeAttemptRow(sc.Tenant(), r)
		if derr != nil {
			return nil, derr
		}
		if _, dup := out[view.AttemptRef]; dup {
			return nil, attemptErr(errCodeLedgerIndeterminate, nil)
		}
		out[view.AttemptRef] = view
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// Reading an attempt parent
// -----------------------------------------------------------------------------

// decodeAttemptRow validates and decodes one parent row.
//
// It refuses rather than repairs. Every refusal below is a shape this binary
// cannot interpret — a corrupt row, or a row written by a phase this cut does not
// implement — and a financial reader that guessed at such a row would be inventing
// the very facts it exists to report.
func decodeAttemptRow(tenant model.TenantID, rec model.Record) (AttemptView, error) {
	bad := func(cause error) (AttemptView, error) {
		return AttemptView{}, attemptErr(errCodeLedgerIndeterminate, cause)
	}
	if tenantCellOf(rec, tenant) != tenantCellMatches {
		return bad(nil)
	}
	if v, ok := int64Cell(rec, colAttemptContractVersion); !ok || v != attemptContractVersion {
		// An unknown contract version does not degrade to a readable row.
		return bad(nil)
	}
	id, err := model.ParseID(rec.String(model.ColID))
	if err != nil {
		return bad(err)
	}
	version, ok := int64Cell(rec, model.ColVersion)
	if !ok {
		return bad(nil)
	}
	refText, ok := textCell(rec, colAttemptRef)
	if !ok || !validAttemptRef(AttemptRef(refText)) {
		return bad(nil)
	}
	requestRefText, ok := textCell(rec, colAttemptRequestRef)
	if !ok || !validAttemptRef(AttemptRef(requestRefText)) {
		return bad(nil)
	}
	handleText, ok := textCell(rec, colAttemptHandle)
	if !ok {
		return bad(nil)
	}
	handle, err := model.ParseID(handleText)
	if err != nil || handle.IsZero() {
		return bad(err)
	}
	phaseText, ok := textCell(rec, colAttemptPhase)
	if !ok || !knownPhase(AttemptPhase(phaseText)) {
		return bad(nil)
	}
	if !interpretablePhase(AttemptPhase(phaseText)) {
		// KNOWING A PHASE'S NAME IS NOT BEING ABLE TO READ IT (R4). Every phase but
		// outcome_unknown implies data this cut has no decoder for — a bound effect,
		// an outcome document, a price, a settlement link — and a reader that accepted
		// one would be resting a hold on fields nobody validated, or concluding
		// "terminal, holds nothing" from a row it cannot check. The phase's own reader
		// ships with the phase's own writer; until then this is an integrity fault.
		return bad(nil)
	}
	bindingDigest, ok := textCell(rec, colAttemptBindingDigest)
	if !ok || !validDigest(Digest(bindingDigest)) {
		return bad(nil)
	}
	resvDigest, ok := textCell(rec, colAttemptResvDigest)
	if !ok || !validDigest(Digest(resvDigest)) {
		return bad(nil)
	}
	bindingBody, ok := textCell(rec, colAttemptBinding)
	if !ok {
		return bad(nil)
	}
	var jb jsonBinding
	if err := strictUnmarshal(bindingBody, &jb); err != nil {
		return bad(err)
	}
	binding, err := decodeBinding(jb)
	if err != nil {
		return bad(err)
	}
	targetsBody, ok := textCell(rec, colAttemptTargets)
	if !ok {
		return bad(nil)
	}
	var jt []jsonTarget
	if err := strictUnmarshal(targetsBody, &jt); err != nil {
		return bad(err)
	}
	targets, err := decodeTargets(jt)
	if err != nil {
		return bad(err)
	}
	basisBody, ok := textCell(rec, colAttemptAccountingBasis)
	if !ok {
		return bad(nil)
	}
	var jbasis jsonAccountingBasis
	if err := strictUnmarshal(basisBody, &jbasis); err != nil {
		return bad(err)
	}
	basis, err := decodeAccountingBasis(jbasis)
	if err != nil {
		return bad(err)
	}
	reviewText, ok := textCell(rec, colAttemptReviewAfter)
	if !ok {
		return bad(nil)
	}
	review, err := model.ParseTimestamp(reviewText)
	if err != nil {
		return bad(err)
	}
	ownerRef, ok := textCell(rec, colAttemptOwnerRef)
	if !ok || ownerRef == "" {
		return bad(nil)
	}
	ownerEpoch, ok := int64Cell(rec, colAttemptOwnerEpoch)
	if !ok || ownerEpoch < 1 {
		return bad(nil)
	}
	publication, ok := textCell(rec, colAttemptPublication)
	if !ok {
		return bad(nil)
	}
	switch publication {
	case publicationNone, publicationPending, publicationAttempted:
	default:
		return bad(nil)
	}

	var originalRows []jsonChildRow
	view := AttemptView{
		ID: id, AttemptRef: AttemptRef(refText), Handle: handle, Version: version,
		Phase: AttemptPhase(phaseText), BindingDigest: Digest(bindingDigest),
		ReservationDigest: Digest(resvDigest), AccountingBasis: basis,
		ReviewAfter: review, OwnerRef: ownerRef, OwnerEpoch: ownerEpoch,
		PublicationState: publication, Targets: targets, Binding: binding,
	}
	if v, ok := textCell(rec, colAttemptAccountingAt); ok {
		ts, perr := model.ParseTimestamp(v)
		if perr != nil {
			return bad(perr)
		}
		view.AccountingAt = &ts
	}
	if v, ok := textCell(rec, colAttemptEffectDigest); ok {
		d := Digest(v)
		if !validDigest(d) {
			return bad(nil)
		}
		view.EffectDigest = &d
	}
	if v, ok := textCell(rec, colAttemptOutcomeDigest); ok {
		d := Digest(v)
		if !validDigest(d) {
			return bad(nil)
		}
		view.OutcomeDigest = &d
	}
	if v, ok := textCell(rec, colAttemptSettlementDgst); ok {
		d := Digest(v)
		if !validDigest(d) {
			return bad(nil)
		}
		view.SettlementDigest = &d
	}
	if v, ok := textCell(rec, colAttemptSampleID); ok {
		sid, perr := model.ParseID(v)
		if perr != nil {
			return bad(perr)
		}
		view.SampleID = &sid
	}
	if v, ok := textCell(rec, colAttemptCostRecordID); ok {
		cid, perr := model.ParseID(v)
		if perr != nil {
			return bad(perr)
		}
		view.CostRecordID = &cid
	}
	if v, ok := textCell(rec, colAttemptLegacyHandles); ok {
		var snaps []jsonImportSnapshot
		if err := strictUnmarshal(v, &snaps); err != nil {
			return bad(err)
		}
		// Exactly ONE imported handle per parent. The column is an array because the
		// contract declares it so; a second element would be an alias this cut has no
		// evidence to justify.
		if len(snaps) != 1 {
			return bad(nil)
		}
		snapshot, snapRows, serr := decodeImportSnapshot(snaps[0])
		if serr != nil {
			return bad(serr)
		}
		if snapshot.OriginalRequest.Handle != handle {
			return bad(nil)
		}
		view.LegacyImport = &snapshot
		originalRows = snapRows
	}

	// Cross-field coherence. The combinations below cannot be produced by any
	// writer in this package, so meeting one means corruption or a writer this
	// binary does not have — and either way the row is not safely readable.
	if view.LegacyImport != nil {
		// An imported attempt NEVER carries a dispatch marker or an effect digest.
		if view.EffectDigest != nil {
			return bad(nil)
		}
		if _, marked := textCell(rec, colAttemptDispatchRef); marked {
			return bad(nil)
		}
		if _, marked := textCell(rec, colAttemptDispatchMarked); marked {
			return bad(nil)
		}
		if view.Binding.Status != bindingLegacyUnbound && view.AccountingBasis.Kind == basisAttemptAdmission {
			return bad(nil)
		}
	}
	if heldPhase(view.Phase) {
		// A held attempt has not settled: no settlement links, nothing published.
		if view.SettlementDigest != nil || view.SampleID != nil || view.CostRecordID != nil {
			return bad(nil)
		}
		if _, settled := textCell(rec, colAttemptSettledAt); settled {
			return bad(nil)
		}
		if view.PublicationState != publicationNone {
			return bad(nil)
		}
	}
	// A NULL accounting instant is meaningful only as legacy_unknown, and a known
	// instant must say on what basis it is known.
	switch view.AccountingBasis.Kind {
	case basisLegacyUnknown:
		if view.AccountingAt != nil {
			return bad(nil)
		}
	case basisLegacyEvidenced, basisAttemptAdmission:
		if view.AccountingAt == nil {
			return bad(nil)
		}
	}
	if err := validateAttemptCommitments(tenant, view, originalRows); err != nil {
		return AttemptView{}, err
	}
	if view.Phase == phasePrepared {
		if requestRefText != string(binding.RequestRef) || !canonicalAttemptID(model.ID(rec.String(model.ColID)), true) ||
			!canonicalAttemptID(model.ID(handleText), true) || version < 1 ||
			len(bindingBody)+len(targetsBody)+len(basisBody) > maxAttemptPayloadBytes {
			return bad(nil)
		}
		for _, t := range jt {
			if !canonicalAttemptID(model.ID(t.ChildID), true) || !canonicalAttemptID(model.ID(t.PolicyID), true) {
				return bad(nil)
			}
		}
		for _, col := range []string{colAttemptEffectDigest, colAttemptDispatchRef, colAttemptDispatchMarked,
			colAttemptOutcome, colAttemptOutcomeDigest, colAttemptSettlementDgst, colAttemptSettledAt,
			colAttemptSampleKey, colAttemptSampleID, colAttemptCostRecordID, colAttemptResolutionEvid, colAttemptLegacyHandles} {
			if !rec.IsNull(col) {
				return bad(nil)
			}
		}
	}
	return view, nil
}

// findAttemptByRef is the EXACT lookup by (tenant, attempt_ref).
func findAttemptByRef(ctx context.Context, sc store.Scope, ref AttemptRef) (model.Record, AttemptView, bool, error) {
	repo, err := attemptRepoOf(sc)
	if err != nil {
		return nil, AttemptView{}, false, err
	}
	rows, page, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{eq(colAttemptRef, string(ref))},
		Limit:   2,
	})
	if err != nil {
		return nil, AttemptView{}, false, storeErr(err)
	}
	if len(rows) == 0 {
		if page.HasMore {
			return nil, AttemptView{}, false, attemptErr(errCodeLedgerIncomplete, nil)
		}
		return nil, AttemptView{}, false, nil
	}
	if len(rows) > 1 {
		return nil, AttemptView{}, false, attemptErr(errCodeLedgerIndeterminate, nil)
	}
	view, err := decodeAttemptRow(sc.Tenant(), rows[0])
	if err != nil {
		return nil, AttemptView{}, false, err
	}
	if page.HasMore {
		return nil, AttemptView{}, false, attemptErr(errCodeLedgerIncomplete, nil)
	}
	if view.AttemptRef != ref {
		return nil, AttemptView{}, false, attemptErr(errCodeLedgerIndeterminate, nil)
	}
	if view.Phase == phasePrepared {
		repo, err := reservationRepoOf(sc)
		if err != nil {
			return nil, AttemptView{}, false, err
		}
		// The union catches both an extra linked child with a changed handle and a
		// legacy/mislinked child left under the parent's handle, including zero-target
		// parents. Inspect once per identity; no per-target database lookup.
		for _, filter := range []model.Filter{eq(colResvAttemptRef, string(ref)), eq(colResvHandle, view.Handle.String())} {
			children, scan, err := scanRows(ctx, repo, []model.Filter{filter})
			if err != nil {
				return nil, AttemptView{}, false, storeErr(err)
			}
			if err := validateAttemptGroup(sc.Tenant(), view, children, scan.complete()); err != nil {
				return nil, AttemptView{}, false, err
			}
			if !scan.complete() {
				return nil, AttemptView{}, false, attemptErr(errCodeLedgerIncomplete, nil)
			}
		}
	}
	return rows[0], view, true, nil
}

// GetAttempt returns one attempt of this tenant.
//
// It VERIFIES CURRENT AUTHORITY BEFORE IT LOOKS. A component that has lost
// recovery access to this tenant is refused without learning whether the reference
// exists — the refusal is not conditioned on the row, so it leaks nothing. For an
// authorized reader, a reference that exists only in ANOTHER tenant is
// attempt_not_found here, and that other tenant is never consulted.
//
// A read produces no grant, no transition and no audit. The reference, the parent
// id and the parent's history are not credentials.
func (m *Module) GetAttempt(ctx context.Context, tenant model.TenantID, ref AttemptRef) (AttemptView, error) {
	if m.data == nil {
		return AttemptView{}, attemptErr(errCodeCapabilityUnavailable, nil)
	}
	if err := businessTenant(tenant); err != nil {
		return AttemptView{}, err
	}
	if !validAttemptRef(ref) {
		return AttemptView{}, attemptErr(errCodeInvalidAttempt, nil)
	}
	if m.attemptVerifier == nil {
		return AttemptView{}, attemptErr(errCodeCapabilityUnavailable, nil)
	}
	var out AttemptView
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		if _, verr := m.verifyAttempt(ctx, sc, queryCheck(ref)); verr != nil {
			return verr
		}
		_, view, found, ferr := findAttemptByRef(ctx, sc, ref)
		if ferr != nil {
			return ferr
		}
		if !found {
			return attemptErrRef(errCodeAttemptNotFound, ref, nil)
		}
		out = view
		return nil
	})
	if err != nil {
		return AttemptView{}, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// ImportLegacyHold
// -----------------------------------------------------------------------------

// ImportLegacyHold brings ONE complete legacy reservation group under an attempt
// parent, as an internal, evidenced recovery operation.
//
// What it produces: a parent in phase outcome_unknown, every original child
// preserved (amounts, periods, sequences, handle) and explicitly re-held —
// including a child the TTL had already EXPIRED, whose previous state and
// settled_at survive only inside the immutable original snapshot. What it does NOT
// produce: a dispatch marker, an effect digest, a cost, a sample, a grant or any
// claim about when the work was accounted for.
//
// Order, and the order is the correctness:
//
//  1. current authority, then
//  2. the existing FinOps writer lock, then
//  3. the deterministic identity, then
//  4. THE PARENT LOOKUP — before the current child versions are read at all. An
//     exact replay is decided by comparing the stored ORIGINAL REQUEST BYTES, so a
//     repeat of the very request that already ran returns its view even though its
//     own import has since moved those versions. Comparing the versions first is
//     the defect this ordering exists to prevent.
//
// A replay returns the CURRENT view: it never restores the original owner, epoch or
// review instant, and it writes no audit.
func (m *Module) ImportLegacyHold(
	ctx context.Context, tenant model.TenantID, req ImportLegacyHoldRequest,
) (AttemptView, error) {
	if m.data == nil {
		return AttemptView{}, attemptErr(errCodeCapabilityUnavailable, nil)
	}
	if err := businessTenant(tenant); err != nil {
		return AttemptView{}, err
	}
	if err := validateImportRequest(req); err != nil {
		return AttemptView{}, err
	}
	if m.attemptVerifier == nil {
		return AttemptView{}, attemptErr(errCodeCapabilityUnavailable, nil)
	}

	ref := legacyImportRef(tenant, req.Handle)
	var out AttemptView
	err := m.mutateAttempt(ctx, tenant, func(sc store.Scope) error {
		if err := attemptLock(ctx, sc); err != nil {
			return err
		}
		if _, err := attemptNow(ctx, sc); err != nil {
			// The transaction clock is a required v1 capability. It is read here so a
			// scope that cannot supply it fails BEFORE any monetary decision, even
			// though this operation stamps no database instant of its own: the review
			// instant is the caller's and the accounting instant is history's.
			return err
		}
		if _, err := m.verifyAttempt(ctx, sc, queryCheck(ref)); err != nil {
			return err
		}

		existingRow, existingView, found, err := findAttemptByRef(ctx, sc, ref)
		if err != nil {
			return err
		}
		if found {
			return m.importReplay(existingRow, existingView, tenant, req, ref, &out)
		}

		group, err := readHandleGroup(ctx, sc, req.Handle)
		if err != nil {
			return err
		}
		if err := matchRequestedChildren(req, group); err != nil {
			return err
		}
		snapshots := make([]jsonChildRow, 0, len(group))
		for _, r := range group {
			snap, serr := childRowSnapshot(r)
			if serr != nil {
				return attemptErr(errCodeLedgerIndeterminate, serr)
			}
			if snap.Amount < 0 {
				return attemptErr(errCodeLedgerIndeterminate, nil)
			}
			snapshots = append(snapshots, snap)
		}
		sortChildRows(snapshots)
		gDigest := groupDigest(tenant, req.Handle, snapshots)

		frontierRef, err := frontierMembership(ctx, sc, req.Handle, gDigest)
		if err != nil {
			return err
		}

		targets := targetsFromRows(group)
		sortTargets(targets)
		amounts := make([]int64, 0, len(snapshots))
		seqs := make([]int64, 0, len(snapshots))
		for _, s := range snapshots {
			amounts = append(amounts, int64(s.Amount))
			seqs = append(seqs, int64(s.Seq))
		}

		basis := AccountingBasis{Kind: basisLegacyUnknown}
		if req.AccountingAt != nil {
			basis = AccountingBasis{Kind: basisLegacyEvidenced, Evidence: req.AccountingEvidence}
		}
		binding := legacyUnboundBinding(ref)
		bDigest := bindingDigestOf(tenant, ref, binding)
		reqDigest := importRequestDigest(tenant, req)
		impDigest := importDigest(tenant, req.Handle, reqDigest, gDigest, frontierRef)
		rDigest := reservationDigestOf(tenant, ref, req.Handle, bDigest, &impDigest,
			basis, req.AccountingAt, targets, amounts, seqs)

		// The transition's own evidence. Distinct from the authority query above: a
		// reader's access is not permission to import.
		actor, err := m.verifyAttempt(ctx, sc, EvidenceCheck{
			Operation:          opImport,
			LegacyImportDigest: &impDigest,
			ScopeFrontierRef:   frontierRef,
			AttemptRef:         ref,
			BindingDigest:      bDigest,
			ReservationDigest:  rDigest,
			OwnerRef:           req.OwnerRef,
			ProposedDigest:     reqDigest,
			ProposedBinding:    &binding,
			Evidence:           req.Evidence,
		})
		if err != nil {
			return err
		}

		snapshot := LegacyImportSnapshot{
			RequestDigest: reqDigest, GroupDigest: gDigest, ImportDigest: impDigest,
			OriginalRequest: req, FrontierRef: frontierRef,
			// The immutable originals, in the SAME decoded form a later read of the
			// stored document produces — so the view this call returns and the view a
			// lookup returns carry the same rows, not two shapes of them.
			OriginalChildren: childRecordsOf(snapshots),
		}
		created, err := m.writeImportedAttempt(ctx, sc, importWrite{
			tenant: tenant, ref: ref, req: req, targets: targets,
			binding: binding, bindingDigest: bDigest, reservationDigest: rDigest,
			basis: basis, snapshot: snapshot, snapshotRows: snapshots,
			group: group, actor: actor,
		})
		if err != nil {
			return err
		}
		out = created
		return nil
	})
	if err != nil {
		if ae := new(AttemptError); errors.As(err, &ae) && ae.AttemptRef == nil {
			ae.AttemptRef = &ref
		}
		return AttemptView{}, err
	}
	return out, nil
}

// validateImportRequest checks the request's shape before anything is opened.
func validateImportRequest(req ImportLegacyHoldRequest) error {
	if req.Handle.IsZero() {
		return attemptErr(errCodeInvalidAttempt, nil)
	}
	if req.OwnerRef == "" || req.ReviewAfter.IsZero() {
		return attemptErr(errCodeInvalidAttempt, nil)
	}
	if len(req.Children) == 0 || len(req.Children) > maxTargetsPerAttempt {
		return attemptErr(errCodeInvalidAttempt, nil)
	}
	seen := map[model.ID]bool{}
	for _, c := range req.Children {
		if c.ID.IsZero() || c.Version < 0 || seen[c.ID] {
			return attemptErr(errCodeInvalidAttempt, nil)
		}
		seen[c.ID] = true
	}
	if !validEvidenceRefs(req.Evidence) || !validEvidenceRefs(req.AccountingEvidence) {
		return attemptErr(errCodeInvalidAttempt, nil)
	}
	// The accounting claim and its evidence stand or fall together. A claimed
	// historical instant with no evidence would be a date the import invented;
	// evidence with no claim would be proof of nothing.
	if req.AccountingAt != nil && len(req.AccountingEvidence) == 0 {
		return attemptErr(errCodeEvidenceIncomplete, nil)
	}
	if req.AccountingAt == nil && len(req.AccountingEvidence) != 0 {
		return attemptErr(errCodeInvalidAttempt, nil)
	}
	return nil
}

// importReplay answers a repeat of an import that already ran. The comparison is on
// the stored ORIGINAL REQUEST BYTES, not on a digest alone: equal digests with
// different bytes would be a collision, and this is where the two are told apart.
func (m *Module) importReplay(
	row model.Record, view AttemptView, tenant model.TenantID,
	req ImportLegacyHoldRequest, ref AttemptRef, out *AttemptView,
) error {
	if view.LegacyImport == nil || view.Handle != req.Handle {
		// The identity resolved to a parent that is not this handle's import: a
		// collision, not a replay.
		return attemptErrRef(errCodeImportConflict, ref, nil)
	}
	stored := importRequestBytes(tenant, view.LegacyImport.OriginalRequest)
	if !bytesEqual(stored, importRequestBytes(tenant, req)) {
		return attemptErrRef(errCodeImportConflict, ref, nil)
	}
	_ = row
	*out = view
	return nil
}

// readHandleGroup enumerates the COMPLETE reservation group of one handle and
// refuses anything that is not a clean, wholly legacy group.
func readHandleGroup(ctx context.Context, sc store.Scope, handle model.ID) ([]model.Record, error) {
	repo, err := reservationRepoOf(sc)
	if err != nil {
		return nil, err
	}
	rows, scan, err := scanRows(ctx, repo, []model.Filter{eq(colResvHandle, handle.String())})
	if err != nil {
		return nil, storeErr(err)
	}
	if !scan.complete() {
		return nil, attemptErr(errCodeLedgerIncomplete, nil)
	}
	if len(rows) == 0 {
		return nil, attemptErr(errCodeLegacyUnresolved, nil)
	}
	tenant := sc.Tenant()
	for _, r := range rows {
		if tenantCellOf(r, tenant) != tenantCellMatches {
			return nil, attemptErr(errCodeLedgerIndeterminate, nil)
		}
		if link, _ := linkageOf(r); link != linkageLegacy {
			// Already linked, or partially linked. Either way this group is not an
			// unimported legacy obligation and must not become a second one.
			return nil, attemptErr(errCodeLegacyUnresolved, nil)
		}
		state, ok := textCell(r, colResvState)
		if !ok {
			return nil, attemptErr(errCodeLedgerIndeterminate, nil)
		}
		pending, known := pendingChildState(state)
		if !known {
			return nil, attemptErr(errCodeLedgerIndeterminate, nil)
		}
		if !pending {
			// A FIRST IMPORT ADOPTS A PENDING GROUP AND NOTHING ELSE. The first cut
			// checked only that the state was in the vocabulary, so a committed or
			// released group was accepted — and the write path then turned every child
			// back to active and cleared settled_at, resurrecting an obligation the
			// tenant had already been released from, with an audit event saying an
			// import had happened. The refusal is here, before any row or audit is
			// touched, and it covers the whole group: a group that mixes a live hold
			// with a settled one is not a hold, and importing the live part alone would
			// split an obligation the ledger recorded as one.
			return nil, attemptErr(errCodeLegacyUnresolved, nil)
		}
	}
	return rows, nil
}

// matchRequestedChildren checks the requested set against the enumerated group.
//
// The two failures are DIFFERENT and are reported differently: a set that does not
// match is a request about another group (import_conflict, never retried), while a
// version that has moved is a stale read of the right group (stale_attempt,
// resolved by reading it again).
func matchRequestedChildren(req ImportLegacyHoldRequest, group []model.Record) error {
	if len(req.Children) != len(group) {
		return attemptErr(errCodeImportConflict, nil)
	}
	current := make(map[model.ID]int64, len(group))
	for _, r := range group {
		id, err := model.ParseID(r.String(model.ColID))
		if err != nil {
			return attemptErr(errCodeLedgerIndeterminate, err)
		}
		version, ok := int64Cell(r, model.ColVersion)
		if !ok {
			return attemptErr(errCodeLedgerIndeterminate, nil)
		}
		current[id] = version
	}
	for _, c := range req.Children {
		version, present := current[c.ID]
		if !present {
			return attemptErr(errCodeImportConflict, nil)
		}
		if version != c.Version {
			return attemptErr(errCodeStaleAttempt, nil)
		}
	}
	return nil
}

// frontierMembership checks this handle against the tenant's frontier, when there
// is one, and returns the server-captured reference to it.
//
// After a Begin, an import may only resolve a group the census recorded as
// PENDING, with the group digest the census recorded. A group that has since been
// deleted, re-labeled terminal or otherwise changed no longer matches, and a
// handle that appeared after the boundary was never covered by it. Both refuse:
// the census is not something a later write gets to edit.
//
// Before a Begin there is no frontier and the import proceeds without one, which
// the contract permits as internal recovery — and which is exactly why the
// snapshot records that no frontier reference was captured.
func frontierMembership(ctx context.Context, sc store.Scope, handle model.ID, digest Digest) (*EvidenceRef, error) {
	row, found, err := lifecycleScopeRow(ctx, sc)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	view, err := decodeLifecycleScope(sc.Tenant(), row)
	if err != nil {
		return nil, err
	}
	doc, err := frontierDocumentOf(row)
	if err != nil {
		return nil, err
	}
	for _, g := range doc.PendingGroups {
		if g.Handle != handle.String() {
			continue
		}
		if g.GroupDigest != string(digest) {
			return nil, attemptErr(errCodeLegacyUnresolved, nil)
		}
		return &EvidenceRef{
			Kind:    evidenceStoreRow,
			Ref:     string(lifecycleScopeKind) + ":" + view.ID.String(),
			Digest:  view.FrontierDigest,
			Version: view.Version,
		}, nil
	}
	return nil, attemptErr(errCodeLegacyUnresolved, nil)
}

// targetsFromRows builds the target manifest from the original rows. Policy facts
// the row does not carry — the policy version, its spec digest, the configured
// limit and the static reserve — stay NULL: they are unknown history, and filling
// them from the policy as it stands today would record a fact that was never true.
func targetsFromRows(rows []model.Record) []TargetSnapshot {
	out := make([]TargetSnapshot, 0, len(rows))
	for _, r := range rows {
		childID, _ := model.ParseID(r.String(model.ColID))
		policyID, _ := model.ParseID(r.String(colResvPolicyRef))
		period := r.String(colResvPeriod)
		start, _ := model.ParseTimestamp(r.String(colResvPeriodStart))
		// The window bounds are derived from the row's OWN preserved period and
		// period_start through the module's existing period arithmetic — calendar
		// math over the stored history, not a lookup of the current policy. "total"
		// has no lower bound and therefore no window.
		_, hasBounds := periodStart(period, start.Time())
		end := model.NewTimestamp(periodEnd(period, start.Time()))
		out = append(out, TargetSnapshot{
			ChildID: childID, PolicyID: policyID,
			PolicyKind:  r.String(colResvPolicyKind),
			Dimension:   r.String(colResvDimension),
			ScopeKey:    r.String(colResvScopeKey),
			Period:      period,
			PeriodStart: start, PeriodEnd: end, HasPeriodBounds: hasBounds,
		})
	}
	return out
}

// importWrite is the fully validated write plan of one import.
type importWrite struct {
	tenant            model.TenantID
	ref               AttemptRef
	req               ImportLegacyHoldRequest
	targets           []TargetSnapshot
	binding           AttemptBinding
	bindingDigest     Digest
	reservationDigest Digest
	basis             AccountingBasis
	snapshot          LegacyImportSnapshot
	snapshotRows      []jsonChildRow
	group             []model.Record
	actor             VerifiedAttemptActor
}

// writeImportedAttempt performs the parent, every child and the audit in ONE
// transaction. A failure at any point rolls the whole group back: there is no
// state in which some children were adopted and others were not.
func (m *Module) writeImportedAttempt(ctx context.Context, sc store.Scope, w importWrite) (AttemptView, error) {
	bindingBody, err := marshalBounded(encodeBinding(w.binding), maxAttemptPayloadBytes, "attempt binding")
	if err != nil {
		return AttemptView{}, attemptErr(errCodeInvalidAttempt, err)
	}
	targetsBody, err := marshalBounded(encodeTargets(w.targets), maxAttemptPayloadBytes, "attempt targets")
	if err != nil {
		return AttemptView{}, attemptErr(errCodeInvalidAttempt, err)
	}
	basisBody, err := marshalBounded(encodeAccountingBasis(w.basis), maxAttemptPayloadBytes, "accounting basis")
	if err != nil {
		return AttemptView{}, attemptErr(errCodeInvalidAttempt, err)
	}
	legacyBody, err := marshalBounded(
		[]jsonImportSnapshot{encodeImportSnapshot(w.snapshot, w.snapshotRows)},
		maxAttemptPayloadBytes, "legacy import snapshot")
	if err != nil {
		return AttemptView{}, attemptErr(errCodeInvalidAttempt, err)
	}

	rec := model.Record{
		colAttemptContractVersion: attemptContractVersion,
		colAttemptRef:             string(w.ref),
		colAttemptRequestRef:      string(w.ref),
		colAttemptHandle:          w.req.Handle.String(),
		colAttemptPhase:           string(phaseOutcomeUnknown),
		colAttemptBindingDigest:   string(w.bindingDigest),
		colAttemptBinding:         bindingBody,
		colAttemptTargets:         targetsBody,
		colAttemptResvDigest:      string(w.reservationDigest),
		colAttemptReviewAfter:     w.req.ReviewAfter.String(),
		colAttemptOwnerRef:        w.req.OwnerRef,
		colAttemptOwnerEpoch:      int64(1),
		colAttemptPublication:     publicationNone,
		colAttemptLegacyHandles:   legacyBody,
		colAttemptAccountingBasis: basisBody,
	}
	if w.req.AccountingAt != nil {
		rec[colAttemptAccountingAt] = w.req.AccountingAt.String()
	}

	repo, err := attemptRepoOf(sc)
	if err != nil {
		return AttemptView{}, err
	}
	created, err := repo.Create(ctx, rec)
	if err != nil {
		// The (tenant, handle) unique index is what makes a second parent for one
		// group impossible. A conflict here is NOT absorbed as a successful replay:
		// on PostgreSQL the transaction is already aborted, and reporting success
		// would take the caller's other writes down with it.
		return AttemptView{}, mapAttemptWriteErr(err)
	}

	resvRepo, err := reservationRepoOf(sc)
	if err != nil {
		return AttemptView{}, err
	}
	for _, child := range w.group {
		next := model.Record{}
		for k, v := range child {
			next[k] = v
		}
		next[colResvAttemptRef] = string(w.ref)
		next[colResvLifecycleVersion] = lifecycleLinkageVersion
		// An expired child is EXPLICITLY re-held. Its previous state and settled_at
		// survive in the immutable original snapshot; the live row reflects a hold,
		// because that is what the obligation is.
		next[colResvState] = resvStateActive
		next[colResvSettledAt] = nil
		if _, err := resvRepo.Update(ctx, next); err != nil {
			return AttemptView{}, mapAttemptWriteErr(err)
		}
	}

	id, perr := model.ParseID(created.String(model.ColID))
	if perr != nil {
		return AttemptView{}, attemptErr(errCodeLedgerIndeterminate, perr)
	}
	if err := appendAttemptAudit(ctx, sc, w.actor, model.AuditDraft{
		Action:     auditActionImport,
		TargetKind: attemptKind,
		TargetID:   id,
		Meta: map[string]any{
			"attempt_ref":      string(w.ref),
			"handle":           w.req.Handle.String(),
			"phase":            string(phaseOutcomeUnknown),
			"import_digest":    string(w.snapshot.ImportDigest),
			"group_digest":     string(w.snapshot.GroupDigest),
			"request_digest":   string(w.snapshot.RequestDigest),
			"accounting_basis": w.basis.Kind,
			// The absence is recorded EXPLICITLY: an imported attempt has no dispatch
			// reference, and the audit says so rather than leaving a reader to assume it.
			"dispatch_ref_absent": "true",
			"child_count":         fmt.Sprintf("%d", len(w.group)),
		},
	}); err != nil {
		return AttemptView{}, err
	}

	version, ok := int64Cell(created, model.ColVersion)
	if !ok {
		return AttemptView{}, attemptErr(errCodeLedgerIndeterminate, nil)
	}
	view := AttemptView{
		ID: id, AttemptRef: w.ref, Handle: w.req.Handle, Version: version,
		Phase: phaseOutcomeUnknown, BindingDigest: w.bindingDigest,
		ReservationDigest: w.reservationDigest, AccountingAt: w.req.AccountingAt,
		AccountingBasis: w.basis, LegacyImport: &w.snapshot,
		ReviewAfter: w.req.ReviewAfter, OwnerRef: w.req.OwnerRef, OwnerEpoch: 1,
		PublicationState: publicationNone, Targets: w.targets, Binding: w.binding,
	}
	return view, nil
}

// -----------------------------------------------------------------------------
// Semantic validation of the durable documents (R4)
// -----------------------------------------------------------------------------

// interpretablePhase reports whether THIS cut can read a parent in that phase.
//
// Imported outcome_unknown and prepared are the two shapes this package writes
// and can decode and check completely. The rest of the vocabulary is
// declared (a reader must classify a row it did not write rather than guess), but
// declaring a name is not the same as having the decoder the name implies.
func interpretablePhase(p AttemptPhase) bool { return p == phaseOutcomeUnknown || p == phasePrepared }

func validatePreparedParent(v AttemptView) error {
	bad := func() error { return attemptErr(errCodeLedgerIndeterminate, nil) }
	if v.LegacyImport != nil || v.AccountingBasis.Kind != basisAttemptAdmission || len(v.AccountingBasis.Evidence) != 0 ||
		v.AccountingAt == nil || v.AccountingAt.Time().IsZero() || v.ReviewAfter.Time().Before(v.AccountingAt.Time()) ||
		v.OwnerEpoch != 1 || !attemptText(v.OwnerRef, true) || v.PublicationState != publicationNone ||
		validatePreparedBinding(v.Binding) != nil || len(v.Targets) > maxTargetsPerAttempt {
		return bad()
	}
	ordered := append([]TargetSnapshot(nil), v.Targets...)
	sortTargets(ordered)
	ids, keys, seatPeriods := map[model.ID]bool{}, map[string]bool{}, map[string]bool{}
	for i, t := range v.Targets {
		if ordered[i].ChildID != t.ChildID || ids[t.ChildID] || !canonicalAttemptID(t.ChildID, true) || !canonicalAttemptID(t.PolicyID, true) ||
			t.PolicyVersion == nil || *t.PolicyVersion < 1 || t.PolicySpecDigest == nil || !validDigest(*t.PolicySpecDigest) ||
			t.StaticReservedMicroUSD == nil || *t.StaticReservedMicroUSD < 0 || !validPeriods[t.Period] || !validEvidenceRefs(t.Membership) {
			return bad()
		}
		ids[t.ChildID] = true
		key := string(canonBytes("target-key", func(w *canonWriter) { w.str(t.PolicyID.String()); w.str(t.ScopeKey); w.ts(t.PeriodStart) }))
		if keys[key] {
			return bad()
		}
		keys[key] = true
		start, bounded := periodStart(t.Period, v.AccountingAt.Time())
		if t.HasPeriodBounds != bounded || t.PeriodStart != model.NewTimestamp(start) || t.PeriodEnd != model.NewTimestamp(periodEnd(t.Period, start)) {
			return bad()
		}
		switch t.PolicyKind {
		case policyKindBudget:
			if !budgetDimensions[t.Dimension] || isGroupDimension(t.Dimension) || (t.Action != "block" && t.Action != "throttle") ||
				t.LimitMicroUSD == nil || *t.LimitMicroUSD <= 0 || len(t.Membership) != 0 {
				return bad()
			}
			if t.Dimension == "global" {
				if t.ScopeKey != "" {
					return bad()
				}
			} else if !knownAttemptScalar(v.Binding, t.Dimension, t.ScopeKey) {
				return bad()
			}
		case policyKindSpendLimit:
			if !v.Binding.ApplySeatLimits || t.Dimension != "spend_limit" || t.ScopeKey != v.Binding.Subject.ActorRef || t.Period == "total" ||
				*t.StaticReservedMicroUSD != 0 || seatPeriods[t.Period] {
				return bad()
			}
			seatPeriods[t.Period] = true
			if !bytesEqual(canonBytes("membership", func(w *canonWriter) { canonEvidenceRefs(w, t.Membership) }),
				canonBytes("membership", func(w *canonWriter) { canonEvidenceRefs(w, v.Binding.Attribution["user_group"].Evidence) })) {
				return bad()
			}
			if t.Action == "unlimited" {
				if t.LimitMicroUSD != nil {
					return bad()
				}
			} else if t.Action != "block" || t.LimitMicroUSD == nil || *t.LimitMicroUSD < 0 {
				return bad()
			}
		default:
			return bad()
		}
	}
	return nil
}

// validateAttemptCommitments RECOMPUTES every commitment this phase can derive and
// binds the target manifest to the immutable original children.
//
// The return that required it gave the exact counterexample: change only
// `targets[0].scope_key`, leave the child row, the snapshot and every stored digest
// alone, and the parent still parsed — after which the hold reader answered a
// confident ZERO for the scope the obligation actually belongs to. A digest that is
// only checked for its hex SHAPE certifies nothing; recomputing it is what makes it
// evidence.
//
// What is deliberately NOT attempted here: settlement, an outcome, or any future
// phase's data. Those are refused by interpretablePhase, not half-validated.
func validateAttemptCommitments(tenant model.TenantID, view AttemptView, originalRows []jsonChildRow) error {
	bad := func(cause error) error { return attemptErr(errCodeLedgerIndeterminate, cause) }

	// The binding digest must cover the binding as stored.
	if bindingDigestOf(tenant, view.AttemptRef, view.Binding) != view.BindingDigest {
		return bad(nil)
	}
	if view.Phase == phasePrepared {
		return validatePreparedParent(view)
	}
	// The other interpretable shape is an imported hold; it must retain its
	// immutable import snapshot and original commitments.
	snapshot := view.LegacyImport
	if snapshot == nil {
		return bad(nil)
	}
	if len(originalRows) == 0 || len(originalRows) != len(snapshot.OriginalChildren) {
		return bad(nil)
	}
	// The original request and the original group, recomputed from the bytes stored
	// beside their digests.
	if importRequestDigest(tenant, snapshot.OriginalRequest) != snapshot.RequestDigest {
		return bad(nil)
	}
	sorted := append([]jsonChildRow(nil), originalRows...)
	sortChildRows(sorted)
	if groupDigest(tenant, view.Handle, sorted) != snapshot.GroupDigest {
		return bad(nil)
	}
	if importDigest(tenant, view.Handle, snapshot.RequestDigest, snapshot.GroupDigest, snapshot.FrontierRef) != snapshot.ImportDigest {
		return bad(nil)
	}

	// THE TARGET SET IS BOUND TO THE ORIGINAL CHILDREN, one to one. A manifest that
	// names a child the group never had — or omits one it did — is not a manifest of
	// this import.
	byChild := make(map[model.ID]jsonChildRow, len(sorted))
	for _, r := range sorted {
		id, err := model.ParseID(r.ID)
		if err != nil {
			return bad(err)
		}
		if _, dup := byChild[id]; dup {
			return bad(nil)
		}
		byChild[id] = r
	}
	if len(view.Targets) != len(byChild) {
		return bad(nil)
	}
	seen := make(map[model.ID]bool, len(view.Targets))
	for _, t := range view.Targets {
		orig, ok := byChild[t.ChildID]
		if !ok || seen[t.ChildID] {
			return bad(nil)
		}
		seen[t.ChildID] = true
		// The attribution the manifest asserts must be the attribution the original
		// row carried. These four are the fields a hold decision rests on.
		if t.PolicyID.String() != orig.PolicyRef ||
			t.ScopeKey != orig.ScopeKey ||
			t.Period != orig.Period ||
			t.PeriodStart.String() != orig.PeriodStart {
			return bad(nil)
		}
		// An imported target never carries resolved policy facts: they were unknown
		// history and are not filled in from the policy as it stands today.
		if t.PolicyVersion != nil || t.PolicySpecDigest != nil ||
			t.LimitMicroUSD != nil || t.StaticReservedMicroUSD != nil {
			return bad(nil)
		}
	}

	// And the reservation digest, over exactly the inputs the writer framed.
	targets := append([]TargetSnapshot(nil), view.Targets...)
	sortTargets(targets)
	amounts := make([]int64, 0, len(sorted))
	seqs := make([]int64, 0, len(sorted))
	for _, r := range sorted {
		amounts = append(amounts, int64(r.Amount))
		seqs = append(seqs, int64(r.Seq))
	}
	imported := snapshot.ImportDigest
	if reservationDigestOf(tenant, view.AttemptRef, view.Handle, view.BindingDigest, &imported,
		view.AccountingBasis, view.AccountingAt, targets, amounts, seqs) != view.ReservationDigest {
		return bad(nil)
	}
	return nil
}

// validateFrontierCommitments recomputes the census commitments of a frontier
// document. The pending set is stored, so its digest is derivable; the terminal
// baseline is stored only as a digest, which is the contract's deliberate choice
// (a second monetary ledger is exactly what a frontier must not become), so the
// frontier digest is what binds it.
func validateFrontierCommitments(tenant model.TenantID, at model.Timestamp, doc jsonFrontier, evidence []EvidenceRef) error {
	bad := func() error { return attemptErr(errCodeLedgerIndeterminate, nil) }
	if int64(doc.PendingGroupCount) != int64(len(doc.PendingGroups)) {
		return bad()
	}
	if doc.FrontierAt != at.String() {
		return bad()
	}
	pending := append([]jsonPendingGroup(nil), doc.PendingGroups...)
	sortPendingGroups(pending)
	seen := make(map[string]bool, len(pending))
	for _, g := range pending {
		if seen[g.Handle] || !validDigest(Digest(g.GroupDigest)) || g.ChildCount < 1 {
			return bad()
		}
		if _, err := model.ParseID(g.Handle); err != nil {
			return bad()
		}
		seen[g.Handle] = true
	}
	if groupSetDigest(frontierTagPending, tenant, pending) != Digest(doc.PendingGroupDigest) {
		return bad()
	}
	if frontierDigestOf(tenant, at, int64(doc.PendingGroupCount),
		Digest(doc.PendingGroupDigest), Digest(doc.HistoricalTerminalDigest), evidence) != Digest(doc.FrontierDigest) {
		return bad()
	}

	// THE STORED BEGIN REQUEST IS VALIDATED BY ITS OWN CREATION FACTS (R4). The
	// commitments above cover the census and nothing else, so the canonical original
	// request kept for replay sat outside all of them: editing one field of it — the
	// ExpectedVersion, from the zero a first Begin necessarily carries, to one — left
	// every digest, instant and evidence intact and the document readable. That
	// creates no frontier, no grant and no activation; what it does is REPLACE the
	// durable identity of the Begin, so the genuine original request starts
	// conflicting while a request nobody ever made replays successfully.
	//
	// Two facts are derivable here without a new digest or a contract change:
	//
	//  1. A frontier row is only ever created by a Begin that found no row, and that
	//     path refuses a nonzero ExpectedVersion. So the stored request's version is
	//     zero, necessarily, for every row this cut can have written.
	//  2. The evidence the frontier preserved IS the evidence its request presented:
	//     the writer encodes the same set into both fields.
	stored, err := decodeActivationRequest(doc.OriginalRequest)
	if err != nil {
		return attemptErr(errCodeLedgerIndeterminate, err)
	}
	if stored.ExpectedVersion != 0 {
		return bad()
	}
	if !bytesEqual(
		canonBytes(domainScopeProposal, func(w *canonWriter) { canonEvidenceRefs(w, stored.Evidence) }),
		canonBytes(domainScopeProposal, func(w *canonWriter) { canonEvidenceRefs(w, evidence) }),
	) {
		return bad()
	}
	return nil
}
