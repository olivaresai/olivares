// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mongoaudit

import (
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// atypeAuthCheck is the MongoDB audit action type the connector consumes: an
// authorization check for an operation. It is the only atype that carries the
// operation + namespace + identity needed for a per-collection R/RW edge.
const atypeAuthCheck = "authCheck"

// resultAuthorized is the MongoDB error code for an authorized/successful
// authorization check. Any other value is a denial/error (e.g. 13 = Unauthorized);
// a denied check did not touch the resource, so it is not an access (see the
// connector's handling — only result==0 is emitted).
const resultAuthorized = 0

// auditLine is the subset of a MongoDB mongo-schema audit log line the connector
// reads. It deliberately omits param.args (the command body: filters, documents,
// query arguments) — the payload is never read, never retained, never emitted
// (docs/SECURITY-HARDENING.md, minimal-data). Only atype, ts, users, and param.command + param.ns
// are declared.
type auditLine struct {
	AType  string      `json:"atype"`
	TS     mongoDate   `json:"ts"`
	Users  []mongoUser `json:"users"`
	Param  auditParam  `json:"param"`
	Result int         `json:"result"`
}

// mongoDate is MongoDB's extended-JSON date wrapper: { "$date": "<ISO-8601 UTC>" }.
type mongoDate struct {
	Date string `json:"$date"`
}

// mongoUser is one acting user in the users[] array: a user name scoped to a
// database. It is the raw identity emitted in OriginRef as "user@db".
type mongoUser struct {
	User string `json:"user"`
	DB   string `json:"db"`
}

// auditParam is the subset of the authCheck param document the connector reads:
// the operation (command) and the target namespace (ns = "db.collection"). The
// args sub-document (the command body) is intentionally NOT declared.
type auditParam struct {
	Command string `json:"command"`
	NS      string `json:"ns"`
}

// commandToMode maps a MongoDB authCheck param.command to an AccessMode, verbatim
// from MongoDB's own command vocabulary (no statement parsing, no guessing). The
// second result is false only when the command is not classifiable, in which case
// the caller emits ModeUnknown explicitly rather than dropping the edge — an
// observed access whose R/RW nature MongoDB does not pin down is still a real
// access to the namespace (ARCHITECTURE.md, unknown is explicit).
func commandToMode(command string) model.AccessMode {
	switch command {
	case "find", "aggregate", "count", "distinct", "getMore", "listCollections", "listIndexes":
		return model.ModeRead
	case "insert", "update", "delete", "findAndModify", "create", "createIndexes",
		"drop", "dropDatabase", "renameCollection":
		return model.ModeWrite
	default:
		// The command is not in MongoDB's known read/write set — the access
		// happened, but its R/RW nature is not pinned down; never guessed.
		return model.ModeUnknown
	}
}

// resourceFor maps an authCheck namespace (param.ns = "db.collection") to a
// resource kind and reference. A full namespace yields "mongo.collection" with the
// whole "db.collection" as the reference; a namespace with no collection part (a
// database-scoped command) degrades to "mongo.database".
func resourceFor(ns string) (kind, ref string) {
	ns = strings.TrimSpace(ns)
	// The first '.' separates the database from the collection; a collection name
	// may itself contain dots, so split only on the first separator.
	if i := strings.IndexByte(ns, '.'); i >= 0 && i < len(ns)-1 {
		return "mongo.collection", ns
	}
	return "mongo.database", strings.TrimSuffix(ns, ".")
}

// originRef builds the raw identity reference "user@db" from the first acting user,
// or "" if there is no user to attribute the access to (the caller skips such a
// record). The raw identity is always emitted; confidence is decided separately.
func originRef(users []mongoUser) string {
	if len(users) == 0 {
		return ""
	}
	u := users[0]
	if strings.TrimSpace(u.User) == "" {
		return ""
	}
	if strings.TrimSpace(u.DB) == "" {
		return u.User
	}
	return u.User + "@" + u.DB
}

// mongoTimeLayouts are the timestamp formats MongoDB emits in ts.$date. MongoDB
// writes the audit timestamp in ISO-8601 / RFC3339 UTC form, either with a 'Z'
// suffix or a numeric offset (e.g. "2026-06-03T10:23:45.806+0000"). All forms are
// normalized to UTC.
var mongoTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	// MongoDB's classic +0000 offset (no colon) is not RFC3339; accept it too.
	"2006-01-02T15:04:05.999-0700",
	"2006-01-02T15:04:05-0700",
}

// parseTime parses a MongoDB ts.$date string and normalizes it to UTC, returning
// ok=false if no layout matches. ObservedAt always comes from the source record's
// own timestamp (the dedup natural key, docs/contracts), never time.Now().
func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range mongoTimeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
