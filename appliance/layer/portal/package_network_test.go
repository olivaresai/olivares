// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package portal

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestPortalPackage_ShipsGuardHelpersPolkitRuleAndAccounts(t *testing.T) {
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
	var p struct {
		Name     string
		Contents []struct{ Src, Dst string }
	}
	if err := json.Unmarshal([]byte(strings.Join(lines, "\n")), &p); err != nil {
		t.Fatal(err)
	}
	files := map[string]bool{}
	for _, f := range p.Contents {
		files[f.Dst] = true
	}
	for _, file := range []string{"/usr/libexec/olivares/olivares-net-guard", "/usr/libexec/olivares/olivares-portal-netrestore", "/usr/libexec/olivares/olivares-portal-netprobe", "/usr/libexec/olivares/olivares-portal-power", "/usr/libexec/olivares/olivares-portal-support-bundle", "/usr/share/polkit-1/rules.d/50-olivares-helpers.rules", "/usr/share/polkit-1/rules.d/60-olivares-portal.rules", "/usr/lib/sysusers.d/olivares-appliance-portal.conf", "/usr/lib/systemd/system/olivares-net-guard.service", "/usr/lib/systemd/system/olivares-net-guard.socket"} {
		if !files[file] {
			t.Error("missing", file)
		}
	}
	accounts, err := os.ReadFile("units/sysusers.d/olivares-appliance-portal.conf")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"olivares-portal", "olivares-net-guard", "olivares-netprobe", "olivares-support-bundle"} {
		if !strings.Contains(string(accounts), "u "+name+" ") {
			t.Error("missing static account", name)
		}
	}
	rule, err := os.ReadFile("../helpers/polkit/60-olivares-network.rules")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rule), `subject.user === "olivares-portal"`) {
		t.Fatal("portal is a writer")
	}
	for _, action := range []string{"settings.modify.system", "network-control", "checkpoint-rollback"} {
		if !strings.Contains(string(rule), action) {
			t.Error(action)
		}
	}
}
