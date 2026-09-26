// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package base

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const repoRoot = "../../.."

type nfpmContent struct {
	Src      string `json:"src"`
	Dst      string `json:"dst"`
	Type     string `json:"type"`
	FileInfo struct {
		Mode uint32 `json:"mode"`
	} `json:"file_info"`
}

// readManifest parses the nfpm document after its leading comment lines. The document is
// the JSON subset of YAML, so this is the data nfpm reads.
func readManifest(t *testing.T) (manifest struct {
	Name       string        `json:"name"`
	Recommends []string      `json:"recommends"`
	Depends    []string      `json:"depends"`
	Contents   []nfpmContent `json:"contents"`
	Scripts    struct {
		Postinstall string `json:"postinstall"`
		Preremove   string `json:"preremove"`
		Postremove  string `json:"postremove"`
	} `json:"scripts"`
}) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, "packaging/nfpm/olivares-appliance-base.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	lines := bufio.NewScanner(bytes.NewReader(data))
	for lines.Scan() {
		if !strings.HasPrefix(lines.Text(), "#") {
			body.WriteString(lines.Text() + "\n")
		}
	}
	if err := json.Unmarshal(body.Bytes(), &manifest); err != nil {
		t.Fatalf("the manifest is not the JSON subset this test and nfpm share: %v", err)
	}
	return manifest
}

// productOwned lists the paths the product's package installs (every nfpm dst in
// .goreleaser.yaml) and those its postinstall creates.
func productOwned(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	owned := []string{"/usr/bin/olivares", "/etc/olivares", "/var/lib/olivares", "/usr/lib/olivares"}
	for _, m := range regexp.MustCompile(`(?m)^\s+(?:- )?dst: (/\S+)$`).FindAllStringSubmatch(string(data), -1) {
		owned = append(owned, m[1])
	}
	return owned
}

func TestPackage_ManifestInstallsTheUnitAndBinariesAndOwnsNoProductFile(t *testing.T) {
	manifest := readManifest(t)
	if manifest.Name != "olivares-appliance-base" || len(manifest.Depends) != 0 {
		t.Fatalf("package %q depends on %v", manifest.Name, manifest.Depends)
	}
	want := map[string]struct {
		mode uint32
		typ  string
	}{
		"/usr/bin/appliance-firstboot":                                 {0o755, ""},
		"/usr/bin/appliance-answers":                                   {0o755, ""},
		"/usr/lib/systemd/system/olivares-appliance-firstboot.service": {0o644, ""},
		"/usr/lib/systemd/system/olivares-appliance-readiness.service": {0o644, ""},
		"/etc/issue.d/olivares-appliance.issue":                        {0o644, "config|noreplace"},
		StateDir:                                                       {0o700, "dir"},
	}
	installed := map[string]nfpmContent{}
	for _, c := range manifest.Contents {
		installed[c.Dst] = c
		if c.Src != "" && !strings.HasPrefix(c.Src, "bin/") {
			if _, err := os.Stat(filepath.Join(repoRoot, c.Src)); err != nil {
				t.Fatalf("%s installs a missing source: %v", c.Dst, err)
			}
		}
	}
	for dst, w := range want {
		c, ok := installed[dst]
		if !ok || c.FileInfo.Mode != w.mode || c.Type != w.typ {
			t.Fatalf("%s: installed %+v, want mode %o type %q", dst, c, w.mode, w.typ)
		}
	}
	for _, script := range []string{manifest.Scripts.Postinstall, manifest.Scripts.Preremove} {
		if _, err := os.Stat(filepath.Join(repoRoot, script)); script == "" || err != nil {
			t.Fatalf("maintainer script %q: %v", script, err)
		}
	}

	unit, err := os.ReadFile(filepath.Join(repoRoot, installed["/usr/lib/systemd/system/olivares-appliance-firstboot.service"].Src))
	if err != nil {
		t.Fatal(err)
	}
	for _, directive := range []string{
		"ConditionPathExists=!" + filepath.Join(StateDir, readyFile),
		"ExecStart=/usr/bin/appliance-firstboot apply",
		"StateDirectory=olivares-appliance",
		"ImportCredential=olivares.appliance.answers",
	} {
		if !strings.Contains(string(unit), "\n"+directive+"\n") {
			t.Fatalf("the first-boot unit lacks %q", directive)
		}
	}

	owned := productOwned(t)
	if !strings.Contains(strings.Join(owned, "\n"), "/usr/lib/systemd/system/olivares.service") {
		t.Fatalf("the product's package paths were not read: %v", owned)
	}
	for dst := range installed {
		for _, p := range owned {
			if dst == p || strings.HasPrefix(dst, p+"/") || strings.HasPrefix(p, dst+"/") {
				t.Fatalf("%s overlaps the product package's %s", dst, p)
			}
		}
	}
}

func readRepo(t *testing.T, path string) string {
	t.Helper()
	if path == "" {
		t.Fatal("no path declared")
	}
	data, err := os.ReadFile(filepath.Join(repoRoot, path))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestPackage_ShipsTheDropInDirectoryRecommendsSSHAndKeepsEnablementAcrossReinstall(t *testing.T) {
	manifest := readManifest(t)
	dropIn := false
	for _, c := range manifest.Contents {
		if c.Dst == "/etc/systemd/system/olivares.service.d" {
			dropIn = c.Type == "dir" && c.FileInfo.Mode == 0o755
		}
	}
	if !dropIn {
		t.Fatal("the drop-in directory is not shipped 0755: the unit's UMask=0077 would create it 0700")
	}
	for _, want := range []string{"olivares", "cloud-init", "openssh-server"} {
		if !slices.Contains(manifest.Recommends, want) {
			t.Fatalf("recommends %v lacks %s", manifest.Recommends, want)
		}
	}
	postinstall := readRepo(t, manifest.Scripts.Postinstall)
	preremove := readRepo(t, manifest.Scripts.Preremove)
	postremove := readRepo(t, manifest.Scripts.Postremove)
	const marker = "/var/lib/olivares-appliance/.firstboot-enabled-at-removal"
	if !strings.Contains(preremove, marker) || !strings.Contains(postinstall, marker) {
		t.Fatal("a reinstallation after removal does not restore the first-boot unit's enablement")
	}
	if !strings.Contains(postremove, "systemctl daemon-reload") {
		t.Fatal("no daemon-reload after the unit files are removed")
	}
	if !strings.Contains(readRepo(t, "appliance/layer/base/units/tty1-banner.txt"), "sudo appliance-firstboot status") {
		t.Fatal("the banner sends a console user to a root-only command without sudo")
	}
}

func TestFixture_BaseImageIsPinnedByDigest(t *testing.T) {
	for _, line := range strings.Split(readRepo(t, "appliance/layer/base/fixture/Containerfile"), "\n") {
		if strings.HasPrefix(line, "FROM ") {
			if !regexp.MustCompile(`^FROM debian:13@sha256:[0-9a-f]{64}$`).MatchString(line) {
				t.Fatalf("base image not pinned by digest: %q", line)
			}
			return
		}
	}
	t.Fatal("no FROM line")
}
