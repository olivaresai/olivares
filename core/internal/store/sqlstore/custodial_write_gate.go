// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// custodial_write_gate.go (P2 / W1) — the ONE choke point every consumer write
// on a tenant Scope passes through, and the three-phase custody state it feeds.
//
// The gate exists because the custodial claim order is only meaningful if the
// transaction around it is known: a claim that anchors an operation must not
// commit alongside unrelated consumer writes it never inspected, and nothing at
// all may be written after the operation's identity has been decided. Both
// rules are impossible to enforce by convention — the Scope hands out thirty-odd
// repositories, an audit log, an access-evidence store and a generic Ext — so
// they are enforced where the SQL is issued instead.
//
// The origin is an UNEXPORTED value and no method takes it from a caller:
//
//   - ordinary is the default and the only origin a module can cause;
//   - authority_touch is set by the engine alone, on the leased-authority
//     touch of applyAuthoritySnapshot, whose input is the engine's OWN locked
//     record (scope.go). It is the only write the custodial contract admits
//     before a binding, because a DAB or bundle locker legitimately renews the
//     facts a managed Stop is authorized against;
//   - custody is set by the custodial handle alone, inside its own methods.
//
// A module therefore cannot name an origin, and cannot acquire one by wrapping,
// embedding or asserting: the field lives on the engine's private genericRepo
// and auditLog values.

// writeOrigin attributes a write to the engine role that caused it.
type writeOrigin uint8

const (
	// originOrdinary is every consumer write. It is the zero value on purpose:
	// a repository built by a path that forgets to choose is ordinary, which is
	// the restrictive answer.
	originOrdinary writeOrigin = iota
	// originAuthorityTouch is the engine's own leased-authority record touch.
	originAuthorityTouch
	// originCustody is a write issued by a bound custodial handle.
	originCustody
)

// String renders the origin for refusal messages.
func (o writeOrigin) String() string {
	switch o {
	case originAuthorityTouch:
		return "authority_touch"
	case originCustody:
		return "custody"
	default:
		return "ordinary"
	}
}

// scopeWriteOp distinguishes a durable row change from a lock that changes no
// row. The distinction matters in exactly one place: a lock is admitted while
// the scope is restricted (the custodial append takes the tenant audit lock
// itself, and a caller may hold a transaction lock across the binding) and
// refused once the scope is sealed, because after the decision the callback's
// only permitted action is to return.
type scopeWriteOp uint8

const (
	scopeRowWrite scopeWriteOp = iota
	scopeRowLock
)

// custodyPhase is the scope's position in the custodial order.
type custodyPhase uint8

const (
	// custodyOpen is every ordinary transaction, bound or not yet bound. The
	// gate only COUNTS here; it refuses nothing.
	custodyOpen custodyPhase = iota
	// custodyRestricted begins when a Claim or Settle binding is admitted. Only
	// custody writes pass.
	custodyRestricted
	// custodySealed begins when Claim or Settle returns, with a result or an
	// error. Nothing passes.
	custodySealed
)

// guardScopeWrite is the choke point. It returns the refusal AND poisons the
// transaction, so a callback that swallows the error still cannot commit: the
// poison is checked by Mutate before the directory and lineage epilogues run.
func (sc *tenantScope) guardScopeWrite(
	op scopeWriteOp, origin writeOrigin, kind model.Kind, id model.ID,
) error {
	sc.custodyMu.Lock()
	defer sc.custodyMu.Unlock()
	switch sc.custodyPhase {
	case custodyOpen:
		// Nothing is refused before a binding. The count is the ONLY state an
		// ordinary transaction touches here, and nothing reads it unless a
		// binding is later attempted.
		if op == scopeRowWrite && origin != originAuthorityTouch {
			sc.custodyForeign++
		}
		return nil
	case custodyRestricted:
		if origin == originCustody || op == scopeRowLock {
			return nil
		}
		return sc.poisonCustodyLocked(fmt.Errorf(
			"%w: %s %s on %s %s inside a bound custodial transaction",
			store.ErrCustodyWriteSet, origin, scopeOpName(op), kind, id))
	case custodySealed:
		return sc.poisonCustodyLocked(fmt.Errorf(
			"%w: %s %s on %s %s after the operation was decided",
			store.ErrCustodySealed, origin, scopeOpName(op), kind, id))
	}
	// An unrecognized phase is a programming error, and the restrictive answer is
	// the only safe one: refuse and poison rather than fall through to a write.
	return sc.poisonCustodyLocked(fmt.Errorf(
		"%w: %s %s on %s %s under an unknown custodial phase %d",
		store.ErrCustodySealed, origin, scopeOpName(op), kind, id, sc.custodyPhase))
}

func scopeOpName(op scopeWriteOp) string {
	if op == scopeRowLock {
		return "lock"
	}
	return "write"
}

// poisonCustodyLocked records the first custodial fault and propagates it to the
// directory and lineage trackers, which are the two epilogues Mutate runs before
// Commit. Recording it in three places is deliberate redundancy: the explicit
// check in Mutate names the custodial cause, and the trackers refuse to finish
// even if a future caller reaches Commit by another route.
func (sc *tenantScope) poisonCustodyLocked(err error) error {
	if err == nil {
		return nil
	}
	if sc.custodyPoisoned == nil {
		sc.custodyPoisoned = err
	}
	if sc.lineageWriter != nil {
		sc.lineageWriter.poison(err)
	}
	if sc.directoryWriter != nil {
		sc.directoryWriter.poison(err)
	}
	return err
}

// poisonCustody is poisonCustodyLocked for callers that do not hold the lock.
func (sc *tenantScope) poisonCustody(err error) error {
	sc.custodyMu.Lock()
	defer sc.custodyMu.Unlock()
	return sc.poisonCustodyLocked(err)
}

// custodyFault returns the recorded custodial fault, or nil.
func (sc *tenantScope) custodyFault() error {
	sc.custodyMu.Lock()
	defer sc.custodyMu.Unlock()
	return sc.custodyPoisoned
}

// finish marks the scope's transaction as over. A handle used afterwards
// refuses with ErrCustodyHandleStale instead of issuing SQL on a finished
// *sql.Tx, whose driver error would classify as an availability fault.
func (sc *tenantScope) finish() {
	sc.custodyMu.Lock()
	defer sc.custodyMu.Unlock()
	sc.finished = true
}

// live reports whether the scope's transaction is still open.
func (sc *tenantScope) live() bool {
	sc.custodyMu.Lock()
	defer sc.custodyMu.Unlock()
	return !sc.finished
}

// reserveCustodyBinding admits at most ONE binding per transaction and, for the
// writing modes, requires that every write recorded so far was an engine
// authority touch. It returns the refusal already poisoned where the contract
// says so.
func (sc *tenantScope) reserveCustodyBinding(mode store.CustodialMode) error {
	sc.custodyMu.Lock()
	defer sc.custodyMu.Unlock()
	if sc.finished {
		return fmt.Errorf("%w: the transaction has finished", store.ErrCustodyHandleStale)
	}
	if sc.custodyPoisoned != nil {
		return sc.custodyPoisoned
	}
	if sc.custodyBound {
		return sc.poisonCustodyLocked(fmt.Errorf(
			"%w: one custodial effect per transaction", store.ErrCustodyAlreadyBound))
	}
	if mode != store.CustodialLookup && sc.custodyForeign > 0 {
		return sc.poisonCustodyLocked(fmt.Errorf(
			"%w: %d write(s) other than an engine authority touch preceded the binding",
			store.ErrCustodyWriteSet, sc.custodyForeign))
	}
	sc.custodyBound = true
	if mode != store.CustodialLookup {
		sc.custodyPhase = custodyRestricted
	}
	return nil
}

// sealCustody closes the scope to every further write, lock, append and journal
// call. It runs from a deferred call in Claim and Settle, so it seals on the
// error paths exactly as on the success path.
func (sc *tenantScope) sealCustody() {
	sc.custodyMu.Lock()
	defer sc.custodyMu.Unlock()
	sc.custodyPhase = custodySealed
}

// custodyRepo returns a tenant-pinned repository whose writes carry the custody
// origin. It is unexported and reachable only from the handle.
func (sc *tenantScope) custodyRepo(desc model.EntityDescriptor) *genericRepo {
	r := sc.repo(desc)
	r.origin = originCustody
	return r
}

// authorityTouchRepo returns a tenant-pinned repository whose writes carry the
// engine's authority-touch origin. Its ONLY call site is the leased-fact touch
// of applyAuthoritySnapshot, whose input is the engine's own locked record.
func (sc *tenantScope) authorityTouchRepo(desc model.EntityDescriptor) *genericRepo {
	r := sc.repo(desc)
	r.origin = originAuthorityTouch
	return r
}

// noteWrite is the genericRepo half of the choke point. Every durable row change
// this repository issues — including the three statements the resource tree and
// the access-edge upsert build by hand rather than through updateAt — calls it
// before the statement runs.
func (r *genericRepo) noteWrite(id model.ID) error {
	if r.writeGuard == nil {
		return nil
	}
	return r.writeGuard(scopeRowWrite, r.origin, r.desc.Kind, id)
}

// noteLock is noteWrite for a row lock, which changes no row.
func (r *genericRepo) noteLock(id model.ID) error {
	if r.writeGuard == nil {
		return nil
	}
	return r.writeGuard(scopeRowLock, r.origin, r.desc.Kind, id)
}

// auditWriteKind names the ledger in a refusal message. The audit relation has
// no descriptor (it is not a tenant entity), so the kind is the conventional
// spelling the API layer already uses for it.
const auditWriteKind model.Kind = "core.audit_event"

// noteWrite and noteLock are the auditLog half of the choke point.
func (a *auditLog) noteWrite() error {
	if a.writeGuard == nil {
		return nil
	}
	return a.writeGuard(scopeRowWrite, a.origin, auditWriteKind, "")
}

func (a *auditLog) noteLock() error {
	if a.writeGuard == nil {
		return nil
	}
	return a.writeGuard(scopeRowLock, a.origin, auditWriteKind, "")
}

// withCustodyOrigin runs fn with this log's appends attributed to the custodial
// handle. The scope hands out ONE auditLog per transaction so that a claim's
// ledger event and its row change ride one chain head; the origin is therefore
// a property of the CALL, restored on return. The scope is used sequentially,
// like every other repository it issues.
func (a *auditLog) withCustodyOrigin(fn func() error) error {
	previous := a.origin
	a.origin = originCustody
	defer func() { a.origin = previous }()
	return fn()
}

// The two mandatory epilogues Mutate runs after the callback and before Commit.
// They are named so a refusal says WHICH one failed, and so the in-package test
// seam can fail exactly one of them.
const (
	epilogueDirectory = "directory"
	epilogueLineage   = "lineage"
)

// finishEpilogue runs one mandatory epilogue and, in this package's tests only,
// consults the fault seam at the exact boundary where Mutate consumes the
// result. In production the seam is nil and this is the bare call.
//
// The epilogues are deliberately NOT consumer writes: they issue their own
// internal SQL and do not pass through the write gate, so they still run after a
// custodial effect has sealed the scope. What must hold is that their failure
// returns BEFORE Commit — a known rollback with no dispatch — and that is what
// this shape keeps true for both of them.
func (sc *tenantScope) finishEpilogue(
	ctx context.Context, name string, finish func(context.Context) error,
) error {
	if err := finish(ctx); err != nil {
		return err
	}
	if sc.s.custodyEpilogueTestHook == nil {
		return nil
	}
	return sc.s.custodyEpilogueTestHook(name)
}
