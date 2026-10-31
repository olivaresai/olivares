// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall/toolinstalltest"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// An install job ends with a --version probe of the freshly staged vendor
// binary. That probe is a child of the engine like a tool's login (#1114): the
// composition confines it, so it cannot read the engine data directory and a
// root engine's probe holds no capability (#1430).
func TestAgentToolInstallProbeIsConfinedLikeASessionChild(t *testing.T) {
	if s := confine.Probe(); s.Mode != confine.ModeLandlock {
		t.Fatal("this security reproducer requires Landlock: " + s.Reason)
	}
	toolinstalltest.RequireGPG(t)
	verifier, err := toolinstall.NewGPGVerifier(context.Background(), exec.LookPath)
	if err != nil {
		t.Fatal(err)
	}
	key := toolinstalltest.GenerateKey(t, "Olivares Probe Fixture <probe-fixture@olivares.invalid>")
	srv := toolinstalltest.NewServer(t)
	dir, home := toolinstalltest.ExecCapableDir(t), t.TempDir()
	engineFile := filepath.Join(dir, "engine-only")
	if err := os.WriteFile(engineFile, []byte("engine-only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A labelled Claude stand-in: its --version reports its capability sets and
	// whether it could read the engine file, in the probe output the receipt keeps.
	const release = "2.1.261"
	script := "#!/bin/sh\n" +
		"printf '%s (Claude Code)\\n' '" + release + "'\n" +
		"/bin/grep -E '^(Cap|NoNewPrivs)' /proc/self/status\n" +
		"if /bin/cat '" + engineFile + "' >/dev/null 2>&1; then echo EngineFileRead; fi\n"
	srv.Publish(t, key, release, map[string][]byte{"linux-x64": []byte(script)})

	// The composition builds its installer through this seam; keep the engine it
	// hands the agent tools module, so the install below is the one a console job runs.
	var installer *toolinstall.Engine
	prev := toolInstallEngine
	toolInstallEngine = func(context.Context) *toolinstall.Engine {
		claude := toolinstall.NewClaudeWithTrust(toolinstall.ClaudeOptions{BaseURL: srv.URL, Verifier: verifier, ProbeBudget: 10 * time.Second, ProbeGrace: time.Second}, key.Public, key.Fingerprint)
		installer = toolinstall.NewEngine(toolinstall.NewCatalog(claude), toolinstall.EngineOptions{InstallerVersion: "test"})
		return installer
	}
	t.Cleanup(func() { toolInstallEngine = prev })
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
	if installer == nil {
		t.Fatal("the composition did not build its installer through toolInstallEngine")
	}

	req := toolinstall.Request{Driver: "claude", Version: release, Platform: toolinstall.Platform{OS: "linux", Arch: "amd64", Libc: "glibc"}, DestRoot: filepath.Join(dir, "tools"), Source: srv.URL}
	plan, err := installer.Plan(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	receipt, _, err := installer.Install(t.Context(), req, plan, io.Discard)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	report := receipt.Probe.Output
	if strings.Contains(report, "EngineFileRead") {
		t.Errorf("engine euid %d: the install probe could read the engine data directory", os.Geteuid())
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
	got := statusCapabilities(t, report)
	want := map[string]string{"CapInh": none, "CapPrm": none, "CapEff": none, "CapAmb": none, "CapBnd": bound, "NoNewPrivs": "1"}
	for set, value := range want {
		if got[set] != value {
			t.Errorf("engine euid %d: the install probe %s = %s, want %s", os.Geteuid(), set, got[set], value)
		}
	}
}

// A release archive's probe runs confined by the composition's builder and still
// passes: it reads the runtime its program loads from lib/<program> and writes its
// temporary directory, but not its own release nor the engine's data (#1430).
func TestReleaseArchiveProbeRunsConfinedAndReadsItsLibraries(t *testing.T) {
	if s := confine.Probe(); s.Mode != confine.ModeLandlock {
		t.Fatal("this security reproducer requires Landlock: " + s.Reason)
	}
	dir := toolinstalltest.ExecCapableDir(t)
	engineFile := filepath.Join(dir, "engine-only")
	release := filepath.Join(dir, "staged", "ollama")
	exe := filepath.Join(release, "bin", "ollama")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{engineFile: "engine-only\n", filepath.Join(release, "lib", "ollama", "libfixture.so"): "fixture runtime\n"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\n" +
		"r=${0%/bin/ollama}\n" +
		"/bin/cat \"$r/lib/ollama/libfixture.so\" >/dev/null 2>&1 && echo LibRead\n" +
		"/bin/cat '" + engineFile + "' >/dev/null 2>&1 && echo EngineFileRead\n" +
		"/usr/bin/touch \"$r/bin/planted\" 2>/dev/null && echo ReleaseWritten\n" +
		"/usr/bin/touch \"$TMPDIR/scratch\" 2>/dev/null && echo TmpWritten\n" +
		"/bin/grep -E '^(Cap|NoNewPrivs)' /proc/self/status\n" +
		"echo 'ollama version is 0.12.3'\n"
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	catalog, err := toolinstall.NewCapabilityCatalog(toolinstall.NewCatalog(), toolinstall.NewOllama(toolinstall.ReleaseArchiveOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	installer := toolinstall.NewEngineWithCapabilities(catalog, toolinstall.EngineOptions{InstallerVersion: "test"})
	installer.SetChildCommand(confinedToolCommand(dir))
	cands, err := installer.Detect(t.Context(), toolinstall.DetectOptions{Driver: toolinstall.DriverOllama, Root: filepath.Join(dir, "tools"), Home: t.TempDir(), ProbePaths: []string{exe}})
	i := slices.IndexFunc(cands, func(c toolinstall.Candidate) bool { return c.Path == exe })
	if i < 0 || cands[i].Probe == nil {
		t.Fatalf("detect = %+v, %v", cands, err)
	}
	if err != nil || cands[i].ProbeError != "" {
		t.Fatalf("engine euid %d: the confined probe failed: %v %s\n%s", os.Geteuid(), err, cands[i].ProbeError, cands[i].Probe.Output)
	}
	report := cands[i].Probe.Output
	for marker, want := range map[string]bool{"LibRead": true, "TmpWritten": true, "EngineFileRead": false, "ReleaseWritten": false} {
		if strings.Contains(report, marker) != want {
			t.Errorf("engine euid %d: %s = %v, want %v:\n%s", os.Geteuid(), marker, !want, want, report)
		}
	}
	if got := statusCapabilities(t, report); got["CapEff"] != "0000000000000000" || got["NoNewPrivs"] != "1" {
		t.Errorf("engine euid %d: the probe CapEff %s NoNewPrivs %s, want none and 1", os.Geteuid(), got["CapEff"], got["NoNewPrivs"])
	}
}

func TestServerFreeToolDetectProbeIsConfined(t *testing.T) {
	if s := confine.Probe(); s.Mode != confine.ModeLandlock {
		t.Fatal("this security reproducer requires Landlock: " + s.Reason)
	}
	dir := toolinstalltest.ExecCapableDir(t)
	dataDir := t.TempDir()
	engineFile := filepath.Join(dataDir, "engine-only")
	if err := os.WriteFile(engineFile, []byte("engine-only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "grok")
	script := "#!/bin/sh\necho 'grok 1.0.13'\nif /bin/cat '" + engineFile + "' >/dev/null 2>&1; then echo EngineFileRead; fi\n"
	if err := os.WriteFile(exe, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLIVARES_DATA_DIR", dataDir)
	cands, err := toolInstallEngine(context.Background()).Detect(context.Background(), toolinstall.DetectOptions{
		Driver: toolinstall.DriverGrok, Root: filepath.Join(dataDir, "tools"), Home: t.TempDir(), ProbePaths: []string{exe},
	})
	if err != nil || len(cands) != 1 || cands[0].Probe == nil {
		t.Fatalf("detect = %+v, %v", cands, err)
	}
	output := cands[0].Probe.Output
	if strings.Contains(output, "EngineFileRead") || !strings.Contains(output, "1.0.13") {
		t.Fatalf("server-free probe was not confined: %s", output)
	}
}
