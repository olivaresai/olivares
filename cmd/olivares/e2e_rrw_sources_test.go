// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// e2e_rrw_sources_test.go is the (FASE P / B1) end-to-end proof that the R/RW
// access-map DIFFERENTIAL is configurable in a STOCK serve — not just in the test
// harness. It stands up the REAL engine (the production module set)
// and wires REAL connectors through the PRODUCTION path the binary uses —
// wireSources → buildInProcSource → rt.AddPollSource — pointing pgAudit and
// CloudTrail at on-disk fixtures, exactly as an operator would via
// OLIVARES_SOURCES_CONFIG. It then asserts, over the real HTTP API, that the
// connectors' observed edges land in module III's R/RW graph (clean tier, verbatim
// read/write, the pg_audit / cloudtrail signal) and that the permitted-vs-observed
// drift marks them as unexpected accesses — the moat working from config, end to
// end. It reuses the harness HTTP helpers and edgeByResource from the suite.

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/runtime"
)

// newSourcesHarness uses production boot with ingestion paused until the real
// setup creates its tenant. Boot binds every private module owner before sources
// are registered, as stock serve does; the original source/graph assertions then
// drive wireSources and the runtime with parsed pgAudit and CloudTrail inputs.
func newSourcesHarness(t *testing.T, mkSources func(tenant string) []sourceSpec) *harness {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	prepareCompositionTestBoot(t)
	eng, err := boot(ctx, bootConfig{
		DataDir: dir, Engine: "sqlite", DSN: ":memory:", Version: "e2e-rrw",
		NoIngest: true, Logger: log,
	})
	if err != nil {
		t.Fatalf("boot source harness: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	h := &harness{t: t, h: eng.api.Handler(), rt: eng.rt, st: eng.store, now: time.Now()}

	// Bootstrap through the real HTTP handler before configuring tenant sources.
	tok, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatalf("setup token: %v", err)
	}
	if code, _ := h.req("POST", "/v1/setup", "", "", map[string]any{
		"token": tok, "email": "admin@e2e.test", "password": "supersecret-e2e",
	}); code != http.StatusCreated {
		t.Fatalf("setup = %d", code)
	}
	var login struct {
		Token string `json:"token"`
	}
	if code := h.reqInto("POST", "/v1/auth/login", "", "", map[string]any{
		"email": "admin@e2e.test", "password": "supersecret-e2e",
	}, &login); code != http.StatusOK || login.Token == "" {
		t.Fatalf("login = %d", code)
	}
	h.adminToken = login.Token
	h.tenantA = h.createOrg("Acme Robotics", "acme")
	wireSources(ctx, eng.rt, sourcesConfig{Sources: mkSources(h.tenantA)}, dir, nil, log)
	if err := eng.rt.Start(ctx); err != nil {
		t.Fatalf("runtime start: %v", err)
	}
	for _, cs := range eng.rt.Status() {
		if cs.Status == runtime.StatusFailed {
			t.Fatalf("source/module %q failed to start: %s", cs.Name, cs.Err)
		}
	}
	return h
}

// pgAuditRow builds a PostgreSQL csvlog record carrying a pgAudit AUDIT message,
// placing each value at the column index the connector reads (0 log_time, 1
// user_name, 2 database_name, 13 message, 22 application_name). It is the on-disk
// shape the REAL pg-audit connector parses — no stub.
func pgAuditRow(ts, user, db, msg, app string) []string {
	rec := make([]string, 26)
	rec[0], rec[1], rec[2] = ts, user, db
	rec[11] = "LOG"
	rec[13] = msg
	rec[22] = app
	rec[23] = "client backend"
	return rec
}

// writePgAuditCSVLog writes a csvlog fixture the pg-audit connector reads as a batch.
func writePgAuditCSVLog(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "postgresql.csv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create csvlog: %v", err)
	}
	defer func() { _ = f.Close() }()
	w := csv.NewWriter(f)
	// A firmly-attributed READ and WRITE by a distinguishing application_name (the
	// per-agent bridge), so the edges land `attributed`, not approximate.
	rows := [][]string{
		pgAuditRow("2026-06-03 10:23:45.123 UTC", "claude_rw", "salesdb", "AUDIT: SESSION,1,1,READ,SELECT,TABLE,public.customers", "claude-rw-agent"),
		pgAuditRow("2026-06-03 10:23:46.200 UTC", "claude_rw", "salesdb", "AUDIT: SESSION,2,1,WRITE,INSERT,TABLE,public.orders", "claude-rw-agent"),
	}
	if err := w.WriteAll(rows); err != nil {
		t.Fatalf("write csvlog: %v", err)
	}
	w.Flush()
	if err := w.Error(); err != nil {
		t.Fatalf("flush csvlog: %v", err)
	}
	return path
}

// cloudTrailFixture is a minimal {"Records":[…]} log: a read (GetObject, readOnly)
// and a write (PutObject) to the same object, by a per-user IAM identity.
const cloudTrailFixture = `{"Records":[
 {"eventTime":"2026-06-03T11:00:00Z","eventSource":"s3.amazonaws.com","eventName":"GetObject","readOnly":true,"userIdentity":{"type":"IAMUser","arn":"arn:aws:iam::123456789012:user/ci","accountId":"123456789012","userName":"ci"},"requestParameters":{"bucketName":"artifacts","key":"build/app.tar.gz"},"resources":[{"type":"AWS::S3::Object","ARN":"arn:aws:s3:::artifacts/build/app.tar.gz"}]},
 {"eventTime":"2026-06-03T11:00:05Z","eventSource":"s3.amazonaws.com","eventName":"PutObject","readOnly":false,"userIdentity":{"type":"IAMUser","arn":"arn:aws:iam::123456789012:user/ci","accountId":"123456789012","userName":"ci"},"requestParameters":{"bucketName":"artifacts","key":"build/release.tar.gz"},"resources":[{"type":"AWS::S3::Object","ARN":"arn:aws:s3:::artifacts/build/release.tar.gz"}]}
]}`

// writeCloudTrailLog writes the CloudTrail fixture the s3-cloudtrail connector reads.
func writeCloudTrailLog(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "cloudtrail.json")
	if err := os.WriteFile(path, []byte(cloudTrailFixture), 0o600); err != nil {
		t.Fatalf("write cloudtrail fixture: %v", err)
	}
	return path
}

// TestE2E_RRWSources_StockServeProducesObservedGraph is the DoD's headline: a stock
// serve, configured with the pgAudit and S3/CloudTrail connectors, PRODUCES the R/RW
// observed map. It proves the wiring end to end — the connectors' EdgeObservations
// flow connector → runtime → bus → module III → store → API — with honest
// clean-tier coverage and the permitted-vs-observed drift marking them.
func TestE2E_RRWSources_StockServeProducesObservedGraph(t *testing.T) {
	fixtureDir := t.TempDir()
	pgPath := writePgAuditCSVLog(t, fixtureDir)
	ctPath := writeCloudTrailLog(t, fixtureDir)

	h := newSourcesHarness(t, func(tenant string) []sourceSpec {
		return []sourceSpec{
			{Name: "appdb-pgaudit", Kind: "pgaudit", Tenant: tenant, Config: map[string]string{"log_path": pgPath, "format": "csvlog"}},
			{Name: "artifacts-trail", Kind: "s3cloudtrail", Tenant: tenant, Config: map[string]string{"path": ctPath}},
		}
	})

	const (
		refCustomers = "salesdb.public.customers"
		refOrders    = "salesdb.public.orders"
		refS3Object  = "arn:aws:s3:::artifacts/build/app.tar.gz"
	)

	// The three observed edges materialize in the access graph (the bus is async, so
	// poll the real read surface until it converges, exactly as an operator would).
	var edges []map[string]any
	h.eventually("pgAudit + CloudTrail edges materialize in the R/RW graph", 10*time.Second, func() error {
		g := h.getJSON(h.adminToken, h.tenantA, "/v1/m/accessmap/graph?limit=200")
		edges = items2(g, "edges")
		for _, ref := range []string{refCustomers, refOrders, refS3Object} {
			if edgeByResource(edges, ref) == nil {
				return fmt.Errorf("edge %q not yet ingested (%d edges so far)", ref, len(edges))
			}
		}
		return nil
	})

	// pgAudit READ — clean tier, verbatim read, the pg_audit signal, firmly attributed
	// (distinguishing application_name), observed and NOT permitted (no grant wired).
	cust := edgeByResource(edges, refCustomers)
	assertEq(t, "customers.mode", cust["mode"], "read")
	assertEq(t, "customers.signal_source", cust["signal_source"], "pg_audit")
	assertEq(t, "customers.coverage_tier", cust["coverage_tier"], "clean")
	assertEq(t, "customers.confidence", cust["confidence"], "attributed")
	assertEq(t, "customers.observed", cust["observed"], true)
	assertEq(t, "customers.permitted", cust["permitted"], false)

	// pgAudit WRITE — the read/write classification is verbatim from pgAudit's CLASS.
	orders := edgeByResource(edges, refOrders)
	assertEq(t, "orders.mode", orders["mode"], "write")
	assertEq(t, "orders.signal_source", orders["signal_source"], "pg_audit")
	assertEq(t, "orders.coverage_tier", orders["coverage_tier"], "clean")

	// CloudTrail READ — the cloudtrail signal, clean tier, readOnly taken verbatim.
	s3 := edgeByResource(edges, refS3Object)
	assertEq(t, "s3.mode", s3["mode"], "read")
	assertEq(t, "s3.signal_source", s3["signal_source"], "cloudtrail")
	assertEq(t, "s3.coverage_tier", s3["coverage_tier"], "clean")
	assertEq(t, "s3.observed", s3["observed"], true)

	// The permitted-vs-observed drift marks every observed-but-unpermitted access as
	// UNEXPECTED — the headline security finding the moat exists to produce.
	d := h.getJSON(h.adminToken, h.tenantA, "/v1/m/accessmap/drift?limit=200")
	unexpected := items2(d, "unexpected_accesses")
	if len(unexpected) == 0 {
		t.Fatal("drift has no unexpected accesses; module III did not consume the observed edges")
	}
	flagged := map[string]bool{}
	for _, e := range unexpected {
		edge, _ := e["edge"].(map[string]any)
		if ref, _ := edge["resource_ref"].(string); ref != "" {
			flagged[ref] = true
		}
	}
	for _, want := range []string{refCustomers, refOrders, refS3Object} {
		if !flagged[want] {
			t.Errorf("observed access %q was not flagged as unexpected in /drift", want)
		}
	}
}
