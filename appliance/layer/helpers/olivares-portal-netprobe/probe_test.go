// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package main

import (
	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"testing"
)

func TestNetprobe_TransientDriverIsRefused(t *testing.T) {
	for _, uid := range []uint32{0, 1000} {
		p := helperschema.Peer{UID: uid, Account: "olivares-portal", Unit: "run-test-driver.service", Attested: true}
		if helperschema.Admit(p, probeRules(), "probe") == nil {
			t.Fatal("transient driver admitted")
		}
	}
	p := helperschema.Peer{UID: 1000, Account: "olivares-net-guard", Unit: "olivares-net-guard.service", Attested: true}
	if helperschema.Admit(p, probeRules(), "probe") == nil {
		t.Fatal("guard became probe invoker")
	}
}
