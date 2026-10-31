// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// openCodeFileTokens is an opencode.json whose MCP command arguments are one
// {file:} token per name.
func openCodeFileTokens(names []string) string {
	var b strings.Builder
	b.WriteString(`{"mcp":{"probe":{"type":"local","command":["`)
	for i, name := range names {
		if i > 0 {
			b.WriteString(`","`)
		}
		b.WriteString("{file:" + name + "}")
	}
	b.WriteString(`"]}}}`)
	return b.String()
}

// sparseFolder is a folder of bounded files with valid text for substitution
// tests. Larger oversize fixtures stay sparse; no large allocation is needed.
func sparseFolder(t *testing.T, names []string, size int64) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if size > maxProjectConfigTreeBytes {
			err = f.Truncate(size)
		} else {
			chunk := make([]byte, 32*1024)
			for i := range chunk {
				chunk[i] = 'x'
			}
			for left := size; left > 0; left -= int64(len(chunk)) {
				n := min(left, int64(len(chunk)))
				if _, err = f.Write(chunk[:n]); err != nil {
					break
				}
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	return dir
}

func distinctNames(n int) []string {
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("f%d", i)
	}
	return names
}

// OpenCode reads a {file:} with an absolute path as written, so a decoy at
// <folder>/<that path> must not stand in for it: the file is refused, in
// opencode.json and in .opencode/opencode.json alike.
func TestProjectConfigOpenCodeRefusesAbsoluteFileTokens(t *testing.T) {
	for _, tc := range []struct{ config, decoyUnder string }{
		{"opencode.json", ""},
		{".opencode/opencode.json", ".opencode"},
	} {
		t.Run(tc.config, func(t *testing.T) {
			dir := t.TempDir()
			real := filepath.Join(t.TempDir(), "cmd")
			config := `{"mcp":{"p":{"type":"local","command":["{file:` + real + `}"]}}}`
			for path, content := range map[string]string{
				real:                                    "true",
				filepath.Join(dir, tc.decoyUnder, real): "true", // what the check would read for the absolute path
				filepath.Join(dir, tc.config):           config,
			} {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			entries, _, err := readProjectConfig(dir)
			if err != nil || len(entries) != 1 || entries[0].Refused == "" {
				t.Fatalf("an absolute {file:} with a decoy in the folder = %+v %v, want it refused", entries, err)
			}
		})
	}
}

// The files that {file:} tokens name are read once each and under one budget, so a
// folder cannot make every check read and hash a large file once per token, nor name
// more files or bytes than a folder tree may hold; and exactly the limits still pass.
func TestProjectConfigOpenCodeFileTokenWorkIsBounded(t *testing.T) {
	check := func(t *testing.T, dir, config string, refused bool) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "opencode.json"), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
		done := make(chan []projectConfigEntry, 1)
		go func() {
			entries, _, _ := readProjectConfig(dir)
			done <- entries
		}()
		select {
		case entries := <-done:
			if len(entries) != 1 || (entries[0].Refused != "") != refused {
				t.Fatalf("refused = %v, want %v: %+v", len(entries) == 1 && entries[0].Refused != "", refused, entries)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("the check is still reading the files its {file:} tokens name")
		}
	}
	t.Run("repeated content over the effective size limit is refused", func(t *testing.T) {
		names := make([]string, 16384)
		for i := range names {
			names[i] = "b"
		}
		check(t, sparseFolder(t, []string{"b"}, maxProjectConfigBytes), openCodeFileTokens(names), true)
	})
	t.Run("a small repeated target is read once and allowed", func(t *testing.T) {
		names := make([]string, 8)
		for i := range names {
			names[i] = "b"
		}
		check(t, sparseFolder(t, []string{"b"}, maxProjectConfigBytes), openCodeFileTokens(names), false)
	})
	t.Run("more distinct files than a folder tree may hold are refused", func(t *testing.T) {
		names := distinctNames(maxProjectConfigTreeFiles + 1)
		check(t, sparseFolder(t, names, 1), openCodeFileTokens(names), true)
	})
	t.Run("as many distinct files as a folder tree may hold are not", func(t *testing.T) {
		names := distinctNames(maxProjectConfigTreeFiles)
		check(t, sparseFolder(t, names, 1), openCodeFileTokens(names), false)
	})
	t.Run("more bytes than a folder tree may hold are refused", func(t *testing.T) {
		names := distinctNames(maxProjectConfigTreeBytes/maxProjectConfigBytes + 1)
		check(t, sparseFolder(t, names, maxProjectConfigBytes), openCodeFileTokens(names), true)
	})
	t.Run("as many bytes as a folder tree may hold are not", func(t *testing.T) {
		names := distinctNames(maxProjectConfigTreeBytes / maxProjectConfigBytes)
		check(t, sparseFolder(t, names, maxProjectConfigBytes), openCodeFileTokens(names), false)
	})
}

// A file over the parse limit is read up to that limit before it is refused, so it is
// charged what was read: many distinct ones must not cost every check gigabytes of
// reads under a budget of 64 MiB.
func TestProjectConfigOpenCodeOversizeFileTokensAreChargedWhatWasRead(t *testing.T) {
	names := distinctNames(maxProjectConfigTreeFiles)
	dir := sparseFolder(t, names, maxProjectConfigBytes+1)
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), []byte(openCodeFileTokens(names)), 0o644); err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	entries, _, err := readProjectConfig(dir)
	runtime.ReadMemStats(&after)
	if err != nil || len(entries) != 1 || entries[0].Refused == "" {
		t.Fatalf("%d files over the limit named by {file:} = %+v %v, want them refused", len(names), entries, err)
	}
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 512<<20 {
		t.Fatalf("the check read and allocated %d bytes for files it refuses, want the 64 MiB budget to end it", grown)
	}
}

// An oversized OpenCode configuration can never be approved, so the gate refuses it
// after the bounded parse read instead of hashing the rest of the file to EOF.
func TestProjectConfigOpenCodeOversizeConfigIsNotHashedToEOF(t *testing.T) {
	dir := sparseFolder(t, []string{"opencode.json"}, 8<<30)
	type result struct {
		entries []projectConfigEntry
		err     error
	}
	done := make(chan result, 1)
	go func() {
		entries, _, err := readProjectConfig(dir)
		done <- result{entries, err}
	}()
	select {
	case got := <-done:
		if got.err != nil || len(got.entries) != 1 || got.entries[0].Refused == "" {
			t.Fatalf("an oversized opencode.json = %+v %v, want it refused", got.entries, got.err)
		}
		if strings.Contains(got.entries[0].Unreadable, "sha256 of its content") {
			t.Fatalf("the gate hashed the whole oversized file before refusing it: %+v", got.entries[0])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the gate is still hashing an 8 GiB sparse opencode.json to EOF")
	}
}

// An OpenCode configuration file that is not a regular file, or cannot be opened or
// read inside the folder, has content no hash binds (a FIFO serves what its writer
// wants at each read): it is refused, not approved by the name of the problem.
func TestProjectConfigOpenCodeFileThatCannotBeReadIsRefused(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "opencode.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, _, err := readProjectConfig(dir)
	if err != nil || len(entries) != 1 || entries[0].Refused == "" {
		t.Fatalf("an opencode.json that is a FIFO = %+v %v, want it refused", entries, err)
	}
}

// A file or directory is opened first and judged on the handle, so an entry
// swapped for a FIFO between a stat and the open cannot hang the check.
func testOpenWithinNeverBlocksOnAFIFO(t *testing.T, open func(*os.Root, string) (*os.File, error), valid string) {
	t.Helper()
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "f"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "r"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	done := make(chan error, 1)
	go func() {
		f, err := open(root, "f")
		if err == nil {
			f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a FIFO was opened")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("opening a FIFO blocks")
	}
	f, err := open(root, valid)
	if err != nil {
		t.Fatalf("%s = %v", valid, err)
	}
	f.Close()
}

func TestOpenRegularWithinNeverBlocksOnAFIFO(t *testing.T) {
	testOpenWithinNeverBlocksOnAFIFO(t, openRegularWithin, "r")
}

func TestOpenDirWithinNeverBlocksOnAFIFO(t *testing.T) {
	testOpenWithinNeverBlocksOnAFIFO(t, openDirWithin, "d")
}
