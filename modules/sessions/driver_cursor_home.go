// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
)

// cursorConfigMaxBytes bounds the cli-config.json a launch reads. A real one is a few KiB.
const cursorConfigMaxBytes = 1 << 20

// cursorDefaultAllow is the one allow list the CLI writes into its own config (its default
// config is `permissions: {allow: ["Shell(ls)"], deny: []}`, approvalMode "allowlist"), so it
// is the one a launch accepts.
var cursorDefaultAllow = []string{"Shell(ls)"}

// cursorHomeRefusal is the launch-time check of a Cursor CLI profile's home.
//
// Cursor approves a tool from its own configuration without session/request_permission, so
// the session's live policy never sees it (read from the pinned 2026.10.22-27dcf5f package,
// not run with an account): the allow list of cli-config.json, its approvalMode when it is
// not the allowlist ("unrestricted" is Run Everything and "auto-review" lets the agent
// decide), autoAcceptWebSearch, and a hook in hooks.json, whose answer can be "allow". Hooks
// and installed plugins (which ship hooks) and the Claude user settings the CLI also loads
// hooks from are refused too. The child can write its own home and the setting takes effect
// at the next start, so the check runs on every create and resume; a change made during a
// session is seen at the next one. A file that cannot be read is refused, never assumed
// empty. MCP servers of the home are not refused: they are reported as not governed
// (mcpGovernanceWarning). Not checked, because not measured: configuration the CLI might
// load from a parent of the working folder.
func cursorHomeRefusal(home *ProviderHomeSnapshot) error {
	if home == nil || home.ConfigHome == "" || home.UserHome == "" {
		return badRequest("a Cursor CLI session needs a profile with its configuration home and its user home")
	}
	dirs := []string{home.ConfigHome}
	// The CLI reads hooks and plugins from <HOME>/.cursor whatever CURSOR_CONFIG_DIR names.
	if dir := filepath.Join(home.UserHome, ".cursor"); dir != home.ConfigHome {
		dirs = append(dirs, dir)
	}
	for _, dir := range dirs {
		if err := cursorAbsent(filepath.Join(dir, "hooks.json"), "holds Cursor hooks (hooks.json)"); err != nil {
			return err
		}
		if err := cursorPluginsAbsent(filepath.Join(dir, "plugins")); err != nil {
			return err
		}
	}
	if err := cursorAbsent(filepath.Join(home.UserHome, ".claude", "settings.json"), "holds Claude settings (.claude/settings.json), whose hooks Cursor also loads"); err != nil {
		return err
	}
	path := filepath.Join(home.ConfigHome, "cli-config.json")
	raw, err := cursorReadConfig(path)
	if err != nil {
		return err
	}
	if raw == nil {
		return nil
	}
	preApproves, err := cursorConfigPreApproves(raw)
	if err != nil {
		return cursorUnchecked("cli-config.json is not the object the CLI writes")
	}
	if preApproves {
		return conflictErr("the Cursor CLI settings in this profile's home pre-approve tools (permissions.allow beyond the CLI's default, " +
			"an approvalMode other than allowlist, or autoAcceptWebSearch), which would skip the session's approval policy; remove them and start the session again")
	}
	return nil
}

// cursorUnchecked is the refusal for a home that could not be checked, with the reason in
// words: never a path or file content.
func cursorUnchecked(reason string) error {
	return conflictErr("the Cursor CLI settings in this profile's home could not be checked (" + reason + "), so the session did not start")
}

// cursorAbsent refuses when path exists, or when whether it exists cannot be told.
func cursorAbsent(path, what string) error {
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return conflictErr("the profile's home " + what + ", which can approve a tool without the session's approval policy; remove it and start the session again")
	case errors.Is(err, fs.ErrNotExist):
		return nil
	}
	return cursorUnchecked("a file in it cannot be examined")
}

// cursorPluginsAbsent refuses a plugins directory with anything in it: a plugin can ship hooks.
func cursorPluginsAbsent(dir string) error {
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return cursorUnchecked("the plugins folder cannot be read")
	case len(entries) > 0:
		return conflictErr("the profile's home holds Cursor plugins, which can carry hooks that approve a tool without the session's approval policy; remove them and start the session again")
	}
	return nil
}

// cursorReadConfig reads cli-config.json; nil when it is absent. The file is opened without
// blocking and judged by the opened handle, so a FIFO swapped in by the child cannot stall the
// launch and a link or oversized file is refused, not followed.
func cursorReadConfig(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, cursorUnchecked("cli-config.json cannot be opened")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, cursorUnchecked("cli-config.json is not a regular file")
	}
	if info.Size() > cursorConfigMaxBytes {
		return nil, cursorUnchecked("cli-config.json is larger than 1 MiB")
	}
	raw, err := io.ReadAll(io.LimitReader(f, cursorConfigMaxBytes+1))
	if err != nil || len(raw) > cursorConfigMaxBytes {
		return nil, cursorUnchecked("cli-config.json cannot be read")
	}
	return raw, nil
}

// cursorConfigPreApproves reads cli-config.json the way the CLI's JSON.parse does: keys are
// case-sensitive and the last of a repeated key wins, which decoding into a map gives. A
// shape that is not the expected one is an error, left to the caller to refuse.
func cursorConfigPreApproves(raw []byte) (bool, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil || top == nil {
		return false, errors.New("cli-config.json is not an object")
	}
	if mode, ok := top["approvalMode"]; ok {
		var value string
		if err := json.Unmarshal(mode, &value); err != nil {
			return false, err
		}
		if value != "allowlist" {
			return true, nil
		}
	}
	if auto, ok := top["autoAcceptWebSearch"]; ok {
		var value bool
		if err := json.Unmarshal(auto, &value); err != nil {
			return false, err
		}
		if value {
			return true, nil
		}
	}
	rawPermissions, ok := top["permissions"]
	if !ok {
		return false, nil
	}
	var permissions map[string]json.RawMessage
	if err := json.Unmarshal(rawPermissions, &permissions); err != nil || permissions == nil {
		return false, errors.New("permissions is not an object")
	}
	rawAllow, ok := permissions["allow"]
	if !ok {
		return false, nil
	}
	var allow []string
	if err := json.Unmarshal(rawAllow, &allow); err != nil {
		return false, err
	}
	for _, rule := range allow {
		if !slices.Contains(cursorDefaultAllow, rule) {
			return true, nil
		}
	}
	return false, nil
}
