// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"github.com/olivaresai/olivares/modules/sessions"
	"strings"
	"testing"
)

func TestSessionLaunchPlanBindsRemoteNode(t *testing.T) {
	local := sessions.LaunchIntent{PermissionMode: "default"}
	one, two := local, local
	one.RunOn, two.RunOn = "node-one", "node-two"
	hashes := map[string]bool{}
	for _, intent := range []sessions.LaunchIntent{local, one, two} {
		hashes[sessionLaunchPlanHash(intent)] = true
	}
	if len(hashes) != 3 {
		t.Fatal("changing the node must invalidate approval")
	}
	if !strings.Contains(describeLaunch(one), "run on node-one") {
		t.Fatal("approver cannot see the node")
	}
	if strings.Contains(describeLaunch(local), "run on") {
		t.Fatal("local launch names a remote node")
	}
}
