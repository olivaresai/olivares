// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestClassifyBindTokenConflictSurfacesUnrelatedConflict pins FIX 5: a token-update
// version conflict during a bind is ErrPEPCredentialBound ONLY when a binding for
// the token now exists. Any other conflict (e.g. a concurrent revocation bumping
// the token version) must surface unchanged, deny-closed — never be mis-reported
// as already-bound.
//
// Note: a genuine mid-transaction token-update conflict is not deterministically
// reproducible on SQLite (a single writer serializes AuthMutate transactions), so
// the reclassification is pinned here at the seam — the pure classifier plus the
// store-backed reload helper it consults — rather than through an end-to-end race.
func TestClassifyBindTokenConflictSurfacesUnrelatedConflict(t *testing.T) {
	// No binding → the conflict is unrelated and must be surfaced unchanged.
	if got := classifyBindTokenConflict(store.ErrConflict, false); !errors.Is(got, store.ErrConflict) {
		t.Errorf("no-binding conflict = %v, want store.ErrConflict surfaced", got)
	}
	if errors.Is(classifyBindTokenConflict(store.ErrConflict, false), ErrPEPCredentialBound) {
		t.Error("no-binding conflict must not be classified as ErrPEPCredentialBound")
	}
	// A binding exists → a genuine competing bind.
	if got := classifyBindTokenConflict(store.ErrConflict, true); !errors.Is(got, ErrPEPCredentialBound) {
		t.Errorf("competing-bind conflict = %v, want ErrPEPCredentialBound", got)
	}
}

// TestPepTokenHasBindingReflectsStore verifies the reload helper the
// reclassification consults: false when no pep_service_credentials row maps the
// token, true once one does.
func TestPepTokenHasBindingReflectsStore(t *testing.T) {
	ctx := context.Background()
	st, err := sqlstore.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: ":memory:", Debug: true,
	}, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	a := NewAuthenticator(st, nil)
	if _, err := a.BootstrapSuperadmin(ctx, "pep-bind-root@example.com", "bootstrap-pass-123"); err != nil {
		t.Fatalf("bootstrap token owner: %v", err)
	}
	loginToken, _, err := a.Login(ctx, "pep-bind-root@example.com", "bootstrap-pass-123", "127.0.0.1")
	if err != nil {
		t.Fatalf("login token owner: %v", err)
	}
	owner, err := a.Authenticate(ctx, loginToken)
	if err != nil {
		t.Fatalf("authenticate token owner: %v", err)
	}
	_, token, err := a.IssueToken(ctx, owner, TokenSpec{Name: "bind-pep-token", Superadmin: true})
	if err != nil {
		t.Fatalf("issue PEP token parent: %v", err)
	}
	tokenID := token.ID

	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		bound, err := pepTokenHasBinding(ctx, as, tokenID)
		if err != nil {
			return err
		}
		if bound {
			t.Error("pepTokenHasBinding = true with no binding, want false")
		}
		return nil
	}); err != nil {
		t.Fatalf("view before binding: %v", err)
	}

	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		svc, err := as.PEPServices().Create(ctx, model.PEPService{Name: "bind-pep"})
		if err != nil {
			return err
		}
		_, err = as.PEPServiceCredentials().Create(ctx, model.PEPServiceCredential{
			ServiceID: svc.ID, TokenID: tokenID,
		})
		return err
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}

	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		bound, err := pepTokenHasBinding(ctx, as, tokenID)
		if err != nil {
			return err
		}
		if !bound {
			t.Error("pepTokenHasBinding = false after a binding exists, want true")
		}
		return nil
	}); err != nil {
		t.Fatalf("view after binding: %v", err)
	}
}
