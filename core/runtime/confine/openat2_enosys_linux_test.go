// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package confine

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// systemd's RestrictSUIDSGID=, which the engine's unit sets, answers every openat2 with ENOSYS so that programs use
// openat, whose flags it can filter (src/shared/seccomp-util.c, seccomp_restrict_sxid). Measured on the Server image:
// every install probe failed with "open confined path ... without symlinks: function not implemented".
const openat2ENOSYSEnv = "OLIVARES_TEST_OPENAT2_ENOSYS"

func TestConfinesWhenOpenat2AnswersENOSYS(t *testing.T) {
	if os.Getenv(openat2ENOSYSEnv) == "1" {
		confineUnderOpenat2ENOSYS(t)
		return
	}
	if state := Probe(); state.Mode != ModeLandlock {
		t.Fatal("this regression needs Landlock: " + state.Reason)
	}
	// The filter cannot be removed once set, so it goes on a child test process; the helper inherits it across exec.
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestConfinesWhenOpenat2AnswersENOSYS$", "-test.v")
	cmd.Env = append(os.Environ(), openat2ENOSYSEnv+"=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("confinement under systemd's openat2 rule: %v\n%s", err, out)
	}
}

func confineUnderOpenat2ENOSYS(t *testing.T) {
	answerOpenat2WithENOSYS(t)
	if _, err := unix.Openat2(unix.AT_FDCWD, "/", &unix.OpenHow{Flags: unix.O_PATH | unix.O_CLOEXEC}); !errors.Is(err, unix.ENOSYS) {
		t.Fatalf("the filter is not in force: openat2 returned %v", err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "dir")
	visible := filepath.Join(dir, "visible")
	secret := filepath.Join(root, "protected-canary")
	link := filepath.Join(root, "link")
	loop := filepath.Join(t.TempDir(), "loop") // outside root: a carve of root refuses a loop it finds, as it must
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]string{visible: "visible-fixture", secret: "protected-fixture"} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for target, name := range map[string]string{"dir": link, loop: loop} {
		if err := os.Symlink(target, name); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("a held directory opens, an empty or missing path is absent, and a symlink anywhere in its path is refused", func(t *testing.T) {
		held, err := OpenDirectory(dir)
		if err != nil {
			t.Fatalf("a plain directory: %v", err)
		}
		_ = held.Close()
		for _, path := range []string{"", filepath.Join(root, "missing")} {
			file, err := OpenDirectory(path)
			if file != nil {
				_ = file.Close()
			}
			if !errors.Is(err, unix.ENOENT) {
				t.Fatalf("%q: got %v, want ENOENT as openat2 gives", path, err)
			}
		}
		for _, path := range []string{link, filepath.Join(link, "sub")} {
			file, err := OpenDirectory(path)
			if file != nil {
				_ = file.Close()
			}
			if !errors.Is(err, unix.ELOOP) {
				t.Fatalf("%s: got %v, want ELOOP as RESOLVE_NO_SYMLINKS gives", path, err)
			}
		}
	})

	t.Run("the helper grants a folder and still refuses a protected file", func(t *testing.T) {
		cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", `
/bin/cat "$1" || exit 2
if /bin/cat "$2" >/dev/null 2>&1; then echo protected-leak; exit 3; fi
printf '\nchild-ran'
`, "sh", visible, secret)
		policy := Policy{ReadOnly: []string{"/bin", "/lib", "/lib64", "/usr", root}, Protect: []string{secret}, Devices: []Device{{Path: "/dev/null", Write: true}}}
		if _, err := Wrap(cmd, policy); err != nil {
			t.Fatal(err)
		}
		if out, err := cmd.CombinedOutput(); err != nil || string(out) != "visible-fixture\nchild-ran" {
			t.Fatalf("confined child under the openat2 rule: %v: %s", err, out)
		}
	})

	t.Run("a named symlink loop is still refused before the child runs", func(t *testing.T) {
		cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", `printf child-ran`)
		policy := Policy{ReadOnly: []string{"/bin", "/lib", "/lib64", "/usr", loop}, Devices: []Device{{Path: "/dev/null", Write: true}}}
		if _, err := Wrap(cmd, policy); err != nil {
			t.Fatal(err)
		}
		out, err := cmd.CombinedOutput()
		exit, ok := err.(*exec.ExitError)
		if !ok || exit.ExitCode() != 126 || strings.Contains(string(out), "child-ran") || !strings.Contains(string(out), "without symlinks: too many levels of symbolic links") {
			t.Fatalf("a symlink loop was not refused as a loop: %v: %s", err, out)
		}
	})
}

// answerOpenat2WithENOSYS installs, on every thread of this process, the rule systemd's RestrictSUIDSGID= installs.
func answerOpenat2WithENOSYS(t *testing.T) {
	arch, ok := map[string]uint32{"amd64": 0xc000003e, "arm64": 0xc00000b7}[runtime.GOARCH] // AUDIT_ARCH_*
	if !ok {
		t.Fatalf("no seccomp architecture value for %s", runtime.GOARCH)
	}
	filter := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 4}, // seccomp_data.arch
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 3, K: arch},
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0}, // seccomp_data.nr
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 1, K: unix.SYS_OPENAT2},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS)},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
	}
	prog := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	r, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&prog)))
	if errno != 0 || r != 0 {
		t.Fatalf("install the seccomp filter: r=%d %v", r, errno)
	}
}
