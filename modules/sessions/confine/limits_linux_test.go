// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package confine

import (
	"bufio"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

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
