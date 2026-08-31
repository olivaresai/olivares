// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package databricksuc

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// Resource kinds emitted by this connector.
const (
	kindTable  = "databricks.table"  // a Unity Catalog table (catalog.schema.table)
	kindColumn = "databricks.column" // a Unity Catalog column (catalog.schema.table.column)
)

// lineageRow is the subset of a Databricks Unity Catalog lineage system-table
// row this connector reads (system.access.table_lineage and
// system.access.column_lineage, exported as one JSON object per line). Only the
// fields the edge needs are declared; the columns the connector deliberately
// does NOT read (statement_id, entity_id, record_id, paths, catalog/schema
// split-outs, entity_metadata, …) are omitted — there is no field for any
// statement body or payload (minimal data, docs/SECURITY-HARDENING.md).
//
// Field names are verbatim from the lineage system-table schema
// (https://docs.databricks.com/aws/en/admin/system-tables/lineage):
//
//	source_table_full_name, source_column_name (column_lineage only),
//	target_table_full_name, target_column_name (column_lineage only),
//	created_by, event_time.
//
// A row is a read edge for its source side (created_by reads the source) and a
// write edge for its target side (created_by writes the target); a row may carry
// either, both, or neither side. The column_*_name fields distinguish a
// column_lineage row from a table_lineage row.
type lineageRow struct {
	SourceTableFullName string `json:"source_table_full_name"`
	SourceColumnName    string `json:"source_column_name"`
	TargetTableFullName string `json:"target_table_full_name"`
	TargetColumnName    string `json:"target_column_name"`
	CreatedBy           string `json:"created_by"`
	EventTime           string `json:"event_time"`
}

// parseRow unmarshals one NDJSON line into a lineageRow. It returns ok=false for
// a line that is not valid JSON.
func parseRow(line []byte) (lineageRow, bool) {
	var r lineageRow
	if err := json.Unmarshal(line, &r); err != nil {
		return lineageRow{}, false
	}
	return r, true
}

// sourceRef returns the resource kind and reference for the source side of a
// lineage row (the side created_by READ). ok=false if the row has no source
// table. A non-empty source column makes it a column resource; the column
// reference is the table full name plus the column ("catalog.schema.table.col").
func (r lineageRow) sourceRef() (kind, ref string, ok bool) {
	tbl := strings.TrimSpace(r.SourceTableFullName)
	if tbl == "" {
		return "", "", false
	}
	if col := strings.TrimSpace(r.SourceColumnName); col != "" {
		return kindColumn, tbl + "." + col, true
	}
	return kindTable, tbl, true
}

// targetRef returns the resource kind and reference for the target side of a
// lineage row (the side created_by WROTE). ok=false if the row has no target
// table.
func (r lineageRow) targetRef() (kind, ref string, ok bool) {
	tbl := strings.TrimSpace(r.TargetTableFullName)
	if tbl == "" {
		return "", "", false
	}
	if col := strings.TrimSpace(r.TargetColumnName); col != "" {
		return kindColumn, tbl + "." + col, true
	}
	return kindTable, tbl, true
}

// dbxTimeLayouts are the timestamp formats Databricks emits for event_time. The
// platform records the UTC offset at the end ("+00:00"), which is RFC3339; the
// space-separated variant covers a JSON export that serializes the TIMESTAMP
// with a space instead of the 'T'. All are parsed in UTC and normalized to UTC.
var dbxTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05Z07:00",
}

// parseTime parses a Databricks lineage event_time and normalizes it to UTC,
// returning ok=false if no layout matches. event_time is always UTC (the source
// records the +00:00 offset), so the parsed instant is exact (docs/contracts).
func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range dbxTimeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// modeRead and modeWrite name the verbatim sides of a lineage row: the source
// side is a read by created_by, the target side a write. Lineage classifies the
// access by structure, so neither side is ever ModeUnknown — a row with no
// classifiable side simply yields no edge.
const (
	modeRead  = model.ModeRead
	modeWrite = model.ModeWrite
)
