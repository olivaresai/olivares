// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package siemforward

import (
	"context"
	"errors"
	"log/slog"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/eventing"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the module's globally unique identifier.
const Name = "olivares.siemforward"

// Namespace is the module's store and API namespace.
const Namespace = "siemforward"

// auditSource is the eventing source stamped on forwarded ledger records; a ledger
// sink subscription matches it (or matches any source with an empty filter).
const auditSource = "olivares.audit"

// forwardBatch bounds one ForwardDue pass: a single pass walks at most this many
// ledger records past the cursor, so a large backlog drains over several passes
// without an unbounded read.
const forwardBatch = 256

// errStopWalk stops the audit Walk once a batch is collected (the closed Walk API
// has no limit parameter).
var errStopWalk = errors.New("siemforward: batch full")

// Entity: the per-tenant forward cursor — the highest ledger Seq already enqueued
// for SIEM forwarding. It is the at-least-once anchor: a crash or restart resumes
// the walk from this seq, and IngestAudit dedups any record re-walked.
const (
	cursorKind          model.Kind = "siemforward.cursor"
	cursorTable                    = "siemforward_cursor"
	colCurLastForwarded            = "last_forwarded_seq"
)

// Module is the SIEM-forward driver. It owns the per-tenant ledger-forward
// cursor and drives ForwardDue (the leader-gated pump calls it); it holds the
// eventing module so it can hand each sealed ledger record to the durable engine via
// IngestAudit. It registers no API routes — forwarding is internal, not a tenant
// self-service surface (a tenant configures WHERE the ledger goes through an
// eventing audit.recorded sink subscription).
type Module struct {
	log  *slog.Logger
	data api.ModuleData
	evt  *eventing.Module
}

// Compile-time proofs.
var (
	_ sdk.Module       = (*Module)(nil)
	_ api.Module       = (*Module)(nil)
	_ api.DataConsumer = (*Module)(nil)
)

// New returns the SIEM-forward module bound to the eventing engine it feeds.
func New(evt *eventing.Module) *Module { return &Module{evt: evt} }

// Descriptor returns the module's self-description.
func (m *Module) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeModule,
		Title:       "SIEM ledger forwarder",
		Description: "Walks the tamper-evident audit ledger from a per-tenant cursor and forwards each sealed record to SIEM control towers over the eventing platform (durable retries/replay/DLQ). Provides the SinkRenderer that re-shapes ledger and findings events into OCSF 1.8/CEF/LEEF and the sink envelope.",
	}
}

// UseData receives the tenant-parameterized data handle before Start.
func (m *Module) UseData(d api.ModuleData) { m.data = d }

// Init keeps the host logger; it subscribes to nothing (the ledger is not on the
// bus — it is walked) and must not block.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	return nil
}

// Start warns if the engine is unwired (no forwarding can happen), then returns —
// the actual work is driven by the composition-root pump (leader-gated).
func (m *Module) Start(context.Context) error {
	if m.log != nil && m.evt == nil {
		m.log.Warn("siemforward: no eventing engine wired; the ledger will not be forwarded")
	}
	return nil
}

// Stop is a no-op (no owned goroutines).
func (m *Module) Stop(context.Context) error { return nil }

// APINamespace roots the (empty) route set and the store namespace.
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares none: the module exposes no routes.
func (m *Module) Permissions() []auth.Permission { return nil }

// APIRoutes mounts nothing: forwarding is pump-driven, not a tenant API surface.
func (m *Module) APIRoutes(api.RouteRegistrar) {}

// RegisterSchema declares the per-tenant forward cursor.
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:  cursorKind,
		Table: cursorTable,
		Fields: []model.FieldSpec{
			{Name: colCurLastForwarded, Kind: model.KindInt},
		},
		Indexes: []model.IndexSpec{{
			Name:    "siemforward_cursor_uniq",
			Columns: []string{model.ColTenantID},
			Unique:  true,
		}},
	})
}

// ForwardDue runs ONE bounded forward pass for a tenant: read the forward cursor,
// walk the ledger from the next seq, and hand each sealed record to the durable
// engine (IngestAudit). The cursor advances only to the last record successfully
// enqueued, so a mid-pass failure is resumed (and re-enqueued idempotently) next
// pass — at-least-once from the tamper-evident ledger, the authoritative source.
// It is exported for the leader-gated composition-root pump; it must run
// single-writer per tenant (the pump is leader-gated, like the eventing pump).
func (m *Module) ForwardDue(ctx context.Context, tenant model.TenantID) (int, error) {
	if m.data == nil || m.evt == nil {
		return 0, nil
	}
	var fromSeq int64
	var batch []model.AuditEvent
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		var rerr error
		fromSeq, rerr = m.readCursor(ctx, sc)
		if rerr != nil {
			return rerr
		}
		batch = batch[:0]
		werr := sc.Audit().Walk(ctx, fromSeq+1, func(ev model.AuditEvent) error {
			batch = append(batch, ev)
			if len(batch) >= forwardBatch {
				return errStopWalk
			}
			return nil
		})
		if werr != nil && !errors.Is(werr, errStopWalk) {
			return werr
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	forwarded := 0
	last := fromSeq
	for _, ev := range batch {
		if err := ctx.Err(); err != nil {
			break
		}
		if ferr := m.Forward(ctx, ev); ferr != nil {
			if m.log != nil {
				m.log.Debug("siemforward: forward failed; will resume from cursor", "tenant", tenant.String(), "seq", ev.Seq, "err", ferr)
			}
			break // do not advance past a failed record; next pass resumes here
		}
		last = ev.Seq
		forwarded++
	}

	if last > fromSeq {
		if werr := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
			return m.writeCursor(ctx, sc, last)
		}); werr != nil {
			return forwarded, werr
		}
	}
	return forwarded, nil
}

// readCursor returns the tenant's last-forwarded ledger seq (0 if none yet).
func (m *Module) readCursor(ctx context.Context, sc store.Scope) (int64, error) {
	repo, err := sc.Ext(cursorKind)
	if err != nil {
		return 0, err
	}
	recs, _, err := repo.List(ctx, model.Query{Limit: 1})
	if err != nil {
		return 0, err
	}
	if len(recs) == 0 {
		return 0, nil
	}
	return recs[0].Int(colCurLastForwarded), nil
}

// writeCursor advances the tenant's forward cursor to seq (creating the row on
// first use). Single-writer by the leader gate, so an optimistic version conflict
// is not expected; a concurrent pass would simply re-walk idempotently.
func (m *Module) writeCursor(ctx context.Context, sc store.Scope, seq int64) error {
	repo, err := sc.Ext(cursorKind)
	if err != nil {
		return err
	}
	recs, _, err := repo.List(ctx, model.Query{Limit: 1})
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		_, err = repo.Create(ctx, model.Record{colCurLastForwarded: seq})
		return err
	}
	rec := recs[0]
	if rec.Int(colCurLastForwarded) >= seq {
		return nil // never regress
	}
	rec[colCurLastForwarded] = seq
	_, err = repo.Update(ctx, rec)
	return err
}
