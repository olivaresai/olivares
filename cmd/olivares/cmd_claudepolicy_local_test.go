//go:build linux

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckinToolsUsesWorkstationNotFleetServer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OLIVARES_DATA_DIR", filepath.Join(home, "data"))
	t.Setenv("PATH", home)
	// The workstation has no tools; the fleet server claims Claude is installed.
	srv := newCheckinToolsEngine(t, checkinToolsInventory, true)
	if _, _, err := execRoot(t, lot3Args(srv.URL, "claude-policy", "checkin", "managed-settings", "--scope", "local-host", "--tools")...); err != nil {
		t.Fatal(err)
	}
	tools, present := checkinBodyTools(t, srv.lastBody())
	if !present {
		t.Fatal("missing tools")
	}
	for _, tool := range tools {
		if tool["state"] != "not_installed" {
			t.Fatalf("reported fleet tool as workstation tool: %v", tool)
		}
	}
}

func localCheckinTool(t *testing.T, version, status string) {
	t.Helper()
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("OLIVARES_DATA_DIR", filepath.Join(home, "data"))
	t.Setenv("PATH", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	script := "#!/bin/sh\ncase \"$1\" in\n--version) printf '%s\\n' '" + version + " (Claude Code)';;\nauth) printf '%s\\n' '" + status + "';;\n*) exit 2;;\nesac\n"
	if err := os.WriteFile(filepath.Join(home, "claude"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestCheckinToolsReportsLocalVersionAndSignIn(t *testing.T) {
	for _, tc := range []struct{ name, version, status, wantVersion, wantState string }{
		{"ready", "2.1.286", `{"loggedIn":true,"email":"private@example.test"}`, "2.1.286", "ready"},
		{"signed out", "2.1.285", `{"loggedIn":false}`, "2.1.285", "not_signed_in"},
		{"invalid version", "bad/version", `{"loggedIn":true}`, "", "ready"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			localCheckinTool(t, tc.version, tc.status)
			srv := newCheckinToolsEngine(t, checkinToolsInventory, false)
			if _, _, err := execRoot(t, lot3Args(srv.URL, "claude-policy", "checkin", "managed-settings", "--scope", "local-host", "--tools")...); err != nil {
				t.Fatal(err)
			}
			tools, _ := checkinBodyTools(t, srv.lastBody())
			found := false
			for _, tool := range tools {
				if tool["driver"] == "claude" {
					found = true
					if tool["state"] != tc.wantState {
						t.Fatalf("state = %v", tool)
					}
					if tc.wantVersion == "" {
						if _, ok := tool["version"]; ok {
							t.Fatalf("invalid version sent: %v", tool)
						}
					} else if tool["version"] != tc.wantVersion {
						t.Fatalf("version = %v", tool)
					}
				}
			}
			if !found {
				t.Fatal("local Claude missing")
			}
			if strings.Contains(srv.lastBody(), "private@example.test") {
				t.Fatal("account leaked")
			}
		})
	}
}

func TestCheckinToolsUsesLocalXDGHome(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	dataHome := filepath.Join(home, "custom-data")
	t.Setenv("HOME", home)
	t.Setenv("OLIVARES_DATA_DIR", filepath.Join(home, "data"))
	t.Setenv("PATH", home)
	t.Setenv("XDG_DATA_HOME", dataHome)
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then printf '1.19.4\\n'; elif [ \"$XDG_DATA_HOME\" = '" + dataHome + "' ]; then printf 'OpenCode api\\n'; fi\n"
	if err := os.WriteFile(filepath.Join(home, "opencode"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	srv := newCheckinToolsEngine(t, checkinToolsInventory, true)
	if _, _, err := execRoot(t, lot3Args(srv.URL, "claude-policy", "checkin", "managed-settings", "--scope", "xdg-host", "--tools")...); err != nil {
		t.Fatal(err)
	}
	tools, _ := checkinBodyTools(t, srv.lastBody())
	for _, tool := range tools {
		if tool["driver"] == "opencode" {
			if tool["state"] != "ready" {
				t.Fatalf("local XDG login was missed: %v", tool)
			}
			return
		}
	}
	t.Fatal("OpenCode missing")
}

func TestCheckinToolsRelativeConfigHomeDoesNotCreateDirectory(t *testing.T) {
	localCheckinTool(t, "2.1.286", `{"loggedIn":true}`)
	t.Chdir(t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "relative-tool-config")
	srv := newCheckinToolsEngine(t, checkinToolsInventory, true)
	if _, _, err := execRoot(t, lot3Args(srv.URL, "claude-policy", "checkin", "managed-settings", "--scope", "host-7", "--tools")...); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("relative-tool-config"); !os.IsNotExist(err) {
		t.Fatal("relative config home caused a local write")
	}
	tools, _ := checkinBodyTools(t, srv.lastBody())
	for _, tool := range tools {
		if tool["driver"] == "claude" {
			if tool["state"] != "error" {
				t.Fatalf("relative home should be reported as unreadable: %v", tool)
			}
			return
		}
	}
	t.Fatal("Claude missing")
}

// A local snapshot failure must stop before the fleet POST; retaining an old
// report after silently failing to read the workstation would hide drift.
func TestClaudePolicyCheckinToolsAbortsBeforeAnySubmission(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, "data")
	t.Setenv("HOME", home)
	t.Setenv("OLIVARES_DATA_DIR", data)
	t.Setenv("PATH", home)
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "tools"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	srv := newCheckinToolsEngine(t, checkinToolsInventory, true)
	_, _, err := execRoot(t, lot3Args(srv.URL, "claude-policy", "checkin", "managed-settings", "--scope", "host-7", "--tools")...)
	if err == nil {
		t.Fatal("a failed local snapshot must fail the check-in")
	}
	if srv.calls.Load() != 0 {
		t.Fatal("submitted despite a failed local snapshot")
	}
}

func TestClaudePolicyCheckinToolsReportsAFailedProbeAsError(t *testing.T) {
	localCheckinTool(t, "2.1.286", `{"loggedIn":true}`)
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then printf '2.1.286 (Claude Code)\\n'; else printf '%070000d' 0; fi\n"
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "claude"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	srv := newCheckinToolsEngine(t, checkinToolsInventory, true)
	if _, _, err := execRoot(t, lot3Args(srv.URL, "claude-policy", "checkin", "managed-settings", "--scope", "host-7", "--tools")...); err != nil {
		t.Fatal(err)
	}
	tools, present := checkinBodyTools(t, srv.lastBody())
	if !present {
		t.Fatal("no tool report")
	}
	found := false
	for _, tool := range tools {
		if tool["driver"] == "claude" {
			found = true
			if tool["state"] != "error" || tool["version"] != "2.1.286" {
				t.Fatalf("wrong local failed probe: %v", tool)
			}
		}
	}
	if !found {
		t.Fatal("local Claude missing")
	}
}

func TestCheckinToolsDestinationValidationPrecedesLocalReads(t *testing.T) {
	localCheckinTool(t, "2.1.286", `{"loggedIn":true}`)
	data := os.Getenv("OLIVARES_DATA_DIR")
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "tools"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(os.Getenv("HOME"), "missing.token")
	srv := newCheckinToolsEngine(t, checkinToolsInventory, true)
	_, _, err := execRoot(t, "claude-policy", "checkin", "managed-settings", "--scope", "host-7", "--tools", "--server", srv.URL, "--tenant", "tenant-a", "--token-file", missing)
	if err == nil || !strings.Contains(err.Error(), "missing.token") {
		t.Fatalf("destination credentials were not validated first: %v", err)
	}
	if srv.calls.Load() != 0 {
		t.Fatal("request made with missing credentials")
	}
}

func TestCheckinToolsRejectsDamagedManagedExecutable(t *testing.T) {
	t.Setenv(cliConfigOverrideEnv, filepath.Join(t.TempDir(), "client.yaml"))
	f := newAgentToolFixture(t)
	f.publish("2.1.261")
	if code, _, stderr := f.run(f.installArgs("--version", "2.1.261", "--yes")...); code != 0 {
		t.Fatalf("fixture install: %s", stderr)
	}
	home := t.TempDir()
	config := filepath.Join(home, ".claude")
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	t.Setenv("PATH", home)
	t.Setenv("OLIVARES_DATA_DIR", filepath.Dir(f.root))
	exe := filepath.Join(f.root, "claude", "2.1.261-linux-x64", "bin", "claude")
	// Change the verified bytes. If the reader executes them, the native status
	// command writes in its permitted config directory and returns a valid login.
	damaged := "#!/bin/sh\nif [ \"$1\" = --version ]; then printf '2.1.261 (Claude Code)\\n'; else printf ran > \"$CLAUDE_CONFIG_DIR/damaged-ran\"; printf '{\"loggedIn\":true}\\n'; fi\n"
	if err := os.WriteFile(exe, []byte(damaged), 0700); err != nil {
		t.Fatal(err)
	}
	srv := newCheckinToolsEngine(t, checkinToolsInventory, true)
	if _, _, err := execRoot(t, lot3Args(srv.URL, "claude-policy", "checkin", "managed-settings", "--scope", "host-7", "--tools")...); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(config, "damaged-ran")); !os.IsNotExist(err) {
		t.Fatal("damaged managed executable was run")
	}
	tools, _ := checkinBodyTools(t, srv.lastBody())
	for _, tool := range tools {
		if tool["driver"] == "claude" {
			if tool["state"] != "error" {
				t.Fatalf("damaged install not reported as error: %v", tool)
			}
			if _, ok := tool["version"]; ok {
				t.Fatalf("untrusted version reported: %v", tool)
			}
			return
		}
	}
	t.Fatal("damaged Claude report missing")
}

// Cursor's generic program name also belongs to unrelated tools such as Grok.
// Neither PATH nor vendor-default discovery may select it for a Cursor probe.
func TestCheckinToolsIgnoresUnmanagedCursorAgent(t *testing.T) {
	for _, source := range []string{"PATH", "vendor default"} {
		t.Run(source, func(t *testing.T) {
			home := t.TempDir()
			if err := os.Chmod(home, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			t.Setenv("OLIVARES_DATA_DIR", filepath.Join(home, "data"))
			t.Setenv("PATH", home)
			t.Setenv(cliConfigOverrideEnv, filepath.Join(home, "client.yaml"))
			bin := home
			if source == "vendor default" {
				bin = filepath.Join(home, ".local", "bin")
				if err := os.MkdirAll(bin, 0700); err != nil {
					t.Fatal(err)
				}
			}
			marker := filepath.Join(home, "agent-ran")
			script := "#!/bin/sh\nprintf ran > '" + marker + "'\nprintf 'Grok Build 1.0.0\\n'\n"
			if err := os.WriteFile(filepath.Join(bin, "agent"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			srv := newCheckinToolsEngine(t, checkinToolsInventory, true)
			if _, _, err := execRoot(t, lot3Args(srv.URL, "claude-policy", "checkin", "managed-settings", "--scope", "local-host", "--tools")...); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Error("unrelated agent was executed as Cursor")
			}
			tools, _ := checkinBodyTools(t, srv.lastBody())
			for _, tool := range tools {
				if tool["driver"] == "cursor" {
					if tool["state"] != "not_installed" || tool["version"] != nil {
						t.Fatalf("unrelated agent reported as Cursor: %v", tool)
					}
					return
				}
			}
			t.Fatal("Cursor inventory row missing")
		})
	}
}
