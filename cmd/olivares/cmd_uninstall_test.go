// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/localinstall"
	"github.com/olivaresai/olivares/core/dr"
)

type doctorOwnedFileInfo struct {
	fs.FileInfo
	uid uint32
}

func (i doctorOwnedFileInfo) Sys() any {
	stat := *i.FileInfo.Sys().(*syscall.Stat_t)
	stat.Uid = i.uid
	return &stat
}

func uninstallFixture(t *testing.T) (root, manifest string) {
	t.Helper()
	root = t.TempDir()
	return root, writeUninstallFixture(t, root)
}

func writeUninstallFixture(t *testing.T, root string) string {
	t.Helper()
	paths := []struct {
		logical string
		body    string
		mode    os.FileMode
	}{
		{"/usr/local/bin/olivares", "binary", 0o755},
		{"/etc/olivares/olivares.env", "OLIVARES_EXTRA_ARGS=", 0o640},
		{"/etc/systemd/system/olivares.service", "[Service]\nExecStart=/usr/local/bin/olivares serve --data-dir=/var/lib/olivares --listen=127.0.0.1:8443\nEnvironmentFile=-/etc/olivares/olivares.env\n", 0o644},
		{"/var/lib/olivares/audit-signing.key", "key", 0o600},
		{"/var/lib/olivares/olivares.log", "log", 0o600},
		{"/var/lib/olivares/demo.db", "seeded-demo", 0o600},
	}
	for _, p := range paths {
		name := filepath.Join(root, p.logical[1:])
		if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(p.body), p.mode); err != nil {
			t.Fatal(err)
		}
	}
	m := localinstall.Manifest{
		Schema: localinstall.ManifestSchema, Mode: "system", Init: "systemd",
		DataDir: "/var/lib/olivares", Config: "/etc/olivares/olivares.env",
		Files: []localinstall.File{
			{Path: "/usr/local/bin/olivares", Role: "binary", Mode: "0755", Managed: true},
			{Path: "/etc/olivares/olivares.env", Role: "config", Mode: "0640", Managed: true},
			{Path: "/etc/systemd/system/olivares.service", Role: "unit", Mode: "0644", Managed: true},
		},
		Account:      localinstall.Account{User: "olivares", Group: "olivares", UserCreated: true, GroupCreated: true},
		ManifestPath: "/var/lib/olivares/install-manifest.json",
	}
	manifest := filepath.Join(root, "var/lib/olivares/install-manifest.json")
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return manifest
}

// TestUninstallMigrationRoundTrip is the full DIST-24-16 witness: install,
// seed the real demo estate, export it through DR, purge, reinstall, import,
// compare restored bytes with the signed inventory, then run doctor green over
// the same offline filesystem view.
func TestUninstallMigrationRoundTrip(t *testing.T) {
	previous := version
	version = "26.9.0"
	t.Cleanup(func() { version = previous })
	t.Setenv("OLIVARES_LICENSE", "")
	t.Setenv("OLIVARES_LICENSE_PATH", "")

	root, manifestPath := uninstallFixture(t)
	dataOnDisk := filepath.Join(root, "var/lib/olivares")
	for _, fixtureOnly := range []string{"audit-signing.key", "olivares.log", "demo.db"} {
		if err := os.Remove(filepath.Join(dataOnDisk, fixtureOnly)); err != nil {
			t.Fatal(err)
		}
	}
	eng, err := boot(context.Background(), bootConfig{
		DataDir: dataOnDisk, Version: version, DemoSeed: true, Logger: discardLog(),
	})
	if err != nil {
		t.Fatalf("seed demo estate: %v", err)
	}
	if err := eng.signer.CheckpointAll(context.Background(), eng.store); err != nil {
		_ = eng.Close()
		t.Fatalf("checkpoint seeded estate: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("close seeded estate: %v", err)
	}

	kekPath := filepath.Join(t.TempDir(), "migration.kek")
	if err := os.WriteFile(kekPath, []byte("0123456789abcdef0123456789abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "demo.drbundle")
	if out, err := runCLI(t, "dr", "backup", "--out", bundle, "--data-dir", dataOnDisk,
		"--kek-key-file", kekPath); err != nil {
		t.Fatalf("export seeded estate: %v\n%s", err, out)
	}

	extracted := t.TempDir()
	f, err := os.Open(bundle)
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := dr.ExtractBundle(f, extracted)
	_ = f.Close()
	if err != nil {
		t.Fatalf("inspect export: %v", err)
	}
	if m.Authentication.Value == "" || len(m.Files) == 0 {
		t.Fatal("export has no authenticated per-file inventory")
	}
	exportedDigest, _, err := dr.FileSHA256(filepath.Join(extracted, m.Store.File))
	if err != nil || exportedDigest != m.Store.SHA256 {
		t.Fatalf("exported store digest = %s, signed manifest = %s, err=%v", exportedDigest, m.Store.SHA256, err)
	}

	if out, err := runCLI(t, "uninstall", "--purge", "--yes", "--data-dir", "/var/lib/olivares",
		"--manifest", manifestPath, "--root", root); err != nil {
		t.Fatalf("purge seeded install: %v\n%s", err, out)
	}
	_ = writeUninstallFixture(t, root)
	for _, seededOnly := range []string{"audit-signing.key", "olivares.log", "demo.db"} {
		if err := os.Remove(filepath.Join(dataOnDisk, seededOnly)); err != nil {
			t.Fatalf("prepare clean reinstall: %v", err)
		}
	}
	if out, err := runCLI(t, "dr", "restore", "--in", bundle, "--data-dir", dataOnDisk,
		"--kek-key-file", kekPath); err != nil {
		t.Fatalf("import seeded estate: %v\n%s", err, out)
	}
	restored, err := boot(context.Background(), bootConfig{DataDir: dataOnDisk, Version: version, Logger: discardLog()})
	if err != nil {
		t.Fatalf("open imported estate: %v", err)
	}
	cpv, err := restored.signer.CheckpointVerifier(context.Background())
	if err != nil {
		_ = restored.Close()
		t.Fatal(err)
	}
	reportDR, err := dr.RestoreVerify(context.Background(), restored.store, m, restored.signer.PublicKey(), cpv)
	if err != nil || !reportDR.OK {
		_ = restored.Close()
		t.Fatalf("imported tenant/key digests differ from export: err=%v problems=%v", err, reportDR.Problems)
	}
	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}

	mapPath := func(name string) string {
		if filepath.IsAbs(name) {
			return filepath.Join(root, name[1:])
		}
		return name
	}
	version = "dev"
	deps := defaultDoctorDeps()
	accountUID := uint32(os.Getuid())
	deps.stat = func(name string) (fs.FileInfo, error) {
		info, err := os.Stat(mapPath(name))
		if err != nil {
			return nil, err
		}
		uid := uint32(0)
		if name == "/var/lib/olivares" {
			uid = accountUID
		}
		return doctorOwnedFileInfo{FileInfo: info, uid: uid}, nil
	}
	deps.readFile = func(name string) ([]byte, error) { return os.ReadFile(mapPath(name)) }
	deps.run = func(context.Context, string, ...string) (int, error) { return 0, nil }
	deps.httpGet = func(_ context.Context, rawURL, _ string, _ time.Duration) (int, []byte, error) {
		if strings.HasSuffix(rawURL, "/status") {
			return 200, []byte(`{"status":"operational","components":[{"name":"store","status":"operational"}]}`), nil
		}
		return 200, []byte("ok"), nil
	}
	deps.getenv = func(string) string { return "" }
	deps.euid = func() int { return 0 }
	deps.lookupUID = func(string) (int, error) { return int(accountUID), nil }
	deps.goos = "linux"
	report, code, err := runDoctor(context.Background(), &doctorOptions{
		mode: "system", init: "systemd", dataDir: "/var/lib/olivares",
		config: "/etc/olivares/olivares.env", unit: "/etc/systemd/system/olivares.service",
		binary: "/usr/local/bin/olivares", server: "https://127.0.0.1:8443", timeout: time.Second,
	}, deps)
	if err != nil || code != exitcode.OK || report.Overall != "healthy" {
		t.Fatalf("doctor after import = code %d overall %q err=%v checks=%+v", code, report.Overall, err, report.Checks)
	}
}

func TestUninstallPlanIsMutationFreeAndComplete(t *testing.T) {
	root, manifest := uninstallFixture(t)
	before, err := os.ReadFile(filepath.Join(root, "var/lib/olivares/demo.db"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, "uninstall", "--plan", "--data-dir", "/var/lib/olivares",
		"--manifest", manifest, "--root", root)
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	for _, want := range []string{"binary", "unit", "system-user", "config", "data", "logs", "keys", "plan only"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan omitted %q:\n%s", want, out)
		}
	}
	after, _ := os.ReadFile(filepath.Join(root, "var/lib/olivares/demo.db"))
	if string(after) != string(before) {
		t.Fatal("--plan mutated seeded data")
	}
}

func TestUninstallPreserveRetainsCustodyAndRemovesManagedSoftware(t *testing.T) {
	root, manifest := uninstallFixture(t)
	out, err := runCLI(t, "uninstall", "--preserve", "--data-dir", "/var/lib/olivares",
		"--manifest", manifest, "--root", root)
	if err != nil {
		t.Fatalf("preserve: %v\n%s", err, out)
	}
	for _, gone := range []string{"usr/local/bin/olivares", "etc/systemd/system/olivares.service"} {
		if _, err := os.Lstat(filepath.Join(root, gone)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("preserve left managed path %s", gone)
		}
	}
	for _, kept := range []string{"etc/olivares/olivares.env", "var/lib/olivares/demo.db", "var/lib/olivares/audit-signing.key", "var/lib/olivares/install-manifest.json"} {
		if _, err := os.Lstat(filepath.Join(root, kept)); err != nil {
			t.Errorf("preserve removed retained path %s: %v", kept, err)
		}
	}
}

func TestUninstallPurgeRequiresConfirmationBeforeMutation(t *testing.T) {
	root, manifest := uninstallFixture(t)
	_, err := runCLI(t, "uninstall", "--purge", "--data-dir", "/var/lib/olivares",
		"--manifest", manifest, "--root", root)
	if exitcode.From(err) != exitcode.Usage {
		t.Fatalf("unconfirmed purge code = %d, want %d: %v", exitcode.From(err), exitcode.Usage, err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "var/lib/olivares/demo.db")); statErr != nil {
		t.Fatalf("unconfirmed purge mutated data: %v", statErr)
	}
}

func TestUninstallPurgeRemovesOnlyAllowlistedEstate(t *testing.T) {
	root, manifest := uninstallFixture(t)
	outside := filepath.Join(root, "srv/operator-owned")
	if err := os.MkdirAll(filepath.Dir(outside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, "uninstall", "--purge", "--yes", "--data-dir", "/var/lib/olivares",
		"--manifest", manifest, "--root", root)
	if err != nil {
		t.Fatalf("purge: %v\n%s", err, out)
	}
	for _, gone := range []string{"usr/local/bin/olivares", "etc/systemd/system/olivares.service", "etc/olivares/olivares.env", "var/lib/olivares"} {
		if _, err := os.Lstat(filepath.Join(root, gone)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("purge left %s", gone)
		}
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "keep" {
		t.Fatalf("purge crossed the install layout: %q, %v", got, err)
	}
}

func TestUninstallUnexpectedPathIsUsageAndPrecedesMutation(t *testing.T) {
	root, manifest := uninstallFixture(t)
	b, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	mutant := strings.Replace(string(b), "/usr/local/bin/olivares", "/srv/not-in-index/olivares", 1)
	if err := os.WriteFile(manifest, []byte(mutant), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = runCLI(t, "uninstall", "--preserve", "--data-dir", "/var/lib/olivares",
		"--manifest", manifest, "--root", root)
	if exitcode.From(err) != exitcode.Usage {
		t.Fatalf("unexpected path code = %d, want %d: %v", exitcode.From(err), exitcode.Usage, err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "usr/local/bin/olivares")); statErr != nil {
		t.Fatalf("unsafe manifest caused a partial mutation: %v", statErr)
	}
}

func TestUninstallRejectsAccountAndTupleInjectionBeforeMutation(t *testing.T) {
	for _, tc := range []struct {
		name, old, replacement, want string
	}{
		{"account", `"user": "olivares"`, `"user": "root"`, "unexpected system service account"},
		{"mixed tuple", "/etc/systemd/system/olivares.service", "/etc/init.d/olivares", "tuple is outside"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, manifest := uninstallFixture(t)
			body, err := os.ReadFile(manifest)
			if err != nil {
				t.Fatal(err)
			}
			mutated := strings.Replace(string(body), tc.old, tc.replacement, 1)
			if mutated == string(body) {
				t.Fatalf("fixture lacks mutation anchor %q", tc.old)
			}
			if err := os.WriteFile(manifest, []byte(mutated), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err = runCLI(t, "uninstall", "--preserve", "--data-dir", "/var/lib/olivares",
				"--manifest", manifest, "--root", root)
			if exitcode.From(err) != exitcode.Usage || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("injected manifest error = %v, want usage containing %q", err, tc.want)
			}
			if _, statErr := os.Stat(filepath.Join(root, "usr/local/bin/olivares")); statErr != nil {
				t.Fatalf("injected manifest caused a partial mutation: %v", statErr)
			}
		})
	}
}

func TestUninstallPurgeRejectsDataSymlinkBeforeSoftwareMutation(t *testing.T) {
	root, manifest := uninstallFixture(t)
	realData := filepath.Join(root, "var/lib/olivares-real")
	dataDir := filepath.Join(root, "var/lib/olivares")
	if err := os.Rename(dataDir, realData); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realData, dataDir); err != nil {
		t.Fatal(err)
	}
	_, err := runCLI(t, "uninstall", "--purge", "--yes", "--data-dir", "/var/lib/olivares",
		"--manifest", manifest, "--root", root)
	if err == nil || !strings.Contains(err.Error(), "through a symlink") {
		t.Fatalf("symlinked data directory was not refused: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "usr/local/bin/olivares")); statErr != nil {
		t.Fatalf("data symlink caused a partial software mutation: %v", statErr)
	}
}

// TestUninstallCustomLayoutIsCorroboratedByTheUnitThroughTheCLI drives the
// installed command over an offline root: a custom data root with an external
// workspace is planned, preserved and purged only when the closed-layout unit
// names it, and the workspace outside the data tree is never touched.
func TestUninstallCustomLayoutIsCorroboratedByTheUnitThroughTheCLI(t *testing.T) {
	stageCustom := func(t *testing.T, execStart string) (root, manifest string) {
		t.Helper()
		root = t.TempDir()
		files := map[string]string{
			"/usr/local/bin/olivares":                              "binary",
			"/etc/olivares/olivares.env":                           "OLIVARES_EXTRA_ARGS=",
			"/etc/olivares/agentops.env":                           "OLIVARES_SESSION_RUNTIME_TOKEN_FILE=/srv/olivares/run/session-token",
			"/etc/systemd/system/olivares.service":                 "[Service]\n" + execStart + "\n",
			"/etc/systemd/system/olivares.service.d/agentops.conf": "[Service]\nEnvironment=HOME=/srv/olivares/claude-home\n",
			"/srv/olivares/demo.db":                                "seeded",
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
		m := localinstall.Manifest{
			Schema: localinstall.ManifestSchema, Mode: "system", Init: "systemd", Layout: localinstall.LayoutCustom,
			DataDir: "/srv/olivares", Config: "/etc/olivares/olivares.env", WorkspaceDir: "/mnt/workspaces",
			Files: []localinstall.File{
				{Path: "/usr/local/bin/olivares", Role: "binary", Mode: "0755", Managed: true},
				{Path: "/etc/olivares/olivares.env", Role: "config", Mode: "0640", Managed: true},
				{Path: "/etc/systemd/system/olivares.service", Role: "unit", Mode: "0644", Managed: true},
				{Path: "/etc/systemd/system/olivares.service.d/agentops.conf", Role: localinstall.RoleDropin, Mode: "0644", Managed: true},
				{Path: "/etc/olivares/agentops.env", Role: localinstall.RoleRuntimeEnv, Mode: "0640", Managed: true},
			},
			Account:      localinstall.Account{User: "olivares", Group: "olivares", UserCreated: true, GroupCreated: true},
			ManifestPath: "/srv/olivares/install-manifest.json",
		}
		b, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		manifest = filepath.Join(root, "srv/olivares/install-manifest.json")
		if err := os.WriteFile(manifest, append(b, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		return root, manifest
	}

	root, manifest := stageCustom(t, "ExecStart=/usr/local/bin/olivares serve --data-dir=/var/lib/olivares --listen=127.0.0.1:8443")
	_, err := runCLI(t, "uninstall", "--plan", "--data-dir", "/srv/olivares", "--manifest", manifest, "--root", root)
	if err == nil || !strings.Contains(err.Error(), "does not name data directory") {
		t.Fatalf("uncorroborated custom layout was planned: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "usr/local/bin/olivares")); statErr != nil {
		t.Fatalf("refusal mutated software: %v", statErr)
	}

	// The two bodies the independent review of 2026-09-05 reproduced (R3), through the
	// command an operator actually runs. Neither is evidence about this estate: systemd
	// never runs an ExecStart= outside [Service], and a command line that starts another
	// program is that program's, whatever it mentions.
	for name, body := range map[string]string{
		"execution directive in the wrong section": "[Unit]\nExecStart=/usr/local/bin/olivares serve --data-dir=/srv/olivares\n[Service]\nExecStart=/usr/local/bin/olivares serve\n",
		"another program's command line":           "[Service]\nExecStart=/bin/echo --data-dir=/srv/olivares\n",
	} {
		decoyRoot, decoyManifest := stageCustom(t, "ExecStart=/usr/local/bin/olivares serve --data-dir=/srv/olivares")
		unit := filepath.Join(decoyRoot, "etc/systemd/system/olivares.service")
		if err := os.WriteFile(unit, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
		out, err := runCLI(t, "uninstall", "--plan", "--data-dir", "/srv/olivares", "--manifest", decoyManifest, "--root", decoyRoot)
		if err == nil || !strings.Contains(err.Error(), "corroborate custom data directory") {
			t.Errorf("%s: the CLI planned an estate this unit does not witness: %v\n%s", name, err, out)
		}
		if _, statErr := os.Stat(filepath.Join(decoyRoot, "srv/olivares/demo.db")); statErr != nil {
			t.Errorf("%s: the refusal mutated the estate: %v", name, statErr)
		}
	}

	root, manifest = stageCustom(t, "ExecStart=/usr/local/bin/olivares serve --data-dir=/srv/olivares --listen=127.0.0.1:8443")
	out, err := runCLI(t, "uninstall", "--plan", "--data-dir", "/srv/olivares", "--manifest", manifest, "--root", root)
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	for _, want := range []string{"dropin", "runtime-env", "workspace", "/mnt/workspaces", "plan only"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan omitted %q:\n%s", want, out)
		}
	}
	if out, err := runCLI(t, "uninstall", "--preserve", "--data-dir", "/srv/olivares", "--manifest", manifest, "--root", root); err != nil {
		t.Fatalf("preserve: %v\n%s", err, out)
	}
	for _, gone := range []string{"usr/local/bin/olivares", "etc/systemd/system/olivares.service", "etc/systemd/system/olivares.service.d/agentops.conf"} {
		if _, err := os.Lstat(filepath.Join(root, gone)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("preserve left managed path %s", gone)
		}
	}
	for _, kept := range []string{"etc/olivares/agentops.env", "srv/olivares/demo.db", "mnt/workspaces/project/README"} {
		if _, err := os.Lstat(filepath.Join(root, kept)); err != nil {
			t.Errorf("preserve removed retained path %s: %v", kept, err)
		}
	}

	root, manifest = stageCustom(t, `ExecStart=/usr/local/bin/olivares serve "--data-dir=/srv/olivares" --listen=127.0.0.1:8443`)
	if out, err := runCLI(t, "uninstall", "--purge", "--yes", "--data-dir", "/srv/olivares", "--manifest", manifest, "--root", root); err != nil {
		t.Fatalf("purge: %v\n%s", err, out)
	}
	for _, gone := range []string{"etc/olivares/agentops.env", "srv/olivares"} {
		if _, err := os.Lstat(filepath.Join(root, gone)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("purge left %s", gone)
		}
	}
	if got, err := os.ReadFile(filepath.Join(root, "mnt/workspaces/project/README")); err != nil || string(got) != "operator data" {
		t.Fatalf("purge crossed into the external workspace: %q, %v", got, err)
	}
}

// TestUninstallOpenRCBaseWitnessesOnlyWhenCopiedThroughTheCLI drives the
// installed command over an offline root against an OpenRC custom estate. The
// independent review of 2026-09-05 (B1) ran `uninstall --preserve` on a unit
// whose command_args_base= named the estate while nothing copied it into
// command_args=, and the CLI removed the binary and unit of an engine that
// starts at the built-in default. The base is a mention until a command_args=
// assignment carries it, and a copy a later assignment replaces is stale: every
// body below must refuse before a plan is disclosed, and the shipped rendering
// must still plan and preserve.
func TestUninstallOpenRCBaseWitnessesOnlyWhenCopiedThroughTheCLI(t *testing.T) {
	stage := func(t *testing.T, unit string) (root, manifest string) {
		t.Helper()
		root = t.TempDir()
		files := map[string]string{
			"/usr/local/bin/olivares":    "binary",
			"/etc/olivares/olivares.env": "OLIVARES_EXTRA_ARGS=",
			"/etc/init.d/olivares":       unit,
			"/srv/olivares/demo.db":      "seeded",
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
		m := localinstall.Manifest{
			Schema: localinstall.ManifestSchema, Mode: "system", Init: "openrc", Layout: localinstall.LayoutCustom,
			DataDir: "/srv/olivares", Config: "/etc/olivares/olivares.env",
			Files: []localinstall.File{
				{Path: "/usr/local/bin/olivares", Role: "binary", Mode: "0755", Managed: true},
				{Path: "/etc/olivares/olivares.env", Role: "config", Mode: "0640", Managed: true},
				{Path: "/etc/init.d/olivares", Role: "unit", Mode: "0755", Managed: true},
			},
			Account:      localinstall.Account{User: "olivares", Group: "olivares", UserCreated: true, GroupCreated: true},
			ManifestPath: "/srv/olivares/install-manifest.json",
		}
		b, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		manifest = filepath.Join(root, "srv/olivares/install-manifest.json")
		if err := os.WriteFile(manifest, append(b, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		return root, manifest
	}
	const command = "command=\"/usr/local/bin/olivares\"\n"
	const base = "command_args_base=\"serve --data-dir=/srv/olivares --listen=127.0.0.1:8443\"\n"
	estate := []string{"usr/local/bin/olivares", "etc/init.d/olivares", "etc/olivares/olivares.env", "srv/olivares/demo.db"}

	for name, unit := range map[string]string{
		"base alone, no command_args":                                                 command + base,
		"base beside a literal command_args without --data-dir":                       command + base + "command_args=\"serve --listen=127.0.0.1:8443\"\n",
		"base beside command_args reset to empty":                                     command + base + "command_args=\"\"\n",
		"base beside command_args expanding another variable":                         command + base + "command_args=\"$other_args\"\n",
		"base copied then command_args reset to empty":                                command + base + "command_args=\"$command_args_base\"\ncommand_args=\"\"\n",
		"base copied then command_args replaced by another variable":                  command + base + "command_args=\"$command_args_base\"\ncommand_args=\"$other_args\"\n",
		"single-quoted copy is literal text":                                          command + base + "command_args='$command_args_base'\n",
		"unreferenced base naming another directory beside a literal naming this one": command + "command_args_base=\"serve --data-dir=/srv/other\"\ncommand_args=\"serve --data-dir=/srv/olivares --listen=127.0.0.1:8443\"\n",
	} {
		root, manifest := stage(t, unit)
		for _, op := range []string{"--plan", "--preserve"} {
			out, err := runCLI(t, "uninstall", op, "--data-dir", "/srv/olivares", "--manifest", manifest, "--root", root)
			if err == nil || !strings.Contains(err.Error(), "corroborate custom data directory") || !strings.Contains(err.Error(), "does not name data directory") {
				t.Errorf("%s: %s acted on an estate this unit does not start: %v\n%s", name, op, err, out)
			}
		}
		for _, kept := range estate {
			if _, statErr := os.Lstat(filepath.Join(root, kept)); statErr != nil {
				t.Errorf("%s: the refusal mutated %s: %v", name, kept, statErr)
			}
		}
	}

	// Control: the shipped rendering copies the base into command_args= at top
	// level and again, with the extra flags, in start_pre. It plans without
	// mutation, and preserve removes only the managed software.
	shipped := command + base + "command_args=\"$command_args_base\"\nstart_pre() {\n\tcommand_args=\"$command_args_base\"\n\tolivares_extra_args=\n\tcommand_args=\"$command_args_base $olivares_extra_args\"\n}\n"
	root, manifest := stage(t, shipped)
	out, err := runCLI(t, "uninstall", "--plan", "--data-dir", "/srv/olivares", "--manifest", manifest, "--root", root)
	if err != nil {
		t.Fatalf("shipped rendering was refused: %v\n%s", err, out)
	}
	for _, want := range []string{"openrc:olivares", "/etc/init.d/olivares", "plan only"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan omitted %q:\n%s", want, out)
		}
	}
	for _, kept := range estate {
		if _, statErr := os.Lstat(filepath.Join(root, kept)); statErr != nil {
			t.Errorf("plan mutated %s: %v", kept, statErr)
		}
	}
	if out, err := runCLI(t, "uninstall", "--preserve", "--data-dir", "/srv/olivares", "--manifest", manifest, "--root", root); err != nil {
		t.Fatalf("preserve: %v\n%s", err, out)
	}
	for _, gone := range []string{"usr/local/bin/olivares", "etc/init.d/olivares"} {
		if _, err := os.Lstat(filepath.Join(root, gone)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("preserve left managed path %s", gone)
		}
	}
	for _, kept := range []string{"etc/olivares/olivares.env", "srv/olivares/demo.db"} {
		if _, err := os.Lstat(filepath.Join(root, kept)); err != nil {
			t.Errorf("preserve removed retained path %s: %v", kept, err)
		}
	}
}

// TestUpgradedCustomEstateSurvivesDoctorPlanPreservePurge stages the manifest the
// AgentOps battery produces AFTER an engine upgrade through the signed adapter
// (testdata/manifest-custom-upgraded.json) and drives the real consumers over
// one offline root in sequence: doctor healthy with agentops-layout measured,
// plan, preserve (witness recorded), plan again, purge; the explicitly selected
// external workspace must survive every step with its content.
func TestUpgradedCustomEstateSurvivesDoctorPlanPreservePurge(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("internal", "localinstall", "testdata", "manifest-custom-upgraded.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m localinstall.Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	if !m.Custom() || m.WorkspaceDir != "/mnt/workspaces" {
		t.Fatalf("golden is not the upgraded custom estate: %+v", m)
	}
	root := t.TempDir()
	dropin := "/etc/systemd/system/olivares.service.d/agentops.conf"
	files := map[string]string{
		"/usr/local/bin/olivares":              "binary",
		"/etc/olivares/olivares.env":           "OLIVARES_EXTRA_ARGS=\n",
		"/etc/olivares/agentops.env":           "OLIVARES_SESSION_RUNTIME_TOKEN_FILE=/srv/olivares/run/session-token\n",
		"/etc/systemd/system/olivares.service": "[Service]\nExecStart=/usr/local/bin/olivares serve --data-dir=/srv/olivares --listen=127.0.0.1:8443\nEnvironmentFile=-/etc/olivares/olivares.env\nReadWritePaths=/srv/olivares\n",
		dropin:                                 "[Service]\nEnvironment=HOME=/srv/olivares/claude-home\nExecStartPre=/usr/bin/install -d -m 0700 /srv/olivares/run\nReadWritePaths=/mnt/workspaces\n",
		"/srv/olivares/olivares.db":            "store",
		"/mnt/workspaces/project/README":       "operator data",
	}
	for logical, content := range files {
		name := filepath.Join(root, logical[1:])
		if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o640)
		if logical == "/usr/local/bin/olivares" {
			mode = 0o755
		} else if strings.HasSuffix(logical, ".service") || logical == dropin {
			mode = 0o644
		}
		if err := os.WriteFile(name, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	manifest := filepath.Join(root, "srv/olivares/install-manifest.json")
	if err := os.WriteFile(manifest, body, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "srv/olivares"), 0o750); err != nil {
		t.Fatal(err)
	}

	mapPath := func(name string) string {
		if filepath.IsAbs(name) {
			return filepath.Join(root, name[1:])
		}
		return name
	}
	deps := defaultDoctorDeps()
	accountUID := uint32(os.Getuid())
	deps.stat = func(name string) (fs.FileInfo, error) {
		info, err := os.Stat(mapPath(name))
		if err != nil {
			return nil, err
		}
		uid := uint32(0)
		if name == "/srv/olivares" || name == "/mnt/workspaces" {
			uid = accountUID
		}
		return doctorOwnedFileInfo{FileInfo: info, uid: uid}, nil
	}
	deps.readFile = func(name string) ([]byte, error) { return os.ReadFile(mapPath(name)) }
	deps.run = func(context.Context, string, ...string) (int, error) { return 0, nil }
	deps.httpGet = func(_ context.Context, rawURL, _ string, _ time.Duration) (int, []byte, error) {
		if strings.HasSuffix(rawURL, "/status") {
			return 200, []byte(`{"status":"operational","components":[{"name":"store","status":"operational"}]}`), nil
		}
		return 200, []byte("ok"), nil
	}
	deps.getenv = func(string) string { return "" }
	deps.euid = func() int { return 0 }
	deps.lookupUID = func(string) (int, error) { return int(accountUID), nil }
	deps.goos = "linux"
	t.Setenv("OLIVARES_LICENSE", "")
	t.Setenv("OLIVARES_LICENSE_PATH", "")
	report, code, err := runDoctor(context.Background(), &doctorOptions{
		mode: "system", init: "systemd", dataDir: "/srv/olivares",
		config: "/etc/olivares/olivares.env", unit: "/etc/systemd/system/olivares.service",
		binary: "/usr/local/bin/olivares", server: "https://127.0.0.1:8443", timeout: time.Second,
	}, deps)
	if err != nil || code != exitcode.OK || report.Overall != "healthy" {
		t.Fatalf("doctor after upgrade = code %d overall %q err=%v checks=%+v", code, report.Overall, err, report.Checks)
	}
	for _, check := range report.Checks {
		if check.Name == "agentops-layout" && (check.Status != "pass" || !strings.Contains(check.Detail, "/mnt/workspaces")) {
			t.Fatalf("agentops-layout after upgrade = %+v", check)
		}
	}

	args := []string{"--data-dir", "/srv/olivares", "--manifest", manifest, "--root", root}
	out, err := runCLI(t, append([]string{"uninstall", "--plan"}, args...)...)
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	for _, want := range []string{"dropin", "runtime-env", "keep         workspace     /mnt/workspaces", "witness"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan omitted %q:\n%s", want, out)
		}
	}
	if out, err := runCLI(t, append([]string{"uninstall", "--preserve"}, args...)...); err != nil {
		t.Fatalf("preserve: %v\n%s", err, out)
	}
	if _, err := os.Lstat(filepath.Join(root, dropin[1:])); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preserve left the managed drop-in")
	}
	if _, err := os.Stat(filepath.Join(root, localinstall.WitnessPath(&m)[1:])); err != nil {
		t.Fatalf("preserve recorded no witness: %v", err)
	}
	if out, err := runCLI(t, append([]string{"uninstall", "--plan"}, args...)...); err != nil {
		t.Fatalf("plan after preserve: %v\n%s", err, out)
	}
	if out, err := runCLI(t, append([]string{"uninstall", "--purge", "--yes"}, args...)...); err != nil {
		t.Fatalf("purge after preserve: %v\n%s", err, out)
	}
	for _, gone := range []string{"srv/olivares", "etc/olivares/agentops.env", "etc/olivares/olivares.env"} {
		if _, err := os.Lstat(filepath.Join(root, gone)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("purge left %s", gone)
		}
	}
	if _, err := os.Lstat(filepath.Join(root, localinstall.WitnessPath(&m)[1:])); !errors.Is(err, os.ErrNotExist) {
		t.Error("purge left the witness")
	}
	if got, err := os.ReadFile(filepath.Join(root, "mnt/workspaces/project/README")); err != nil || string(got) != "operator data" {
		t.Fatalf("external workspace did not survive the lifecycle: %q, %v", got, err)
	}
}
