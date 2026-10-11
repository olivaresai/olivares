// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestRecordAPITokenUseWritesOnlyLastUsedAt runs the targeted write on SQLite
// and PostgreSQL: it sets last_used_at and leaves version and updated_at as they
// were, so a credential pinned to the token's version survives a recorded use;
// a read-only view refuses it and an absent token is ErrNotFound.
func TestRecordAPITokenUseWritesOnlyLastUsedAt(t *testing.T) {
	for _, engine := range []struct {
		name string
		open func(*testing.T) store.Store
	}{
		{name: "sqlite", open: func(t *testing.T) store.Store { return openSQLiteTest(t, nil) }},
		{name: "postgres", open: func(t *testing.T) store.Store {
			st, err := Open(context.Background(), store.Config{
				Engine: store.EnginePostgres, DSN: isolatedPG(t).App,
			}, nil)
			if err != nil {
				t.Fatalf("open postgres: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })
			return st
		}},
	} {
		t.Run(engine.name, func(t *testing.T) { testRecordAPITokenUse(t, engine.open(t)) })
	}
}

func testRecordAPITokenUse(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	provisionTenant(t, st, "token-use")
	var token model.APIToken
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		user, err := as.Users().Create(ctx, model.User{
			Email: "token-use@example.test", Status: model.StatusActive,
		})
		if err != nil {
			return err
		}
		token, err = as.Tokens().Create(ctx, model.APIToken{
			Name: "token-use", UserID: user.ID,
			Selector: "token-use", SecretHash: []byte("token-use-hash"),
		})
		return err
	}); err != nil {
		t.Fatalf("seed token: %v", err)
	}

	usedAt := model.NewTimestamp(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		return as.RecordAPITokenUse(ctx, token.ID, usedAt)
	}); err != nil {
		t.Fatalf("record use: %v", err)
	}
	var got model.APIToken
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		if err := as.RecordAPITokenUse(ctx, token.ID, usedAt); !errors.Is(err, store.ErrReadOnly) {
			t.Errorf("record use in a read-only view = %v, want ErrReadOnly", err)
		}
		var err error
		got, err = as.Tokens().Get(ctx, token.ID)
		return err
	}); err != nil {
		t.Fatalf("read token: %v", err)
	}
	if got.LastUsedAt == nil || !got.LastUsedAt.Time().Equal(usedAt.Time()) {
		t.Errorf("last_used_at = %v, want %v", got.LastUsedAt, usedAt)
	}
	if got.Version != token.Version || !got.UpdatedAt.Time().Equal(token.UpdatedAt.Time()) {
		t.Errorf("version/updated_at = %d/%v, want unchanged %d/%v",
			got.Version, got.UpdatedAt, token.Version, token.UpdatedAt)
	}

	err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		return as.RecordAPITokenUse(ctx, model.NewID(), usedAt)
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("record use of an absent token = %v, want ErrNotFound", err)
	}
}
