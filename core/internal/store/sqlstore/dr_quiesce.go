// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/core/store"
)

// QuiesceForSQLiteRestore refuses new work and drains every borrowed connection
// before a snapshot replaces the database. Close alone does not wait for existing
// transactions: they could append with the boot-bound signer after promotion.
// The pool stays closed on success AND failure. Restart reloads the restored
// custody and module state before anything can write again.
func (s *sqlStore) QuiesceForSQLiteRestore(ctx context.Context) error {
	if s.engine != store.EngineSQLite {
		return fmt.Errorf("restore drain supports SQLite only")
	}
	if err := s.Close(); err != nil {
		return fmt.Errorf("close live SQLite store: %w", err)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for s.db.Stats().InUse != 0 {
		select {
		case <-ctx.Done():
			return fmt.Errorf("drain live SQLite store: %w", ctx.Err())
		case <-ticker.C:
		}
	}
	return ctx.Err()
}
