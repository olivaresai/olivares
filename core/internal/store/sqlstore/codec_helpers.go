// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"encoding/json"

	"github.com/olivaresai/olivares/core/model"
)

// These helpers map entity field values to and from the normalized Record
// values, keeping the per-entity codecs in catalog.go short and uniform. JSON
// columns store nil for an empty/absent map (NULL); ID and timestamp pointers
// store nil when zero/absent.

// encJSON encodes a metadata map to a canonical JSON string, or nil when empty.
func encJSON(m map[string]any) (any, error) {
	if len(m) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// decJSON decodes a JSON column into a map, or nil when NULL.
func decJSON(rec model.Record, col string) (map[string]any, error) {
	if rec.IsNull(col) {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(rec.String(col)), &m); err != nil {
		return nil, err
	}
	return m, nil
}

// encOptID encodes an optional foreign-key id, storing nil when zero.
func encOptID(id model.ID) any {
	if id.IsZero() {
		return nil
	}
	return id.String()
}

// decID reads an id column (returns the zero id when NULL).
func decID(rec model.Record, col string) model.ID {
	return model.ID(rec.String(col))
}

// encTS encodes a required timestamp to its canonical text.
func encTS(ts model.Timestamp) any { return ts.String() }

// decTS reads a required timestamp column.
func decTS(rec model.Record, col string) (model.Timestamp, error) {
	return model.ParseTimestamp(rec.String(col))
}

// encOptTS encodes an optional timestamp, storing nil when absent.
func encOptTS(ts *model.Timestamp) any {
	if ts == nil {
		return nil
	}
	return ts.String()
}

// decOptTS reads an optional timestamp column, returning nil when NULL.
func decOptTS(rec model.Record, col string) (*model.Timestamp, error) {
	if rec.IsNull(col) {
		return nil, nil
	}
	t, err := model.ParseTimestamp(rec.String(col))
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// encBytes encodes an optional byte slice, storing nil when empty.
func encBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// encOptInt encodes an optional integer, storing nil when zero (so a nullable
// reconciled column reads identically on fresh and legacy rows).
func encOptInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// encOptStr encodes an optional text value, storing nil when empty so a nullable
// reconciled column (e.g. a resource's materialized path) reads identically on
// fresh and legacy rows. decode reads it back with Record.String, which returns
// "" for NULL.
func encOptStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// encStrings encodes a string slice as a JSON array, or nil when empty.
func encStrings(ss []string) (any, error) {
	if len(ss) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(ss)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// decStrings decodes a JSON-array column into a string slice, or nil when NULL.
func decStrings(rec model.Record, col string) ([]string, error) {
	if rec.IsNull(col) {
		return nil, nil
	}
	var ss []string
	if err := json.Unmarshal([]byte(rec.String(col)), &ss); err != nil {
		return nil, err
	}
	return ss, nil
}

// encBools encodes a named boolean vector as a JSON object, or nil when empty.
func encBools(values map[string]bool) (any, error) {
	if len(values) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// decBools decodes a JSON-object boolean vector, or nil when NULL.
func decBools(rec model.Record, col string) (map[string]bool, error) {
	if rec.IsNull(col) {
		return nil, nil
	}
	var values map[string]bool
	if err := json.Unmarshal([]byte(rec.String(col)), &values); err != nil {
		return nil, err
	}
	return values, nil
}

// field is a small helper to declare a FieldSpec.
func field(name string, k model.SQLKind, nullable bool) model.FieldSpec {
	return model.FieldSpec{Name: name, Kind: k, Nullable: nullable}
}

// indexedField declares a FieldSpec with a secondary index.
func indexedField(name string, k model.SQLKind, nullable bool) model.FieldSpec {
	return model.FieldSpec{Name: name, Kind: k, Nullable: nullable, Indexed: true}
}
