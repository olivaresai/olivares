// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package confine

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 3 && os.Args[1] == "__owned_held_abi_helper" {
		abi, err := strconv.Atoi(os.Args[2])
		if err != nil {
			panic(err)
		}
		landlockABI = func() (int, error) { return abi, nil }
		os.Exit(RunHelper(os.Args[3:]))
	}
	if len(os.Args) > 1 && os.Args[1] == HelperArg {
		os.Exit(RunHelper(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "__owned_limits_probe" {
		os.Exit(limitsProbe())
	}
	os.Exit(m.Run())
}

type limitResult struct {
	Core, File                           uint64
	Nice                                 int
	DeniedFile, DeniedDevice, NoNewPrivs bool
}

func limitsProbe() int {
	var core, file unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_CORE, &core); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := unix.Getrlimit(unix.RLIMIT_FSIZE, &file); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	priority, err := unix.Getpriority(unix.PRIO_PROCESS, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_, readErr := os.ReadFile(os.Args[2])
	f, deviceErr := os.Open("/dev/null")
	if f != nil {
		_ = f.Close()
	}
	nnp, err := unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_ = json.NewEncoder(os.Stdout).Encode(limitResult{Core: core.Cur, File: file.Cur, Nice: 20 - priority, DeniedFile: os.IsPermission(readErr), DeniedDevice: os.IsPermission(deviceErr), NoNewPrivs: nnp == 1})
	return 0
}

func TestSharedHelperHasNoImplicitGrantsOrLimits(t *testing.T) {
	if Probe().Mode != ModeLandlock {
		t.Skip(Probe().Reason)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	secret := t.TempDir() + "/synthetic-secret"
	if err := os.WriteFile(secret, []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	var core, file unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_CORE, &core); err != nil {
		t.Fatal(err)
	}
	if err := unix.Getrlimit(unix.RLIMIT_FSIZE, &file); err != nil {
		t.Fatal(err)
	}
	priority, err := unix.Getpriority(unix.PRIO_PROCESS, 0)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), self, "__owned_limits_probe", secret)
	// Race builds need the ELF interpreter and libc; the fixture grants them explicitly.
	if _, err := Wrap(cmd, Policy{ReadOnly: []string{self, "/lib", "/lib64"}}); err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("shared child: %v: %s", err, out)
	}
	var got limitResult
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("child output %q: %v", out, err)
	}
	if got.Core != core.Cur || got.File != file.Cur || got.Nice != 20-priority {
		t.Errorf("shared helper imposed limits: %+v; parent core=%d file=%d nice=%d", got, core.Cur, file.Cur, 20-priority)
	}
	if !got.DeniedFile {
		t.Error("shared helper implicitly granted the synthetic secret")
	}
	if !got.DeniedDevice {
		t.Error("shared helper implicitly granted /dev/null")
	}
	if !got.NoNewPrivs {
		t.Error("shared helper did not set no_new_privs")
	}
}

func TestSharedHelperKeepsAValidatedDirectoryReadOnlyAndClosesItsHandle(t *testing.T) {
	if Probe().Mode != ModeLandlock || Probe().ABI < 3 {
		t.Fatal("this named kernel check needs Landlock with truncation protection")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture")
	if err := os.WriteFile(path, []byte("synthetic-shared-handle"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := OpenDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", `
/bin/cat "$1" || exit 6
if ( : <&3 ) 2>/dev/null; then echo inherited-directory-handle; exit 7; fi
if ( printf changed > "$1" ) 2>/dev/null; then echo reopened-write; exit 8; fi
`, "sh", path)
	if _, err := Wrap(cmd, Policy{ReadOnly: []string{"/bin", "/lib", "/lib64"}, SealedFiles: []*os.File{file}, Devices: []Device{{Path: "/dev/null", Write: true}}}); err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != "synthetic-shared-handle" {
		t.Fatalf("shared held-directory read/deny/close control: %v: %s", err, out)
	}
	if value, err := os.ReadFile(path); err != nil || strings.TrimSpace(string(value)) != "synthetic-shared-handle" {
		t.Fatal("the sealed file changed")
	}
}
