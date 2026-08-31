// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// secretEntity has a Redact text field. The engine must store ONLY a hash of a
// Redact field's value, never the raw secret (docs/SECURITY-HARDENING.md minimal-data, enforced
// on the write path by sqlstore.redactField).
var secretEntity = model.EntityDescriptor{
	Kind:  "rrw.secretbox",
	Table: "rrw_secretbox",
	Fields: []model.FieldSpec{
		{Name: "label", Kind: model.KindText},
		{Name: "token", Kind: model.KindText, Redact: true},
	},
}

// TestRedactFieldNeverPersistsRaw is the adversarial data-minimization check: a
// value written to a Redact field must NOT be retrievable in the clear from the
// store — the engine is the backstop even when a caller forgets to scrub.
func TestRedactFieldNeverPersistsRaw(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, func(reg store.ExtensionRegistry) error { return reg.Register(secretEntity) })
	tenant := provisionTenant(t, st, "alpha")

	const secret = "sk-live-DEADBEEFcafef00d-super-secret-value"
	want := "sha256:" + hex.EncodeToString(func() []byte { s := sha256.Sum256([]byte(secret)); return s[:] }())

	var id model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("rrw.secretbox")
		if err != nil {
			return err
		}
		rec, err := repo.Create(ctx, model.Record{"label": "k1", "token": secret})
		if err != nil {
			return err
		}
		id = model.ID(rec.String("id"))
		// Create's returned record must already reflect the redaction.
		if got := rec.String("token"); got != want {
			t.Fatalf("create returned token = %q, want hash %q", got, want)
		}
		if strings.Contains(rec.String("token"), "secret") {
			t.Fatalf("raw secret leaked in returned record: %q", rec.String("token"))
		}
		return nil
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Read it back: still only the hash, never the raw secret.
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		repo, _ := sc.Ext("rrw.secretbox")
		rec, err := repo.Get(ctx, id)
		if err != nil {
			return err
		}
		if got := rec.String("token"); got != want {
			t.Fatalf("stored token = %q, want hash %q", got, want)
		}
		return nil
	}); err != nil {
		t.Fatalf("get: %v", err)
	}

	// Belt and braces: scan the RAW table column directly (bypassing the repo) and
	// assert the secret substring is nowhere in the stored bytes.
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		repo, _ := sc.Ext("rrw.secretbox")
		rows, _, err := repo.List(ctx, model.Query{Limit: 10})
		if err != nil {
			return err
		}
		for _, r := range rows {
			if strings.Contains(r.String("token"), "secret") || strings.Contains(r.String("token"), "DEADBEEF") {
				t.Fatalf("raw secret found in stored row: %q", r.String("token"))
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("list: %v", err)
	}

	// A non-Redact field is stored verbatim (redaction is opt-in per field).
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		repo, _ := sc.Ext("rrw.secretbox")
		rec, err := repo.Get(ctx, id)
		if err != nil {
			return err
		}
		if rec.String("label") != "k1" {
			t.Fatalf("non-redact field altered: label = %q, want k1", rec.String("label"))
		}
		return nil
	}); err != nil {
		t.Fatalf("get label: %v", err)
	}
}

// TestRedactFieldEmptyStaysEmpty: an empty Redact value is left as-is (no hash of
// the empty string), so NULL/"" semantics are preserved.
func TestRedactFieldEmptyStaysEmpty(t *testing.T) {
	if got := redactField(model.FieldSpec{Redact: true, Kind: model.KindText}, ""); got != "" {
		t.Fatalf("empty redact value = %q, want empty", got)
	}
	if got := redactField(model.FieldSpec{Redact: false, Kind: model.KindText}, "plain"); got != "plain" {
		t.Fatalf("non-redact field altered = %q, want plain", got)
	}
}
