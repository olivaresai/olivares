// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// accessEdgeRepo is the typed AccessEdge repository plus the graph queries that
// make the R/RW map a view over the model (ARCHITECTURE.md). The embedded
// Repository provides CRUD; Neighbors/Drift/Upsert add the differential queries.
type accessEdgeRepo struct {
	store.Repository[model.AccessEdge]
	g  *genericRepo
	sc *tenantScope
}

func newAccessEdgeRepo(g *genericRepo, sc *tenantScope) store.AccessEdgeRepo {
	return &accessEdgeRepo{Repository: newTypedRepo(g, accessEdgeCodec), g: g, sc: sc}
}

// Neighbors returns the edges touching node in the given direction.
func (r *accessEdgeRepo) Neighbors(ctx context.Context, node model.NodeRef, dir model.Direction) ([]model.AccessEdge, error) {
	switch dir {
	case model.Outgoing:
		return r.listBy(ctx, "origin_id", node.ID)
	case model.Incoming:
		return r.listBy(ctx, "resource_id", node.ID)
	case model.Both:
		out, err := r.listBy(ctx, "origin_id", node.ID)
		if err != nil {
			return nil, err
		}
		in, err := r.listBy(ctx, "resource_id", node.ID)
		if err != nil {
			return nil, err
		}
		// A self-loop edge (origin_id == resource_id) matches both queries;
		// merge by edge id so it is returned at most once.
		seen := make(map[model.ID]bool, len(out))
		for _, e := range out {
			seen[e.ID] = true
		}
		for _, e := range in {
			if !seen[e.ID] {
				out = append(out, e)
				seen[e.ID] = true
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("invalid direction %q", dir)
	}
}

// listBy lists edges filtered by one id column, via the tenant-scoped repository.
func (r *accessEdgeRepo) listBy(ctx context.Context, col string, id model.ID) ([]model.AccessEdge, error) {
	edges, _, err := r.List(ctx, model.Query{
		Filters: []model.Filter{{Column: col, Op: model.OpEq, Value: id.String()}},
		Limit:   maxLimit,
	})
	return edges, err
}

// Drift returns least-privilege discrepancies: edges where permitted and
// observed disagree (ARCHITECTURE.md). permitted && !observed is an unused grant;
// observed && !permitted is a violation.
//
// Results are capped at q.Limit (default and maximum maxLimit); a tenant with
// more drifts than the cap sees a truncated slice with no continuation token.
// Full pagination of the drift set belongs to the access-map module (III); for
// Callers should treat a full-cap result as "there may be more".
func (r *accessEdgeRepo) Drift(ctx context.Context, q model.Query) ([]model.PrivilegeDrift, error) {
	cols := accessEdgeDescriptor.AllColumns()
	where := []string{"tenant_id = ?", "permitted <> observed"}
	args := []any{r.g.tenant.String()}
	for _, f := range q.Filters {
		frag, val, err := r.g.filterFragment(f)
		if err != nil {
			return nil, err
		}
		where = append(where, frag)
		args = append(args, val)
	}
	limit := q.Limit
	if limit <= 0 || limit > maxLimit {
		limit = maxLimit
	}
	sqlText := fmt.Sprintf("SELECT %s FROM %s WHERE %s ORDER BY id ASC LIMIT %d",
		strings.Join(cols, ", "), accessEdgeDescriptor.Table, strings.Join(where, " AND "), limit)

	rows, err := r.g.tx.QueryContext(ctx, r.g.dia.Rebind(sqlText), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.PrivilegeDrift
	for rows.Next() {
		st, err := newScanState(accessEdgeDescriptor, cols)
		if err != nil {
			return nil, err
		}
		if err := rows.Scan(st.dests...); err != nil {
			return nil, err
		}
		rec := st.record()
		base, err := baseFromRecord(rec)
		if err != nil {
			return nil, err
		}
		edge, err := accessEdgeCodec.Decode(base, rec)
		if err != nil {
			return nil, err
		}
		kind := model.DriftViolation
		if edge.Permitted && !edge.Observed {
			kind = model.DriftUnusedGrant
		}
		out = append(out, model.PrivilegeDrift{Edge: edge, Kind: kind})
	}
	return out, rows.Err()
}

// Upsert merges an observation into an existing edge by natural key, or inserts
// a new one. It is a monotonic merge — last_seen advances, occurrence_count
// accumulates, observed and permitted are OR'd — and so does not take a version.
func (r *accessEdgeRepo) Upsert(ctx context.Context, e model.AccessEdge) (model.AccessEdge, error) {
	if e.OccurrenceCount <= 0 {
		e.OccurrenceCount = 1
	}
	enc, err := accessEdgeCodec.Encode(e)
	if err != nil {
		return model.AccessEdge{}, err
	}
	now := r.g.clock.Now()
	baseToRecord(enc, model.BaseFields{
		ID: model.NewID(), TenantID: r.g.tenant, CreatedAt: now, UpdatedAt: now, Version: 1,
	}, false)

	cols := accessEdgeDescriptor.AllColumns()
	args := make([]any, len(cols))
	for i, c := range cols {
		args[i] = enc[c]
	}
	// The merge expressions qualify the CURRENT row's columns with the table
	// name: Postgres rejects an unqualified reference on the right-hand side of
	// DO UPDATE SET as ambiguous (target vs excluded, SQLSTATE 42702) — SQLite
	// resolves it silently, which is how this stayed latent until the
	// benchmark first drove the upsert against a real Postgres.
	q := fmt.Sprintf(`INSERT INTO %[1]s (%[2]s) VALUES (%[3]s)
ON CONFLICT (tenant_id, origin_kind, origin_id, resource_id, mode) DO UPDATE SET
  last_seen = excluded.last_seen,
  occurrence_count = %[1]s.occurrence_count + excluded.occurrence_count,
  observed = %[1]s.observed OR excluded.observed,
  permitted = %[1]s.permitted OR excluded.permitted,
  confidence = excluded.confidence,
  signal_source = excluded.signal_source,
  metadata = excluded.metadata,
  updated_at = excluded.updated_at,
  version = %[1]s.version + 1`,
		accessEdgeDescriptor.Table, strings.Join(cols, ", "), placeholders(len(cols)))

	if _, err := r.g.tx.ExecContext(ctx, r.g.dia.Rebind(q), args...); err != nil {
		return model.AccessEdge{}, mapWriteErr(err)
	}
	return r.getByNaturalKey(ctx, e)
}

// getByNaturalKey reads the edge identified by an observation's natural key.
func (r *accessEdgeRepo) getByNaturalKey(ctx context.Context, e model.AccessEdge) (model.AccessEdge, error) {
	cols := accessEdgeDescriptor.AllColumns()
	q := fmt.Sprintf("SELECT %s FROM %s WHERE tenant_id = ? AND origin_kind = ? AND origin_id = ? AND resource_id = ? AND mode = ?",
		strings.Join(cols, ", "), accessEdgeDescriptor.Table)
	st, err := newScanState(accessEdgeDescriptor, cols)
	if err != nil {
		return model.AccessEdge{}, err
	}
	row := r.g.tx.QueryRowContext(ctx, r.g.dia.Rebind(q),
		r.g.tenant.String(), e.OriginKind, e.OriginID.String(), e.ResourceID.String(), string(e.Mode))
	if err := row.Scan(st.dests...); err != nil {
		return model.AccessEdge{}, err
	}
	rec := st.record()
	base, err := baseFromRecord(rec)
	if err != nil {
		return model.AccessEdge{}, err
	}
	return accessEdgeCodec.Decode(base, rec)
}
