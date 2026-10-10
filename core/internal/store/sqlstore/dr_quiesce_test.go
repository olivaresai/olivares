// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/store"
)

// database/sql.Close refuses new work but permits an already-borrowed connection
// to finish. Promotion must wait even for a connection that has not issued SQL.
func TestSQLiteRestoreQuiesceDrainsBorrowedConnections(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		name := "drained"
		if cancelled {
			name = "cancelled"
		}
		t.Run(name, func(t *testing.T) {
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			conn, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			s := &sqlStore{engine: store.EngineSQLite, db: db}
			q, ok := any(s).(interface{ QuiesceForSQLiteRestore(context.Context) error })
			if !ok {
				t.Fatal("SQLite store has no restore drain")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- q.QuiesceForSQLiteRestore(ctx) }()
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for db.PingContext(ctx) == nil {
				select {
				case <-ticker.C:
				case <-ctx.Done():
					t.Fatal("pool still accepts new work")
				}
			}
			select {
			case err := <-done:
				t.Fatalf("drain returned with a borrowed connection: %v", err)
			default:
			}
			// Closing the pool must not permit promotion while a borrowed connection
			// can still execute SQL with the engine's old signer.
			if _, err := conn.ExecContext(context.Background(), "CREATE TABLE still_borrowed(x)"); err != nil {
				t.Fatal(err)
			}
			if cancelled {
				cancel()
			}
			if !cancelled {
				if err := conn.Close(); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-done:
				if cancelled && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel: %v", err)
				}
				if !cancelled && err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("drain did not return")
			}
			if err := db.Ping(); err == nil {
				t.Fatal("pool reopened before restart")
			}
		})
	}
}
