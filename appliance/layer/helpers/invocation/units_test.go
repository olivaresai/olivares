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

// unitDirectives maps "Section.Key" to its values in file order.
func unitDirectives(t *testing.T, path string) map[string][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	directives := map[string][]string{}
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
			t.Fatalf("%s: not a directive: %q", path, line)
		}
		directives[section+"."+key] = append(directives[section+"."+key], value)
	}
	return directives
}

// rootUnitDirs is where a root helper's socket and template are shipped from when it is not the
// seam's own directory: the firewall's local entry point is a program of the firewall module, which
// ships its units beside the owner's other units.
var rootUnitDirs = map[string]string{
	helperschema.HelperFirewallLocal: filepath.Join("..", "..", "firewall", "units"),
}

// rootUnit is the path of the unit file of the root helper name.
func rootUnit(name, file string) string {
	dir, ok := rootUnitDirs[name]
	if !ok {
		dir = filepath.Join("..", "units")
	}
	return filepath.Join(dir, file)
}

// rootConfinement is what a root helper's template states of its network and its capabilities,
// exactly; nil is a directive the template does not state. A root helper runs with no network and no
// capability, but the firewall's local entry point loads the host's own table: it runs in the host's
// network namespace, with netlink, and holds CAP_NET_ADMIN alone, as the owner's other units do.
func rootConfinement(name string) map[string][]string {
	if name == helperschema.HelperFirewallLocal {
		return map[string][]string{"Service.PrivateNetwork": nil, "Service.RestrictAddressFamilies": {"AF_UNIX AF_NETLINK"},
			"Service.CapabilityBoundingSet": {"CAP_NET_ADMIN"}}
	}
	return map[string][]string{"Service.PrivateNetwork": {"yes"}, "Service.RestrictAddressFamilies": {"AF_UNIX"},
		"Service.CapabilityBoundingSet": {""}}
}

func TestHelperUnits_OneInstancePerConnectionOnARootOnlySocket(t *testing.T) {
	// The binary each template runs is TestHelperUnits_FlatPathsAndTheStaticSupportBundleAccount's.
	for _, name := range helperschema.Helpers() {
		t.Run(name, func(t *testing.T) {
			socket := unitDirectives(t, rootUnit(name, "olivares-helper-"+name+".socket"))
			service := unitDirectives(t, rootUnit(name, "olivares-helper-"+name+"@.service"))
			for key, want := range map[string]string{
				"Socket.ListenStream": filepath.Join(helperschema.SocketDir, name+".sock"),
				"Socket.Accept":       "yes",
				"Socket.SocketUser":   "root",
				"Socket.SocketGroup":  "root",
				"Socket.SocketMode":   "0600",
			} {
				if !slices.Equal(socket[key], []string{want}) {
					t.Errorf("socket %s=%v, want %s", key, socket[key], want)
				}
			}
			for key, want := range map[string]string{
				"Service.StandardInput":   "socket",
				"Service.StandardOutput":  "socket",
				"Service.NoNewPrivileges": "yes",
				"Service.ProtectSystem":   "strict",
			} {
				if !slices.Equal(service[key], []string{want}) {
					t.Errorf("service %s=%v, want %q", key, service[key], want)
				}
			}
			for key, want := range rootConfinement(name) {
				if !slices.Equal(service[key], want) {
					t.Errorf("service %s=%q, want exactly %q", key, service[key], want)
				}
			}
			for key := range socket {
				if strings.HasPrefix(key, "Install.") {
					t.Errorf("the socket has an [Install] section (%s), so packaging would enable it", key)
				}
			}
			for key, values := range service {
				if strings.HasPrefix(key, "Install.") {
					t.Errorf("the service has an [Install] section (%s)", key)
				}
				if strings.HasPrefix(key, "Service.Exec") {
					for _, value := range values {
						if strings.Contains(value, " ") || strings.IndexAny(value, "+!@-:") == 0 {
							t.Errorf("%s=%s: the helper takes no argument and no privilege prefix", key, value)
						}
					}
				}
			}
			for _, key := range []string{"Service.ReadWritePaths", "Service.BindPaths", "Service.Environment", "Service.EnvironmentFile"} {
				if len(service[key]) > 0 {
					t.Errorf("service %s=%v: nothing but the unit decides the helper's inputs", key, service[key])
				}
			}
		})
	}
}

func TestHelperPolkit_TheConsoleAccountsHoldNoManagerRight(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "polkit", "50-olivares-helpers.rules"))
	if err != nil {
		t.Fatal(err)
	}
	rules := string(data)
	// The rule is JavaScript polkitd evaluates; this reads what it names. Its evaluation by a
	// real polkitd is measured on an appliance image, not here.
	for _, want := range []string{
		`"olivares-portal"`, `"olivares-support-bundle"`,
		`"org.freedesktop.systemd1."`, `"org.freedesktop.login1."`, `"org.freedesktop.udisks2."`, `"org.freedesktop.packagekit."`,
		"return polkit.Result.NO;", "return polkit.Result.NOT_HANDLED;",
	} {
		if !strings.Contains(rules, want) {
			t.Errorf("the rule does not name %s", want)
		}
	}
	for _, grant := range []string{"polkit.Result.YES", "AUTH_SELF", "AUTH_ADMIN"} {
		if strings.Contains(rules, grant) {
			t.Errorf("the rule grants or asks (%s): it may only refuse", grant)
		}
	}
}
