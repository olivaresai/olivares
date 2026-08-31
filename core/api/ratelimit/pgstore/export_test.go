// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package pgstore

import (
	"context"
	"io"

	"github.com/olivaresai/olivares/core/internal/pgpin"
)

// SweepOnceForTest runs one sweep cycle synchronously (the loop's cadence is
// wall-clock; tests drive it directly).
func (s *Store) SweepOnceForTest() { s.sweepOnce() }

// SearchPathForTest reads search_path from a connection of the STORE'S OWN DML
// pool. Asserting against a separately-opened pinned pool would only prove that
// pgpin works — measured: removing the pin from Open left such a test green,
// because it never observed the product's pool. This reads the real one.
func (s *Store) SearchPathForTest(ctx context.Context) (string, error) {
	var got string
	err := s.db.QueryRowContext(ctx, `SELECT pg_catalog.current_setting('search_path')`).Scan(&got)
	return got, err
}

// WriteBucketsForTest scrapes the gauge into w, so a hostile-resolution leg can
// assert the count comes from the real table rather than a shadow.
func (s *Store) WriteBucketsForTest(w io.Writer) { s.writeBuckets(w) }

// AdmitDMLForTest runs the admission check alone against dsn, so the EXECUTE
// arm can be pinned. It cannot be reached through Open in the serial supported
// path — ensureSchemaOn re-grants EXECUTE immediately before admission — but it
// is not dead: it covers a revocation racing between the post-flight and the
// admission, and DDL/DML DSNs pointing at states that differ.
func AdmitDMLForTest(ctx context.Context, dsn string) error {
	db, err := pgpin.Open(dsn, engineSchema, 1)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck
	return admitDML(ctx, db)
}

// EngineSchemaForTest is the schema the package ACTUALLY renders its SQL with at
// run time. The static ratchet seeds its AST resolution with dialect.EngineSchema
// because the production initializer is a function call it cannot fold; comparing
// the two closes the gap — measured, `mustSafeIdent(dialect.EngineSchema[:0] +
// "evil")` built every statement with "evil" while the ratchet reconstructed
// "public" and stayed green.
func EngineSchemaForTest() string { return engineSchema }
