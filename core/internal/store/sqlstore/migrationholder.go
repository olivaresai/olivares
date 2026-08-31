// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// lockHolder is a REDACTED view of a session that holds, or blocks on, a lock this
// node is waiting for.
//
// The query text is deliberately absent and must stay absent. pg_stat_activity
// exposes it, it can contain tenant data, and this ends up in a boot log that
// operators paste into tickets. PID, application name, state and wait event are
// enough to name a holder without carrying its payload.
//
// Every column except the PID is NULLABLE, and that is not defensive typing — it is
// the ordinary case. A role without pg_read_all_stats sees its OWN sessions in full
// and other roles' sessions with the interesting columns blanked. Measured on
// PostgreSQL 15.18, one NOSUPERUSER role observing another:
//
//	row_absent=false app_null=false state_null=true wait_null=true start_null=true
//
// The previous shape coalesced backend_start to TIMESTAMPTZ '-infinity' and scanned
// it into a time.Time. pgx renders that sentinel as the STRING "-infinity", the scan
// failed, and because attribution is best-effort by contract the whole answer was
// discarded — silently, and only ever under the restricted role, which is to say
// only ever in production. The measured failure was:
//
//	err=sql: Scan error on column index 4 ... storing driver.Value type string
//	    into type *time.Time      -> holders=0
//
// So NULL is carried as NULL and rendered as an explicit "unavailable". "I could not
// see this" and "this was empty" are different facts about a holder, and an operator
// chasing a stuck boot needs to know which one they have.
type lockHolder struct {
	// PID is nullable, and not for privilege reasons — pg_locks.pid is not
	// privilege-gated. PostgreSQL documents it as NULL when the lock is held by a
	// PREPARED TRANSACTION, which has no backend behind it and yet keeps every lock
	// it took. A prepared pg_advisory_xact_lock lands in the same objsubid=1 key space
	// and can genuinely block the coordination lock, so this is a real holder with no
	// PID rather than a row to discard.
	//
	// Scanning it into a plain int made ONE such holder fail the scan and take the
	// whole attribution down with it — the same shape as the backend_start defect,
	// one column over.
	PID             sql.NullInt64
	ApplicationName sql.NullString
	State           sql.NullString
	WaitEvent       sql.NullString
	BackendStart    sql.NullTime
}

// holderUnavailable renders a column the observing role is not allowed to see.
//
// It is deliberately UNQUOTED while real values are quoted, so a session that
// literally sets application_name to "unavailable" is still distinguishable from one
// whose application_name could not be read.
const holderUnavailable = "unavailable"

// holderSourceRuneMax bounds how many runes are copied out of any one
// pg_stat_activity column and into a log line.
//
// It bounds the SOURCE, not the rendered result: the output also carries the
// truncation marker, and a rune can be four bytes, so the rendered field is a
// different (still finite) number. Measured: 64 source runes produced 78 output runes
// and 270 bytes. Naming it for the source is what stops the constant being read as a
// promise about the output.
//
// PostgreSQL already truncates application_name to NAMEDATALEN-1 and replaces
// non-printable bytes with '?' — measured on 15.18, where a client sending TAB,
// newline and U+202E (RIGHT-TO-LEFT OVERRIDE) got back
// "olv?probe?line???rtl…" at exactly 63 characters. This limit therefore changes
// nothing against a stock server, which is the point: it removes the DEPENDENCE on
// that behavior. The value crosses a version boundary and possibly a pooler, it is
// attacker-chosen, and it lands in an operator's terminal.
const holderSourceRuneMax = 64

// holderColumns is the redacted projection, spelled ONCE and shared by every query
// that produces a lockHolder.
//
// Two queries build this type — the self-attribution between polls and the external
// block observer — and a projection duplicated in both is a projection that can
// diverge. Adding `query` to one of them would be a data-leak regression that the
// other one's test would not catch.
const holderColumns = `a.application_name,
       a.state,
       a.wait_event,
       a.backend_start`

// scanHolders drains rows produced by holderColumns.
func scanHolders(rows *sql.Rows) ([]lockHolder, error) {
	var out []lockHolder
	for rows.Next() {
		var h lockHolder
		if err := rows.Scan(&h.PID, &h.ApplicationName, &h.State, &h.WaitEvent, &h.BackendStart); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// String renders a holder for a log line, in the same redacted shape.
func (h lockHolder) String() string {
	return fmt.Sprintf("pid=%s application=%s state=%s wait_event=%s since=%s",
		holderPID(h.PID),
		holderText(h.ApplicationName),
		holderText(h.State),
		holderText(h.WaitEvent),
		holderTime(h.BackendStart))
}

// holderPID renders the backend PID, or names a holder that has none.
//
// "prepared-transaction" rather than "unavailable": the PID is not missing because
// this role may not see it, it is missing because there is no backend. Those send an
// operator to different places.
func holderPID(v sql.NullInt64) string {
	if !v.Valid {
		return "prepared-transaction"
	}
	return strconv.FormatInt(v.Int64, 10)
}

// holderText renders one nullable text column: quoted and sanitized when it was
// readable, and an unquoted marker when it was not.
func holderText(v sql.NullString) string {
	if !v.Valid {
		return holderUnavailable
	}
	return strconv.Quote(sanitizeHolderText(v.String))
}

// holderTime renders a nullable timestamp the same way.
func holderTime(v sql.NullTime) string {
	if !v.Valid {
		return holderUnavailable
	}
	return v.Time.UTC().Format(time.RFC3339)
}

// truncationMarker says a value was cut rather than ended. A name that was cut and a
// name that ended are different facts, and only one of them means an operator is
// looking at a partial identifier.
const truncationMarker = "...(truncated)"

// sanitizeHolderText applies the content policy for untrusted session metadata:
// every non-printable rune becomes '?', and the result is bounded.
//
// unicode.IsPrint is the right predicate rather than a control-character check: it
// also rejects the format category, which is where U+202E and the other
// bidirectional overrides live. Those render as nothing at all while reversing the
// text after them, so a log line can be made to read as something it does not say.
func sanitizeHolderText(s string) string {
	var b strings.Builder
	// Reserve the OUTPUT bound, not the input length. Reserving len(s) would allocate
	// for a value this function exists to refuse to copy in full, which quietly
	// reintroduces the dependence on the source being small.
	b.Grow(holderSourceRuneMax*utf8.UTFMax + len(truncationMarker))
	n := 0
	for _, r := range s {
		if n == holderSourceRuneMax {
			// Say so rather than truncating silently: a name that was cut and a name
			// that ended are different facts, and only one of them means an operator
			// is looking at a partial identifier.
			b.WriteString(truncationMarker)
			break
		}
		if !unicode.IsPrint(r) {
			b.WriteRune('?')
		} else {
			b.WriteRune(r)
		}
		n++
	}
	return b.String()
}
