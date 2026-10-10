// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package confine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The compatibility default keeps Landlock's available protections and states
// the missing one; opting into stricter protection is a separate policy choice.
func TestReadOnlyDefaultContinuesAndStatesTheTruncationLimit(t *testing.T) {
	requireLandlock(t)
	realABI := Probe().ABI
	query := probe
	t.Cleanup(func() { probe = query })
	for _, abi := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(abi), func(t *testing.T) {
			if abi > realABI {
				t.Skip("host cannot enforce this ABI-equivalent ruleset")
			}
			probe = func() State { return State{Mode: ModeLandlock, ABI: abi} }
			f := newFixture(t)
			p := Policy{ReadWrite: []string{f.data}, ReadOnly: []string{f.folder}, Sealed: []string{f.folder}}
			cmd, state, err := Command(t.Context(), p, "/bin/sh", "-c", "exit 0")
			if err != nil || cmd == nil {
				t.Fatalf("default sealed command on ABI %d refused: %v", abi, err)
			}
			want := ""
			if abi < 3 {
				want = "this kernel cannot block truncation of existing files"
			}
			if state.Reason != want || (want != "" && !strings.Contains(state.String(), want)) {
				t.Fatalf("default ABI %d state = %+v (%s), want limit %q", abi, state, state.String(), want)
			}
			marker := filepath.Join(f.data, "default-started")
			out, err := runABIHelper(t, abi, f, "default_sealed", "/bin/sh", "-c", `printf started > "$1"`, "sh", marker)
			if b, readErr := os.ReadFile(marker); err != nil || readErr != nil || string(b) != "started" {
				t.Fatalf("default ABI %d did not start: run=%v read=%v output=%s", abi, err, readErr, out)
			}
		})
	}
}

func TestReadOnlyPresetRefusesAnABIWithoutTruncateProtection(t *testing.T) {
	requireLandlock(t)
	query := probe
	t.Cleanup(func() { probe = query })
	for _, abi := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(abi), func(t *testing.T) {
			probe = func() State { return State{Mode: ModeLandlock, ABI: abi} }
			f := newFixture(t)
			p := Policy{ReadWrite: []string{f.data}, Sealed: []string{f.folder}}
			cmd, _, err := CommandWithTruncateProtection(context.Background(), p, true, "/bin/sh", "-c", "exit 0")
			if abi < 3 {
				if err == nil || cmd != nil || !strings.Contains(err.Error(), "ABI 3") {
					t.Fatalf("ABI %d admitted a read-only command: command=%v error=%v", abi, cmd != nil, err)
				}
			} else if err != nil || cmd == nil {
				t.Fatalf("ABI 3 refused a read-only command: %v", err)
			}
			p.Sealed = nil
			if cmd, _, err := CommandWithTruncateProtection(context.Background(), p, true, "/bin/sh", "-c", "exit 0"); err != nil || cmd == nil {
				t.Fatalf("ABI %d refused an ordinary writable command: %v", abi, err)
			}
		})
	}
}

// A helper re-probes the kernel rather than trusting the parent's decision.
// The override changes only the queried ABI in this isolated test process;
// The native low-ABI truncation controls live with the shared core installer.
func TestReadOnlyHelperRefusesAnABIWithoutTruncateProtection(t *testing.T) {
	requireLandlock(t)
	realABI := Probe().ABI
	for _, abi := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(abi), func(t *testing.T) {
			if abi > realABI {
				t.Skip("host cannot enforce this ABI-equivalent ruleset")
			}
			f := newFixture(t)
			marker := filepath.Join(f.data, "started")
			out, err := runABIHelper(t, abi, f, "sealed", "/bin/sh", "-c", `printf started > "$1"`, "sh", marker)
			if abi < 3 {
				var exit *exec.ExitError
				if err == nil || !strings.Contains(out, "ABI 3") {
					t.Fatalf("ABI %d helper admitted sealed policy: error=%v output=%s", abi, err, out)
				}
				exit, _ = err.(*exec.ExitError)
				if exit == nil || exit.ExitCode() != 126 {
					t.Fatalf("helper refusal exit = %v, want 126", err)
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("refused helper started the program: %v", err)
				}
			} else if err != nil {
				t.Fatalf("ABI 3 helper refused sealed policy: %v\n%s", err, out)
			}
			out, err = runABIHelper(t, abi, f, "writable", "/bin/sh", "-c", `printf started > "$1"`, "sh", marker)
			if b, readErr := os.ReadFile(marker); err != nil || readErr != nil || string(b) != "started" {
				t.Fatalf("ABI %d writable control: run=%v read=%v output=%s", abi, err, readErr, out)
			}
		})
	}
}

func runABIHelper(t *testing.T, abi int, f fixture, mode string, argv ...string) (string, error) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, append([]string{"-test.run=^TestReadOnlyABIHelperProcess$", "--"}, argv...)...)
	cmd.Env = append(os.Environ(), "OLV_TEST_ABI="+strconv.Itoa(abi), "OLV_TEST_FOLDER="+f.folder,
		"OLV_TEST_WRITABLE="+f.data, "OLV_TEST_POLICY="+mode)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestReadOnlyABIHelperProcess(t *testing.T) {
	value := os.Getenv("OLV_TEST_ABI")
	if value == "" {
		return
	}
	abi, err := strconv.Atoi(value)
	if err != nil {
		t.Fatal(err)
	}
	probe = func() State { return State{Mode: ModeLandlock, ABI: abi} }
	p := Policy{ReadWrite: []string{os.Getenv("OLV_TEST_WRITABLE")}, ReadOnly: []string{os.Getenv("OLV_TEST_FOLDER")}}
	mode := os.Getenv("OLV_TEST_POLICY")
	if mode == "sealed" || mode == "default_sealed" {
		p.Sealed = p.ReadOnly
	}
	for i, arg := range os.Args {
		if arg == "--" {
			encoded := p.encode()
			if mode == "sealed" {
				encoded = append([]string{"--require-truncate-protection"}, encoded...)
			}
			os.Exit(RunHelper(append(encoded, os.Args[i:]...)))
		}
	}
	t.Fatal("missing helper program")
}
