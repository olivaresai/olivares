// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package firewall_test

import (
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/firewall"
	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

// The owner's program serves the seam's firewall helper: its socket name is the registry's, the
// seam knows it as a module helper, and its instances run as root, as its template states.
func TestHelperName_IsTheRegistrysFirewallModuleHelper(t *testing.T) {
	if firewall.HelperName != "firewall" || firewall.HelperName != helperschema.HelperFirewall {
		t.Fatalf("the program serves %q and the registry names %q; both are firewall", firewall.HelperName, helperschema.HelperFirewall)
	}
	if class, ok := helperschema.ClassOf(firewall.HelperName); !ok || class != helperschema.ClassModule {
		t.Fatalf("the seam knows %s as class %v (known %v), want the module class", firewall.HelperName, class, ok)
	}
	if got := helperschema.Account(firewall.HelperName); got != "root" {
		t.Errorf("the seam runs %s as %q, want root", firewall.HelperName, got)
	}
}
