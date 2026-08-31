// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package sqlstore implements the store contract over database/sql for both the
// SQLite and Postgres backends. It is internal: a module never imports it, it
// only ever receives a store.Scope. Everything tenant-sensitive funnels through
// the descriptor-driven generic repository here, so there is one place to get
// isolation right.
package sqlstore

import (
	"database/sql"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
)

// baseToRecord writes the engine-managed base columns of b into rec. Timestamps
// are written in their canonical text form; deleted_at is included only for
// soft-deletable entities (nil while live).
func baseToRecord(rec model.Record, b model.BaseFields, softDelete bool) {
	rec.Set(model.ColID, b.ID.String())
	rec.Set(model.ColTenantID, b.TenantID.String())
	rec.Set(model.ColCreatedAt, b.CreatedAt.String())
	rec.Set(model.ColUpdatedAt, b.UpdatedAt.String())
	rec.Set(model.ColVersion, b.Version)
	if softDelete {
		if b.DeletedAt != nil {
			rec.Set(model.ColDeletedAt, b.DeletedAt.String())
		} else {
			rec.Set(model.ColDeletedAt, nil)
		}
	}
}

// baseFromRecord parses the base columns out of a full row record.
func baseFromRecord(rec model.Record) (model.BaseFields, error) {
	var b model.BaseFields
	b.ID = model.ID(rec.String(model.ColID))
	b.TenantID = model.TenantID(rec.String(model.ColTenantID))
	created, err := model.ParseTimestamp(rec.String(model.ColCreatedAt))
	if err != nil {
		return b, fmt.Errorf("created_at: %w", err)
	}
	b.CreatedAt = created
	updated, err := model.ParseTimestamp(rec.String(model.ColUpdatedAt))
	if err != nil {
		return b, fmt.Errorf("updated_at: %w", err)
	}
	b.UpdatedAt = updated
	b.Version = rec.Int(model.ColVersion)
	if !rec.IsNull(model.ColDeletedAt) {
		del, err := model.ParseTimestamp(rec.String(model.ColDeletedAt))
		if err != nil {
			return b, fmt.Errorf("deleted_at: %w", err)
		}
		b.DeletedAt = &del
	}
	return b, nil
}

// scanState holds the typed scan destinations for one row plus the columns they
// map to, so a scanned row can be assembled into a normalized Record.
type scanState struct {
	cols  []string
	dests []any
	kinds []model.SQLKind
}

// newScanState builds typed scan destinations for the given columns of a
// descriptor. Nullable columns use sql.Null* wrappers (or *[]byte for bytes);
// non-null columns use plain pointers. This normalizes the driver-native types
// of both backends into the Record's small value set.
func newScanState(desc model.EntityDescriptor, cols []string) (*scanState, error) {
	s := &scanState{cols: cols, dests: make([]any, len(cols)), kinds: make([]model.SQLKind, len(cols))}
	for i, c := range cols {
		k, ok := desc.KindOfColumn(c)
		if !ok {
			return nil, fmt.Errorf("unknown column %q for %s", c, desc.Kind)
		}
		s.kinds[i] = k
		nullable := desc.NullableColumn(c)
		switch k {
		case model.KindInt:
			if nullable {
				s.dests[i] = new(sql.NullInt64)
			} else {
				s.dests[i] = new(int64)
			}
		case model.KindFloat:
			if nullable {
				s.dests[i] = new(sql.NullFloat64)
			} else {
				s.dests[i] = new(float64)
			}
		case model.KindBool:
			if nullable {
				s.dests[i] = new(sql.NullBool)
			} else {
				s.dests[i] = new(bool)
			}
		case model.KindBytes:
			s.dests[i] = new([]byte) // NULL scans to nil naturally
		default: // text/json/timestamp/uuid
			if nullable {
				s.dests[i] = new(sql.NullString)
			} else {
				s.dests[i] = new(string)
			}
		}
	}
	return s, nil
}

// record assembles the scanned destinations into a normalized Record. NULLs
// become nil entries.
func (s *scanState) record() model.Record {
	rec := make(model.Record, len(s.cols))
	for i, c := range s.cols {
		switch d := s.dests[i].(type) {
		case *int64:
			rec[c] = *d
		case *sql.NullInt64:
			if d.Valid {
				rec[c] = d.Int64
			} else {
				rec[c] = nil
			}
		case *float64:
			rec[c] = *d
		case *sql.NullFloat64:
			if d.Valid {
				rec[c] = d.Float64
			} else {
				rec[c] = nil
			}
		case *bool:
			rec[c] = *d
		case *sql.NullBool:
			if d.Valid {
				rec[c] = d.Bool
			} else {
				rec[c] = nil
			}
		case *[]byte:
			if *d == nil {
				rec[c] = nil
			} else {
				rec[c] = *d
			}
		case *string:
			rec[c] = *d
		case *sql.NullString:
			if d.Valid {
				rec[c] = d.String
			} else {
				rec[c] = nil
			}
		}
	}
	return rec
}
