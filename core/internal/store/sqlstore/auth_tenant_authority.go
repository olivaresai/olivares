// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Only the concrete auth view implements the barrier. The tenantScope the
// callback is built over, ordinary workspace scopes and the read view do not
// acquire it, so a consumer cannot reach it from a business-tenant Mutate.
var _ store.AuthTenantAuthorityBarrier = (*authScope)(nil)

var (
	// errAuthTenantAuthorityRepeated refuses a second acquisition in one
	// transaction. It is deliberately distinct from the bundle method's own
	// message: this one fires BEFORE any lock is taken.
	errAuthTenantAuthorityRepeated = errors.New(
		"sqlstore: auth tenant authority is already acquired in this transaction",
	)
	// errAuthTenantAuthorityOrder refuses the inverse orders the tracker can
	// actually observe: a directory writer that some earlier wrapper already
	// used, a recorded audit-before-directory sequence, and a tenant authority
	// acquisition that ran first.
	errAuthTenantAuthorityOrder = errors.New(
		"sqlstore: auth tenant authority must precede directory admission, authority and audit locks",
	)
)

// authTenantAuthorityUserBudget bounds the SUPPLIED User references of this
// interface only, before equal duplicates are removed. The existing tenant-scope
// bundle deliberately keeps its larger acceptance; narrowing it here would change
// a ratified contract that other callers already rely on.
const authTenantAuthorityUserBudget = 64

// Narrow, unexported fault seams. They are nil in every build that does not set
// them from a test in this package: there is no runtime configuration, no
// exported symbol and no way for a consumer to reach them.
var (
	// authTenantAuthorityEntryReadTestHook fails the ENTRY presentation read.
	authTenantAuthorityEntryReadTestHook func() error
	// authTenantAuthorityBindTestHook fails the borrow bind before it happens.
	authTenantAuthorityBindTestHook func() error
	// authTenantAuthorityBorrowedTestHook runs inside the borrowed interval.
	authTenantAuthorityBorrowedTestHook func(context.Context) error
	// authTenantAuthorityRestoreTestHook SUBSTITUTES the restore bind, so a test
	// can make the restore fail outright or make it land on the wrong tenant and
	// be caught by the post-restore re-read.
	authTenantAuthorityRestoreTestHook func(context.Context, model.TenantID) error
)

// LockAuthTenantAuthority implements store.AuthTenantAuthorityBarrier.
//
// ONE original AuthMutate *sql.Tx owns every step and no authority or row payload
// escapes. The order is: global directory writer admission, canonical User
// authority rows, then the complete business-tenant authority facts through the
// unchanged bundle algorithm. Nothing here bumps H, a directory epoch or a source
// row, and the engine-owned writer generation and transaction clock are the
// existing ones throughout.
//
// FIRST-STAGE ORDER. The ACTUAL SQL presentation is read and required to be
// SYSTEM before anything that could normalize it. This ordering is the contract,
// not a style choice: directoryWriteTracker.prepare rebinds the transaction to
// the writer's permanent presentation before arming its generation, so a scope
// whose logical field says SYSTEM while its real SQL presentation is a business
// tenant would otherwise be silently REPAIRED by admission and then pass a later
// check. A presentation that is merely valid on exit proves cleanup; it never
// proves the transaction entered in a valid state.
//
// REFUSALS THAT POISON VERSUS REFUSALS THAT MERELY RETURN.
//
//   - Entry-state defects POISON the original transaction: a logical scope that
//     is not SYSTEM, an unreadable actual SQL presentation, and an actual SQL
//     presentation that is not SYSTEM. These say the transaction is not in the
//     state this capability requires, so it must not be able to commit at all —
//     including when the callback discards the returned error.
//   - Order and input defects do NOT poison: repeated acquisition, prior
//     directory/authority/audit order, an invalid business tenant and the whole
//     input grammar return a precise error with no source write, so a caller that
//     recovers from malformed input ON A VALID PRESENTATION keeps a usable
//     transaction. That distinction is only meaningful because the presentation
//     was already established above.
//   - A read-only scope returns ErrReadOnly and poisons nothing: there is no
//     write to contain.
//
// From the moment admission starts (the prepare call below) every failure again
// poisons the original directory tracker and leaves authorityLocked set, so a
// discarded error can neither commit nor retry under the old value.
func (a *authScope) LockAuthTenantAuthority(
	ctx context.Context,
	tenant model.TenantID,
	bundle store.AuthoritySnapshotBundle,
) error {
	sc := a.ts
	// --- Stage 0: nothing to contain. ---
	if sc.readOnly {
		return store.ErrReadOnly
	}
	if sc.bindingPoison != nil {
		return sc.bindingPoison
	}
	t := sc.directoryWriter
	if t == nil {
		return sc.poisonAuthTenantEntry(
			directoryUnavailable("auth tenant authority requires a directory writer", nil))
	}
	if t.poisoned != nil {
		// Already contained, and reading the presentation would touch SQL on a
		// transaction that is known bad.
		return fmt.Errorf("directory writer transaction is poisoned: %w", t.poisoned)
	}

	// --- Stage 1: entry state, BEFORE anything that could normalize it. ---
	if sc.tenant != model.SystemTenantID {
		return sc.poisonAuthTenantEntry(
			directoryUnavailable("auth tenant authority requires SYSTEM scope", nil))
	}
	presented, err := sc.readAuthTenantEntryPresentation(ctx)
	if err != nil {
		return err
	}

	// --- Stage 2: order refusals on an established presentation. No poison. ---
	if sc.authorityLocked {
		return errAuthTenantAuthorityRepeated
	}
	// A used writer means some earlier wrapper already admitted itself; the
	// barrier must come first. auditBeforeDirectory is the tracker's own record
	// of the known inverse order. authorityBeforeDirectory covers a tenant
	// authority acquisition that ran before any directory admission.
	if t.locked || t.auditBeforeDirectory || t.authorityBeforeDirectory {
		return errAuthTenantAuthorityOrder
	}

	// --- Stage 3: input grammar on an established presentation. No poison. ---
	// The business tenant must be a real, canonical, non-SYSTEM tenant. Reuse the
	// directory writer's own canonicalizer so this refusal cannot drift from the
	// one the writer would apply to an affected tenant.
	if _, err := canonicalDirectoryTenants([]model.TenantID{tenant}); err != nil {
		return err
	}

	// Copy the caller-owned slices BEFORE validating them. Everything after this
	// point reads the copy, so a caller that mutates its own slice once the call
	// has returned — or from another goroutine during it — cannot change the
	// transaction question that was actually checked and locked.
	pinned := store.AuthoritySnapshotBundle{
		Facts:           append([]store.AuthorizationFactRef(nil), bundle.Facts...),
		UserAuthorities: append([]store.UserAuthorityFactRef(nil), bundle.UserAuthorities...),
	}
	if len(pinned.UserAuthorities) > authTenantAuthorityUserBudget {
		return directoryUnavailable(
			fmt.Sprintf("auth tenant authority accepts at most %d supplied User references",
				authTenantAuthorityUserBudget), nil)
	}
	// Validate the COMPLETE fact grammar for the requested business tenant before
	// taking any lock. The returned order is discarded on purpose: the bundle
	// method recomputes it under the borrowed presentation, and keeping a second
	// copy would be a second source of truth.
	if _, _, err := sc.prepareAuthoritySnapshotFor(tenant, pinned.Facts); err != nil {
		return err
	}
	users, err := canonicalUserAuthorities(pinned.UserAuthorities)
	if err != nil {
		return err
	}

	// --- Stage 4: admission. Every failure below poisons. ---
	return sc.admitAuthTenantAuthority(ctx, tenant, pinned, users, presented)
}

// admitAuthTenantAuthority owns the post-admission half. It is separate so the
// poisoning defer cannot accidentally cover the pre-admission refusals above.
func (sc *tenantScope) admitAuthTenantAuthority(
	ctx context.Context,
	tenant model.TenantID,
	pinned store.AuthoritySnapshotBundle,
	users []store.UserAuthorityFactRef,
	presented string,
) (retErr error) {
	t := sc.directoryWriter
	defer func() {
		if retErr == nil {
			return
		}
		// Never reset a prior acquisition: this only ever SETS the flag, so a
		// failed admission still refuses a later attempt, and a success path that
		// already went through the bundle method finds it unchanged.
		sc.authorityLocked = true
		t.poison(retErr)
	}()

	// Global directory admission first, with a discovery callback that reports no
	// affected tenants, so nothing bumps an epoch. The supplied User rows are
	// reserved inside that callback, under the global lock, in canonical order
	// and through the existing held-user bookkeeping. An absent H is refused
	// there; where the staged legacy writer may reserve absence instead, the
	// exact bundle comparison below still refuses it, because it compares real
	// versions and a reserved absence has none.
	if err := t.prepare(ctx, func() ([]model.TenantID, error) {
		if len(users) == 0 {
			return nil, nil
		}
		ids := make([]model.ID, 0, len(users))
		for _, ref := range users {
			ids = append(ids, ref.UserID)
		}
		return nil, t.lockUserAuthorities(ctx, ids)
	}); err != nil {
		return err
	}
	// prepare has restored SYSTEM and re-armed the existing generation proof. That
	// exact generation and tracker stay in force; no new marker protocol exists.
	return sc.withBorrowedAuthorityTenant(ctx, tenant, presented, func() error {
		if authTenantAuthorityBorrowedTestHook != nil {
			if err := authTenantAuthorityBorrowedTestHook(ctx); err != nil {
				return err
			}
		}
		// The complete, unchanged bundle algorithm. It performs its OWN
		// authorityLocked guard and assignment, re-acquires the already-held H
		// with full version comparison, and applies the canonical tenant facts —
		// including the identity-table predicate barrier and lease touching — now
		// that the scope's logical tenant is the business one.
		return sc.LockAuthoritySnapshotBundle(ctx, pinned)
	})
}

// readAuthTenantEntryPresentation reads the ACTUAL SQL tenant presentation of the
// transaction before any admission or rebind can change it. It performs no write.
// Both a read failure and a value that is not SYSTEM are entry-state defects, so
// both poison the original transaction: the caller asked this capability to act on
// a transaction whose real partition it could not establish.
func (sc *tenantScope) readAuthTenantEntryPresentation(ctx context.Context) (string, error) {
	if authTenantAuthorityEntryReadTestHook != nil {
		if err := authTenantAuthorityEntryReadTestHook(); err != nil {
			return "", sc.poisonAuthTenantEntry(
				directoryUnavailable("read auth tenant authority entry presentation", err))
		}
	}
	presented, err := readUserAuthorityPresentation(ctx, sc.tx, sc.s.dia)
	if err != nil {
		return "", sc.poisonAuthTenantEntry(
			directoryUnavailable("read auth tenant authority entry presentation", err))
	}
	if presented != model.SystemTenantID.String() {
		return "", sc.poisonAuthTenantEntry(directoryUnavailable(
			"auth tenant authority entry presentation is not SYSTEM", nil))
	}
	return presented, nil
}

// poisonAuthTenantEntry records an entry-state defect in the ORIGINAL scope and
// tracker and returns the same error, so a callback that discards it still cannot
// commit. It only ever fills an EMPTY binding poison: an earlier, more precise
// cause must survive.
func (sc *tenantScope) poisonAuthTenantEntry(err error) error {
	if sc.bindingPoison == nil {
		sc.bindingPoison = err
	}
	if sc.directoryWriter != nil {
		sc.directoryWriter.poison(err)
	}
	return err
}

// withBorrowedAuthorityTenant lends the SYSTEM-pinned scope to one business
// tenant for the duration of fn and takes it back on every exit.
//
// It is private, synchronous and non-escaping by construction: fn is supplied by
// this package, never by a service, the borrowed scope is never handed to a
// callback or retained in a repository, and no exported generic rebind exists.
// The scope is MUTATED IN PLACE rather than copied — tenantScope carries a mutex
// and transaction-clock state, so a value copy would duplicate both and silently
// fork the clock.
//
// It applies the strict restoration discipline of withUserAuthorityBinding, not
// the weak Mutate branch of withDirectoryTenantBinding: a bounded non-canceled
// context, a re-read of the ACTUAL presentation, required equality with the
// captured SYSTEM value, and the failure recorded in the ORIGINAL scope's
// bindingPoison so the transaction envelope refuses to commit even when the
// caller discards the returned error. A panic is never recovered: cleanup runs
// during unwinding and the original panic continues, so nothing can report
// success for an interval that did not finish.
func (sc *tenantScope) withBorrowedAuthorityTenant(
	ctx context.Context,
	tenant model.TenantID,
	presented string,
	fn func() error,
) (retErr error) {
	// presented is the value observed at ENTRY, before admission could normalize
	// anything, so restoration is verified against the transaction's real original
	// presentation rather than against whatever admission happened to leave.
	if presented != model.SystemTenantID.String() || presented != sc.tenant.String() {
		return sc.poisonAuthTenantEntry(
			directoryUnavailable("auth tenant authority presentation is not SYSTEM", nil))
	}
	original := sc.tenant

	defer func() {
		// Restore the logical scope first: after this assignment every repository
		// the callback can build is a SYSTEM repository again, whatever happens to
		// the SQL presentation below.
		sc.tenant = original
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), systemRestoreTimeout)
		defer cancel()
		var restoreErr error
		if authTenantAuthorityRestoreTestHook != nil {
			restoreErr = authTenantAuthorityRestoreTestHook(restoreCtx, original)
		} else {
			restoreErr = sc.s.dia.BindTenant(restoreCtx, sc.tx, original)
		}
		if restoreErr == nil {
			var restored string
			restored, restoreErr = readUserAuthorityPresentation(restoreCtx, sc.tx, sc.s.dia)
			if restoreErr == nil && restored != presented {
				restoreErr = errors.New("restored tenant differs from captured presentation")
			}
		}
		if restoreErr == nil {
			return
		}
		poison := directoryUnavailable("restore auth tenant authority binding", restoreErr)
		// Only ever fill an EMPTY poison: an inner failure recorded by the bundle
		// or the H binding is the earlier, more precise cause and must survive a
		// successful outer restore.
		if sc.bindingPoison == nil {
			sc.bindingPoison = poison
		}
		if sc.directoryWriter != nil {
			sc.directoryWriter.poison(poison)
		}
		retErr = errors.Join(retErr, poison)
	}()

	// No allowlisted authority descriptor is descriptor-Audited at this baseline,
	// so the built-in algorithm creates no audit log while the business tenant is
	// borrowed. That is a property of the current catalog, not of this code, so it
	// is CHECKED rather than assumed: if a future authority descriptor became
	// audited, sc.auditLog() would cache a log pinned to the BORROWED tenant and
	// the caller's own Audit() would silently inherit it.
	cachedAudit := sc.audit
	defer func() {
		if sc.audit == cachedAudit {
			return
		}
		sc.audit = cachedAudit
		leak := directoryUnavailable(
			"auth tenant authority cached an audit log under the borrowed tenant", nil)
		if sc.bindingPoison == nil {
			sc.bindingPoison = leak
		}
		if sc.directoryWriter != nil {
			sc.directoryWriter.poison(leak)
		}
		retErr = errors.Join(retErr, leak)
	}()

	if authTenantAuthorityBindTestHook != nil {
		if err := authTenantAuthorityBindTestHook(); err != nil {
			return directoryUnavailable("bind auth tenant authority partition", err)
		}
	}
	// Direct dialect binding, never bindDirectoryTenant: that helper clears the
	// SQLite writer marker and would destroy the generation proof prepare just
	// armed. Both presentations move together — the SQL one and the logical one
	// the repositories and the epoch grammar read.
	if err := sc.s.dia.BindTenant(ctx, sc.tx, tenant); err != nil {
		return directoryUnavailable("bind auth tenant authority partition", err)
	}
	sc.tenant = tenant
	return fn()
}
