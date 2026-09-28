// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package storage_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// repoRoot is the repository root, from this package's directory.
const repoRoot = "../../.."

// nfpmManifest reads an nfpm document written in the JSON subset of YAML after its comment
// lines, as nfpm and the other package tests read it.
func nfpmManifest(t *testing.T, name string, v any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, "packaging/nfpm", name))
	if err != nil {
		t.Fatal(err)
	}
	var body []string
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "#") {
			body = append(body, line)
		}
	}
	if err := json.Unmarshal([]byte(strings.Join(body, "\n")), v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

type fileInfo struct {
	Mode  int    `json:"mode"`
	Owner string `json:"owner"`
	Group string `json:"group"`
}

type content struct {
	Src      string   `json:"src"`
	Dst      string   `json:"dst"`
	FileInfo fileInfo `json:"file_info"`
}

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

func TestPackage_PortalShipsTheStorageHelperAndItsUnits(t *testing.T) {
	var portal struct {
		Contents []content `json:"contents"`
	}
	nfpmManifest(t, "olivares-appliance-portal.yaml", &portal)
	want := []content{
		{"bin/olivares-portal-storage", "/usr/libexec/olivares/olivares-portal-storage", fileInfo{493, "root", "root"}},
		{"appliance/layer/storage/units/olivares-helper-storage.socket", "/usr/lib/systemd/system/olivares-helper-storage.socket", fileInfo{420, "root", "root"}},
		{"appliance/layer/storage/units/olivares-helper-storage@.service", "/usr/lib/systemd/system/olivares-helper-storage@.service", fileInfo{420, "root", "root"}},
	}
	for _, entry := range want {
		i := slices.IndexFunc(portal.Contents, func(c content) bool { return c.Dst == entry.Dst })
		if i < 0 {
			t.Errorf("the portal package does not install %s", entry.Dst)
			continue
		}
		if portal.Contents[i] != entry {
			t.Errorf("%s is %+v, want %+v", entry.Dst, portal.Contents[i], entry)
		}
		if strings.HasPrefix(entry.Src, "appliance/") {
			if info, err := os.Stat(filepath.Join(repoRoot, entry.Src)); err != nil || !info.Mode().IsRegular() {
				t.Errorf("%s is not in the repository: %v", entry.Src, err)
			}
		}
	}
	if info, err := os.Stat("olivares-portal-storage/main.go"); err != nil || !info.Mode().IsRegular() {
		t.Errorf("no program olivares-portal-storage is built from this layer: %v", err)
	}

	socket := unitDirectives(t, "units/olivares-helper-storage.socket")
	for key, value := range map[string]string{
		"Socket.ListenStream": "/run/olivares-helpers/storage.sock", "Socket.Accept": "yes", "Socket.SocketUser": "root",
		"Socket.SocketGroup": "olivares-portal", "Socket.SocketMode": "0660", "Socket.MaxConnections": "4",
	} {
		if !slices.Equal(socket[key], []string{value}) {
			t.Errorf("socket %s=%v, want %s", key, socket[key], value)
		}
	}
	service := unitDirectives(t, "units/olivares-helper-storage@.service")
	for key, value := range map[string]string{
		"Service.ExecStart": "/usr/libexec/olivares/olivares-portal-storage", "Service.User": "root",
		"Service.StandardInput": "socket", "Service.StandardOutput": "socket", "Service.NoNewPrivileges": "yes",
		"Service.ProtectSystem": "strict", "Service.ProtectHome": "yes", "Service.PrivateNetwork": "yes",
		"Service.RestrictAddressFamilies": "AF_UNIX", "Service.CapabilityBoundingSet": "", "Service.AmbientCapabilities": "",
		"Service.RuntimeMaxSec": "30",
	} {
		if !slices.Equal(service[key], []string{value}) {
			t.Errorf("service %s=%v, want %s", key, service[key], value)
		}
	}
	for _, key := range []string{"Service.DynamicUser", "Service.PrivateDevices", "Service.Group", "Service.SupplementaryGroups"} {
		if len(service[key]) > 0 {
			t.Errorf("service states %s=%v: the helper runs as root with no capability and must see the block devices it reads", key, service[key])
		}
	}
}

func TestPackage_BaseDependsOnUDisks2ItsLVM2ModuleAndLVM2(t *testing.T) {
	var base struct {
		Depends   []string `json:"depends"`
		Overrides map[string]struct {
			Depends []string `json:"depends"`
		} `json:"overrides"`
	}
	nfpmManifest(t, "olivares-appliance-base.yaml", &base)
	for _, family := range []string{"deb", "rpm"} {
		for _, want := range []string{"udisks2", "udisks2-lvm2", "lvm2"} {
			if !slices.Contains(base.Overrides[family].Depends, want) {
				t.Errorf("the %s base package does not depend on %s", family, want)
			}
		}
	}
	if len(base.Depends) != 0 {
		t.Errorf("the base package's family-independent depends is %v", base.Depends)
	}
	containerfile, err := os.ReadFile(filepath.Join(repoRoot, "appliance/layer/base/fixture/Containerfile"))
	if err != nil {
		t.Fatal(err)
	}
	install := regexp.MustCompile(`apt-get install -y --no-install-recommends ([^\\\n]*)`).FindSubmatch(containerfile)
	if install == nil {
		t.Fatal("the fixture installs no package list")
	}
	for _, want := range []string{"udisks2", "udisks2-lvm2", "lvm2"} {
		if !slices.Contains(strings.Fields(string(install[1])), want) {
			t.Errorf("the fixture does not install %s before the base package", want)
		}
	}
	fedora, err := os.ReadFile(filepath.Join(repoRoot, "appliance/layer/base/fixture/fedora/Containerfile"))
	if err != nil {
		t.Fatal(err)
	}
	dnf := regexp.MustCompile(`(?s)dnf -y [^\n]*? install (.*?)&& dnf clean all`).FindSubmatch(fedora)
	if dnf == nil {
		t.Fatal("the Fedora fixture installs no package list")
	}
	for _, want := range []string{"udisks2", "udisks2-lvm2", "lvm2"} {
		if !slices.Contains(strings.Fields(strings.ReplaceAll(string(dnf[1]), "\\", " ")), want) {
			t.Errorf("the Fedora fixture does not install %s before rpm installs the base package", want)
		}
	}
}
