// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mssqlaudit

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// SignalMSSQLAudit is the SignalSource for this connector. The SDK seeds pg_audit
// and cloudtrail but not SQL Server; a connector introduces its own open-string
// source without an SDK release (docs/contracts/S02 §6).
const SignalMSSQLAudit model.SignalSource = "mssql_audit"

// tableClassType is the SQL Server Audit class_type code for a user table ("U",
// from sys.objects/spt_values). Any other class_type (view "V", procedure "P",
// function "FN"/"TF"/"IF", …) is reported as the generic "mssql.object".
const tableClassType = "U"

// record is the subset of a SQL Server Audit row (as emitted by
// sys.fn_get_audit_file and exported to NDJSON) that the connector reads. The
// `statement` column (the raw T-SQL) and the other ~40 audit columns are
// deliberately NOT declared here: the connector emits only the access edge,
// never the SQL body (docs/SECURITY-HARDENING.md).
type record struct {
	EventTime             string `json:"event_time"`
	ActionID              string `json:"action_id"`   // "SL" | "IN" | "UP" | "DL" | "EX" | …
	ActionName            string `json:"action_name"` // "SELECT" | "INSERT" | … (when exported)
	Succeeded             *bool  `json:"succeeded"`
	ServerPrincipalName   string `json:"server_principal_name"`
	DatabasePrincipalName string `json:"database_principal_name"`
	DatabaseName          string `json:"database_name"`
	SchemaName            string `json:"schema_name"`
	ObjectName            string `json:"object_name"`
	ClassType             string `json:"class_type"`
}

// recordFromLine parses one exported NDJSON audit row. It returns ok=false for a
// line that is not valid JSON.
func recordFromLine(line []byte) (record, bool) {
	var r record
	if err := json.Unmarshal(line, &r); err != nil {
		return record{}, false
	}
	return r, true
}

// classifyAction maps a SQL Server Audit data action to an access mode and the
// canonical action name (used as ToolRef), VERBATIM from SQL Server's own
// classification (docs/contracts). The match is on the action_id character
// code that sys.fn_get_audit_file emits (SL/IN/UP/DL/EX), falling back to the
// action_name when an export carries the long form instead.
//
// EXECUTE is intentionally ModeUnknown: executing a procedure or function may read
// or write, and the audit does not say which — the connector never fakes what a
// procedure does (ARCHITECTURE.md). The second result is false when the action is not a
// data-resource access this connector emits.
func classifyAction(actionID, actionName string) (mode model.AccessMode, tool string, ok bool) {
	switch strings.ToUpper(strings.TrimSpace(actionID)) {
	case "SL":
		return model.ModeRead, "SELECT", true
	case "IN":
		return model.ModeWrite, "INSERT", true
	case "UP":
		return model.ModeWrite, "UPDATE", true
	case "DL":
		return model.ModeWrite, "DELETE", true
	case "EX":
		// Executing a procedure/function may read or write; the audit does not say.
		return model.ModeUnknown, "EXECUTE", true
	}

	switch strings.ToUpper(strings.TrimSpace(actionName)) {
	case "SELECT":
		return model.ModeRead, "SELECT", true
	case "INSERT":
		return model.ModeWrite, "INSERT", true
	case "UPDATE":
		return model.ModeWrite, "UPDATE", true
	case "DELETE":
		return model.ModeWrite, "DELETE", true
	case "EXECUTE":
		return model.ModeUnknown, "EXECUTE", true
	default:
		// A non-DML/EXECUTE action (e.g. RECEIVE "RC", REFERENCES "RF", or a
		// server/permission action): not a data access this connector emits.
		return "", "", false
	}
}

// resourceKindFor returns the resource kind for a class_type: "mssql.table" for a
// user table ("U"), "mssql.object" for any other auditable schema entity (view,
// procedure, function, …) or when class_type is absent.
func resourceKindFor(classType string) string {
	if strings.EqualFold(strings.TrimSpace(classType), tableClassType) {
		return "mssql.table"
	}
	return "mssql.object"
}

// resourceRef builds the qualified resource reference "database.schema.object"
// from the parts present, joining with "." and skipping any empty component. It
// returns "" if there is no object name to anchor the reference.
func resourceRef(database, schema, object string) string {
	object = strings.TrimSpace(object)
	if object == "" {
		return ""
	}
	parts := make([]string, 0, 3)
	if d := strings.TrimSpace(database); d != "" {
		parts = append(parts, d)
	}
	if s := strings.TrimSpace(schema); s != "" {
		parts = append(parts, s)
	}
	parts = append(parts, object)
	return strings.Join(parts, ".")
}

// mssqlTimeLayouts are the timestamp formats SQL Server Audit emits for
// event_time. sys.fn_get_audit_file documents event_time as a datetime2 that is
// "UTC date and time when the auditable action is fired"; a JSON export of a
// datetime2 is rendered without a zone (e.g. "2026-06-03T10:23:45.1234567" or
// "2026-06-03 10:23:45.123"), which is interpreted as UTC. The RFC3339 'Z'/offset
// forms are accepted too, for exporters that stamp an explicit zone.
var mssqlTimeLayouts = []string{
	"2006-01-02T15:04:05.9999999",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05.9999999",
	"2006-01-02 15:04:05.999",
	"2006-01-02 15:04:05",
	time.RFC3339Nano,
	time.RFC3339,
}

// parseTime parses a SQL Server Audit event_time and normalizes it to UTC. A
// zone-less datetime2 is interpreted as UTC (the column is documented UTC);
// returns ok=false if no layout matches. ObservedAt always comes from this source
// timestamp, never time.Now() (it is the dedup natural key; docs/contracts).
func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range mssqlTimeLayouts {
		if t, err := time.ParseInLocation(l, s, time.UTC); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
