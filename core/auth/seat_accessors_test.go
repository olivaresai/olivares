// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// SeatLimit reports the figure the WIRED policy ADVERTISES, for display only (the
// Console edition panel). Since B10 nothing enforces it: nil and the community
// policy both report unlimited, and even an explicit figure is display-only
// (TestSeatCap_FinitePolicyNeverGates proves it cannot refuse an account).
func TestSeatLimitReportsWiredPolicy(t *testing.T) {
	// nil policy → unlimited.
	a := auth.NewAuthenticator(testStore(t), nil)
	if limit, ok := a.SeatLimit(); ok || limit > 0 {
		t.Fatalf("nil policy SeatLimit = (%d,%v), want unlimited (0,false)", limit, ok)
	}
	// community policy → unlimited too (B10 removed the cap of 3).
	a = auth.NewAuthenticator(testStore(t), nil).WithSeatPolicy(auth.NewCommunitySeatPolicy())
	if limit, ok := a.SeatLimit(); ok || limit > 0 {
		t.Fatalf("community SeatLimit = (%d,%v), want unlimited (0,false)", limit, ok)
	}
	// an explicit figure → reported verbatim, for display.
	a = auth.NewAuthenticator(testStore(t), nil).WithSeatPolicy(stubSeatPolicy{limit: 25, ok: true})
	if limit, ok := a.SeatLimit(); !ok || limit != 25 {
		t.Fatalf("advertised SeatLimit = (%d,%v), want (25,true)", limit, ok)
	}
}

// ActiveUserCount counts active accounts up to a bound, reporting whether it was
// capped at the bound — the console's active-user usage display.
func TestActiveUserCount(t *testing.T) {
	a := auth.NewAuthenticator(testStore(t), nil) // nil policy: uncapped, so we can seed freely
	ctx, actor := context.Background(), fedTestActor()
	bootstrapAdmin(t, a) // 1 active
	for i := 0; i < 4; i++ {
		if _, err := a.CreateUser(ctx, actor, auth.NewUser{Email: fmt.Sprintf("u%d@acme.test", i), DisplayName: "U", Password: "password-1x"}); err != nil {
			t.Fatalf("create user %d: %v", i, err)
		}
	}
	// 5 active accounts total.
	count, capped, err := a.ActiveUserCount(ctx, 100)
	if err != nil {
		t.Fatalf("ActiveUserCount: %v", err)
	}
	if count != 5 || capped {
		t.Fatalf("count = (%d, capped=%v), want (5, false)", count, capped)
	}
	// A bound BELOW the population reports the bound + capped (the "N+" / over-cap signal).
	count, capped, err = a.ActiveUserCount(ctx, 3)
	if err != nil {
		t.Fatalf("ActiveUserCount(3): %v", err)
	}
	if count != 3 || !capped {
		t.Fatalf("bounded count = (%d, capped=%v), want (3, true)", count, capped)
	}
}
