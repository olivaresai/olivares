// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package confine

import (
	"bufio"
	"crypto/sha256"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/olivaresai/olivares/core/runtime/plugjail"

	"golang.org/x/sys/unix"
)

func TestSessionAndSDKChildrenHaveDifferentDefaultReadPolicy(t *testing.T) {
	requireLandlock(t)
	if os.Geteuid() == 0 {
		t.Skip("fixture requires an unprivileged namespace-mapped engine")
	}
	fixture, scratch := t.TempDir(), t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(scratch, "probe")
	if err := os.WriteFile(program, image, 0o755); err != nil {
		t.Fatal(err)
	}
	// Recreate the same absolute executable paths inside an owned rootfs. The
	// /opt marker never exists on the host; all parents and marker are DAC-readable.
	for _, path := range []string{self, program} {
		dest := filepath.Join(fixture, path)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, image, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	marker := "/opt/olivares-policy-proof/ungranted"
	if err := os.MkdirAll(filepath.Join(fixture, filepath.Dir(marker)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture, marker), []byte("synthetic"), 0o444); err != nil {
		t.Fatal(err)
	}
	runIsolated := func(cmd *exec.Cmd) string {
		t.Helper()
		if cmd.SysProcAttr == nil {
			cmd.SysProcAttr = &syscall.SysProcAttr{}
		}
		cmd.SysProcAttr.Cloneflags |= syscall.CLONE_NEWUSER
		cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{{ContainerID: os.Getuid(), HostID: os.Getuid(), Size: 1}}
		cmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{{ContainerID: os.Getgid(), HostID: os.Getgid(), Size: 1}}
		cmd.SysProcAttr.GidMappingsEnableSetgroups = false
		cmd.SysProcAttr.Chroot = fixture
		cmd.Dir = "/"
		out, err := cmd.CombinedOutput()
		if errors.Is(err, syscall.EPERM) {
			t.Skipf("runner refuses owned user-namespace/rootfs isolation: %v", err)
		}
		if err != nil {
			t.Fatalf("isolated child: %v: %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if got := runIsolated(exec.CommandContext(t.Context(), program, "__owned_policy_probe", marker)); got != "read-ok" {
		t.Fatalf("unrestricted same-child DAC control = %q", got)
	}
	session, _, err := Command(t.Context(), Policy{ReadWrite: []string{scratch}}, program, "__owned_policy_probe", marker)
	if err != nil {
		t.Fatal(err)
	}
	if got := runIsolated(session); got != "read-ok" {
		t.Fatalf("SESSION marker = %q, want read-ok", got)
	}
	sdk := exec.CommandContext(t.Context(), program, "__owned_policy_probe", marker)
	c := plugjail.Default("policy-difference")
	c.WritableScratch = scratch
	_, cleanup, err := plugjail.Apply(sdk, c)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	defer plugjail.CloseSpawnFD(sdk)
	if got := runIsolated(sdk); got != "read-denied" {
		t.Fatalf("SDK marker = %q, want read-denied", got)
	}
	t.Logf("owned SESSION/SDK child binary_sha256=%x; unrestricted=read-ok SESSION=read-ok SDK=read-denied for %s", sha256.Sum256(image), marker)
}

// limitOf reads one "Max ..." row of /proc/self/limits output: the soft value.
func limitOf(t *testing.T, out, name string) string {
	t.Helper()
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		if line := sc.Text(); strings.HasPrefix(line, name) {
			return strings.Fields(strings.TrimPrefix(line, name))[0]
		}
	}
	t.Fatalf("no %q row in:\n%s", name, out)
	return ""
}

// A session process runs lower than the engine, cannot write a core dump and may
// not write a file larger than 64 GiB. Everything it starts inherits that. Its
// memory is not limited: race and sanitizer builds reserve terabytes.
func TestConfinedChildRunsWithTheSessionLimits(t *testing.T) {
	requireLandlock(t)
	f := newFixture(t)
	out := run(t, Policy{ReadWrite: []string{f.folder}, Protect: []string{f.data}}, f.folder,
		`cat /proc/self/limits; echo "nice=$(awk '{print $19}' /proc/self/stat)"`)

	if got := limitOf(t, out, "Max core file size"); got != "0" {
		t.Errorf("core file size = %s, want 0", got)
	}
	if got := limitOf(t, out, "Max file size"); got != "68719476736" {
		t.Errorf("file size = %s, want 68719476736 (64 GiB)", got)
	}
	if got := limitOf(t, out, "Max data size"); got != "unlimited" {
		t.Errorf("data size = %s, want unlimited (a race or sanitizer build reserves terabytes)", got)
	}
	prio, err := unix.Getpriority(unix.PRIO_PROCESS, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantNice := min(20-prio+10, 19)
	if !strings.Contains(out, "nice="+strconv.Itoa(wantNice)+"\n") {
		t.Errorf("session nice: want %d in\n%s", wantNice, out)
	}
}
