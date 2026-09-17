// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package opgate is the LOCAL half of the DR restore control: a filesystem lock
// that fences publication of a destination, and a durable record of what the
// operation in progress is doing to it.
//
// It is deliberately a stdlib-only leaf. Both the composition root (cmd/olivares,
// core/api) and the store constructor (core/internal/store/sqlstore) have to reach
// it, and the store must not grow a dependency on the DR bundle stack to do so.
//
// # The two files, and why they are two
//
// An anchor owns TWO paths:
//
//   - a LOCK file, whose inode is created once and NEVER replaced or unlinked, and
//   - a RECORD file, a JSON document replaced atomically by temp/write/fsync/
//     rename/dir-fsync.
//
// They are separate because flock() is a property of the INODE. If the durable
// record were itself the lock target, then the moment a holder replaced it by
// rename the file at that path would be a NEW inode — and a second process opening
// that path would take a lock on the new inode and succeed while the first process
// still held the old one. Both would then believe they were the only writer, which
// is precisely the exclusion this package exists to provide. The rename is what the
// durability discipline requires and the lock is what the exclusion requires, so
// they cannot be the same file.
//
// The lock file is never removed for the same reason cmd/olivares' upgrade lock is
// never removed: unlinking it would break the exclusion it provides, because a
// waiter would be holding an inode that no longer has a name while a newcomer
// created a fresh one at the same path.
//
// # Ownership inside one process
//
// flock() locks are held on the open file DESCRIPTION, so two descriptors on the
// same inode conflict EVEN IN THE SAME PROCESS. A restore that held the exclusive
// lease and then opened the store would deadlock against itself, so this package
// keeps a process-wide registry of what it already holds and can satisfy a nested
// request from it rather than opening a second descriptor.
//
// ⛔ THAT NESTING IS EXPLICIT, AND UNTIL F3-IR-1 IT WAS NOT. This comment used to
// say the grant was justified "because this process demonstrably holds something
// stronger", and that sentence is the defect written down: a PROCESS holding
// something stronger is not the same thing as the CALLER being entitled to it. The
// registry granted every shared request whenever any holder existed, so an
// unrelated goroutine calling engine.Open was handed the console restore's own
// exclusive hold and published a Store across it — measured, through the real
// console guard and the real public constructor.
//
// What replaces it:
//
//   - A NEW request (TryAcquire) is judged as if it came from another process.
//     Shared coexists with shared; shared against an exclusive holder is BUSY, here
//     exactly as it is across a process boundary. An exclusive request over anything
//     already held is refused rather than upgraded, because a silent upgrade is how
//     lock order gets reversed.
//   - The legitimate nesting is Derive: it needs a LIVE parent lease, an exact
//     subset of that parent's anchors, and it stamps the child with the ROOT's mode
//     so a child of an exclusive restore can never be presented as an ordinary
//     publication token. Ownership is handed over, never inferred.
package opgate

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Format is the record format this build writes and the only one it reads. A
// record of any other format is REFUSED rather than ignored: an unreadable
// control is not an absent one.
const Format = 1

// The states a local control may carry. They mirror the durable operation states
// the ratified contract defines; every one of them except a complete control
// blocks publication.
const (
	StatePending       = "pending"
	StateIndeterminate = "indeterminate"
	StateQuarantined   = "quarantined"
	StateComplete      = "complete"
)

// Anchor kinds. A destination has at most two anchors — the data directory that
// holds its custody, and the store file when a SQLite DSN points outside it.
const (
	KindDataDir   = "data-dir"
	KindStoreFile = "store-file"
)

const (
	lockSuffix   = ".dr-control.lock"
	recordSuffix = ".dr-control"
	lockPerm     = 0o600
	recordPerm   = 0o600
)

// Errors this package returns. They are sentinels because callers branch on them:
// a busy anchor is a diagnosis, a self-held anchor is a programming error in the
// caller's lock order, and a symlinked anchor is a refusal.
var (
	// ErrSelfHeld reports that this PROCESS already holds the anchor and the
	// request cannot be satisfied from what it holds. It is never returned for a
	// shared request under an exclusive lease, which is the legitimate nesting.
	ErrSelfHeld = errors.New("opgate: this process already holds the anchor and the request would need a second, conflicting descriptor")
	// ErrNotExclusive reports a write attempted through a shared lease.
	ErrNotExclusive = errors.New("opgate: the durable record may only be written under an exclusive lease")
	// ErrForeignAnchor reports a read or write aimed at an anchor this lease does
	// not hold. It exists so a lease cannot be used as a general filesystem handle.
	ErrForeignAnchor = errors.New("opgate: this lease does not hold that anchor")
	// ErrSymlink reports that a control path is a symbolic link. A control that can
	// be redirected is not a control.
	ErrSymlink = errors.New("opgate: a restore control path is a symbolic link, which would let the destination be redirected")
	// ErrLeaseClosed reports an operation on a lease that has already been released.
	// A released lease is not a weaker capability; it is none.
	ErrLeaseClosed = errors.New("opgate: the lease has been released")
	// ErrExclusiveRoot reports an attempt to obtain an ordinary publication lease
	// from a chain rooted at an EXCLUSIVE hold. It is the anti-laundering refusal:
	// the operation that fenced a destination cannot issue permission to publish over
	// its own fence.
	ErrExclusiveRoot = errors.New("opgate: an ordinary publication lease cannot be derived from an exclusive root")
	// ErrLockNotRegular reports a lock path occupied by something that is not a
	// regular file: a FIFO, a device, a directory. It is a refusal and never a
	// repair — the thing that is there is left exactly where it is.
	ErrLockNotRegular = errors.New("opgate: the restore control lock path is not a regular file, and a lock that is not a file is not a lock")
	// ErrLockProvisioningRequired reports a destination whose stable coordination
	// lock file does not exist in a directory this process cannot write. It carries
	// the exact path and the offline action, because the alternative — skipping the
	// fence when the directory is read-only — is publication with no coordination at
	// all and no diagnosis.
	ErrLockProvisioningRequired = errors.New("opgate: the restore control lock file is missing and cannot be created here")
)

// Mode is what a caller asks of an anchor.
type Mode uint8

const (
	// ModeShared is what an ordinary publication decision takes: it coexists with
	// other readers and is refused while a restore holds the anchor.
	ModeShared Mode = iota + 1
	// ModeExclusive is what an operation that changes the destination takes.
	ModeExclusive
)

func (m Mode) String() string {
	switch m {
	case ModeShared:
		return "shared"
	case ModeExclusive:
		return "exclusive"
	default:
		return "invalid"
	}
}

// Anchor is one fenced destination path with its two files resolved.
//
// The canonical form is resolved through the PARENT directory, not the control
// files themselves: the files may not exist yet, and a caller that passed an alias
// (a symlinked mount, a relative path, a trailing slash) must land on the same
// registry key as one that passed the resolved path, or the same destination would
// be lockable twice.
type Anchor struct {
	kind       string
	canonical  string
	lockPath   string
	recordPath string
}

// Kind reports whether this anchor fences a data directory or a store file.
func (a Anchor) Kind() string { return a.kind }

// Canonical is the destination path this anchor fences, symlink-resolved.
func (a Anchor) Canonical() string { return a.canonical }

// LockPath and RecordPath are exported so a diagnostic can name the exact files an
// operator has to look at; nothing in this package takes them back as input.
func (a Anchor) LockPath() string   { return a.lockPath }
func (a Anchor) RecordPath() string { return a.recordPath }

// Zero reports an anchor that names nothing, which is what the constructors return
// for a destination that has no local control (an in-memory store, a data
// directory that does not exist).
func (a Anchor) Zero() bool { return a.lockPath == "" }

func (a Anchor) String() string {
	if a.Zero() {
		return "opgate: no anchor"
	}
	return a.kind + " " + a.canonical
}

// AnchorForDataDir resolves the control that fences a data directory's custody.
//
// present is false when the directory does not exist. That is not a degraded
// answer: a data directory that is not there holds no keys and no store, so there
// is nothing to fence and nothing this call should create. A read-only boot that
// finds nothing reports NotFound on its own.
//
// ⛔ THE ANCHOR IS ABSOLUTE, AND IT USED NOT TO BE. filepath.EvalSymlinks preserves
// the relativity of what it is given, so a caller that named `custody` got the
// literal key `custody/.dr-control.lock` — a key that means a DIFFERENT directory
// once the process changes its working directory, and that two different
// directories therefore SHARE. It was measured: a shared lease held over directory
// A's `custody` satisfied a later shared request made from directory B, out of the
// process registry, while a separate process held B's real lock exclusively. The
// registry entry, the durable record path and the diagnostic all named a path that
// resolves to whatever the cwd happens to be at the moment they are read.
//
// The working directory is therefore captured ONCE, here, before anything is
// resolved or keyed, exactly as a store file's destination is. A later chdir cannot
// move this anchor and cannot make another directory borrow its holder.
func AnchorForDataDir(dir string) (a Anchor, present bool, err error) {
	if strings.TrimSpace(dir) == "" {
		return Anchor{}, false, nil
	}
	abs, err := absoluteSpelling(dir)
	if err != nil {
		return Anchor{}, false, fmt.Errorf("opgate: resolve data dir %s: %w", dir, err)
	}
	info, err := os.Stat(abs)
	if errors.Is(err, os.ErrNotExist) {
		return Anchor{}, false, nil
	}
	if err != nil {
		return Anchor{}, false, fmt.Errorf("opgate: stat data dir %s: %w", abs, err)
	}
	if !info.IsDir() {
		return Anchor{}, false, fmt.Errorf("opgate: data dir %s is not a directory", abs)
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return Anchor{}, false, fmt.Errorf("opgate: resolve data dir %s: %w", abs, err)
	}
	if !filepath.IsAbs(canonical) {
		// EvalSymlinks returns an absolute path for an absolute input. If that ever
		// stopped being true, the anchor below would be a key another directory can
		// collide with, so this refuses rather than building it.
		return Anchor{}, false, fmt.Errorf("opgate: data dir %s resolved to the relative path %s, which cannot identify a custody anchor", abs, canonical)
	}
	return Anchor{
		kind:       KindDataDir,
		canonical:  canonical,
		lockPath:   filepath.Join(canonical, lockSuffix),
		recordPath: filepath.Join(canonical, recordSuffix),
	}, true, nil
}

// AnchorForStoreFile resolves the control that fences a SQLite store file.
//
// It is a PATH-ONLY constructor and takes no DSN: the one place a DSN becomes a
// destination is ResolveSQLiteTarget, and a second grammar here is exactly the
// duplication that let a fenced path and an opened path drift apart (F3-IR-2).
// Callers that hold a DSN go through the resolver; callers that hold a path — the
// console's promotion, which fences the file it is about to replace — come here,
// and both land on the same frozen identity because both call
// FreezeDestinationPath.
//
// present is false ONLY for the empty path. Every other unprovable input is a typed
// error: a missing parent directory, a dangling final symlink, a target that is not
// a regular file or one reachable under several names used to arrive here as
// present=false, and an absent anchor is an UNFENCED destination.
func AnchorForStoreFile(path string) (a Anchor, present bool, err error) {
	if strings.TrimSpace(path) == "" {
		return Anchor{}, false, nil
	}
	canonical, err := FreezeDestinationPath(path)
	if err != nil {
		return Anchor{}, false, err
	}
	return anchorAtResolvedPath(canonical), true, nil
}

// anchorAtResolvedPath builds the anchor of an ALREADY frozen path. It resolves
// nothing: everything that reaches it has been through FreezeDestinationPath, and
// resolving a second time is how two callers end up with two answers.
func anchorAtResolvedPath(canonical string) Anchor {
	return Anchor{
		kind:       KindStoreFile,
		canonical:  canonical,
		lockPath:   canonical + lockSuffix,
		recordPath: canonical + recordSuffix,
	}
}

// Destination is the exact identity a record is bound to. A control that does not
// name the destination it fences can be copied to another one and believed there.
type Destination struct {
	Engine           string `json:"engine"`
	CanonicalPath    string `json:"canonical_path"`
	SQLiteFile       string `json:"sqlite_file"`
	Database         string `json:"database"`
	Schema           string `json:"schema"`
	SystemIdentifier string `json:"system_identifier"`
}

// KeyFingerprint is one custody purpose's PUBLIC fingerprint and where the
// selection resolves from. No key bytes, no passphrase, no DSN.
type KeyFingerprint struct {
	Purpose      string `json:"purpose"`
	PublicSHA256 string `json:"public_sha256"`
	Source       string `json:"source"`
}

// Keyset is the custody generation a completed operation authorized. Keys is
// ordered by purpose so the digest over a record is stable.
type Keyset struct {
	Format int              `json:"format"`
	SHA256 string           `json:"sha256"`
	Keys   []KeyFingerprint `json:"keys"`
}

// Record is the durable local control. It carries METADATA only.
type Record struct {
	Format      int         `json:"format"`
	Revision    int64       `json:"revision"`
	OpID        string      `json:"op_id"`
	State       string      `json:"state"`
	Enrolled    bool        `json:"enrolled"`
	PlanSHA256  string      `json:"plan_sha256"`
	Destination Destination `json:"destination"`
	Keyset      Keyset      `json:"keyset"`
	JournalPath string      `json:"journal_path,omitempty"`
	ObservedAt  string      `json:"observed_at"`
}

// Blocks reports whether this record forbids publishing a store for its
// destination. Only a complete control does not.
func (r Record) Blocks() bool { return r.State != StateComplete }

// Validate proves a record is one this build wrote for THIS anchor. Every failure
// is a refusal to publish, never a fallback to "absent".
//
// The revision is validated rather than merely stored: it is the token a compare-
// and-set transition is judged against, and a record whose revision is zero or
// negative cannot be the predecessor of anything.
func (r Record) Validate(a Anchor) error {
	if r.Format != Format {
		return fmt.Errorf("opgate: record format %d is not the supported format %d", r.Format, Format)
	}
	if r.Revision <= 0 {
		return fmt.Errorf("opgate: record revision %d is not a compare-and-set predecessor", r.Revision)
	}
	if err := validOpID(r.OpID); err != nil {
		return err
	}
	switch r.State {
	case StatePending, StateIndeterminate, StateQuarantined, StateComplete:
	default:
		return fmt.Errorf("opgate: record state %q is not a supported state", r.State)
	}
	if err := validDigest("plan", r.PlanSHA256); err != nil {
		return err
	}
	if !r.Enrolled {
		return errors.New("opgate: a local control must explicitly name enrolled=true")
	}
	if err := r.Destination.validate(); err != nil {
		return err
	}
	if r.Destination.CanonicalPath == "" {
		return errors.New("opgate: record names no destination path")
	}
	if !a.Zero() && r.Destination.CanonicalPath != a.canonical {
		return fmt.Errorf("opgate: record is bound to destination %s and was found at %s, so it belongs to another destination",
			r.Destination.CanonicalPath, a.canonical)
	}
	if a.Kind() == KindStoreFile && (r.Destination.Engine != "sqlite" || r.Destination.SQLiteFile != a.Canonical()) {
		return errors.New("opgate: record does not name its frozen SQLite file target")
	}
	if strings.TrimSpace(r.ObservedAt) == "" {
		return errors.New("opgate: record carries no observation instant")
	}
	if _, err := time.Parse(time.RFC3339, r.ObservedAt); err != nil {
		return fmt.Errorf("opgate: record observation instant %q is not RFC3339: %w", r.ObservedAt, err)
	}
	if r.State == StateComplete {
		_, err := r.Keyset.Typed()
		return err
	}
	if !r.Keyset.empty() {
		return errors.New("opgate: a noncomplete record must carry an explicitly absent keyset")
	}
	return nil
}

func validOpID(id string) error {
	if len(id) != 32 {
		return fmt.Errorf("opgate: operation id %q is not 32 hex characters (128 bits)", id)
	}
	if _, err := hex.DecodeString(id); err != nil {
		return fmt.Errorf("opgate: operation id %q is not hexadecimal", id)
	}
	if strings.ToLower(id) != id {
		return fmt.Errorf("opgate: operation id %q is not lowercase", id)
	}
	return nil
}

func validDigest(label, digest string) error {
	if len(digest) != 64 {
		return fmt.Errorf("opgate: %s digest %q is not 64 hex characters", label, digest)
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return fmt.Errorf("opgate: %s digest %q is not hexadecimal", label, digest)
	}
	if strings.ToLower(digest) != digest {
		return fmt.Errorf("opgate: %s digest %q is not lowercase", label, digest)
	}
	return nil
}

// holder is one open lock descriptor this PROCESS owns, with the number of leases
// currently satisfied by it.
type holder struct {
	file *os.File
	mode Mode
	refs int
}

var (
	processMu   sync.Mutex
	processHeld = map[string]*holder{}
)

// Lease is a held set of anchors. It is not a capability to touch a destination:
// its only powers are reading and (when exclusive) replacing the durable record of
// the anchors it holds.
//
// # Root mode is provenance, and provenance is the authority
//
// A lease is either a ROOT — it took its own locks through TryAcquire — or a CHILD
// derived from a live root over an exact subset of its anchors. rootMode records
// which kind of root the chain started at, and a child cannot erase it.
//
// That field is the whole of F3-IR-1. The measured defect was that a restore's
// EXCLUSIVE hold satisfied any unrelated shared request in the same process, so an
// ordinary engine.Open published a Store while the real console restore held both
// anchors exclusively. Ownership is now something a caller must be HANDED — a live
// parent, a subset, a preserved root mode — and never something it can infer from
// the fact that some goroutine somewhere holds something stronger.
type Lease struct {
	mode     Mode
	rootMode Mode
	derived  bool
	anchors  []Anchor
	mu       sync.Mutex
	closed   bool
}

// Mode reports what this lease was granted.
func (l *Lease) Mode() Mode { return l.mode }

// RootMode reports the mode of the ROOT this lease descends from, which for a root
// lease is its own mode.
//
// An ordinary publication requires a SHARED root: a child derived under an exclusive
// restore is a private maintenance capability, and admitting it as an ordinary
// publication token would launder the restore's own exclusion into permission to
// publish over it.
func (l *Lease) RootMode() Mode { return l.rootMode }

// Derived reports whether this lease was derived from a parent rather than taking
// its own locks.
func (l *Lease) Derived() bool { return l.derived }

// Anchors reports the anchors this lease holds, in acquisition order.
func (l *Lease) Anchors() []Anchor {
	out := make([]Anchor, len(l.anchors))
	copy(out, l.anchors)
	return out
}

// TryAcquire takes every anchor ONCE, without blocking and without polling.
//
// ok=false means another holder has one of them. It is a diagnosis, not an error:
// the caller decides whether being fenced out is a refusal (an ordinary boot) or a
// message (a second restore).
//
// Anchors are acquired in canonical lock-path order so two callers naming the same
// pair in different orders cannot interleave into a cycle, and a partial
// acquisition is unwound in reverse.
func TryAcquire(mode Mode, anchors ...Anchor) (*Lease, bool, error) {
	switch mode {
	case ModeShared, ModeExclusive:
	default:
		return nil, false, fmt.Errorf("opgate: invalid mode %d", mode)
	}
	wanted := make([]Anchor, 0, len(anchors))
	seen := make(map[string]bool, len(anchors))
	for _, a := range anchors {
		if a.Zero() || seen[a.lockPath] {
			continue
		}
		seen[a.lockPath] = true
		wanted = append(wanted, a)
	}
	sort.Slice(wanted, func(i, j int) bool { return wanted[i].lockPath < wanted[j].lockPath })

	l := &Lease{mode: mode, rootMode: mode}
	for _, a := range wanted {
		ok, err := acquireOne(a, mode)
		if err != nil || !ok {
			_ = l.Release()
			return nil, false, err
		}
		l.anchors = append(l.anchors, a)
	}
	return l, true, nil
}

// acquireOne takes ONE anchor for a NEW, unrelated root request.
//
// ⛔ THE RULE THIS FUNCTION EXISTS TO STATE, and the one it used to get wrong:
// membership of this process is NOT ownership. A request arriving here has no
// parent lease, so it is judged exactly as a request from another process would be
// — genuine contention is BUSY, in this process or any other.
//
// Until F3-IR-1 the branch below granted EVERY shared request whenever any holder
// existed, exclusive holders included. An unrelated goroutine calling engine.Open
// was therefore handed the console restore's own exclusion and published a Store
// across it. Shared/shared coexistence is kept — several ordinary boots of one
// installation are a normal posture, and the kernel would grant them anyway — and
// the legitimate nesting a restore needs is served by Derive, which demands a live
// parent rather than inferring one.
func acquireOne(a Anchor, mode Mode) (bool, error) {
	processMu.Lock()
	defer processMu.Unlock()
	if h, ok := processHeld[a.lockPath]; ok {
		switch {
		case mode == ModeShared && h.mode == ModeShared:
			// Two readers. flock() would grant this to a second descriptor as well;
			// sharing the one this process already has costs one fewer descriptor and
			// makes the release refcounted rather than order-dependent.
			h.refs++
			return true, nil
		case mode == ModeShared:
			// An exclusive holder. BUSY, not an error and not a grant: being fenced out
			// by a restore is a diagnosis, and it is the same diagnosis whether the
			// restore runs here or in another process.
			return false, nil
		default:
			return false, fmt.Errorf("%w: %s is held %s by this process", ErrSelfHeld, a, h.mode)
		}
	}
	f, err := openLockFile(a, mode)
	if err != nil {
		return false, err
	}
	ok, err := flockTry(f, mode == ModeExclusive)
	if err != nil || !ok {
		_ = f.Close()
		return false, err
	}
	if err := verifyLockIdentity(a, f); err != nil {
		_ = f.Close()
		return false, err
	}
	processHeld[a.lockPath] = &holder{file: f, mode: mode, refs: 1}
	return true, nil
}

// Derive returns a CHILD lease over an exact subset of this lease's anchors,
// preserving the root's mode as provenance.
//
// This is the only nesting this package performs, and every word of it is load
// bearing:
//
//   - a LIVE parent. A released parent derives nothing; the capability died with it.
//   - an EXACT SUBSET. A child may narrow, never grow: a nested operation that needs
//     an anchor its parent does not hold must start over with the whole set, because
//     acquiring the extra one here would take a lock outside the sorted order the
//     parent established and that is how two callers deadlock.
//   - NO UPGRADE. A child is shared. There is no path from a shared root to an
//     exclusive child, because an exclusive child would have to take a real second
//     lock the kernel would refuse.
//   - PRESERVED ROOT MODE. A child of an exclusive restore stays marked as one, so
//     the ordinary publication path can refuse it. See RootMode.
//
// The child pins the parent's file descriptions by reference count, so closing the
// PARENT does not unlock the kernel fence beneath a child that is still open — it
// only stops new children being made.
func (l *Lease) Derive(anchors ...Anchor) (*Lease, error) {
	if l == nil {
		return nil, errors.New("opgate: a nil lease derives nothing")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, fmt.Errorf("%w: the parent lease has been released, so it can no longer carry ownership", ErrLeaseClosed)
	}
	wanted := make([]Anchor, 0, len(anchors))
	seen := make(map[string]bool, len(anchors))
	for _, a := range anchors {
		if a.Zero() || seen[a.lockPath] {
			continue
		}
		if !l.holdsLocked(a) {
			return nil, fmt.Errorf("%w: %s, and a derived lease may narrow its parent's anchors but never grow them", ErrForeignAnchor, a)
		}
		seen[a.lockPath] = true
		wanted = append(wanted, a)
	}
	if len(wanted) == 0 {
		// A child holding nothing would still LOOK like ownership to whoever received
		// it, which is the exact confusion this type exists to remove.
		return nil, fmt.Errorf("%w: a derived lease must name at least one anchor its parent holds", ErrForeignAnchor)
	}
	sort.Slice(wanted, func(i, j int) bool { return wanted[i].lockPath < wanted[j].lockPath })

	child := &Lease{mode: ModeShared, rootMode: l.rootMode, derived: true}
	processMu.Lock()
	defer processMu.Unlock()
	for _, a := range wanted {
		h, ok := processHeld[a.lockPath]
		if !ok {
			// The parent believes it holds this anchor and the registry does not. That
			// is a broken invariant, not contention, so it is an error rather than busy
			// — and the references taken on the way here are given back in reverse.
			unwindRefsLocked(child.anchors)
			return nil, fmt.Errorf("opgate: the parent lease names %s but this process no longer holds it", a)
		}
		h.refs++
		child.anchors = append(child.anchors, a)
	}
	return child, nil
}

// unwindRefsLocked gives back the references a partial derivation took, in reverse.
// processMu must be held.
func unwindRefsLocked(anchors []Anchor) {
	for i := len(anchors) - 1; i >= 0; i-- {
		if h, ok := processHeld[anchors[i].lockPath]; ok {
			h.refs--
		}
	}
}

// DeriveShared is Derive restricted to a SHARED root: the only child an ordinary
// publication may be admitted on.
//
// The restriction is the anti-laundering rule. A restore holds its destination
// exclusively and legitimately needs read access to the control underneath it, so
// Derive stays available to it — but the value it gets back carries an exclusive
// root mode, and this constructor is what the ordinary path uses so that value can
// never be mistaken for an ordinary publication token.
func (l *Lease) DeriveShared(anchors ...Anchor) (*Lease, error) {
	if l == nil {
		return nil, errors.New("opgate: a nil lease derives nothing")
	}
	if l.RootMode() != ModeShared {
		return nil, fmt.Errorf(
			"%w: this lease descends from an %s root, and an operation that holds the destination exclusively cannot hand out an ordinary publication lease over its own exclusion",
			ErrExclusiveRoot, l.RootMode())
	}
	return l.Derive(anchors...)
}

// openLockFile opens the anchor's stable inode.
//
// ⛔ IT NO LONGER OPENS WITH O_CREATE|O_RDWR UNCONDITIONALLY, and that single flag
// set was F3-IR-7. A remote-PostgreSQL installation whose already-installed signing
// keys live in a NON-WRITABLE directory booted fine before this control existed and
// was refused afterwards — at lock CREATION, on a deployment that needed no local
// writes at all. The author's argument (a SQLite data directory must be writable
// for WAL anyway) is true and does not reach that deployment.
//
// The order is therefore: OPEN AN EXISTING FILE FIRST, and only provision when the
// file is genuinely absent.
//
//  1. An existing lock is opened O_RDONLY. flock(2) places a shared OR exclusive
//     lock regardless of the mode the descriptor was opened in, so read access is
//     sufficient for both modes and it is the access a read-only custody directory
//     can still give. (The durable RECORD is a separate file, and writing it needs a
//     writable directory — which is exactly why only an exclusive restore writes it.)
//  2. An absent lock is provisioned through provisionLockFile when the directory
//     allows it: O_CREATE|O_EXCL, 0600, fsync of the file and of the directory.
//  3. An absent lock the directory does NOT allow is a NAMED refusal that carries
//     the exact path and the offline action. It is never a skipped fence, never a
//     fallback to another lock location, and permission-denied is never reported as
//     absence.
//
// Nothing here truncates, chmod-repairs, replaces or unlinks an existing file. The
// inode is the exclusion; repairing it would break it.
func openLockFile(a Anchor, mode Mode) (*os.File, error) {
	if err := requireRegularLockPath(a.lockPath); err != nil {
		return nil, err
	}
	if err := refuseSymlink(a.recordPath); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(a.lockPath, os.O_RDONLY|openNoFollow|openNonBlock, 0)
	if err == nil {
		if verr := verifyOpenedLockIsRegular(a, f); verr != nil {
			_ = f.Close()
			return nil, verr
		}
		return f, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		// EACCES, ELOOP, EROFS on an existing file: a lock this process cannot open
		// is a lock it cannot honor. Reporting it as absence is how a fence turns
		// itself off.
		return nil, fmt.Errorf("opgate: open the restore control lock %s (%s): %w", a.lockPath, mode, err)
	}
	if perr := provisionLockFile(a); perr != nil {
		return nil, perr
	}
	f, err = os.OpenFile(a.lockPath, os.O_RDONLY|openNoFollow|openNonBlock, 0)
	if err != nil {
		return nil, fmt.Errorf("opgate: open the restore control lock %s after provisioning it: %w", a.lockPath, err)
	}
	if verr := verifyOpenedLockIsRegular(a, f); verr != nil {
		_ = f.Close()
		return nil, verr
	}
	return f, nil
}

// requireRegularLockPath refuses a lock path occupied by anything that is not a
// regular file, BEFORE the open.
//
// ⛔ IT IS ORDERED THIS WAY BECAUSE THE OPEN CAN BLOCK, and blocking here blocks
// everything: acquireOne holds the process registry mutex across it, so a single
// bad path stalls every other acquisition and release in the process. Measured with
// a real FIFO at the lock path: `open(O_RDONLY)` on a FIFO waits for a writer that
// never comes, so a child process printed its readiness marker and never returned,
// and the identity check that would have rejected the FIFO — which runs AFTER the
// open and after flock — was never reached.
//
// Lstat cannot block on any file type, so the diagnosis is taken from it. The open
// itself additionally carries O_NONBLOCK (see openNonBlock), which closes the
// window between this check and the open for a path replaced in between: with it,
// a FIFO or a device opens immediately and is rejected by
// verifyOpenedLockIsRegular instead of waiting.
func requireRegularLockPath(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("opgate: inspect %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s", ErrSymlink, path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is a %s", ErrLockNotRegular, path, describeFileType(info.Mode()))
	}
	return nil
}

// verifyOpenedLockIsRegular re-checks the DESCRIPTOR, so a path swapped between the
// check above and the open is refused rather than locked. It runs before flock
// because a lock taken on a FIFO or a device is not a lock on a coordination file.
func verifyOpenedLockIsRegular(a Anchor, f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("opgate: inspect the opened restore control lock %s: %w", a.lockPath, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is a %s", ErrLockNotRegular, a.lockPath, describeFileType(info.Mode()))
	}
	return nil
}

// describeFileType names what is at a path in words an operator can act on. The
// zero Type() of a regular file prints as "---------", which reads like a mode and
// says nothing.
func describeFileType(mode os.FileMode) string {
	switch {
	case mode.IsDir():
		return "directory"
	case mode&os.ModeNamedPipe != 0:
		return "named pipe (FIFO)"
	case mode&os.ModeSocket != 0:
		return "socket"
	case mode&os.ModeDevice != 0 && mode&os.ModeCharDevice != 0:
		return "character device"
	case mode&os.ModeDevice != 0:
		return "block device"
	case mode&os.ModeSymlink != 0:
		return "symbolic link"
	case mode.IsRegular():
		return "regular file"
	default:
		return "not a regular file (" + mode.Type().String() + ")"
	}
}

// ProvisionLock creates an anchor's stable coordination lock file, and nothing else.
//
// This is the concrete operation the read-only rollout prerequisite names. It is
// deliberately the SMALLEST thing that closes the prerequisite:
//
//   - it creates only the 0600 regular LOCK file, never a durable control record,
//   - it uses exclusive creation, then fsyncs the file and its directory, so a crash
//     cannot leave a name without an inode,
//   - an existing file is VERIFIED and left alone — never truncated, never
//     chmod-repaired into acceptance, never replaced or unlinked,
//   - it repairs no other state, and it makes no claim about a destination it did
//     not create the lock for.
//
// It is run BEFORE a custody directory becomes read-only. On a directory that is
// already read-only it fails with ErrLockProvisioningRequired, which is the honest
// answer: an offline step is required, and this call is what that step runs.
func ProvisionLock(a Anchor) error {
	if a.Zero() {
		return errors.New("opgate: there is no anchor to provision a lock for")
	}
	return provisionLockFile(a)
}

func provisionLockFile(a Anchor) error {
	if err := requireRegularLockPath(a.lockPath); err != nil {
		return err
	}
	f, err := os.OpenFile(a.lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY|openNoFollow, lockPerm)
	switch {
	case err == nil:
		// fsync the empty file and then its directory: the lock's value is that its
		// INODE survives, and a name whose inode was never made durable is a name a
		// crash can turn into a second, different lock.
		syncErr := f.Sync()
		closeErr := f.Close()
		if syncErr != nil {
			return fmt.Errorf("opgate: make the restore control lock %s durable: %w", a.lockPath, syncErr)
		}
		if closeErr != nil {
			return fmt.Errorf("opgate: close the new restore control lock %s: %w", a.lockPath, closeErr)
		}
		return fsyncDir(filepath.Dir(a.lockPath))
	case errors.Is(err, os.ErrExist):
		// Another process provisioned it between our open and this one. That is the
		// intended outcome, not a race to repair: verify and accept.
		return verifyExistingLockFile(a)
	default:
		// ⛔ THE COMMAND IN THIS MESSAGE USED TO BE `install -m 0600 /dev/null <lock>`,
		// and it contradicted every promise around it: `install` opens the destination
		// O_CREAT|O_TRUNC and falls back to unlinking and recreating it, so on an
		// EXISTING lock it resets the mode and can give the path a new inode while a
		// holder is still fencing on the old one. The operator-facing diagnostic is
		// the one place this has to be right, because it is read exactly when somebody
		// is about to type it. The form below creates with O_EXCL (the shell's
		// noclobber) and fails rather than touching anything already there — the same
		// promise ProvisionLock keeps, and the one docs/DR-RUNBOOK.md §9.6 documents.
		//
		// ⛔ AND THE PATHNAME IS A QUOTED LITERAL, WHICH IT WAS NOT. See
		// shellSingleQuote: substituting the path raw made a custody directory with
		// a space in its name produce a command that created a DIFFERENT file and
		// still exited 0.
		quoted := shellSingleQuote(a.lockPath)
		return fmt.Errorf(
			"%w: %s. The directory does not permit creating it (%v). Provision it OFFLINE, before the custody directory is made read-only, as the account that owns the data directory: `[ -e %s ] || (umask 077; set -C; : > %s)` (or run this installation once with the directory writable; see docs/DR-RUNBOOK.md section 9.6). That form creates the file and NEVER truncates, replaces or re-modes an existing one. Nothing here falls back to another lock location, and nothing here treats a permission error as an absent control",
			ErrLockProvisioningRequired, a.lockPath, err, quoted, quoted)
	}
}

// shellSingleQuote renders a pathname as ONE literal POSIX shell word.
//
// ⛔ A COMMAND IN A DIAGNOSTIC IS EXECUTED, NOT READ, and this one used to
// interpolate the path raw. Measured by the independent review on a custody
// directory named, validly, `custody space`:
//
//	[ -e /base/custody space/.dr-control.lock ] || (umask 077; set -C; : > /base/custody space/.dr-control.lock)
//
// The test operand splits into two words, so `[` fails with `unexpected
// operator`; the redirection then applies to the FIRST word alone, so the command
// CREATES `/base/custody`, leaves the real lock absent — and EXITS 0. An operator
// who copies the instruction the product itself gave them gets a success, a file
// nobody asked for, and a node that still will not boot. The lock is missing, so
// the fence is not weakened; what is broken is the only instruction the operator
// has.
//
// Single quotes rather than escaping, because inside them EVERY byte but a single
// quote is literal to any POSIX shell: a space, a dollar, a backtick, `;`, `&`,
// `|`, a glob. The one exception is closed the documented way — end the quoted
// run, emit an escaped quote, reopen it:
//
//	a'b   becomes   'a'\''b'
//
// so a path carrying a quote stays one inert operand instead of ending the word.
// A NUL cannot occur here: a pathname cannot contain one.
//
// This does NOT change what the command does. Create-only through the shell's
// noclobber, 0600 through the umask, no truncation and no replacement of an
// existing inode are exactly as they were; only the pathname's word boundaries
// are now the ones the operator meant.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// verifyExistingLockFile proves an already-present lock is one this build may use.
// It reads metadata only; it never modifies the file.
func verifyExistingLockFile(a Anchor) error {
	info, err := os.Lstat(a.lockPath)
	if err != nil {
		return fmt.Errorf("opgate: inspect the existing restore control lock %s: %w", a.lockPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s", ErrSymlink, a.lockPath)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is a %s", ErrLockNotRegular, a.lockPath, describeFileType(info.Mode()))
	}
	return nil
}

// verifyLockIdentity re-checks, AFTER the lock has been taken, that the descriptor
// holding it is still the file at that path.
//
// It detects an observed replacement between the path lookup and the acquisition —
// which is a real interleaving and worth refusing. It is NOT a claim of isolation
// from whoever owns the filesystem: an owner can replace the file at any later
// instant, and no descriptor check would see it. Nothing is unlinked or repaired
// here; a mismatch is a refusal and the live lock is left exactly where it is.
func verifyLockIdentity(a Anchor, f *os.File) error {
	held, err := f.Stat()
	if err != nil {
		return fmt.Errorf("opgate: inspect the held restore control lock %s: %w", a.lockPath, err)
	}
	if !held.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is a %s", ErrLockNotRegular, a.lockPath, describeFileType(held.Mode()))
	}
	named, err := os.Lstat(a.lockPath)
	if err != nil {
		return fmt.Errorf("opgate: re-inspect the restore control lock %s after taking it: %w", a.lockPath, err)
	}
	if !os.SameFile(held, named) {
		return fmt.Errorf(
			"opgate: the restore control lock at %s was replaced while it was being taken, so the lock now held fences an inode that path no longer names",
			a.lockPath)
	}
	return nil
}

func refuseSymlink(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("opgate: inspect %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s", ErrSymlink, path)
	}
	return nil
}

// Release gives every anchor back. It is idempotent and safe to defer.
//
// It gives back this lease's REFERENCES, not the kernel lock: the descriptor is
// closed only when the last reference to it goes. That is what makes closing a
// parent while a child is still open safe — the parent stops being able to derive,
// and the fence underneath the child stays exactly where it was until the child
// closes too.
//
// Lock order, in one place so nobody has to derive it: the LEASE mutex is taken
// first and the process registry second, here and in Derive. Nothing blocks while
// holding the registry.
func (l *Lease) Release() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	var firstErr error
	processMu.Lock()
	defer processMu.Unlock()
	for i := len(l.anchors) - 1; i >= 0; i-- {
		a := l.anchors[i]
		h, ok := processHeld[a.lockPath]
		if !ok {
			continue
		}
		h.refs--
		if h.refs > 0 {
			continue
		}
		delete(processHeld, a.lockPath)
		// Closing the descriptor releases the flock; the FILE stays, because
		// unlinking it would break the exclusion for everyone waiting on that inode.
		if err := h.file.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("opgate: release the restore control lock %s: %w", a.lockPath, err)
		}
	}
	l.anchors = nil
	return firstErr
}

// Released reports whether this lease has been given back. A caller that has to
// prove a lease is still live before acting on it asks here rather than keeping its
// own boolean, which would be a copy that can disagree.
func (l *Lease) Released() bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

func (l *Lease) holdsLocked(a Anchor) bool {
	for _, held := range l.anchors {
		if held.lockPath == a.lockPath {
			return true
		}
	}
	return false
}

// Read returns the durable record of one held anchor.
//
// present=false means the record file is not there. A record that exists and
// cannot be parsed or validated is an ERROR, never an absence: "I could not read
// the control" and "there is no control" authorize completely different actions.
func (l *Lease) Read(a Anchor) (Record, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return Record{}, false, errors.New("opgate: the lease is released")
	}
	if !l.holdsLocked(a) {
		return Record{}, false, fmt.Errorf("%w: %s", ErrForeignAnchor, a)
	}
	raw, present, err := readRecordFile(a.recordPath)
	if err != nil {
		return Record{}, present, fmt.Errorf("opgate: read the restore control %s: %w", a.recordPath, err)
	}
	if !present {
		return Record{}, false, nil
	}
	rec, err := decodeRecord(raw)
	if err != nil {
		return Record{}, true, fmt.Errorf("opgate: the restore control %s is not a record this build can read: %w", a.recordPath, err)
	}
	if err := rec.Validate(a); err != nil {
		return Record{}, true, err
	}
	return rec, true, nil
}

// Commit replaces the durable record of one held anchor.
//
// temp(O_EXCL) → write → fsync(file) → rename → fsync(directory). The rename gives
// the record a NEW inode every time, which is exactly why the lock lives on a
// different file: see the package comment.
func (l *Lease) Commit(a Anchor, rec Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errors.New("opgate: the lease is released")
	}
	if l.mode != ModeExclusive {
		return ErrNotExclusive
	}
	if !l.holdsLocked(a) {
		return fmt.Errorf("%w: %s", ErrForeignAnchor, a)
	}
	if rec.Format == 0 {
		rec.Format = Format
	}
	if err := rec.Validate(a); err != nil {
		return err
	}
	if _, err := regularRecordPath(a.recordPath); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("opgate: render the restore control: %w", err)
	}
	payload = append(payload, '\n')

	dir := filepath.Dir(a.recordPath)
	tmp, err := os.CreateTemp(dir, filepath.Base(a.recordPath)+".tmp-*")
	if err != nil {
		return fmt.Errorf("opgate: stage the restore control in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmpName) }
	if err := tmp.Chmod(recordPerm); err != nil {
		cleanup()
		return fmt.Errorf("opgate: restrict the staged restore control: %w", err)
	}
	if _, err := tmp.Write(payload); err != nil {
		cleanup()
		return fmt.Errorf("opgate: write the staged restore control: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("opgate: sync the staged restore control: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("opgate: close the staged restore control: %w", err)
	}
	if err := os.Rename(tmpName, a.recordPath); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("opgate: publish the restore control %s: %w", a.recordPath, err)
	}
	return fsyncDir(dir)
}

// fsyncDir makes the rename itself durable. Without it the record can survive a
// crash while its NAME does not, which is the same as having lost the control.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("opgate: open %s to make the restore control durable: %w", dir, err)
	}
	defer d.Close() //nolint:errcheck // the sync below carries the diagnosis
	if err := d.Sync(); err != nil {
		return fmt.Errorf("opgate: sync %s to make the restore control durable: %w", dir, err)
	}
	return nil
}
