//go:build linux

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hostmetrics

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParsers(t *testing.T) {
	cpu, err := parseCPU("cpu  100 10 50 800 40 0 0 0 0 0\ncpu0 1 1 1 1 1 0 0 0 0 0\n")
	if err != nil || cpu.total != 1000 || cpu.idle != 840 {
		t.Fatalf("parseCPU = %+v, %v", cpu, err)
	}
	if got := cpuPercent(cpuTimes{idle: 840, total: 1000}, cpuTimes{idle: 880, total: 1100}); got != 60 {
		t.Fatalf("cpuPercent = %v, want 60", got)
	}
	mem, swap, err := parseMemory("MemTotal: 1000 kB\nMemAvailable: 400 kB\nSwapTotal: 200 kB\nSwapFree: 50 kB\n")
	if err != nil || mem.TotalBytes != 1000<<10 || mem.UsedBytes != 600<<10 || swap.UsedBytes != 150<<10 {
		t.Fatalf("parseMemory = %+v %+v %v", mem, swap, err)
	}
	if _, _, err = parseMemory("MemTotal: 1000 kB\n"); err == nil {
		t.Fatal("a meminfo without MemAvailable must be refused")
	}
	mount, dev, typ, src, ok := parseMountinfo(`36 35 98:0 / /mnt/my\040disk rw,noatime shared:1 - ext4 /dev/sda1 rw`)
	if !ok || mount != "/mnt/my disk" || dev != "98:0" || typ != "ext4" || src != "/dev/sda1" {
		t.Fatalf("parseMountinfo = %q %q %q %q %v", mount, dev, typ, src, ok)
	}
}

func TestMeasureFoldersSizesEachSessionFolder(t *testing.T) {
	root := t.TempDir()
	for name, size := range map[string]int{"run-small": 10, "run-big": 1 << 20} {
		if err := os.MkdirAll(filepath.Join(root, name, "sub"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name, "sub", "f"), bytes.Repeat([]byte("x"), size), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := measureFolders(context.Background(), root, time.Now())
	if len(got.Folders) != 2 || got.Folders[0].Name != "run-big" || got.Folders[0].Bytes < 1<<20 || got.Truncated {
		t.Fatalf("folders = %+v", got)
	}
	if absent := measureFolders(context.Background(), filepath.Join(root, "none"), time.Now()); len(absent.Folders) != 0 || absent.Truncated {
		t.Fatalf("an absent root must measure as empty, got %+v", absent)
	}
}

func TestHistoryIsCappedAndSurvivesRestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "host-metrics.jsonl")
	m := New(file, "", nil)
	base := time.Now()
	// 2*HistoryLen+5 samples one minute apart, ending now: the file compacts once.
	n := 2*HistoryLen + 5
	for i := range n {
		at := base.Add(-time.Duration(n-1-i) * time.Minute)
		m.now = func() time.Time { return at }
		if err := m.tick(); err != nil {
			t.Fatal(err)
		}
	}
	if len(m.history) != HistoryLen {
		t.Fatalf("history holds %d samples, want %d", len(m.history), HistoryLen)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if lines := bytes.Count(b, []byte("\n")); lines > 2*HistoryLen {
		t.Fatalf("file holds %d lines, cap is %d", lines, 2*HistoryLen)
	}
	again := New(file, "", nil)
	if len(again.history) != HistoryLen || !again.history[len(again.history)-1].At.Equal(m.history[len(m.history)-1].At) {
		t.Fatalf("restart lost the history: %d samples", len(again.history))
	}
	st, err := again.HostStatus(context.Background())
	if err != nil || len(st.History) != HistoryLen || st.Memory.TotalBytes == 0 || len(st.Filesystems) == 0 {
		t.Fatalf("HostStatus on this host = %+v, %v", st, err)
	}
}

func BenchmarkSample(b *testing.B) {
	m := New("", "", nil)
	for b.Loop() {
		if err := m.tick(); err != nil {
			b.Fatal(err)
		}
	}
}
