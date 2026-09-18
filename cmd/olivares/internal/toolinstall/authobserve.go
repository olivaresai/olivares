// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Auth observation states. Probe --version is not authentication. A login file
// that exists is not a ready account. Values of credential files are never read.
const (
	AuthStateAbsent      = "absent"
	AuthStatePresent     = "present"
	AuthStateUnreadable  = "unreadable"
	AuthStateNotExamined = "not-examined"

	AuthKindLoginFilePresence = "login-file-presence"
)

// AuthObservation is a Stat-only look at a vendor login or config file. It
// never opens the file, so it cannot disclose a secret value.
type AuthObservation struct {
	State string `json:"state"`
	Kind  string `json:"kind"`
	Note  string `json:"note"`
}

func authNote() string {
	return "probe --version is not authentication; presence of a login or config file is not a ready account; file contents are not read"
}

func observeLoginFile(path string) AuthObservation {
	out := AuthObservation{Kind: AuthKindLoginFilePresence, Note: authNote()}
	if path == "" || !filepath.IsAbs(path) {
		out.State = AuthStateNotExamined
		return out
	}
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		out.State = AuthStatePresent
	case errors.Is(err, fs.ErrNotExist):
		out.State = AuthStateAbsent
	default:
		out.State = AuthStateUnreadable
	}
	return out
}

func grokAuthPath(home string) string {
	if !filepath.IsAbs(home) {
		return ""
	}
	return filepath.Join(home, ".grok", "config.toml")
}

func codexAuthPath(home string) string {
	if !filepath.IsAbs(home) {
		return ""
	}
	return filepath.Join(home, ".codex", "auth.json")
}

func claudeAuthPath(home string) string {
	if !filepath.IsAbs(home) {
		return ""
	}
	return filepath.Join(home, ".claude.json")
}
