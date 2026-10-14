// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestInventoryReportsSessionToolPresence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kind    string
		present bool
	}{
		{"executable", "executable", true},
		{"bare name on PATH", "path", true},
		{"denied to effective user", "denied", false},
		{"denied bare name on PATH", "denied_path", false},
		{"no resolver", "nil", false},
		{"unresolved", "empty", false},
		{"missing", "missing", false},
		{"not executable", "file", false},
		{"directory", "directory", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call, _ := newSignInServer(t, func(m *Module, bin string) {
				program := filepath.Join(bin, "gemini")
				switch tc.kind {
				case "nil":
					m.SetProgramResolver(nil)
					return
				case "empty":
					program = ""
				case "executable", "file", "path", "denied", "denied_path":
					mode := os.FileMode(0o644)
					if tc.present || tc.kind == "denied" || tc.kind == "denied_path" {
						mode = 0o755
					}
					// Listing must not run the program; execution leaves a sentinel.
					if err := os.WriteFile(program, []byte("#!/bin/sh\nprintf ran > \"$0.ran\"\nexit 99\n"), mode); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						if _, err := os.Stat(filepath.Join(bin, "gemini.ran")); !errors.Is(err, fs.ErrNotExist) {
							t.Fatalf("inventory executed the program: sentinel stat = %v", err)
						}
					})
					if tc.kind == "denied" || tc.kind == "denied_path" {
						if err := os.Chmod(program, 0o401); err != nil {
							t.Fatal(err)
						}
						if _, err := os.Stat(program); err != nil {
							t.Fatalf("permission fixture must remain statable: %v", err)
						}
						if _, err := exec.LookPath(program); !errors.Is(err, fs.ErrPermission) {
							t.Skipf("effective permission denial NOT exercised: euid=%d lookup=%v", os.Geteuid(), err)
						}
					}
					if tc.kind == "path" || tc.kind == "denied_path" {
						t.Setenv("PATH", bin)
						program = "gemini"
					}
				case "directory":
					if err := os.Mkdir(program, 0o755); err != nil {
						t.Fatal(err)
					}
				}
				m.SetProgramResolver(func(driver string) string {
					if driver == "gemini-cli" {
						return program
					}
					return ""
				})
			})
			status, body := call("GET", "/v1/m/agenttools/inventory", nil)
			if status != 200 {
				t.Fatalf("inventory status %d: %v", status, body)
			}
			presence, ok := body["presence"].(map[string]any)
			if !ok {
				t.Fatalf("inventory has no session-tool presence: %v", body)
			}
			row, ok := presence["gemini-cli"].(map[string]any)
			if !ok || row["program"] != "gemini" || row["present"] != tc.present {
				t.Fatalf("Gemini presence %v; want program=gemini present=%v", row, tc.present)
			}
			for _, driver := range []string{"claude", "codex", "grok", "opencode", "ollama"} {
				if _, ok := presence[driver]; ok {
					t.Fatalf("installer tool %s duplicated in presence", driver)
				}
			}
		})
	}
}

// "Installed here" lists the program each tool's new session runs: the launch
// resolver's answer when it names an executable, never a run of the tool.
func TestInventoryReportsEachLaunchProgram(t *testing.T) {
	var claude string
	call, _ := newSignInServer(t, func(m *Module, bin string) {
		claude = filepath.Join(bin, "claude")
		if err := os.WriteFile(claude, []byte("#!/bin/sh\nprintf ran > \"$0.ran\"\nexit 99\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		codex := filepath.Join(bin, "codex-not-executable")
		if err := os.WriteFile(codex, []byte("not executable"), 0o644); err != nil {
			t.Fatal(err)
		}
		m.SetProgramResolver(func(driver string) string {
			switch driver {
			case "claude":
				return claude
			case "codex":
				return codex
			}
			return ""
		})
	})
	status, body := call("GET", "/v1/m/agenttools/inventory", nil)
	if status != 200 {
		t.Fatalf("inventory status %d: %v", status, body)
	}
	programs, ok := body["launch_programs"].(map[string]any)
	if !ok || len(programs) != 1 || programs["claude"] != claude {
		t.Fatalf("launch programs %v; want only claude=%s", body["launch_programs"], claude)
	}
	if _, err := os.Stat(claude + ".ran"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("inventory executed the program: sentinel stat = %v", err)
	}
}
