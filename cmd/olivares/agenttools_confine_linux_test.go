// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// A tool's own login, its status read and the provider probes are children of
// the engine, like a session. The composition confines them the same way: they
// cannot read the engine data directory, and a root engine's child holds no
// capability (#1114). Only an engine holding CAP_SETPCAP can empty the bounding
// set; without it, no_new_privs keeps any exec from regaining the cleared sets.
func TestAgentToolChildIsConfinedLikeASessionChild(t *testing.T) {
	if s := confine.Probe(); s.Mode != confine.ModeLandlock {
		t.Fatal("this security reproducer requires Landlock: " + s.Reason)
	}
	dir, home, bin := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	for _, name := range []string{"OLIVARES_SOURCES_CONFIG", "OLIVARES_AGENT_GATEWAY_CONFIG", "OLIVARES_HOOK_PEP_CONFIG", "OLIVARES_COMMUNICATION_ACTIVATION", envSessionTokenFile, envSessionRuntimeWIF} {
		t.Setenv(name, "")
	}
	eng, err := boot(t.Context(), bootConfig{DataDir: dir, Engine: "sqlite", Version: version, Logger: discardLogger(), ApplyModuleProfile: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	engineFile := filepath.Join(dir, "engine-only")
	if err := os.WriteFile(engineFile, []byte("engine-only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A labelled Claude stand-in: its status read reports its capability sets and
	// whether it could read the engine file, into its own login home.
	program := filepath.Join(bin, "claude")
	script := "#!/bin/sh\n" +
		"/bin/grep -E '^(Cap|NoNewPrivs)' /proc/self/status > \"$HOME/report\"\n" +
		"if /bin/cat '" + engineFile + "' >/dev/null 2>&1; then echo EngineFileRead >> \"$HOME/report\"; fi\n" +
		"echo '{\"loggedIn\":false}'\n"
	if err := os.WriteFile(program, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	loginHome := filepath.Join(dir, toolLoginsDir, "fixture", "claude")
	eng.agentTools.SetProgramResolver(func(driver string) string {
		if driver == "claude" {
			return program
		}
		return ""
	})
	eng.agentTools.SetLoginHome(func(context.Context, model.TenantID, string, string) (string, string, error) {
		return loginHome, filepath.Join(loginHome, ".claude"), nil
	})

	installed, signedIn, err := eng.agentTools.LoginStatus(t.Context(), "fixture", "claude")
	if err != nil || !installed || signedIn {
		t.Fatalf("status read = installed %v, signed in %v, %v", installed, signedIn, err)
	}
	report, err := os.ReadFile(filepath.Join(loginHome, "report"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(report), "EngineFileRead") {
		t.Errorf("engine euid %d: the tool's status read could read the engine data directory", os.Geteuid())
	}
	const none = "0000000000000000"
	engine := statusCapabilities(t, readProcStatus(t))
	bound := engine["CapBnd"]
	if effective, err := strconv.ParseUint(engine["CapEff"], 16, 64); err != nil {
		t.Fatal(err)
	} else if effective&(1<<unix.CAP_SETPCAP) != 0 {
		bound = none
	}
	t.Logf("engine euid %d: CapEff %s CapBnd %s", os.Geteuid(), engine["CapEff"], engine["CapBnd"])
	got := statusCapabilities(t, string(report))
	want := map[string]string{"CapInh": none, "CapPrm": none, "CapEff": none, "CapAmb": none, "CapBnd": bound, "NoNewPrivs": "1"}
	for set, value := range want {
		if got[set] != value {
			t.Errorf("engine euid %d: the tool's status read %s = %s, want %s", os.Geteuid(), set, got[set], value)
		}
	}
}

func readProcStatus(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// statusCapabilities parses the Cap* and NoNewPrivs lines of a /proc/<pid>/status text.
func statusCapabilities(t *testing.T, status string) map[string]string {
	t.Helper()
	sets := map[string]string{}
	for _, line := range strings.Split(status, "\n") {
		if name, value, ok := strings.Cut(strings.TrimSpace(line), ":"); ok && (strings.HasPrefix(name, "Cap") || name == "NoNewPrivs") {
			sets[name] = strings.TrimSpace(value)
		}
	}
	for _, set := range []string{"CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb"} {
		if len(sets[set]) != 16 {
			t.Fatalf("no %s in the capability report:\n%s", set, status)
		}
	}
	return sets
}
