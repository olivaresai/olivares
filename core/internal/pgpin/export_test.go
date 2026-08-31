// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package pgpin

import "github.com/jackc/pgx/v5"

// ConnConfigForTest returns the config Open would dial with, so the pooler
// ratchet can assert that pinning added no startup parameter.
func ConnConfigForTest(dsn, path string) (*pgx.ConnConfig, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	return pinBeforeValidate(cfg, path), nil
}
