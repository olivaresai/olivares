// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package base

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
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
// packaging/nfpm/packages.json) and those its postinstall creates.
func productOwned(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, "packaging/nfpm/packages.json"))
	if err != nil {
		t.Fatal(err)
	}
	owned := []string{"/usr/bin/olivares", "/etc/olivares", "/var/lib/olivares", "/usr/lib/olivares"}
	var recipes struct {
		Nfpms []struct {
			Contents []nfpmContent `json:"contents"`
		} `json:"nfpms"`
	}
	if err := json.Unmarshal(data, &recipes); err != nil {
		t.Fatal(err)
	}
	if len(recipes.Nfpms) != 2 {
		t.Fatalf("native recipe inventory: got %d, want 2", len(recipes.Nfpms))
	}
	for _, recipe := range recipes.Nfpms {
		for _, content := range recipe.Contents {
			owned = append(owned, content.Dst)
		}
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

func TestPackage_VisibleHostNameIsOlivaresServer(t *testing.T) {
	banner := readRepo(t, "appliance/layer/base/units/tty1-banner.txt")
	if first := strings.SplitN(banner, "\n", 2)[0]; first != `Olivares Server \n` {
		t.Fatalf("banner starts with %q, want Olivares Server and the host name", first)
	}
	for path, want := range map[string]string{
		firstBootUnitPath: "Olivares Server first boot",
		readinessUnitPath: "Olivares Server first-boot readiness",
	} {
		unit := parseUnit(readRepo(t, path))
		if got := unit["Description"]; len(got) != 1 || got[0] != want {
			t.Errorf("%s descriptions %q, want %q", path, got, want)
		}
	}
}

// The maintainer scripts run under dpkg's action words and rpm's instance counts alike: the
// harness beside them drives each through installation, upgrade, removal and reinstallation
// under both conventions and both /bin/sh the targets use.
func TestPackage_MaintainerScriptsBehaveAlikeUnderDpkgAndRpmArguments(t *testing.T) {
	out, err := exec.Command("bash", "package/maintainer-scripts-test.sh").CombinedOutput()
	if err != nil {
		t.Fatalf("the maintainer scripts do not hold under both conventions: %v\n%s", err, out)
	}
}

// The base package is built for both of its targets from the one manifest: rpm for the Fedora
// image, deb for Debian as an installation target.
func TestPackage_TheBaseTaskBuildsAnRpmAndADebFromTheOneManifest(t *testing.T) {
	_, task, ok := strings.Cut(readRepo(t, "Taskfile.yml"), "\n  appliance:package:base:\n")
	if !ok {
		t.Fatal("Taskfile.yml has no appliance:package:base task")
	}
	if end := strings.Index(task, "\n\n"); end >= 0 {
		task = task[:end]
	}
	for _, packager := range []string{"rpm", "deb"} {
		want := "--config packaging/nfpm/olivares-appliance-base.yaml --packager " + packager +
			" --target dist/olivares-appliance-base." + packager
		if strings.Count(task, want) != 1 {
			t.Errorf("appliance:package:base does not build the %s once: want %q in\n%s", packager, want, task)
		}
	}
}

// The appliance-a1 workflow runs the fixture for both families, each with its own package format
// and tools: the harness beside the fixture reads the workflow's two jobs and its pinned actions.
func TestFixture_TheWorkflowRunsTheDebAndTheRpmLegs(t *testing.T) {
	out, err := exec.Command("bash", "fixture/workflow-legs-test.sh").CombinedOutput()
	if err != nil {
		t.Fatalf("appliance-a1 does not run both legs as designed: %v\n%s", err, out)
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

// Each fixture family builds from its own image, pinned by digest, and installs with its own
// package tools: Debian 13 with apt-get and dpkg, Fedora 44 with dnf and rpm. Neither runs the
// other's.
func TestFixture_EachFamilyIsPinnedByDigestAndUsesOnlyItsOwnPackageTools(t *testing.T) {
	for _, family := range []struct {
		file, from   string
		own, foreign []string
	}{
		{"Containerfile", `^FROM debian:13@sha256:[0-9a-f]{64}$`, []string{"apt-get install", "dpkg -i "}, []string{"dnf", "rpm "}},
		{"fedora/Containerfile", `^FROM fedora:44@sha256:[0-9a-f]{64}$`, []string{"dnf -y", "rpm -i "}, []string{"apt-get", "dpkg"}},
	} {
		var froms, instructions []string
		for _, line := range strings.Split(readRepo(t, "appliance/layer/base/fixture/"+family.file), "\n") {
			if strings.HasPrefix(line, "#") {
				continue
			}
			if strings.HasPrefix(line, "FROM ") {
				froms = append(froms, line)
			}
			instructions = append(instructions, line)
		}
		if len(froms) != 1 || !regexp.MustCompile(family.from).MatchString(froms[0]) {
			t.Errorf("%s: FROM lines %q, want one matching %s", family.file, froms, family.from)
		}
		text := strings.Join(instructions, "\n")
		for _, tool := range family.own {
			if !strings.Contains(text, tool) {
				t.Errorf("%s does not use %q", family.file, tool)
			}
		}
		for _, tool := range family.foreign {
			if strings.Contains(text, tool) {
				t.Errorf("%s runs %q, the other family's tool", family.file, tool)
			}
		}
	}
}

// The battery removes the base package with the booted family's own tool, chosen by the
// container's /etc/os-release: dpkg on Debian, rpm on Fedora. Nowhere else does it remove it.
func TestFixture_TheBatteryRemovesTheBasePackageWithTheFamilysOwnTool(t *testing.T) {
	battery := readRepo(t, "appliance/layer/base/fixture/firstboot-battery.sh")
	_, body, ok := strings.Cut(battery, "\nremove_base() {\n")
	if !ok {
		t.Fatal("the battery has no remove_base function")
	}
	body, _, _ = strings.Cut(body, "\n}\n")
	for _, want := range []string{"/etc/os-release", "debian)", "dpkg --remove olivares-appliance-base", "fedora)",
		"rpm --erase olivares-appliance-base"} {
		if !strings.Contains(body, want) {
			t.Errorf("remove_base lacks %q", want)
		}
	}
	if strings.Count(battery, "dpkg --remove") != 1 || strings.Count(battery, "rpm --erase") != 1 {
		t.Error("the battery removes the base package outside remove_base")
	}
}
