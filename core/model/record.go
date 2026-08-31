// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

// SQLKind is a portable column type. The store maps it to a concrete column
// type per engine (TEXT/INTEGER/BOOLEAN/…), so descriptors and codecs never
// name an engine-specific type. Only kinds in this set are allowed in a
// descriptor — that is what lets the same descriptor generate both dialects
// plus the row-level-security and immutability guards.
type SQLKind int

// The portable column kinds.
const (
	// KindText is variable-length unicode text.
	KindText SQLKind = iota
	// KindInt is a 64-bit signed integer.
	KindInt
	// KindFloat is a 64-bit IEEE float (REAL/DOUBLE PRECISION). Never used for
	// money (which is integer micro-units); fine for scores, ratios, latencies.
	KindFloat
	// KindBool is a boolean (INTEGER 0/1 on SQLite, BOOLEAN on Postgres).
	KindBool
	// KindJSON is a JSON document stored as text on both engines (minimal-data:
	// little JSON, deterministic bytes for hashing, no jsonb codepath split).
	KindJSON
	// KindTimestamp is a canonical RFC3339 UTC timestamp stored as text.
	KindTimestamp
	// KindUUID is a UUID stored as canonical text on both engines.
	KindUUID
	// KindBytes is an opaque binary blob (e.g. a hash).
	KindBytes
)

// Valid reports whether k is a known portable kind.
func (k SQLKind) Valid() bool { return k >= KindText && k <= KindBytes }

// String returns a stable name for the kind, used in errors and golden tests.
func (k SQLKind) String() string {
	switch k {
	case KindText:
		return "text"
	case KindInt:
		return "int"
	case KindFloat:
		return "float"
	case KindBool:
		return "bool"
	case KindJSON:
		return "json"
	case KindTimestamp:
		return "timestamp"
	case KindUUID:
		return "uuid"
	case KindBytes:
		return "bytes"
	default:
		return "invalid"
	}
}

// Record is one row as a column-name → normalized-value map. The store scans
// every column into one of a small, engine-independent set of Go types so that
// codecs see the same values regardless of backend:
//
//	KindText/KindJSON/KindTimestamp/KindUUID → string
//	KindInt                                  → int64
//	KindFloat                                → float64
//	KindBool                                 → bool
//	KindBytes                                → []byte
//	any NULL column                          → nil (absent or stored as nil)
//
// A module's GenericRepo exchanges values as Record; a core entity's typed
// Codec maps its struct to and from a Record. There is exactly one such mapping
// per entity and it is hand-written (no runtime reflection on the hot path).
type Record map[string]any

// Set stores a normalized value for a column.
func (r Record) Set(col string, v any) { r[col] = v }

// IsNull reports whether the column is absent or nil.
func (r Record) IsNull(col string) bool {
	v, ok := r[col]
	return !ok || v == nil
}

// String returns the column as a string, or "" if absent/null/other type.
func (r Record) String(col string) string {
	if s, ok := r[col].(string); ok {
		return s
	}
	return ""
}

// Int returns the column as an int64, or 0 if absent/null/other type.
func (r Record) Int(col string) int64 {
	if i, ok := r[col].(int64); ok {
		return i
	}
	return 0
}

// Float returns the column as a float64, or 0 if absent/null/other type.
func (r Record) Float(col string) float64 {
	if f, ok := r[col].(float64); ok {
		return f
	}
	return 0
}

// Bool returns the column as a bool, or false if absent/null/other type.
func (r Record) Bool(col string) bool {
	if b, ok := r[col].(bool); ok {
		return b
	}
	return false
}

// Bytes returns the column as a byte slice, or nil if absent/null/other type.
func (r Record) Bytes(col string) []byte {
	if b, ok := r[col].([]byte); ok {
		return b
	}
	return nil
}
