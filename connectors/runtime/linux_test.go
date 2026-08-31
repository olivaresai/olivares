// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeProc describes one process to materialize under a fake proc root.
type fakeProc struct {
	pid    int
	comm   string
	argv   []string // NUL-joined into cmdline
	uid    int
	cgroup string // cgroup file contents; "" ⇒ no container
}

// writeFakeProc builds a procfs-like tree under root for the given processes,
// plus a couple of non-pid entries to prove they are ignored.
func writeFakeProc(t *testing.T, root string, procs []fakeProc) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	// non-numeric entries that must be skipped.
	for _, name := range []string{"self", "cpuinfo", "meminfo"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	for _, p := range procs {
		dir := filepath.Join(root, strconv.Itoa(p.pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir pid %d: %v", p.pid, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(p.comm+"\n"), 0o600); err != nil {
			t.Fatalf("write comm: %v", err)
		}
		cmdline := strings.Join(p.argv, "\x00")
		if len(p.argv) > 0 {
			cmdline += "\x00"
		}
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmdline), 0o600); err != nil {
			t.Fatalf("write cmdline: %v", err)
		}
		status := "Name:\t" + p.comm + "\nUid:\t" + strconv.Itoa(p.uid) + "\t" + strconv.Itoa(p.uid) + "\t" + strconv.Itoa(p.uid) + "\t" + strconv.Itoa(p.uid) + "\n"
		if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o600); err != nil {
			t.Fatalf("write status: %v", err)
		}
		if p.cgroup != "" {
			if err := os.WriteFile(filepath.Join(dir, "cgroup"), []byte(p.cgroup), 0o600); err != nil {
				t.Fatalf("write cgroup: %v", err)
			}
		}
	}
}

func testConfig(host, procRoot string) config {
	c := loadConfig(sinkConfig(map[string]string{
		cfgEnableLinux:  "true",
		cfgEnableDocker: "false",
		cfgEnableK8s:    "false",
		cfgHost:         host,
		cfgProcRoot:     procRoot,
	}))
	return c
}

func TestGatherLinux_GoldenEdges(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proc")
	const secret = "sk-ant-supersecrettoken1234567890abcdefABCDEF"

	procs := []fakeProc{
		// matches "ollama" via argv[0] basename; not containerized.
		{pid: 7, comm: "ollama", argv: []string{"/usr/local/bin/ollama", "serve"}, uid: 1000},
		// matches "claude" via argv[0]; containerized via 64-hex cgroup. Carries a
		// secret as an argument that must NEVER appear in any emitted ref.
		{pid: 12, comm: "node", argv: []string{"/usr/bin/claude-code", "--token", secret}, uid: 0,
			cgroup: "0::/system.slice/docker-" + strings.Repeat("a", 64) + ".scope\n"},
		// matches "python"? no — must NOT match (not in patterns).
		{pid: 30, comm: "bash", argv: []string{"/bin/bash"}, uid: 1000},
		// matches via SCRIPT basename argv[1] = "langgraph_app.py" -> contains
		// "langgraph" (demonstrates matching an AI framework by its script name).
		{pid: 5, comm: "python3", argv: []string{"/usr/bin/python3", "/srv/langgraph_app.py"}, uid: 1000},
	}
	writeFakeProc(t, root, procs)

	cfg := testConfig("box1", root)
	sink := &fakeSink{}
	if err := gatherLinux(context.Background(), cfg, sink, time.Now().UTC()); err != nil {
		t.Fatalf("gatherLinux: %v", err)
	}

	got := sink.sortedEdgeKeys()
	want := []string{
		// pid 5 python langgraph_app.py -> matched "langgraph" (script basename)
		"host|box1 -> process|box1/python3#5 tool=langgraph mode=unknown src=runtime conf=attributed",
		// pid 7 ollama
		"host|box1 -> process|box1/ollama#7 tool=ollama mode=unknown src=runtime conf=attributed",
		// pid 12 claude-code, host edge (matched token is "claude" — it precedes
		// "claude-code" in the default pattern list and is a substring).
		"host|box1 -> process|box1/claude-code#12 tool=claude mode=unknown src=runtime conf=attributed",
		// pid 12 container edge (short id = 12x 'a')
		"container|" + strings.Repeat("a", 12) + " -> process|box1/claude-code#12 tool= mode=unknown src=runtime conf=attributed",
	}
	// reflect.DeepEqual needs both sorted; want is not yet sorted.
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("edge set mismatch:\n got=%v\nwant=%v", got, want)
	}

	// No finding for a present, readable procfs.
	if fs := sink.findings(); len(fs) != 0 {
		t.Fatalf("expected 0 findings, got %d: %+v", len(fs), fs)
	}

	// The secret must not survive in ANY emitted ref or toolRef.
	for _, e := range sink.edges() {
		for _, field := range []string{e.OriginRef, e.ResourceRef, e.ToolRef} {
			if strings.Contains(field, secret) {
				t.Fatalf("secret leaked into emitted field %q", field)
			}
		}
	}
}

func TestGatherLinux_MissingProcRoot_HealthFinding(t *testing.T) {
	cfg := testConfig("box1", filepath.Join(t.TempDir(), "does-not-exist"))
	sink := &fakeSink{}
	if err := gatherLinux(context.Background(), cfg, sink, time.Now().UTC()); err != nil {
		t.Fatalf("gatherLinux returned error (should be a finding): %v", err)
	}
	if es := sink.edges(); len(es) != 0 {
		t.Fatalf("expected 0 edges on missing procfs, got %d", len(es))
	}
	fs := sink.findings()
	if len(fs) != 1 {
		t.Fatalf("expected exactly 1 health finding, got %d: %+v", len(fs), fs)
	}
	f := fs[0]
	if f.Kind != "health" || f.SubjectKind != subjectHost || f.SubjectRef != "box1" {
		t.Fatalf("unexpected finding: %+v", f)
	}
	if f.DetailHash == "" {
		t.Fatalf("finding must carry a hashed detail")
	}
}

func TestGatherLinux_ContextCanceled(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proc")
	writeFakeProc(t, root, []fakeProc{{pid: 1, comm: "ollama", argv: []string{"/bin/ollama"}, uid: 0}})
	cfg := testConfig("box1", root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := gatherLinux(ctx, cfg, &fakeSink{}, time.Now().UTC())
	if err == nil {
		t.Fatalf("expected ctx error, got nil")
	}
}

func TestReadContainerID(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"docker-" + strings.Repeat("b", 64) + ".scope\n":         strings.Repeat("b", 12),
		"0::/kubepods/cri-containerd-" + strings.Repeat("c", 64): strings.Repeat("c", 12),
		"0::/user.slice/session.scope\n":                         "", // no container
		strings.Repeat("d", 64) + "\n":                           strings.Repeat("d", 12),
	}
	for content, want := range cases {
		p := filepath.Join(dir, "cgroup")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("write cgroup: %v", err)
		}
		if got := readContainerID(p); got != want {
			t.Fatalf("readContainerID(%q) = %q, want %q", content, got, want)
		}
	}
}
