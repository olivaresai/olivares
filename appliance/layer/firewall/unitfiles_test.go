// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package firewall_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/firewall"
	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

// directives maps "Section.Key" to its values in file order.
func directives(t *testing.T, name string) map[string][]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("units", name))
	if err != nil {
		t.Fatalf("the unit %s is not in this layer: %v", name, err)
	}
	out := map[string][]string{}
	section := ""
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' && line[len(line)-1] == ']' {
			section = line[1 : len(line)-1]
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || section == "" {
			t.Fatalf("%s: not a directive: %q", name, line)
		}
		out[section+"."+key] = append(out[section+"."+key], value)
	}
	return out
}

func expect(t *testing.T, unit string, got map[string][]string, want map[string]string) {
	t.Helper()
	for key, value := range want {
		if !slices.Equal(got[key], []string{value}) {
			t.Errorf("%s %s=%q, want %q", unit, key, got[key], value)
		}
	}
}

// ownerHardening is what every unit that runs the owner states: root with CAP_NET_ADMIN alone, in the
// host's network namespace, with the owner's state and runtime directories.
var ownerHardening = map[string]string{
	"Service.User":                     "root",
	"Service.NoNewPrivileges":          "yes",
	"Service.ProtectSystem":            "strict",
	"Service.ProtectHome":              "yes",
	"Service.PrivateTmp":               "yes",
	"Service.PrivateDevices":           "yes",
	"Service.RestrictAddressFamilies":  "AF_UNIX AF_NETLINK",
	"Service.IPAddressDeny":            "any",
	"Service.StateDirectory":           "olivares-firewall",
	"Service.StateDirectoryMode":       "0700",
	"Service.RuntimeDirectory":         "olivares-firewall",
	"Service.RuntimeDirectoryMode":     "0755",
	"Service.RuntimeDirectoryPreserve": "yes",
	"Service.CapabilityBoundingSet":    "CAP_NET_ADMIN",
	"Service.AmbientCapabilities":      "",
	"Service.UMask":                    "0077",
}

// Every unit that runs the owner holds one capability: the kernel refuses nftables netlink
// messages from a sender without CAP_NET_ADMIN in the network namespace, root included.
func TestUnits_TheOwnerRunsAsRootWithOnlyNetAdminInTheHostNetwork(t *testing.T) {
	socket := directives(t, "olivares-helper-firewall.socket")
	expect(t, "socket", socket, map[string]string{
		"Socket.ListenStream":   helperschema.SocketDir + "/" + firewall.HelperName + ".sock",
		"Socket.Accept":         "yes",
		"Socket.SocketUser":     "root",
		"Socket.SocketGroup":    "olivares-portal",
		"Socket.SocketMode":     "0660",
		"Socket.MaxConnections": "4",
		"Install.WantedBy":      "sockets.target",
	})
	hardening := ownerHardening
	units := map[string]map[string]string{
		"olivares-helper-firewall@.service": {
			"Service.ExecStart":      "/usr/libexec/olivares/olivares-portal-firewall",
			"Service.StandardInput":  "socket",
			"Service.StandardOutput": "socket",
		},
		"olivares-firewall.service": {
			"Unit.DefaultDependencies": "no",
			"Unit.Wants":               "network-pre.target",
			"Unit.Before":              "network-pre.target shutdown.target",
			"Unit.After":               "local-fs.target",
			"Unit.Conflicts":           "shutdown.target nftables.service",
			"Service.Type":             "oneshot",
			"Service.RemainAfterExit":  "yes",
			"Service.ExecStart":        "/usr/libexec/olivares/olivares-portal-firewall --boot-load",
			"Install.WantedBy":         "sysinit.target",
		},
		"olivares-firewall-guard.service": {
			"Unit.Wants":        "olivares-firewall.service",
			"Unit.After":        "olivares-firewall.service",
			"Service.Type":      "simple",
			"Service.ExecStart": "/usr/libexec/olivares/olivares-portal-firewall --guard",
			"Service.Restart":   "always",
			"Install.WantedBy":  "multi-user.target",
		},
	}
	for name, own := range units {
		got := directives(t, name)
		expect(t, name, got, hardening)
		expect(t, name, got, own)
		// The owner acts on the host's network namespace and names nothing else of its own.
		for _, key := range []string{"Service.PrivateNetwork", "Service.ReadWritePaths", "Service.BindPaths", "Service.Environment",
			"Service.EnvironmentFile", "Service.DynamicUser", "Service.Group", "Service.SupplementaryGroups"} {
			if len(got[key]) > 0 {
				t.Errorf("%s %s=%q", name, key, got[key])
			}
		}
	}
	// The boot load runs before network-pre.target with no default dependencies, like the
	// distribution's own firewall unit: nothing may order it after basic.target, where units that
	// must finish before sysinit.target would close an ordering cycle through NetworkManager.
	boot := directives(t, "olivares-firewall.service")
	for _, key := range []string{"Unit.Requires", "Unit.BindsTo", "Unit.Requisite"} {
		if len(boot[key]) > 0 {
			t.Errorf("olivares-firewall.service %s=%q", key, boot[key])
		}
	}
	for key := range directives(t, "olivares-helper-firewall@.service") {
		if strings.HasPrefix(key, "Install.") {
			t.Errorf("the helper template has an [Install] section (%s)", key)
		}
	}
	if info, err := os.Stat(filepath.Join("olivares-portal-firewall", "main.go")); err != nil || !info.Mode().IsRegular() {
		t.Errorf("no olivares-portal-firewall program is built from this layer: %v", err)
	}
}

func TestPackage_PortalPackageShipsTheFirewallOwner(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	data, err := os.ReadFile(filepath.Join(root, "packaging", "nfpm", "olivares-appliance-portal.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	var pkg struct {
		Name     string `json:"name"`
		Contents []struct {
			Src      string `json:"src"`
			Dst      string `json:"dst"`
			FileInfo struct {
				Mode  int    `json:"mode"`
				Owner string `json:"owner"`
				Group string `json:"group"`
			} `json:"file_info"`
		} `json:"contents"`
	}
	if err := json.Unmarshal([]byte(strings.Join(lines, "\n")), &pkg); err != nil || pkg.Name != "olivares-appliance-portal" {
		t.Fatalf("the portal package does not parse: %v", err)
	}
	units := "appliance/layer/firewall/units/"
	want := map[string]struct {
		src  string
		mode int
	}{
		"/usr/libexec/olivares/olivares-portal-firewall":                  {"bin/olivares-portal-firewall", 0o755},
		"/usr/lib/systemd/system/olivares-helper-firewall.socket":         {units + "olivares-helper-firewall.socket", 0o644},
		"/usr/lib/systemd/system/olivares-helper-firewall@.service":       {units + "olivares-helper-firewall@.service", 0o644},
		"/usr/lib/systemd/system/olivares-firewall.service":               {units + "olivares-firewall.service", 0o644},
		"/usr/lib/systemd/system/olivares-firewall-guard.service":         {units + "olivares-firewall-guard.service", 0o644},
		"/usr/libexec/olivares/olivares-portal-firewall-local":            {"bin/olivares-portal-firewall-local", 0o755},
		"/usr/lib/systemd/system/olivares-helper-firewall-local.socket":   {units + "olivares-helper-firewall-local.socket", 0o644},
		"/usr/lib/systemd/system/olivares-helper-firewall-local@.service": {units + "olivares-helper-firewall-local@.service", 0o644},
	}
	seen := map[string]int{}
	for _, entry := range pkg.Contents {
		if !strings.Contains(entry.Dst, "firewall") && !strings.HasPrefix(entry.Src, "appliance/layer/firewall/") {
			continue
		}
		seen[entry.Dst]++
		w, ok := want[entry.Dst]
		if !ok {
			t.Errorf("the package ships %s from %s, which is not one of the firewall owner's files", entry.Dst, entry.Src)
			continue
		}
		if entry.Src != w.src || entry.FileInfo.Mode != w.mode || entry.FileInfo.Owner != "root" || entry.FileInfo.Group != "root" {
			t.Errorf("%s: src %s mode %o owner %q group %q, want %s %o root root", entry.Dst, entry.Src, entry.FileInfo.Mode, entry.FileInfo.Owner, entry.FileInfo.Group, w.src, w.mode)
		}
		if !strings.HasPrefix(entry.Src, "bin/") {
			if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(entry.Src))); err != nil || !info.Mode().IsRegular() {
				t.Errorf("%s: its source %s is not in the tree: %v", entry.Dst, entry.Src, err)
			}
		}
	}
	for dst := range want {
		if seen[dst] != 1 {
			t.Errorf("the package ships %s %d times, want once", dst, seen[dst])
		}
	}
}

// The local entry point is a root helper over the same owner: its socket is root's alone and packaged,
// not enabled, since the repair console's unit starts it; its template runs the local program with the
// owner's own hardening and takes no argument.
func TestUnits_TheLocalEntryPointIsRootsAloneWithTheOwnersHardening(t *testing.T) {
	socket := directives(t, "olivares-helper-firewall-local.socket")
	expect(t, "local socket", socket, map[string]string{
		"Socket.ListenStream":   helperschema.SocketDir + "/" + helperschema.HelperFirewallLocal + ".sock",
		"Socket.Accept":         "yes",
		"Socket.SocketUser":     "root",
		"Socket.SocketGroup":    "root",
		"Socket.SocketMode":     "0600",
		"Socket.MaxConnections": "4",
	})
	service := directives(t, "olivares-helper-firewall-local@.service")
	expect(t, "local template", service, ownerHardening)
	expect(t, "local template", service, map[string]string{
		"Service.ExecStart":      "/usr/libexec/olivares/olivares-portal-firewall-local",
		"Service.StandardInput":  "socket",
		"Service.StandardOutput": "socket",
	})
	for name, unit := range map[string]map[string][]string{"socket": socket, "template": service} {
		for key := range unit {
			if strings.HasPrefix(key, "Install.") {
				t.Errorf("the local %s has an [Install] section (%s)", name, key)
			}
		}
	}
	for _, key := range []string{"Service.PrivateNetwork", "Service.ReadWritePaths", "Service.BindPaths", "Service.Environment",
		"Service.EnvironmentFile", "Service.DynamicUser", "Service.Group", "Service.SupplementaryGroups"} {
		if len(service[key]) > 0 {
			t.Errorf("the local template %s=%q", key, service[key])
		}
	}
	// The module helper's socket keeps its one audience, the Appliance Console's group.
	expect(t, "module socket", directives(t, "olivares-helper-firewall.socket"), map[string]string{
		"Socket.SocketGroup": "olivares-portal", "Socket.SocketMode": "0660"})
	if info, err := os.Stat(filepath.Join("olivares-portal-firewall-local", "main.go")); err != nil || !info.Mode().IsRegular() {
		t.Errorf("no olivares-portal-firewall-local program is built from this layer: %v", err)
	}
}
