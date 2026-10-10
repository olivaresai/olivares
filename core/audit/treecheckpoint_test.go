// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strconv"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func treeFixture(t *testing.T, events int) (store.Store, model.TenantID, *audit.Signer, ed25519.PublicKey) {
	t.Helper()
	st := testStore(t)
	tenant := provisionTenant(t, st)
	appendEvents(t, st, tenant, events)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	return st, tenant, signer, pub
}

func withLog(t *testing.T, st store.Store, tenant model.TenantID, fn func(store.AuditLog)) {
	t.Helper()
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		fn(sc.Audit())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTreeCheckpointRoundTripsAsC2SPNote(t *testing.T) {
	ctx := context.Background()
	st, tenant, signer, _ := treeFixture(t, 9)
	signed, head, ok, err := signer.TreeCheckpoint(ctx, st, tenant)
	if err != nil || !ok {
		t.Fatalf("TreeCheckpoint ok=%v err=%v", ok, err)
	}
	pub := signer.PublicKey()
	// C2SP tlog-checkpoint: origin, size, root as three lines, then a blank line
	// and the signature line "— <origin> <base64 keyhash+signature>".
	body, sig, found := strings.Cut(string(signed), "\n\n")
	if !found || !strings.HasPrefix(sig, "— "+audit.TreeOrigin(tenant)+" ") {
		t.Fatalf("not a signed note: %q", signed)
	}
	if lines := strings.Split(body, "\n"); len(lines) != 3 || lines[0] != audit.TreeOrigin(tenant) {
		t.Fatalf("checkpoint body = %q, want origin, size, root", body)
	}
	cp, err := audit.VerifyTreeCheckpoint(signed, tenant, pub)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if cp.Size != head.Size || !bytes.Equal(cp.Root, head.Root) {
		t.Fatalf("verified checkpoint = %d %x, want %d %x", cp.Size, cp.Root, head.Size, head.Root)
	}
	withLog(t, st, tenant, func(log store.AuditLog) {
		if err := audit.VerifyTreeAgainstLedger(ctx, log, cp); err != nil {
			t.Fatalf("a fresh checkpoint must match its own ledger: %v", err)
		}
	})
}

func TestTreeCheckpointRefusesWhatItDoesNotVouchFor(t *testing.T) {
	ctx := context.Background()
	st, tenant, signer, _ := treeFixture(t, 4)
	pub := signer.PublicKey()
	signed, head, _, err := signer.TreeCheckpoint(ctx, st, tenant)
	if err != nil {
		t.Fatal(err)
	}
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	otherTenant := provisionTenant(t, st)

	edited := []byte(strings.Replace(string(signed), "\n"+strconv.FormatInt(head.Size, 10)+"\n", "\n"+strconv.FormatInt(head.Size+1, 10)+"\n", 1))
	if bytes.Equal(edited, signed) {
		t.Fatal("fixture did not edit the size line")
	}
	for name, run := range map[string]func() error{
		"a size edited after signing": func() error {
			_, err := audit.VerifyTreeCheckpoint(edited, tenant, pub)
			return err
		},
		"another key": func() error {
			_, err := audit.VerifyTreeCheckpoint(signed, tenant, otherPub)
			return err
		},
		"another tenant": func() error {
			_, err := audit.VerifyTreeCheckpoint(signed, otherTenant, pub)
			return err
		},
		"no signature": func() error {
			body, _, _ := strings.Cut(string(signed), "\n\n")
			_, err := audit.VerifyTreeCheckpoint([]byte(body+"\n"), tenant, pub)
			return err
		},
	} {
		if err := run(); err == nil {
			t.Errorf("%s verified, want a refusal", name)
		}
	}
}

func TestTreeCheckpointDetectsRewrittenAndRemovedHistory(t *testing.T) {
	ctx := context.Background()
	st, tenant, signer, _ := treeFixture(t, 6)
	pub := signer.PublicKey()
	signed, _, _, err := signer.TreeCheckpoint(ctx, st, tenant)
	if err != nil {
		t.Fatal(err)
	}
	cp, err := audit.VerifyTreeCheckpoint(signed, tenant, pub)
	if err != nil {
		t.Fatal(err)
	}
	// Grown ledger: the checkpoint still holds for its first cp.Size events.
	appendEvents(t, st, tenant, 5)
	withLog(t, st, tenant, func(log store.AuditLog) {
		if err := audit.VerifyTreeAgainstLedger(ctx, log, cp); err != nil {
			t.Fatalf("an older checkpoint must still hold for a grown ledger: %v", err)
		}
	})
	// Same number of events, different history: the ledger of another run.
	rewritten := testStore(t)
	rewrittenTenant := provisionTenant(t, rewritten)
	appendEvents(t, rewritten, rewrittenTenant, int(cp.Size)-1)
	withLog(t, rewritten, rewrittenTenant, func(log store.AuditLog) {
		err := audit.VerifyTreeAgainstLedger(ctx, log, cp)
		if err == nil || !strings.Contains(err.Error(), "rewritten") {
			t.Fatalf("a different history verified or was misnamed: %v", err)
		}
	})
	// Fewer events than the checkpoint commits to.
	short := testStore(t)
	shortTenant := provisionTenant(t, short)
	appendEvents(t, short, shortTenant, 2)
	withLog(t, short, shortTenant, func(log store.AuditLog) {
		err := audit.VerifyTreeAgainstLedger(ctx, log, cp)
		if err == nil || !strings.Contains(err.Error(), "removed") {
			t.Fatalf("a truncated ledger verified or was misnamed: %v", err)
		}
	})
}

func TestTreeInclusionProvesOneEventOffline(t *testing.T) {
	ctx := context.Background()
	st, tenant, signer, _ := treeFixture(t, 10)
	signed, head, _, err := signer.TreeCheckpoint(ctx, st, tenant)
	if err != nil {
		t.Fatal(err)
	}
	cp, err := audit.VerifyTreeCheckpoint(signed, tenant, signer.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	appendEvents(t, st, tenant, 3) // the proof is for the checkpoint's size, not the current one
	for seq := int64(1); seq <= head.Size; seq++ {
		var proof audit.TreeInclusion
		var found bool
		withLog(t, st, tenant, func(log store.AuditLog) {
			var err error
			proof, found, err = audit.ProveInclusion(ctx, log, seq, head.Size)
			if err != nil {
				t.Fatal(err)
			}
		})
		if !found {
			t.Fatalf("no proof for seq %d", seq)
		}
		if err := audit.VerifyInclusion(proof, cp); err != nil {
			t.Fatalf("seq %d: %v", seq, err)
		}
		forged := proof
		forged.EventHash = bytes.Repeat([]byte{0xA5}, 32)
		if err := audit.VerifyInclusion(forged, cp); err == nil {
			t.Fatalf("seq %d: a proof for an event that was not sealed verified", seq)
		}
	}
	withLog(t, st, tenant, func(log store.AuditLog) {
		if _, found, err := audit.ProveInclusion(ctx, log, head.Size+1, head.Size); err != nil || found {
			t.Fatalf("an event past the checkpoint has a proof: found=%v err=%v", found, err)
		}
	})
}
