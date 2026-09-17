// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/release"
)

// license_crl_witness_test.go holds the CR1 causal witnesses for the CRL observation store.
// They reach the recorder ONLY through its long-standing entry points (recordCRLObservations,
// loadCRLObservations, crlFilePath), so the SAME bytes compile against the pre-CR1 recorder
// and against the repaired one. That is the point: the old recorder must go red here and the
// repaired one green, with nothing in between but license_crl.go and its private helpers.
//
// The lease holder in this file takes an ad-hoc flock on the lock path, because the old tree
// has no lock helper to call. license_crl_persistence_test.go holds the lease through the
// production helper instead, so this guessed-path oracle is never the only one.

const (
	envCRLWitnessChild    = "OLIVARES_CRL_WITNESS_CHILD"
	envCRLWitnessDir      = "OLIVARES_CRL_WITNESS_DIR"
	envCRLWitnessReleased = "OLIVARES_CRL_WITNESS_RELEASED"
	envCRLWitnessNow      = "OLIVARES_CRL_WITNESS_NOW"
	envCRLWitnessSerial   = "OLIVARES_CRL_WITNESS_SERIAL"

	// crlWitnessRecorderFailed is the child's exit status when recordCRLObservations returned
	// an error; any other nonzero status is a broken child, not a recorder answer.
	crlWitnessRecorderFailed = 3
)

// TestCRLWitnessChild is not a test: it is the helper process the witnesses re-execute.
func TestCRLWitnessChild(t *testing.T) {
	dir := os.Getenv(envCRLWitnessDir)
	switch os.Getenv(envCRLWitnessChild) {
	case "":
		t.Skip("helper process for the CRL witnesses")
	case "flock-hold":
		f, err := os.OpenFile(filepath.Join(dir, "license-crl.lock"), os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			fmt.Fprintln(os.Stderr, "open:", err)
			os.Exit(2)
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			fmt.Fprintln(os.Stderr, "flock:", err)
			os.Exit(2)
		}
		fmt.Println("held")
		_, _ = io.Copy(io.Discard, os.Stdin) // hold until the parent closes stdin
		os.Exit(0)
	case "record":
		released, err1 := strconv.ParseInt(os.Getenv(envCRLWitnessReleased), 10, 64)
		now, err2 := strconv.ParseInt(os.Getenv(envCRLWitnessNow), 10, 64)
		if err1 != nil || err2 != nil {
			os.Exit(2)
		}
		// Start barrier: every competing child is released by the same parent write.
		if _, err := os.Stdin.Read(make([]byte, 1)); err != nil {
			os.Exit(2)
		}
		m := manifestRevoking(&release.RevokedSet{Serials: []string{"s-common", os.Getenv(envCRLWitnessSerial)}},
			time.Unix(released, 0).UTC())
		if err := recordCRLObservations(dir, m, time.Unix(now, 0).UTC()); err != nil {
			fmt.Println("recorder error:", err)
			os.Exit(crlWitnessRecorderFailed)
		}
		fmt.Println("recorded")
		os.Exit(0)
	default:
		os.Exit(2)
	}
}

func crlWitnessCommand(dir, mode string, extra ...string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestCRLWitnessChild$", "-test.count=1")
	cmd.Env = append(os.Environ(),
		"OLIVARES_CLI_TRAMPOLINE=", // the child must be a test process, not the CLI
		envCRLWitnessChild+"="+mode,
		envCRLWitnessDir+"="+dir,
	)
	cmd.Env = append(cmd.Env, extra...)
	return cmd
}

type crlDirEntry struct {
	mode os.FileMode
	data string
}

// snapshotCRLDir captures every entry of the data directory by type, permission and bytes.
func snapshotCRLDir(t *testing.T, dir string) map[string]crlDirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]crlDirEntry{}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		info, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		entry := crlDirEntry{mode: info.Mode()}
		if info.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			entry.data = string(b)
		}
		out[e.Name()] = entry
	}
	return out
}

func diffCRLDir(before, after map[string]crlDirEntry) string {
	names := map[string]bool{}
	for n := range before {
		names[n] = true
	}
	for n := range after {
		names[n] = true
	}
	var diffs []string
	for n := range names {
		b, inB := before[n]
		a, inA := after[n]
		switch {
		case !inA:
			diffs = append(diffs, "removed "+n)
		case !inB:
			diffs = append(diffs, "created "+n)
		case a != b:
			diffs = append(diffs, fmt.Sprintf("changed %s (mode %v -> %v, bytes equal=%v)", n, b.mode, a.mode, a.data == b.data))
		}
	}
	sort.Strings(diffs)
	return strings.Join(diffs, "; ")
}

// Contract §5 item 1: an independent process holds the CRL lease; a recorder must refuse
// and leave the store, the historical staging sentinel and every other entry unchanged.
func TestCRLWitnessRecorderRespectsAnIndependentLease(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if err := recordCRLObservations(dir, manifestRevoking(&release.RevokedSet{Serials: []string{"s1"}}, t0), t0); err != nil {
		t.Fatalf("seeding the store must succeed: %v", err)
	}
	if err := os.WriteFile(crlFilePath(dir)+".tmp", []byte("historical staging sentinel\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	holder := crlWitnessCommand(dir, "flock-hold")
	stdin, err := holder.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := holder.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var holderErr bytes.Buffer
	holder.Stderr = &holderErr
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = holder.Wait()
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "held\n" {
		t.Fatalf("the holder process did not take the lease: %q %v (stderr %s)", line, err, holderErr.String())
	}

	before := snapshotCRLDir(t, dir)
	t1 := t0.Add(24 * time.Hour)
	rerr := recordCRLObservations(dir, manifestRevoking(&release.RevokedSet{Serials: []string{"s2"}}, t1), t1)
	after := snapshotCRLDir(t, dir)
	if rerr == nil {
		t.Errorf("the recorder returned success while an independent process held the CRL lease")
	}
	if d := diffCRLDir(before, after); d != "" {
		t.Errorf("the recorder changed the data directory under an independent lease: %s", d)
	}

	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := holder.Wait(); err != nil {
		t.Fatalf("the holder must exit cleanly after release: %v (stderr %s)", err, holderErr.String())
	}
}

// Contract §5 item 2: the old fixed staging name is unrelated historical material. A
// successful recording must not overwrite, rename or re-permission it, and the published
// store is 0600 even though the sentinel is 0644.
func TestCRLWitnessHistoricalStagingNameIsUntouched(t *testing.T) {
	dir := t.TempDir()
	sentinel := crlFilePath(dir) + ".tmp"
	want := []byte("historical staging sentinel\n")
	if err := os.WriteFile(sentinel, want, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sentinel, 0o644); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if err := recordCRLObservations(dir, manifestRevoking(&release.RevokedSet{Serials: []string{"s1"}}, t0), t0); err != nil {
		t.Fatalf("recording must succeed: %v", err)
	}
	got, err := os.ReadFile(sentinel)
	switch {
	case errors.Is(err, os.ErrNotExist):
		t.Errorf("the historical %s sentinel was consumed (renamed or removed) by the recorder", filepath.Base(sentinel))
	case err != nil:
		t.Fatal(err)
	case !bytes.Equal(got, want):
		t.Errorf("the historical staging sentinel was overwritten: %q", got)
	}
	if info, err := os.Lstat(sentinel); err == nil && info.Mode().Perm() != 0o644 {
		t.Errorf("the historical staging sentinel was re-permissioned to %v", info.Mode().Perm())
	}
	info, err := os.Stat(crlFilePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("the published CRL store must be 0600, got %v", info.Mode().Perm())
	}
	obs, ok, err := loadCRLObservations(dir)
	if err != nil || !ok || len(obs.Serials) != 1 || obs.Serials[0] != "s1" {
		t.Errorf("the published store must hold this observation: %+v ok=%v err=%v", obs, ok, err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, "license-crl-*")); len(leftovers) != 0 {
		t.Errorf("successful recording left staging files behind: %v", leftovers)
	}
}

// Contract §5 item 3: competing recorders in separate processes. Every invocation that
// returned success must be reflected: the final anchor is the newest successful release
// of the round (an older success after a newer commit is a no-op, never a rollback), and
// the still-listed identity keeps the clock seeded before any competition started.
func TestCRLWitnessCompetingRecordersNeverLoseASuccess(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	seedNow := base
	if err := recordCRLObservations(dir, manifestRevoking(&release.RevokedSet{Serials: []string{"s-common"}}, base), seedNow); err != nil {
		t.Fatalf("seeding the store must succeed: %v", err)
	}
	const rounds, competitors = 6, 6
	totalSuccess, totalRefused := 0, 0
	expectAnchor := base
	for r := 1; r <= rounds; r++ {
		roundBase := base.Add(time.Duration(r) * 24 * time.Hour)
		type child struct {
			cmd      *exec.Cmd
			stdin    io.WriteCloser
			out      bytes.Buffer
			released time.Time
		}
		children := make([]*child, competitors)
		for i := 0; i < competitors; i++ {
			// Interleave older and newer manifests inside one round: 5,0,4,1,3,2 hours.
			offset := []int{5, 0, 4, 1, 3, 2}[i]
			c := &child{released: roundBase.Add(time.Duration(offset) * time.Hour)}
			c.cmd = crlWitnessCommand(dir, "record",
				envCRLWitnessReleased+"="+strconv.FormatInt(c.released.Unix(), 10),
				envCRLWitnessNow+"="+strconv.FormatInt(roundBase.Add(time.Duration(i)*time.Minute).Unix(), 10),
				envCRLWitnessSerial+"="+fmt.Sprintf("s-r%d-c%d", r, i),
			)
			var err error
			if c.stdin, err = c.cmd.StdinPipe(); err != nil {
				t.Fatal(err)
			}
			c.cmd.Stdout = &c.out
			c.cmd.Stderr = &c.out
			if err := c.cmd.Start(); err != nil {
				t.Fatal(err)
			}
			children[i] = c
		}
		for _, c := range children {
			_, _ = c.stdin.Write([]byte{'g'})
		}
		var newestSuccess time.Time
		for i, c := range children {
			_ = c.stdin.Close()
			err := c.cmd.Wait()
			var exitErr *exec.ExitError
			switch {
			case err == nil:
				totalSuccess++
				if c.released.After(newestSuccess) {
					newestSuccess = c.released
				}
			case errors.As(err, &exitErr) && exitErr.ExitCode() == crlWitnessRecorderFailed:
				totalRefused++
			default:
				t.Fatalf("round %d child %d broke instead of answering: %v\n%s", r, i, err, c.out.String())
			}
		}
		if newestSuccess.After(expectAnchor) {
			expectAnchor = newestSuccess
		}
		obs, ok, err := loadCRLObservations(dir)
		if err != nil || !ok {
			t.Fatalf("round %d left an unreadable store: ok=%v err=%v", r, ok, err)
		}
		if got := obs.SetByReleasedAt; got != expectAnchor.Format(time.RFC3339) {
			t.Errorf("round %d: a successful recorder's transaction was lost: anchor %s, newest success %s",
				r, got, expectAnchor.Format(time.RFC3339))
		}
		if got := obs.FirstObserved["serial:s-common"]; got != seedNow.Format(time.RFC3339) {
			t.Errorf("round %d: the still-listed identity's clock reset to %s", r, got)
		}
	}
	t.Logf("competing recorders: %d successes, %d explicit refusals over %d rounds", totalSuccess, totalRefused, rounds)
	if totalSuccess < rounds {
		t.Errorf("fewer successes (%d) than rounds (%d): the witness did not exercise commits", totalSuccess, rounds)
	}
}
