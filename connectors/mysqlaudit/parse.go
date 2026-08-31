// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mysqlaudit

import (
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// SignalMySQLAudit is the SignalSource for this connector. The SDK seeds pg_audit
// and cloudtrail but not MySQL; a connector introduces its own open-string source
// without an SDK release (docs/contracts/S02 §6).
const SignalMySQLAudit model.SignalSource = "mysql_audit"

// mariaEvent is one parsed MariaDB Audit Plugin record.
type mariaEvent struct {
	timestamp string
	user      string // account user name
	host      string // client host
	operation string // READ|WRITE|CREATE|ALTER|DROP|RENAME|QUERY*|CONNECT|…
	database  string
	object    string // table name (TABLE events) or the SQL text (QUERY events)
}

// parseAuditLine parses one MariaDB Audit Plugin log line. The format is
//
//	[timestamp],[serverhost],[username],[host],[connectionid],[queryid],[operation],[database],[object],[retcode]
//
// The object field (a table name, or for QUERY events the single-quoted SQL,
// which may itself contain commas) is everything between the 8th comma and the
// trailing retcode. It returns ok=false for a line that does not have the shape.
func parseAuditLine(line string) (mariaEvent, bool) {
	parts := strings.SplitN(line, ",", 9)
	if len(parts) < 9 {
		return mariaEvent{}, false
	}
	rest := parts[8] // "object,...,retcode"
	lc := strings.LastIndex(rest, ",")
	if lc < 0 {
		return mariaEvent{}, false
	}
	return mariaEvent{
		timestamp: strings.TrimSpace(parts[0]),
		user:      strings.TrimSpace(parts[2]),
		host:      strings.TrimSpace(parts[3]),
		operation: strings.ToUpper(strings.TrimSpace(parts[6])),
		database:  strings.TrimSpace(parts[7]),
		object:    rest[:lc],
	}, true
}

// tableOpToMode maps a MariaDB Audit Plugin TABLE-event operation to a mode,
// verbatim from the plugin's classification (docs/contracts). ok=false
// means the operation is not a TABLE-event data access.
func tableOpToMode(op string) (model.AccessMode, bool) {
	switch op {
	case "READ":
		return model.ModeRead, true
	case "WRITE":
		return model.ModeWrite, true
	case "CREATE", "ALTER", "DROP", "RENAME":
		return model.ModeWrite, true // DDL — a schema write
	default:
		return "", false
	}
}

// classifyVerb classifies a SQL statement by its leading verb and returns the
// access mode and the upper-cased verb (used as ToolRef). The statement is read
// only far enough to read the first keyword; the body is never retained. An
// unrecognized or ambiguous verb yields ModeUnknown — the read/write nature is
// never guessed (ARCHITECTURE.md).
func classifyVerb(sql string) (model.AccessMode, string) {
	s := strings.TrimSpace(strings.Trim(strings.TrimSpace(sql), "'"))
	verb := firstWord(s)
	switch verb {
	case "SELECT", "SHOW", "DESC", "DESCRIBE", "EXPLAIN", "HANDLER":
		return model.ModeRead, verb
	case "INSERT", "UPDATE", "DELETE", "REPLACE", "TRUNCATE", "LOAD":
		return model.ModeWrite, verb
	case "CREATE", "ALTER", "DROP", "RENAME", "GRANT", "REVOKE":
		return model.ModeWrite, verb // DDL/DCL — a write
	case "":
		return model.ModeUnknown, "QUERY"
	default:
		return model.ModeUnknown, verb
	}
}

// firstWord returns the upper-cased first whitespace-delimited token of s.
func firstWord(s string) string {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\n', '\r', '(':
			return strings.ToUpper(s[:i])
		}
	}
	return strings.ToUpper(s)
}

// mariaTimeLayouts are the timestamp formats the MariaDB Audit Plugin emits.
// The plugin writes server-local time without a zone, interpreted here as UTC
// (configure the server to log UTC); ISO variants are also accepted.
var mariaTimeLayouts = []string{
	"20060102 15:04:05",
	"2006-01-02 15:04:05",
	time.RFC3339Nano,
	time.RFC3339,
}

// generalTimeLayouts are the timestamp formats the MySQL/MariaDB general query
// log emits in its modern (5.7+/10.1+) per-line UTC form. The legacy abbreviated
// format (060102 ...) is deliberately NOT supported: it omits the timestamp on
// the second and later events within the same second, so accepting it would
// silently drop same-second rows. A legacy-format log therefore parses to no
// edges (visibly unsupported) rather than to a lossy subset.
var generalTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
}

func parseTime(s string, layouts []string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range layouts {
		if t, err := time.ParseInLocation(l, s, time.UTC); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
