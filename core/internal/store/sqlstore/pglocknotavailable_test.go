// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// postgresLockNotAvailable reports whether err carries PostgreSQL's lock_not_available
// SQLSTATE (55P03), the code a lock_timeout expiry raises. It reads the code structurally
// through the existing serverSQLState helper, so the server's message language cannot decide
// it: a Spanish server says "cancelando la sentencia debido a que se agotó el tiempo de espera
// de candados (locks)" for the same 55P03 an English one calls "lock timeout" (r106, measured).
func postgresLockNotAvailable(err error) bool {
	return serverSQLState(err) == "55P03"
}

func TestPostgresLockNotAvailableClassifiesBySQLStateNotMessage(t *testing.T) {
	spanish := &pgconn.PgError{Code: "55P03", Message: "cancelando la sentencia debido a que se agotó el tiempo de espera de candados (locks)"}
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"localized 55P03", spanish, true},
		{"localized 55P03 wrapped twice", fmt.Errorf("migrate: %w", fmt.Errorf("bootstrap: %w", spanish)), true},
		{"English 55P03", &pgconn.PgError{Code: "55P03", Message: "canceling statement due to lock timeout"}, true},
		{"statement timeout 57014 even when its text names the lock timeout", &pgconn.PgError{Code: "57014", Message: "canceling statement due to lock timeout"}, false},
		{"deadlock 40P01", &pgconn.PgError{Code: "40P01", Message: "deadlock detected"}, false},
		{"non-driver error whose text names 55P03", errors.New("ERROR: canceling statement due to lock timeout (SQLSTATE 55P03)"), false},
		{"nil", nil, false},
	} {
		if got := postgresLockNotAvailable(tc.err); got != tc.want {
			t.Errorf("%s: postgresLockNotAvailable(%v) = %t, want %t", tc.name, tc.err, got, tc.want)
		}
	}
}
