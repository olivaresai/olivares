// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/secure"
)

// THE DEFECT THIS FILE PINS.
//
// Stopped READ-ONLY and MUTATING verbs installing what they were asked to
// read (readonly_boot_test.go). It deliberately left `serve`/`quickstart`/`setup`
// alone, because initializing an installation IS their job — see the
// NoImplicitInstall doc comment in boot.go.
//
// That left the exact case a first-time user hits: `olivares serve` with no
// --data-dir. defaultDataDir returned the RELATIVE literal "olivares-data", so the
// engine minted four private keys (0600) and a multi-megabyte store INTO WHATEVER
// DIRECTORY THE OPERATOR HAPPENED TO BE IN. When that directory is a git work tree
// — which, for anyone trying the product out of a clone, it is — `git status` lists
// them and a single `git add -A` publishes private keys.
//
// The oracle here is git itself, not a file list: the question is not "where did
// the bytes go" but "can the operator's next commit carry them".

// gitAvailable reports whether a usable git is on PATH. A test whose oracle IS git
// must say out loud when it could not consult it, rather than pass quietly.
func gitAvailable(t *testing.T) bool {
	t.Helper()
	_, err := exec.LookPath("git")
	return err == nil
}

// decoyRepo makes a THROWAWAY git work tree and returns its path. Never the repo
// under development: the defect is "files appear in a working tree", and proving it
// against the live clone would mean creating that exact mess in a shared tree.
func decoyRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q", "--initial-branch=main", dir)
	// A sandboxed HOME keeps the developer's ~/.gitconfig (and this repo's absolute
	// core.hooksPath) out of the decoy.
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return dir
}

// gitSees returns the porcelain status of repo — every path git would offer to
// commit. Empty means the tree is clean AND carries nothing untracked.
func gitSees(t *testing.T, repo string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "status", "--porcelain")
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// sandboxHome points the per-user default at a throwaway root, so a test never
// reads or writes the developer's real ~/.local/share.
func sandboxHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("OLIVARES_DATA_DIR", "")
	return home
}

// bootedKeyMaterial lists the private keys and store an initializing boot creates.
// It exists so the assertions below cannot pass VACUOUSLY: "git sees nothing" is
// only evidence when the engine did in fact mint something somewhere.
func bootedKeyMaterial(t *testing.T, dataDir string) []string {
	t.Helper()
	var found []string
	for _, name := range []string{
		"audit-signing.key", "catalog-signing.key", "policy-signing.key", "olivares.db",
	} {
		if fileExistsAt(filepath.Join(dataDir, name)) {
			found = append(found, name)
		}
	}
	return found
}

// TestZeroConfigBootPutsNothingWhereGitCanSeeIt is the headline case: the command
// a first-time user runs, in the directory a first-time user runs it from.
func TestZeroConfigBootPutsNothingWhereGitCanSeeIt(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git is not on PATH; this test's ORACLE is git, so it cannot run here")
	}
	repo := decoyRepo(t)
	sandboxHome(t)
	t.Chdir(repo)

	if before := gitSees(t, repo); before != "" {
		t.Fatalf("the decoy repo was not clean before boot: %q", before)
	}

	// The `serve` shape: no --data-dir, no ReadOnly, no NoImplicitInstall. Its whole
	// job IS to initialize, which is why guards do not apply to it.
	eng, err := boot(context.Background(), bootConfig{Version: "test", Logger: discardLog(), ServeMode: true})
	if err != nil {
		t.Fatalf("zero-config boot must still work — starting with no configuration is a "+
			"product virtue and is NOT what this defect asks us to remove: %v", err)
	}
	dataDir := eng.dataDir
	if cerr := eng.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}

	// Vacuity guard: the engine must actually have installed something.
	if minted := bootedKeyMaterial(t, dataDir); len(minted) == 0 {
		t.Fatalf("boot created no key material at %s — this test would then be proving "+
			"nothing about where key material lands", dataDir)
	}

	if seen := gitSees(t, repo); seen != "" {
		t.Fatalf("`olivares serve` with no --data-dir left files git offers to commit:\n%s\n"+
			"(data dir resolved to %s). A single `git add -A` would publish private keys.", seen, dataDir)
	}

	// AND the installation must not be in the working directory at all.
	//
	// This second assertion exists because the MUTATION PASS caught its absence:
	// reverting defaultDataDir to the relative literal left this test GREEN, because
	// the data directory's own .gitignore (the second layer) hid it from git anyway.
	// One invariant was masking the other, so the headline case measured half of what
	// its name claims.
	//
	// Git-invisibility is not the whole requirement. An installation that appears in
	// whatever directory the operator was standing in is still wrong when git ignores
	// it: `ls` shows it, a tarball collects it, rsync copies it, a Docker build
	// context uploads it, and the next `serve` from a different directory silently
	// starts a DIFFERENT installation.
	if rel, rerr := filepath.Rel(repo, absOrSame(dataDir)); rerr == nil && !strings.HasPrefix(rel, "..") {
		t.Fatalf("the data directory resolved INSIDE the working directory (%s). Hiding it "+
			"from git is the second layer, not a substitute for not putting it there.", dataDir)
	}
}

// TestDataDirInsideARepoIsInvisibleToGit covers the half a .gitignore in OUR repo
// can never reach: the operator who deliberately points --data-dir at a path inside
// THEIR git work tree. Moving the default out of the CWD does not help them.
func TestDataDirInsideARepoIsInvisibleToGit(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git is not on PATH; this test's ORACLE is git, so it cannot run here")
	}
	repo := decoyRepo(t)
	sandboxHome(t)
	t.Chdir(repo)

	dataDir := filepath.Join(repo, "my-olivares-install")
	eng, err := boot(context.Background(), bootConfig{DataDir: dataDir, Version: "test", Logger: discardLog()})
	if err != nil {
		t.Fatalf("boot at an explicit data dir: %v", err)
	}
	if cerr := eng.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}
	if minted := bootedKeyMaterial(t, dataDir); len(minted) == 0 {
		t.Fatalf("boot created no key material at %s", dataDir)
	}

	if seen := gitSees(t, repo); seen != "" {
		t.Fatalf("an explicitly-chosen data dir inside a git work tree is visible to git:\n%s\n"+
			"the data directory must exclude itself, because no .gitignore of OURS ships "+
			"in the customer's repository", seen)
	}
}

// TestDefaultDataDirDoesNotResolveIntoTheWorkingDirectory states the rule the two
// tests above depend on, at the unit the rest of the CLI shares (every `--data-dir`
// flag documents this same default).
func TestDefaultDataDirDoesNotResolveIntoTheWorkingDirectory(t *testing.T) {
	sandboxHome(t)
	cwd := t.TempDir()
	t.Chdir(cwd)

	got, err := defaultDataDir()
	if err != nil {
		t.Fatalf("defaultDataDir: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("defaultDataDir() = %q, want an ABSOLUTE path: a relative default is "+
			"resolved against whatever directory the operator was standing in", got)
	}
	if rel, rerr := filepath.Rel(cwd, got); rerr == nil && !strings.HasPrefix(rel, "..") {
		t.Errorf("defaultDataDir() = %q resolves INSIDE the working directory %q", got, cwd)
	}
}

// TestDefaultDataDirHonoursTheEnvironment pins the override that already worked, so
// moving the default cannot silently break the documented escape hatch.
func TestDefaultDataDirHonoursTheEnvironment(t *testing.T) {
	sandboxHome(t)
	t.Setenv("OLIVARES_DATA_DIR", "/var/lib/olivares")
	got, err := defaultDataDir()
	if err != nil {
		t.Fatalf("defaultDataDir: %v", err)
	}
	if got != "/var/lib/olivares" {
		t.Errorf("defaultDataDir() = %q, want the OLIVARES_DATA_DIR value verbatim", got)
	}
}

// TestDefaultDataDirKeepsUsingAnExistingLegacyInstallation is the compatibility
// half. Someone who ran an older build has a REAL installation at ./olivares-data;
// moving the default must not orphan it and silently start a second, empty one.
func TestDefaultDataDirKeepsUsingAnExistingLegacyInstallation(t *testing.T) {
	sandboxHome(t)
	cwd := t.TempDir()
	t.Chdir(cwd)

	legacy := filepath.Join(cwd, legacyDataDirName)
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	// installationExistsAt's own evidence: a store or a signing key.
	if err := os.WriteFile(filepath.Join(legacy, "olivares.db"), []byte("not really a db"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := defaultDataDir()
	if err != nil {
		t.Fatalf("defaultDataDir: %v", err)
	}
	if got != legacy {
		t.Errorf("defaultDataDir() = %q, want the existing legacy installation %q — an "+
			"upgrade must not orphan the operator's data", got, legacy)
	}
}

// TestDefaultDataDirIgnoresAnEmptyLegacyDirectory is the other side of the same
// coin: an EMPTY ./olivares-data is not an installation (installationExistsAt says
// so), so it must not pull the default back into the working directory.
func TestDefaultDataDirIgnoresAnEmptyLegacyDirectory(t *testing.T) {
	sandboxHome(t)
	cwd := t.TempDir()
	t.Chdir(cwd)
	if err := os.MkdirAll(filepath.Join(cwd, legacyDataDirName), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := defaultDataDir()
	if err != nil {
		t.Fatalf("defaultDataDir: %v", err)
	}
	if got == filepath.Join(cwd, legacyDataDirName) {
		t.Errorf("an EMPTY %s pulled the default back into the working directory", legacyDataDirName)
	}
}

// TestDefaultDataDirRefusesWhenItCannotNameASafeLocation pins the fail-closed edge.
// With no HOME there is no per-user root to use, and the ONE answer that must never
// be reached is "fall back to the working directory" — that is the defect itself.
func TestDefaultDataDirRefusesWhenItCannotNameASafeLocation(t *testing.T) {
	sandboxHome(t)
	t.Setenv("HOME", "")
	cwd := t.TempDir()
	t.Chdir(cwd)

	got, err := defaultDataDir()
	if err == nil {
		t.Fatalf("defaultDataDir() = %q with no HOME; it must refuse and name the flag "+
			"instead of silently choosing the working directory", got)
	}
	if !strings.Contains(err.Error(), "--data-dir") || !strings.Contains(err.Error(), "OLIVARES_DATA_DIR") {
		t.Errorf("the refusal must tell the operator how to state a location, got: %v", err)
	}
}

// --- what the sol-max contrast found, each pinned so it cannot come back --------

// TestDataDirWithAPermissiveGitignoreIsStillExcluded. EnsureDataDir used to return
// success on seeing ANY entry named .gitignore, without reading it. A data directory
// that already carried a permissive ignore file was therefore left fully visible
// while the function reported that it had protected it.
func TestDataDirWithAPermissiveGitignoreIsStillExcluded(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git is not on PATH; this test's ORACLE is git, so it cannot run here")
	}
	repo := decoyRepo(t)
	sandboxHome(t)
	t.Chdir(repo)

	dataDir := filepath.Join(repo, "install")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// The operator's own ignore file: real, and irrelevant to key material.
	const theirs = "# my notes\n*.tmp\n"
	if err := os.WriteFile(filepath.Join(dataDir, ".gitignore"), []byte(theirs), 0o600); err != nil {
		t.Fatal(err)
	}

	eng, err := boot(context.Background(), bootConfig{DataDir: dataDir, Version: "test", Logger: discardLog()})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	if cerr := eng.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}
	if minted := bootedKeyMaterial(t, dataDir); len(minted) == 0 {
		t.Fatalf("boot created no key material at %s", dataDir)
	}

	if seen := gitSees(t, repo); seen != "" {
		t.Fatalf("a data dir holding the operator's own .gitignore stayed visible to git:\n%s\n"+
			"\"a .gitignore exists\" is not \"this directory is excluded\"", seen)
	}
	// And their file must survive: it is theirs, the exclusion is ours.
	got, rerr := os.ReadFile(filepath.Join(dataDir, ".gitignore"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !strings.Contains(string(got), "*.tmp") {
		t.Errorf("the operator's own rules were destroyed:\n%s", got)
	}
}

// TestDataDirAtARepositoryRootDoesNotSwallowTheProject. The other half of the same
// finding, and the more dangerous one: writing `*` beside .git would hide the
// operator's ENTIRE project from git. The engine must refuse to do that and SAY the
// directory is unprotected, rather than quietly doing either.
func TestDataDirAtARepositoryRootDoesNotSwallowTheProject(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git is not on PATH; this test's ORACLE is git, so it cannot run here")
	}
	repo := decoyRepo(t)
	sandboxHome(t)
	t.Chdir(repo)
	if err := os.WriteFile(filepath.Join(repo, "source.go"), []byte("package p\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	eng, err := boot(context.Background(), bootConfig{DataDir: repo, Version: "test", Logger: discardLog()})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	if cerr := eng.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}

	if fileExistsAt(filepath.Join(repo, ".gitignore")) {
		t.Fatal("the engine wrote a `*` exclusion at a REPOSITORY ROOT — that hides the " +
			"operator's whole project, which is worse than the exposure it prevents")
	}
	// The operator's own file must still be visible to git: proof we did not hide it.
	if seen := gitSees(t, repo); !strings.Contains(seen, "source.go") {
		t.Fatalf("the operator's own source stopped being visible to git: %q", seen)
	}
	if w := secure.DataDirVCSWarning(repo); w == "" {
		t.Error("the engine must WARN that this data directory cannot exclude itself; " +
			"a protection that silently does not apply is the defect this change is about")
	}
}

// TestDefaultDataDirRefusesARelativeHome. os.UserHomeDir returns $HOME verbatim
// without validating it, so HOME="." produced ".local/share/olivares" — a relative
// default, which is the whole defect. The earlier tests only used absolute or empty.
func TestDefaultDataDirRefusesARelativeHome(t *testing.T) {
	for _, home := range []string{".", "relative/path", "  ", "./x"} {
		t.Run(home, func(t *testing.T) {
			sandboxHome(t)
			t.Setenv("HOME", home)
			cwd := t.TempDir()
			t.Chdir(cwd)

			got, err := defaultDataDir()
			if err == nil {
				t.Fatalf("defaultDataDir() = %q with HOME=%q; a non-absolute home is not a "+
					"safe location and must be refused, not joined", got, home)
			}
		})
	}
}

// TestLegacyInstallationIsRecognisedWithoutSQLiteOrSigningKeys. A supported Postgres
// deployment whose three signing keys are under external custody (BYOK/CMEK never
// writes them locally) has no olivares.db and no *-signing.key — but it does have
// tls.key and its sealed secret stores. The narrow evidence set counted that as
// "nothing here", so the compatibility branch walked away from those keys and minted
// a second installation elsewhere.
func TestLegacyInstallationIsRecognisedWithoutSQLiteOrSigningKeys(t *testing.T) {
	for _, artifact := range []string{"tls.key", "secret-store.key", "sso-secret.key", "eventing-secret.key"} {
		t.Run(artifact, func(t *testing.T) {
			sandboxHome(t)
			cwd := t.TempDir()
			t.Chdir(cwd)
			legacy := filepath.Join(cwd, legacyDataDirName)
			if err := os.MkdirAll(legacy, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(legacy, artifact), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := defaultDataDir()
			if err != nil {
				t.Fatalf("defaultDataDir: %v", err)
			}
			if got != legacy {
				t.Errorf("a data directory holding %s was not recognized as an installation: "+
					"defaultDataDir() = %q, want %q. Walking away from it mints a SECOND "+
					"installation and leaves those keys behind.", artifact, got, legacy)
			}
		})
	}
}
