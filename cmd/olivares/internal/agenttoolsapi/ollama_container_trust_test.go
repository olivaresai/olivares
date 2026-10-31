// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What the engine trusts about the container CLI it execs, and what it refuses to
// drive: the exit-0 noise a runtime prints on stderr is not state, a
// daemon that is not this node's own cannot serve the loopback endpoint, and a
// program is a container CLI by its exact name, not by a prefix.

// errorMessage is the message of an error answer, empty when the answer is not one, so
// a start that was wrongly accepted fails its assertion instead of panicking the run.
func errorMessage(st map[string]any) string {
	e, _ := st["error"].(map[string]any)
	message, _ := e["message"].(string)
	return message
}

// A runtime that exits 0 but warns on stderr (Docker does when its config file is
// unreadable) still answers `inspect`: the state is read from stdout only.
func TestOllamaContainerInspectIgnoresWhatTheRuntimeSaysOnStderr(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	if err := os.WriteFile(f.containerLog+".inspect-warn", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true}); code != 202 {
		t.Fatalf("start = %d %v, want 202", code, st)
	}
	waitOllama(t, call, "running")
	if code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil); code != 200 || st["state"] != "stopped" {
		t.Fatalf("stop = %d %v, want a clean stop although the runtime warns on stderr", code, st)
	}
}

// The container publishes the loopback endpoint on the daemon's host. A daemon the
// environment points elsewhere would publish it on another machine while a local
// process could answer on this one, so the container flavour refuses it, and says
// which setting to unset.
func TestOllamaContainerStartRefusesADaemonThatIsNotThisNodes(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		refused     bool
	}{
		{"DOCKER_HOST", "tcp://10.0.0.5:2375", true},
		{"DOCKER_HOST", "ssh://ops@build-host", true},
		{"CONTAINER_HOST", "tcp://build-host:8080", true},
		{"CONTAINER_CONNECTION", "staging", true},
		{"DOCKER_CONTEXT", "staging", true},
		{"DOCKER_HOST", "unix:///run/user/1000/docker.sock", false},
		{"DOCKER_CONTEXT", "default", false},
	} {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			call, f := newOllamaServer(t)
			f.cfg.Getenv = func(name string) string {
				if name == tc.name {
					return tc.value
				}
				return os.Getenv(name)
			}
			f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
			code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
			if !tc.refused {
				if code != 202 {
					t.Fatalf("start = %d %v, want 202 for a local daemon", code, st)
				}
				waitOllama(t, call, "running")
				return
			}
			message := errorMessage(st)
			if code != 409 || !strings.Contains(message, tc.name) || !strings.Contains(message, "this node's own") {
				t.Fatalf("start = %d %v, want 409 naming %s and this node's own daemon", code, st, tc.name)
			}
			if calls := containerCalls(t, f.containerLog); len(calls) != 0 {
				t.Fatalf("the runtime was called although the daemon was refused: %q", calls)
			}
		})
	}
}

// A container CLI is podman, podman-remote or docker by its file name, exactly. A
// program that merely starts with "podman" is not one, wherever the path came from.
func TestOllamaContainerRuntimeNeedsTheExactCLIName(t *testing.T) {
	for path, want := range map[string]bool{
		"/usr/bin/podman":         true,
		"/usr/bin/podman-remote":  true,
		"/usr/bin/docker":         true,
		"/x/podman-evil":          false,
		"/x/podmanager":           false,
		"/usr/bin/docker-compose": false,
		"/usr/bin/dockerd":        false,
		"/bin/sh":                 false,
		"podman":                  false, // relative: a PATH lookup, never a trusted path
	} {
		if got := trustedOllamaRuntime(path); got != want {
			t.Errorf("trustedOllamaRuntime(%q) = %v, want %v", path, got, want)
		}
	}
}

// A start record that names a program which only looks like Podman is not run at
// Stop: the engine's own data directory is the only writer, but a record is still
// input, and the exec at every boot and every Stop must not follow it anywhere.
func TestOllamaStopDoesNotRunARememberedProgramThatOnlyLooksLikePodman(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	marker := filepath.Join(t.TempDir(), "ran")
	lookalike := filepath.Join(t.TempDir(), "podman-evil")
	if err := os.WriteFile(lookalike, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	record := fmt.Sprintf(`{"container":true,"runtime":%q,"image":"docker.io/ollama/ollama:test"}`, lookalike)
	if err := os.WriteFile(f.cfg.StateFile, []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	call("POST", "/v1/m/agenttools/ollama/stop", nil)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("Stop ran the program the record named although it is not a container CLI")
	}
}

// The runtime's answer about this container is read by its words: both runtimes' real
// wording counts, and an answer about anything else does not.
func TestOllamaContainerMissingReadsOnlyAnAnswerAboutThisContainer(t *testing.T) {
	for out, want := range map[string]bool{
		`Error: no container with name or ID "olivares-ollama" found: no such container`: true,
		"Error response from daemon: No such container: olivares-ollama":                 true,
		"Error: No such object: olivares-ollama":                                         true,
		"Error: No such container: someone-else":                                         false,
		`Error: context "other" does not exist`:                                          false,
		"":                                                                               false,
	} {
		if got := ollamaContainerMissing([]byte(out)); got != want {
			t.Errorf("ollamaContainerMissing(%q) = %v, want %v", out, got, want)
		}
	}
}

// A start over a record that cannot be read, when the start itself fails, says that
// whether a container serves is unknown and keeps a record: the file is the only
// place a later Stop looks, and forgetting it would turn the doubt into silence.
func TestOllamaContainerFailedStartOverAnUnreadableRecordKeepsARecord(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	if err := os.WriteFile(f.cfg.StateFile, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.containerLog+".fail-rm", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{"container": true})
	message := errorMessage(st)
	if code != 409 || !strings.Contains(message, "whether a container serves is unknown") {
		t.Fatalf("start = %d %v, want 409 and the doubt said", code, st)
	}
	raw, err := os.ReadFile(f.cfg.StateFile)
	if err != nil || !strings.Contains(string(raw), `"container":true`) {
		t.Fatalf("the failed start left record %q (%v), want one that names the container flavour", raw, err)
	}
}

// Stop over a record it cannot read, on a node that cannot drive a container CLI, says
// so and keeps the record, so the next Stop says it again instead of reading clean.
func TestOllamaStopKeepsAnUnreadableRecordAndWarnsEveryTime(t *testing.T) {
	call, f := newOllamaServer(t)
	f.cfg.Container = nil
	f.mod.UseOllama(f.cfg)
	if err := os.WriteFile(f.cfg.StateFile, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil)
		message, _ := st["message"].(string)
		if code != 200 || !strings.Contains(message, "could not confirm") {
			t.Fatalf("stop %d = %d %v, want 200 and the warning", i, code, st)
		}
		if raw, err := os.ReadFile(f.cfg.StateFile); err != nil || string(raw) != "{not json" {
			t.Fatalf("stop %d changed the unreadable record: %q (%v)", i, raw, err)
		}
	}
}

// The process flavour shares the start record: a start that cannot be remembered is
// refused before anything runs, and a start over an unreadable earlier record says so.
func TestOllamaProcessStartHonoursTheStartRecord(t *testing.T) {
	t.Run("refused when the record cannot be kept", func(t *testing.T) {
		call, f := newOllamaServer(t)
		if err := os.Mkdir(f.cfg.StateFile, 0o700); err != nil {
			t.Fatal(err)
		}
		code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{})
		message := errorMessage(st)
		if code != 409 || !strings.Contains(message, "could not be kept") {
			t.Fatalf("start = %d %v, want 409 and the record failure", code, st)
		}
		if listening(f.addr) {
			t.Fatal("a start that cannot be remembered ran the program")
		}
	})
	t.Run("says so over an unreadable earlier record", func(t *testing.T) {
		call, f := newOllamaServer(t)
		if err := os.WriteFile(f.cfg.StateFile, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		if code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]any{}); code != 202 {
			t.Fatalf("start = %d %v, want 202", code, st)
		}
		st := waitOllama(t, call, "running")
		if msg, _ := st["message"].(string); !strings.Contains(msg, "could not be read") {
			t.Fatalf("message = %q, want the unreadable-record note", msg)
		}
	})
}

// A record from before the runtime was remembered names none. The removal still runs, but a
// success through today's CLI says nothing about the daemon that ran the container, so the
// Stop warns and the record stays for the retry.
func TestOllamaStopOfALegacyRecordWarnsAfterARemovalItCannotVouchFor(t *testing.T) {
	call, f := newOllamaServer(t)
	f.useContainer(t, OllamaContainer{Image: "docker.io/ollama/ollama:test"})
	if err := os.WriteFile(f.cfg.StateFile, []byte(`{"container":true,"image":"docker.io/ollama/ollama:test"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil)
	message, _ := st["message"].(string)
	if code != 200 || !strings.Contains(message, "may still be serving") {
		t.Fatalf("stop = %d %v, want 200 and the surviving-container warning", code, st)
	}
	if calls := containerCalls(t, f.containerLog); len(calls) != 2 || !argsHave(calls[1], "rm", "-f", "olivares-ollama") {
		t.Fatalf("calls = %q, want the removal tried once after the ownership check", calls)
	}
	if _, err := os.Stat(f.cfg.StateFile); err != nil {
		t.Fatalf("the unproven removal forgot the record: %v", err)
	}
}
