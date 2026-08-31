// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inventory

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// findOne returns the first entity matching the AND of filters, or ok=false.
// It is the lookup half of every find-or-create: the engine validates each
// filter column against the entity descriptor (an unknown column is rejected),
// and the value is always bound, never interpolated.
func findOne[T any](ctx context.Context, repo store.ReadRepository[T], filters ...model.Filter) (T, bool, error) {
	var zero T
	list, _, err := repo.List(ctx, model.Query{Filters: filters, Limit: 1})
	if err != nil {
		return zero, false, err
	}
	if len(list) == 0 {
		return zero, false, nil
	}
	return list[0], true, nil
}

// The materialization helpers below are find-or-create by an entity's natural
// key (external id, name, uri…). They are idempotent: a re-delivered observation
// finds the existing row and returns its id, so discovery never duplicates an
// entity. They return only the entity id; the caller records the catalog entry.
//
// A note on the idempotency guarantee: the bus delivers events to one subscriber
// goroutine, and each onEdge/onCost runs as a single Mutate transaction, so
// find-then-create is consistent within a process and a re-delivered observation
// finds the existing row. The core entity tables carry NO unique index on these
// natural keys (only the module's OWN tables — catalog_entry, sessions_live — and
// access_edges do), so there is no DB-level backstop: this is correct for the v1
// single-process / single-writer model, but a second concurrent writer to these
// tables (a future multi-instance deployment) could duplicate an entity. Adding
// tenant_id-leading unique indexes on the core natural keys is the engine's call
//, tracked for when multi-writer discovery lands.

func foSession(ctx context.Context, sc store.Scope, externalID string, at time.Time) (model.ID, error) {
	if s, ok, err := findOne(ctx, sc.Sessions(), eq("external_id", externalID)); err != nil {
		return "", err
	} else if ok {
		return s.ID, nil
	}
	s, err := sc.Sessions().Create(ctx, model.Session{
		ExternalID: externalID,
		State:      model.SessionRunning,
		StartedAt:  model.NewTimestamp(at),
		Metadata:   map[string]any{"discovered_via": "edge"},
	})
	return s.ID, err
}

func foAgent(ctx context.Context, sc store.Scope, externalID string) (model.ID, error) {
	if a, ok, err := findOne(ctx, sc.Agents(), eq("external_id", externalID)); err != nil {
		return "", err
	} else if ok {
		return a.ID, nil
	}
	a, err := sc.Agents().Create(ctx, model.Agent{
		Name:       externalID,
		Kind:       "unknown",
		ExternalID: externalID,
		Status:     model.StatusActive,
	})
	return a.ID, err
}

func foIdentity(ctx context.Context, sc store.Scope, externalID string) (model.ID, error) {
	if i, ok, err := findOne(ctx, sc.Identities(), eq("external_id", externalID)); err != nil {
		return "", err
	} else if ok {
		return i.ID, nil
	}
	i, err := sc.Identities().Create(ctx, model.Identity{
		Name:       externalID,
		Kind:       "unknown",
		ExternalID: externalID,
	})
	return i.ID, err
}

func foMCPServer(ctx context.Context, sc store.Scope, name string) (model.ID, error) {
	if s, ok, err := findOne(ctx, sc.MCPServers(), eq("name", name)); err != nil {
		return "", err
	} else if ok {
		return s.ID, nil
	}
	s, err := sc.MCPServers().Create(ctx, model.MCPServer{
		Name:      name,
		Transport: "unknown",
		Status:    model.StatusActive,
	})
	return s.ID, err
}

// foTool find-or-creates a tool by (name, server). hint, when non-nil, sets the
// readOnlyHint on creation (from an mcp.tool capability edge). The hint is
// UNTRUSTED and recorded for corroboration, never relied upon.
func foTool(ctx context.Context, sc store.Scope, name string, serverID model.ID, hint *bool) (model.ID, error) {
	filters := []model.Filter{eq("name", name)}
	if !serverID.IsZero() {
		filters = append(filters, eq("mcp_server_id", serverID.String()))
	}
	if t, ok, err := findOne(ctx, sc.Tools(), filters...); err != nil {
		return "", err
	} else if ok {
		return t.ID, nil
	}
	tool := model.Tool{Name: name, MCPServerID: serverID}
	if hint != nil {
		tool.ReadOnlyHint = *hint
	}
	t, err := sc.Tools().Create(ctx, tool)
	return t.ID, err
}

// foResource find-or-creates a resource by (kind, uri), or by (kind, name) when
// the uri is empty (e.g. web.search, whose query is never stored).
func foResource(ctx context.Context, sc store.Scope, kind, uri, name string) (model.ID, error) {
	var key model.Filter
	if uri != "" {
		key = eq("uri", uri)
	} else {
		key = eq("name", name)
	}
	if r, ok, err := findOne(ctx, sc.Resources(), eq("kind", kind), key); err != nil {
		return "", err
	} else if ok {
		return r.ID, nil
	}
	r, err := sc.Resources().Create(ctx, model.Resource{Name: name, Kind: kind, URI: uri})
	return r.ID, err
}

func foSkill(ctx context.Context, sc store.Scope, name, source string, serverID model.ID) (model.ID, error) {
	if s, ok, err := findOne(ctx, sc.Skills(), eq("name", name), eq("source", source)); err != nil {
		return "", err
	} else if ok {
		return s.ID, nil
	}
	s, err := sc.Skills().Create(ctx, model.Skill{
		Name:        name,
		Source:      source,
		MCPServerID: serverID,
		Status:      model.StatusActive,
	})
	return s.ID, err
}

func foProvider(ctx context.Context, sc store.Scope, name string) (model.ID, error) {
	if p, ok, err := findOne(ctx, sc.Providers(), eq("name", name)); err != nil {
		return "", err
	} else if ok {
		return p.ID, nil
	}
	p, err := sc.Providers().Create(ctx, model.Provider{
		Name:   name,
		Kind:   name,
		Status: model.StatusActive,
	})
	return p.ID, err
}

// foModel find-or-creates a model by (name, provider). Pricing/context-window
// are left zero: this is discovery, not model management — FinOps enriches
// the pricing fields. A bare discovered model still belongs in the catalog.
func foModel(ctx context.Context, sc store.Scope, name string, providerID model.ID) (model.ID, error) {
	if m, ok, err := findOne(ctx, sc.Models(), eq("name", name), eq("provider_id", providerID.String())); err != nil {
		return "", err
	} else if ok {
		return m.ID, nil
	}
	m, err := sc.Models().Create(ctx, model.Model{
		Name:       name,
		ProviderID: providerID,
		Status:     model.StatusActive,
	})
	return m.ID, err
}

// eq is a shorthand for an equality filter.
func eq(col, val string) model.Filter {
	return model.Filter{Column: col, Op: model.OpEq, Value: val}
}
