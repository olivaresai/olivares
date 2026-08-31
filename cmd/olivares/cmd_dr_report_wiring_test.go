// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/spf13/cobra"
)

// THE WITNESSES FOR THE TWO DISCARDED printReport ERRORS (LINT-02, errcheck 2 -> 0).
//
// Two of printReport's four call sites ignored its error. Both were repaired — a render
// failure now ends the command — and NOTHING measured either repair: the change was made
// under a lint heading, and a lint finding closing does not prove the new behavior is
// reachable. Reverting `if err := printReport(...)` to `printReport(...)` at either site
// left every test in this package green, which is the shape this repository keeps finding:
// a correct guard that nobody has to call.
//
// The two sites are NOT the same claim. The drill's whole artifact IS the report, so the
// property is "do not announce a passing drill nobody can read". The in-place restore's
// next statement PROMOTES — it moves the live data files aside and replaces them — so the
// property is much stronger: the live estate must be untouched. The second witness asserts
// that directly, because "the command exited non-zero" would also be satisfied by a command
// that promoted and then complained.
//
// Both assert the REASON and not merely that an error came back. On the restore path a
// failing stdout can also break the seal a few lines earlier, whose refusal carries its own
// (correct) message about the estate being untouched — so an assertion of `err != nil` would
// measure whichever guard fires first rather than the one it names.

// alwaysFailingSink is a stdout that cannot be written to: a closed pipe, a full disk, a
// `| head` that has already gone away. It records the attempts so a witness can prove the
// command really tried to write rather than skipping the render.
type alwaysFailingSink struct{ writes int }

func (s *alwaysFailingSink) Write(p []byte) (int, error) {
	s.writes++
	return 0, errors.New("write /dev/stdout: broken pipe (injected)")
}

// runDROut is runDR with the caller's stdout, so a witness can hand the command a stream
// that fails. Stderr stays a buffer: the human notes go there and must not be the thing
// that fails, or the witness would measure the wrong write.
func runDROut(out io.Writer, args ...string) (string, error) {
	cmd := newDRCmd()
	var errBuf bytes.Buffer
	cmd.SetOut(out)
	cmd.SetErr(&errBuf)
	cmd.SilenceUsage = true
	cmd.SetArgs(args)
	err := cmd.Execute()
	return errBuf.String(), err
}

// TestDRDrillDoesNotAnnounceADrillWhoseReportCouldNotBeRendered is the wiring witness for
// cmd_dr_drill.go's checked printReport.
func TestDRDrillDoesNotAnnounceADrillWhoseReportCouldNotBeRendered(t *testing.T) {
	// NOT FIRING FIRST. Without this, a drill that failed for any reason at all would
	// satisfy the firing case below, and the witness would prove nothing about the report.
	var ok bytes.Buffer
	if _, err := runDROut(&ok, "drill", "--events", "1"); err != nil {
		t.Fatalf("an undisturbed drill must pass, or the firing case below proves nothing: %v\n%s", err, ok.String())
	}
	if !strings.Contains(ok.String(), "\"ok\"") && !strings.Contains(ok.String(), "ok") {
		t.Fatalf("the undisturbed drill printed no report to check against:\n%s", ok.String())
	}

	// FIRING: the report cannot be written.
	sink := &alwaysFailingSink{}
	stderr, err := runDROut(sink, "drill", "--events", "1")
	if err == nil {
		t.Fatal("the drill reported success although its report — the only artifact it produces — could not be rendered")
	}
	if !strings.Contains(err.Error(), "DR drill could not be reported") {
		t.Errorf("the failure must name the guard that fired, not arrive from somewhere else; got: %v\nstderr:\n%s", err, stderr)
	}
	if sink.writes == 0 {
		t.Error("nothing was ever written to stdout, so the render was skipped rather than failed and this witness measured nothing")
	}
}

// TestDRInPlaceRestoreDoesNotPromoteWhenItsReportCannotBeRendered is the wiring witness for
// cmd_dr.go's checked printReport, and it asserts the property rather than the exit code:
// the live estate is still the live estate.
func TestDRInPlaceRestoreDoesNotPromoteWhenItsReportCannotBeRendered(t *testing.T) {
	src, bundle, pf := drDeclarationFixture(t)
	live := filepath.Join(src, "olivares.db")

	// Move the live estate on after the backup, so "untouched" and "restored" cannot be
	// the same bytes by accident — the test would otherwise pass on a promoted estate.
	seedExtraTenant(t, src)
	before, err := os.ReadFile(live)
	if err != nil {
		t.Fatal(err)
	}

	args := []string{"restore", "--in", bundle, "--data-dir", src, "--engine", "sqlite",
		"--passphrase-file", pf, "--in-place", "--operator", "alice@x.io", "--reason", "INC-42"}

	// FIRING: stdout is gone, so the staged restore's only account cannot be rendered.
	sink := &alwaysFailingSink{}
	stderr, err := runDROut(sink, args...)
	if err == nil {
		t.Fatal("the in-place restore promoted over the live estate although the verdict it promoted on could not be rendered")
	}
	if !strings.Contains(err.Error(), "was not promoted") {
		t.Errorf("the refusal must be the promote guard's own, not another one that happens to fire first on a broken stdout; got: %v\nstderr:\n%s", err, stderr)
	}
	if pre, _ := filepath.Glob(live + ".pre-restore-*"); len(pre) != 0 {
		t.Errorf("the promote started anyway: it moved the live store aside (%v)", pre)
	}
	after, err := os.ReadFile(live)
	if err != nil {
		t.Fatalf("the live store is not readable after a refused restore: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("the live store was replaced by a restore that refused: %d bytes before, %d after", len(before), len(after))
	}

	// NOT FIRING: the identical restore with a working stdout must still promote. Without
	// this the guard could be a constant refusal and everything above would stay green.
	var okOut bytes.Buffer
	if _, err := runDROut(&okOut, args...); err != nil {
		t.Fatalf("an in-place restore with a working stdout must promote: %v\n%s", err, okOut.String())
	}
	pre, _ := filepath.Glob(live + ".pre-restore-*")
	if len(pre) != 1 {
		t.Fatalf("the promoted restore left %d pre-restore copies, want 1:\n%s", len(pre), okOut.String())
	}
	kept, err := os.ReadFile(pre[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(kept, before) {
		t.Error("the pre-restore copy does not hold the bytes the promote replaced, so the two runs are not comparable")
	}
}

// printReport's own contract, kept separate from the two wiring witnesses above: the
// component must RETURN the render error. Both call sites can only propagate what they are
// given, so if this regressed to `return nil` the two witnesses above would go green while
// the behavior they describe was gone.
func TestPrintReportReturnsTheRenderFailureItWasGiven(t *testing.T) {
	cmd := &cobra.Command{Use: "x"}
	var okBuf bytes.Buffer
	cmd.SetOut(&okBuf)
	cmd.SetErr(&okBuf)

	// NOT FIRING: a writable stream renders and returns nil.
	if err := printReport(cmd, nil); err != nil {
		t.Fatalf("a writable stream must render without error: %v", err)
	}
	if okBuf.Len() == 0 {
		t.Fatal("nothing was rendered, so the firing case below would not be about the render")
	}

	// FIRING: the write fails and the error must come back.
	cmd.SetOut(&alwaysFailingSink{})
	if err := printReport(cmd, nil); err == nil {
		t.Fatal("printReport swallowed a write failure, which turns every caller's check into a no-op")
	}
}
