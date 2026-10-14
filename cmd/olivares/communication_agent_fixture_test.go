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

// seedCommunicationAgentIdentity installs durable identity/lifecycle state as a
// fixture for Community safety checks. It seeds no credential or communication
// record: exchanges, delegation, grants and messages still use product APIs.
func seedCommunicationAgentIdentity(t *testing.T, eng *engine, tenant model.TenantID, agentID, sponsorID model.ID, agentRef, sponsorRef string) model.ID {
	t.Helper()
	ctx := context.Background()
	var identityID model.ID
	lifecycle := nhiRecord(agentRef, "sponsor_ref", sponsorRef)
	lifecycle["source"] = "test-roster"
	(&consentEstate{t: t, eng: eng}).seedFenced(tenant, "governance.nhi_lifecycle", lifecycle, sponsorID)
	if err := eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
		identity, err := sc.Identities().Create(ctx, model.Identity{Name: agentRef, Kind: "nhi", ExternalID: agentRef, Metadata: map[string]any{"principal_type": "nhi"}})
		if err != nil {
			return err
		}
		agent, err := sc.Agents().Get(ctx, agentID)
		if err != nil {
			return err
		}
		agent.IdentityID = identity.ID
		if _, err := sc.Agents().Update(ctx, agent); err != nil {
			return err
		}
		identityID = identity.ID
		return nil
	}); err != nil {
		t.Fatalf("seed durable agent identity: %v", err)
	}
	if identityID.IsZero() {
		t.Fatal("durable agent identity is empty")
	}
	return identityID
}
