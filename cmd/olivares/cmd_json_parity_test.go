// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestJSONOutputIsUnchangedAgainstABaseBinary runs the SAME command through two
// built binaries and asserts `-o json` is byte-identical.
//
// WHY A SECOND BINARY AND NOT A GOLDEN. The claim the whole presentation change
// rests on is "the text form is free to become readable because a script reads
// -o json, whose bytes are unchanged". A golden captured in this branch proves the
// bytes are stable within the branch; it cannot prove they are the same as the
// bytes the previous binary emitted, which is the claim. Only two binaries can.
//
// It SELF-SKIPS without OLIVARES_JSON_PARITY_BASE, and says how to run it. That is
// a skip and not a pass: a run that did not happen is reported as one.
//
//	go build -o /tmp/olivares-new ./cmd/olivares
//	git archive <base-sha> | tar -x -C /tmp/base && (cd /tmp/base && go build -o /tmp/olivares-base ./cmd/olivares)
//	OLIVARES_JSON_PARITY_BASE=/tmp/olivares-base OLIVARES_JSON_PARITY_NEW=/tmp/olivares-new \
//	  go test ./cmd/olivares -run TestJSONOutputIsUnchangedAgainstABaseBinary
//
// The commands are the ten that answer with no engine, no database and no network,
// chosen so the comparison measures the binaries and not the state around them.
func TestJSONOutputIsUnchangedAgainstABaseBinary(t *testing.T) {
	base := strings.TrimSpace(os.Getenv("OLIVARES_JSON_PARITY_BASE"))
	next := strings.TrimSpace(os.Getenv("OLIVARES_JSON_PARITY_NEW"))
	if base == "" || next == "" {
		t.Skip("set OLIVARES_JSON_PARITY_BASE and OLIVARES_JSON_PARITY_NEW to two built binaries " +
			"(see the comment above this test); NOT RUN is not a pass")
	}
	for _, p := range []string{base, next} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("binary %s: %v", p, err)
		}
	}

	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	data := filepath.Join(dir, "data")
	root := filepath.Join(dir, "tools")
	for _, d := range []string{home, data, root} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// The ten that answer with no engine, no database and no network, so the
	// comparison measures the binaries and not the state around them. Commands that
	// refuse for want of a server write their refusal to STDERR and nothing to
	// stdout, which would compare two empty strings and prove nothing — they are
	// excluded here rather than counted as passes.
	cases := [][]string{
		{"version"},
		{"openapi"},
		{"doctor", "--data-dir", data},
		{"keys", "status"},
		{"license", "status"},
		{"config", "effective"},
		{"dr", "ls"},
		{"migrate", "manifest"},
		{"agent", "tool", "detect", "--driver", "claude", "--root", root},
		{"agent", "tool", "ls", "--root", root},
	}
	if len(cases) < 10 {
		t.Fatalf("the brief asks for ten commands and this table has %d", len(cases))
	}

	// BOTH binaries run from the SAME absolute path, one after the other. `doctor`
	// reports the path of the binary it is inspecting, so two binaries with
	// different names differ in their JSON for a reason that has nothing to do with
	// the presentation — and a comparison that reports that as a change is one
	// nobody will trust the second time. Measured: it was the only difference
	// `doctor` had.
	slot := filepath.Join(dir, "bin", "olivares")
	if err := os.MkdirAll(filepath.Dir(slot), 0o755); err != nil {
		t.Fatal(err)
	}
	install := func(from string) {
		t.Helper()
		b, err := os.ReadFile(from) // #nosec G304 -- the path comes from the test's own environment
		if err != nil {
			t.Fatalf("read %s: %v", from, err)
		}
		// UNLINK first. Writing over a binary that has just run answers ETXTBSY;
		// removing the name and creating a new one does not, because the running
		// image keeps the old inode and nobody is holding this name.
		if err := os.Remove(slot); err != nil && !os.IsNotExist(err) {
			t.Fatalf("clear the slot: %v", err)
		}
		if err := os.WriteFile(slot, b, 0o755); err != nil { // #nosec G306 -- it has to be executable
			t.Fatalf("install %s: %v", from, err)
		}
	}

	run := func(bin string, args []string) ([]byte, int) {
		install(bin)
		cmd := exec.Command(slot, append(append([]string{}, args...), "-o", "json")...) // #nosec G204 -- both paths come from the test's own environment
		cmd.Env = append(os.Environ(),
			"HOME="+home,
			"OLIVARES_DATA_DIR="+data,
			"NO_COLOR=1",
		)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = nil
		err := cmd.Run()
		code := 0
		var ee *exec.ExitError
		if err != nil {
			if ok := asExitError(err, &ee); ok {
				code = ee.ExitCode()
			} else {
				code = -1
			}
		}
		return out.Bytes(), code
	}

	compared := 0
	for _, args := range cases {
		name := strings.Join(args, " ")
		t.Run(name, func(t *testing.T) {
			gotBase, codeBase := run(base, args)
			gotNext, codeNext := run(next, args)
			// A command that cannot even start on this host compares two empty
			// strings and proves nothing, so it is named rather than counted.
			if len(bytes.TrimSpace(gotBase)) == 0 && len(bytes.TrimSpace(gotNext)) == 0 {
				t.Skipf("%s wrote nothing to stdout on either binary (rc %d/%d): nothing to compare",
					name, codeBase, codeNext)
			}
			if codeBase != codeNext {
				t.Errorf("%s: exit code changed from %d to %d", name, codeBase, codeNext)
			}
			if !bytes.Equal(gotBase, gotNext) {
				t.Errorf("%s: -o json is NOT byte-identical.\nbase:\n%s\nnew:\n%s", name, gotBase, gotNext)
			}
			compared++
		})
	}
	if compared == 0 {
		t.Fatal("no command produced output on either binary; the comparison measured nothing")
	}
	t.Logf("compared %d of %d commands byte for byte", compared, len(cases))
}

// asExitError keeps the errors import out of the file for one type assertion.
//
// It is declared HERE and only here. This file carries no build constraint, so it is
// compiled into every configuration of this package's tests, the e2e-tagged ones included;
// the e2e bootstrap test uses this declaration rather than a second one, which under
// -tags e2e was a redeclaration and stopped that whole test binary from building.
func asExitError(err error, into **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError)
	if ok {
		*into = ee
	}
	return ok
}
