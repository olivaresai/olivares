// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package confine

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootConfinedPathHelper(t *testing.T) {
	if os.Getenv("OLIVARES_TEST_ROOT_CONFINE") == "1" {
		os.Exit(RunHelper(os.Args[3:]))
	}
}

func TestRootConfinedPathsExplainMissingTraversal(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to create another uid's private home")
	}
	if Probe().Mode != ModeLandlock {
		t.Fatal(Probe().Reason)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"program", "symlink-program", "symlink-grant", "symlink-device", "read-write", "read-only", "sealed", "sealed-handle", "device", "root-owned", "group-search"} {
		t.Run(kind, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "home")
			data := filepath.Join(home, "data")
			if err := os.MkdirAll(data, 0700); err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile("/bin/true")
			if err != nil {
				t.Fatal(err)
			}
			program := filepath.Join(data, "true")
			if err := os.WriteFile(program, content, 0755); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(kind, "symlink-") {
				if err := os.Remove(program); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/bin/true", program); err != nil {
					t.Fatal(err)
				}
			}
			allowed := kind == "root-owned" || kind == "group-search"
			if kind != "root-owned" {
				if err := os.Chown(home, 12345, os.Getegid()); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "group-search" {
				if err := os.Chmod(home, 0710); err != nil {
					t.Fatal(err)
				}
			}
			p := Policy{ReadOnly: []string{"/usr", "/lib", "/lib64", "/bin", "/etc/ld.so.cache"}}
			target := "/bin/true"
			switch kind {
			case "program", "root-owned", "group-search":
				target = program
				p.ReadOnly = append(p.ReadOnly, data)
			case "symlink-program":
				target = program
			case "symlink-grant":
				p.ReadOnly = append(p.ReadOnly, program)
			case "symlink-device":
				p.Devices = []Device{{Path: program}}
			case "read-write":
				p.ReadWrite = []string{data}
			case "read-only":
				p.ReadOnly = append(p.ReadOnly, data)
			case "sealed":
				p.Sealed = []string{data}
			case "sealed-handle":
				file, err := OpenDirectory(data)
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				p.SealedFiles = []*os.File{file}
			case "device":
				p.Devices = []Device{{Path: program}}
			}
			req := request{Policy: p, Program: target}
			for i, file := range p.SealedFiles {
				req.Handles = append(req.Handles, directoryHandle{FD: 3 + i, Path: file.Name()})
			}
			payload, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), self, "-test.run=^TestRootConfinedPathHelper$", "--", policyArg, string(payload), "--", target)
			cmd.Env = append(os.Environ(), "OLIVARES_TEST_ROOT_CONFINE=1")
			cmd.ExtraFiles = p.SealedFiles
			output, err := cmd.CombinedOutput()
			if allowed {
				if err != nil {
					t.Fatalf("accessible root child refused: %v: %s", err, output)
				}
				return
			}
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 126 {
				t.Fatalf("inaccessible root child was not refused: %v: %s", err, output)
			}
			for _, sentence := range []string{data, "cannot be traversed without DAC override", "root-owned data directory", "/var/lib/olivares"} {
				if !strings.Contains(string(output), sentence) {
					t.Errorf("refusal lacks %q: %s", sentence, output)
				}
			}
		})
	}
}
