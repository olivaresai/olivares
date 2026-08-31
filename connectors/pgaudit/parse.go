// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package pgaudit

import (
	"encoding/csv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// auditPrefix marks a pgAudit log message inside a PostgreSQL log line. Only
// lines whose message begins with it are pgAudit records; everything else (a
// plain LOG line, a connection notice) is skipped.
const auditPrefix = "AUDIT: "

// frame is the log-line envelope around a pgAudit message: the identity and
// timestamp columns PostgreSQL adds (csvlog columns / jsonlog keys), which
// pgAudit itself does not carry. This is why the connector requires a structured
// log format — the attribution lives in the frame, not in the AUDIT message.
type frame struct {
	timestamp string // PostgreSQL log_time / "timestamp"
	user      string // session role (user_name / "user")
	database  string // database_name / "dbname"
	appName   string // application_name (the per-agent bridge when set)
	message   string // the raw log message, expected to start with auditPrefix
}

// auditRecord holds the fields of a pgAudit AUDIT message the connector uses.
// The STATEMENT (raw SQL) and PARAMETER fields are deliberately NOT captured:
// the connector emits only the access edge, never the SQL body (docs/SECURITY-HARDENING.md).
type auditRecord struct {
	auditType string // SESSION | OBJECT
	class     string // READ | WRITE | DDL | FUNCTION | ROLE | MISC | MISC_SET
	command   string // SELECT | INSERT | UPDATE | …
	objType   string // TABLE | VIEW | …
	objName   string // schema.object
}

// parseAuditMessage parses a pgAudit "AUDIT: …" message. The payload after the
// prefix is itself CSV (pgAudit quotes fields that contain commas/quotes), so it
// is parsed with encoding/csv. It returns ok=false for a non-pgAudit message or
// a malformed payload.
//
// pgAudit message fields (https://github.com/pgaudit/pgaudit): AUDIT_TYPE,
// STATEMENT_ID, SUBSTATEMENT_ID, CLASS, COMMAND, OBJECT_TYPE, OBJECT_NAME,
// STATEMENT, PARAMETER. Only indices 0,3,4,5,6 are read; 7 (STATEMENT) and 8
// (PARAMETER) are intentionally ignored.
func parseAuditMessage(msg string) (auditRecord, bool) {
	if !strings.HasPrefix(msg, auditPrefix) {
		return auditRecord{}, false
	}
	r := csv.NewReader(strings.NewReader(msg[len(auditPrefix):]))
	r.FieldsPerRecord = -1
	f, err := r.Read()
	if err != nil || len(f) < 7 {
		return auditRecord{}, false
	}
	return auditRecord{
		auditType: f[0],
		class:     strings.TrimSpace(f[3]),
		command:   strings.TrimSpace(f[4]),
		objType:   strings.TrimSpace(f[5]),
		objName:   strings.TrimSpace(f[6]),
	}, true
}

// classToMode maps a pgAudit CLASS to an AccessMode, verbatim from pgAudit's own
// classification (docs/contracts). The second result is false for
// classes that are not a data-resource access (ROLE/MISC/MISC_SET), which the
// connector skips rather than emit a meaningless edge.
func classToMode(class string) (model.AccessMode, bool) {
	switch strings.ToUpper(class) {
	case "READ":
		return model.ModeRead, true
	case "WRITE":
		return model.ModeWrite, true
	case "DDL":
		// DDL mutates the catalog — a schema write.
		return model.ModeWrite, true
	case "FUNCTION":
		// Executing a function may read or write; pgAudit does not say which.
		return model.ModeUnknown, true
	default:
		return "", false
	}
}

// resourceKindFor maps a pgAudit OBJECT_TYPE to a resource kind ("postgres.<type>").
func resourceKindFor(objType string) string {
	t := strings.ToLower(strings.TrimSpace(objType))
	if t == "" {
		return "postgres.object"
	}
	return "postgres." + strings.ReplaceAll(t, " ", "_")
}

// pgTimeLayouts are the timestamp formats PostgreSQL emits in csvlog/jsonlog,
// most-specific first. PostgreSQL formats log_time as a zone ABBREVIATION (per
// log_timezone), e.g. "UTC" or "EDT", never a numeric offset; the RFC3339 forms
// cover jsonlog setups and numeric offsets if a deployment produces them.
var pgTimeLayouts = []string{
	"2006-01-02 15:04:05.999 MST",
	"2006-01-02 15:04:05 MST",
	"2006-01-02 15:04:05.999-07",
	"2006-01-02 15:04:05-07",
	time.RFC3339Nano,
	time.RFC3339,
}

// parseTimestamp parses a PostgreSQL log timestamp and normalizes it to UTC,
// returning ok=false if no layout matches OR the zone is an abbreviation Go
// cannot resolve to an offset.
//
// This guard matters: Go maps "UTC"/"GMT" and numeric offsets correctly, but for
// any other zone abbreviation (EDT, CEST, …) it cannot know the offset and
// silently assigns 0, which would shift ObservedAt — the dedup and forensic
// timeline key — by hours with no error. Rather than corrupt the timestamp, the
// connector skips such records; the PostgreSQL server must log in UTC
// (log_timezone = 'UTC'), the standard setup for log shipping.
func parseTimestamp(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range pgTimeLayouts {
		t, err := time.Parse(l, s)
		if err != nil {
			continue
		}
		// Reject an unresolved zone abbreviation: a named zone with a zero offset
		// that is not actually UTC/GMT is Go's fallback for an abbreviation it
		// could not map, and its wall-clock digits are not really UTC.
		if name, off := t.Zone(); off == 0 && name != "" && name != "UTC" && name != "GMT" {
			continue
		}
		return t.UTC(), true
	}
	return time.Time{}, false
}
