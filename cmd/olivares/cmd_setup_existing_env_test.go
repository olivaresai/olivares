// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The deb and rpm install /etc/olivares/olivares.env as the unmodified
// packaging/olivares.env.example, and INSTALL.md recommends `sudo olivares setup`
// with no --force as the first configuration step. These tests run that step
// against the real shipped file: setup must replace it (keeping a .bak) and must
// still refuse to replace a file the operator has configured.

func packagedEnvFile(t *testing.T) (path string, shipped []byte) {
	t.Helper()
	shipped, err := os.ReadFile(filepath.Join(repoRoot(t), "packaging", "olivares.env.example"))
	if err != nil {
		t.Fatalf("read the packaged env file: %v", err)
	}
	path = filepath.Join(t.TempDir(), "olivares.env")
	if err := os.WriteFile(path, shipped, 0o640); err != nil {
		t.Fatal(err)
	}
	return path, shipped
}

func singleNodePlan() installPlan {
	return newPlanForProfile(profileSingleNode)
}

func TestSetupReplacesTheUnmodifiedPackagedEnvFile(t *testing.T) {
	t.Parallel()
	path, shipped := packagedEnvFile(t)
	plan := singleNodePlan()

	if err := writePlan(&bytes.Buffer{}, plan, path, false); err != nil {
		t.Fatalf("setup refused the file the package itself installed: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != plan.render() {
		t.Errorf("env file was not rewritten:\n%s", got)
	}
	bak, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("no .bak of the replaced file: %v", err)
	}
	if !bytes.Equal(bak, shipped) {
		t.Error(".bak is not the file that was replaced")
	}
	fi, err := os.Stat(path + ".bak")
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o640 {
		t.Errorf(".bak mode = %v, want 0640", fi.Mode().Perm())
	}
}

func TestSetupKeepsAnOperatorConfiguredEnvFile(t *testing.T) {
	t.Parallel()
	for name, edit := range map[string]string{
		"serve flags":          "OLIVARES_EXTRA_ARGS=--listen=127.0.0.1:8443\n",
		"public url":           "OLIVARES_PUBLIC_URL=https://olivares.example.com\n",
		"a key the file lacks": "OLIVARES_DB_MAX_CONNS=40\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path, shipped := packagedEnvFile(t)
			configured := append(append([]byte{}, shipped...), edit...)
			if err := os.WriteFile(path, configured, 0o640); err != nil {
				t.Fatal(err)
			}

			err := writePlan(&bytes.Buffer{}, singleNodePlan(), path, false)
			if err == nil || !strings.Contains(err.Error(), "already exists") {
				t.Fatalf("setup replaced a configured env file without --force: err=%v", err)
			}
			if got, _ := os.ReadFile(path); !bytes.Equal(got, configured) {
				t.Error("the configured env file was changed")
			}
			if _, err := os.Stat(path + ".bak"); err == nil {
				t.Error("a refused write left a .bak")
			}
		})
	}
}

// A refusal writes nothing: before this, postgres-prod wrote the 0600 DSN files and
// only then refused the env file, so the next run tripped on app.dsn as well.
func TestSetupRefusalWritesNoSecretFiles(t *testing.T) {
	t.Parallel()
	path, shipped := packagedEnvFile(t)
	if err := os.WriteFile(path, append(shipped, "OLIVARES_EXTRA_ARGS=--insecure\n"...), 0o640); err != nil {
		t.Fatal(err)
	}
	secrets := filepath.Join(t.TempDir(), "secrets")
	plan := validPostgresProdPlan()
	plan.Secrets = []envSecretFile{{Path: filepath.Join(secrets, "app.dsn"), Content: "postgres://app@db/olivares"}}

	if err := writePlan(&bytes.Buffer{}, plan, path, false); err == nil {
		t.Fatal("setup replaced a configured env file without --force")
	}
	if _, err := os.Stat(secrets); err == nil {
		t.Error("a refused setup still created the secrets directory or files")
	}
}

// An existing secret file is still protected, and the unmodified env file is left
// alone too when that refusal comes first.
func TestSetupKeepsAnExistingSecretFile(t *testing.T) {
	t.Parallel()
	path, shipped := packagedEnvFile(t)
	dir := t.TempDir()
	secret := filepath.Join(dir, "owner.dsn")
	if err := os.WriteFile(secret, []byte("postgres://kept@db/olivares\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(dir, "app.dsn") // listed first, so it would be written before the refusal
	plan := validPostgresProdPlan()
	plan.Secrets = []envSecretFile{
		{Path: first, Content: "postgres://new@db/olivares"},
		{Path: secret, Content: "postgres://new@db/olivares"},
	}

	err := writePlan(&bytes.Buffer{}, plan, path, false)
	if err == nil || !strings.Contains(err.Error(), secret) {
		t.Fatalf("setup replaced an existing secret file without --force: err=%v", err)
	}
	if got, _ := os.ReadFile(secret); string(got) != "postgres://kept@db/olivares\n" {
		t.Error("the existing secret file was changed")
	}
	if _, err := os.Stat(first); err == nil {
		t.Error("a refused setup still wrote the secret file listed before the existing one")
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, shipped) {
		t.Error("the env file was replaced although setup refused")
	}
}

// config generate is the scriptable twin and the packaged file's own header
// recommends it, so it follows the same rule.
func TestConfigGenerateReplacesTheUnmodifiedPackagedEnvFile(t *testing.T) {
	t.Parallel()
	path, shipped := packagedEnvFile(t)

	cmd := configGenerateCmd()
	cmd.SetArgs([]string{"--profile", profileSingleNode, "--out", path})
	cmd.SetOut(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config generate refused the file the package installed: %v", err)
	}
	if bak, err := os.ReadFile(path + ".bak"); err != nil || !bytes.Equal(bak, shipped) {
		t.Errorf("no faithful .bak of the replaced file (err=%v)", err)
	}
}

func configuredEnvFile(t *testing.T) (path string, configured []byte) {
	t.Helper()
	path, shipped := packagedEnvFile(t)
	configured = append(append([]byte{}, shipped...), "OLIVARES_EXTRA_ARGS=--listen=127.0.0.1:8443\n"...)
	if err := os.WriteFile(path, configured, 0o640); err != nil {
		t.Fatal(err)
	}
	return path, configured
}

// --force still replaces what the operator configured, keeping it as .bak, and the
// secret files with it.
func TestSetupForceReplacesAConfiguredEnvFile(t *testing.T) {
	t.Parallel()
	path, configured := configuredEnvFile(t)
	secret := filepath.Join(t.TempDir(), "app.dsn")
	if err := os.WriteFile(secret, []byte("postgres://old@db/olivares\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := singleNodePlan()
	plan.Secrets = []envSecretFile{{Path: secret, Content: "postgres://new@db/olivares"}}

	if err := writePlan(&bytes.Buffer{}, plan, path, true); err != nil {
		t.Fatalf("setup --force: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != plan.render() {
		t.Errorf("env file was not rewritten:\n%s", got)
	}
	if bak, _ := os.ReadFile(path + ".bak"); !bytes.Equal(bak, configured) {
		t.Error(".bak is not the configured file that was replaced")
	}
	if got, _ := os.ReadFile(secret); string(got) != "postgres://new@db/olivares\n" {
		t.Errorf("secret file was not rewritten: %q", got)
	}
	if bak, _ := os.ReadFile(secret + ".bak"); string(bak) != "postgres://old@db/olivares\n" {
		t.Errorf("secret .bak = %q", bak)
	}
}

// k8s writes no env file, so an env file on this host is none of its business.
func TestSetupK8sIgnoresAConfiguredEnvFile(t *testing.T) {
	t.Parallel()
	path, configured := configuredEnvFile(t)
	out := &bytes.Buffer{}

	if err := writePlan(out, newPlanForProfile(profileK8s), path, false); err != nil {
		t.Fatalf("k8s setup refused an env file it never writes: %v", err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, configured) {
		t.Error("k8s setup changed the env file")
	}
	if _, err := os.Stat(path + ".bak"); err == nil {
		t.Error("k8s setup left a .bak")
	}
	if !strings.Contains(out.String(), "Helm") {
		t.Errorf("k8s output missing: %q", out.String())
	}
}

// config generate follows the same rule as setup: keep a configured file, replace it
// with --force and a .bak.
func TestConfigGenerateKeepsAConfiguredEnvFileUnlessForced(t *testing.T) {
	t.Parallel()
	path, configured := configuredEnvFile(t)
	run := func(extra ...string) error {
		cmd := configGenerateCmd()
		cmd.SetArgs(append([]string{"--profile", profileSingleNode, "--out", path}, extra...))
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		return cmd.Execute()
	}

	if err := run(); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("config generate replaced a configured env file without --force: err=%v", err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, configured) {
		t.Fatal("the configured env file was changed")
	}
	if err := run("--force"); err != nil {
		t.Fatalf("config generate --force: %v", err)
	}
	if bak, _ := os.ReadFile(path + ".bak"); !bytes.Equal(bak, configured) {
		t.Error(".bak is not the configured file that was replaced")
	}
}

// Anything setup cannot read as a plain small file is treated as configured: a
// symlink would have its target rewritten and chowned, and a device would be chmod'ed.
func TestSetupRefusesWhatItCannotInspectAsAPlainFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "olivares.env")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	err := writePlan(&bytes.Buffer{}, singleNodePlan(), link, false)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("setup went ahead on a symlinked env file without --force: err=%v", err)
	}
	if got, _ := os.ReadFile(target); len(got) != 0 {
		t.Errorf("the symlink target was rewritten: %q", got)
	}
	// Read-only: a device reads as empty, and must never be classed as replaceable.
	if !envFileHoldsConfig(os.DevNull) {
		t.Error("a device was classed as an env file that holds nothing")
	}
}

// The package default is replaced only with its backup in hand: a backup that cannot
// be written stops the write.
func TestSetupStopsWhenTheBackupCannotBeMade(t *testing.T) {
	t.Parallel()
	path, shipped := packagedEnvFile(t)
	if err := os.Mkdir(path+".bak", 0o755); err != nil { // a directory cannot be written as a file
		t.Fatal(err)
	}

	err := writePlan(&bytes.Buffer{}, singleNodePlan(), path, false)
	if err == nil || !strings.Contains(err.Error(), "back up") {
		t.Fatalf("setup replaced the env file without a backup: err=%v", err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, shipped) {
		t.Error("the env file was replaced although its backup failed")
	}
}

func TestEnvFileHoldsConfig(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, body string
		want       bool
	}{
		{"empty file", "", false},
		{"comments and blank lines", "# a\n\n   # b\n", false},
		{"empty keys", "OLIVARES_EXTRA_ARGS=\nOLIVARES_PUBLIC_URL=\n", false},
		{"empty quoted keys", "OLIVARES_EXTRA_ARGS=\"\"\nOLIVARES_PUBLIC_URL=''\n", false},
		{"a value", "OLIVARES_EXTRA_ARGS=--insecure\n", true},
		{"a value after comments", "# x\nOLIVARES_PUBLIC_URL=https://a.example\n", true},
		{"a line that is not KEY=", "export FOO\n", true},
		{"a setting after a bare carriage return", "# note\rOLIVARES_EXTRA_ARGS=--insecure\n", true},
		{"CRLF line endings, no setting", "# note\r\nOLIVARES_EXTRA_ARGS=\r\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "olivares.env")
			if err := os.WriteFile(path, []byte(c.body), 0o640); err != nil {
				t.Fatal(err)
			}
			if got := envFileHoldsConfig(path); got != c.want {
				t.Errorf("envFileHoldsConfig = %v, want %v", got, c.want)
			}
		})
	}
	t.Run("unreadable path is treated as configured", func(t *testing.T) {
		t.Parallel()
		if !envFileHoldsConfig(t.TempDir()) { // a directory cannot be read as a file
			t.Error("an unreadable target must be protected, not replaced")
		}
	})
}
