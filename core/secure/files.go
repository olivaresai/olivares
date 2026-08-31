// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package secure

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// dataDirPerm and keyFilePerm are the required permissions for the data
// directory and for private key/secret files.
const (
	dataDirPerm = 0o700
	keyFilePerm = 0o600
)

// EnsureDir creates dir (and parents) with 0700 permissions if absent. It does
// not loosen an existing directory.
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, dataDirPerm); err != nil {
		return fmt.Errorf("secure: create %s: %w", dir, err)
	}
	return nil
}

// vcsExclusion is a .gitignore that excludes its own directory, and itself, from
// the enclosing repository. `*` matches every entry at this level and below.
const vcsExclusionHeader = "# Created by Olivares AI. This directory holds PRIVATE KEYS (0600) and the\n" +
	"# engine's store. It excludes itself from any enclosing git repository so a\n" +
	"# `git add -A` cannot publish that key material. Do not commit this directory.\n"

const vcsExclusionRule = "*\n"

// EnsureDataDir creates the ENGINE'S data directory: EnsureDir, plus a .gitignore
// that hides the directory from any git work tree it happens to land in.
//
// WHY THE SECOND HALF EXISTS. The data directory holds four private keys at
// 0600 and the store. Until the default path was RELATIVE, so a plain
// `olivares serve` created it in whatever directory the operator was standing in —
// routinely a clone of something — where `git status` listed it and one `git add -A`
// would have committed the keys. Moving the default out of the working directory
// (defaultDataDir) fixes the default; it CANNOT fix the operator who deliberately
// passes --data-dir ./olivares-data inside their own repository, and no .gitignore
// of ours ships in the customer's tree. So the directory carries its own exclusion.
//
// This is the pattern Python's `venv` adopted in 3.11 for the same reason: a
// tool-created directory that must never be committed states so where git will
// actually read it, rather than relying on every downstream repository to know.
//
// WHAT THIS DOES NOT DO, stated because a control whose limits are unwritten gets
// believed past them (all three named by the sol-max contrast):
//
//   - It cannot hide files git ALREADY TRACKS. An ignore rule never applies to a
//     tracked path; if key material was committed once, this does not retract it.
//   - It does not survive `git add -f`, which exists precisely to override ignores.
//   - At a repository ROOT it does nothing at all, deliberately (see above), and
//     DataDirVCSWarning is what tells the operator so.
//
// It closes the accident — `git status` listing an installation and `git add -A`
// sweeping it up — not a determined commit.
func EnsureDataDir(dir string) error {
	if err := EnsureDir(dir); err != nil {
		return err
	}
	// NEVER at a repository root. `--data-dir .` from the top of a checkout would
	// otherwise drop `*` beside .git and hide the operator's ENTIRE project from
	// git — a far worse outcome than the one this marker prevents. Found by the
	// sol-max contrast, which read the consequence this function's first version
	// did not. DataDirVCSWarning is how the caller tells the operator instead.
	if IsRepositoryRoot(dir) {
		return nil
	}
	marker := filepath.Join(dir, ".gitignore")
	existing, err := os.ReadFile(marker)
	switch {
	case err == nil:
		// "A .gitignore exists" is NOT "this directory is excluded", and the first
		// version of this function conflated them: a data directory that already
		// carried a permissive ignore file was left fully visible while this
		// returned success. Read it, and add the rule only if it is not already there.
		if excludesEverything(existing) {
			return nil
		}
		if err := appendVCSExclusion(marker, existing); err != nil {
			return fmt.Errorf("secure: extend VCS exclusion in %s: %w", dir, err)
		}
		return nil
	case errors.Is(err, fs.ErrNotExist):
		if werr := os.WriteFile(marker, []byte(vcsExclusionHeader+vcsExclusionRule), 0o600); werr != nil {
			return fmt.Errorf("secure: write VCS exclusion in %s: %w", dir, werr)
		}
		return nil
	default:
		return fmt.Errorf("secure: read %s: %w", marker, err)
	}
}

// excludesEverything reports whether an ignore file already carries a bare `*`
// rule, which excludes this directory and everything under it.
func excludesEverything(content []byte) bool {
	for _, line := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(line) == "*" {
			return true
		}
	}
	return false
}

// appendVCSExclusion adds the rule to an ignore file the operator already had,
// rather than replacing it: the file is theirs, and the exclusion is ours.
func appendVCSExclusion(marker string, existing []byte) error {
	f, err := os.OpenFile(marker, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err //nolint:wrapcheck // caller wraps with the directory
	}
	defer func() { _ = f.Close() }()
	prefix := ""
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		prefix = "\n"
	}
	_, err = f.WriteString(prefix + "\n" + vcsExclusionHeader + vcsExclusionRule)
	return err //nolint:wrapcheck // caller wraps with the directory
}

// IsRepositoryRoot reports whether dir is the top of a git work tree (or a
// worktree/submodule, whose .git is a FILE). Used to refuse writing an exclusion
// that would swallow a whole project.
func IsRepositoryRoot(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// DataDirVCSWarning returns a message when the data directory cannot carry its own
// exclusion — today, only when it IS a repository root. Empty means it is covered.
// The caller logs this: silence would be the same conflation this function fixes.
func DataDirVCSWarning(dir string) string {
	if !IsRepositoryRoot(dir) {
		return ""
	}
	return "the data directory is the ROOT of a git repository, so it cannot exclude " +
		"itself (a `*` rule here would hide your entire project). The private keys and " +
		"store in " + dir + " are visible to git: add them to your .gitignore, or point " +
		"--data-dir somewhere outside the work tree"
}

// writeSecret writes b to path with 0600 permissions, creating parents at 0700.
// It writes atomically (temp file + rename) so a reader never sees a partial key,
// and it GUARANTEES the mode rather than requesting it.
//
// THE DEFECT THIS CLOSES, measured 2026-08-06 on the sibling `license keygen` and found
// here by looking for the same shape. `os.WriteFile` applies its perm argument ONLY WHEN
// IT CREATES THE FILE: an existing path is truncated and keeps whatever mode it already
// had. The staging file used to be `path + ".tmp"` — a PREDICTABLE name in the data
// directory — so a leftover from a restore, a copy, an interrupted run or an older
// version sitting there at 0644 was truncated, kept 0644, and then RENAMED OVER THE
// TARGET. The audit signing key would land world-readable while this function returned
// nil, and the security-hardening guide cites this line as the evidence for its
// "strict file perms (0600) on keys/secrets" claim.
//
// readSecret below fails closed on a too-wide mode, so the end state was a refusal rather
// than a silent leak — that mitigation is real and is why this is not rated higher. It is
// not a reason to leave it: the file was still written readable by everyone, and a control
// whose only defense is another control downstream is one edit away from being none.
//
// os.CreateTemp names the staging file randomly (nothing can pre-empt it) and creates it
// 0600. The Chmod is still explicit and the mode is VERIFIED before the rename carries the
// secret into place, because a promise nobody checks is a promise nobody keeps.
func writeSecret(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := EnsureDir(dir); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".olivares-secret-*")
	if err != nil {
		return fmt.Errorf("secure: stage %s: %w", path, err)
	}
	tmpName := tmp.Name()
	fail := func(format string, a ...any) error {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf(format, a...)
	}
	if err := tmp.Chmod(keyFilePerm); err != nil {
		return fail("secure: set mode %04o while staging %s: %w", keyFilePerm, path, err)
	}
	if _, err := tmp.Write(b); err != nil {
		return fail("secure: write %s: %w", path, err)
	}
	// The secret must survive a crash between the write and the rename; without this the
	// rename can be durable while the bytes it published are not.
	if err := tmp.Sync(); err != nil {
		return fail("secure: flush %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("secure: close the staging file for %s: %w", path, err)
	}
	if err := assertPerm(tmpName, keyFilePerm); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("secure: install %s: %w", path, err)
	}
	// The rename preserves the staging file's mode, but asserting it on the FINAL path is
	// what makes this function's promise checkable from outside.
	return assertPerm(path, keyFilePerm)
}

// assertPerm refuses when the file on disk does not carry the mode that was promised.
// Without it every guarantee above is a comment. It is the same discipline the license
// keygen ceremony now uses, and it is deliberately separate from readSecret's fail-closed
// read: one proves the write, the other refuses a bad read, and neither substitutes for
// the other.
func assertPerm(path string, want os.FileMode) error {
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("secure: stat %s to verify its mode: %w", path, err)
	}
	if got := st.Mode().Perm(); got != want.Perm() {
		return fmt.Errorf("secure: %s has mode %04o, not the %04o this package promises — "+
			"refusing to report success over secret material with the wrong custody", path, got, want.Perm())
	}
	return nil
}

// readSecret reads a secret file, FAILING CLOSED if its permissions are wider
// than owner-only (any group/other bit set). It does not silently fix the mode.
// It is for a LOCAL, engine-owned key (the mint-on-first-boot path).
func readSecret(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("secure: refusing to read %s: permissions %o are too open (want 0600)", path, info.Mode().Perm())
	}
	return os.ReadFile(path)
}

// readSharedSecret reads a SHARED secret file — one provisioned out of band and
// mounted read-only (a Kubernetes Secret) for the HA shared-key path. A
// Secret volume's files are owned root:fsGroup, so a non-root engine reads them
// through the GROUP bit (mode 0440); the owner-only check of readSecret would
// wrongly reject that. So this relaxes to "must not be WORLD-readable" (no other
// bits) while still rejecting a 0644/0444 mount — the confidentiality boundary for
// a mounted Secret is the Secret + RBAC + at-rest encryption, not the file mode,
// but world-readable is still refused as defense in depth.
func readSharedSecret(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o007 != 0 {
		return nil, fmt.Errorf("secure: refusing to read %s: permissions %o are world-readable (mount the Secret at 0400/0440)", path, info.Mode().Perm())
	}
	return os.ReadFile(path)
}

// fileExists reports whether path exists as a regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
