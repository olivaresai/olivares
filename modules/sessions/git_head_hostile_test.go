// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"compress/zlib"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The folder is written by the agent, so what the reader finds in .git is hostile input:
// it must answer with an error, never panic, never grow without bound, never wait.

func hashObject(t *testing.T, dir, kind, body string) string {
	t.Helper()
	cmd := exec.Command("git", "hash-object", "-t", kind, "-w", "--stdin", "--literally")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "LC_ALL=C"}
	cmd.Stdin = strings.NewReader(body)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git hash-object: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func pointMainAt(t *testing.T, dir, id string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".git", "refs", "heads", "main"), []byte(id+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestHeadBlobRefusesMalformedIds: an id read out of the folder (the tree of a commit, a
// ref) is indexed by the lookups, so a short, non-hex or absent one must be refused before
// it is, with a pack index present (the lookup that would index it).
func TestHeadBlobRefusesMalformedIds(t *testing.T) {
	dir := t.TempDir()
	gitFixture(t, dir, "init", "-q", "-b", "main")
	fixtureWrite(t, dir, "a.txt", "x\n")
	gitFixture(t, dir, "add", ".")
	gitFixture(t, dir, "commit", "-q", "-m", "one")
	gitFixture(t, dir, "repack", "-adq") // a pack index exists
	root := openFixtureRoot(t, dir)

	for name, tree := range map[string]string{
		"one character":               "a",
		"empty":                       "",
		"forty non-hex":               strings.Repeat("z", 40),
		"an object that is not there": strings.Repeat("ab", 20),
		"a path":                      "../../HEAD",
		"sha-256 length":              strings.Repeat("ab", 32),
	} {
		commit := hashObject(t, dir, "commit", "tree "+tree+"\nauthor t <t@example.com> 0 +0000\ncommitter t <t@example.com> 0 +0000\n\nm\n")
		pointMainAt(t, dir, commit)
		if got, err := headBlob(root, "a.txt"); !errors.Is(err, errNoRepository) {
			t.Errorf("commit with tree %q: %q, %v; want errNoRepository", name, got, err)
		}
	}
	for name, ref := range map[string]string{"short": "abc", "not hex": strings.Repeat("g", 40), "sha-256 length": strings.Repeat("ab", 32)} {
		pointMainAt(t, dir, ref)
		if got, err := headBlob(root, "a.txt"); !errors.Is(err, errNoRepository) {
			t.Errorf("ref %s: %q, %v; want errNoRepository", name, got, err)
		}
	}
}

// delta builds a git delta: varint sizes, then the instructions.
func delta(baseSize, size int, ops ...byte) []byte {
	varint := func(n int) []byte {
		var b []byte
		for {
			c := byte(n & 0x7f)
			if n >>= 7; n > 0 {
				b = append(b, c|0x80)
				continue
			}
			return append(b, c)
		}
	}
	return append(append(varint(baseSize), varint(size)...), ops...)
}

// TestApplyDeltaIsBoundedWhileItRuns: a delta is a program written by the agent. One byte
// of it (0x80) copies 64 KiB, so a few KiB of instructions could build gigabytes before a
// check at the end; the result may never pass its declared size.
func TestApplyDeltaIsBoundedWhileItRuns(t *testing.T) {
	base := make([]byte, 1<<16)
	const copyAll = 0x80 // copy with no offset and no size bytes: offset 0, 0x10000 bytes

	if got, err := applyDelta(base, delta(len(base), len(base), copyAll)); err != nil || len(got) != len(base) {
		t.Fatalf("one exact copy = %d bytes, %v", len(got), err)
	}
	// The final size check alone would reject this delta too, but only after building it:
	// 2000 copies are 128 MiB. What shows the bound is that nothing of the sort is allocated.
	bomb := delta(len(base), 100, bytes.Repeat([]byte{copyAll}, 2000)...)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := applyDelta(base, bomb)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, errNoRepository) {
		t.Fatalf("copy past the declared size: err = %v", err)
	}
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 4<<20 {
		t.Fatalf("a delta declared for 100 bytes allocated %d MiB before it was refused", grown>>20)
	}
	twice := delta(len(base), len(base), copyAll, copyAll)
	if _, err := applyDelta(base, twice); !errors.Is(err, errNoRepository) {
		t.Fatalf("two copies of a result declared for one: err = %v", err)
	}
	if _, err := applyDelta(base, delta(len(base), 2, 3, 'a', 'b', 'c')); !errors.Is(err, errNoRepository) {
		t.Fatalf("insert past the declared size: err = %v", err)
	}
	if got, err := applyDelta(base, delta(len(base), 2, 2, 'a', 'b')); err != nil || string(got) != "ab" {
		t.Fatalf("exact insert = %q, %v", got, err)
	}
	if _, err := applyDelta(base, delta(len(base)+1, 2, 2, 'a', 'b')); !errors.Is(err, errNoRepository) {
		t.Fatalf("wrong base size: err = %v", err)
	}
	if _, err := applyDelta(base, delta(len(base), 2, 0x81, 0xff, 0x00)); !errors.Is(err, errNoRepository) {
		t.Fatalf("copy outside the base: err = %v", err)
	}
	if _, err := applyDelta(base, delta(len(base), headMaxObject+1)); !errors.Is(err, errHeadTooLarge) {
		t.Fatalf("declared size past the ceiling: err = %v", err)
	}
}

// TestHeadBlobPackVariants: a delta that names its base by id (REF_DELTA, git's
// repack.useDeltaBaseOffset=false) and an object in the second of two packs.
func TestHeadBlobPackVariants(t *testing.T) {
	dir := t.TempDir()
	gitFixture(t, dir, "init", "-q", "-b", "main")
	for v := 0; v < 5; v++ {
		fixtureWrite(t, dir, "notes.txt", numbered(v*11))
		gitFixture(t, dir, "add", ".")
		gitFixture(t, dir, "commit", "-q", "-m", fmt.Sprintf("v%d", v))
	}
	gitFixture(t, dir, "-c", "repack.useDeltaBaseOffset=false", "repack", "-adq", "--depth=20", "--window=50")
	if out := gitFixture(t, dir, "verify-pack", "-v", firstPackIdx(t, dir)); !strings.Contains(out, "chain length") {
		t.Fatalf("fixture pack has no delta chain:\n%s", out)
	}
	root := openFixtureRoot(t, dir)
	want := gitFixture(t, dir, "show", "HEAD:notes.txt")
	if got, err := headBlob(root, "notes.txt"); err != nil || string(got) != want {
		t.Fatalf("REF_DELTA pack: %d bytes, %v; want %d bytes", len(got), err, len(want))
	}

	// A second pack holds only what came after the first.
	fixtureWrite(t, dir, "later.txt", "added after the first pack\n")
	gitFixture(t, dir, "add", ".")
	gitFixture(t, dir, "commit", "-q", "-m", "later")
	gitFixture(t, dir, "repack", "-dq")
	idx, err := filepath.Glob(filepath.Join(dir, ".git", "objects", "pack", "*.idx"))
	if err != nil || len(idx) != 2 {
		t.Fatalf("want two packs, have %v, %v", idx, err)
	}
	for rel, text := range map[string]string{"later.txt": "added after the first pack\n", "notes.txt": want} {
		if got, err := headBlob(root, rel); err != nil || string(got) != text {
			t.Fatalf("two packs: %s = %d bytes, %v", rel, len(got), err)
		}
	}
}

// TestHeadBlobBudgetStopsATreeThatListsItself: an object file need not hash to its name, so
// an agent can write a tree whose entry "a" is the tree itself and ask for a/a/a/...: one
// full object per component, for as long as the request line allows. The path is capped and
// the whole request has one byte budget.
func TestHeadBlobBudgetStopsATreeThatListsItself(t *testing.T) {
	dir := t.TempDir()
	gitFixture(t, dir, "init", "-q", "-b", "main")
	self := strings.Repeat("ab", 20)
	raw, err := hex.DecodeString(self)
	if err != nil {
		t.Fatal(err)
	}
	// ~7.3 MiB of padding entries (35 bytes each), then "a" pointing at this very tree.
	var tree bytes.Buffer
	for i := 0; i < 220000; i++ {
		fmt.Fprintf(&tree, "100644 p%06d\x00", i)
		tree.Write(make([]byte, 20))
	}
	tree.WriteString("40000 a\x00")
	tree.Write(raw)
	if tree.Len() > headMaxObject {
		t.Fatalf("fixture tree is %d bytes, past the object ceiling", tree.Len())
	}
	var packed bytes.Buffer
	zw := zlib.NewWriter(&packed)
	fmt.Fprintf(zw, "tree %d\x00", tree.Len())
	zw.Write(tree.Bytes())
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".git", "objects", self[:2]), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "objects", self[:2], self[2:]), packed.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	commit := hashObject(t, dir, "commit", "tree "+self+"\nauthor t <t@example.com> 0 +0000\ncommitter t <t@example.com> 0 +0000\n\nm\n")
	pointMainAt(t, dir, commit)
	root := openFixtureRoot(t, dir)

	if _, err := headBlob(root, "a/a/missing"); !errors.Is(err, errNotInHead) {
		t.Fatalf("a short walk through the tree = %v, want errNotInHead", err)
	}
	if _, err := headBlob(root, strings.Repeat("a/", 60)+"a"); !errors.Is(err, errHeadTooLarge) {
		t.Fatalf("60 hops through a 7 MiB tree = %v, want errHeadTooLarge (the request budget)", err)
	}
	if _, err := headBlob(root, strings.Repeat("a/", headMaxDepth)+"a"); !errors.Is(err, errNotInHead) {
		t.Fatalf("a path past %d components = %v, want errNotInHead before any object is read", headMaxDepth, err)
	}
}
