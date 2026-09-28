// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package services

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

// directives maps "Section.Key" to its values in file order.
func directives(t *testing.T, path string) map[string][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
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
			t.Fatalf("%s: not a directive: %q", path, line)
		}
		out[section+"."+key] = append(out[section+"."+key], value)
	}
	return out
}

func TestServices_UnitsHelperRunsOneRootInstancePerConnectionOnItsSocket(t *testing.T) {
	socket := directives(t, filepath.Join("units", "olivares-helper-units.socket"))
	service := directives(t, filepath.Join("units", "olivares-helper-units@.service"))
	for key, want := range map[string]string{
		"Socket.ListenStream":   helperschema.SocketDir + "/" + HelperName + ".sock",
		"Socket.Accept":         "yes",
		"Socket.SocketUser":     "root",
		"Socket.SocketGroup":    "olivares-portal",
		"Socket.SocketMode":     "0660",
		"Socket.MaxConnections": "4",
		"Install.WantedBy":      "sockets.target",
	} {
		if !slices.Equal(socket[key], []string{want}) {
			t.Errorf("socket %s=%v, want %s", key, socket[key], want)
		}
	}
	for key, want := range map[string]string{
		"Service.ExecStart":               "/usr/libexec/olivares/olivares-portal-units",
		"Service.User":                    "root",
		"Service.StandardInput":           "socket",
		"Service.StandardOutput":          "socket",
		"Service.NoNewPrivileges":         "yes",
		"Service.ProtectSystem":           "strict",
		"Service.ProtectHome":             "yes",
		"Service.PrivateTmp":              "yes",
		"Service.PrivateDevices":          "yes",
		"Service.PrivateNetwork":          "yes",
		"Service.IPAddressDeny":           "any",
		"Service.RestrictAddressFamilies": "AF_UNIX",
		// The service manager admits a uid-0 bus caller's unit methods by its uid, and root
		// reads the journal files it owns: the instance keeps no capability, as the power
		// helper's does.
		"Service.CapabilityBoundingSet": "",
		"Service.AmbientCapabilities":   "",
	} {
		if !slices.Equal(service[key], []string{want}) {
			t.Errorf("service %s=%v, want %q", key, service[key], want)
		}
	}
	for key := range service {
		if strings.HasPrefix(key, "Install.") {
			t.Errorf("the template has an [Install] section (%s)", key)
		}
	}
	// The sibling power helper runs as root with an empty bounding set; this one keeps no more.
	power := directives(t, filepath.Join("..", "helpers", "units", "olivares-helper-power@.service"))
	if !slices.Equal(power["Service.CapabilityBoundingSet"], service["Service.CapabilityBoundingSet"]) {
		t.Errorf("the units template bounds capabilities to %v, the power helper's to %v", service["Service.CapabilityBoundingSet"], power["Service.CapabilityBoundingSet"])
	}
	for _, key := range []string{"Service.ReadWritePaths", "Service.BindPaths", "Service.Environment", "Service.EnvironmentFile", "Service.DynamicUser", "Service.Group", "Service.SupplementaryGroups"} {
		if len(service[key]) > 0 {
			t.Errorf("service %s=%v: nothing but the template decides the helper's inputs and account", key, service[key])
		}
	}
	// One instance outlives the longest job wait and the journal reader's bound.
	runtime := service["Service.RuntimeMaxSec"]
	if len(runtime) != 1 {
		t.Fatalf("RuntimeMaxSec=%v", runtime)
	}
	seconds, err := strconv.Atoi(runtime[0])
	if err != nil || time.Duration(seconds)*time.Second <= DefaultJobWait+journalTimeout {
		t.Errorf("RuntimeMaxSec=%s does not outlive the job wait and the journal bound", runtime[0])
	}
	if info, err := os.Stat(filepath.Join("..", "helpers", "olivares-portal-units", "main.go")); err != nil || !info.Mode().IsRegular() {
		t.Errorf("no olivares-portal-units program is built from this layer: %v", err)
	}
}
