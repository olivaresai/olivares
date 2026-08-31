// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package notify

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// createRouteInput declares a routing rule. name and destination are required; an
// empty match dimension means "any".
type createRouteInput struct {
	Name                  string   `json:"name"`
	Enabled               *bool    `json:"enabled"`
	MatchTypes            []string `json:"match_types"`
	MatchKinds            []string `json:"match_kinds"`
	MinSeverity           string   `json:"min_severity"`
	MatchSources          []string `json:"match_sources"`
	MatchSubjectKinds     []string `json:"match_subject_kinds"`
	Destination           string   `json:"destination"`
	DedupWindowSeconds    int64    `json:"dedup_window_seconds"`
	ThrottleWindowSeconds int64    `json:"throttle_window_seconds"`
	Priority              int64    `json:"priority"`
}

// nonNeg floors a window/priority value at zero (a negative window is meaningless).
func nonNeg(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

// validateDestination refuses a route that names a destination this tenant cannot
// address. The destination used to be stored as free text, clamped to a length and
// never compared to anything — so a route could name a destination that did not
// exist (a silent misconfiguration discovered only when a finding failed to deliver)
// or one provisioned for a DIFFERENT tenant.
//
// The message names only what the caller supplied. Listing the valid destinations
// here would hand a route author the names scoped to other tenants, which is the
// thing the scoping exists to withhold.
func (m *Module) validateDestination(mc api.ModuleContext, destination string) string {
	// An UNWIRED transport does not constrain authoring. An operator may legitimately
	// declare routes before provisioning destinations, and refusing here would make
	// the order mandatory for no gain: with no transport nothing delivers anyway, and
	// the ledger already records no_dispatcher. The check exists to stop a tenant
	// naming ANOTHER tenant's destination, which requires destinations to exist.
	if len(m.dispatch.Destinations()) == 0 {
		return ""
	}
	for _, d := range m.dispatch.DestinationsFor(mc.Tenant) {
		if d == destination {
			return ""
		}
	}
	return "destination " + clamp(destination, maxNameLen) + " is not provisioned for this tenant (see GET /destinations)"
}

// handleCreateRoute declares a routing rule. Creating a route is a privileged,
// self-audited write. A duplicate name is a 409.
func (m *Module) handleCreateRoute(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in createRouteInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Name == "" || in.Destination == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("name and destination are required"))
		return
	}
	if msg := m.validateDestination(mc, in.Destination); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	if !validSeverity(in.MinSeverity) {
		writeJSON(w, http.StatusBadRequest, errorBody("min_severity must be empty or info|low|medium|high|critical"))
		return
	}
	if msg := validateMatchTypes(in.MatchTypes); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	rec := model.Record{
		colName:          clamp(in.Name, maxNameLen),
		colEnabled:       enabled,
		colMatchTypes:    csvJoin(in.MatchTypes),
		colMatchKinds:    csvJoin(in.MatchKinds),
		colMinSeverity:   in.MinSeverity,
		colMatchSources:  csvJoin(in.MatchSources),
		colMatchSubjects: csvJoin(in.MatchSubjectKinds),
		colDestination:   clamp(in.Destination, maxNameLen),
		colDedupWindow:   nonNeg(in.DedupWindowSeconds),
		colThrottleWin:   nonNeg(in.ThrottleWindowSeconds),
		colPriority:      in.Priority,
		colOwnerActor:    mc.Principal.Actor(),
		colOwnerActorK:   mc.Principal.ActorKind(),
	}
	var dto routeDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(routeKind)
		if err != nil {
			return err
		}
		created, err := repo.Create(r.Context(), rec)
		if err != nil {
			return err
		}
		dto = toRouteDTO(created)
		if err := appendRevision(r.Context(), sc, mc, model.ID(dto.ID), revOpCreate, dto); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "notify.route.create", routeKind, model.ID(dto.ID), map[string]any{
			"name": dto.Name, "destination": dto.Destination, "enabled": dto.Enabled,
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

// handleListRoutes lists routing rules, optionally filtered by ?destination and
// ?enabled.
func (m *Module) handleListRoutes(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if d := r.URL.Query().Get("destination"); d != "" {
		q.Filters = append(q.Filters, eq(colDestination, d))
	}
	if en := r.URL.Query().Get("enabled"); en == "true" || en == "false" {
		q.Filters = append(q.Filters, eq(colEnabled, en == "true"))
	}
	out := []routeDTO{}
	var page model.Page
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(routeKind)
		if err != nil {
			return err
		}
		recs, p, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		page = p
		for _, rec := range recs {
			out = append(out, toRouteDTO(rec))
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse[routeDTO]{Items: out, Cursor: page.Cursor, HasMore: page.HasMore})
}

// handleGetRoute returns one route.
func (m *Module) handleGetRoute(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := chiID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var dto routeDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(routeKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		dto = toRouteDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// handleUpdateRoute replaces a route's predicate, destination and windows.
// Privileged write, self-audited. The name (the natural key) is not changed here.
func (m *Module) handleUpdateRoute(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := chiID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in createRouteInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Destination == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("destination is required"))
		return
	}
	if msg := m.validateDestination(mc, in.Destination); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	if !validSeverity(in.MinSeverity) {
		writeJSON(w, http.StatusBadRequest, errorBody("min_severity must be empty or info|low|medium|high|critical"))
		return
	}
	if msg := validateMatchTypes(in.MatchTypes); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var dto routeDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(routeKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		if in.Enabled != nil {
			rec[colEnabled] = *in.Enabled
		}
		rec[colMatchTypes] = csvJoin(in.MatchTypes)
		rec[colMatchKinds] = csvJoin(in.MatchKinds)
		rec[colMinSeverity] = in.MinSeverity
		rec[colMatchSources] = csvJoin(in.MatchSources)
		rec[colMatchSubjects] = csvJoin(in.MatchSubjectKinds)
		rec[colDestination] = clamp(in.Destination, maxNameLen)
		rec[colDedupWindow] = nonNeg(in.DedupWindowSeconds)
		rec[colThrottleWin] = nonNeg(in.ThrottleWindowSeconds)
		rec[colPriority] = in.Priority
		updated, err := repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		dto = toRouteDTO(updated)
		if err := appendRevision(r.Context(), sc, mc, id, revOpUpdate, dto); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "notify.route.update", routeKind, id, map[string]any{
			"name": dto.Name, "destination": dto.Destination, "enabled": dto.Enabled,
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// handleDeleteRoute removes a route (hard delete). Admin-tier, self-audited.
func (m *Module) handleDeleteRoute(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := chiID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(routeKind)
		if err != nil {
			return err
		}
		// Snapshot the pre-delete state BEFORE dropping it: the delete revision
		// is the evidence of what existed.
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		last := toRouteDTO(rec)
		if err := repo.Delete(r.Context(), id); err != nil {
			return err
		}
		if err := appendRevision(r.Context(), sc, mc, id, revOpDelete, last); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "notify.route.delete", routeKind, id, nil)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// testResult is the outcome of a route test.
type testResult struct {
	Destination string `json:"destination"`
	Status      string `json:"status"`
	Detail      string `json:"detail,omitempty"`
}

// LookupRoute resolves a route id to its operator-facing name. It is the
// read half of the composition-root seam another module's authoring path uses
// to validate a route reference before storing it: ok=false means the
// route does not exist in this tenant. It reads nothing sensitive — a name.
func (m *Module) LookupRoute(ctx context.Context, tenant model.TenantID, id model.ID) (string, bool, error) {
	if m.data == nil {
		return "", false, errors.New("notify: module has no data handle")
	}
	var name string
	found := false
	if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(routeKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			return err
		}
		name, found = rec.String(colName), true
		return nil
	}); err != nil {
		return "", false, err
	}
	return name, found, nil
}

// RouteFingerprint returns an OPAQUE digest of a route's effect-bearing target —
// the destination a synthetic test would actually go to, plus whether it is
// enabled. It is the read half of the D-06 seam a workflow uses to FREEZE
// the approved route at approval and BLOCK on any change at execution. ok=false
// means the route does not exist. It never returns the raw destination or a
// secret — only the one-way fingerprint (docs/SECURITY-HARDENING.md).
func (m *Module) RouteFingerprint(ctx context.Context, tenant model.TenantID, id model.ID) (string, bool, error) {
	if m.data == nil {
		return "", false, errors.New("notify: module has no data handle")
	}
	var fp string
	found := false
	connOK := false
	if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(routeKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			return err
		}
		fp, connOK = m.routeTargetFingerprint(rec)
		found = true
		return nil
	}); err != nil {
		return "", false, err
	}
	// Item 5c: an unknown/absent LIVE connector for the route's destination is
	// DENY-CLOSED — report ok=false so the caller blocks, never a fingerprint that
	// silently accepts whatever the destination now resolves to.
	return fp, found && connOK, nil
}

// routeTargetFingerprint is the one-way, canonical (length-prefixed) digest of a
// route's effect-bearing target — name, destination NAME, enabled state AND the
// LIVE operator CONNECTOR config behind that destination (its resolved effective
// config, via the dispatcher seam — Flag B), so a connector swap/reconfig or
// a resolved-secret rotation under an unchanged route voids the approval. ok=false
// when the connector is unknown/absent (the caller then DENIES — never a
// match-by-omission). The SAME formula backs RouteFingerprint (freeze) and
// TestBound (atomic verify), so the two can never disagree.
func (m *Module) routeTargetFingerprint(rec model.Record) (string, bool) {
	dest := rec.String(colDestination)
	connFp, ok := m.dispatch.ConnectorFingerprint(dest)
	if !ok {
		return "", false // unknown live connector ⇒ deny-closed
	}
	return canonicalNotifyHash("notify.route.target.v2",
		rec.String(colName), dest, strconv.FormatBool(rec.Bool(colEnabled)), connFp), true
}

// canonicalNotifyHash length-prefixes fields (canonicalization) and hashes.
func canonicalNotifyHash(tag string, fields ...string) string {
	var b strings.Builder
	b.WriteString(tag)
	for _, f := range fields {
		b.WriteString(strconv.Itoa(len(f)))
		b.WriteByte(':')
		b.WriteString(f)
	}
	return hashHex(b.String())
}

// ErrRouteBindingChanged is TestBound's refusal when the route's CURRENT
// fingerprint differs from the one the caller froze at approval (a re-point, or
// the route was deleted). The composition-root adapter maps it to the
// orchestration seam's equivalent so the acting step blocks.
var ErrRouteBindingChanged = errors.New("notify: route changed since it was approved")

// TestBound is the ATOMIC D-06 delivery seam. It reads the route ONCE,
// refuses (ErrRouteBindingChanged) unless the read's fingerprint equals
// expectedFingerprint, and delivers THAT SAME read's destination — so a route
// re-pointed between a caller's check and this send can never divert delivery
// (the flaw in RunRouteTest's independent re-read). operationID is folded into
// the delivery dedup key so replaying the same governed operation cannot
// double-send.
func (m *Module) TestBound(ctx context.Context, tenant model.TenantID, id model.ID, expectedFingerprint, operationID string) (status, detail string, err error) {
	if m.data == nil {
		return "", "", errors.New("notify: module has no data handle")
	}
	var name, destination, fp string
	found := false
	connOK := false
	if verr := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, e := sc.Ext(routeKind)
		if e != nil {
			return e
		}
		rec, e := repo.Get(ctx, id)
		if e != nil {
			if errors.Is(e, store.ErrNotFound) {
				return nil
			}
			return e
		}
		name, destination = rec.String(colName), rec.String(colDestination)
		fp, connOK = m.routeTargetFingerprint(rec)
		found = true
		return nil
	}); verr != nil {
		return "", "", verr
	}
	// A deleted or re-pointed route, an unknown LIVE connector (item 5c), or a
	// fingerprint that no longer matches what was approved: refuse rather than
	// deliver to whatever the destination now resolves to.
	if !found || !connOK || fp != expectedFingerprint {
		return "", "", ErrRouteBindingChanged
	}

	now := m.clock.Now().Time()
	s := signal{
		eventType: string(event.TypeFindingReported), kind: "notify_test", severity: sdkmodel.SeverityInfo,
		source: Name, subjectKind: "route", subjectRef: name, title: "Test notification from Olivares AI", at: now,
	}
	// Claim-then-send, dedup keyed on the governed OperationID so a replay of the
	// same operation never sends twice.
	p := pending{routeID: id, routeName: name, destination: destination, dedupKey: canonicalNotifyHash("notify.test.dedup.v2", name, operationID)}
	if cerr := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, e := sc.Ext(deliveryKind)
		if e != nil {
			return e
		}
		_, e = repo.Create(ctx, claimRecord(s, p, now))
		return e
	}); cerr != nil {
		return "", "", cerr
	}
	n := m.buildNotification(tenant, s, p)
	status, detail = m.dispatchOne(ctx, tenant, destination, n)
	m.finalizeDelivery(ctx, tenant, s, p, status, detail)
	return status, detail, nil
}

// RunRouteTest sends the synthetic test notification through route id and
// records the attempt in the delivery ledger — the exact claim-then-send path
// of the admin verb. It is the exported seam for the composition root's
// workflow-step adapter: a workflow-driven test rides the SAME
// evidenced pipeline as a human-driven one (tier parity holds — the manual
// verb is admin-tier, a workflow run is admin-tier AND HITL-approved). The
// caller owns its own audit/evidence attribution; this method owns the
// delivery ledger rows.
func (m *Module) RunRouteTest(ctx context.Context, tenant model.TenantID, id model.ID) (destination, status, detail string, err error) {
	if m.data == nil {
		return "", "", "", errors.New("notify: module has no data handle")
	}
	var name string
	if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(routeKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, id)
		if err != nil {
			return err
		}
		name, destination = rec.String(colName), rec.String(colDestination)
		return nil
	}); err != nil {
		return "", "", "", err
	}

	now := m.clock.Now().Time()
	s := signal{
		eventType: string(event.TypeFindingReported), kind: "notify_test", severity: sdkmodel.SeverityInfo,
		source: Name, subjectKind: "route", subjectRef: name, title: "Test notification from Olivares AI", at: now,
	}
	p := pending{routeID: id, routeName: name, destination: destination, dedupKey: hashHex("test|" + name)}
	// Claim-then-send, like the live path: the claim row exists before
	// the external send, so even the manual test cannot send unrecorded.
	if cerr := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(deliveryKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(ctx, claimRecord(s, p, now))
		return err
	}); cerr != nil {
		return "", "", "", cerr
	}
	n := m.buildNotification(tenant, s, p)
	status, detail = m.dispatchOne(ctx, tenant, destination, n)
	m.finalizeDelivery(ctx, tenant, s, p, status, detail)
	return destination, status, detail, nil
}

// handleTestRoute sends a synthetic test notification through a route's destination
// and records the attempt in the ledger. Admin-tier, self-audited — it actuates an
// outbound integration, so it is a privileged action. The dispatch happens outside
// any store transaction (network I/O).
func (m *Module) handleTestRoute(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := chiID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	destination, status, detail, err := m.RunRouteTest(r.Context(), mc.Tenant, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if aerr := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		return auditEvent(r.Context(), sc, mc, "notify.route.test", routeKind, id, map[string]any{
			"destination": destination, "status": status,
		})
	}); aerr != nil {
		m.debugf("notify: route-test audit failed", "err", aerr)
	}
	writeJSON(w, http.StatusOK, testResult{Destination: destination, Status: status, Detail: detail})
}
