// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mysqlaudit

import "strings"

// genEntry is one parsed line of the MySQL general query log.
type genEntry struct {
	timestamp string
	connID    string
	command   string // Connect | Query | Init DB | Quit | Change user | …
	argument  string
}

// parseGeneralLine parses one general-query-log line of the shape
//
//	<timestamp>\t<id> <Command>\t<argument>
//
// It returns ok=false for a header line or a statement-continuation line (a
// multi-line query's later lines, which lack a leading timestamp) — those carry
// no new event, and the verb of a multi-line query is already on its first line.
func parseGeneralLine(line string) (genEntry, bool) {
	parts := strings.SplitN(line, "\t", 3)
	if len(parts) < 2 {
		return genEntry{}, false
	}
	ts := strings.TrimSpace(parts[0])
	if _, ok := parseTime(ts, generalTimeLayouts); !ok {
		return genEntry{}, false // not the start of a new entry
	}
	mid := strings.TrimSpace(parts[1]) // e.g. "42 Query" or "42 Init DB"
	sp := strings.IndexByte(mid, ' ')
	if sp < 0 {
		return genEntry{}, false
	}
	arg := ""
	if len(parts) == 3 {
		arg = parts[2]
	}
	return genEntry{
		timestamp: ts,
		connID:    mid[:sp],
		command:   strings.TrimSpace(mid[sp+1:]),
		argument:  arg,
	}, true
}

// parseConnectArg extracts the user@host and (optional) initial database from a
// Connect/Change-user argument such as "app_rw@10.0.0.5 on salesdb" or
// "root@localhost on  using SSL/TLS" (no database).
func parseConnectArg(arg string) (userHost, db string) {
	arg = strings.TrimSpace(arg)
	on := strings.Index(arg, " on ")
	if on < 0 {
		if f := strings.Fields(arg); len(f) > 0 {
			return f[0], ""
		}
		return "", ""
	}
	userHost = strings.TrimSpace(arg[:on])
	if f := strings.Fields(arg[on+len(" on "):]); len(f) > 0 && f[0] != "using" {
		db = f[0]
	}
	return userHost, db
}

// dbFromUse returns the database named by a "USE <db>" statement and ok=true if
// the statement is a USE; otherwise ok=false. A USE changes the connection's
// current database but is not itself a data access.
func dbFromUse(sql string) (string, bool) {
	s := strings.TrimSpace(strings.Trim(strings.TrimSpace(sql), "'"))
	if firstWord(s) != "USE" {
		return "", false
	}
	f := strings.Fields(s)
	if len(f) < 2 {
		return "", true
	}
	return strings.Trim(strings.TrimRight(f[1], ";"), "`"), true
}

// userOf returns the user part of a "user@host" reference (or the whole string
// if there is no host).
func userOf(userHost string) string {
	if i := strings.IndexByte(userHost, '@'); i >= 0 {
		return userHost[:i]
	}
	return userHost
}
