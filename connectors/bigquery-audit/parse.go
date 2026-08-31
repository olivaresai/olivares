// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package bigqueryaudit

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// logEntry is the subset of a Cloud Logging LogEntry the connector reads. The
// entry-level `timestamp` is the event time (ObservedAt); `protoPayload` is the
// Cloud Audit Logs AuditLog. Only the fields mapped onto an edge are declared —
// notably NO query/SQL/job-configuration field is read (minimal data, docs/SECURITY-HARDENING.md).
type logEntry struct {
	Timestamp    string       `json:"timestamp"`
	ProtoPayload protoPayload `json:"protoPayload"`
}

// protoPayload is the subset of the Cloud Audit Logs AuditLog the connector reads.
// `methodName` is the BigQuery API method (ToolRef); `resourceName` is the target
// table in "projects/P/datasets/D/tables/T" form; `metadata` is the
// BigQueryAuditMetadata payload carrying the read/write classification.
type protoPayload struct {
	AuthenticationInfo authenticationInfo `json:"authenticationInfo"`
	MethodName         string             `json:"methodName"`
	ResourceName       string             `json:"resourceName"`
	Metadata           bqAuditMetadata    `json:"metadata"`
}

// authenticationInfo carries the authenticated principal. Only the identifier
// (principalEmail) is read; no credential/token value is ever read or emitted.
type authenticationInfo struct {
	PrincipalEmail string `json:"principalEmail"`
}

// bqAuditMetadata is the subset of BigQueryAuditMetadata the connector reads. The
// proto models the event as a `oneof event`; `tableDataRead` and `tableDataChange`
// are two of its arms and are the only ones this connector classifies (a table
// DATA access). Both are read as a presence flag only — their inner fields (job
// uris, row counts, accessed column lists) are deliberately NOT declared, so the
// connector cannot leak them.
type bqAuditMetadata struct {
	TableDataRead   *tableDataEvent `json:"tableDataRead"`
	TableDataChange *tableDataEvent `json:"tableDataChange"`
}

// tableDataEvent is an empty struct used solely as a presence marker for the
// tableDataRead / tableDataChange oneof arms. It intentionally declares no fields:
// the connector needs only to know WHICH arm is set, never its contents.
type tableDataEvent struct{}

// classifyMode maps a BigQueryAuditMetadata event to an access mode, verbatim
// from the platform's own event oneof (docs/contracts): a tableDataRead
// event is a read, a tableDataChange event is a write. The second result is
// false when the metadata carries neither table-data event — that entry is not a
// table data access, so no edge is emitted (the mode is never guessed; ARCHITECTURE.md).
func classifyMode(m bqAuditMetadata) (model.AccessMode, bool) {
	switch {
	case m.TableDataRead != nil:
		return model.ModeRead, true
	case m.TableDataChange != nil:
		return model.ModeWrite, true
	default:
		return "", false
	}
}

// resourceRefFromName parses a BigQuery audit resourceName of the form
// "projects/P/datasets/D/tables/T" into the dotted "P.D.T" reference. It returns
// ok=false for any resourceName that is not a fully-qualified table (e.g. a
// dataset- or job-level resource), so a non-table entry yields no edge.
func resourceRefFromName(resourceName string) (string, bool) {
	parts := strings.Split(strings.TrimSpace(resourceName), "/")
	if len(parts) != 6 ||
		parts[0] != "projects" || parts[2] != "datasets" || parts[4] != "tables" ||
		parts[1] == "" || parts[3] == "" || parts[5] == "" {
		return "", false
	}
	return parts[1] + "." + parts[3] + "." + parts[5], true
}

// bqTimeLayouts are the timestamp formats Cloud Logging emits for a LogEntry's
// `timestamp`. The protobuf JSON marshaler renders a google.protobuf.Timestamp as
// RFC3339 in UTC with a trailing 'Z' (with or without fractional seconds), so the
// two RFC3339 layouts cover every shape (docs/contracts: ObservedAt is the
// source record's own clock, normalized to UTC, never time.Now()).
var bqTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
}

// parseTime parses a Cloud Logging entry timestamp and normalizes it to UTC,
// returning ok=false if no layout matches.
func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range bqTimeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// entryFromJSON parses one NDJSON line into a LogEntry, returning ok=false for a
// line that is not valid JSON.
func entryFromJSON(line []byte) (logEntry, bool) {
	var e logEntry
	if err := json.Unmarshal(line, &e); err != nil {
		return logEntry{}, false
	}
	return e, true
}
