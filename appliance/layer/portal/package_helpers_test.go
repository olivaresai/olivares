// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package portal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

// portalPackage is the portal package's configuration: its contents and its postinstall.
type portalPackage struct {
	Contents []struct{ Src, Dst string }
	Scripts  struct{ Postinstall string }
}

// readPortalPackage reads the portal package's configuration, whose lines starting with # are
// comments.
func readPortalPackage(t *testing.T) portalPackage {
	t.Helper()
	b, err := os.ReadFile("../../../packaging/nfpm/olivares-appliance-portal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	var p portalPackage
	if err := json.Unmarshal([]byte(strings.Join(lines, "\n")), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// shipped maps each installed path of the package to its source.
func (p portalPackage) shipped() map[string]string {
	shipped := map[string]string{}
	for _, f := range p.Contents {
		shipped[f.Dst] = f.Src
	}
	return shipped
}

// moduleUnitDirectories is the directory from which the package ships each module helper's socket
// and template: its module's own.
var moduleUnitDirectories = map[string]string{
	helperschema.HelperUnits:    "appliance/layer/services/units/",
	helperschema.HelperStorage:  "appliance/layer/storage/units/",
	helperschema.HelperFirewall: "appliance/layer/firewall/units/",
}

func TestPortalPackage_ShipsEachModuleHelpersUnitsFromItsModule(t *testing.T) {
	if len(moduleUnitDirectories) != len(helperschema.ModuleHelpers()) {
		t.Fatalf("the census names %d module directories for %d module helpers", len(moduleUnitDirectories), len(helperschema.ModuleHelpers()))
	}
	shipped := readPortalPackage(t).shipped()
	for _, name := range helperschema.ModuleHelpers() {
		dir, ok := moduleUnitDirectories[name]
		if !ok {
			t.Fatalf("no module ships the units of %s", name)
		}
		for _, unit := range []string{"olivares-helper-" + name + ".socket", "olivares-helper-" + name + "@.service"} {
			if src := shipped["/usr/lib/systemd/system/"+unit]; src != dir+unit {
				t.Errorf("the package ships %s from %q, want %s%s", unit, src, dir, unit)
			}
		}
	}
}

// postinstallEnables returns the units the package's postinstall enables, in its order.
func postinstallEnables(t *testing.T, p portalPackage) []string {
	t.Helper()
	if p.Scripts.Postinstall != "appliance/layer/netguard/package/postinstall.sh" {
		t.Fatalf("the package's postinstall is %q", p.Scripts.Postinstall)
	}
	script, err := os.ReadFile(filepath.Join("..", "..", "..", p.Scripts.Postinstall))
	if err != nil {
		t.Fatal(err)
	}
	var enabled []string
	for _, line := range strings.Split(string(script), "\n") {
		if fields := strings.Fields(line); len(fields) > 2 && fields[0] == "systemctl" && fields[1] == "enable" {
			enabled = append(enabled, fields[2:]...)
		}
	}
	return enabled
}

func TestPortalPackage_EnablesTheModuleHelperSocketsAndNoRootHelperSocket(t *testing.T) {
	// TestPortalPackage_ShipsEachModuleHelpersUnitsFromItsModule reads where each unit comes from.
	enabled := postinstallEnables(t, readPortalPackage(t))
	for _, name := range helperschema.ModuleHelpers() {
		if socket := "olivares-helper-" + name + ".socket"; !slices.Contains(enabled, socket) {
			t.Errorf("the postinstall does not enable %s, so the Appliance Console cannot reach the %s helper", socket, name)
		}
	}
	for _, unit := range enabled {
		if strings.HasPrefix(unit, "-") || strings.Contains(unit, "*") {
			t.Errorf("the postinstall enables %q: a unit by name, never a flag or a pattern", unit)
		}
		if strings.HasPrefix(unit, "olivares-helper-") && strings.Contains(unit, "@") {
			t.Errorf("the postinstall enables the template %s: an instance is its socket's", unit)
		}
	}
	for _, name := range helperschema.Helpers() {
		if socket := "olivares-helper-" + name + ".socket"; slices.Contains(enabled, socket) {
			t.Errorf("the postinstall enables the root helper socket %s", socket)
		}
	}
}

// The firewall owner's boot load and guard are enabled with its socket, so that every boot loads the
// confirmed policy before the network and an unconfirmed change is reverted at its deadline. Each is
// named once and shipped from the firewall module; no other firewall unit, and no second owner of the
// host firewall, is enabled.
func TestPortalPackage_EnablesTheFirewallOwnersSocketAndTwoServicesByName(t *testing.T) {
	p := readPortalPackage(t)
	enabled := postinstallEnables(t, p)
	shipped := p.shipped()
	want := []string{"olivares-firewall-guard.service", "olivares-firewall.service", "olivares-helper-firewall.socket"}
	var firewallUnits []string
	for _, unit := range enabled {
		if strings.Contains(unit, "firewall") {
			firewallUnits = append(firewallUnits, unit)
		}
	}
	slices.Sort(firewallUnits)
	if !slices.Equal(firewallUnits, want) {
		t.Errorf("the postinstall enables the firewall units %v, want exactly %v, each once", firewallUnits, want)
	}
	for _, unit := range want {
		if src := shipped["/usr/lib/systemd/system/"+unit]; src != "appliance/layer/firewall/units/"+unit {
			t.Errorf("the package ships %s from %q, want the firewall module's own unit", unit, src)
		}
	}
	for _, other := range []string{"nftables.service", "firewalld.service"} {
		if slices.Contains(enabled, other) {
			t.Errorf("the postinstall enables %s, a second owner of the host firewall", other)
		}
	}
}
