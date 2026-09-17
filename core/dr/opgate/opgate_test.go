// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package opgate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// helperEnv turns this test binary into the SECOND PROCESS the exclusion cases
// need. flock is a property of the open file description, so a goroutine or a
// second descriptor in this process would not measure what a real competitor
// measures: only another process proves the kernel is enforcing anything.
const helperEnv = "OLIVARES_OPGATE_HELPER"

// Helper exit codes. They are distinct so a parent can tell "the child was fenced
// out" (the property under test) from "the child failed" (a broken test).
const (
	exitAcquired = 0
	exitFailed   = 1
	exitBusy     = 3
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		os.Exit(runHelper(mode, os.Args[len(os.Args)-1]))
	}
	os.Exit(m.Run())
}

func runHelper(mode, path string) int {
	// The anchor KIND follows the path. It used to be AnchorForStoreFile
	// unconditionally, and the independent review found the consequence: the unwind
	// section of the two-anchor case below passed a DATA DIRECTORY to it and got a
	// store-file anchor for a name nobody locks, so the case never made the
	// conflicting acquisition it claimed to. That was an evidence defect and it is
	// corrected here; AnchorForStoreFile now refuses a directory outright, which is
	// what turned the silent mismatch into a visible one.
	anchor, present, err := helperAnchor(path)
	if err != nil || !present {
		fmt.Fprintf(os.Stderr, "helper: anchor %s: %v (present=%t)\n", path, err, present)
		return exitFailed
	}
	switch mode {
	case "try-shared", "try-exclusive":
		want := ModeShared
		if mode == "try-exclusive" {
			want = ModeExclusive
		}
		lease, ok, err := TryAcquire(want, anchor)
		if err != nil {
			fmt.Fprintf(os.Stderr, "helper: acquire: %v\n", err)
			return exitFailed
		}
		if !ok {
			return exitBusy
		}
		_ = lease.Release()
		return exitAcquired
	case "hold-exclusive":
		// Acquires exclusively, announces it, and holds until its stdin closes. The
		// parent needs a hold that OUTLIVES the child's own call, because what it
		// measures is a request made while a real competitor is still holding the
		// kernel lock — a child that acquired and released would prove nothing.
		lease, ok, err := TryAcquire(ModeExclusive, anchor)
		if err != nil || !ok {
			fmt.Fprintf(os.Stderr, "helper: acquire: %v (ok=%t)\n", err, ok)
			return exitFailed
		}
		defer func() { _ = lease.Release() }()
		fmt.Println(helperHeldMarker)
		var b [1]byte
		_, _ = os.Stdin.Read(b[:])
		return exitAcquired
	case "commit-then-die":
		// Acquires, writes a durable record and exits WITHOUT releasing. The kernel
		// drops the lock; the record must survive. This is the crash the durable state
		// has to outlive.
		lease, ok, err := TryAcquire(ModeExclusive, anchor)
		if err != nil || !ok {
			fmt.Fprintf(os.Stderr, "helper: acquire: %v (ok=%t)\n", err, ok)
			return exitFailed
		}
		if err := lease.Commit(anchor, helperRecord(anchor, StatePending)); err != nil {
			fmt.Fprintf(os.Stderr, "helper: commit: %v\n", err)
			return exitFailed
		}
		os.Exit(exitAcquired) // no Release, no deferred cleanup: this is the crash
		return exitFailed
	default:
		fmt.Fprintf(os.Stderr, "helper: unknown mode %q\n", mode)
		return exitFailed
	}
}

func helperAnchor(path string) (Anchor, bool, error) {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return AnchorForDataDir(path)
	}
	return AnchorForStoreFile(path)
}

func helperRecord(a Anchor, state string) Record {
	return Record{
		Format:      Format,
		Revision:    7,
		OpID:        "0123456789abcdef0123456789abcdef",
		State:       state,
		Enrolled:    true,
		PlanSHA256:  strings.Repeat("a", 64),
		Destination: Destination{Engine: "sqlite", CanonicalPath: a.Canonical(), SQLiteFile: a.Canonical()},
		ObservedAt:  "2026-09-07T12:00:00Z",
	}
}

// helperHeldMarker is printed by the holding helper once the kernel lock is
// actually in its hands. The parent waits for it rather than sleeping: a sleep
// would make the case pass whenever the child was merely slow.
const helperHeldMarker = "OLIVARES_OPGATE_HELD"

// startHoldingHelper starts a second process holding path exclusively and returns a
// stop function. It returns only once the child has announced the hold.
func startHoldingHelper(t *testing.T, path string) (stop func()) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperNeverRuns", path)
	cmd.Env = append(os.Environ(), helperEnv+"=hold-exclusive")
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the holding helper: %v", err)
	}
	var once sync.Once
	stop = func() {
		once.Do(func() {
			_ = in.Close()
			_ = cmd.Wait()
		})
	}
	t.Cleanup(stop)

	held := make(chan bool, 1)
	go func() {
		var seen strings.Builder
		buf := make([]byte, 256)
		for {
			n, err := out.Read(buf[:])
			seen.Write(buf[:n])
			if strings.Contains(seen.String(), helperHeldMarker) {
				held <- true
				return
			}
			if err != nil {
				held <- false
				return
			}
		}
	}()
	select {
	case ok := <-held:
		if !ok {
			stop()
			t.Fatalf("the helper never took %s exclusively", path)
		}
	case <-time.After(20 * time.Second):
		stop()
		t.Fatalf("the helper did not announce its hold on %s", path)
	}
	return stop
}

// runHelperProcessWithin is runHelperProcess with a deadline, for the cases whose
// property is that the call RETURNS at all. timedOut is the finding: a helper that
// had to be killed measured a block, not a refusal.
func runHelperProcessWithin(t *testing.T, mode, path string, d time.Duration) (code int, timedOut bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestHelperNeverRuns", path)
	cmd.Env = append(os.Environ(), helperEnv+"="+mode)
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return exitFailed, true
	}
	if err == nil {
		return exitAcquired, false
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), false
	}
	t.Fatalf("helper %q on %s did not run: %v", mode, path, err)
	return exitFailed, false
}

// runHelperProcess re-executes THIS test binary in helper mode and returns its
// exit code.
func runHelperProcess(t *testing.T, mode, path string) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperNeverRuns", path)
	cmd.Env = append(os.Environ(), helperEnv+"="+mode)
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err == nil {
		return exitAcquired
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	t.Fatalf("helper %q on %s did not run: %v", mode, path, err)
	return exitFailed
}

func inode(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("stat %s: no unix stat", path)
	}
	return st.Ino
}

func storeAnchor(t *testing.T) Anchor {
	t.Helper()
	a, present, err := AnchorForStoreFile(filepath.Join(t.TempDir(), "olivares.db"))
	if err != nil || !present {
		t.Fatalf("anchor: %v (present=%t)", err, present)
	}
	return a
}

// THE CASE THE TWO-FILE DESIGN EXISTS FOR.
//
// A holder replaces the durable record repeatedly — each replacement is a rename,
// so the record's inode changes every time — and a SECOND PROCESS must still be
// fenced out. If the lock lived on the record, the second process would take a
// lock on the record's NEW inode and both would believe they were the only writer.
//
// The inode assertions are the mechanism made visible: the record's changes, the
// lock's does not.
func TestASecondProcessIsFencedOutAcrossRecordReplacements(t *testing.T) {
	a := storeAnchor(t)
	lease, ok, err := TryAcquire(ModeExclusive, a)
	if err != nil || !ok {
		t.Fatalf("the first exclusive acquisition failed: %v (ok=%t)", err, ok)
	}
	defer lease.Release() //nolint:errcheck // asserted by the cases below

	if err := lease.Commit(a, helperRecord(a, StatePending)); err != nil {
		t.Fatalf("first commit: %v", err)
	}
	lockInode := inode(t, a.LockPath())
	recordInodes := map[uint64]bool{inode(t, a.RecordPath()): true}

	for i := 0; i < 4; i++ {
		rec := helperRecord(a, StatePending)
		rec.Revision = int64(8 + i)
		if err := lease.Commit(a, rec); err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
		recordInodes[inode(t, a.RecordPath())] = true

		if got := inode(t, a.LockPath()); got != lockInode {
			t.Fatalf("the LOCK file's inode changed from %d to %d after a record replacement: the lock must live on a stable inode, or a second process locks the new one while the first still holds the old", lockInode, got)
		}
		if code := runHelperProcess(t, "try-exclusive", a.Canonical()); code != exitBusy {
			t.Fatalf("after record replacement %d a second process took the EXCLUSIVE lock (exit %d); the fence is not being enforced across the rename", i, code)
		}
		if code := runHelperProcess(t, "try-shared", a.Canonical()); code != exitBusy {
			t.Fatalf("after record replacement %d a second process took a SHARED lock (exit %d) while an exclusive holder was live", i, code)
		}
	}
	if len(recordInodes) < 2 {
		t.Fatalf("the durable record kept ONE inode across %d replacements, so this case never exercised the rename it exists to survive", len(recordInodes))
	}
}

// The control for the case above: with no holder, a second process acquires.
// Without it, a permanently broken lock would pass the exclusion assertions.
func TestASecondProcessAcquiresWhenNobodyHolds(t *testing.T) {
	a := storeAnchor(t)
	if code := runHelperProcess(t, "try-exclusive", a.Canonical()); code != exitAcquired {
		t.Fatalf("a second process could not take an unheld exclusive lock (exit %d)", code)
	}
	if code := runHelperProcess(t, "try-shared", a.Canonical()); code != exitAcquired {
		t.Fatalf("a second process could not take an unheld shared lock (exit %d)", code)
	}
}

// A crash releases the LOCK and keeps the RECORD. Both halves matter: a fence that
// outlived its holder would need a stale-lock heuristic, and a durable state that
// did not outlive it would make resume impossible.
func TestACrashReleasesTheLockAndKeepsTheDurableState(t *testing.T) {
	a := storeAnchor(t)
	if code := runHelperProcess(t, "commit-then-die", a.Canonical()); code != exitAcquired {
		t.Fatalf("the crashing helper did not reach its commit (exit %d)", code)
	}
	lease, ok, err := TryAcquire(ModeExclusive, a)
	if err != nil {
		t.Fatalf("after the holder died the anchor could not be acquired: %v", err)
	}
	if !ok {
		t.Fatal("after the holder died the anchor was still fenced: the kernel releases an flock when the process ends, so a lock that survives is a stale-lock heuristic nobody wrote")
	}
	defer lease.Release() //nolint:errcheck // best effort in a test teardown
	rec, present, err := lease.Read(a)
	if err != nil {
		t.Fatalf("read the durable record the crashed holder wrote: %v", err)
	}
	if !present {
		t.Fatal("the durable record did not survive the crash, so a resumed operation would have nothing to resume from")
	}
	if rec.State != StatePending || rec.OpID != "0123456789abcdef0123456789abcdef" || rec.Revision != 7 {
		t.Fatalf("the durable record survived with the wrong contents: %+v", rec)
	}
}

// Nested ownership inside ONE process: a shared request under an exclusive lease
// this process already holds is granted from what it holds, and an exclusive
// request over anything held is refused rather than silently upgraded.
// ⛔ THIS CASE USED TO CODIFY F3-IR-1, and it is rewritten rather than deleted so
// the record of what it used to assert survives.
//
// It previously required that a SHARED request under this process's own EXCLUSIVE
// lease be GRANTED, with no parent lease named and no caller identity checked. That
// is the defect: the independent review took the real console restore guard and then
// called the real public engine.Open from an unrelated goroutine, and Open was handed
// the restore's own exclusion and published a Store across it.
//
// What the corrected case measures instead:
//
//   - an unrelated shared request under an exclusive hold is BUSY, in this process
//     exactly as across a process boundary;
//   - the legitimate nesting is DERIVED from the live parent, over an exact subset;
//   - the derived child preserves the ROOT's mode, so a child of an exclusive restore
//     can never be presented as an ordinary publication lease;
//   - releasing the child does not drop the parent's kernel lock, and releasing the
//     parent does not drop it beneath a child that is still open.
func TestAnUnrelatedSharedRequestIsRefusedUnderAnExclusiveHold(t *testing.T) {
	a := storeAnchor(t)
	outer, ok, err := TryAcquire(ModeExclusive, a)
	if err != nil || !ok {
		t.Fatalf("outer exclusive acquisition: %v (ok=%t)", err, ok)
	}
	defer outer.Release() //nolint:errcheck // teardown

	// THE CORRECTION. No parent, no derivation: an ordinary caller asking for the
	// destination while a restore holds it. It is fenced out, and being fenced out is
	// a diagnosis (ok=false) rather than an error.
	borrowed, ok, err := TryAcquire(ModeShared, a)
	if borrowed != nil {
		_ = borrowed.Release()
	}
	if ok {
		t.Fatal("an UNRELATED shared request was granted under this process's exclusive lease: process membership is not ownership, and this is exactly how an ordinary Open published a store across a running restore")
	}
	if err != nil {
		t.Fatalf("genuine contention was reported as an error rather than as busy: %v", err)
	}

	// And an exclusive request over an already-held anchor is still refused rather
	// than upgraded, because a silent upgrade is how lock order gets reversed.
	if _, _, err := TryAcquire(ModeExclusive, a); !errors.Is(err, ErrSelfHeld) {
		t.Fatalf("an exclusive request over an already-held anchor returned %v", err)
	}
	// The kernel is still enforcing the fence against everybody else.
	if code := runHelperProcess(t, "try-shared", a.Canonical()); code != exitBusy {
		t.Fatalf("a second process took a shared lock while this one held the anchor exclusively (exit %d)", code)
	}
}

// The legitimate nesting, which the refusal above makes necessary: a restore that
// holds its destination and needs to read under it derives from its OWN lease.
func TestDerivationNeedsALiveParentAnExactSubsetAndKeepsItsRootMode(t *testing.T) {
	dir := t.TempDir()
	dirAnchor, present, err := AnchorForDataDir(dir)
	if err != nil || !present {
		t.Fatalf("data dir anchor: %v (present=%t)", err, present)
	}
	fileAnchor, present, err := AnchorForStoreFile(filepath.Join(dir, "olivares.db"))
	if err != nil || !present {
		t.Fatalf("store file anchor: %v (present=%t)", err, present)
	}
	foreign := storeAnchor(t)

	root, ok, err := TryAcquire(ModeShared, dirAnchor, fileAnchor)
	if err != nil || !ok {
		t.Fatalf("shared root: %v (ok=%t)", err, ok)
	}

	// A SUBSET is derivable.
	child, err := root.DeriveShared(fileAnchor)
	if err != nil {
		t.Fatalf("a subset of a live shared root was refused: %v", err)
	}
	if child.RootMode() != ModeShared || !child.Derived() {
		t.Fatalf("the child does not carry its provenance: root=%s derived=%t", child.RootMode(), child.Derived())
	}
	// GROWTH is not. A nested operation that needs more must start with more.
	if _, err := root.DeriveShared(fileAnchor, foreign); !errors.Is(err, ErrForeignAnchor) {
		t.Fatalf("a derivation GREW its parent's anchor set (err=%v)", err)
	}

	// Releasing the CHILD leaves the parent's kernel lock in place.
	if err := child.Release(); err != nil {
		t.Fatalf("release the child: %v", err)
	}
	if code := runHelperProcess(t, "try-exclusive", fileAnchor.Canonical()); code != exitBusy {
		t.Fatalf("releasing the derived lease dropped the root's lock (exit %d)", code)
	}

	// A parent that has been RELEASED derives nothing: the capability died with it.
	second, err := root.DeriveShared(fileAnchor)
	if err != nil {
		t.Fatalf("second derivation: %v", err)
	}
	if err := root.Release(); err != nil {
		t.Fatalf("release the root: %v", err)
	}
	if _, err := root.DeriveShared(fileAnchor); !errors.Is(err, ErrLeaseClosed) {
		t.Fatalf("a released parent still derived a child (err=%v)", err)
	}
	// And the child it made BEFORE that release still pins the fence.
	if code := runHelperProcess(t, "try-exclusive", fileAnchor.Canonical()); code != exitBusy {
		t.Fatalf("closing the parent unlocked the kernel fence beneath a live child (exit %d)", code)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("release the surviving child: %v", err)
	}
	// Only now, after the LAST reference, is the destination free.
	if code := runHelperProcess(t, "try-exclusive", fileAnchor.Canonical()); code != exitAcquired {
		t.Fatalf("after the last child closed the anchor is still fenced (exit %d)", code)
	}
}

// AN EXCLUSIVE ROOT CANNOT BE LAUNDERED INTO AN ORDINARY PUBLICATION LEASE.
//
// Derive stays available to the operation that owns the exclusion — it legitimately
// needs to read the control underneath itself — but what it gets back is stamped with
// the exclusive root, and DeriveShared, which is what the ordinary publication path
// uses, refuses the whole chain.
func TestAnExclusiveRootCannotIssueAnOrdinaryPublicationLease(t *testing.T) {
	a := storeAnchor(t)
	restore, ok, err := TryAcquire(ModeExclusive, a)
	if err != nil || !ok {
		t.Fatalf("exclusive root: %v (ok=%t)", err, ok)
	}
	defer restore.Release() //nolint:errcheck // teardown

	if _, err := restore.DeriveShared(a); !errors.Is(err, ErrExclusiveRoot) {
		t.Fatalf("an exclusive root issued an ordinary publication lease (err=%v)", err)
	}
	// Its own private child exists and is USABLE for reading, and it still says where
	// it came from — which is what keeps it out of the ordinary path.
	own, err := restore.Derive(a)
	if err != nil {
		t.Fatalf("the exclusive owner could not derive its own read child: %v", err)
	}
	defer own.Release() //nolint:errcheck // teardown
	if own.RootMode() != ModeExclusive {
		t.Fatalf("the child erased its exclusive provenance: root=%s", own.RootMode())
	}
	if _, err := own.DeriveShared(a); !errors.Is(err, ErrExclusiveRoot) {
		t.Fatalf("provenance was laundered one level down (err=%v)", err)
	}
}

// Two concurrent SHARED roots still coexist. Without this the refusal above would be
// satisfied by a lock that refused everything, and ordinary boots of one installation
// are a normal posture.
func TestConcurrentSharedRootsCoexist(t *testing.T) {
	a := storeAnchor(t)
	first, ok, err := TryAcquire(ModeShared, a)
	if err != nil || !ok {
		t.Fatalf("first shared root: %v (ok=%t)", err, ok)
	}
	second, ok, err := TryAcquire(ModeShared, a)
	if err != nil || !ok {
		t.Fatalf("a second, unrelated SHARED request was refused: %v (ok=%t)", err, ok)
	}
	if code := runHelperProcess(t, "try-shared", a.Canonical()); code != exitAcquired {
		t.Fatalf("a second process could not take a shared lock alongside two shared holders (exit %d)", code)
	}
	if code := runHelperProcess(t, "try-exclusive", a.Canonical()); code != exitBusy {
		t.Fatalf("a second process took the anchor EXCLUSIVELY while shared holders had it (exit %d)", code)
	}
	// Releasing one does not release the other's fence.
	if err := first.Release(); err != nil {
		t.Fatalf("release the first: %v", err)
	}
	if code := runHelperProcess(t, "try-exclusive", a.Canonical()); code != exitBusy {
		t.Fatalf("releasing one shared root dropped the other's lock (exit %d)", code)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("release the second: %v", err)
	}
	if code := runHelperProcess(t, "try-exclusive", a.Canonical()); code != exitAcquired {
		t.Fatalf("after both shared roots released, the anchor is still fenced (exit %d)", code)
	}
	// Release is idempotent.
	if err := second.Release(); err != nil {
		t.Fatalf("a second Release reported an error: %v", err)
	}
}

// Two anchors are taken in canonical order regardless of the order a caller names
// them, and a partial acquisition is unwound rather than left half-held.
func TestTwoAnchorsAreOrderedAndUnwound(t *testing.T) {
	dir := t.TempDir()
	dirAnchor, present, err := AnchorForDataDir(dir)
	if err != nil || !present {
		t.Fatalf("data dir anchor: %v (present=%t)", err, present)
	}
	fileAnchor, present, err := AnchorForStoreFile(filepath.Join(dir, "olivares.db"))
	if err != nil || !present {
		t.Fatalf("store file anchor: %v (present=%t)", err, present)
	}
	forward, ok, err := TryAcquire(ModeExclusive, dirAnchor, fileAnchor)
	if err != nil || !ok {
		t.Fatalf("acquire both anchors: %v (ok=%t)", err, ok)
	}
	got := forward.Anchors()
	if len(got) != 2 || got[0].LockPath() >= got[1].LockPath() {
		t.Fatalf("anchors were not taken in canonical order: %v", got)
	}
	if err := forward.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	reverse, ok, err := TryAcquire(ModeExclusive, fileAnchor, dirAnchor)
	if err != nil || !ok {
		t.Fatalf("acquire both anchors named in the other order: %v (ok=%t)", err, ok)
	}
	if second := reverse.Anchors(); second[0].LockPath() != got[0].LockPath() {
		t.Fatalf("the acquisition order followed the CALLER's order (%s then %s), so two callers naming the same pair differently can interleave",
			second[0].LockPath(), second[1].LockPath())
	}
	if err := reverse.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	// A partial acquisition is unwound: hold ONE anchor, ask for BOTH, and the one
	// that was taken on the way must be given back.
	//
	// ⛔ THIS SECTION USED TO PROVE NOTHING, and the independent review is what caught
	// it: it held one anchor, then asked a helper process about the OTHER one — which
	// nobody had taken — so it passed whether or not any unwinding happened, and its
	// helper silently built a store-file anchor out of a directory name. The corrected
	// version makes the actual conflicting two-anchor request in this process and then
	// proves the anchor taken on the way was released.
	holder, ok, err := TryAcquire(ModeExclusive, fileAnchor)
	if err != nil || !ok {
		t.Fatalf("hold one anchor: %v (ok=%t)", err, ok)
	}

	// The data-dir anchor sorts FIRST (its lock path is shorter and shares the
	// prefix), so this request takes it and then meets the held file anchor.
	if dirAnchor.LockPath() >= fileAnchor.LockPath() {
		t.Fatalf("this case assumes the data-dir anchor is acquired first: %s vs %s", dirAnchor.LockPath(), fileAnchor.LockPath())
	}
	partial, ok, err := TryAcquire(ModeExclusive, dirAnchor, fileAnchor)
	if partial != nil {
		_ = partial.Release()
	}
	if ok {
		t.Fatal("a two-anchor request was granted while one of its anchors was already held")
	}
	if !errors.Is(err, ErrSelfHeld) {
		t.Fatalf("the conflicting anchor produced the wrong diagnosis: %v", err)
	}
	// The one it DID take on the way must be free again — measured by a real second
	// process, because an in-process check would be answered by the registry.
	if code := runHelperProcess(t, "try-exclusive", dirAnchor.Canonical()); code != exitAcquired {
		t.Fatalf("the data-dir anchor was left held after the partial acquisition was refused (exit %d)", code)
	}
	if err := holder.Release(); err != nil {
		t.Fatalf("release the holder: %v", err)
	}
}

// A read that FAILED is never an absence. This is the distinction the whole
// judgement rests on: a truncated or hand-edited control must refuse, not permit.
func TestACorruptRecordIsAnErrorAndNotAnAbsence(t *testing.T) {
	a := storeAnchor(t)
	lease, ok, err := TryAcquire(ModeExclusive, a)
	if err != nil || !ok {
		t.Fatalf("acquire: %v (ok=%t)", err, ok)
	}
	defer lease.Release() //nolint:errcheck // teardown

	if _, present, err := lease.Read(a); err != nil || present {
		t.Fatalf("an anchor with no record reported present=%t err=%v", present, err)
	}
	if err := os.WriteFile(a.RecordPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, present, err := lease.Read(a)
	if err == nil {
		t.Fatalf("a corrupt control was read as a valid record %+v", rec)
	}
	if !present {
		t.Fatal("a corrupt control reported present=false, which would let a boot treat it as a destination that never had one")
	}
}

func TestRecordValidationRefusesEveryMalformedShape(t *testing.T) {
	a := storeAnchor(t)
	base := helperRecord(a, StateComplete)
	base.Keyset = factualTestKeyset()
	if err := base.Validate(a); err != nil {
		t.Fatalf("the well-formed complete record was refused: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*Record)
		want   string
	}{
		{"future format", func(r *Record) { r.Format = 2 }, "format"},
		{"zero revision", func(r *Record) { r.Revision = 0 }, "revision"},
		{"short operation id", func(r *Record) { r.OpID = "abc" }, "operation id"},
		{"uppercase operation id", func(r *Record) { r.OpID = strings.ToUpper(r.OpID) }, "lowercase"},
		{"unknown state", func(r *Record) { r.State = "finished" }, "state"},
		{"another destination", func(r *Record) { r.Destination.CanonicalPath = "/somewhere/else.db" }, "belongs to another destination"},
		{"no engine", func(r *Record) { r.Destination.Engine = "" }, "engine"},
		{"unparsable instant", func(r *Record) { r.ObservedAt = "yesterday" }, "RFC3339"},
		{"complete with no keyset digest", func(r *Record) { r.Keyset.SHA256 = "" }, "keyset digest"},
		{"complete with no keys", func(r *Record) { r.Keyset.Keys = nil }, "no keys"},
		{"duplicate purpose", func(r *Record) {
			r.Keyset.Keys[1] = r.Keyset.Keys[0]
		}, "twice"},
		{"unordered purposes", func(r *Record) {
			r.Keyset.Keys[0], r.Keyset.Keys[1] = r.Keyset.Keys[1], r.Keyset.Keys[0]
		}, "canonical purpose order"},
		{"key with no source", func(r *Record) { r.Keyset.Keys[0].Source = "" }, "custody source"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := base
			rec.Keyset.Keys = append([]KeyFingerprint(nil), base.Keyset.Keys...)
			tc.mutate(&rec)
			err := rec.Validate(a)
			if err == nil {
				t.Fatalf("Validate ACCEPTED %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate refused %s with %q, which does not name %q", tc.name, err, tc.want)
			}
		})
	}
	// A PENDING record carries no keyset, and demanding one would refuse the very
	// first transition an operation writes.
	pending := helperRecord(a, StatePending)
	if err := pending.Validate(a); err != nil {
		t.Fatalf("a pending record with no keyset was refused: %v", err)
	}
}

// A control path that is a symbolic link is refused: a control that can be
// redirected is not a control.
func TestASymlinkedControlIsRefused(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "olivares.db")
	a, present, err := AnchorForStoreFile(target)
	if err != nil || !present {
		t.Fatalf("anchor: %v (present=%t)", err, present)
	}
	elsewhere := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(elsewhere, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, a.LockPath()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := TryAcquire(ModeExclusive, a); !errors.Is(err, ErrSymlink) {
		t.Fatalf("a symlinked lock path was accepted (err=%v)", err)
	}
}

// A shared lease may not write. Commit is the only mutation this package offers,
// and offering it to a reader would make the fence decorative.
func TestASharedLeaseCannotCommit(t *testing.T) {
	a := storeAnchor(t)
	lease, ok, err := TryAcquire(ModeShared, a)
	if err != nil || !ok {
		t.Fatalf("acquire shared: %v (ok=%t)", err, ok)
	}
	defer lease.Release() //nolint:errcheck // teardown
	if err := lease.Commit(a, helperRecord(a, StatePending)); !errors.Is(err, ErrNotExclusive) {
		t.Fatalf("a shared lease wrote the durable record (err=%v)", err)
	}
	// And a lease cannot be used as a general filesystem handle for an anchor it
	// does not hold.
	other := storeAnchor(t)
	if _, _, err := lease.Read(other); !errors.Is(err, ErrForeignAnchor) {
		t.Fatalf("a lease read an anchor it does not hold (err=%v)", err)
	}
}

// An anchor for a data directory that does not exist is ABSENT rather than
// created: a read-only command that finds nothing must report nothing, not
// manufacture a coordination file in a directory it was told to leave alone.
func TestAMissingDataDirYieldsNoAnchor(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-there")
	a, present, err := AnchorForDataDir(missing)
	if err != nil {
		t.Fatalf("a missing data dir produced an error rather than absence: %v", err)
	}
	if present || !a.Zero() {
		t.Fatalf("a missing data dir produced an anchor: %v", a)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("resolving the anchor CREATED the data directory")
	}
}

// The record is written 0600 and the rename is durable. The mode matters because
// the record sits beside key material in the data directory.
func TestTheDurableRecordIsPrivateAndReplaced(t *testing.T) {
	a := storeAnchor(t)
	lease, ok, err := TryAcquire(ModeExclusive, a)
	if err != nil || !ok {
		t.Fatalf("acquire: %v (ok=%t)", err, ok)
	}
	defer lease.Release() //nolint:errcheck // teardown
	if err := lease.Commit(a, helperRecord(a, StatePending)); err != nil {
		t.Fatalf("commit: %v", err)
	}
	info, err := os.Stat(a.RecordPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("the durable control is mode %v, and it sits beside key material", info.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(a.RecordPath()))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("a staging file was left behind: %s", e.Name())
		}
	}
	_ = time.Now
}

// THE PARENT/CHILD RACE, under the detector.
//
// Derivation, Release and the process registry are three pieces of shared state, and
// the ratified contract names the interleaving that matters: closing a parent must
// prevent NEW children without unlocking the kernel fence beneath one that already
// exists. This drives both sides concurrently and asserts the only two outcomes that
// are allowed — a child that is genuinely held, or ErrLeaseClosed — with no third
// answer and no torn state.
func TestDerivationRacesTheParentsRelease(t *testing.T) {
	for i := 0; i < 24; i++ {
		a := storeAnchor(t)
		root, ok, err := TryAcquire(ModeShared, a)
		if err != nil || !ok {
			t.Fatalf("root: %v (ok=%t)", err, ok)
		}

		const derivers = 4
		results := make(chan *Lease, derivers)
		errs := make(chan error, derivers)
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(derivers + 1)
		for d := 0; d < derivers; d++ {
			go func() {
				defer wg.Done()
				<-start
				child, derr := root.DeriveShared(a)
				if derr != nil {
					errs <- derr
					return
				}
				results <- child
			}()
		}
		go func() {
			defer wg.Done()
			<-start
			_ = root.Release()
		}()
		close(start)
		wg.Wait()
		close(results)
		close(errs)

		for derr := range errs {
			if !errors.Is(derr, ErrLeaseClosed) {
				t.Fatalf("a racing derivation failed for a reason other than the parent's release: %v", derr)
			}
		}
		children := 0
		for child := range results {
			children++
			// A child that WAS handed out really holds the anchor: the fence must still
			// be there for a real competitor.
			if code := runHelperProcess(t, "try-exclusive", a.Canonical()); code != exitBusy {
				t.Fatalf("a child derived during the parent's release does not hold the fence (exit %d)", code)
			}
			if err := child.Release(); err != nil {
				t.Fatalf("release the child: %v", err)
			}
		}
		// Once the parent and every child are gone, so is the fence — no reference is
		// leaked by either side of the race.
		if code := runHelperProcess(t, "try-exclusive", a.Canonical()); code != exitAcquired {
			t.Fatalf("after the parent and %d children released, the anchor is still held (exit %d)", children, code)
		}
	}
}

// A lease used AFTER its release is refused rather than acting on stale anchors, and
// a lease is never a general filesystem handle for an anchor it does not hold. These
// are the two ways a stale capability would leak into an action.
func TestAReleasedLeaseIsNoCapabilityAtAll(t *testing.T) {
	a := storeAnchor(t)
	lease, ok, err := TryAcquire(ModeExclusive, a)
	if err != nil || !ok {
		t.Fatalf("acquire: %v (ok=%t)", err, ok)
	}
	if err := lease.Commit(a, helperRecord(a, StatePending)); err != nil {
		t.Fatalf("commit under a live lease: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if !lease.Released() {
		t.Fatal("a released lease does not report itself released")
	}
	if _, _, rerr := lease.Read(a); rerr == nil {
		t.Fatal("a released lease still read its anchor's record")
	}
	if cerr := lease.Commit(a, helperRecord(a, StateComplete)); cerr == nil {
		t.Fatal("a released lease still WROTE its anchor's record")
	}
	if _, derr := lease.Derive(a); !errors.Is(derr, ErrLeaseClosed) {
		t.Fatalf("a released lease still derived a child: %v", derr)
	}
}

// THE DESCRIPTOR MODE, PROVEN RATHER THAN ASSERTED.
//
// The read-only rollout decision rests on one fact about the platform: flock(2)
// places a shared OR an exclusive lock regardless of the mode the descriptor was
// opened in. This build therefore opens an existing lock O_RDONLY, which is the
// access a non-writable custody directory can still give.
//
// If that were wrong, a read-only deployment would fail in a way no unit of this
// package would otherwise notice, so it is measured here on a real read-only
// DIRECTORY and against a real second process — for BOTH modes.
func TestAnExistingLockIsTakenThroughAReadOnlyDescriptor(t *testing.T) {
	for _, mode := range []Mode{ModeShared, ModeExclusive} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := t.TempDir()
			a, present, err := AnchorForDataDir(dir)
			if err != nil || !present {
				t.Fatalf("anchor: %v (present=%t)", err, present)
			}
			if err := ProvisionLock(a); err != nil {
				t.Fatalf("provision: %v", err)
			}
			before, err := os.Stat(a.LockPath())
			if err != nil {
				t.Fatal(err)
			}
			if before.Size() != 0 || before.Mode().Perm() != lockPerm {
				t.Fatalf("the provisioned lock is %d bytes, mode %#o", before.Size(), before.Mode().Perm())
			}
			// THE DIRECTORY IS READ-ONLY from here: nothing may be created, renamed or
			// unlinked in it, and the lock must still be takeable.
			if err := os.Chmod(dir, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

			lease, ok, err := TryAcquire(mode, a)
			if err != nil || !ok {
				t.Fatalf("an existing lock in a read-only directory could not be taken %s: %v (ok=%t)", mode, err, ok)
			}
			// And the kernel really is enforcing it against a real competitor.
			want := exitBusy
			if mode == ModeShared {
				want = exitAcquired
			}
			if code := runHelperProcess(t, "try-shared", dir); code != want {
				t.Fatalf("a second process's shared request returned exit %d under a %s hold, want %d", code, mode, want)
			}
			if code := runHelperProcess(t, "try-exclusive", dir); code != exitBusy {
				t.Fatalf("a second process took the anchor exclusively under a %s hold (exit %d)", mode, code)
			}
			if err := lease.Release(); err != nil {
				t.Fatalf("release: %v", err)
			}
			// The lock file is byte-for-byte and inode-for-inode what it was.
			after, err := os.Stat(a.LockPath())
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(before, after) || after.Size() != 0 || after.Mode().Perm() != lockPerm {
				t.Fatal("taking the lock modified the lock file")
			}
		})
	}
}

// A MISSING lock in a directory that cannot be written is a NAMED refusal carrying
// the exact path and the offline action. It is never a skipped fence, and permission
// denied is never reported as absence.
func TestAMissingLockInAReadOnlyDirectoryIsAProvisioningRefusal(t *testing.T) {
	if runWithoutDACOverride(t) {
		return
	}
	dir := t.TempDir()
	a, present, err := AnchorForDataDir(dir)
	if err != nil || !present {
		t.Fatalf("anchor: %v (present=%t)", err, present)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	requireUnwritableDirectory(t, dir)

	lease, ok, err := TryAcquire(ModeShared, a)
	if lease != nil {
		_ = lease.Release()
	}
	if ok {
		t.Fatal("a destination with no coordination file in an unwritable directory was granted: the fence was skipped")
	}
	if !errors.Is(err, ErrLockProvisioningRequired) {
		t.Fatalf("permission denied was not reported as a provisioning requirement: %v", err)
	}
	if !strings.Contains(err.Error(), a.LockPath()) || !strings.Contains(err.Error(), "OFFLINE") {
		t.Fatalf("the refusal does not carry the exact path and the offline action: %v", err)
	}
	// It created nothing, and it did not fall back to another location.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("the refusal created %d entries in the read-only directory", len(entries))
	}

	// ProvisionLock is the documented operation, and it refuses the same way here
	// rather than pretending to succeed.
	if perr := ProvisionLock(a); !errors.Is(perr, ErrLockProvisioningRequired) {
		t.Fatalf("provisioning into a read-only directory did not refuse: %v", perr)
	}
	// Once the directory allows it, provisioning creates exactly the lock and the
	// same request succeeds.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if perr := ProvisionLock(a); perr != nil {
		t.Fatalf("provision: %v", perr)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	lease, ok, err = TryAcquire(ModeShared, a)
	if err != nil || !ok {
		t.Fatalf("after provisioning, the same read-only acquisition still failed: %v (ok=%t)", err, ok)
	}
	_ = lease.Release()
}

// The lock file is NEVER repaired into acceptance. A path occupied by something that
// is not a regular file is a refusal, and the thing that is there is left alone.
func TestAnUnusableLockPathIsRefusedAndNeverReplaced(t *testing.T) {
	dir := t.TempDir()
	a, present, err := AnchorForDataDir(dir)
	if err != nil || !present {
		t.Fatalf("anchor: %v (present=%t)", err, present)
	}
	if err := os.Mkdir(a.LockPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	lease, ok, err := TryAcquire(ModeShared, a)
	if lease != nil {
		_ = lease.Release()
	}
	if ok || err == nil {
		t.Fatal("a directory at the lock path was accepted as a lock")
	}
	if info, serr := os.Lstat(a.LockPath()); serr != nil || !info.IsDir() {
		t.Fatalf("the refusal replaced what was at the lock path: %v %v", info, serr)
	}
	if perr := ProvisionLock(a); perr == nil {
		t.Fatal("provisioning accepted a directory at the lock path")
	}
	if info, serr := os.Lstat(a.LockPath()); serr != nil || !info.IsDir() {
		t.Fatalf("provisioning replaced what was at the lock path: %v %v", info, serr)
	}
}

// ⛔ F3A-IR-2: A CUSTODY ANCHOR IS ABSOLUTE AND FROZEN, SO ANOTHER DIRECTORY CANNOT
// BORROW ITS HOLDER.
//
// The independent review measured the defect end to end: filepath.EvalSymlinks
// keeps a relative input relative, so `custody` produced the registry key
// `custody/.dr-control.lock` — and after a chdir, a SECOND directory produced the
// very same key. A shared request made from B was then granted out of A's registry
// entry while a separate process held B's real lock exclusively. Both keys printed
// identically in that log, which is what makes this a lock misbinding rather than a
// cosmetic path difference.
//
// The competitor here is a real second process, because flock is a property of the
// open file description: an in-process second descriptor would not measure the
// kernel.
func TestADataDirAnchorIsAbsoluteAndCannotBorrowAnotherDirectory(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	one, two := t.TempDir(), t.TempDir()
	for _, root := range []string{one, two} {
		if err := os.Mkdir(filepath.Join(root, "custody"), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.Chdir(one); err != nil {
		t.Fatal(err)
	}
	first, present, err := AnchorForDataDir("custody")
	if err != nil || !present {
		t.Fatalf("anchor: %v (present=%t)", err, present)
	}
	if !filepath.IsAbs(first.LockPath()) || !filepath.IsAbs(first.RecordPath()) || !filepath.IsAbs(first.Canonical()) {
		t.Fatalf("a relative data dir produced the relative identity %q: a later chdir would move it", first.LockPath())
	}
	lease, ok, err := TryAcquire(ModeShared, first)
	if err != nil || !ok {
		t.Fatalf("shared acquisition of the first custody directory: %v (ok=%t)", err, ok)
	}
	defer func() { _ = lease.Release() }()

	// A REAL competitor holds the SECOND directory exclusively.
	stop := startHoldingHelper(t, filepath.Join(two, "custody"))

	// The cwd moves under the live lease. The anchor taken before it must not
	// follow, and the one taken after must name the directory it was asked about.
	if err := os.Chdir(two); err != nil {
		t.Fatal(err)
	}
	second, present, err := AnchorForDataDir("custody")
	if err != nil || !present {
		t.Fatalf("anchor after chdir: %v (present=%t)", err, present)
	}
	if second.LockPath() == first.LockPath() {
		t.Fatalf("two different custody directories share the registry key %q", second.LockPath())
	}
	for _, want := range []struct {
		anchor Anchor
		root   string
	}{{first, one}, {second, two}} {
		resolved, err := filepath.EvalSymlinks(filepath.Join(want.root, "custody"))
		if err != nil {
			t.Fatal(err)
		}
		if want.anchor.Canonical() != resolved {
			t.Fatalf("the anchor for %s names %q, want %q", want.root, want.anchor.Canonical(), resolved)
		}
	}

	// THE CAUSAL: a new shared request for the second directory must consult the
	// kernel, where a separate process is holding it, instead of being satisfied
	// from the first directory's entry.
	borrowed, granted, err := TryAcquire(ModeShared, second)
	if borrowed != nil {
		_ = borrowed.Release()
	}
	if err != nil {
		t.Fatalf("the shared request was an error rather than a diagnosis: %v", err)
	}
	if granted {
		t.Fatal("a shared lease was granted for the second custody directory while a separate process holds it exclusively: the relative registry entry borrowed the first directory's inode")
	}

	// THE POSITIVE, without which the refusal above would be satisfied by a fence
	// that refuses everything: once the competitor lets go, the same request is
	// granted — and the first directory's lease is still live and still its own.
	stop()
	granted2, ok, err := TryAcquire(ModeShared, second)
	if err != nil || !ok {
		t.Fatalf("after the holder released, the second directory was still refused: %v (ok=%t)", err, ok)
	}
	_ = granted2.Release()
	if lease.Released() {
		t.Fatal("the first lease was released by another directory's traffic")
	}
	if code := runHelperProcess(t, "try-exclusive", filepath.Join(one, "custody")); code != exitBusy {
		t.Fatalf("a second process took the FIRST custody directory exclusively while this process holds it shared (exit %d)", code)
	}
}

// ⛔ F3A-IR-3: AN INVALID LOCK PATH REFUSES PROMPTLY, AND NEVER BLOCKS UNDER THE
// REGISTRY MUTEX.
//
// A FIFO at the lock path made `open(O_RDONLY)` wait for a writer that never came.
// acquireOne holds the process registry across that open, so the stall was not one
// caller's: every other acquisition and release in the process would have queued
// behind it. The independent review's child process printed its readiness marker
// and had to be killed after three seconds.
//
// The bound is measured in a SECOND PROCESS on purpose. A blocked goroutine here
// would hold processMu for the rest of the run and take the whole package's
// remaining cases with it, so the failure would arrive as a package timeout that
// names nothing.
func TestAnInvalidLockPathIsRefusedPromptlyAndNeverOpened(t *testing.T) {
	cases := map[string]struct {
		place func(t *testing.T, path string)
		want  error
	}{
		"fifo": {
			place: func(t *testing.T, path string) {
				if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: ErrLockNotRegular,
		},
		"directory": {
			place: func(t *testing.T, path string) {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			},
			want: ErrLockNotRegular,
		},
		"symlink": {
			place: func(t *testing.T, path string) {
				other := filepath.Join(t.TempDir(), "elsewhere.lock")
				if err := os.WriteFile(other, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, path); err != nil {
					t.Fatal(err)
				}
			},
			want: ErrSymlink,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			a, present, err := AnchorForDataDir(dir)
			if err != nil || !present {
				t.Fatalf("anchor: %v (present=%t)", err, present)
			}
			c.place(t, a.LockPath())
			before, err := os.Lstat(a.LockPath())
			if err != nil {
				t.Fatal(err)
			}

			// THE BOUND: a second process asking for this anchor must come back.
			code, timedOut := runHelperProcessWithin(t, "try-shared", dir, 15*time.Second)
			if timedOut {
				t.Fatalf("a %s at the lock path blocked the acquisition until the child was killed", name)
			}
			if code != exitFailed {
				t.Fatalf("a %s at the lock path was not refused (exit %d)", name, code)
			}

			// THE CLASSIFICATION, in this process, plus the proof that the registry
			// is still usable afterwards.
			lease, ok, err := TryAcquire(ModeShared, a)
			if lease != nil {
				_ = lease.Release()
			}
			if ok {
				t.Fatalf("a %s at the lock path was accepted as a lock", name)
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("a %s at the lock path was refused with the wrong classification: %v", name, err)
			}
			if perr := ProvisionLock(a); !errors.Is(perr, c.want) {
				t.Fatalf("provisioning over a %s did not refuse with the right classification: %v", name, perr)
			}
			after, err := os.Lstat(a.LockPath())
			if err != nil {
				t.Fatalf("the refusal removed what was at the lock path: %v", err)
			}
			if !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatalf("the refusal replaced what was at the lock path: %v -> %v", before.Mode(), after.Mode())
			}
			// And a healthy anchor still works in this process: nothing is stuck.
			healthy, present, err := AnchorForDataDir(t.TempDir())
			if err != nil || !present {
				t.Fatalf("anchor: %v (present=%t)", err, present)
			}
			good, ok, err := TryAcquire(ModeShared, healthy)
			if err != nil || !ok {
				t.Fatalf("after the refusal an ordinary acquisition failed: %v (ok=%t)", err, ok)
			}
			_ = good.Release()
		})
	}
}

// runbookProvisioningScript extracts the operator recipe from docs/DR-RUNBOOK.md
// §9.6 and writes it where it can be executed.
//
// It is EXTRACTED rather than restated. The finding this case closes is that the
// documented shell recipe and the Go helper beside it made different promises, and
// a test that retyped the command would go on passing the day the document drifted
// again.
const runbookPath = "../../../docs/DR-RUNBOOK.md"

func runbookProvisioningScript(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(runbookPath)
	if err != nil {
		t.Fatalf("read the runbook: %v", err)
	}
	doc := string(raw)
	begin := strings.Index(doc, "BEGIN provision-dr-lock.sh")
	if begin < 0 {
		t.Fatalf("%s no longer marks the provisioning recipe this test executes", runbookPath)
	}
	rest := doc[begin:]
	open := strings.Index(rest, "```sh\n")
	if open < 0 {
		t.Fatalf("%s marks the recipe but carries no shell block after it", runbookPath)
	}
	body := rest[open+len("```sh\n"):]
	end := strings.Index(body, "\n```")
	if end < 0 {
		t.Fatalf("%s leaves the provisioning shell block unterminated", runbookPath)
	}
	script := body[:end+1]
	if !strings.Contains(script, "set -C") {
		t.Fatalf("the documented recipe no longer creates exclusively:\n%s", script)
	}
	if strings.Contains(script, "install ") {
		t.Fatalf("the documented recipe uses install(1), which truncates and can re-create the inode:\n%s", script)
	}
	path := filepath.Join(t.TempDir(), "provision-dr-lock.sh")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func runProvisioningScript(t *testing.T, script, lock string) (stdout string, err error) {
	t.Helper()
	cmd := exec.Command("/bin/sh", script, lock)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ⛔ THE DOCUMENTED OFFLINE OPERATION, EXECUTED — not a different helper described
// as equivalent.
//
// The independent review accepted `ProvisionLock` as a safe create-only operation
// and then found that the shell recipe printed beside it in the runbook was
// `install -m 0600 /dev/null <lock>`, which opens the destination O_CREAT|O_TRUNC
// and falls back to unlinking and re-creating it. On an existing lock that resets
// the mode and can hand the path a NEW INODE while a holder is still fencing on the
// old one — two writers, each believing it is alone, which is the exact failure the
// two-file design exists to prevent. The cases labeled "documented operation"
// called the Go helper, so nothing measured the sentence an operator actually types.
//
// This runs the document.
func TestTheDocumentedProvisioningRecipeIsCreateOnlyAndAgreesWithProvisionLock(t *testing.T) {
	script := runbookProvisioningScript(t)

	t.Run("creates-what-the-product-accepts", func(t *testing.T) {
		dir := t.TempDir()
		a, present, err := AnchorForDataDir(dir)
		if err != nil || !present {
			t.Fatalf("anchor: %v (present=%t)", err, present)
		}
		out, err := runProvisioningScript(t, script, a.LockPath())
		if err != nil {
			t.Fatalf("the documented recipe failed on an absent lock: %v\n%s", err, out)
		}
		info, err := os.Lstat(a.LockPath())
		if err != nil {
			t.Fatalf("the documented recipe reported success and created nothing: %v", err)
		}
		if !info.Mode().IsRegular() || info.Size() != 0 || info.Mode().Perm() != lockPerm {
			t.Fatalf("the recipe created a %s of %d bytes, mode %#o; want an empty regular file at %#o",
				describeFileType(info.Mode()), info.Size(), info.Mode().Perm(), lockPerm)
		}
		// THE POINT OF PROVISIONING: the directory can now go read-only and the
		// fence still works. This is the rollout dependency the ratification
		// accepted, measured through the documented step rather than the Go helper.
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		lease, ok, err := TryAcquire(ModeShared, a)
		if err != nil || !ok {
			t.Fatalf("a lock provisioned by the documented recipe could not be taken in a read-only directory: %v (ok=%t)", err, ok)
		}
		_ = lease.Release()
	})

	t.Run("leaves-an-existing-inode-and-bytes-alone", func(t *testing.T) {
		dir := t.TempDir()
		a, present, err := AnchorForDataDir(dir)
		if err != nil || !present {
			t.Fatalf("anchor: %v (present=%t)", err, present)
		}
		// Content the product itself never writes, precisely so a truncation would
		// be visible: the promise is about the FILE, not about what happens to be
		// in it.
		if err := os.WriteFile(a.LockPath(), []byte("do not truncate me"), 0o600); err != nil {
			t.Fatal(err)
		}
		before, err := os.Stat(a.LockPath())
		if err != nil {
			t.Fatal(err)
		}
		out, err := runProvisioningScript(t, script, a.LockPath())
		if err != nil {
			t.Fatalf("the documented recipe failed on an existing lock, which must be a no-op: %v\n%s", err, out)
		}
		after, err := os.Stat(a.LockPath())
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(before, after) {
			t.Fatal("the documented recipe replaced the lock inode, which is the exclusion itself")
		}
		if after.Size() != before.Size() || after.Mode() != before.Mode() {
			t.Fatalf("the documented recipe changed the existing lock: %d bytes mode %v -> %d bytes mode %v",
				before.Size(), before.Mode(), after.Size(), after.Mode())
		}
		body, err := os.ReadFile(a.LockPath())
		if err != nil || string(body) != "do not truncate me" {
			t.Fatalf("the documented recipe truncated the existing lock: %q %v", string(body), err)
		}
		// AND THE TWO OPERATIONS AGREE. ProvisionLock makes the same promise on the
		// same file, in both orders.
		if perr := ProvisionLock(a); perr != nil {
			t.Fatalf("ProvisionLock refused a lock the documented recipe accepted: %v", perr)
		}
		final, err := os.Stat(a.LockPath())
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(before, final) || final.Size() != before.Size() {
			t.Fatal("ProvisionLock and the documented recipe do not make the same promise about an existing lock")
		}
	})

	t.Run("refuses-a-path-the-product-refuses", func(t *testing.T) {
		for name, place := range map[string]func(t *testing.T, path string){
			"fifo": func(t *testing.T, path string) {
				if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			"symlink": func(t *testing.T, path string) {
				if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), path); err != nil {
					t.Fatal(err)
				}
			},
			"directory": func(t *testing.T, path string) {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		} {
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				a, present, err := AnchorForDataDir(dir)
				if err != nil || !present {
					t.Fatalf("anchor: %v (present=%t)", err, present)
				}
				place(t, a.LockPath())
				before, err := os.Lstat(a.LockPath())
				if err != nil {
					t.Fatal(err)
				}
				out, err := runProvisioningScript(t, script, a.LockPath())
				if err == nil {
					t.Fatalf("the documented recipe accepted a %s at the lock path, which the product refuses:\n%s", name, out)
				}
				after, err := os.Lstat(a.LockPath())
				if err != nil {
					t.Fatalf("the documented recipe removed the %s it should have refused: %v", name, err)
				}
				if !os.SameFile(before, after) || before.Mode() != after.Mode() {
					t.Fatalf("the documented recipe replaced the %s at the lock path", name)
				}
				if perr := ProvisionLock(a); perr == nil {
					t.Fatalf("ProvisionLock accepted a %s the documented recipe refused", name)
				}
			})
		}
	})

	// The refusal an operator meets at boot carries a command too, and it is read at
	// exactly the moment somebody is about to type it. It must be the same
	// create-only operation, not the destructive one this correction removed.
	t.Run("the-refusal-recommends-the-same-operation", func(t *testing.T) {
		if runWithoutDACOverride(t) {
			return
		}
		dir := t.TempDir()
		a, present, err := AnchorForDataDir(dir)
		if err != nil || !present {
			t.Fatalf("anchor: %v (present=%t)", err, present)
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		requireUnwritableDirectory(t, dir)
		perr := ProvisionLock(a)
		if !errors.Is(perr, ErrLockProvisioningRequired) {
			t.Fatalf("provisioning into a read-only directory did not refuse: %v", perr)
		}
		msg := perr.Error()
		if strings.Contains(msg, "install ") {
			t.Fatalf("the refusal still recommends install(1), which can replace a live lock inode: %v", perr)
		}
		if !strings.Contains(msg, "set -C") || !strings.Contains(msg, a.LockPath()) {
			t.Fatalf("the refusal does not name a create-only operation on the exact path: %v", perr)
		}
	})
}

// emittedCommand pulls the shell command OUT OF AN ACTUAL PRODUCT ERROR.
//
// ⛔ IT DOES NOT SPLIT ON BACKTICKS, and the first version of this helper did.
// The message names the lock path in plain prose BEFORE the command, so on a path
// that contains a backtick — a valid pathname, and one of the cases below — "the
// first backtick" lands inside that prose and the helper hands back a fragment.
// It cost one confusing red: the fragment failed under sh and the product's
// command was correct all along.
//
// So the opening delimiter is the one immediately followed by the command's own
// first token, and the closing one is the LAST backtick in the message —
// everything after it is fixed prose with none. If the extraction ever slips, the
// checks below fail loudly rather than measuring something shorter than what the
// operator was told to run.
func emittedCommand(t *testing.T, err error) string {
	t.Helper()
	const opener = "`[ -e "
	msg := err.Error()
	start := strings.Index(msg, opener)
	end := strings.LastIndex(msg, "`")
	if start < 0 || end < start+len(opener) {
		t.Fatalf("the diagnostic carries no command to execute: %v", err)
	}
	command := msg[start+1 : end]
	if !strings.HasSuffix(command, ")") {
		t.Fatalf("the extracted command is not whole: %q", command)
	}
	return command
}

// runEmittedCommand executes it with /bin/sh, bounded, and returns everything the
// operator would see.
func runEmittedCommand(t *testing.T, command string) (output string, err error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, runErr := exec.CommandContext(ctx, "/bin/sh", "-c", command).CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("the emitted command did not terminate: %q", command)
	}
	return string(out), runErr
}

// ⛔ F3R2-IR1: THE EMITTED PROVISIONING COMMAND IS EXECUTED, SO IT IS EXECUTED HERE.
//
// The independent review found that the diagnostic interpolated the lock path raw.
// On a custody directory validly named `custody space` the emitted command split
// the path into two words: `[` failed with `unexpected operator`, the redirection
// applied to the first word only, the command CREATED a file nobody asked for,
// left the real lock absent — and EXITED 0. The operator following the product's
// own instruction got a success and no lock.
//
// The previous permanent assertion could not see any of that: it checked the
// message's TEXT — no `install`, a `set -C` present, the path named — and text was
// exactly what was right about it. So this case runs the thing.
//
// Nothing here is hand-written: the command comes out of a real
// ErrLockProvisioningRequired raised by the real ProvisionLock against a real
// unwritable directory, as the unprivileged user the tests run as.
func TestTheEmittedProvisioningCommandCarriesALiteralPathname(t *testing.T) {
	// The names are valid pathnames a deployment may genuinely have. The third one
	// is every shell metacharacter that could turn an operand into a command,
	// including the quote that closes the quoting itself and a backtick, which the
	// diagnostic also uses as its own delimiter.
	for name, dirName := range map[string]string{
		"ordinary":                 "custody",
		"space":                    "custody space",
		"quote-and-metacharacters": "custody's $HOME;touch pwned&`id`|(sub)*?[x] \\slash\ttab",
	} {
		t.Run(name, func(t *testing.T) {
			if runWithoutDACOverride(t) {
				return
			}
			base := t.TempDir()
			dir := filepath.Join(base, dirName)
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			a, present, err := AnchorForDataDir(dir)
			if err != nil || !present {
				t.Fatalf("anchor: %v (present=%t)", err, present)
			}

			// THE REFUSAL AN OPERATOR ACTUALLY MEETS, from the real function.
			if err := os.Chmod(dir, 0o500); err != nil {
				t.Fatal(err)
			}
			requireUnwritableDirectory(t, dir)
			perr := ProvisionLock(a)
			// The instructed step runs with the directory writable again; that is the
			// precondition the message itself states.
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if !errors.Is(perr, ErrLockProvisioningRequired) {
				t.Fatalf("no provisioning diagnostic was raised, so there is no command to execute (running as root would explain it): %v", perr)
			}
			command := emittedCommand(t, perr)

			// 1. IT PROVISIONS THE PATHNAME IT WAS GIVEN.
			output, runErr := runEmittedCommand(t, command)
			if runErr != nil {
				t.Fatalf("the emitted command failed: %v\ncommand: %s\noutput: %s", runErr, command, output)
			}
			if output != "" {
				// The broken form exited 0 while the shell complained on stderr, so a
				// zero status alone is not the property.
				t.Fatalf("the emitted command wrote diagnostics of its own: %q\ncommand: %s", output, command)
			}
			info, err := os.Lstat(a.LockPath())
			if err != nil {
				t.Fatalf("the emitted command reported success and did not create the lock it names: %v\ncommand: %s", err, command)
			}
			if !info.Mode().IsRegular() || info.Size() != 0 || info.Mode().Perm() != lockPerm {
				t.Fatalf("the emitted command created a %s of %d bytes, mode %#o; want an empty regular file at %#o",
					describeFileType(info.Mode()), info.Size(), info.Mode().Perm(), lockPerm)
			}

			// 2. AND IT CREATES NOTHING ELSE. This is the half a zero exit status hid:
			// the split operand made a file one component short of the intended one.
			entries, err := os.ReadDir(base)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != dirName {
				var got []string
				for _, e := range entries {
					got = append(got, e.Name())
				}
				t.Fatalf("the emitted command created something outside the custody directory: %q\ncommand: %s", got, command)
			}
			inside, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(inside) != 1 || inside[0].Name() != filepath.Base(a.LockPath()) {
				var got []string
				for _, e := range inside {
					got = append(got, e.Name())
				}
				t.Fatalf("the emitted command left %q in the custody directory, want only the lock", got)
			}

			// 3. AND ON AN EXISTING LOCK IT IS A NO-OP, inode, bytes and mode alike.
			// A 0400 file with content the product never writes, so a truncation or a
			// re-create would be visible.
			if err := os.Remove(a.LockPath()); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(a.LockPath(), []byte("preserve this inode"), 0o400); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(a.LockPath())
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				out, err := runEmittedCommand(t, command)
				if err != nil || out != "" {
					t.Fatalf("re-running the emitted command over an existing lock failed: %v %q", err, out)
				}
			}
			after, err := os.Stat(a.LockPath())
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(a.LockPath())
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(before, after) || after.Mode() != before.Mode() || string(body) != "preserve this inode" {
				t.Fatalf("re-running the emitted command changed the existing lock: inode-same=%t mode %v -> %v, %d bytes",
					os.SameFile(before, after), before.Mode(), after.Mode(), len(body))
			}
			// And the product agrees with its own instruction about that file.
			if perr := ProvisionLock(a); perr != nil {
				t.Fatalf("ProvisionLock refused the lock its own emitted command provisioned: %v", perr)
			}

			// 4. AN INVALID PATH IS STILL REFUSED, and the command does not repair it.
			if err := os.Remove(a.LockPath()); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(a.LockPath(), 0o700); err != nil {
				t.Fatal(err)
			}
			if out, err := runEmittedCommand(t, command); err != nil || out != "" {
				t.Fatalf("the emitted command errored on an occupied lock path instead of leaving it: %v %q", err, out)
			}
			occupied, err := os.Lstat(a.LockPath())
			if err != nil || !occupied.IsDir() {
				t.Fatalf("the emitted command replaced what was at the lock path: %v %v", occupied, err)
			}
			if perr := ProvisionLock(a); !errors.Is(perr, ErrLockNotRegular) {
				t.Fatalf("a directory at the lock path was not refused: %v", perr)
			}
			if lease, ok, aerr := TryAcquire(ModeShared, a); ok || !errors.Is(aerr, ErrLockNotRegular) {
				if lease != nil {
					_ = lease.Release()
				}
				t.Fatalf("an occupied lock path was accepted for acquisition: ok=%t %v", ok, aerr)
			}
		})
	}
}

// The documented INVOCATION has to be as literal as the script it calls.
//
// The script itself is safe — it takes `"$@"` and quotes `"$lock"` — but a caller
// who copies `sh provision-dr-lock.sh /var/lib/o data/.dr-control.lock` hands it
// TWO arguments, and it then provisions two files neither of which is the one the
// operator meant. That is the same class as F3R2-IR1, one layer up, so the
// examples are quoted and this case keeps them that way.
func TestTheDocumentedInvocationQuotesItsPathOperand(t *testing.T) {
	script := runbookProvisioningScript(t)
	raw, err := os.ReadFile(runbookPath)
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "sh provision-dr-lock.sh"
	var invocations, operands []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		operand := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		if !strings.HasPrefix(operand, "'") || !strings.HasSuffix(operand, "'") || len(operand) < 2 {
			t.Fatalf("the documented invocation passes an operand the shell would split before the script ever saw it: %q", line)
		}
		invocations = append(invocations, line)
		operands = append(operands, operand)
	}
	if len(invocations) == 0 {
		t.Fatalf("%s no longer shows how to invoke the provisioning script", runbookPath)
	}

	// AND THE DOCUMENTED FORM IS EXECUTED, on a path the examples do not have.
	base := t.TempDir()
	dir := filepath.Join(base, "custody space")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	a, present, err := AnchorForDataDir(dir)
	if err != nil || !present {
		t.Fatalf("anchor: %v (present=%t)", err, present)
	}
	command := strings.Replace(invocations[0], "provision-dr-lock.sh", shellSingleQuote(script), 1)
	command = strings.Replace(command, operands[0], shellSingleQuote(a.LockPath()), 1)
	if out, err := runEmittedCommand(t, command); err != nil {
		t.Fatalf("the documented invocation failed on a custody path with a space: %v\ncommand: %s\noutput: %s", err, command, out)
	}
	if info, err := os.Lstat(a.LockPath()); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("the documented invocation did not provision the path it was given: %v", err)
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "custody space" {
		t.Fatalf("the documented invocation created something outside the custody directory: %v", entries)
	}
}
