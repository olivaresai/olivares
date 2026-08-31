// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package snowflakeaudit

import (
	"encoding/json"
	"strings"
	"time"
)

// Resource kinds and ToolRef bucket names. The ToolRef values are the verbatim
// ACCESS_HISTORY column names the object was found in (docs/contracts).
const (
	kindTable  = "snowflake.table"
	kindColumn = "snowflake.column"

	toolDirect   = "direct_objects_accessed"
	toolBase     = "base_objects_accessed"
	toolModified = "objects_modified"
)

// accessRow is the subset of one SNOWFLAKE.ACCOUNT_USAGE.ACCESS_HISTORY row the
// connector reads. The JSON keys are the view's uppercase column names as they
// appear in a row exported with JSON output. ROLE_NAME is NOT a column of
// ACCESS_HISTORY (it has no role column); it is read only so that an export which
// joins QUERY_HISTORY.ROLE_NAME in can influence shared-account confidence, and is
// otherwise empty and harmless.
//
// There is deliberately NO field for QUERY_TEXT or any SQL body: ACCESS_HISTORY
// rows carry only a QUERY_ID, and the connector emits the access edge, never the
// statement (docs/SECURITY-HARDENING.md). QUERY_ID itself is not emitted.
type accessRow struct {
	QueryStartTime        string         `json:"QUERY_START_TIME"`
	UserName              string         `json:"USER_NAME"`
	RoleName              string         `json:"ROLE_NAME"`
	DirectObjectsAccessed []accessObject `json:"DIRECT_OBJECTS_ACCESSED"`
	BaseObjectsAccessed   []accessObject `json:"BASE_OBJECTS_ACCESSED"`
	ObjectsModified       []accessObject `json:"OBJECTS_MODIFIED"`
}

// accessObject is one element of an access array. The nested keys are camelCase
// in the ACCESS_HISTORY JSON (objectName, objectDomain, columns[].columnName).
// Only the read fields are declared: lineage (baseSources/directSources),
// objectId/columnId and argument signatures are intentionally not parsed.
type accessObject struct {
	ObjectName   string         `json:"objectName"`
	ObjectDomain string         `json:"objectDomain"`
	Columns      []accessColumn `json:"columns"`
}

// accessColumn is one element of an object's columns[] array.
type accessColumn struct {
	ColumnName string `json:"columnName"`
}

// parseRow parses one NDJSON ACCESS_HISTORY row. It returns ok=false for a line
// that is not valid JSON (e.g. a header line or a blank record).
func parseRow(line []byte) (accessRow, bool) {
	var r accessRow
	if err := json.Unmarshal(line, &r); err != nil {
		return accessRow{}, false
	}
	return r, true
}

// nonEmptyColumns returns the trimmed, non-empty column names of an object,
// preserving order. An object with no usable column names is treated as
// table-grained by the caller.
func nonEmptyColumns(cols []accessColumn) []string {
	var out []string
	for _, c := range cols {
		if n := strings.TrimSpace(c.ColumnName); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// snowflakeTimeLayouts are the timestamp formats Snowflake emits for a
// QUERY_START_TIME (TIMESTAMP_LTZ) value exported to JSON. Snowflake's default
// rendering is "2006-01-02 15:04:05.999 -0700" (space-separated offset with no
// colon); the RFC3339 forms cover an export that renders ISO-8601 'Z'/offset.
var snowflakeTimeLayouts = []string{
	"2006-01-02 15:04:05.999999999 -0700",
	"2006-01-02 15:04:05.999 -0700",
	"2006-01-02 15:04:05 -0700",
	time.RFC3339Nano,
	time.RFC3339,
}

// parseTime parses a Snowflake QUERY_START_TIME and normalizes it to UTC. It
// returns ok=false if no layout matches. ObservedAt is the dedup natural key, so
// it always comes from the source row's own timestamp, never time.Now()
// (docs/contracts).
func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range snowflakeTimeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
