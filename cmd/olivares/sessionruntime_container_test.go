// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/sessions/cliruntime"
)

// isolation=container is available exactly when the node has a container runtime, and a
// node without one is exactly what it was: the native runner, refusing container.
func TestWithContainerIsolation(t *testing.T) {
	podman := filepath.Join(t.TempDir(), "podman")
	if err := os.WriteFile(podman, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	found := func(name string) (string, error) {
		if name == "podman" {
			return podman, nil
		}
		return "", errors.New("absent")
	}
	absent := func(string) (string, error) { return "", errors.New("absent") }
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }

	// container reports what the composed runner says about a container launch.
	container := func(r sessions.Runner) sessions.RunnerIsolationState {
		inspector, ok := r.(sessions.RunnerInspector)
		if !ok {
			t.Fatalf("%T cannot be inspected", r)
		}
		obs, err := inspector.InspectLaunch(context.Background(), sessions.RunnerInspection{Program: "/bin/sh", Isolation: sessions.IsolationContainer})
		if err != nil {
			t.Fatal(err)
		}
		return obs.Isolation
	}

	official, err := sessions.NewOfficialRunner(cliruntime.Kinds()...)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		native   sessions.Runner
		getenv   func(string) string
		lookPath func(string) (string, error)
		want     sessions.RunnerIsolationState
		logged   string
	}{
		{"the runner the official CLIs declare gets containers where podman is installed", official, env(nil), found, sessions.RunnerIsolationSupported, "isolation=container is available"},
		{"no runtime installed changes nothing", official, env(nil), absent, sessions.RunnerIsolationUnsupported, ""},
		{"turned off changes nothing", official, env(map[string]string{sessions.ContainerRuntimeEnv: "off"}), found, sessions.RunnerIsolationUnsupported, ""},
		{"a wrong setting changes nothing and says so", official, env(map[string]string{sessions.ContainerRuntimeEnv: "lxc"}), found, sessions.RunnerIsolationUnsupported, "OLIVARES_SESSION_CONTAINER_RUNTIME"},
		{"a runtime named but missing changes nothing and says so", official, env(map[string]string{sessions.ContainerRuntimeEnv: "docker"}), found, sessions.RunnerIsolationUnsupported, "not installed"},
		{"a terminal-transport runner is left alone", sessions.NewPTYRunner(), env(nil), found, sessions.RunnerIsolationUnsupported, "stdio transport"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var logs bytes.Buffer
			got := withContainerIsolation(c.native, c.getenv, c.lookPath, slog.New(slog.NewTextHandler(&logs, nil)))
			if state := container(got); state != c.want {
				t.Fatalf("container isolation = %q, want %q", state, c.want)
			}
			if c.want == sessions.RunnerIsolationUnsupported && got != c.native {
				t.Fatalf("the native runner was replaced although containers are not available")
			}
			if c.logged != "" && !strings.Contains(logs.String(), c.logged) {
				t.Fatalf("log = %q, want it to say %q", logs.String(), c.logged)
			}
		})
	}
}

// A container the runner cannot remove once its session ended is reported in the engine's
// log, because nobody else is left to hear of it: the wiring sets what the runner reports with.
func TestWithContainerIsolationReportsAContainerItCouldNotRemove(t *testing.T) {
	dir := t.TempDir()
	podman := filepath.Join(dir, "podman")
	script := "#!/bin/sh\ncase \"$1\" in\n  rm) echo 'daemon unreachable' >&2; exit 1 ;;\n  run) printf 'ready\\n'; cat >/dev/null ;;\nesac\n"
	if err := os.WriteFile(podman, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(dir, "claude")
	if err := os.WriteFile(program, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	runner := withContainerIsolation(sessions.NewProcRunner(),
		func(string) string { return "" },
		func(name string) (string, error) {
			if name == "podman" {
				return podman, nil
			}
			return "", errors.New("absent")
		}, slog.New(slog.NewTextHandler(&logs, nil)))
	proc, err := runner.Launch(context.Background(), sessions.LaunchSpec{
		Program: program, Dir: "/workspace", Isolation: sessions.IsolationContainer, WaitDelay: time.Second,
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := proc.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := logs.String(); !strings.Contains(got, "could not be removed") || !strings.Contains(got, "olivares-session-") {
		t.Fatalf("log = %q, want the container named as one that could not be removed", got)
	}
}
