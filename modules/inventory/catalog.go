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
)

// listCap bounds an internal List page. It matches the store's own maximum; a
// tenant with more rows than this in one sweep is handled across passes.
const listCap = 1000

// upsertCatalogEntry records or refreshes the catalog entry for one materialized
// entity: it bumps last-seen and occurrence, unions the discovering signal
// source (and host, when known), refreshes the denormalized name/ref, and flips
// a previously stale entry back to active because the entity has been seen
// again. It is idempotent — a re-delivered observation finds the existing entry
// and merges — which is what makes discovery safe under at-least-once delivery.
func (m *Module) upsertCatalogEntry(ctx context.Context, sc store.Scope, kind string, id model.ID, name, ref, source, host string, at time.Time) error {
	repo, err := sc.Ext(catalogEntryKind)
	if err != nil {
		return err
	}
	atTS := model.NewTimestamp(at).String()
	existing, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{
			{Column: colEntityKind, Op: model.OpEq, Value: kind},
			{Column: colEntityID, Op: model.OpEq, Value: id.String()},
		},
		Limit: 1,
	})
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		_, err := repo.Create(ctx, model.Record{
			colEntityKind:    kind,
			colEntityID:      id.String(),
			colName:          name,
			colRef:           ref,
			colStatus:        statusActive,
			colSignalSources: marshalSet(addToSet(nil, source)),
			colHosts:         marshalSet(addToSet(nil, host)),
			colFirstSeen:     atTS,
			colLastSeen:      atTS,
			colOccurrence:    int64(1),
		})
		// A redelivered create can race the unique index; treat a conflict as
		// "already exists" and merge on the next observation (idempotent).
		if err != nil && errors.Is(err, store.ErrConflict) {
			return nil
		}
		return err
	}
	rec := existing[0]
	rec[colName] = name
	rec[colRef] = ref
	rec[colStatus] = statusActive
	rec[colSignalSources] = marshalSet(addToSet(parseSet(rec.String(colSignalSources)), source))
	rec[colHosts] = marshalSet(addToSet(parseSet(rec.String(colHosts)), host))
	// Fixed-width canonical timestamps sort lexically, so a string compare is a
	// valid chronological "advance only forward" (core/model/time.go).
	if cur := rec.String(colLastSeen); cur == "" || cur < atTS {
		rec[colLastSeen] = atTS
	}
	rec[colOccurrence] = rec.Int(colOccurrence) + 1
	_, err = repo.Update(ctx, rec)
	return err
}

// sweepLoop runs the staleness sweep on a ticker until stop is closed. The stop
// channel is captured by Start and passed in, so the goroutine never reads the
// mutable m.stop field (which Stop nils under the lock). It uses the system
// clock; the unit-tested entry point is Sweep.
func (m *Module) sweepLoop(stop chan struct{}) {
	defer m.wg.Done()
	ticker := time.NewTicker(m.sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if _, err := m.Sweep(ctx, m.clock.Now().Time()); err != nil {
				m.debugf("inventory: sweep error", "err", err)
			}
			cancel()
		}
	}
}

// Sweep marks every catalog entry that has not been seen since now-staleAfter as
// stale, across every tenant the module has observed. It returns the number of
// entries marked. A gap in observation is itself a signal (docs/SECURITY-HARDENING.md); the
// sweep records it on the catalog without touching the core entities (their
// lifecycle/health is owned elsewhere —), so re-seeing an entity simply
// flips its catalog entry back to active. Sweep is exported so tests can drive
// it deterministically with an injected clock.
func (m *Module) Sweep(ctx context.Context, now time.Time) (int, error) {
	if m.data == nil {
		return 0, nil
	}
	cutoff := model.NewTimestamp(now.Add(-m.staleAfter)).String()
	total := 0
	for _, tenant := range m.tenantsSnapshot() {
		n, err := m.sweepTenant(ctx, tenant, cutoff)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// sweepTenant marks the stale catalog entries of one tenant.
func (m *Module) sweepTenant(ctx context.Context, tenant model.TenantID, cutoff string) (int, error) {
	n := 0
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(catalogEntryKind)
		if err != nil {
			return err
		}
		stale, _, err := repo.List(ctx, model.Query{
			Filters: []model.Filter{
				{Column: colStatus, Op: model.OpEq, Value: statusActive},
				{Column: colLastSeen, Op: model.OpLt, Value: cutoff},
			},
			Limit: listCap,
		})
		if err != nil {
			return err
		}
		for _, rec := range stale {
			rec[colStatus] = statusStale
			if _, err := repo.Update(ctx, rec); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// --- small JSON-set helpers (signal sources / hosts) -------------------------

// parseSet decodes a JSON string array, tolerating empty/invalid input.
func parseSet(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

// addToSet appends v to set if v is non-empty and not already present.
func addToSet(set []string, v string) []string {
	if v == "" {
		return set
	}
	for _, e := range set {
		if e == v {
			return set
		}
	}
	return append(set, v)
}

// marshalSet encodes a string set as a JSON array (always non-nil: "[]" empty).
func marshalSet(set []string) string {
	if set == nil {
		set = []string{}
	}
	b, err := json.Marshal(set)
	if err != nil {
		return "[]"
	}
	return string(b)
}
