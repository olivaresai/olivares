// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/internal/store/canon"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func testStore(t *testing.T) store.Store {
	t.Helper()
	st, err := sqlstore.Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

var tenantSeq int

func provisionTenant(t *testing.T, st store.Store) model.TenantID {
	t.Helper()
	tenantSeq++
	slug := "t" + strconv.Itoa(tenantSeq)
	var id model.TenantID
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		o, err := sys.CreateOrg(context.Background(), model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
		id = o.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func appendEvents(t *testing.T, st store.Store, tenant model.TenantID, n int) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		for i := 0; i < n; i++ {
			if _, err := sc.Audit().Append(context.Background(), model.AuditDraft{
				Actor: "user:x", ActorKind: "user", Action: "agent.create", TargetKind: "core.agent",
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
}

func TestCheckpointSignVerify(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	tenant := provisionTenant(t, st) // seeds 1 audit event (org.create)
	appendEvents(t, st, tenant, 3)   // seqs 2,3,4

	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	ev, ok, err := signer.Checkpoint(ctx, st, tenant)
	if err != nil || !ok {
		t.Fatalf("checkpoint = (%v,%v)", ok, err)
	}
	if ev.Action != audit.ActionCheckpoint || len(ev.Sig) == 0 {
		t.Fatalf("checkpoint event = %+v", ev)
	}

	verify := func(p ed25519.PublicKey) audit.CheckpointReport {
		var rep audit.CheckpointReport
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			r, e := audit.VerifyCheckpoints(ctx, sc.Audit(), p)
			rep = r
			return e
		}); err != nil {
			t.Fatalf("verify: %v", err)
		}
		return rep
	}

	rep := verify(pub)
	if !rep.OK || rep.Checkpoints != 1 || rep.LatestAttestedSeq != 4 {
		t.Fatalf("good verify = %+v", rep)
	}

	// The wrong public key must reject the checkpoint signature.
	otherPub, _, _ := ed25519.GenerateKey(nil)
	if bad := verify(otherPub); bad.OK || bad.Reason != "checkpoint-sig-invalid" {
		t.Fatalf("wrong-key verify = %+v", bad)
	}

	// A second checkpoint after more events is also verified.
	appendEvents(t, st, tenant, 2)
	if _, _, err := signer.Checkpoint(ctx, st, tenant); err != nil {
		t.Fatal(err)
	}
	if rep := verify(pub); !rep.OK || rep.Checkpoints != 2 {
		t.Fatalf("two-checkpoint verify = %+v", rep)
	}
}

func TestCheckpointAll(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	// Provision the system org and two tenants, each with events.
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	t1 := provisionTenant(t, st)
	t2 := provisionTenant(t, st)
	appendEvents(t, st, t1, 2)
	appendEvents(t, st, t2, 1)

	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)
	if err := signer.CheckpointAll(ctx, st); err != nil {
		t.Fatalf("checkpoint all: %v", err)
	}
	for _, tn := range []model.TenantID{t1, t2, model.SystemTenantID} {
		if err := st.View(ctx, tn, func(sc store.Scope) error {
			rep, e := audit.VerifyCheckpoints(ctx, sc.Audit(), pub)
			if e != nil {
				return e
			}
			if !rep.OK || rep.Checkpoints < 1 {
				t.Errorf("tenant %s checkpoints = %+v", tn, rep)
			}
			return nil
		}); err != nil {
			t.Fatalf("verify %s: %v", tn, err)
		}
	}
}

func fixtureEvent() model.AuditEvent {
	return model.AuditEvent{
		ID:         "11111111-1111-7111-8111-111111111111",
		TenantID:   "22222222-2222-7222-8222-222222222222",
		Seq:        7,
		OccurredAt: model.NewTimestamp(time.Unix(1700000000, 0).UTC()),
		Actor:      "user:abc",
		ActorKind:  "user",
		Action:     "agent.create",
		TargetKind: "core.agent",
		TargetID:   "33333333-3333-7333-8333-333333333333",
		// Real widths, not three bytes: a sealed ledger event carries 32-byte digests
		// and a 64-byte Ed25519 signature, and the export now refuses anything else.
		// The leading bytes are kept so the assertions that look for them still read
		// the same, and the signature is full width ON PURPOSE — its base64 ends in
		// "==", which is the padding that exposes whether a dialect's escaping
		// survives a round trip.
		MetaCommitment: canon.MetaDigest("{}"),
		PrevHash:       fixedWidth(32, 0x01, 0x02, 0x03),
		Hash:           fixedWidth(32, 0x0a, 0x0b, 0x0c),
		Sig:            fixedWidth(64, 0xff, 0xee),
	}
}

// fixedWidth builds a deterministic n-byte fixture value with the given prefix.
func fixedWidth(n int, prefix ...byte) []byte {
	out := make([]byte, n)
	copy(out, prefix)
	return out
}

func TestExportDeterministicAndCarriesIntegrity(t *testing.T) {
	ev := fixtureEvent()
	for _, f := range []audit.Format{audit.FormatCEF, audit.FormatSyslog, audit.FormatOTLP, audit.FormatOTLPLogRecord} {
		a, err := audit.FormatEvent(ev, f)
		if err != nil {
			t.Fatalf("format %s: %v", f, err)
		}
		b, _ := audit.FormatEvent(ev, f)
		if a != b {
			t.Fatalf("format %s not deterministic", f)
		}
		// Every format must carry the chain hash (hex) and the sequence.
		if !strings.Contains(a, "0a0b0c") || !strings.Contains(a, "7") {
			t.Fatalf("format %s missing integrity fields: %s", f, a)
		}
	}

	cef, _ := audit.FormatEvent(ev, audit.FormatCEF)
	if !strings.HasPrefix(cef, "CEF:0|Olivares|ControlPlane|") || !strings.Contains(cef, "olvHash=0a0b0c") {
		t.Fatalf("CEF = %s", cef)
	}
	sl, _ := audit.FormatEvent(ev, audit.FormatSyslog)
	// The hash is the full 32-byte digest in hex, so the assertion anchors on the
	// SD-PARAM key and the digest's leading bytes rather than on a whole short value.
	if !strings.HasPrefix(sl, "<134>1 ") || !strings.Contains(sl, ` hash="0a0b0c`) {
		t.Fatalf("syslog = %s", sl)
	}
	// otlp is the request envelope since the catalog remap; the bare LogRecord
	// projection lives under otlp_log_record with its bytes unchanged.
	otlp, _ := audit.FormatEvent(ev, audit.FormatOTLP)
	var env map[string]any
	if err := json.Unmarshal([]byte(otlp), &env); err != nil {
		t.Fatalf("OTLP not valid JSON: %v (%s)", err, otlp)
	}
	if _, ok := env["resourceLogs"]; !ok {
		t.Fatalf("OTLP must be a request envelope since the catalog remap: %s", otlp)
	}
	bare, _ := audit.FormatEvent(ev, audit.FormatOTLPLogRecord)
	var rec map[string]any
	if err := json.Unmarshal([]byte(bare), &rec); err != nil {
		t.Fatalf("OTLP log record not valid JSON: %v (%s)", err, bare)
	}
	if rec["severityText"] != "INFO" {
		t.Fatalf("OTLP log record = %s", bare)
	}
	if !audit.ValidFormat(audit.FormatCEF) || audit.ValidFormat("bogus") {
		t.Fatal("ValidFormat wrong")
	}
}
