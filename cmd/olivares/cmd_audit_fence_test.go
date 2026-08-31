// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"log/slog"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestAuditVerifyEventPubkeyFencing is the E2 CLI proof: a pinned
// --event-pubkey with an @<last_seq> boundary FENCES that key to its epoch, so an
// event beyond the boundary no longer verifies against it. Before the pin fed
// a flat set and the boundary was ignored — any pinned key verified any sequence.
func TestAuditVerifyEventPubkeyFencing(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	eng, err := boot(ctx, bootConfig{DataDir: dir, Engine: "sqlite", Logger: slog.Default(), DemoSeed: true})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	if eng.demoTenant.IsZero() {
		t.Fatal("no demo tenant seeded")
	}
	// Extend the demo tenant's chain to a known length so the fence boundary is
	// unambiguous (the demo seed alone leaves only 2 events).
	if err := eng.store.Mutate(ctx, eng.demoTenant, func(sc store.Scope) error {
		for i := 0; i < 4; i++ {
			if _, aerr := sc.Audit().Append(ctx, model.AuditDraft{
				Actor: "user:x", ActorKind: "user", Action: "agent.create", TargetKind: "core.agent",
			}); aerr != nil {
				return aerr
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("extend demo chain: %v", err)
	}
	if _, ok, err := eng.signer.Checkpoint(ctx, eng.store, eng.demoTenant); err != nil || !ok {
		t.Fatalf("checkpoint demo tenant: ok=%v err=%v", ok, err)
	}
	tenant := eng.demoTenant.String()
	onbox := base64.StdEncoding.EncodeToString(eng.signer.PublicKey())
	_ = eng.Close()

	run := func(args ...string) (string, error) {
		cmd := newAuditCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetContext(ctx)
		cmd.SetArgs(args)
		e := cmd.Execute()
		return out.String(), e
	}

	// Baseline: the UNBOUNDED current-key pin verifies the whole chain (the demo
	// events were signed on-box) — so any rejection below is the FENCE, not the key.
	out, err := run("verify", "--tenant", tenant, "--data-dir", dir, "--event-pubkey", onbox, "--strict")
	if err != nil {
		t.Fatalf("unbounded event-pubkey pin should verify the chain: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"fenced": true`) {
		t.Errorf("event_keys should report fenced=true when a pin is given\n%s", out)
	}

	// Fence the same key to seq <= 3 (the chain now has 6 events). Real events
	// beyond seq 3 have NO covering key, so the per-event check fails.
	out, err = run("verify", "--tenant", tenant, "--data-dir", dir, "--event-pubkey", onbox+"@3")
	if err != nil {
		t.Fatalf("verify without --strict must exit 0 even on a failed check: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"OK": false`) {
		t.Errorf("a fenced key must reject events beyond its boundary (OK:false expected)\n%s", out)
	}
	// --strict turns the fenced rejection into a non-zero exit.
	if _, serr := run("verify", "--tenant", tenant, "--data-dir", dir, "--event-pubkey", onbox+"@3", "--strict"); serr == nil {
		t.Error("verify --strict with a key fenced below the tail must exit non-zero")
	}
}

func TestParseEventPubKeySpec(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	b64 := base64.StdEncoding.EncodeToString(pub)

	// Bare key = current (unbounded).
	if fk, err := parseEventPubKeySpec(b64); err != nil || fk.FirstSeq != 0 || fk.LastSeq != 0 {
		t.Fatalf("bare key = %+v err=%v (want unbounded)", fk, err)
	}
	// @last_seq = retired generation upper bound.
	if fk, err := parseEventPubKeySpec(b64 + "@100"); err != nil || fk.FirstSeq != 0 || fk.LastSeq != 100 {
		t.Fatalf("@100 = %+v err=%v (want LastSeq=100)", fk, err)
	}
	// @lo:hi = explicit window.
	if fk, err := parseEventPubKeySpec(b64 + "@50:100"); err != nil || fk.FirstSeq != 50 || fk.LastSeq != 100 {
		t.Fatalf("@50:100 = %+v err=%v (want [50,100])", fk, err)
	}
	// Invalid forms are loud.
	for _, bad := range []string{b64 + "@0", b64 + "@-1", b64 + "@100:50", b64 + "@abc", "not-base64@10", b64 + "@10:"} {
		if _, err := parseEventPubKeySpec(bad); err == nil {
			t.Errorf("parseEventPubKeySpec(%q) accepted an invalid spec", bad)
		}
	}
}
