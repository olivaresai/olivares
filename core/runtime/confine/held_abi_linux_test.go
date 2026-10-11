// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package confine

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestHeldReadOnlyGrantsRequireTruncationProtection(t *testing.T) {
	file, err := OpenDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	query := landlockABI
	t.Cleanup(func() { landlockABI = query })
	for _, abi := range []int{1, 2, 3} {
		t.Run(strconv.Itoa(abi), func(t *testing.T) {
			landlockABI = func() (int, error) { return abi, nil }
			cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", "printf tool-ran")
			_, err := Wrap(cmd, Policy{SealedFiles: []*os.File{file}})
			if abi < 3 {
				if err == nil || !strings.Contains(err.Error(), "cannot block truncation") || !strings.Contains(err.Error(), "Landlock ABI 3") {
					t.Fatalf("ABI %d needs an actionable pre-spawn refusal, got %v", abi, err)
				}
				if IsWrapped(cmd) || len(cmd.ExtraFiles) != 0 {
					t.Fatal("a refused grant changed the command or attached handles")
				}
			} else if err != nil || !IsWrapped(cmd) {
				t.Fatalf("ABI 3 held grant refused: %v", err)
			}
			// Published commands without the new grants retain their ABI 1/2 behavior.
			plain := exec.CommandContext(t.Context(), "/bin/sh", "-c", "printf tool-ran")
			if _, err := Wrap(plain, Policy{}); err != nil || !IsWrapped(plain) {
				t.Fatalf("ABI %d changed no-handle wrapping: %v", abi, err)
			}
		})
	}
}

func TestHeldReadOnlyHelperRequiresTruncationProtection(t *testing.T) {
	if state := Probe(); state.Mode != ModeLandlock || state.ABI < 3 {
		t.Fatal("this named acceptance check needs a kernel with Landlock ABI 3 or later")
	}
	file, err := OpenDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	for _, abi := range []int{1, 2, 3} {
		t.Run(strconv.Itoa(abi), func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", "printf tool-ran")
			if _, err := Wrap(cmd, Policy{ReadOnly: []string{"/bin", "/lib", "/lib64"}, SealedFiles: []*os.File{file}}); err != nil {
				t.Fatal(err)
			}
			// The helper receives the real typed payload/FD transport with a synthetic ABI.
			cmd.Args = append([]string{cmd.Path, "__owned_held_abi_helper", strconv.Itoa(abi)}, cmd.Args[2:]...)
			out, err := cmd.CombinedOutput()
			if abi < 3 {
				exit, ok := err.(*exec.ExitError)
				if !ok || exit.ExitCode() != 126 || strings.Contains(string(out), "tool-ran") || !strings.Contains(string(out), "cannot block truncation") {
					t.Fatalf("ABI %d helper executed the tool or missed the refusal: %v: %s", abi, err, out)
				}
			} else if err != nil || string(out) != "tool-ran" {
				t.Fatalf("ABI 3 helper did not start the tool: %v: %s", err, out)
			}
			if err := ValidateDirectoryHandle(file); err != nil {
				t.Fatalf("the helper closed the parent's borrowed handle: %v", err)
			}
		})
	}
}
