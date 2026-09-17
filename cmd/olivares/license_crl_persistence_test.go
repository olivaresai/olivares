// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package main

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/release"
)

// license_crl_persistence_test.go covers the CR1 contract clauses the causal witnesses in
// license_crl_witness_test.go cannot name, because those must also compile against the old
// recorder: the stable failure categories, the production lease helper held from another
// process, release on every return and on process death, refusal of non-regular lock paths,
// and the publication boundaries under injected faults.

const envCRLPersistChild = "OLIVARES_CRL_PERSIST_CHILD"

// TestCRLPersistenceChild is not a test: it holds the lease through the PRODUCTION helper.
func TestCRLPersistenceChild(t *testing.T) {
	if os.Getenv(envCRLPersistChild) != "lease-hold" {
		t.Skip("helper process for the CRL persistence tests")
	}
	ops := defaultCRLPersistOps()
	lease, err := acquireCRLStoreLock(os.Getenv(envCRLWitnessDir), ops)
	if err != nil {
		os.Stderr.WriteString("acquire: " + err.Error() + "\n")
		os.Exit(2)
	}
	os.Stdout.WriteString("held\n")
	_, _ = io.Copy(io.Discard, os.Stdin)
	if err := releaseCRLStoreLock(lease, ops, nil, false); err != nil {
		os.Exit(4)
	}
	os.Exit(0)
}

type crlLeaseHolder struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
}

func startCRLLeaseHolder(t *testing.T, dir string) *crlLeaseHolder {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCRLPersistenceChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "OLIVARES_CLI_TRAMPOLINE=", envCRLPersistChild+"=lease-hold", envCRLWitnessDir+"="+dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h := &crlLeaseHolder{cmd: cmd, stdin: stdin}
	t.Cleanup(func() {
		_ = h.stdin.Close()
		if h.cmd.ProcessState == nil {
			_ = h.cmd.Process.Kill()
			_ = h.cmd.Wait()
		}
	})
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "held\n" {
		t.Fatalf("the holder process did not take the lease through acquireCRLStoreLock: %q %v", line, err)
	}
	return h
}

func seedCRLStoreWithSentinel(t *testing.T, dir string) time.Time {
	t.Helper()
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if err := recordCRLObservations(dir, manifestRevoking(&release.RevokedSet{Serials: []string{"s1"}}, t0), t0); err != nil {
		t.Fatalf("seeding the store must succeed: %v", err)
	}
	if err := os.WriteFile(crlFilePath(dir)+".tmp", []byte("historical staging sentinel\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return t0
}

func lstatCRLLock(t *testing.T, dir string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(filepath.Join(dir, crlLockFileName))
	if err != nil {
		t.Fatalf("the permanent CRL lock must exist: %v", err)
	}
	return info
}

func mustAcquireAndReleaseCRLLease(t *testing.T, dir, after string) {
	t.Helper()
	ops := defaultCRLPersistOps()
	lease, err := acquireCRLStoreLock(dir, ops)
	if err != nil {
		t.Fatalf("after %s the CRL lease could not be retaken: %v", after, err)
	}
	if err := releaseCRLStoreLock(lease, ops, nil, false); err != nil {
		t.Fatalf("releasing a retaken lease: %v", err)
	}
}

func crlSerials(t *testing.T, dir string) []string {
	t.Helper()
	obs, ok, err := loadCRLObservations(dir)
	if err != nil || !ok {
		t.Fatalf("store unreadable: ok=%v err=%v", ok, err)
	}
	return obs.Serials
}

// Contract §5 items 1 and 4, through the production helper: busy is its own category and
// changes nothing; the lease is released by a normal exit and by SIGKILL; the lock inode is
// permanent across all of it.
func TestCRLLeaseHeldByAnotherProcessThroughTheProductionHelper(t *testing.T) {
	dir := t.TempDir()
	t0 := seedCRLStoreWithSentinel(t, dir)
	lockInode := lstatCRLLock(t, dir)

	holder := startCRLLeaseHolder(t, dir)
	before := snapshotCRLDir(t, dir)
	t1 := t0.Add(24 * time.Hour)
	err := recordCRLObservations(dir, manifestRevoking(&release.RevokedSet{Serials: []string{"s2"}}, t1), t1)
	if !errors.Is(err, errCRLStoreBusy) {
		t.Fatalf("a held lease must be reported as busy, got %v", err)
	}
	if errors.Is(err, errCRLNotPublished) || errors.Is(err, errCRLPublishedUnconfirmed) {
		t.Errorf("a busy refusal happens before any publication step: %v", err)
	}
	if d := diffCRLDir(before, snapshotCRLDir(t, dir)); d != "" {
		t.Errorf("a busy recorder changed the data directory: %s", d)
	}

	// Normal release: the holder closes its lease and exits.
	_ = holder.stdin.Close()
	if err := holder.cmd.Wait(); err != nil {
		t.Fatalf("the holder must release and exit cleanly: %v", err)
	}
	if err := recordCRLObservations(dir, manifestRevoking(&release.RevokedSet{Serials: []string{"s2"}}, t1), t1); err != nil {
		t.Fatalf("after the holder released, recording must succeed: %v", err)
	}
	if got := crlSerials(t, dir); len(got) != 1 || got[0] != "s2" {
		t.Fatalf("the retried observation must be published, got %v", got)
	}

	// Process death: SIGKILL gives the holder no chance to release anything itself.
	dead := startCRLLeaseHolder(t, dir)
	t2 := t1.Add(24 * time.Hour)
	if err := recordCRLObservations(dir, manifestRevoking(&release.RevokedSet{Serials: []string{"s3"}}, t2), t2); !errors.Is(err, errCRLStoreBusy) {
		t.Fatalf("the second holder must hold the lease: %v", err)
	}
	if err := dead.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = dead.cmd.Wait()
	if err := recordCRLObservations(dir, manifestRevoking(&release.RevokedSet{Serials: []string{"s3"}}, t2), t2); err != nil {
		t.Fatalf("the kernel must release the lease of a killed holder: %v", err)
	}
	if got := crlSerials(t, dir); len(got) != 1 || got[0] != "s3" {
		t.Fatalf("the observation after the holder's death must be published, got %v", got)
	}
	if !os.SameFile(lockInode, lstatCRLLock(t, dir)) {
		t.Error("the CRL lock inode changed: releasing must never unlink or replace the lock")
	}
	if got, _ := os.ReadFile(crlFilePath(dir) + ".tmp"); string(got) != "historical staging sentinel\n" {
		t.Errorf("the historical staging sentinel changed: %q", got)
	}
}

// Contract §5 item 4, in process: every return path releases the lease — success, the
// older-manifest no-op and a load/parse failure — and the lock stays the same empty inode.
func TestCRLLeaseReleasedOnEveryReturn(t *testing.T) {
	dir := t.TempDir()
	t0 := seedCRLStoreWithSentinel(t, dir)
	mustAcquireAndReleaseCRLLease(t, dir, "a successful publication")
	lockInode := lstatCRLLock(t, dir)

	published, _ := os.ReadFile(crlFilePath(dir))
	older := t0.Add(-24 * time.Hour)
	if err := recordCRLObservations(dir, manifestRevoking(nil, older), t0.Add(time.Hour)); err != nil {
		t.Fatalf("an older manifest is a successful no-op: %v", err)
	}
	if got, _ := os.ReadFile(crlFilePath(dir)); string(got) != string(published) {
		t.Fatal("the older-manifest no-op changed the store")
	}
	mustAcquireAndReleaseCRLLease(t, dir, "an older-manifest no-op")

	corrupt := []byte("{not json")
	if err := os.WriteFile(crlFilePath(dir), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	t1 := t0.Add(24 * time.Hour)
	err := recordCRLObservations(dir, manifestRevoking(&release.RevokedSet{Serials: []string{"s2"}}, t1), t1)
	if err == nil || errors.Is(err, errCRLStoreBusy) {
		t.Fatalf("a corrupt store must refuse with its own error, got %v", err)
	}
	if got, _ := os.ReadFile(crlFilePath(dir)); string(got) != string(corrupt) {
		t.Fatalf("the corrupt store must be preserved, got %q", got)
	}
	mustAcquireAndReleaseCRLLease(t, dir, "a load/parse failure")

	after := lstatCRLLock(t, dir)
	if !os.SameFile(lockInode, after) {
		t.Error("the CRL lock inode changed across releases")
	}
	if after.Size() != 0 || after.Mode().Perm() != 0o600 {
		t.Errorf("the CRL lock must be an empty 0600 file, got size %d mode %v", after.Size(), after.Mode().Perm())
	}
}

// Contract §5 item 6: flock excludes a second descriptor inside one process.
func TestCRLLeaseTwoDescriptorsInOneProcessConflict(t *testing.T) {
	dir := t.TempDir()
	ops := defaultCRLPersistOps()
	first, err := acquireCRLStoreLock(dir, ops)
	if err != nil {
		t.Fatalf("a fresh regular lock must acquire: %v", err)
	}
	if second, err := acquireCRLStoreLock(dir, ops); !errors.Is(err, errCRLStoreBusy) {
		if second != nil {
			_ = releaseCRLStoreLock(second, ops, nil, false)
		}
		t.Fatalf("a second descriptor in the same process must be busy, got %v", err)
	}
	if err := releaseCRLStoreLock(first, ops, nil, false); err != nil {
		t.Fatal(err)
	}
	mustAcquireAndReleaseCRLLease(t, dir, "the first descriptor's release")
}

// Contract §5 item 6: an existing regular lock file is used as is — never truncated.
func TestCRLLeaseKeepsAnExistingRegularLockUntruncated(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, crlLockFileName)
	want := "diagnostic left by an older build\n"
	if err := os.WriteFile(lockPath, []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if err := recordCRLObservations(dir, manifestRevoking(&release.RevokedSet{Serials: []string{"s1"}}, t0), t0); err != nil {
		t.Fatalf("a regular pre-existing lock must acquire: %v", err)
	}
	if got, _ := os.ReadFile(lockPath); string(got) != want {
		t.Errorf("the recorder truncated or rewrote the lock file: %q", got)
	}
}

// Contract §5 item 6: a FIFO, socket, directory, or symlink (to an existing target or
// dangling) at the lock path is refused promptly, with no change to the store, the historical
// staging sentinel, the symlink target, or the directory the symlink points into.
func TestCRLLeaseRefusesNonRegularLockPathsWithoutSideEffects(t *testing.T) {
	cases := []struct {
		name  string
		plant func(t *testing.T, lockPath, outside string)
	}{
		{"FIFO", func(t *testing.T, lockPath, _ string) {
			if err := syscall.Mkfifo(lockPath, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"socket", func(t *testing.T, lockPath, _ string) {
			if err := syscall.Mknod(lockPath, syscall.S_IFSOCK|0o600, 0); err != nil {
				t.Skipf("this platform does not let an unprivileged process create a socket node: %v", err)
			}
		}},
		{"directory", func(t *testing.T, lockPath, _ string) {
			if err := os.Mkdir(lockPath, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink to an existing file", func(t *testing.T, lockPath, outside string) {
			target := filepath.Join(outside, "foreign")
			if err := os.WriteFile(target, []byte("foreign bytes\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, lockPath); err != nil {
				t.Fatal(err)
			}
		}},
		{"dangling symlink", func(t *testing.T, lockPath, outside string) {
			if err := os.Symlink(filepath.Join(outside, "absent"), lockPath); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, outside := t.TempDir(), t.TempDir()
			// Seed through a regular lock, then replace the lock path with the planted entry.
			t0 := seedCRLStoreWithSentinel(t, dir)
			lockPath := filepath.Join(dir, crlLockFileName)
			if err := os.Remove(lockPath); err != nil {
				t.Fatal(err)
			}
			tc.plant(t, lockPath, outside)
			beforeDir, beforeOutside := snapshotCRLDir(t, dir), snapshotCRLDir(t, outside)

			start := time.Now()
			t1 := t0.Add(24 * time.Hour)
			err := recordCRLObservations(dir, manifestRevoking(&release.RevokedSet{Serials: []string{"s2"}}, t1), t1)
			elapsed := time.Since(start)
			if !errors.Is(err, errCRLLockUnsafe) {
				t.Fatalf("a %s at the lock path must be refused as unsafe, got %v", tc.name, err)
			}
			if elapsed > 10*time.Second {
				t.Errorf("the refusal took %s; it must not wait", elapsed)
			}
			if d := diffCRLDir(beforeDir, snapshotCRLDir(t, dir)); d != "" {
				t.Errorf("the refused recorder changed the data directory: %s", d)
			}
			if d := diffCRLDir(beforeOutside, snapshotCRLDir(t, outside)); d != "" {
				t.Errorf("the refused recorder changed the symlink's target directory: %s", d)
			}
			t.Logf("refused in %s: %v", elapsed, err)
		})
	}
}

var errInjectedCRLFault = errors.New("injected fault through the private test seam (not a device failure)")

// Contract §5 item 5: every publication boundary, through the production recorder with a
// private ops copy. INJECTED failures: they prove the error classification, the byte custody
// and the single close per descriptor, not power-loss durability.
func TestCRLPublicationFaultBoundaries(t *testing.T) {
	type closes struct{ file, dir, lock int }
	counting := func(c *closes) crlPersistOps {
		ops := defaultCRLPersistOps()
		ops.closeFile = func(f *os.File) error { c.file++; return f.Close() }
		ops.closeDir = func(d *os.File) error { c.dir++; return d.Close() }
		ops.closeLock = func(f *os.File) error { c.lock++; return f.Close() }
		return ops
	}
	// A failing close still releases the descriptor, as close(2) does on Linux.
	failingClose := func(n *int) func(*os.File) error {
		return func(f *os.File) error { *n++; _ = f.Close(); return errInjectedCRLFault }
	}
	cases := []struct {
		name      string
		inject    func(ops *crlPersistOps, c *closes)
		published bool
		dirCloses int
		wantIs    []error
		wantNot   []error
	}{
		{name: "write fails", inject: func(o *crlPersistOps, _ *closes) {
			o.write = func(*os.File, []byte) (int, error) { return 0, errInjectedCRLFault }
		}, wantIs: []error{errCRLNotPublished, errInjectedCRLFault}, wantNot: []error{errCRLPublishedUnconfirmed, errCRLLeaseCleanup}},
		{name: "short write", inject: func(o *crlPersistOps, _ *closes) {
			o.write = func(f *os.File, b []byte) (int, error) { return f.Write(b[:len(b)/2]) }
		}, wantIs: []error{errCRLNotPublished, io.ErrShortWrite}, wantNot: []error{errCRLPublishedUnconfirmed}},
		{name: "file sync fails", inject: func(o *crlPersistOps, _ *closes) {
			o.syncFile = func(*os.File) error { return errInjectedCRLFault }
		}, wantIs: []error{errCRLNotPublished, errInjectedCRLFault}, wantNot: []error{errCRLPublishedUnconfirmed}},
		{name: "file close fails", inject: func(o *crlPersistOps, c *closes) {
			o.closeFile = failingClose(&c.file)
		}, wantIs: []error{errCRLNotPublished, errInjectedCRLFault}, wantNot: []error{errCRLPublishedUnconfirmed}},
		{name: "rename fails", inject: func(o *crlPersistOps, _ *closes) {
			o.rename = func(string, string) error { return errInjectedCRLFault }
		}, wantIs: []error{errCRLNotPublished, errInjectedCRLFault}, wantNot: []error{errCRLPublishedUnconfirmed}},
		{name: "directory sync fails after rename", inject: func(o *crlPersistOps, _ *closes) {
			o.syncDir = func(*os.File) error { return errInjectedCRLFault }
		}, published: true, dirCloses: 1, wantIs: []error{errCRLPublishedUnconfirmed, errInjectedCRLFault}, wantNot: []error{errCRLNotPublished, errCRLLeaseCleanup}},
		{name: "directory close fails after rename", inject: func(o *crlPersistOps, c *closes) {
			o.closeDir = failingClose(&c.dir)
		}, published: true, dirCloses: 1, wantIs: []error{errCRLPublishedUnconfirmed, errInjectedCRLFault}, wantNot: []error{errCRLNotPublished}},
		{name: "lease close fails after confirmed publication", inject: func(o *crlPersistOps, c *closes) {
			o.closeLock = failingClose(&c.lock)
		}, published: true, dirCloses: 1, wantIs: []error{errCRLLeaseCleanup, errInjectedCRLFault}, wantNot: []error{errCRLNotPublished, errCRLPublishedUnconfirmed}},
		{name: "write and lease close both fail", inject: func(o *crlPersistOps, c *closes) {
			o.write = func(*os.File, []byte) (int, error) { return 0, errInjectedCRLFault }
			o.closeLock = failingClose(&c.lock)
		}, wantIs: []error{errCRLNotPublished, errCRLLeaseCleanup}, wantNot: []error{errCRLPublishedUnconfirmed}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t0 := seedCRLStoreWithSentinel(t, dir)
			oldBytes, err := os.ReadFile(crlFilePath(dir))
			if err != nil {
				t.Fatal(err)
			}
			var c closes
			ops := counting(&c)
			tc.inject(&ops, &c)

			t1 := t0.Add(24 * time.Hour)
			err = recordCRLObservationsWith(dir, manifestRevoking(&release.RevokedSet{Serials: []string{"s2"}}, t1), t1, ops)
			if err == nil {
				t.Fatal("an injected fault must surface as an error")
			}
			for _, want := range tc.wantIs {
				if !errors.Is(err, want) {
					t.Errorf("error must match %q: %v", want, err)
				}
			}
			for _, not := range tc.wantNot {
				if errors.Is(err, not) {
					t.Errorf("error must NOT match %q: %v", not, err)
				}
			}
			if joined, ok := err.(interface{ Unwrap() []error }); ok && len(joined.Unwrap()) > 0 && len(tc.wantIs) > 0 {
				if !errors.Is(joined.Unwrap()[0], tc.wantIs[0]) {
					t.Errorf("the primary cause must come first, got %v", joined.Unwrap()[0])
				}
			}

			gotBytes, _ := os.ReadFile(crlFilePath(dir))
			if tc.published {
				if got := crlSerials(t, dir); len(got) != 1 || got[0] != "s2" {
					t.Errorf("after the rename the new store must stay published, got %v", got)
				}
			} else if string(gotBytes) != string(oldBytes) {
				t.Errorf("before the rename the previous store bytes must stand, got %q", gotBytes)
			}
			if info, err := os.Stat(crlFilePath(dir)); err != nil || info.Mode().Perm() != 0o600 {
				t.Errorf("the store must remain 0600: %v %v", info, err)
			}
			if left, _ := filepath.Glob(filepath.Join(dir, "license-crl-staging-*")); len(left) != 0 {
				t.Errorf("this invocation's staging file was left behind: %v", left)
			}
			if got, _ := os.ReadFile(crlFilePath(dir) + ".tmp"); string(got) != "historical staging sentinel\n" {
				t.Errorf("the historical staging sentinel changed: %q", got)
			}
			if c.file != 1 || c.lock != 1 || c.dir != tc.dirCloses {
				t.Errorf("each descriptor must be closed exactly once: staging %d, directory %d (want %d), lease %d",
					c.file, c.dir, tc.dirCloses, c.lock)
			}
			mustAcquireAndReleaseCRLLease(t, dir, tc.name)
			t.Logf("injected: %v", err)
		})
	}
}
