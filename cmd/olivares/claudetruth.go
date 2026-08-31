// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"

	claudemanagedagents "github.com/olivaresai/olivares/connectors/claude-managed-agents"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/sdk"
)

// the live ThreadEventProvider for the claude-agents console. It is the
// claude-api pattern — the runtime-owned Gather instance never serves module
// routes, so the composition root constructs a DEDICATED request-time reader per
// tenant from the SAME claude-managed-agents source entries the operator already
// declared in OLIVARES_SOURCES_CONFIG (no second credential, no new config file).
//
// Honesty contract (governance.ThreadEventProvider): a tenant with no configured
// CMA source answers ok=false (the console serves its honest empty — no live
// source is wired); a WIRED reader that cannot answer returns the error (the
// console reports 502 — an upstream outage must never read as "no events").
type claudeThreadEventProvider struct {
	srcCfg  sourcesConfig
	readers map[model.TenantID]*claudemanagedagents.ThreadEventReader
	log     *slog.Logger
}

var _ governance.ThreadEventProvider = (*claudeThreadEventProvider)(nil)

// newClaudeThreadEventProvider records the configured sources; it builds the
// per-tenant readers later, in populate, because a reader's config may
// reference a secret (`store:<name>`) that can only resolve once the store exists.
// It is always non-nil: until populate runs (and for a tenant with no usable CMA
// source after it) ThreadEvents serves the honest empty.
func newClaudeThreadEventProvider(srcCfg sourcesConfig, log *slog.Logger) *claudeThreadEventProvider {
	return &claudeThreadEventProvider{
		srcCfg:  srcCfg,
		readers: make(map[model.TenantID]*claudemanagedagents.ThreadEventReader),
		log:     log,
	}
}

// populate builds the per-tenant readers from the configured claude-managed-agents
// sources, resolving each source's secret references (the api_key) to live values.
// A source without an api_key (a webhook-only source) cannot read and is skipped —
// the console keeps its honest empty for that tenant. It runs in boot before
// rt.Start, single-threaded, so the readers map needs no lock.
func (p *claudeThreadEventProvider) populate(ctx context.Context, r *secret.Resolver, log *slog.Logger) {
	desc := claudemanagedagents.New().Descriptor()
	for _, spec := range p.srcCfg.Sources {
		if spec.Kind != "claude-managed-agents" || spec.Plugin != nil {
			continue
		}
		tenant, present, terr := parseBusinessTenant("claude-managed-agents source: tenant", spec.Tenant)
		if terr != nil || !present {
			continue // wireSources already rejects/warns on a bad tenant
		}
		if _, dup := p.readers[tenant]; dup {
			log.Warn("claude-agents: multiple claude-managed-agents sources for one tenant; thread events read through the first", "name", spec.Name)
			continue
		}
		resolved, rerr := resolveConfig(ctx, r, desc, sdk.Config{Settings: spec.Config})
		if rerr != nil {
			log.Warn("claude-agents: managed-agents source secret reference could not be resolved; console serves honest empty for its tenant", "name", spec.Name)
			continue
		}
		reader, err := claudemanagedagents.NewThreadEventReader(resolved)
		if err != nil {
			// Typically a webhook-only source (no api_key): the console keeps its
			// honest empty for this tenant. Never log the error (it could embed
			// configured endpoints), only the fact.
			log.Info("claude-agents: managed-agents source not usable as a thread-event reader; console serves honest empty for its tenant", "name", spec.Name)
			continue
		}
		p.readers[tenant] = reader
	}
}

// ThreadEvents maps the connector's structural events to the console's DTO.
func (p *claudeThreadEventProvider) ThreadEvents(ctx context.Context, tenant model.TenantID, sessionID string) ([]governance.ThreadEvent, bool, error) {
	reader := p.readers[tenant]
	if reader == nil {
		return nil, false, nil // no live source for this tenant — honest empty
	}
	evs, err := reader.ThreadEvents(ctx, sessionID)
	if err != nil {
		return nil, true, err // wired but unanswerable — the console reports 502
	}
	out := make([]governance.ThreadEvent, 0, len(evs))
	for _, ev := range evs {
		out = append(out, governance.ThreadEvent{
			ID:        ev.ID,
			Type:      ev.Type,
			AgentRef:  ev.AgentRef,
			PeerRef:   ev.PeerRef,
			ToolName:  ev.ToolName,
			ToolUseID: ev.ToolUseID,
			CreatedAt: ev.CreatedAt,
		})
	}
	return out, true, nil
}
