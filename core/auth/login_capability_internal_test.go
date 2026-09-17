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

// TestLoginComponentAbsent_LateCreateSeamRefusesAndPoisons pins the R5 defense at the
// session create seam: a caller that reaches mintSessionTx after the transaction took
// the audit lock gets the store's lock-order error, and the transaction does not commit
// even though the callback discards that error. It needs the store's login capability
// port (migration 13).
func TestLoginComponentAbsent_LateCreateSeamRefusesAndPoisons(t *testing.T) {
	ctx := context.Background()
	st, err := sqlstore.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	a := NewAuthenticator(st, nil)
	user, err := a.BootstrapSuperadmin(ctx, "late-order-root@example.com", "bootstrap-pass-123")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	a.WithLoginComponentState(LoginComponentAbsent)
	beforeSessions, beforeSeq := lateOrderState(t, st, user.ID)

	var seamErr error
	mutateErr := st.AuthMutate(ctx, func(as store.AuthScope) error {
		if _, err := as.Audit().Append(ctx, model.AuditDraft{
			Actor: "user:" + user.ID.String(), ActorKind: model.ActorUser, Action: "auth.r5.late_order_probe",
		}); err != nil {
			return err
		}
		_, _, seamErr = a.mintSessionTx(ctx, as, user, "127.0.0.1", "auth.login", []string{"pwd"})
		return nil // discarded on purpose: the store must still refuse to commit
	})
	if !errors.Is(seamErr, store.ErrLoginCapabilityLockOrder) {
		t.Fatalf("late mintSessionTx = %v, want store.ErrLoginCapabilityLockOrder", seamErr)
	}
	if mutateErr == nil {
		t.Fatal("the transaction committed after a discarded capability lock-order error")
	}
	afterSessions, afterSeq := lateOrderState(t, st, user.ID)
	if afterSessions != beforeSessions || afterSeq != beforeSeq {
		t.Fatalf("the refused transaction left sessions %d -> %d and audit seq %d -> %d",
			beforeSessions, afterSessions, beforeSeq, afterSeq)
	}
}

func lateOrderState(t *testing.T, st store.Store, userID model.ID) (int, int64) {
	t.Helper()
	ctx := context.Background()
	var (
		sessions int
		seq      int64
	)
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		rows, _, err := as.Sessions().List(ctx, byEq("user_id", userID.String(), 100))
		if err != nil {
			return err
		}
		head, _, err := as.Audit().Head(ctx)
		if err != nil {
			return err
		}
		sessions, seq = len(rows), head.Seq
		return nil
	}); err != nil {
		t.Fatalf("read late-order state: %v", err)
	}
	return sessions, seq
}
