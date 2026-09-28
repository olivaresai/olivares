// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package invocation

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

// moduleUnits is the directory in which each module helper's module ships its socket and its
// template.
var moduleUnits = map[string]string{
	helperschema.HelperUnits:    filepath.Join("..", "..", "services", "units"),
	helperschema.HelperStorage:  filepath.Join("..", "..", "storage", "units"),
	helperschema.HelperFirewall: filepath.Join("..", "..", "firewall", "units"),
}

// moduleConfinement is what each module helper's template states of its network and its
// capabilities, exactly, as its module needs; nil is a directive the template does not state. The
// services and storage reads run with no network and no capability. The firewall owner loads the
// host's own table: it runs in the host's network namespace, with netlink, and holds CAP_NET_ADMIN
// alone, which the kernel requires of an nftables sender, root included.
var moduleConfinement = map[string]map[string][]string{
	helperschema.HelperUnits: {"Service.PrivateNetwork": {"yes"}, "Service.RestrictAddressFamilies": {"AF_UNIX"},
		"Service.CapabilityBoundingSet": {""}},
	helperschema.HelperStorage: {"Service.PrivateNetwork": {"yes"}, "Service.RestrictAddressFamilies": {"AF_UNIX"},
		"Service.CapabilityBoundingSet": {""}},
	helperschema.HelperFirewall: {"Service.PrivateNetwork": nil, "Service.RestrictAddressFamilies": {"AF_UNIX AF_NETLINK"},
		"Service.CapabilityBoundingSet": {"CAP_NET_ADMIN"}},
}

func TestHelperUnits_ModuleHelpersListenForTheApplianceConsoleUnderTheirModule(t *testing.T) {
	if len(moduleUnits) != len(helperschema.ModuleHelpers()) || len(moduleConfinement) != len(helperschema.ModuleHelpers()) {
		t.Fatalf("the census names %d module directories and %d confinements for %d module helpers",
			len(moduleUnits), len(moduleConfinement), len(helperschema.ModuleHelpers()))
	}
	for _, name := range helperschema.ModuleHelpers() {
		t.Run(name, func(t *testing.T) {
			dir, ok := moduleUnits[name]
			if !ok {
				t.Fatalf("no module ships the units of %s", name)
			}
			socket := unitDirectives(t, filepath.Join(dir, "olivares-helper-"+name+".socket"))
			service := unitDirectives(t, filepath.Join(dir, "olivares-helper-"+name+"@.service"))
			// The node is root's with the Appliance Console's group, so the console can connect;
			// connecting is necessary, never sufficient: the seam admits the console's process.
			for key, want := range map[string]string{
				"Socket.ListenStream": filepath.Join(helperschema.SocketDir, name+".sock"),
				"Socket.Accept":       "yes",
				"Socket.SocketUser":   "root",
				"Socket.SocketGroup":  helperschema.Portal.Account,
				"Socket.SocketMode":   "0660",
				"Install.WantedBy":    "sockets.target",
			} {
				if !slices.Equal(socket[key], []string{want}) {
					t.Errorf("socket %s=%v, want %s", key, socket[key], want)
				}
			}
			for key, want := range map[string]string{
				"Service.ExecStart":           libexec + "/olivares-portal-" + name,
				"Service.User":                helperschema.Account(name),
				"Service.StandardInput":       "socket",
				"Service.StandardOutput":      "socket",
				"Service.NoNewPrivileges":     "yes",
				"Service.AmbientCapabilities": "",
				"Service.ProtectSystem":       "strict",
			} {
				if !slices.Equal(service[key], []string{want}) {
					t.Errorf("service %s=%v, want %q", key, service[key], want)
				}
			}
			confinement, ok := moduleConfinement[name]
			if !ok {
				t.Fatalf("no confinement is stated for %s", name)
			}
			for key, want := range confinement {
				if !slices.Equal(service[key], want) {
					t.Errorf("service %s=%q, want exactly %q", key, service[key], want)
				}
			}
			for key, values := range service {
				if strings.HasPrefix(key, "Install.") {
					t.Errorf("the template has an [Install] section (%s)", key)
				}
				if strings.HasPrefix(key, "Service.Exec") {
					for _, value := range values {
						if strings.Contains(value, " ") || strings.IndexAny(value, "+!@-:") == 0 {
							t.Errorf("%s=%s: the helper takes no argument and no privilege prefix", key, value)
						}
					}
				}
			}
			for _, key := range []string{"Service.ReadWritePaths", "Service.BindPaths", "Service.Environment", "Service.EnvironmentFile",
				"Service.DynamicUser", "Service.Group", "Service.SupplementaryGroups"} {
				if len(service[key]) > 0 {
					t.Errorf("service %s=%v: nothing but the template decides the helper's inputs and account", key, service[key])
				}
			}
			// The module ships its units; the root helpers' directory holds none of them.
			for _, file := range []string{"olivares-helper-" + name + ".socket", "olivares-helper-" + name + "@.service"} {
				if _, err := os.Stat(filepath.Join("..", "units", file)); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("%s is in the root helpers' directory (%v)", file, err)
				}
			}
		})
	}

	// The console's group is the module class's alone: no root helper's socket names it.
	for _, name := range helperschema.Helpers() {
		socket := unitDirectives(t, rootUnit(name, "olivares-helper-"+name+".socket"))
		if slices.Contains(socket["Socket.SocketGroup"], helperschema.Portal.Account) || !slices.Equal(socket["Socket.SocketMode"], []string{"0600"}) {
			t.Errorf("the root helper %s's socket is group %v mode %v, want root's alone", name, socket["Socket.SocketGroup"], socket["Socket.SocketMode"])
		}
	}
}
