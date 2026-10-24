// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A session's read names only accounts the tenant holds: a member's display name comes
// back, a stranger's does not, and an account is never named by its email.
func TestSessionFactsUserNamesAnswerOnlyForTenantMembers(t *testing.T) {
	f := newDirectoryEligibilityFixture(t)
	member := f.createUser(t, model.StatusActive)
	f.grantMembership(t, member, model.ID(""))
	stranger := f.createUser(t, model.StatusActive)
	var unnamed model.ID
	if err := f.st.AuthMutate(context.Background(), func(sc store.AuthScope) error {
		user, err := sc.Users().Create(context.Background(), model.User{Email: model.NewID().String() + "@directory.test", Status: model.StatusActive})
		unnamed = user.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.grantMembership(t, unnamed, model.ID(""))
	source := sessionFactsSource{st: f.st}
	got, err := source.UserNames(context.Background(), f.tenant, []string{member.String(), stranger.String(), unnamed.String()})
	if err != nil {
		t.Fatal(err)
	}
	// A member with no display name is answered with an empty name, never its email, so
	// the read says the name is missing rather than that the account is gone.
	if name, ok := got[unnamed.String()]; len(got) != 2 || got[member.String()] != "user" || !ok || name != "" {
		t.Fatalf("UserNames = %v, want the member's display name and an empty name for the unnamed member", got)
	}
	if none, err := source.UserNames(context.Background(), f.tenant, nil); err != nil || len(none) != 0 {
		t.Fatalf("empty ask = %v, %v", none, err)
	}
}
