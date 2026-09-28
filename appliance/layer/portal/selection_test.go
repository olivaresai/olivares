// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package portal

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// publishedSelection is what first boot publishes for portal.enabled true, portal.listen
// management and host.management_interfaces [eth1, eth0] (the base package's
// TestProductConfig_PublishesThePortalSelectionFromValidatedAnswers pins the same bytes).
const publishedSelection = `{
  "schema_version": "olivares-portal-selection/v1",
  "portal": {
    "enabled": true,
    "listen": "management"
  },
  "host": {
    "management_interfaces": [
      "eth0",
      "eth1"
    ]
  }
}
`

// ownedByRoot makes the owner source report root for the file at path, for the rest of the
// test, so a case needs no privilege to describe the file first boot writes.
func ownedByRoot(t *testing.T, path string) {
	t.Helper()
	target, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	production := fileOwner
	fileOwner = func(info os.FileInfo) (uint32, bool) {
		if os.SameFile(info, target) {
			return 0, true
		}
		return production(info)
	}
	t.Cleanup(func() { fileOwner = production })
}

// publish writes a selection document as first boot does, mode 0644, and returns its path.
func publish(t *testing.T, dir, document string) string {
	t.Helper()
	path := filepath.Join(dir, "selection.json")
	writeFile(t, path, []byte(document), 0o644)
	return path
}

func TestPortalSelection_ReadsOnlyTheRootOwnedPublishedFile(t *testing.T) {
	want := Selection{Enabled: enabled(true), Listen: ListenManagement, ManagementInterfaces: []string{"eth0", "eth1"}}
	same := func(got Selection) bool {
		return got.Enabled != nil && *got.Enabled == *want.Enabled && got.Listen == want.Listen &&
			slices.Equal(got.ManagementInterfaces, want.ManagementInterfaces)
	}
	empty := func(got Selection) bool {
		return got.Enabled == nil && got.Listen == "" && len(got.ManagementInterfaces) == 0
	}

	t.Run("the file first boot publishes, owned by root", func(t *testing.T) {
		path := publish(t, t.TempDir(), publishedSelection)
		ownedByRoot(t, path)
		got, reason := ReadSelection(path)
		if !same(got) || reason != "" {
			t.Fatalf("the published selection read as %+v (%q), want %+v", got, reason, want)
		}
	})

	t.Run("no file is the empty selection, not a refusal", func(t *testing.T) {
		got, reason := ReadSelection(filepath.Join(t.TempDir(), "selection.json"))
		if !empty(got) || reason != "" {
			t.Fatalf("an absent selection read as %+v (%q)", got, reason)
		}
	})

	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string) string
	}{
		{"owned by this service's own user, not root", func(t *testing.T, dir string) string {
			return publish(t, dir, publishedSelection)
		}},
		{"owned by another user", func(t *testing.T, dir string) string {
			path := publish(t, dir, publishedSelection)
			ownedByAnotherUser(t, path)
			return path
		}},
		{"writable by its group", func(t *testing.T, dir string) string {
			path := publish(t, dir, publishedSelection)
			ownedByRoot(t, path)
			chmod(t, path, 0o664)
			return path
		}},
		{"writable by others", func(t *testing.T, dir string) string {
			path := publish(t, dir, publishedSelection)
			ownedByRoot(t, path)
			chmod(t, path, 0o646)
			return path
		}},
		{"a symbolic link to a root-owned selection", func(t *testing.T, dir string) string {
			elsewhere := publish(t, t.TempDir(), publishedSelection)
			ownedByRoot(t, elsewhere)
			link := filepath.Join(dir, "selection.json")
			symlink(t, elsewhere, link)
			return link
		}},
		{"a directory", func(t *testing.T, dir string) string {
			path := filepath.Join(dir, "selection.json")
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatal(err)
			}
			ownedByRoot(t, path)
			return path
		}},
	} {
		t.Run("refuses a file "+tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := tc.setup(t, dir)
			got, reason := ReadSelection(path)
			if !empty(got) || reason == "" {
				t.Fatalf("a selection %s read as %+v (%q), want the empty selection and a reason", tc.name, got, reason)
			}
			if strings.Contains(reason, dir) || strings.Contains(reason, "eth0") {
				t.Fatalf("the reason carries a path or a value: %q", reason)
			}
		})
	}

	for _, tc := range []struct{ name, document string }{
		{"not JSON", "portal enabled\n"},
		{"another schema", strings.Replace(publishedSelection, "olivares-portal-selection/v1", "olivares-portal-selection/v2", 1)},
		{"no schema", strings.Replace(publishedSelection, `"schema_version": "olivares-portal-selection/v1",`, "", 1)},
		{"an unknown field", strings.Replace(publishedSelection, `"listen": "management"`, `"listen": "management", "bind": "DO-NOT-PRINT"`, 1)},
		{"a wildcard listen", strings.Replace(publishedSelection, `"listen": "management"`, `"listen": "0.0.0.0"`, 1)},
		{"management without interfaces", strings.Replace(publishedSelection, `"eth0",
      "eth1"`, "", 1)},
		{"an interface that is not a name", strings.Replace(publishedSelection, `"eth1"`, `"../DO-NOT-PRINT"`, 1)},
		{"a repeated interface", strings.Replace(publishedSelection, `"eth1"`, `"eth0"`, 1)},
		{"a null", strings.Replace(publishedSelection, `"enabled": true`, `"enabled": null`, 1)},
		{"a second document", publishedSelection + "{}\n"},
		{"more than 4 KiB", publishedSelection + strings.Repeat(" ", 4096)},
	} {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			path := publish(t, t.TempDir(), tc.document)
			ownedByRoot(t, path)
			got, reason := ReadSelection(path)
			if !empty(got) || reason == "" {
				t.Fatalf("%s read as %+v (%q), want the empty selection and a reason", tc.name, got, reason)
			}
			if strings.Contains(reason, "DO-NOT-PRINT") {
				t.Fatalf("the reason echoes a value: %q", reason)
			}
		})
	}
}

func TestServe_SnapshotUsesThePublishedSelection(t *testing.T) {
	if productionFiles != (hostFiles{
		selection:   "/etc/olivares-portal/selection.json",
		measurement: "/run/olivares-firewall/measured.json",
		bootID:      "/proc/sys/kernel/random/boot_id",
	}) {
		t.Fatalf("the console reads %+v, not the files first boot and the firewall owner publish", productionFiles)
	}

	tlsDir := t.TempDir()
	writeTLSPair(t, tlsDir, 0o600)
	env := environment(tlsDir, tlsDir)
	getenv := func(key string) string { return env[key] }

	t.Run("a published portal.enabled false turns the console off", func(t *testing.T) {
		dir := t.TempDir()
		files := hostFiles{
			selection:   publish(t, dir, "{\n  \"schema_version\": \"olivares-portal-selection/v1\",\n  \"portal\": {\n    \"enabled\": false\n  }\n}\n"),
			measurement: filepath.Join(dir, "measured.json"),
			bootID:      filepath.Join(dir, "boot_id"),
		}
		ownedByRoot(t, files.selection)
		var log strings.Builder
		if code := run(getenv, os.Getpid(), &log, files, nil); code != 1 {
			t.Fatalf("run exited %d", code)
		}
		if !strings.Contains(log.String(), "disabled: portal.enabled is false") {
			t.Fatalf("the console did not decide on the published selection:\n%s", log.String())
		}

		// Control: the same host without a published selection is not disabled.
		files.selection = filepath.Join(t.TempDir(), "selection.json")
		log.Reset()
		run(getenv, os.Getpid(), &log, files, nil)
		if strings.Contains(log.String(), "disabled") {
			t.Fatalf("control: without a selection the console was disabled:\n%s", log.String())
		}
	})

	t.Run("the snapshot carries the published selection and reads the firewall measurement", func(t *testing.T) {
		dir := t.TempDir()
		files := hostFiles{
			selection:   publish(t, dir, publishedSelection),
			measurement: filepath.Join(dir, "measured.json"),
			bootID:      writeBootID(t, dir),
		}
		ownedByRoot(t, files.selection)
		custody := verifiedCustody(t)

		status := files.snapshot(custody, func(string, ...any) {})
		if status.Selection.Listen != ListenManagement || !slices.Equal(status.Selection.ManagementInterfaces, []string{"eth0", "eth1"}) {
			t.Fatalf("the snapshot's selection is %+v, not the published one", status.Selection)
		}
		if status.Listen.Mode != LocalOnly || !slices.Equal(status.Listen.Reasons, []string{"firewall prerequisite is unmeasured"}) {
			t.Fatalf("with no measurement the published selection waits for the firewall only: %+v", status.Listen)
		}

		writeMeasurement(t, files.measurement, measurement(testBootID, "drop", row("9443/tcp", "eth0", "eth1")))
		ownedByRoot(t, files.measurement)
		status = files.snapshot(custody, func(string, ...any) {})
		if status.Listen.Mode != Remote || !status.Listen.Serves(bound("192.0.2.10", "eth1")) {
			t.Fatalf("the snapshot did not read the firewall owner's measurement: %+v", status.Listen)
		}
	})
}
