// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"
	"sync"

	claudewif "github.com/olivaresai/olivares/connectors/claude-wif"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
)

// wifGraphAdapter implements governance.WifGraphProvider (E) over the configured
// claude-wif identity connectors. When a tenant's source carries an org:admin OAuth token
// the adapter serves the LIVE federation config reconciled against the operator-declared
// rules (declared|live|both provenance + drift); without it, it serves the declared
// graph. It is populated by wireRoster as it Opens the providers; the identity console
// reads it per request. With no claude-wif source configured for a tenant it returns
// ok=false → the console serves an honest empty graph (never a fabricated one).
type wifGraphAdapter struct {
	mu       sync.RWMutex
	byTenant map[model.TenantID]*claudewif.Source
	log      *slog.Logger
}

// compile-time proof the adapter satisfies the governance seam.
var _ governance.WifGraphProvider = (*wifGraphAdapter)(nil)

// newWifGraphAdapter returns an empty adapter (populated during roster wiring).
func newWifGraphAdapter(log *slog.Logger) *wifGraphAdapter {
	return &wifGraphAdapter{byTenant: map[model.TenantID]*claudewif.Source{}, log: log}
}

// add records a tenant's Opened claude-wif source (its federation is parsed). A later
// add for the same tenant replaces the prior source (the most recent config wins).
func (a *wifGraphAdapter) add(tenant model.TenantID, src *claudewif.Source) {
	if src == nil || tenant.IsZero() {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.byTenant[tenant] = src
}

// WifGraph returns the WIF object graph for a tenant, reconciled against the live WIF
// Admin API when an org:admin OAuth token is configured (else the declared graph), or
// ok=false when no claude-wif source is configured for it. A live-reconciliation failure
// is logged but never hides the graph: ReconciledWIFGraph returns the declared baseline
// with an honest Reconciliation.Unavailable status, so the console degrades visibly
// rather than serving a fabricated "all clear".
func (a *wifGraphAdapter) WifGraph(ctx context.Context, tenant model.TenantID) (claudewif.WIFGraph, bool) {
	a.mu.RLock()
	src := a.byTenant[tenant]
	a.mu.RUnlock()
	if src == nil {
		return claudewif.WIFGraph{}, false
	}
	g, err := src.ReconciledWIFGraph(ctx)
	if err != nil && a.log != nil {
		a.log.WarnContext(ctx, "wif: live reconciliation unavailable; serving declared baseline",
			"tenant", tenant, "error", err)
	}
	return g, true
}

// FederationExchangeParams returns the DECLARED federation exchange targets for a tenant's
// claude-wif source (rule/org/service-account/workspace ids), or ok=false when no claude-wif
// source is configured for it. It is the no-network declared baseline the in-process WIF
// credential broker mints under — never the live-reconciled set. It reveals no secret
// (ExchangeParams carries only non-secret ids).
func (a *wifGraphAdapter) FederationExchangeParams(tenant model.TenantID) ([]claudewif.ExchangeParams, bool) {
	a.mu.RLock()
	src := a.byTenant[tenant]
	a.mu.RUnlock()
	if src == nil {
		return nil, false
	}
	return src.FederationExchangeParams(), true
}
