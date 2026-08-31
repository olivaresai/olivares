// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package redshiftaudit

import (
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// activityRecord holds the fields the connector reads from one Redshift
// user-activity log line. Only the bracketed-prefix fields the edge needs are
// declared (timestamp, db, user); the SQL statement after `LOG:` is NOT a field
// of this struct — it is read locally to classify by verb and then discarded, so
// the body can never travel into an emitted edge (docs/SECURITY-HARDENING.md).
type activityRecord struct {
	timestamp string // leading ISO-8601 timestamp (UTC)
	db        string // `db=` — the database name (degraded resource granularity)
	user      string // `user=` — the database credential the statement ran under
	verb      string // the leading SQL verb (ToolRef); the rest of the SQL is dropped
}

// logMarker separates the bracketed session prefix from the statement text. The
// AWS docs describe the user-activity `query` column as "a prefix of LOG:
// followed by the text of the query"; the prefix closes with "]'" before it.
const logMarker = "LOG: "

// parseLine parses one Redshift user-activity log line of the form
//
//	'2026-06-03T10:23:45Z UTC [ db=dev user=analyst pid=123 userid=100 xid=456 ]' LOG: SELECT ...
//
// It extracts the leading timestamp, the bracketed db= and user= fields, and the
// leading verb of the statement. The statement text after the verb is read but
// NOT retained: only the verb (a fixed keyword, never user data) is kept. It
// returns ok=false for a line that does not have this shape (e.g. a continuation
// line of a multi-line statement, which carries no bracketed prefix).
func parseLine(line string) (activityRecord, bool) {
	s := strings.TrimSpace(line)
	s = strings.TrimPrefix(s, "'") // Redshift wraps the prefix in single quotes

	// The session prefix is the bracketed segment "[ ... ]"; the timestamp is
	// everything before the '['.
	open := strings.IndexByte(s, '[')
	closeIdx := strings.IndexByte(s, ']')
	if open < 0 || closeIdx < 0 || closeIdx < open {
		return activityRecord{}, false
	}

	// The leading timestamp: "2026-06-03T10:23:45Z UTC" — drop a trailing zone
	// abbreviation word (the offset is already encoded by the 'Z'/numeric suffix).
	ts := strings.TrimSpace(s[:open])
	if i := strings.IndexByte(ts, ' '); i >= 0 {
		ts = ts[:i]
	}
	if ts == "" {
		return activityRecord{}, false
	}

	rec := activityRecord{timestamp: ts}
	for _, tok := range strings.Fields(s[open+1 : closeIdx]) {
		k, v, found := strings.Cut(tok, "=")
		if !found {
			continue
		}
		switch k {
		case "db":
			rec.db = v
		case "user":
			rec.user = v
		}
	}

	// The statement follows the "]' LOG: " marker. Only its leading verb is read;
	// the body is intentionally not stored on the record.
	if m := strings.Index(s, logMarker); m >= 0 {
		rec.verb = firstWord(s[m+len(logMarker):])
	}
	return rec, true
}

// classifyVerb maps a Redshift statement's LEADING SQL verb to an access mode,
// the DEGRADED by-verb classification (docs/contracts): the
// user-activity log records the statement, not a read/write class, so the verb is
// the only honest signal available. An unrecognized or empty verb yields
// ModeUnknown — the read/write nature is never guessed (ARCHITECTURE.md). The returned
// verb is the upper-cased keyword, used verbatim as ToolRef.
func classifyVerb(verb string) (model.AccessMode, string) {
	v := strings.ToUpper(strings.TrimSpace(verb))
	switch v {
	case "SELECT", "SHOW", "UNLOAD":
		return model.ModeRead, v
	case "INSERT", "UPDATE", "DELETE", "COPY", "TRUNCATE":
		return model.ModeWrite, v
	case "CREATE", "ALTER", "DROP", "GRANT", "REVOKE":
		return model.ModeWrite, v // DDL/DCL — a schema/privilege write
	case "":
		return model.ModeUnknown, ""
	default:
		return model.ModeUnknown, v
	}
}

// firstWord returns the upper-cased first whitespace- or paren-delimited token of
// s. It reads only the leading keyword; the remainder of the statement (the SQL
// body) is never consumed beyond finding the verb's end, and is never returned.
func firstWord(s string) string {
	s = strings.TrimSpace(s)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\n', '\r', '(', ';':
			return strings.ToUpper(s[:i])
		}
	}
	return strings.ToUpper(s)
}

// redshiftTimeLayouts are the timestamp formats the Redshift user-activity log
// emits in its line prefix. Redshift writes ISO-8601 in UTC with a 'Z' suffix
// (e.g. "2026-06-03T10:23:45Z"); the RFC3339 forms also accept a numeric offset
// or fractional seconds if a deployment produces them.
var redshiftTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05Z",
}

// parseTime parses a Redshift user-activity timestamp and normalizes it to UTC,
// returning ok=false if no layout matches. ObservedAt must be the source clock
// (the dedup natural key), never time.Now().
func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range redshiftTimeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
