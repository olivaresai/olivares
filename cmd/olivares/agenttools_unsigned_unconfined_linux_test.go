// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall/toolinstalltest"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// Deny the Landlock probe in a subprocess, on every thread and future child
// (as sessionconfinement_unavailable_linux_test.go does), and install through
// the real composition on that node: the first run of an unsigned install's
// bytes — the install's --version probe — is refused with one sentence naming
// Landlock, while a signed install probes as before (#3312).
func TestUnsignedInstallFirstRunRefusedWithoutLandlock(t *testing.T) {
	if os.Getenv("OLIVARES_TEST_NO_LANDLOCK") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestUnsignedInstallFirstRunRefusedWithoutLandlock$", "-test.v")
		cmd.Env = append(os.Environ(), "OLIVARES_TEST_NO_LANDLOCK=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("no-Landlock subprocess: %v\n%s", err, out)
		}
		t.Log(string(out))
		return
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	filter := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: unix.SYS_LANDLOCK_CREATE_RULESET, Jf: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS)},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	prog := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if _, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&prog))); errno != 0 {
		t.Fatal(errno)
	}
	if state := confine.Probe(); state.Mode != confine.ModeNone {
		t.Fatalf("probe = %+v", state)
	}
	toolinstalltest.RequireGPG(t)

	// A Grok Build origin that publishes a version and its unverified binary.
	const grokVersion = "1.0.13"
	dir := toolinstalltest.ExecCapableDir(t)
	grokMarker := filepath.Join(dir, "grok-ran.marker")
	grokMux := http.NewServeMux()
	grokOrigin := httptest.NewTLSServer(grokMux)
	t.Cleanup(grokOrigin.Close)
	grokMux.HandleFunc("/latest", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, grokVersion+"\n")
	})
	grokMux.HandleFunc("/grok-"+grokVersion+"-linux-x86_64", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(toolinstalltest.VersionReporter("Grok", grokVersion, grokMarker))
	})

	// A signed Claude release, as the confined-probe test publishes it.
	verifier, err := toolinstall.NewGPGVerifier(context.Background(), exec.LookPath)
	if err != nil {
		t.Fatal(err)
	}
	key := toolinstalltest.GenerateKey(t, "Olivares Unsigned Fixture <unsigned-fixture@olivares.invalid>")
	srv := toolinstalltest.NewServer(t)
	const release = "2.1.261"
	claudeMarker := filepath.Join(dir, "claude-ran.marker")
	srv.Publish(t, key, release, map[string][]byte{"linux-x64": toolinstalltest.Executable(release, claudeMarker)})

	var installer *toolinstall.Engine
	prev := toolInstallEngine
	toolInstallEngine = func(context.Context) *toolinstall.Engine {
		grok := toolinstall.NewGrok(toolinstall.GrokOptions{BaseURL: grokOrigin.URL, Client: grokOrigin.Client(), ProbeBudget: 10 * time.Second, ProbeGrace: time.Second})
		claude := toolinstall.NewClaudeWithTrust(toolinstall.ClaudeOptions{BaseURL: srv.URL, Verifier: verifier, ProbeBudget: 10 * time.Second, ProbeGrace: time.Second}, key.Public, key.Fingerprint)
		cat, cerr := toolinstall.NewCapabilityCatalog(toolinstall.NewCatalog(claude), grok)
		if cerr != nil {
			t.Fatal(cerr)
		}
		installer = toolinstall.NewEngineWithCapabilities(cat, toolinstall.EngineOptions{InstallerVersion: "test"})
		return installer
	}
	t.Cleanup(func() { toolInstallEngine = prev })
	home := t.TempDir()
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

	// The unsigned install is refused before its staged bytes ever run.
	req := toolinstall.RequestV2{Driver: "grok", Version: toolinstall.ChannelLatest, Platform: toolinstall.PlatformV2{OS: "linux", Arch: "amd64"}, DestRoot: filepath.Join(dir, "tools"), Source: grokOrigin.URL}
	_, _, err = installer.InstallV2(t.Context(), req, nil, nil)
	if toolinstall.KindOf(err) != toolinstall.KindVerificationUnavailable {
		t.Fatalf("grok install = %v, want a %s refusal", err, toolinstall.KindVerificationUnavailable)
	}
	for _, want := range []string{"grok", "publisher signature", "Landlock", "olivares doctor"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("grok refusal %q lacks %q", err, want)
		}
	}
	if _, merr := os.Stat(grokMarker); !os.IsNotExist(merr) {
		t.Errorf("the unverified grok bytes ran: marker stat = %v", merr)
	}
	if inv, lerr := installer.List(t.Context(), filepath.Join(dir, "tools")); lerr != nil || len(inv.Installed) != 0 {
		t.Errorf("the refused install left releases behind: %+v %v", inv.Installed, lerr)
	}

	// The signed install probes as before on the same node.
	claudeReq := toolinstall.Request{Driver: "claude", Version: release, Platform: toolinstall.Platform{OS: "linux", Arch: "amd64", Libc: "glibc"}, DestRoot: filepath.Join(dir, "tools"), Source: srv.URL}
	plan, err := installer.Plan(t.Context(), claudeReq)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := installer.Install(t.Context(), claudeReq, plan, io.Discard); err != nil {
		t.Fatalf("a signed install must probe as before without Landlock: %v", err)
	}
	if b, rerr := os.ReadFile(claudeMarker); rerr != nil || strings.Count(string(b), "ran\n") != 1 {
		t.Errorf("the signed probe ran %s times, want 1 (%v)", string(b), rerr)
	}
}
