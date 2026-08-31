// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"log/slog"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/capabilities"
)

// durable tool pins. The enterprise verifier keeps pins in memory, which
// meant a restart cleared every pin and the next tools/list re-legitimated a
// rug-pull through compatibility TOFU. This file gives pins a tenant-scoped
// table in the engine store (the data belongs to the tenant) behind the
// connector's ToolPinPersistence seam. The community build registers the schema
// (deterministic across editions) but binds no verifier, so nothing writes:
// enforcement remains enterprise-only and the licensing posture is unchanged.

// toolPinKind is the entity kind and is a model.Kind; everything below is a plain
// name. They were one const group, where a single explicit type on the first line
// reads as though it governed the rest (SA9004) — it does not, since each line has
// its own value, so the names were already untyped strings. Splitting the groups
// states that on purpose instead of leaving it to be inferred, and changes no type.
const toolPinKind model.Kind = "mcp.tool_pin"

const (
	toolPinTable = "mcp_tool_pins"

	colTPTool        = "tool"
	colTPFingerprint = "fingerprint"
	colTPPinnedAt    = "pinned_at"
	colTPPinCount    = "pin_count"
	colTPDriftFp     = "drift_fingerprint"
	colTPDriftAt     = "drift_at"
)

// registerToolPinSchema declares the tool-pin entity. Called from the boot's
// composite schema registrar alongside the module fan-out.
func registerToolPinSchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:  toolPinKind,
		Table: toolPinTable,
		Fields: []model.FieldSpec{
			{Name: colTPTool, Kind: model.KindText, Indexed: true},
			{Name: colTPFingerprint, Kind: model.KindText},
			{Name: colTPPinnedAt, Kind: model.KindTimestamp},
			// The pin's UpdatedAt maps to the engine-stamped base updated_at:
			// the row is only ever written when the pin changes, so the two
			// instants are the same event.
			{Name: colTPPinCount, Kind: model.KindInt},
			{Name: colTPDriftFp, Kind: model.KindText, Nullable: true},
			{Name: colTPDriftAt, Kind: model.KindTimestamp, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			Name:    "mcp_tool_pin_uniq",
			Columns: []string{model.ColTenantID, colTPTool},
			Unique:  true,
		}},
	})
}

// toolPinPersistence implements the connector's ToolPinPersistence seam over
// the engine store: one row per (tenant, tool). The verifier's server key IS
// the tenant (the resource server binds it), so Server parses as the TenantID
// and a snapshot can never address another tenant's rows.
type toolPinPersistence struct {
	st  store.Store
	log *slog.Logger
}

var _ mcpc.ToolPinPersistence = (*toolPinPersistence)(nil)

func newToolPinPersistence(st store.Store, log *slog.Logger) *toolPinPersistence {
	return &toolPinPersistence{st: st, log: log}
}

// tpTenant validates and parses the snapshot's server key as a tenant.
func tpTenant(server string) (model.TenantID, error) {
	t, err := model.ParseTenantID(server)
	if err != nil || t.IsZero() || t.IsSystem() {
		return "", fmt.Errorf("tool-pin persistence: server key %q is not a business tenant", server)
	}
	return t, nil
}

// Load returns every stored pin across business tenants (boot-time rebuild).
func (p *toolPinPersistence) Load(ctx context.Context) ([]mcpc.PinSnapshot, error) {
	tenants, err := servedBusinessTenants(ctx, p.st)
	if err != nil {
		return nil, err
	}

	var out []mcpc.PinSnapshot
	for _, tenant := range tenants {
		err := p.st.View(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(toolPinKind)
			if err != nil {
				return err
			}
			cursor := ""
			for {
				recs, page, err := repo.List(ctx, model.Query{Limit: 500, Cursor: cursor})
				if err != nil {
					return err
				}
				for _, rec := range recs {
					out = append(out, recToPinSnapshot(tenant, rec))
				}
				if !page.HasMore || page.Cursor == "" {
					return nil
				}
				cursor = page.Cursor
			}
		})
		if err != nil {
			return nil, fmt.Errorf("tool-pin persistence: load tenant %s: %w", tenant, err)
		}
	}
	return out, nil
}

// UpsertPin stores or updates the snapshot keyed by (Server=tenant, Tool).
//
// It deliberately does NOT compare-and-swap on snap.Version: it CASes on the version it
// just read, i.e. write-through storage, which is what the seam contract asks of it
// ("the verify-path auto-pin treats persistence as availability, not authorization" —
// toolpin.go). The AUTHORIZATION CAS belongs to ApplyPinChange, whose caller supplies an
// operator-read expected_version. Enforcing it here instead would break the TOFU
// auto-pin, which legitimately has no version to supply.
func (p *toolPinPersistence) UpsertPin(ctx context.Context, snap mcpc.PinSnapshot) error {
	tenant, err := tpTenant(snap.Server)
	if err != nil {
		return err
	}
	return p.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(toolPinKind)
		if err != nil {
			return err
		}
		existing, err := tpFind(ctx, repo, snap.Tool)
		if err != nil {
			return err
		}
		rec := model.Record{}
		if existing != nil {
			rec = existing
		}
		rec[colTPTool] = snap.Tool
		rec[colTPFingerprint] = snap.Fingerprint
		rec[colTPPinnedAt] = model.NewTimestamp(snap.PinnedAt).String()
		rec[colTPPinCount] = int64(snap.PinCount)
		if snap.DriftFingerprint != "" {
			rec[colTPDriftFp] = snap.DriftFingerprint
			rec[colTPDriftAt] = model.NewTimestamp(snap.DriftAt).String()
		} else {
			rec[colTPDriftFp] = nil
			rec[colTPDriftAt] = nil
		}
		if existing != nil {
			_, err = repo.Update(ctx, rec)
			return err
		}
		_, err = repo.Create(ctx, rec)
		return err
	})
}

// DeletePin removes the pin row; an absent row is not an error.
func (p *toolPinPersistence) DeletePin(ctx context.Context, server, tool string) error {
	tenant, err := tpTenant(server)
	if err != nil {
		return err
	}
	return p.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(toolPinKind)
		if err != nil {
			return err
		}
		existing, err := tpFind(ctx, repo, tool)
		if err != nil || existing == nil {
			return err
		}
		return repo.Delete(ctx, model.ID(existing.String(model.ColID)))
	})
}

// tpFind returns the row for tool, or nil when absent.
func tpFind(ctx context.Context, repo store.GenericRepo, tool string) (model.Record, error) {
	recs, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{{Column: colTPTool, Op: model.OpEq, Value: tool}},
		Limit:   1,
	})
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, nil
	}
	return recs[0], nil
}

// recToPinSnapshot maps a stored row back to the seam's snapshot.
//
// Version comes from the engine's injected `version` base column, NOT from a declared
// field: model.IsReservedColumn rejects a module descriptor that spells one
// (core/internal/store/sqlstore/registry.go:60-63), and it would be the wrong value
// anyway — genericRepo.Update writes `version = version + 1` under
// `WHERE ... AND version = ?` (generic.go:220-226), so the base column already IS the
// durable compare-and-swap counter of this row. Restoring it at boot is what keeps the
// CAS honest across a restart: a snapshot rebuilt at version 0 would make every stale
// expected_version a client still held accidentally valid.
func recToPinSnapshot(tenant model.TenantID, rec model.Record) mcpc.PinSnapshot {
	snap := mcpc.PinSnapshot{
		Server:           tenant.String(),
		Tool:             rec.String(colTPTool),
		Fingerprint:      rec.String(colTPFingerprint),
		PinCount:         int(rec.Int(colTPPinCount)),
		Version:          rec.Int(model.ColVersion),
		DriftFingerprint: rec.String(colTPDriftFp),
	}
	if ts, err := model.ParseTimestamp(rec.String(colTPPinnedAt)); err == nil {
		snap.PinnedAt = ts.Time()
	}
	if ts, err := model.ParseTimestamp(rec.String(model.ColUpdatedAt)); err == nil {
		snap.UpdatedAt = ts.Time()
	}
	if s := rec.String(colTPDriftAt); s != "" {
		if ts, err := model.ParseTimestamp(s); err == nil {
			snap.DriftAt = ts.Time()
		}
	}
	return snap
}

// capabilitiesOpts wires the pin verifier's optional operator surface
// into the capabilities module. The type assertion keeps the community build
// free of any enterprise type: a nil verifier (or one without the admin
// surface) yields no option and the routes stay honestly 501.
func capabilitiesOpts(verifier mcpc.ToolPinVerifier) []capabilities.Option {
	if admin, ok := verifier.(mcpc.ToolPinAdmin); ok && admin != nil {
		return []capabilities.Option{capabilities.WithToolPinAdmin(admin)}
	}
	return nil
}
