// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// TestDRDrillFromRealBundle: `dr drill --from <bundle>` restores a real bundle
// into a throwaway directory with no network, measures RTO and RPO against the
// operator's targets, and seals the report in the audit ledger of the estate
// that ran it. A missed target still records the report and exits 7.
func TestDRDrillFromRealBundle(t *testing.T) {
	prevVersion := version
	version = "1.900"
	t.Cleanup(func() { version = prevVersion })
	src := t.TempDir()
	seedDataDir(t, src)
	pf := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(pf, []byte("a strong DR passphrase\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A target with a provider and egress configured: the estate holds a provider
	// record whose base URL, like every endpoint the engine can be pointed at, is
	// one loopback server that counts the connections it accepts.
	var accepted atomic.Int64
	endpoint := httptest.NewUnstartedServer(http.NotFoundHandler())
	endpoint.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateNew {
			accepted.Add(1)
		}
	}
	endpoint.Start()
	t.Cleanup(endpoint.Close)
	seedProvider(t, src, endpoint.URL)
	bundle := filepath.Join(t.TempDir(), "estate.drbundle")
	if out, err := runDR(t, "backup", "--data-dir", src, "--engine", "sqlite", "--out", bundle, "--passphrase-file", pf); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}

	for _, k := range []string{
		"OLIVARES_EMBEDDINGS_OPENAI_BASE_URL", "OLIVARES_EMBEDDINGS_VOYAGE_BASE_URL", "OLIVARES_EMBEDDINGS_BASE_URL",
		"OLIVARES_OTEL_ENDPOINT", "OLIVARES_UPDATE_ENDPOINT", "OLIVARES_DR_OFFSITE_ENDPOINT", "OLIVARES_BASE_URL",
	} {
		t.Setenv(k, endpoint.URL)
	}
	// Every outbound connection of a default-transport client is a dial here;
	// the drill must make none.
	var dials atomic.Int64
	prevTransport := http.DefaultTransport
	counted := prevTransport.(*http.Transport).Clone()
	counted.DialContext = func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("the drill must not open a connection")
	}
	http.DefaultTransport = counted
	t.Cleanup(func() { http.DefaultTransport = prevTransport })

	// Seeding the provider booted a full engine, which probes it once: count from here.
	seededConns, seededDials := accepted.Load(), dials.Load()
	out, err := runDR(t, "drill", "--from", bundle, "--data-dir", src, "--passphrase-file", pf,
		"--rto-target", "1h", "--rpo-target", "24h")
	if err != nil {
		t.Fatalf("drill --from: %v\n%s", err, out)
	}
	var rep struct {
		ChainOK       bool   `json:"chain_ok"`
		Network       string `json:"network"`
		Events        int64  `json:"events"`
		RTOMillis     int64  `json:"rto_ms"`
		RTOTargetMS   int64  `json:"rto_target_ms"`
		RTOExceeded   bool   `json:"rto_exceeded"`
		RPOTargetSecs int64  `json:"rpo_target_seconds"`
		RPOExceeded   bool   `json:"rpo_exceeded"`
		LedgerSeq     int64  `json:"ledger_seq"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("report is not JSON: %v\n%s", err, out)
	}
	if !rep.ChainOK || rep.Events < 1 || rep.Network != "none" || rep.RTOMillis <= 0 || rep.RTOExceeded || rep.RPOExceeded ||
		rep.RTOTargetMS != 3_600_000 || rep.RPOTargetSecs != 86_400 || rep.LedgerSeq < 1 {
		t.Fatalf("report = %+v\n%s", rep, out)
	}

	// A target the restore cannot meet: the report is still sealed, the exit is 7.
	out, err = runDR(t, "drill", "--from", bundle, "--data-dir", src, "--passphrase-file", pf, "--rto-target", "1ns")
	if exitcode.From(err) != exitcode.Degraded {
		t.Fatalf("missed RTO target: err=%v (exit %d), want exit %d\n%s", err, exitcode.From(err), exitcode.Degraded, out)
	}
	if msg := err.Error(); !strings.Contains(msg, "rto ") || strings.Contains(msg, "rpo") {
		t.Fatalf("a missed RTO must name only the RTO (the RPO target was not set): %q", msg)
	}

	if d, c := dials.Load()-seededDials, accepted.Load()-seededConns; d != 0 || c != 0 {
		t.Fatalf("the drill reached the network: %d dials, %d connections to the configured endpoints", d, c)
	}

	// Both reports are in the estate's system-tenant ledger and the chain holds.
	prepareCompositionTestBoot(t)
	defer prepareCompositionTestBoot(t)
	ctx := context.Background()
	eng, err := drBoot(ctx, drFlags{dataDir: src, engineKind: string(store.EngineSQLite)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = eng.Close() }()
	var drills int
	if err := eng.store.View(ctx, model.SystemTenantID, func(sc store.Scope) error {
		if err := sc.Audit().Walk(ctx, 0, func(ev model.AuditEvent) error {
			if ev.Action == "dr.drill" {
				drills++
			}
			return nil
		}); err != nil {
			return err
		}
		vr, err := sc.Audit().Verify(ctx, 0)
		if err != nil || !vr.OK {
			t.Fatalf("system chain after the drills: ok=%v err=%v", vr.OK, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if drills != 2 {
		t.Fatalf("dr.drill events in the ledger = %d, want 2", drills)
	}
}

// seedProvider registers a provider record for the seeded organization of dir
// and checkpoints, so the bundle taken afterwards holds a configured provider.
func seedProvider(t *testing.T, dir, baseURL string) {
	t.Helper()
	prepareCompositionTestBoot(t)
	defer prepareCompositionTestBoot(t)
	ctx := context.Background()
	eng, err := boot(ctx, bootConfig{DataDir: dir, Engine: "sqlite", Version: "test", ApplyModuleProfile: true})
	if err != nil {
		t.Fatalf("provider seed boot: %v", err)
	}
	defer func() { _ = eng.Close() }()
	var orgs []model.Org
	if err := eng.store.System(ctx, func(sys store.SystemScope) (e error) { orgs, e = sys.ListOrgs(ctx); return e }); err != nil || len(orgs) == 0 {
		t.Fatalf("list orgs: %v (%d)", err, len(orgs))
	}
	if _, err := eng.sessionsMod.CreateProviderRecord(ctx, orgs[0].TenantID, sessions.CreateProviderRecordInput{
		Kind: "ollama", DisplayName: "local", BaseURL: baseURL,
	}); err != nil {
		t.Fatalf("register provider: %v", err)
	}
	if err := eng.signer.CheckpointAll(ctx, eng.store); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
}
