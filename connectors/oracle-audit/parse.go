// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package oracleaudit

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// row is the subset of an Oracle UNIFIED_AUDIT_TRAIL row the connector reads. The
// field names match the view's column names as exported to NDJSON. The export
// keys are upper-cased by Oracle's JSON unload; the json tags use the exact
// column names and Go's encoding/json matches them case-insensitively, so a
// lower-cased export also parses.
//
// SQL_TEXT and any bind/parameter column are deliberately NOT declared: the SQL
// body never enters the struct, so it can never be emitted (docs/SECURITY-HARDENING.md,
// minimal-data). Only the columns needed to build an access edge are read.
type row struct {
	DBUsername   string `json:"DBUSERNAME"`          // database user that performed the action
	ActionName   string `json:"ACTION_NAME"`         // the audited action (e.g. SELECT, "CREATE TABLE")
	ObjectSchema string `json:"OBJECT_SCHEMA"`       // owning schema of the object acted on
	ObjectName   string `json:"OBJECT_NAME"`         // name of the object acted on
	EventTSUTC   string `json:"EVENT_TIMESTAMP_UTC"` // event time in UTC (preferred)
	EventTS      string `json:"EVENT_TIMESTAMP"`     // event time in DB-local time (fallback)
}

// parseRow parses one NDJSON line into a row. It returns ok=false for a line that
// is not valid JSON (a blank or malformed line is skipped, not fatal).
func parseRow(line []byte) (row, bool) {
	var r row
	if err := json.Unmarshal(line, &r); err != nil {
		return row{}, false
	}
	return r, true
}

// resourceRef returns the qualified "OBJECT_SCHEMA.OBJECT_NAME" reference and
// ok=true when an object name is present. An audit row with no object (e.g. a
// session/logon action) is not a data-resource access and is skipped.
func (r row) resourceRef() (string, bool) {
	schema := strings.TrimSpace(r.ObjectSchema)
	name := strings.TrimSpace(r.ObjectName)
	if name == "" {
		return "", false
	}
	if schema != "" {
		return schema + "." + name, true
	}
	return name, true
}

// eventTimestamp returns the event time to use as ObservedAt, preferring the UTC
// column (EVENT_TIMESTAMP_UTC) and falling back to EVENT_TIMESTAMP only when the
// UTC column is absent from the export.
func (r row) eventTimestamp() string {
	if ts := strings.TrimSpace(r.EventTSUTC); ts != "" {
		return ts
	}
	return r.EventTS
}

// classifyAction maps an Oracle UNIFIED_AUDIT_TRAIL ACTION_NAME to an AccessMode,
// verbatim from Oracle's own action vocabulary (docs/contracts). The
// read/write nature is taken from the action name only — the statement text is
// never read, so it is never used to refine the mode. An action Oracle does not
// classify as a plain read or write (EXECUTE, LOCK, anything unrecognized) yields
// ModeUnknown: it is stated honestly, never guessed (ARCHITECTURE.md).
//
// Oracle reports DDL actions as a verb + object kind, e.g. "CREATE TABLE",
// "ALTER INDEX", "DROP TABLE"; those are matched by their leading verb. DML and
// SELECT are reported as the bare verb.
func classifyAction(action string) model.AccessMode {
	a := strings.ToUpper(strings.TrimSpace(action))
	switch a {
	case "SELECT":
		return model.ModeRead
	case "INSERT", "UPDATE", "DELETE":
		return model.ModeWrite
	case "TRUNCATE TABLE":
		return model.ModeWrite // DDL — a schema/data write
	case "EXECUTE":
		// A procedure/function call may read or write; the audit does not say which.
		return model.ModeUnknown
	case "LOCK":
		// A lock acquisition is not classified read or write by the trail.
		return model.ModeUnknown
	}
	// DDL verbs come as "<VERB> <OBJECT_KIND>" (e.g. "CREATE TABLE", "ALTER INDEX",
	// "DROP TABLE") — a catalog mutation, i.e. a write.
	switch firstWord(a) {
	case "CREATE", "ALTER", "DROP":
		return model.ModeWrite
	}
	return model.ModeUnknown
}

// firstWord returns the first whitespace-delimited token of an already-upper-cased
// string (the action name). It is used only to recognize the DDL verb prefix.
func firstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i]
	}
	return s
}

// oracleTimeLayouts are the timestamp formats an NDJSON export of the Unified
// Audit Trail emits for EVENT_TIMESTAMP_UTC / EVENT_TIMESTAMP. Oracle's TIMESTAMP(6)
// renders without a zone; a layout without a zone is interpreted as UTC (the UTC
// column is already UTC, and the operator is told to configure the server in UTC
// for the local-time fallback). RFC3339 'Z'/offset forms are also accepted for
// exporters that render an ISO-8601 timestamp.
var oracleTimeLayouts = []string{
	"2006-01-02T15:04:05.999999999Z07:00", // RFC3339Nano (Z or numeric offset)
	"2006-01-02T15:04:05Z07:00",           // RFC3339
	"2006-01-02 15:04:05.999999999",       // Oracle TIMESTAMP(6), space-separated, no zone
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05.999999999", // ISO 'T' separator, no zone
	"2006-01-02T15:04:05",
}

// parseTime parses an Oracle event timestamp and normalizes it to UTC, returning
// ok=false if no layout matches. A zone-less layout is interpreted in UTC so a
// missing zone never silently shifts ObservedAt — the dedup natural key.
func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range oracleTimeLayouts {
		if t, err := time.ParseInLocation(l, s, time.UTC); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
