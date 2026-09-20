// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package localinstall

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func witnessOnDisk(t *testing.T, root string, m *Manifest) (string, bool) {
	t.Helper()
	target := filepath.Join(root, WitnessPath(m)[1:])
	body, err := os.ReadFile(target)
	if errors.Is(err, os.ErrNotExist) {
		return target, false
	}
	if err != nil {
		t.Fatal(err)
	}
	var w Witness
	if err := json.Unmarshal(body, &w); err != nil {
		t.Fatalf("witness is not JSON: %v", err)
	}
	if w.Schema != WitnessSchema || w.DataDir != m.DataDir || w.Unit != m.Unit() {
		t.Fatalf("witness describes %+v, want %s / %s", w, m.DataDir, m.Unit())
	}
	return target, true
}

// legacyManifest is the shape packaging/nfpm/postinstall.sh and the first
// install-service.sh producers wrote: no layout, no workspace, default tuple.
func legacyManifest() *Manifest {
	return &Manifest{
		Schema: ManifestSchema, Mode: "system", Init: "systemd",
		DataDir: "/var/lib/olivares", Config: "/etc/olivares/olivares.env",
		Files: []File{
			{Path: "/usr/bin/olivares", Role: "binary", Mode: "0755"},
			{Path: "/etc/olivares/olivares.env", Role: "config", Mode: "0640"},
			{Path: "/usr/lib/systemd/system/olivares.service", Role: "unit", Mode: "0644"},
		},
		Account:      Account{User: "olivares", Group: "olivares"},
		ManifestPath: "/var/lib/olivares/install-manifest.json",
	}
}

// customManifest is what install-service.sh --data-dir plus install-agentops.sh
// record for a custom data root with an external workspace.
func customManifest(dataDir string) *Manifest {
	return &Manifest{
		Schema: ManifestSchema, Mode: "system", Init: "systemd", Layout: LayoutCustom,
		DataDir: dataDir, Config: "/etc/olivares/olivares.env", WorkspaceDir: "/mnt/workspaces",
		Files: []File{
			{Path: "/usr/local/bin/olivares", Role: "binary", Mode: "0755", Managed: true},
			{Path: "/etc/olivares/olivares.env", Role: "config", Mode: "0640", Managed: true},
			{Path: "/etc/systemd/system/olivares.service", Role: "unit", Mode: "0644", Managed: true},
			{Path: "/etc/systemd/system/olivares.service.d/agentops.conf", Role: RoleDropin, Mode: "0644", Managed: true},
			{Path: "/etc/olivares/agentops.env", Role: RoleRuntimeEnv, Mode: "0640", Managed: true},
		},
		Account:      Account{User: "olivares", Group: "olivares", UserCreated: true, GroupCreated: true},
		ManifestPath: filepath.Join(dataDir, "install-manifest.json"),
	}
}

func TestValidateLegacyDefaultManifestWithoutLayoutField(t *testing.T) {
	m := legacyManifest()
	if err := Validate(m, "/root"); err != nil {
		t.Fatalf("legacy default manifest must keep validating: %v", err)
	}
	m.Layout = LayoutDefault
	if err := Validate(m, "/root"); err != nil {
		t.Fatalf("explicit default layout must validate: %v", err)
	}
	// Without the custom marker a custom data directory is still outside the
	// closed tuple: the producer opts in explicitly, never by accident.
	m.Layout = ""
	m.DataDir = "/srv/olivares"
	m.ManifestPath = "/srv/olivares/install-manifest.json"
	err := Validate(m, "/root")
	if err == nil || !strings.Contains(err.Error(), "absent from release-index install_layout") {
		t.Fatalf("unmarked custom data dir was not refused by the closed layout: %v", err)
	}
}

func TestValidateCustomLayoutPolicy(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Manifest)
		wantErr string
	}{
		{"dedicated custom data dir is admitted", func(*Manifest) {}, ""},
		{"default data dir under custom marker is admitted", func(m *Manifest) {
			m.DataDir = "/var/lib/olivares"
			m.ManifestPath = "/var/lib/olivares/install-manifest.json"
		}, ""},
		{"path with a space is admitted", func(m *Manifest) {
			m.DataDir = "/srv/olivares data"
			m.ManifestPath = "/srv/olivares data/install-manifest.json"
		}, ""},
		{"top-level directory is refused", func(m *Manifest) {
			m.DataDir = "/srv"
			m.ManifestPath = "/srv/install-manifest.json"
		}, "at least two levels deep"},
		{"trailing slash is not clean", func(m *Manifest) {
			m.DataDir = "/srv/olivares/"
			m.ManifestPath = "/srv/olivares/install-manifest.json"
		}, "clean, absolute and non-root"},
		{"traversal is not clean", func(m *Manifest) {
			m.DataDir = "/srv/olivares/../etc"
			m.ManifestPath = "/srv/olivares/../etc/install-manifest.json"
		}, "clean, absolute and non-root"},
		{"relative path is refused", func(m *Manifest) {
			m.DataDir = "srv/olivares"
			m.ManifestPath = "srv/olivares/install-manifest.json"
		}, "clean, absolute and non-root"},
		{"control characters are refused", func(m *Manifest) {
			m.DataDir = "/srv/oliv\nares"
			m.ManifestPath = "/srv/oliv\nares/install-manifest.json"
		}, "control characters"},
		{"data dir containing the config is refused", func(m *Manifest) {
			m.DataDir = "/etc/olivares"
			m.ManifestPath = "/etc/olivares/install-manifest.json"
		}, "must not contain the config path"},
		{"data dir containing the unit is refused", func(m *Manifest) {
			m.DataDir = "/etc/systemd"
			m.ManifestPath = "/etc/systemd/install-manifest.json"
		}, "must not contain the unit path"},
		{"unknown layout word is refused", func(m *Manifest) { m.Layout = "anywhere" }, "unexpected install layout"},
		{"binary stays inside the closed layout", func(m *Manifest) {
			m.Files[0].Path = "/srv/olivares-bin/olivares"
		}, "absent from release-index install_layout"},
		{"config stays inside the closed layout", func(m *Manifest) {
			m.Config = "/srv/olivares.env"
			m.Files[1].Path = "/srv/olivares.env"
			m.Files[4].Path = "/srv/agentops.env"
		}, "absent from release-index install_layout"},
		{"unit stays inside the closed layout", func(m *Manifest) {
			m.Files[2].Path = "/etc/systemd/system/custom.service"
			m.Files[3].Path = "/etc/systemd/system/custom.service.d/agentops.conf"
		}, "absent from release-index install_layout"},
		{"drop-in path is derived from the unit", func(m *Manifest) {
			m.Files[3].Path = "/etc/systemd/system/olivares.service.d/99-local.conf"
		}, "unexpected AgentOps drop-in path"},
		{"drop-in requires systemd", func(m *Manifest) {
			m.Init = "openrc"
			m.Files[2].Path = "/etc/init.d/olivares"
			m.Files[3].Path = "/etc/init.d/olivares.d/agentops.conf"
		}, "requires the systemd adapter"},
		{"second drop-in is refused", func(m *Manifest) {
			m.Files = append(m.Files, File{Path: "/etc/systemd/system/olivares.service.d/agentops.conf.bak", Role: RoleDropin, Mode: "0644"})
		}, "more than one AgentOps drop-in"},
		{"runtime env path is derived from the config", func(m *Manifest) {
			m.Files[4].Path = "/etc/olivares/other.env"
		}, "unexpected runtime env path"},
		{"unclean workspace is refused", func(m *Manifest) { m.WorkspaceDir = "/mnt/workspaces/" }, "unexpected workspace path"},
		{"relative workspace is refused", func(m *Manifest) { m.WorkspaceDir = "workspaces" }, "unexpected workspace path"},
		{"root workspace is refused", func(m *Manifest) { m.WorkspaceDir = "/" }, "unexpected workspace path"},
		{"workspace inside the data dir is admitted", func(m *Manifest) { m.WorkspaceDir = "/srv/olivares/workspaces" }, ""},
		{"absent workspace is admitted", func(m *Manifest) { m.WorkspaceDir = "" }, ""},
		{"manifest must live inside the custom data dir", func(m *Manifest) {
			m.ManifestPath = "/var/lib/olivares/install-manifest.json"
		}, "unexpected manifest path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := customManifest("/srv/olivares")
			tc.mutate(m)
			err := Validate(m, "/root")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected refusal: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoadRejectsUnknownFieldsButAcceptsOptionalOnes(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	legacy, err := json.Marshal(legacyManifest())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(write("legacy.json", string(legacy)), false); err != nil {
		t.Fatalf("legacy manifest without optional fields: %v", err)
	}
	custom, err := json.Marshal(customManifest("/srv/olivares"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(write("custom.json", string(custom)), false); err != nil {
		t.Fatalf("custom manifest with optional fields: %v", err)
	}
	forged := strings.Replace(string(custom), `"layout"`, `"purge_also"`, 1)
	if _, err := Load(write("forged.json", forged), false); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field was not refused: %v", err)
	}
}

func TestBuildPlanDisclosesAgentOpsRolesAndWorkspace(t *testing.T) {
	m := customManifest("/srv/olivares")
	want := func(op Operation, role, action string) {
		t.Helper()
		items, err := BuildPlan(m, op, "/root")
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			if item.Role == role {
				if item.Action != action {
					t.Fatalf("%s %s = %s, want %s", op, role, item.Action, action)
				}
				return
			}
		}
		t.Fatalf("%s plan omitted role %s", op, role)
	}
	want(Preserve, RoleDropin, "remove")
	want(Preserve, RoleRuntimeEnv, "keep")
	want(Purge, RoleDropin, "remove")
	want(Purge, RoleRuntimeEnv, "remove")
	want(Plan, "workspace", "keep")
	want(Purge, "workspace", "keep")
	m.WorkspaceDir = "/srv/olivares/workspaces"
	want(Preserve, "workspace", "keep")
	want(Purge, "workspace", "remove-tree")
	m.Files[3].Managed = false
	want(Preserve, RoleDropin, "keep")
}

// stage lays a custom-layout installation under an offline root, with the
// unit naming the data directory the way install-service.sh renders it.
func stage(t *testing.T, m *Manifest, execStart string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"/usr/local/bin/olivares":                              "binary",
		"/etc/olivares/olivares.env":                           "OLIVARES_EXTRA_ARGS=\n",
		"/etc/olivares/agentops.env":                           "OLIVARES_SESSION_RUNTIME_TOKEN_FILE=" + m.DataDir + "/run/session-token\n",
		"/etc/systemd/system/olivares.service":                 "[Service]\n" + execStart + "\nReadWritePaths=\"" + m.DataDir + "\"\n",
		"/etc/systemd/system/olivares.service.d/agentops.conf": "[Service]\nEnvironment=HOME=" + m.DataDir + "/claude-home\n",
		m.DataDir + "/olivares.db":                             "store",
		m.DataDir + "/audit-signing.key":                       "key",
		"/mnt/workspaces/project/README":                       "operator data",
	}
	for logical, body := range files {
		name := filepath.Join(root, logical[1:])
		if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, m.ManifestPath[1:]), body, 0o640); err != nil {
		t.Fatal(err)
	}
	return root
}

func exists(t *testing.T, root, logical string) bool {
	t.Helper()
	_, err := os.Lstat(filepath.Join(root, logical[1:]))
	if err == nil {
		return true
	}
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	t.Fatal(err)
	return false
}

func TestExecuteRefusesUncorroboratedCustomLayoutBeforeAnyMutation(t *testing.T) {
	m := customManifest("/srv/olivares")
	root := stage(t, m, "ExecStart=/usr/local/bin/olivares serve --data-dir=/srv/olivares-old --listen=127.0.0.1:8443")
	for _, op := range []Operation{Plan, Preserve, Purge} {
		var out strings.Builder
		err := Execute(m, Options{Operation: op, Root: root, Out: &out})
		if err == nil || !strings.Contains(err.Error(), "does not name data directory") {
			t.Fatalf("%s: uncorroborated custom layout was not refused: %v", op, err)
		}
		if out.Len() != 0 {
			t.Fatalf("%s: plan was disclosed before corroboration:\n%s", op, out.String())
		}
	}
	for _, kept := range []string{"/usr/local/bin/olivares", "/etc/systemd/system/olivares.service",
		"/etc/systemd/system/olivares.service.d/agentops.conf", "/srv/olivares/olivares.db"} {
		if !exists(t, root, kept) {
			t.Fatalf("refusal mutated %s", kept)
		}
	}
}

func TestExecuteRefusesUnitLinkAndSiblingPrefixAsCorroboration(t *testing.T) {
	m := customManifest("/srv/olivares")
	root := stage(t, m, "ExecStart=/usr/local/bin/olivares serve --data-dir=/srv/olivares --listen=127.0.0.1:8443")
	unit := filepath.Join(root, "etc/systemd/system/olivares.service")
	real := unit + ".real"
	if err := os.Rename(unit, real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, unit); err != nil {
		t.Fatal(err)
	}
	err := Execute(m, Options{Operation: Plan, Root: root})
	if err == nil || !strings.Contains(err.Error(), "not a link") {
		t.Fatalf("linked unit was accepted as a witness: %v", err)
	}
	if err := os.Remove(unit); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(real, unit); err != nil {
		t.Fatal(err)
	}
	if err := Execute(m, Options{Operation: Plan, Root: root}); err != nil {
		t.Fatalf("regular unit naming the data dir must corroborate: %v", err)
	}
}

func TestExecuteCustomLayoutPreserveAndPurgeStayInsideTheRecordedEstate(t *testing.T) {
	forms := []string{
		`ExecStart=/usr/local/bin/olivares serve --data-dir=/srv/olivares --listen=127.0.0.1:8443`,
		`ExecStart=/usr/local/bin/olivares serve --data-dir="/srv/olivares" --listen=127.0.0.1:8443`,
		`ExecStart=/usr/local/bin/olivares serve "--data-dir=/srv/olivares" --listen=127.0.0.1:8443`,
	}
	for _, form := range forms {
		m := customManifest("/srv/olivares")
		root := stage(t, m, form)
		// A second operator drop-in keeps the drop-in directory alive.
		other := filepath.Join(root, "etc/systemd/system/olivares.service.d/50-local.conf")
		if err := os.WriteFile(other, []byte("[Service]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		if err := Execute(m, Options{Operation: Preserve, Root: root, Out: &out}); err != nil {
			t.Fatalf("preserve (%s): %v\n%s", form, err, out.String())
		}
		for _, gone := range []string{"/usr/local/bin/olivares", "/etc/systemd/system/olivares.service",
			"/etc/systemd/system/olivares.service.d/agentops.conf"} {
			if exists(t, root, gone) {
				t.Fatalf("preserve left managed %s", gone)
			}
		}
		for _, kept := range []string{"/etc/olivares/olivares.env", "/etc/olivares/agentops.env",
			"/etc/systemd/system/olivares.service.d/50-local.conf", "/srv/olivares/olivares.db",
			"/srv/olivares/audit-signing.key", "/srv/olivares/install-manifest.json", "/mnt/workspaces/project/README"} {
			if !exists(t, root, kept) {
				t.Fatalf("preserve removed retained %s", kept)
			}
		}
	}

	m := customManifest("/srv/olivares")
	root := stage(t, m, forms[0])
	if err := Execute(m, Options{Operation: Purge, Root: root}); err != nil {
		t.Fatalf("purge: %v", err)
	}
	for _, gone := range []string{"/usr/local/bin/olivares", "/etc/systemd/system/olivares.service",
		"/etc/systemd/system/olivares.service.d", "/etc/olivares/olivares.env", "/etc/olivares/agentops.env", "/srv/olivares"} {
		if exists(t, root, gone) {
			t.Fatalf("purge left %s", gone)
		}
	}
	if !exists(t, root, "/mnt/workspaces/project/README") {
		t.Fatal("purge crossed into the explicitly selected external workspace")
	}
}

func TestPurgeUnlinksWorkspaceLinkInsideDataDirWithoutFollowingIt(t *testing.T) {
	m := customManifest("/srv/olivares")
	m.WorkspaceDir = "/srv/olivares/workspaces"
	root := stage(t, m, `ExecStart=/usr/local/bin/olivares serve --data-dir=/srv/olivares --listen=127.0.0.1:8443`)
	link := filepath.Join(root, "srv/olivares/workspaces")
	if err := os.Symlink(filepath.Join(root, "mnt/workspaces"), link); err != nil {
		t.Fatal(err)
	}
	if err := Execute(m, Options{Operation: Purge, Root: root}); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if exists(t, root, "/srv/olivares") {
		t.Fatal("purge left the data tree")
	}
	if !exists(t, root, "/mnt/workspaces/project/README") {
		t.Fatal("purge followed the workspace link out of the data tree")
	}
}

// The AgentOps installer battery writes these manifests through the real
// POSIX annotation code and compares them byte for byte. Loading them here
// proves the shell producer and the Go consumer agree on one contract.
func TestGoldenAgentOpsManifestsFromInstallerBatteryValidate(t *testing.T) {
	for _, name := range []string{"manifest-default.json", "manifest-custom.json"} {
		m, err := Load(filepath.Join("testdata", name), false)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if m.WorkspaceDir == "" || m.Unit() == "" {
			t.Fatalf("%s: workspace or unit missing from the annotated manifest", name)
		}
		var dropin, env bool
		for _, f := range m.Files {
			switch f.Role {
			case RoleDropin:
				dropin = f.Path == DropinPath(m.Unit())
			case RoleRuntimeEnv:
				env = f.Path == RuntimeEnvPath(m.Config)
			}
		}
		if !dropin || !env {
			t.Fatalf("%s: AgentOps drop-in/runtime env not recorded at their derived paths: %+v", name, m.Files)
		}
		if strings.HasSuffix(name, "custom.json") && (!m.Custom() || m.DataDir == "/var/lib/olivares") {
			t.Fatalf("%s: custom golden lost its custom layout: %+v", name, m)
		}
	}
}

// TestCustomLayoutPreserveThenPlanThenPurgeOnOneRoot is the sequence the
// independent review reproduced as a dead end: after preserve removed the
// unit, plan and purge must still work on the SAME root, through the witness
// preserve recorded beside the config, and purge must leave nothing behind.
func TestCustomLayoutPreserveThenPlanThenPurgeOnOneRoot(t *testing.T) {
	m := customManifest("/srv/olivares")
	root := stage(t, m, `ExecStart=/usr/local/bin/olivares serve --data-dir=/srv/olivares --listen=127.0.0.1:8443`)

	var out strings.Builder
	if err := Execute(m, Options{Operation: Preserve, Root: root, Out: &out}); err != nil {
		t.Fatalf("preserve: %v\n%s", err, out.String())
	}
	if !planRow(out.String(), "record", "witness") {
		t.Fatalf("preserve plan did not disclose the witness:\n%s", out.String())
	}
	if exists(t, root, "/etc/systemd/system/olivares.service") {
		t.Fatal("preserve kept the unit")
	}
	if _, ok := witnessOnDisk(t, root, m); !ok {
		t.Fatal("preserve did not record the uninstall witness")
	}

	out.Reset()
	if err := Execute(m, Options{Operation: Plan, Root: root, Out: &out}); err != nil {
		t.Fatalf("plan after preserve: %v", err)
	}
	if !planRow(out.String(), "keep", "witness") || !strings.Contains(out.String(), "/mnt/workspaces") {
		t.Fatalf("plan after preserve is incomplete:\n%s", out.String())
	}
	// A second preserve is a no-op that keeps the estate and the witness.
	if err := Execute(m, Options{Operation: Preserve, Root: root}); err != nil {
		t.Fatalf("repeated preserve: %v", err)
	}
	if !exists(t, root, "/srv/olivares/olivares.db") {
		t.Fatal("repeated preserve removed data")
	}

	if err := Execute(m, Options{Operation: Purge, Root: root}); err != nil {
		t.Fatalf("purge after preserve: %v", err)
	}
	for _, gone := range []string{"/srv/olivares", "/etc/olivares/olivares.env", "/etc/olivares/agentops.env", "/etc/systemd/system/olivares.service.d"} {
		if exists(t, root, gone) {
			t.Fatalf("purge left %s", gone)
		}
	}
	if _, ok := witnessOnDisk(t, root, m); ok {
		t.Fatal("purge left the uninstall witness behind")
	}
	if !exists(t, root, "/mnt/workspaces/project/README") {
		t.Fatal("purge crossed into the external workspace")
	}
}

// TestCustomLayoutPurgeInterruptedIsRetryable: the unit is removed before the
// data tree, so a purge that fails inside the tree must leave the manifest
// and the witness for the retry, and the retry must finish the job.
func TestCustomLayoutPurgeInterruptedIsRetryable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based interruption cannot be staged as root")
	}
	m := customManifest("/srv/olivares")
	root := stage(t, m, `ExecStart=/usr/local/bin/olivares serve --data-dir=/srv/olivares --listen=127.0.0.1:8443`)
	locked := filepath.Join(root, "srv/olivares/locked")
	if err := os.MkdirAll(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "busy"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	err := Execute(m, Options{Operation: Purge, Root: root})
	if err == nil || !strings.Contains(err.Error(), "purge data directory") {
		t.Fatalf("interrupted purge did not report the tree failure: %v", err)
	}
	if exists(t, root, "/etc/systemd/system/olivares.service") {
		t.Fatal("interrupted purge premise is wrong: unit still present")
	}
	if !exists(t, root, "/srv/olivares/install-manifest.json") {
		t.Fatal("interrupted purge removed the manifest before the tree was gone")
	}
	if _, ok := witnessOnDisk(t, root, m); !ok {
		t.Fatal("interrupted purge left no witness for the retry")
	}
	if err := Execute(m, Options{Operation: Plan, Root: root}); err != nil {
		t.Fatalf("plan after interrupted purge: %v", err)
	}

	if err := os.Chmod(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Execute(m, Options{Operation: Purge, Root: root}); err != nil {
		t.Fatalf("retry purge: %v", err)
	}
	if exists(t, root, "/srv/olivares") {
		t.Fatal("retry left the data tree")
	}
	if _, ok := witnessOnDisk(t, root, m); ok {
		t.Fatal("retry left the witness")
	}
}

func TestCustomLayoutWithoutUnitAndWithoutWitnessIsRefusedActionably(t *testing.T) {
	m := customManifest("/srv/olivares")
	root := stage(t, m, `ExecStart=/usr/local/bin/olivares serve --data-dir=/srv/olivares --listen=127.0.0.1:8443`)
	// The operator (or a package) removed the unit; this product recorded nothing.
	if err := os.Remove(filepath.Join(root, "etc/systemd/system/olivares.service")); err != nil {
		t.Fatal(err)
	}
	for _, op := range []Operation{Plan, Preserve, Purge} {
		err := Execute(m, Options{Operation: op, Root: root})
		if err == nil {
			t.Fatalf("%s without unit or witness was accepted", op)
		}
		for _, want := range []string{"no uninstall witness", "Re-run the signed installer"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%s refusal is not actionable: %v", op, err)
			}
		}
	}
	if !exists(t, root, "/srv/olivares/olivares.db") || !exists(t, root, "/usr/local/bin/olivares") {
		t.Fatal("refusal mutated the estate")
	}
}

func TestCustomLayoutWitnessIsNotSufficientWhenForgedOrLinked(t *testing.T) {
	m := customManifest("/srv/olivares")
	write := func(t *testing.T, root string, body string) string {
		t.Helper()
		target := filepath.Join(root, WitnessPath(m)[1:])
		if err := os.WriteFile(target, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
		return target
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, root string)
		want  string
	}{
		{"witness for another data directory", func(t *testing.T, root string) {
			write(t, root, `{"schema":"olivares.ai/uninstall-witness/v1","data_dir":"/srv/other","unit":"/etc/systemd/system/olivares.service","removed_by":"preserve","recorded_at":"2026-09-05T12:00:00Z"}`)
		}, "does not describe data directory"},
		{"witness for another unit", func(t *testing.T, root string) {
			write(t, root, `{"schema":"olivares.ai/uninstall-witness/v1","data_dir":"/srv/olivares","unit":"/etc/init.d/olivares","removed_by":"preserve","recorded_at":"2026-09-05T12:00:00Z"}`)
		}, "does not describe data directory"},
		{"unknown removal operation", func(t *testing.T, root string) {
			write(t, root, `{"schema":"olivares.ai/uninstall-witness/v1","data_dir":"/srv/olivares","unit":"/etc/systemd/system/olivares.service","removed_by":"operator","recorded_at":"2026-09-05T12:00:00Z"}`)
		}, "does not describe data directory"},
		{"extra field", func(t *testing.T, root string) {
			write(t, root, `{"schema":"olivares.ai/uninstall-witness/v1","data_dir":"/srv/olivares","unit":"/etc/systemd/system/olivares.service","removed_by":"preserve","recorded_at":"2026-09-05T12:00:00Z","also":"/home"}`)
		}, "malformed"},
		{"no timestamp", func(t *testing.T, root string) {
			write(t, root, `{"schema":"olivares.ai/uninstall-witness/v1","data_dir":"/srv/olivares","unit":"/etc/systemd/system/olivares.service","removed_by":"preserve","recorded_at":""}`)
		}, "recorded_at"},
		{"witness is a link", func(t *testing.T, root string) {
			real := write(t, root, `{"schema":"olivares.ai/uninstall-witness/v1","data_dir":"/srv/olivares","unit":"/etc/systemd/system/olivares.service","removed_by":"preserve","recorded_at":"2026-09-05T12:00:00Z"}`)
			if err := os.Rename(real, real+".real"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real+".real", real); err != nil {
				t.Fatal(err)
			}
		}, "not a link"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := stage(t, m, `ExecStart=/usr/local/bin/olivares serve --data-dir=/srv/olivares --listen=127.0.0.1:8443`)
			if err := os.Remove(filepath.Join(root, "etc/systemd/system/olivares.service")); err != nil {
				t.Fatal(err)
			}
			tc.setup(t, root)
			err := Execute(m, Options{Operation: Purge, Root: root})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("forged witness was accepted or misreported: %v", err)
			}
			if !exists(t, root, "/srv/olivares/olivares.db") {
				t.Fatal("refusal mutated the estate")
			}
		})
	}
}

// A later installation at another data directory renders a unit that no
// longer names the preserved estate; its own witness still authorises the
// purge of exactly that estate, and the live unit is left alone.
func TestPreservedEstateCanBePurgedAfterAnotherInstallTookTheUnit(t *testing.T) {
	m := customManifest("/srv/olivares")
	root := stage(t, m, `ExecStart=/usr/local/bin/olivares serve --data-dir=/srv/olivares --listen=127.0.0.1:8443`)
	if err := Execute(m, Options{Operation: Preserve, Root: root}); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(root, "etc/systemd/system/olivares.service")
	if err := os.WriteFile(unit, []byte("[Service]\nExecStart=/usr/local/bin/olivares serve --data-dir=/srv/new --listen=127.0.0.1:8443\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Execute(m, Options{Operation: Purge, Root: root}); err != nil {
		t.Fatalf("purge of the preserved estate: %v", err)
	}
	if exists(t, root, "/srv/olivares") {
		t.Fatal("preserved estate was not purged")
	}
	body, err := os.ReadFile(unit)
	if err != nil || !strings.Contains(string(body), "/srv/new") {
		t.Fatalf("the newer install's unit was touched: %v %q", err, body)
	}
}

func TestExecuteRefusesDecoyMentionsThroughTheRealExecutor(t *testing.T) {
	decoys := []string{
		"# --data-dir=/srv/olivares is what we would like\nExecStart=/usr/local/bin/olivares serve --data-dir=/var/lib/olivares --listen=127.0.0.1:8443",
		"Environment=OLIVARES_HINT=--data-dir=/srv/olivares\nExecStart=/usr/local/bin/olivares serve --data-dir=/var/lib/olivares",
		"ExecStartPre=/usr/local/bin/olivares check --data-dir=/srv/olivares\nExecStart=/usr/local/bin/olivares serve --data-dir=/var/lib/olivares",
		"ExecStart=/usr/local/bin/olivares serve --data-dir=/srv/olivares\nExecStart=/usr/local/bin/olivares serve --data-dir=/var/lib/olivares",
		"ExecStart=/usr/local/bin/olivares serve --data-dir=/srv/olivares-old --listen=127.0.0.1:8443",
		"ExecStart=/usr/local/bin/olivares serve --data-dir=/srv/olivares/sub --listen=127.0.0.1:8443",
	}
	for _, execStart := range decoys {
		m := customManifest("/srv/olivares")
		root := stage(t, m, execStart)
		err := Execute(m, Options{Operation: Plan, Root: root})
		if err == nil || !strings.Contains(err.Error(), "does not name data directory") {
			t.Fatalf("decoy corroborated a purge target:\n%s\nerr=%v", execStart, err)
		}
	}
}

// userEstate stages a live user-mode custom installation under home: the three
// closed-layout files, the data tree and a unit whose ExecStart= executes the
// recorded binary with dataDir. Nothing outside home is touched, and no host
// service or account command can run: Execute receives an inert Run.
func userEstate(t *testing.T, home, dataDir string) *Manifest {
	t.Helper()
	config := filepath.Join(home, ".config/olivares/olivares.env")
	unit := filepath.Join(home, ".config/systemd/user/olivares.service")
	binary := filepath.Join(home, ".local/bin/olivares")
	m := &Manifest{
		Schema: ManifestSchema, Mode: "user", Init: "systemd", Layout: LayoutCustom,
		DataDir: dataDir, Config: config, ManifestPath: filepath.Join(dataDir, "install-manifest.json"),
		Files: []File{
			{Path: binary, Role: "binary", Mode: "0755", Managed: true},
			{Path: config, Role: "config", Mode: "0600", Managed: true},
			{Path: unit, Role: "unit", Mode: "0644", Managed: true},
		},
	}
	for _, f := range m.Files {
		if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.Path, []byte("fixture "+f.Role), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.ManifestPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, m, dataDir)
	return m
}

// writeUnit renders the execution directive install-service.sh renders, for
// whichever data directory the caller says the live service now serves.
func writeUnit(t *testing.T, m *Manifest, dataDir string) {
	t.Helper()
	body := "[Service]\nExecStart=" + m.ExecutedProgram() + " serve --data-dir=" + dataDir + " --listen=127.0.0.1:8443\n"
	if err := os.WriteFile(m.Unit(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPurgeOfAPreservedEstateNeverControlsALaterInstallation is the reviewed
// P1: a witness proves which DATA this manifest owns, never that this
// installation is still the live one. With another installation's unit at the
// closed path, or with no unit at all, the purge must not order the service
// manager around and must not take that installation's software.
func TestPurgeOfAPreservedEstateNeverControlsALaterInstallation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		newer    bool   // a later installation rendered its own unit
		wantNote string // what the disclosed service line must say
	}{
		{"another installation took the unit", true, "another installation's service is live"},
		{"no service definition is left", false, "nothing to stop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			m := userEstate(t, home, filepath.Join(home, "old/data"))
			var calls []string
			run := func(name string, args ...string) error {
				calls = append(calls, strings.Join(append([]string{name}, args...), " "))
				return nil
			}
			if err := Execute(m, Options{Operation: Preserve, Run: run}); err != nil {
				t.Fatalf("preserve: %v", err)
			}
			// Preserve is the live estate's own operation, so it does stop it.
			if len(calls) == 0 {
				t.Fatal("preserve of the live estate did not stop its own service")
			}
			if exists(t, "/", m.Unit()) {
				t.Fatal("preserve kept the unit")
			}
			newerData := filepath.Join(home, "new/data")
			if tc.newer {
				// A later installation lands its own engine and unit at the same
				// closed paths, exactly as the signed adapter would.
				writeUnit(t, m, newerData)
				if err := os.WriteFile(roleOf(t, m, "binary"), []byte("newer engine"), 0o755); err != nil {
					t.Fatal(err)
				}
			}

			calls = nil
			var out strings.Builder
			if err := Execute(m, Options{Operation: Purge, Run: run, Out: &out}); err != nil {
				t.Fatalf("purge of the preserved estate: %v\n%s", err, out.String())
			}
			for _, call := range calls {
				t.Fatalf("purging the preserved estate ordered the service manager: %q", call)
			}
			if !strings.Contains(out.String(), tc.wantNote) {
				t.Fatalf("the disclosed plan does not say why the service is left alone (%s):\n%s", tc.wantNote, out.String())
			}
			if exists(t, "/", m.DataDir) {
				t.Fatal("the preserved estate was not purged")
			}
			if tc.newer {
				if !exists(t, "/", m.Unit()) {
					t.Fatal("the newer installation's unit was removed")
				}
				body, err := os.ReadFile(m.Unit())
				if err != nil || !strings.Contains(string(body), newerData) {
					t.Fatalf("the newer installation's unit was rewritten: %v %q", err, body)
				}
				for _, role := range []string{"binary", "config"} {
					path := roleOf(t, m, role)
					if !exists(t, "/", path) {
						t.Fatalf("the newer installation's %s was removed", role)
					}
					if !strings.Contains(out.String(), "keep         "+role) {
						t.Fatalf("the plan did not disclose keeping the %s:\n%s", role, out.String())
					}
				}
			}
		})
	}
}

func roleOf(t *testing.T, m *Manifest, role string) string {
	t.Helper()
	for _, f := range m.Files {
		if f.Role == role {
			return f.Path
		}
	}
	t.Fatalf("manifest records no %s", role)
	return ""
}

// TestSystemPurgePlanKeepsTheIdentitiesALiveInstallationUses covers the system
// branch of the same rule without touching a host path or a host account: the
// disclosed plan IS the effect list Execute applies, so asserting it here
// asserts the account decision itself.
func TestSystemPurgePlanKeepsTheIdentitiesALiveInstallationUses(t *testing.T) {
	m := customManifest("/srv/olivares")
	// The live estate's own purge: it stops its service and removes the
	// identities it created.
	own := stage(t, m, `ExecStart=/usr/local/bin/olivares serve --data-dir=/srv/olivares --listen=127.0.0.1:8443`)
	var out strings.Builder
	if err := Execute(m, Options{Operation: Purge, Root: own, Out: &out}); err != nil {
		t.Fatalf("purge of the live estate: %v\n%s", err, out.String())
	}
	for _, want := range [][]string{
		{"stop-disable", "service"}, {"remove", "system-user"}, {"remove", "system-group"},
	} {
		if !planRow(out.String(), want...) {
			t.Fatalf("the live estate's own purge lost %q:\n%s", strings.Join(want, " "), out.String())
		}
	}

	// The same estate after a later installation took the unit: same data
	// removal, no service control and no account removal. The disclosed lines
	// are the effect list Execute applies, so they are what is asserted; the
	// staging root additionally makes any host command impossible.
	foreign := stage(t, m, `ExecStart=/usr/local/bin/olivares serve --data-dir=/srv/olivares --listen=127.0.0.1:8443`)
	if err := Execute(m, Options{Operation: Preserve, Root: foreign}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign, m.Unit()[1:]),
		[]byte("[Service]\nExecStart=/usr/local/bin/olivares serve --data-dir=/srv/new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign, "usr/local/bin/olivares"), []byte("newer engine"), 0o755); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Execute(m, Options{Operation: Purge, Root: foreign, Out: &out}); err != nil {
		t.Fatalf("purge after another installation took the unit: %v\n%s", err, out.String())
	}
	for _, want := range []string{
		"keep         service", "keep         system-user", "keep         system-group",
		"still used by the installation whose service is live",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("the purge still claims authority over shared identities (%q):\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "stop-disable") {
		t.Fatalf("the purge still stops a service this estate does not control:\n%s", out.String())
	}
	if exists(t, foreign, "/srv/olivares") {
		t.Fatal("the estate this manifest owns was not purged")
	}
	body, err := os.ReadFile(filepath.Join(foreign, "usr/local/bin/olivares"))
	if err != nil || string(body) != "newer engine" {
		t.Fatalf("the newer installation's binary was taken: %v %q", err, body)
	}
	if !exists(t, foreign, "/etc/olivares/olivares.env") {
		t.Fatal("the configuration the live installation uses was removed")
	}
}

// TestPurgeInterruptedBeforeTheSoftwareIsRemovedFinishesOnRetry is the reviewed
// P2: the witness is written before the removals, so it proves an uninstall
// STARTED. A retry has to finish what stayed pending instead of reading the
// record as proof that everything was already removed.
func TestPurgeInterruptedBeforeTheSoftwareIsRemovedFinishesOnRetry(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based interruption cannot be staged as root")
	}
	m := customManifest("/srv/olivares")
	root := stage(t, m, `ExecStart=/usr/local/bin/olivares serve --data-dir=/srv/olivares --listen=127.0.0.1:8443`)
	binary := filepath.Join(root, "usr/local/bin/olivares")
	parent := filepath.Dir(binary)
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o750) })

	err := Execute(m, Options{Operation: Purge, Root: root})
	if err == nil || !strings.Contains(err.Error(), "remove binary") {
		t.Fatalf("interrupted purge did not fail on the binary: %v", err)
	}
	if exists(t, root, m.Unit()) {
		t.Fatal("premise is wrong: the unit was not removed first")
	}
	target, ok := witnessOnDisk(t, root, m)
	if !ok {
		t.Fatal("interrupted purge left no witness for the retry")
	}
	var w Witness
	body, rerr := os.ReadFile(target)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if err := json.Unmarshal(body, &w); err != nil {
		t.Fatal(err)
	}
	if len(w.PendingSoftware) != 1 || w.PendingSoftware[0] != "/usr/local/bin/olivares" {
		t.Fatalf("the record does not name what stayed pending: %v", w.PendingSoftware)
	}

	if err := os.Chmod(parent, 0o750); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := Execute(m, Options{Operation: Purge, Root: root, Out: &out}); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if exists(t, root, "/usr/local/bin/olivares") {
		t.Fatalf("the retry reported success and left the managed binary installed:\n%s", out.String())
	}
	if exists(t, root, "/srv/olivares") {
		t.Fatal("the retry left the data tree")
	}
	if _, ok := witnessOnDisk(t, root, m); ok {
		t.Fatal("the retry left the witness")
	}
	if !exists(t, root, "/mnt/workspaces/project/README") {
		t.Fatal("the retry crossed into the external workspace")
	}
}

// TestRetryAfterAnotherInstallationLandsKeepsItsSoftware is the other half of
// the same rule: finishing a pending removal must never take a file a later
// installation put at that closed path in the meantime.
func TestRetryAfterAnotherInstallationLandsKeepsItsSoftware(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based interruption cannot be staged as root")
	}
	m := customManifest("/srv/olivares")
	root := stage(t, m, `ExecStart=/usr/local/bin/olivares serve --data-dir=/srv/olivares --listen=127.0.0.1:8443`)
	binary := filepath.Join(root, "usr/local/bin/olivares")
	parent := filepath.Dir(binary)
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o750) })
	if err := Execute(m, Options{Operation: Purge, Root: root}); err == nil {
		t.Fatal("premise is wrong: the purge did not fail")
	}
	if err := os.Chmod(parent, 0o750); err != nil {
		t.Fatal(err)
	}
	// A later installation lands its own engine and unit at the closed paths.
	if err := os.WriteFile(binary, []byte("newer engine"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, m.Unit()[1:]),
		[]byte("[Service]\nExecStart=/usr/local/bin/olivares serve --data-dir=/srv/new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := Execute(m, Options{Operation: Purge, Root: root, Out: &out}); err != nil {
		t.Fatalf("retry after another installation landed: %v\n%s", err, out.String())
	}
	body, err := os.ReadFile(binary)
	if err != nil || string(body) != "newer engine" {
		t.Fatalf("the retry took the newer installation's binary: %v %q", err, body)
	}
	if !exists(t, root, "/etc/olivares/olivares.env") {
		t.Fatal("the retry removed the configuration the live installation uses")
	}
	if exists(t, root, "/srv/olivares") {
		t.Fatal("the retry did not purge the data this manifest owns")
	}
}

// A record written before this product listed its pending effects proves only
// that an uninstall started. It still corroborates the data, and it no longer
// authorises a claim about software: the plan says so in the same line.
func TestLegacyWitnessWithoutPendingListKeepsSoftwareAndSaysWhy(t *testing.T) {
	m := customManifest("/srv/olivares")
	root := stage(t, m, `ExecStart=/usr/local/bin/olivares serve --data-dir=/srv/olivares --listen=127.0.0.1:8443`)
	if err := os.Remove(filepath.Join(root, m.Unit()[1:])); err != nil {
		t.Fatal(err)
	}
	legacy := `{"schema":"olivares.ai/uninstall-witness/v1","data_dir":"/srv/olivares","unit":"/etc/systemd/system/olivares.service","removed_by":"purge","recorded_at":"2026-09-05T12:00:00Z"}`
	if err := os.WriteFile(filepath.Join(root, WitnessPath(m)[1:]), []byte(legacy), 0o640); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := Execute(m, Options{Operation: Purge, Root: root, Out: &out}); err != nil {
		t.Fatalf("purge with a v1 record: %v", err)
	}
	if !strings.Contains(out.String(), "left no list of what stayed pending") {
		t.Fatalf("the plan does not say why the software is kept:\n%s", out.String())
	}
	if !exists(t, root, "/usr/local/bin/olivares") {
		t.Fatal("a record that cannot say what is pending was read as permission to remove software")
	}
	if exists(t, root, "/srv/olivares") {
		t.Fatal("the data this record does corroborate was not purged")
	}
}

// TestExecuteRefusesDirectivesThatDoNotRunThisEstate is the reviewed P1 on the
// parser side, through the real executor: a directive systemd never runs, and
// one that runs another program, corroborate nothing.
func TestExecuteRefusesDirectivesThatDoNotRunThisEstate(t *testing.T) {
	bodies := map[string]string{
		"execution directive in the wrong section": "[Unit]\nExecStart=/usr/local/bin/olivares serve --data-dir=/srv/olivares\n[Service]\nExecStart=/usr/local/bin/olivares serve\n",
		"another program's command line":           "[Service]\nExecStart=/bin/echo --data-dir=/srv/olivares\n",
		"another installed engine":                 "[Service]\nExecStart=/usr/bin/olivares serve --data-dir=/srv/olivares\n",
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			m := customManifest("/srv/olivares")
			root := stage(t, m, "")
			if err := os.WriteFile(filepath.Join(root, m.Unit()[1:]), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, op := range []Operation{Plan, Preserve, Purge} {
				err := Execute(m, Options{Operation: op, Root: root})
				if err == nil || !strings.Contains(err.Error(), "corroborate custom data directory") {
					t.Fatalf("%s accepted a directive that does not run this estate: %v", op, err)
				}
			}
			if !exists(t, root, "/srv/olivares/olivares.db") || !exists(t, root, "/usr/local/bin/olivares") {
				t.Fatal("the refusal mutated the estate")
			}
		})
	}
}
