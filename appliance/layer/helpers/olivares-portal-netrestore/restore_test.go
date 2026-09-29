// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package main

import (
	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"testing"
)

func TestNetrestore_OnlyGuardAndQualifiedConsoleRowsCanRequestRecovery(t *testing.T) {
	guard := helperschema.Peer{UID: 123, Account: "olivares-net-guard", Unit: "olivares-net-guard.service", Attested: true}
	if err := helperschema.Admit(guard, restoreRules(), "restore"); err != nil {
		t.Fatal(err)
	}
	console := helperschema.Peer{UID: 0, Account: "root", Unit: "olivares-repair-console.service", TTY: "/dev/tty1", Attested: true}
	if err := helperschema.Admit(console, restoreRules(), "restore"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []helperschema.Peer{{UID: 0, Account: "root", Unit: "run-driver.service", Attested: true}, {UID: 124, Account: "olivares-portal", Unit: "olivares-portal.service", Attested: true}} {
		if helperschema.Admit(p, restoreRules(), "restore") == nil {
			t.Fatal("foreign caller admitted")
		}
	}
}
