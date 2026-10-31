// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"testing"

	"golang.org/x/mod/sumdb/note"

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

func TestTreeCheckpointPublicationRemainsSigned(t *testing.T) {
	st, tenant, signer, pub := treeFixture(t, 9)
	signed, head, ok, err := signer.TreeCheckpoint(t.Context(), st, tenant)
	if err != nil || !ok {
		t.Fatalf("TreeCheckpoint ok=%v err=%v", ok, err)
	}
	key, err := audit.TreeVerifierKey(tenant, pub)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := note.NewVerifier(key)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := note.Open(signed, note.VerifierList(verifier))
	if err != nil {
		t.Fatalf("published checkpoint signature: %v", err)
	}
	want := fmt.Sprintf("%s\n%d\n%s\n", audit.TreeOrigin(tenant), head.Size, base64.StdEncoding.EncodeToString(head.Root))
	if head.Size < 9 || opened.Text != want {
		t.Fatalf("checkpoint = %q, size %d", opened.Text, head.Size)
	}
	if !bytes.Contains(signed, []byte("— "+audit.TreeOrigin(tenant)+" ")) {
		t.Fatal("checkpoint has no tenant signature line")
	}
}
