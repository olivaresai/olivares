// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

// This wire-proof runs ONLY under `-tags e2e` against a REAL PostgreSQL, so a plain
// `go test` (and the shared-container gate) never needs a database. CI provides one
// via testdata/docker-compose.e2e.yml and sets:
//
//	PGCONTENT_E2E_ADMIN_DSN — an owner/superuser DSN used ONLY to seed the fixture,
//	PGCONTENT_E2E_RO_DSN    — the least-privilege read-only role the connector uses
//	                          (GRANT SELECT only; testdata/init.sql provisions it).
//
// The test proves, end-to-end against the live database: the connector verifies a
// read-only session at Open, List/Fetch/DeltaList return the seeded rows with their
// mapped ACL/classification, and a write attempted on the read-only session is
// REJECTED by PostgreSQL — the read-only guarantee proven by construction, not asserted.
package pgcontent

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/olivaresai/olivares/sdk"
)

func e2eEnv(t *testing.T) (adminDSN, roDSN string) {
	t.Helper()
	adminDSN = os.Getenv("PGCONTENT_E2E_ADMIN_DSN")
	roDSN = os.Getenv("PGCONTENT_E2E_RO_DSN")
	if adminDSN == "" || roDSN == "" {
		t.Skip("set PGCONTENT_E2E_ADMIN_DSN and PGCONTENT_E2E_RO_DSN to run the pgcontent wire-proof")
	}
	return adminDSN, roDSN
}

func TestE2EReadOnlyIngestAndWriteRejected(t *testing.T) {
	adminDSN, roDSN := e2eEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Seed the fixture with the ADMIN role (the connector never writes).
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatalf("admin connect: %v", err)
	}
	defer func() { _ = admin.Close(ctx) }()
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS pgc_e2e`,
		`CREATE TABLE pgc_e2e (id int PRIMARY KEY, title text, body text, owner_group text, ssn text, updated_at timestamptz DEFAULT now())`,
		`INSERT INTO pgc_e2e (id, title, body, owner_group, ssn) VALUES
			(1, 'Alpha', 'first body', 'eng', '111-11-1111'),
			(2, 'Beta',  'second body', 'sales', NULL)`,
		`GRANT SELECT ON pgc_e2e TO CURRENT_USER`, // ensure the RO role can read (init.sql also grants)
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}

	// Open the connector as the read-only role.
	s := New()
	if err := s.Open(ctx, sdk.Config{Settings: map[string]string{
		fMode:             "live",
		fDSN:              roDSN,
		fSchema:           "public",
		fTable:            "pgc_e2e",
		fKeyColumns:       "id",
		fBodyColumns:      "body",
		fTitleColumn:      "title",
		fACLColumns:       "owner_group",
		fSensitiveColumns: "ssn",
		fUpdatedAtColumn:  "updated_at",
	}}); err != nil {
		t.Fatalf("open (read-only verify happens here): %v", err)
	}
	defer func() { _ = s.Close(ctx) }()

	refs, _, err := s.List(ctx, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("listed %d rows, want 2", len(refs))
	}

	doc, err := s.Fetch(ctx, "postgres:public.pgc_e2e#1")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if doc.Body != "first body" {
		t.Errorf("body = %q", doc.Body)
	}
	if len(doc.ACL) != 1 || doc.ACL[0] != "group:eng" {
		t.Errorf("acl = %v, want [group:eng]", doc.ACL)
	}
	if len(doc.ExternalLabels) != 1 || doc.ExternalLabels[0] != "pii:ssn" {
		t.Errorf("labels = %v, want [pii:ssn]", doc.ExternalLabels)
	}

	// Incremental delta from the beginning returns both rows.
	page, err := s.DeltaList(ctx, "")
	if err != nil {
		t.Fatalf("delta: %v", err)
	}
	if len(page.Changes) != 2 {
		t.Fatalf("delta changes = %d, want 2", len(page.Changes))
	}

	// Discovery sees the table + its columns.
	disc, err := s.Discover(ctx, "public")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	found := false
	for _, tb := range disc.Tables {
		if tb.Name == "pgc_e2e" {
			found = true
		}
	}
	if !found {
		t.Errorf("discovery did not find pgc_e2e")
	}

	// THE read-only proof: a write on the read-only session is rejected by PostgreSQL.
	roConn, err := pgx.Connect(ctx, roDSN)
	if err != nil {
		t.Fatalf("ro connect: %v", err)
	}
	defer func() { _ = roConn.Close(ctx) }()
	if _, err := roConn.Exec(ctx, `SET default_transaction_read_only = on`); err != nil {
		t.Fatalf("set read-only: %v", err)
	}
	if _, err := roConn.Exec(ctx, `INSERT INTO pgc_e2e (id, body) VALUES (99, 'nope')`); err == nil {
		t.Fatal("read-only session ALLOWED a write — the read-only guarantee is broken")
	}
}
