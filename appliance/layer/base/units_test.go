// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package base

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// unitFile maps each directive to its values in file order. List directives such as After=
// are split on whitespace; every other value is kept whole, so the last one is what systemd
// applies.
type unitFile map[string][]string

var listDirectives = map[string]bool{
	"After": true, "Before": true, "Wants": true, "Requires": true, "WantedBy": true, "Conflicts": true,
}

func parseUnit(text string) unitFile {
	u := unitFile{}
	text = strings.ReplaceAll(text, "\\\n", " ")
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' || line[0] == '[' {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if listDirectives[key] {
			u[key] = append(u[key], strings.Fields(value)...)
		} else {
			u[key] = append(u[key], value)
		}
	}
	return u
}

func (u unitFile) last(key string) (string, bool) {
	values, ok := u[key]
	if !ok || len(values) == 0 {
		return "", false
	}
	return values[len(values)-1], true
}

func readUnit(t *testing.T, path string) (unitFile, string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, path))
	if err != nil {
		t.Fatal(err)
	}
	return parseUnit(string(data)), string(data)
}

const (
	firstBootUnitPath = "appliance/layer/base/units/olivares-appliance-firstboot.service"
	readinessUnitPath = "appliance/layer/base/units/olivares-appliance-readiness.service"
	productUnitPath   = "packaging/systemd/olivares.service"
)

// Debian 13's cloud-init 25.1.4-1+deb13u1 units as sources.debian.org publishes them
// (systemd/cloud-final.service, cloud-config.service, cloud-init.target), and the link its
// generator creates when cloud-init is enabled: multi-user.target.wants/cloud-init.target.
var debianCloudInit = map[string]unitFile{
	"cloud-final.service": {
		"After":    {"network-online.target", "time-sync.target", "cloud-config.service", "rc-local.service", "multi-user.target"},
		"Before":   {"apt-daily.service"},
		"Wants":    {"network-online.target", "cloud-config.service"},
		"WantedBy": {"cloud-init.target"},
	},
	"cloud-config.service": {
		"After":    {"network-online.target", "cloud-config.target"},
		"Wants":    {"network-online.target", "cloud-config.target"},
		"WantedBy": {"cloud-init.target"},
	},
	"cloud-init.target": {
		"After":    {"multi-user.target"},
		"WantedBy": {"multi-user.target"},
	},
}

// Fedora 44's cloud-init 26.1 units as its package installs them: meson copies upstream's
// systemd/cloud-final.service, cloud-config.service and cloud-init.target unchanged (Fedora's one
// patch touches the local, main and network templates only), and the generator the package
// ships creates the same multi-user.target.wants/cloud-init.target link. cloud-final still
// names apt-daily.service, which no Fedora package ships, so that edge orders nothing there.
var fedoraCloudInit = map[string]unitFile{
	"cloud-final.service": {
		"After":    {"network-online.target", "time-sync.target", "cloud-config.service", "rc-local.service", "multi-user.target"},
		"Before":   {"apt-daily.service"},
		"Wants":    {"network-online.target", "cloud-config.service"},
		"WantedBy": {"cloud-init.target"},
	},
	"cloud-config.service": {
		"After":    {"network-online.target", "cloud-config.target"},
		"Wants":    {"network-online.target", "cloud-config.target"},
		"WantedBy": {"cloud-init.target"},
	},
	"cloud-init.target": {
		"After":    {"multi-user.target"},
		"WantedBy": {"multi-user.target"},
	},
}

// cloudInitFamilies are the families whose cloud-init the first-boot unit is ordered against.
var cloudInitFamilies = map[string]map[string]unitFile{"debian-13": debianCloudInit, "fedora-44": fedoraCloudInit}

// orderingGraph returns "a starts before b" edges for the units, with the default dependency
// exactly as systemd.target(5) states it: a target that wants a unit is ordered after it
// unless that unit sets DefaultDependencies=no. Our units are judged on this graph only.
// Debian's cloud-init.target, wanted by and ordered after multi-user.target, is on a cycle
// in this graph; systemd avoids that one with a check the page does not state, and whether a
// booted system orders it without a cycle is measured by the fixture's journal check
// (no_ordering_cycle_at_boot), not asserted here.
func orderingGraph(units map[string]unitFile) map[string]map[string]bool {
	before := map[string]map[string]bool{}
	edge := func(a, b string) {
		if before[a] == nil {
			before[a] = map[string]bool{}
		}
		before[a][b] = true
	}
	for name, u := range units {
		for _, a := range u["After"] {
			edge(a, name)
		}
		for _, b := range u["Before"] {
			edge(name, b)
		}
	}
	wanted := map[string][]string{}
	for name, u := range units {
		for _, target := range u["WantedBy"] {
			wanted[target] = append(wanted[target], name)
		}
		if strings.HasSuffix(name, ".target") {
			wanted[name] = append(wanted[name], u["Wants"]...)
			wanted[name] = append(wanted[name], u["Requires"]...)
		}
	}
	for target, members := range wanted {
		if dd, _ := units[target].last("DefaultDependencies"); dd == "no" {
			continue
		}
		for _, member := range members {
			if dd, _ := units[member].last("DefaultDependencies"); dd == "no" {
				continue
			}
			edge(member, target)
		}
	}
	return before
}

func TestUnits_FirstBootJobGraphHasNoOrderingCycleWithCloudInit(t *testing.T) {
	firstBoot, _ := readUnit(t, firstBootUnitPath)
	readiness, _ := readUnit(t, readinessUnitPath)
	product, _ := readUnit(t, productUnitPath)
	for family, cloudInit := range cloudInitFamilies {
		units := map[string]unitFile{
			"olivares-appliance-firstboot.service": firstBoot,
			"olivares-appliance-readiness.service": readiness,
			// Enabled by start-services once first boot has run: its [Install] section applies.
			"olivares.service":  product,
			"multi-user.target": {},
		}
		for name, u := range cloudInit {
			units[name] = u
		}
		graph := orderingGraph(units)
		for _, ours := range []string{"olivares-appliance-firstboot.service", "olivares-appliance-readiness.service", "olivares.service"} {
			if onCycle(graph, ours) {
				t.Fatalf("%s: %s is on an ordering cycle; systemd would delete a start job at every boot", family, ours)
			}
		}
		if !graph["cloud-final.service"]["olivares-appliance-firstboot.service"] {
			t.Fatalf("%s: first boot does not wait for cloud-init's final stage", family)
		}
		if graph["olivares-appliance-firstboot.service"]["multi-user.target"] {
			t.Fatalf("%s: multi-user.target waits for first boot, which waits for cloud-final, which follows multi-user.target", family)
		}
	}
	// Without default dependencies the unit restates the ones systemd.service(5) lists.
	if dd, _ := firstBoot.last("DefaultDependencies"); dd == "no" {
		for key, want := range map[string]string{
			"Requires": "sysinit.target", "After": "basic.target", "Conflicts": "shutdown.target", "Before": "shutdown.target",
		} {
			if !slices.Contains(firstBoot[key], want) {
				t.Fatalf("DefaultDependencies=no without %s=%s", key, want)
			}
		}
		if !slices.Contains(firstBoot["After"], "sysinit.target") {
			t.Fatal("DefaultDependencies=no without After=sysinit.target")
		}
	}
}

// hardening returns the product unit's hardening directives: every [Service] key after its
// "--- hardening" marker, except the unit-specific paths.
func hardening(t *testing.T) (keys []string, values unitFile) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, productUnitPath))
	if err != nil {
		t.Fatal(err)
	}
	_, block, ok := strings.Cut(string(data), "--- hardening")
	if !ok {
		t.Fatal("the product unit has no hardening block")
	}
	values = parseUnit(block)
	for key := range values {
		if key != "StateDirectory" && key != "ReadWritePaths" && key != "WantedBy" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys, values
}

func TestUnits_CarryTheProductHardeningByLastValueOrStateWhy(t *testing.T) {
	keys, product := hardening(t)
	if len(keys) < 20 {
		t.Fatalf("read only %d hardening directives from the product unit: %v", len(keys), keys)
	}
	for _, path := range []string{firstBootUnitPath, readinessUnitPath} {
		unit, text := readUnit(t, path)
		for _, key := range keys {
			justified := strings.Contains(text, "\n# "+key+": ")
			if key == "SystemCallFilter" {
				if !slices.Equal(unit[key], product[key]) && !justified {
					t.Fatalf("%s: SystemCallFilter %q, the product's %q, and no stated reason", path, unit[key], product[key])
				}
				continue
			}
			got, present := unit.last(key)
			want, _ := product.last(key)
			if (!present || got != want) && !justified {
				t.Fatalf("%s: %s=%q (present %v), the product's %q, and no stated reason", path, key, got, present, want)
			}
		}
		// Every path may be missing: the stage that needs one then records its refusal, where a
		// missing required path would stop the unit before first boot could record anything.
		want := "-/etc/olivares -/etc/systemd/system/olivares.service.d -/etc/olivares-portal -/etc/cloud/cloud.cfg.d " +
			"-/var/lib/olivares"
		if got, _ := unit.last("ReadWritePaths"); got != want {
			t.Fatalf("%s: ReadWritePaths=%q: the product configuration, its drop-in directory, the Appliance "+
				"Console's selection directory, the cloud-init drop-in directory and the product data directory "+
				"(the setup token), each prefixed '-'", path, got)
		}
		// apply's context (appliance-firstboot's applyTimeout, 10 minutes) ends before systemd's.
		if got, _ := unit.last("TimeoutStartSec"); got != "15min" {
			t.Fatalf("%s: TimeoutStartSec=%q, not longer than the 10-minute apply context", path, got)
		}
	}
}

// TestUnits_LetFirstBootGiveTheSetupTokenToTheProduct pins what prepare-setup-delivery needs from
// the sandbox: it writes into the product's 0750 data directory as root (CAP_DAC_OVERRIDE) and gives
// the token to the product service account (CAP_CHOWN and the @chown calls the deny list removes).
// Without them the stage refuses on every real boot while the fake-host tests stay green.
func TestUnits_LetFirstBootGiveTheSetupTokenToTheProduct(t *testing.T) {
	for _, path := range []string{firstBootUnitPath, readinessUnitPath} {
		unit, _ := readUnit(t, path)
		caps, _ := unit.last("CapabilityBoundingSet")
		for _, want := range []string{"CAP_DAC_OVERRIDE", "CAP_CHOWN"} {
			if !slices.Contains(strings.Fields(caps), want) {
				t.Fatalf("%s: CapabilityBoundingSet=%q lacks %s", path, caps, want)
			}
		}
		filters := unit["SystemCallFilter"]
		deny := slices.IndexFunc(filters, func(v string) bool { return strings.HasPrefix(v, "~") })
		allow := slices.Index(filters, "@chown")
		if deny < 0 || allow < deny {
			t.Fatalf("%s: SystemCallFilter %q does not allow @chown after the deny list", path, filters)
		}
	}
}
