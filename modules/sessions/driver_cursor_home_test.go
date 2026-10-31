// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// cursorTestHome is a profile home: HOME holds .cursor, which CURSOR_CONFIG_DIR names.
func cursorTestHome(t *testing.T, files map[string]string) *ProviderHomeSnapshot {
	t.Helper()
	user := t.TempDir()
	config := filepath.Join(user, ".cursor")
	if err := os.MkdirAll(config, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		path := filepath.Join(user, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &ProviderHomeSnapshot{Driver: providerDriverCursor, ConfigHome: config, UserHome: user}
}

// The CLI approves a tool from its own config without session/request_permission, so the live
// policy never sees it: the allow list and the approval mode of cli-config.json, and a hook
// that answers "allow". Read from the pinned 2026.10.22-27dcf5f bundle, not run with an account.
// The CLI writes `permissions.allow: ["Shell(ls)"]` and `approvalMode: "allowlist"` itself, so
// that default is the one allow list a launch accepts.
func TestCursorHomeThatPreApprovesToolsRefusesTheLaunch(t *testing.T) {
	t.Parallel()
	for name, files := range map[string]map[string]string{
		"a shell allow":     {".cursor/cli-config.json": `{"permissions":{"allow":["Shell(ls)","Shell(*)"],"deny":[]}}`},
		"a write allow":     {".cursor/cli-config.json": `{"permissions":{"allow":["Write(**)"],"deny":[]}}`},
		"run everything":    {".cursor/cli-config.json": `{"approvalMode":"unrestricted","permissions":{"allow":["Shell(ls)"],"deny":[]}}`},
		"auto review":       {".cursor/cli-config.json": `{"approvalMode":"auto-review"}`},
		"hooks":             {".cursor/hooks.json": `{"version":1,"hooks":{"beforeShellExecution":[{"command":"./allow.sh"}]}}`},
		"not json":          {".cursor/cli-config.json": `{"permissions":`},
		"an array":          {".cursor/cli-config.json": `[]`},
		"allow not a list":  {".cursor/cli-config.json": `{"permissions":{"allow":"Shell(*)"}}`},
		"approval not text": {".cursor/cli-config.json": `{"approvalMode":7}`},
		"unknown approval":  {".cursor/cli-config.json": `{"approvalMode":"yolo"}`},
		"web search":        {".cursor/cli-config.json": `{"autoAcceptWebSearch":true}`},
		"web search a text": {".cursor/cli-config.json": `{"autoAcceptWebSearch":"yes"}`},
		"null":              {".cursor/cli-config.json": `null`},
		"a plugin":          {".cursor/plugins/x/hooks.json": `{}`},
		"claude hooks":      {".claude/settings.json": `{"hooks":{}}`},
		// JSON.parse keeps the last of a repeated key, and keys are case-sensitive.
		"repeated allow key": {".cursor/cli-config.json": `{"permissions":{"allow":[],"allow":["Shell(*)"]}}`},
	} {
		if err := cursorHomeRefusal(cursorTestHome(t, files)); err == nil {
			t.Errorf("%s: the launch was allowed although the home pre-approves tools or cannot be read", name)
		}
	}
}

// A hook is refused as a hook: the person is told what to remove, not that something could not be read.
func TestCursorHomeHooksAreRefusedByName(t *testing.T) {
	t.Parallel()
	for _, dir := range []string{".cursor", ""} {
		home := cursorTestHome(t, map[string]string{".cursor/hooks.json": `{"version":1,"hooks":{}}`})
		if dir == "" { // CURSOR_CONFIG_DIR elsewhere: the CLI still reads <HOME>/.cursor/hooks.json
			home.ConfigHome = t.TempDir()
		}
		if err := cursorHomeRefusal(home); err == nil || !strings.Contains(err.Error(), "hooks") {
			t.Errorf("config home %q: err = %v, want a refusal naming the hooks", home.ConfigHome, err)
		}
	}
}

// A config that is a link is not followed, and a FIFO in its place cannot stall the launch.
func TestCursorHomeConfigThatIsALinkOrAFIFORefusesTheLaunch(t *testing.T) {
	t.Parallel()
	home := cursorTestHome(t, nil)
	target := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home.ConfigHome, "cli-config.json")); err != nil {
		t.Fatal(err)
	}
	if err := cursorHomeRefusal(home); err == nil {
		t.Error("a linked cli-config.json was followed")
	}
	fifo := cursorTestHome(t, nil)
	if err := syscall.Mkfifo(filepath.Join(fifo.ConfigHome, "cli-config.json"), 0o600); err != nil {
		t.Skip("no FIFOs here:", err)
	}
	done := make(chan error, 1)
	go func() { done <- cursorHomeRefusal(fifo) }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a FIFO in place of cli-config.json started a session")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a FIFO in place of cli-config.json stalled the launch")
	}
}

func TestCursorHomeWithOnlyTheCLIsOwnDefaultsStartsTheSession(t *testing.T) {
	t.Parallel()
	for name, files := range map[string]map[string]string{
		"an empty home":    {},
		"the CLI's config": {".cursor/cli-config.json": `{"version":1,"permissions":{"allow":["Shell(ls)"],"deny":[]},"approvalMode":"allowlist","model":{"modelId":"gpt-5"}}`},
		"a deny list":      {".cursor/cli-config.json": `{"permissions":{"allow":["Shell(ls)"],"deny":["Shell(rm)"]}}`},
		"no allow list":    {".cursor/cli-config.json": `{"permissions":{"deny":[]}}`},
		"mcp servers":      {".cursor/mcp.json": `{"mcpServers":{"x":{"command":"y"}}}`},
	} {
		if err := cursorHomeRefusal(cursorTestHome(t, files)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A config that is not a plain readable file is refused, not assumed empty: the child can
// replace its own config with a directory or a file of any size.
func TestCursorHomeConfigThatCannotBeReadRefusesTheLaunch(t *testing.T) {
	t.Parallel()
	home := cursorTestHome(t, nil)
	if err := os.Mkdir(filepath.Join(home.ConfigHome, "cli-config.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := cursorHomeRefusal(home); err == nil {
		t.Error("a directory in place of cli-config.json started a session")
	}
	big := cursorTestHome(t, map[string]string{".cursor/cli-config.json": `{"x":"` + string(make([]byte, 2<<20)) + `"}`})
	if err := cursorHomeRefusal(big); err == nil {
		t.Error("an oversized cli-config.json started a session")
	}
}

func TestCursorHomeNeedsAConfigurationHome(t *testing.T) {
	t.Parallel()
	// Both homes are needed: the hooks the CLI loads sit in <HOME>/.cursor, so a snapshot that
	// names only the configuration home cannot be shown free of them.
	for _, home := range []*ProviderHomeSnapshot{nil, {Driver: providerDriverCursor}, {Driver: providerDriverCursor, ConfigHome: t.TempDir()}} {
		if err := cursorHomeRefusal(home); err == nil {
			t.Errorf("%+v: a Cursor session started without a configuration home", home)
		}
	}
}
