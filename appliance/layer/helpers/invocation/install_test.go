// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package invocation

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

// helperBinaries are the programs the seam's templates run, one per helper, each built from the
// directory of the same name.
var helperBinaries = map[string]string{
	helperschema.HelperPower:         "olivares-portal-power",
	helperschema.HelperSupportBundle: "olivares-portal-support-bundle",
	helperschema.HelperCert:          "olivares-portal-cert",
	helperschema.HelperFirewallLocal: "olivares-portal-firewall-local",
}

// helperProgram is the directory a helper's program is built from: the helpers' own directory, or
// the module whose program it is.
func helperProgram(name string) string {
	if name == helperschema.HelperFirewallLocal {
		return filepath.Join("..", "..", "firewall", helperBinaries[name])
	}
	return filepath.Join("..", helperBinaries[name])
}

// libexec is the one directory every helper program is installed in, directly.
const libexec = "/usr/libexec/olivares"

// supportBundleAccount is the support-bundle helper's static, non-login account.
const supportBundleAccount = "olivares-support-bundle"

func TestHelperUnits_FlatPathsAndTheStaticSupportBundleAccount(t *testing.T) {
	if len(helperBinaries) != len(helperschema.Helpers()) {
		t.Fatalf("the census names %d binaries for %d helpers", len(helperBinaries), len(helperschema.Helpers()))
	}
	units, err := filepath.Glob(filepath.Join("..", "units", "*"))
	if err != nil || len(units) != 2*(len(helperschema.Helpers())-len(rootUnitDirs)) {
		t.Fatalf("the helpers' units are %v (%v), want one socket and one template per helper", units, err)
	}
	for _, unit := range units {
		data, err := os.ReadFile(unit)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "DynamicUser") {
				t.Errorf("%s: %q: every helper runs as its named account, never a dynamic one", unit, line)
			}
		}
	}
	for _, name := range helperschema.Helpers() {
		binary := helperBinaries[name]
		service := unitDirectives(t, rootUnit(name, "olivares-helper-"+name+"@.service"))
		want := libexec + "/" + binary
		if !slices.Equal(service["Service.ExecStart"], []string{want}) {
			t.Errorf("%s runs %v, want %s: every helper program is installed directly under %s/", name,
				service["Service.ExecStart"], want, libexec)
		}
		if info, err := os.Stat(filepath.Join(helperProgram(name), "main.go")); err != nil || !info.Mode().IsRegular() {
			t.Errorf("%s: no program %s is built from this layer: %v", name, binary, err)
		}
	}

	bundle := unitDirectives(t, filepath.Join("..", "units", "olivares-helper-support-bundle@.service"))
	for key, want := range map[string]string{
		"Service.User":               supportBundleAccount,
		"Service.StateDirectory":     supportBundleAccount,
		"Service.StateDirectoryMode": "0700",
	} {
		if !slices.Equal(bundle[key], []string{want}) {
			t.Errorf("the support-bundle template states %s=%v, want exactly %s", key, bundle[key], want)
		}
	}
	if got := helperschema.Account(helperschema.HelperSupportBundle); got != supportBundleAccount {
		t.Errorf("the seam names the support-bundle account %q, want %s", got, supportBundleAccount)
	}

	// The layer's text and sources name the one account and the flat directory, and no other.
	rules, err := os.ReadFile(filepath.Join("..", "polkit", "50-olivares-helpers.rules"))
	if err != nil || !strings.Contains(string(rules), `"`+supportBundleAccount+`"`) {
		t.Errorf("the polkit rule does not name %s (%v)", supportBundleAccount, err)
	}
	var texts []string
	for _, pattern := range []string{"../../../README.md", "../*/*.go", "../units/*", "../polkit/*"} {
		matched, err := filepath.Glob(filepath.FromSlash(pattern))
		if err != nil {
			t.Fatal(err)
		}
		texts = append(texts, matched...)
	}
	for _, file := range texts {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		for _, other := range []string{libexec + "/helpers", "olivares-bundle", "olivares-portal-bundle", "DynamicUser=yes"} {
			if strings.Contains(text, other) && !strings.HasSuffix(file, "_test.go") {
				t.Errorf("%s names %q, outside the one helper directory and account", file, other)
			}
		}
	}
	if len(texts) < 10 {
		t.Fatalf("the name census read %d files, so it is not reading the layer", len(texts))
	}
}

func TestHelperUnits_EachTemplateRunsAsItsHelpersAccount(t *testing.T) {
	for _, name := range helperschema.Helpers() {
		t.Run(name, func(t *testing.T) {
			account := helperschema.Account(name)
			if account == "" {
				t.Fatalf("the seam names no account for %s", name)
			}
			service := unitDirectives(t, rootUnit(name, "olivares-helper-"+name+"@.service"))
			if !slices.Equal(service["Service.User"], []string{account}) {
				t.Fatalf("the template states User=%v, want exactly %s", service["Service.User"], account)
			}
			dynamic := slices.Equal(service["Service.DynamicUser"], []string{"yes"})
			if account == "root" && dynamic {
				t.Fatal("a root helper's template allocates a dynamic account")
			}
			if account != "root" && len(service["Service.CapabilityBoundingSet"]) != 1 {
				t.Fatalf("the unprivileged template bounds its capabilities %v times", len(service["Service.CapabilityBoundingSet"]))
			}
			for _, key := range []string{"Service.Group", "Service.SupplementaryGroups"} {
				if len(service[key]) > 0 {
					t.Errorf("%s=%v: an instance runs as its account alone", key, service[key])
				}
			}
		})
	}
}

func TestContext_NamesNoSupportBundleRow(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "CONTEXT.md"))
	if err != nil {
		t.Fatal(err)
	}
	// The support-bundle row (its consumer, peer, spool owner and path) is proposed for
	// admission outside this layer's glossary, whose one writer is not this seam: no fact of the
	// row appears in it, as built or as proposed.
	glossary := strings.ToLower(string(data))
	for _, fact := range []string{"olivares-portal-support-bundle", "olivares-portal-bundle", "support-bundle.sock",
		"olivares-helper-support-bundle", "/var/lib/olivares-support-bundle", "/var/lib/olivares-bundle",
		"appliance-helper:olivares-portal-bundle", "appliance-helper:olivares-portal-support-bundle"} {
		if strings.Contains(glossary, fact) {
			t.Errorf("CONTEXT.md names the support-bundle row's %s", fact)
		}
	}
}

// dynamicUserImplies is what systemd.exec(5) says DynamicUser= implies, each with the values
// that keep it. A template that runs a helper under a static account states every one of them
// itself, so that allocating the account statically loses none.
var dynamicUserImplies = map[string][]string{
	"Service.ProtectSystem":    {"strict"},
	"Service.ProtectHome":      {"yes", "read-only"},
	"Service.PrivateTmp":       {"yes", "disconnected"},
	"Service.RemoveIPC":        {"yes"},
	"Service.RestrictSUIDSGID": {"yes"},
	"Service.NoNewPrivileges":  {"yes"},
}

func TestHelperUnits_NonRootTemplatesStateEveryProtectionDynamicUserImplies(t *testing.T) {
	unprivileged := 0
	for _, name := range helperschema.Helpers() {
		if helperschema.Account(name) == "root" {
			continue
		}
		unprivileged++
		t.Run(name, func(t *testing.T) {
			service := unitDirectives(t, filepath.Join("..", "units", "olivares-helper-"+name+"@.service"))
			for key, keep := range dynamicUserImplies {
				if got := service[key]; len(got) != 1 || !slices.Contains(keep, got[0]) {
					t.Errorf("the %s template states %s=%v, want exactly one of %v: DynamicUser= would imply it", name, key, got, keep)
				}
			}
		})
	}
	if unprivileged == 0 {
		t.Fatal("no helper runs under a non-root account, so this test reads nothing")
	}
}
