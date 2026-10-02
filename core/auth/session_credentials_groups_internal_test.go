// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"errors"
	"github.com/olivaresai/olivares/core/model"
	"testing"
)

func TestSessionGroupClosureComparesSubjectsAsASet(t *testing.T) {
	tenant := model.TenantID(model.NewID())
	scope := SessionScope{TenantID: tenant, WorkspaceID: model.NewID(), SessionRef: "session", RunRef: "run"}
	for _, tc := range []struct {
		name            string
		launch, current []string
		changed         bool
	}{
		{"same", []string{"a", "b"}, []string{"a", "b"}, false},
		{"reordered", []string{"a", "b"}, []string{"b", "a"}, false},
		{"duplicate", []string{"a", "b"}, []string{"a", "a", "b"}, false},
		{"added", []string{"a"}, []string{"a", "b"}, true},
		{"removed", []string{"a", "b"}, []string{"a"}, true},
		{"replaced", []string{"a"}, []string{"b"}, true},
		{"empty", nil, []string{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user, credential := model.NewID(), model.NewID()
			launch := newPrincipal(KindUser, user, credential, false, "launcher", map[model.TenantID]string{tenant: RoleEditor}, map[model.TenantID][]string{tenant: tc.launch})
			current := newPrincipal(KindUser, user, credential, false, "launcher", map[model.TenantID]string{tenant: RoleOwner}, map[model.TenantID][]string{tenant: tc.current})
			narrowed, err := narrowSessionPrincipal(current, launch, scope)
			if tc.changed {
				if !errors.Is(err, ErrSessionAccessChanged) || !errors.Is(err, ErrUnauthenticated) {
					t.Fatalf("changed closure=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("identical closure refused: %v", err)
			}
			if role, _ := narrowed.RoleIn(tenant); role != RoleEditor {
				t.Fatal("identical closure widened the role ceiling")
			}
		})
	}
}
