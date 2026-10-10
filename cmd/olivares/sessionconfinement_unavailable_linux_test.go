// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/sessions/confine"
	"golang.org/x/sys/unix"
)

// Deny the Landlock probe in a subprocess, on every thread and future child.
// This exercises the actual unavailable-kernel path without a production bypass.
func TestSessionLaunchWithoutLandlockCannotReadEngineData(t *testing.T) {
	if os.Getenv("OLIVARES_TEST_NO_LANDLOCK") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSessionLaunchWithoutLandlockCannotReadEngineData$", "-test.v")
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

	h := newHarness(t)
	data := t.TempDir()
	key := filepath.Join(data, "secret-store.key")
	if err := os.WriteFile(key, []byte("synthetic-engine-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := h.set.sessions
	sessionConfinementOption(data, discardLog())(m)
	runner := &engineDataReadRunner{key: key}
	sessions.WithRunner(runner)(m)
	m.UseExecutionEnvironmentRef("confinement-test")
	var profile struct {
		Ref string `json:"profile_ref"`
	}
	if code := h.reqInto(http.MethodPost, "/v1/m/sessions/provider-profiles", h.adminToken, h.tenantA, map[string]any{"driver": "claude", "auth_source": "provider_account_home", "config_home": t.TempDir(), "user_home": t.TempDir()}, &profile); code != http.StatusCreated {
		t.Fatalf("profile=%d", code)
	}
	for _, mode := range []string{"default", "acceptEdits", "plan"} {
		t.Run(mode, func(t *testing.T) {
			runner.called, runner.spawned, runner.output, runner.err = false, false, "", nil
			code, body := h.req(http.MethodPost, "/v1/m/sessions/runs", h.adminToken, h.tenantA, map[string]any{"transport": "stream-json", "permission_mode": mode, "isolation": "native", "provider_profile_ref": profile.Ref})
			if !runner.called {
				t.Fatalf("launch did not reach the native runner: %d %s", code, body)
			}
			if code != http.StatusBadGateway || !strings.Contains(string(body), "Landlock") || !strings.Contains(string(body), "olivares doctor") || strings.Contains(string(body), "synthetic-engine-secret") {
				t.Errorf("launch refusal is not actionable and secret-free: %d %s", code, body)
			}
			if runner.spawned || runner.output != "" || runner.err == nil || !strings.Contains(runner.err.Error(), "cannot confine") {
				t.Fatalf("session must refuse before spawn: spawned=%v output=%q error=%v", runner.spawned, runner.output, runner.err)
			}
		})
	}
	t.Run("MCP", func(t *testing.T) {
		management := &mcpManagement{eng: &engine{sessionsMod: m}}
		spec := sessions.LaunchSpec{Program: "/bin/cat", Dir: t.TempDir()}
		if err := management.confineLocalServer(&spec); err != nil {
			t.Fatal(err)
		}
		runner.called, runner.spawned, runner.output, runner.err = false, false, "", nil
		_, _ = runner.Launch(t.Context(), spec)
		if runner.spawned || runner.err == nil || !strings.Contains(runner.err.Error(), "cannot confine") {
			t.Fatalf("MCP child escaped confinement: spawned=%v output=%q error=%v", runner.spawned, runner.output, runner.err)
		}
	})
	t.Run("doctor", func(t *testing.T) {
		options, deps := doctorFixture(t)
		deps.confinement = confine.Probe
		report, _, err := runDoctor(t.Context(), options, deps)
		if err != nil {
			t.Fatal(err)
		}
		check := doctorCheckByName(report, "session-confinement")
		if check.Status != "fail" || !strings.Contains(check.Remediation, "Landlock") {
			t.Fatalf("doctor does not explain refused sessions: %+v", check)
		}
	})
}

// Keep the real runtime's launch policy, but use a labelled local program that
// attempts exactly the forbidden read instead of contacting an AI provider.
type engineDataReadRunner struct {
	key, output     string
	called, spawned bool
	err             error
}

func (r *engineDataReadRunner) Launch(ctx context.Context, spec sessions.LaunchSpec) (sessions.Process, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	r.called = true
	spec.Program, spec.Args = "/bin/cat", []string{r.key}
	proc, err := sessions.NewProcRunner().Launch(ctx, spec)
	r.err = err
	if err != nil {
		return nil, err
	}
	r.spawned = true
	for frame := range proc.Output() {
		r.output += string(frame.Data)
	}
	_, _ = proc.Wait()
	return nil, errors.New("local read probe completed")
}
