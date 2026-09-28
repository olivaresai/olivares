// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package netguard

import "testing"

func TestTransport_MutationsUseClosedMethodsFlagsAndVersions(t *testing.T) {
	p := Profile{Device: "/device/1", Connection: "/connection/1"}
	for _, m := range []Mutation{{Method: Reapply, Profile: p, Version: 0, EmptySettings: true}, {Method: Reapply, Profile: p, Version: 1}, {Method: UpdateInMemory, Profile: p, Version: 0, Flags: 2}, {Method: UpdateToDisk, Profile: p, Version: 1, Flags: 1}, {Method: CheckpointCreate, Profile: p, Flags: 1, TimeoutSeconds: 90}, {Method: Method("activate_candidate"), Profile: p}, {Method: Method("adjust_timeout"), Profile: p}} {
		if _, _, _, _, err := mutationMessage(m, nil); err == nil {
			t.Fatalf("unsafe mutation accepted: %+v", m)
		}
	}
	m := Mutation{Method: Reapply, Profile: p, Version: 8, EmptySettings: true}
	_, _, member, body, err := mutationMessage(m, nil)
	if err != nil || member != "Reapply" || len(body) != 3 || body[1] != uint64(8) || body[2] != uint32(0) {
		t.Fatal(member, body, err)
	}
}
func TestTransport_CandidateActivationRefusedBeforeAnySend(t *testing.T) {
	if _, _, _, _, err := mutationMessage(Mutation{Method: RestoreActivation}, nil); err == nil {
		t.Fatal("unproved restoration activation accepted")
	}
}
