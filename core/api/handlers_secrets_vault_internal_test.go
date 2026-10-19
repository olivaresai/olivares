// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// An agent session is minted as KindUser with its launcher's UserID, so the owner
// check must refuse it on its own: reaching the vault must not depend on the
// workspace-confinement check in secretScope staying in place.
func TestOwnsVaultItemRefusesAnAgentSession(t *testing.T) {
	id := model.NewID()
	name := auth.PersonalVaultPrefix + id.String() + "/item"
	person := auth.Principal{Kind: auth.KindUser, UserID: id}
	if !ownsVaultItem(person, name) {
		t.Fatal("the signed-in owner must own their own vault item")
	}
	for label, p := range map[string]auth.Principal{
		"agent":   {Kind: auth.KindUser, UserID: id, AgentIdentity: "agent-1"},
		"session": {Kind: auth.KindUser, UserID: id, SessionIdentity: "session-1"},
	} {
		if ownsVaultItem(p, name) {
			t.Errorf("a principal carrying an %s identity owned the launcher's vault item", label)
		}
	}
}
