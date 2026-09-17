// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
)

// B2 — the observation scope of a live row, computed by the SERVER.
//
// A live row used to be keyed by the provider's external id alone. Two homes of
// one provider can announce the same id for two different sessions, so the key is
// now (scope, external id), where the scope says which CHANNEL the observation
// came through and is derived from facts the host stamped or the plane itself
// wrote — never from a label in the payload:
//
//	legacy    no stamped registration: a legacy connector, a pushed collector
//	          observation, an old event. scope NULL. Never assigned a profile.
//	observed  a registered source whose binding was approved at host admission for
//	          this observation's exact source id, applied revision and environment.
//	source    a registered source with no approved binding at admission (including
//	          new frames after revocation): known channel, unattributed instance.
//	managed   the plane's own bridge, for a run it launched: the only scope that
//	          carries canonical_sid and run_ref, because the bridge is the only
//	          writer that can prove them.
//
// A cooperative observation that copies a managed run's external id lands in an
// observed/source/legacy row — never in the managed one — so it can never lend a
// run its control or borrow the run's evidence.

// Attribution values as the API reports them.
const (
	attributionLegacy   = "legacy"
	attributionObserved = "observed"
	attributionSource   = "source"
	attributionManaged  = "managed"
)

const (
	scopeObservedPrefix = "observed:"
	scopeSourcePrefix   = "source:"
	scopeManagedPrefix  = "managed:"
)

// liveScope is the resolved attribution of one observation channel.
type liveScope struct {
	scope        string // "" = legacy (stored as NULL)
	profileID    string
	provider     string
	envRef       string
	bindingRef   string
	canonicalSID string
	runRef       string
}

func (s liveScope) legacy() bool { return s.scope == "" }

// attributionOf classifies a stored scope string.
func attributionOf(scope string) string {
	switch {
	case scope == "":
		return attributionLegacy
	case strings.HasPrefix(scope, scopeObservedPrefix):
		return attributionObserved
	case strings.HasPrefix(scope, scopeSourcePrefix):
		return attributionSource
	case strings.HasPrefix(scope, scopeManagedPrefix):
		return attributionManaged
	default:
		return attributionSource // an unknown encoding is a known channel, not a profile
	}
}

// scopeForRegistration reads the binding decision admitted by the host before
// queueing. Empty stays unattributed; revocation never changes an old decision.
func (m *Module) scopeForRegistration(ctx context.Context, sc store.Scope, reg *event.SourceRegistration) (liveScope, error) {
	if reg == nil || !reg.Valid() {
		return liveScope{}, nil
	}
	b, ok, err := historicalBinding(ctx, sc, reg)
	if err != nil {
		return liveScope{}, err
	}
	if ok {
		return liveScope{
			scope: scopeObservedPrefix + b.ProfileRef, profileID: b.ProfileRef, provider: b.Driver,
			envRef: reg.EnvironmentRef, bindingRef: b.Ref,
		}, nil
	}
	// The driver is deliberately NOT part of an unattributed source scope: the host
	// has no honest driver for a source nobody dedicated to a profile, and a
	// connector kind is not one.
	return liveScope{
		scope:  scopeSourcePrefix + reg.SourceID + ":" + strconv.FormatInt(reg.SourceRevision, 10) + ":" + reg.EnvironmentRef,
		envRef: reg.EnvironmentRef,
	}, nil
}

// managedScope is the scope of the plane's own row for a launched run.
func managedScope(profileID, provider, envRef, sid, runRef string) liveScope {
	return liveScope{
		scope: scopeManagedPrefix + sid, profileID: profileID, provider: provider, envRef: envRef,
		canonicalSID: sid, runRef: runRef,
	}
}

// liveKeyFilters selects the ONE row of (scope, external id). NULL is matched as
// NULL: a legacy lookup never sees a scoped row and a scoped lookup never sees the
// legacy one, on both engines.
func liveKeyFilters(ref string, scope string) []model.Filter {
	if scope == "" {
		return []model.Filter{eq(colSessionRef, ref), {Column: colObservationScope, Op: model.OpIsNull}}
	}
	return []model.Filter{eq(colSessionRef, ref), eq(colObservationScope, scope)}
}

// applyScopeColumns stamps a NEW row with its scope. Existing rows keep theirs:
// a scope is part of the row's identity and never rewritten by a later fold.
func applyScopeColumns(rec model.Record, s liveScope) {
	if s.legacy() {
		return
	}
	rec[colObservationScope] = s.scope
	setIf(rec, colLiveProfileID, s.profileID)
	setIf(rec, colLiveProvider, s.provider)
	setIf(rec, colLiveEnvRef, s.envRef)
	setIf(rec, colLiveBindingRef, s.bindingRef)
	setIf(rec, colLiveCanonicalSID, s.canonicalSID)
	setIf(rec, colLiveRunRef, s.runRef)
}

// findManagedLive reads the plane's row for a canonical session (partial unique
// on canonical_sid), if the bridge has written one.
func findManagedLive(ctx context.Context, sc store.Scope, sid string) (model.Record, bool, error) {
	repo, err := sc.Ext(liveKind)
	if err != nil {
		return nil, false, err
	}
	recs, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colLiveCanonicalSID, sid)}, Limit: 1})
	if err != nil || len(recs) == 0 {
		return nil, false, err
	}
	return recs[0], true, nil
}

// upsertManagedLive writes the plane's own live row for a launched run, inside the
// SAME transaction that proved the run owns the announced id (the scoped alias
// bind). It looks the row up by canonical sid first: a validated continuation that
// announces a different external id updates session_ref on the same row rather
// than minting a second identity for one session. It never touches an observed
// or legacy row that happens to share the external id.
func (m *Module) upsertManagedLive(ctx context.Context, sc store.Scope, in managedAliasInput, envRef string, at time.Time) (model.Record, error) {
	repo, err := sc.Ext(liveKind)
	if err != nil {
		return nil, err
	}
	atTS := model.NewTimestamp(at).String()
	if rec, ok, err := findManagedLive(ctx, sc, in.sid); err != nil {
		return nil, err
	} else if ok {
		rec[colSessionRef] = in.externalID
		advanceLast(rec, at)
		return repo.Update(ctx, rec)
	}
	rec := model.Record{
		colSessionRef:   in.externalID,
		colInputTokens:  int64(0),
		colOutputTokens: int64(0),
		colCostMicroUSD: int64(0),
		colEventCount:   int64(0),
		colToolCalls:    int64(0),
		colFirstEventAt: atTS,
		colLastEventAt:  atTS,
		colEngine:       in.provider,
	}
	applyScopeColumns(rec, managedScope(in.profileID, in.provider, envRef, in.sid, in.runRef))
	created, err := repo.Create(ctx, rec)
	if err == nil {
		return created, nil
	}
	if errors.Is(err, store.ErrConflict) {
		// A racing bind of the same sid: converge on the winner.
		again, ok, lerr := findManagedLive(ctx, sc, in.sid)
		if lerr != nil {
			return nil, lerr
		}
		if ok {
			again[colSessionRef] = in.externalID
			advanceLast(again, at)
			return repo.Update(ctx, again)
		}
	}
	return nil, err
}

// touchManagedLive advances the managed row's last_event_at for a live profiled
// run, best-effort and outside any other transaction: liveness of the plane's own
// process is a fact the plane can state without a connector.
func (m *Module) touchManagedLive(ctx context.Context, lr *liveRun, at time.Time) {
	_ = m.withRegisteredProfiledRun(lr, func() error {
		var snap *liveSnapshot
		err := m.mutateProfiledRun(ctx, lr, func(sc store.Scope, run model.Record) error {
			snap = nil
			rec, ok, err := findManagedLive(ctx, sc, lr.claim.SID)
			if err != nil || !ok {
				return err
			}
			repo, err := sc.Ext(liveKind)
			if err != nil {
				return err
			}
			advanceLast(rec, at)
			updated, err := repo.Update(ctx, rec)
			if err != nil {
				return err
			}
			runs, err := sc.Ext(runKind)
			if err != nil {
				return err
			}
			before := run.String(colLastActivityAt)
			run[colLastActivityAt] = model.NewTimestamp(at).String()
			conservaElSelloMasNuevo(run, before)
			if _, err := runs.Update(ctx, run); err != nil {
				return err
			}
			s := m.snapshot(updated, lr.tenant)
			snap = &s
			return nil
		})
		if err == nil && snap != nil {
			m.broker.publish(*snap)
		}
		return err
	})
}

// findLiveByID reads one live row by its opaque id (the public live_ref) within
// the tenant scope. An id from another tenant is simply not found.
func findLiveByID(ctx context.Context, sc store.Scope, id model.ID) (model.Record, error) {
	repo, err := sc.Ext(liveKind)
	if err != nil {
		return nil, err
	}
	return repo.Get(ctx, id)
}

// timelineFiltersFor selects the timeline of ONE live row: a scoped row by the
// exact live_ref its events were written with; a legacy row by external id among
// the events that carry no live_ref. A legacy selector therefore never sees a
// scoped row's events and a scoped selector never sees another home's.
func timelineFiltersFor(rec model.Record) []model.Filter {
	if rec.String(colObservationScope) == "" {
		return []model.Filter{eq(colTLSessionRef, rec.String(colSessionRef)), {Column: colTLLiveRef, Op: model.OpIsNull}}
	}
	return []model.Filter{eq(colTLLiveRef, rec.String(model.ColID))}
}

// legacyTimelineFilters is the bare-external-id timeline: legacy events only.
func legacyTimelineFilters(ref string) []model.Filter {
	return []model.Filter{eq(colTLSessionRef, ref), {Column: colTLLiveRef, Op: model.OpIsNull}}
}

// parseLiveRef bounds the opaque id a caller presents.
func parseLiveRef(s string) (model.ID, bool) {
	id, err := model.ParseID(strings.TrimSpace(s))
	if err != nil || id.IsZero() {
		return "", false
	}
	return id, true
}
