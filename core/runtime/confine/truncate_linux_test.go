// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package confine

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

type fixture struct{ data, folder string }

func newFixture(t *testing.T) fixture {
	return fixture{data: t.TempDir(), folder: t.TempDir()}
}

func requireLandlock(t *testing.T) {
	t.Helper()
	if state := Probe(); state.Mode != ModeLandlock {
		t.Fatal("this named kernel check needs Landlock: " + state.String())
	}
}

// Explicit native test grants; the mechanism supplies no defaults.
var systemReadOnly = []string{"/usr", "/bin", "/lib", "/lib64", "/etc", "/proc", "/sys"}
var deviceFiles = []string{"/dev/null", "/dev/zero", "/dev/random", "/dev/urandom"}

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
	landlockABI = func() (int, error) { return abi, nil }
	p := Policy{ReadWrite: []string{os.Getenv("OLV_TEST_WRITABLE")}, ReadOnly: []string{os.Getenv("OLV_TEST_FOLDER")}}
	mode := os.Getenv("OLV_TEST_POLICY")
	if mode == "sealed" || mode == "default_sealed" {
		p.Sealed = p.ReadOnly
	}
	for i, arg := range os.Args {
		if arg == "--" {
			if os.Getenv("OLV_TEST_POLICY") == "write_without_truncate" {
				if err := execWithoutTruncate(p, os.Args[i+1:]); err != nil {
					t.Fatal(err)
				}
				return
			}
			p.ReadOnly = append(p.ReadOnly, systemReadOnly...)
			p.ReadOnly = append(p.ReadOnly, filepath.Dir(os.Args[i+1]))
			for _, path := range deviceFiles {
				p.Devices = append(p.Devices, Device{Path: path, Write: true})
			}
			if err := Exec(p, os.Args[i+1], os.Args[i+1:]); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("missing helper program")
}

func TestReadOnlyExistingFileCannotBeTruncated(t *testing.T) {
	requireLandlock(t)
	if state := Probe(); state.ABI < 3 {
		t.Skip("current-ABI protection needs ABI 3")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, abi := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(abi), func(t *testing.T) {
			f := newFixture(t)
			file := filepath.Join(f.folder, "existing")
			const contents = "keep every byte"
			if err := os.WriteFile(file, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			mode := "sealed"
			if abi < 3 {
				mode = "legacy_readonly" // reproduces the old kernel's unhandled TRUNCATE right
			}
			out, err := runABIHelper(t, abi, f, mode, self, "-test.run=^TestReadOnlyABITruncateProcess$")
			if err != nil {
				t.Fatalf("ABI %d real ruleset probe: %v\n%s", abi, err, out)
			}
			b, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if abi < 3 {
				if len(b) != 0 || !strings.Contains(out, "truncate-open=allowed") {
					t.Fatalf("legacy-ABI control did not reproduce truncation: bytes=%q output=%s", b, out)
				}
			} else if string(b) != contents {
				t.Fatalf("read-only bytes changed or protection unproven: bytes=%q output=%s", b, out)
			}
			for _, process := range []string{"parent", "descendant"} {
				open := "denied"
				if abi < 3 {
					open = "allowed"
				}
				for _, marker := range []string{"truncate-open=" + open, "write-open=denied", "writable-temp=allowed"} {
					if !strings.Contains(out, process+":"+marker) {
						t.Fatalf("missing %s:%s control: %s", process, marker, out)
					}
				}
			}
		})
	}
}

func TestReadOnlyABITruncateProcess(t *testing.T) {
	folder, writable := os.Getenv("OLV_TEST_FOLDER"), os.Getenv("OLV_TEST_WRITABLE")
	if folder == "" {
		return
	}
	process := "parent"
	if os.Getenv("OLV_TEST_DESCENDANT") != "" {
		process = "descendant"
	}
	report := func(message string) { fmt.Println(process + ":" + message) }
	file := filepath.Join(folder, "existing")
	f, err := os.OpenFile(file, os.O_RDONLY|os.O_TRUNC, 0)
	if err != nil {
		report("truncate-open=denied")
	} else {
		_ = f.Close()
		report("truncate-open=allowed")
	}
	f, err = os.OpenFile(file, os.O_WRONLY, 0)
	if err != nil {
		report("write-open=denied")
	} else {
		err = f.Truncate(0)
		_ = f.Close()
		if err != nil {
			report("ftruncate=denied")
		} else {
			report("ftruncate=allowed")
		}
	}
	if err := os.WriteFile(filepath.Join(writable, "scratch"), []byte("scratch"), 0o600); err != nil {
		t.Fatal(err)
	}
	report("writable-temp=allowed")
	if os.Getenv("OLV_TEST_DESCENDANT") == "" {
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		child := exec.Command(self, "-test.run=^TestReadOnlyABITruncateProcess$")
		child.Env = append(os.Environ(), "OLV_TEST_DESCENDANT=1")
		out, err := child.CombinedOutput()
		if err != nil {
			t.Fatalf("descendant truncation probe: %v\n%s", err, out)
		}
		fmt.Print(string(out))
	}
}

// This isolated ruleset grants WRITE_FILE but withholds TRUNCATE. Unlike a
// read-only policy, it lets the probe open a writable descriptor after restriction
// and attempt ftruncate itself. It is not a production session policy.
func execWithoutTruncate(p Policy, argv []string) error {
	runtime.LockOSThread()
	acc := accessFor(3)
	attr := unix.LandlockRulesetAttr{Access_fs: acc.handled}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return errno
	}
	r := ruleset{fd: int(fd), access: acc}
	for _, path := range append(append([]string{}, systemReadOnly...), filepath.Dir(argv[0])) {
		if err := r.grant(path, false); err != nil {
			return err
		}
	}
	if err := r.grant(p.ReadWrite[0], true); err != nil {
		return err
	}
	if err := r.addRule(p.ReadOnly[0], acc.dirRW&^unix.LANDLOCK_ACCESS_FS_TRUNCATE); err != nil {
		return err
	}
	for _, path := range deviceFiles {
		if err := r.addRule(path, acc.fileRW); err != nil {
			return err
		}
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, fd, 0, 0); errno != 0 {
		return errno
	}
	_ = unix.Close(int(fd))
	return syscall.Exec(argv[0], argv, os.Environ())
}

func TestFtruncateRequiresTheTruncateRightOnANewlyOpenedFile(t *testing.T) {
	requireLandlock(t)
	if Probe().ABI < 3 {
		t.Skip("ftruncate protection requires ABI 3")
	}
	f := newFixture(t)
	file := filepath.Join(f.folder, "existing")
	const contents = "keep every byte"
	if err := os.WriteFile(file, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out, err := runABIHelper(t, 3, f, "write_without_truncate", self, "-test.run=^TestReadOnlyABITruncateProcess$")
	if err != nil {
		t.Fatalf("ftruncate rights probe: %v\n%s", err, out)
	}
	for _, process := range []string{"parent", "descendant"} {
		if !strings.Contains(out, process+":ftruncate=denied") || strings.Contains(out, process+":write-open=denied") {
			t.Fatalf("%s did not attempt a denied ftruncate: %s", process, out)
		}
	}
	if b, err := os.ReadFile(file); err != nil || string(b) != contents {
		t.Fatalf("ftruncate changed bytes: %q %v", b, err)
	}
}
