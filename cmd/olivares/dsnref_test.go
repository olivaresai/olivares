// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveDSNRef(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	getenv := func(k string) string {
		if k == "OLIVARES_TEST_DSN" {
			return "postgres://from-env/olivares"
		}
		return ""
	}

	// Literal passthrough.
	if got, err := resolveDSNRef(ctx, "--dsn", "postgres://olivares_app@db/olivares", getenv); err != nil || got != "postgres://olivares_app@db/olivares" {
		t.Fatalf("literal passthrough = %q, %v", got, err)
	}
	// Empty passes through.
	if got, err := resolveDSNRef(ctx, "--dsn", "", getenv); err != nil || got != "" {
		t.Fatalf("empty = %q, %v", got, err)
	}
	// env: resolves.
	if got, err := resolveDSNRef(ctx, "--dsn", "env:OLIVARES_TEST_DSN", getenv); err != nil || got != "postgres://from-env/olivares" {
		t.Fatalf("env ref = %q, %v", got, err)
	}
	// env: unset fails closed.
	if _, err := resolveDSNRef(ctx, "--dsn", "env:NOPE_UNSET", getenv); err == nil {
		t.Fatal("env ref to an unset var should fail")
	}
	// file: resolves (trailing newline trimmed by the handler).
	f := filepath.Join(t.TempDir(), "db.dsn")
	if err := os.WriteFile(f, []byte("postgres://from-file/olivares\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveDSNRef(ctx, "--dsn", "file:"+f, getenv); err != nil || got != "postgres://from-file/olivares" {
		t.Fatalf("file ref = %q, %v", got, err)
	}
	// file: missing fails closed.
	if _, err := resolveDSNRef(ctx, "--dsn", "file:/no/such/db.dsn", getenv); err == nil {
		t.Fatal("file ref to a missing file should fail")
	}
	// A store-backed scheme is refused early (the store is the database we open).
	if _, err := resolveDSNRef(ctx, "--dsn", "store:db-password", getenv); err == nil {
		t.Fatal("store: reference for a DSN should be refused before the store opens")
	}
	if _, err := resolveDSNRef(ctx, "--dsn", "vault:secret/db#dsn", getenv); err == nil {
		t.Fatal("vault: reference for a DSN should be refused before the store opens")
	}
}
