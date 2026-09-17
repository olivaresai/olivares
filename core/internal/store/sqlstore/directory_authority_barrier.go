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

// The ordinary tenant scope owns DAB1. The SAME concrete type also backs every
// View and the SYSTEM-tenant Mutate that AuthMutate builds its auth view over,
// so the assertion below always succeeds against this Store and the refusal for
// those two is always a RUNTIME one (see the entry stages in
// LockDirectoryAuthoritySnapshot). Absence is meaningful only for a non-SQL
// implementation. authScope and custodyScope hold their inner scope in a NAMED
// field, so neither promotes this method and neither may declare it.
var _ store.DirectoryAuthoritySnapshotLocker = (*tenantScope)(nil)

var (
	// errDirectoryAuthorityRepeated refuses a second acquisition in one
	// transaction. It fires BEFORE any lock is taken and is deliberately
	// distinct from the bundle method's own message.
	errDirectoryAuthorityRepeated = errors.New(
		"sqlstore: directory authority is already acquired in this transaction",
	)
	// errDirectoryAuthorityOrder refuses the inverse orders the tracker can
	// actually observe: a directory writer some earlier wrapper already used, a
	// recorded audit-before-directory sequence, and a tenant authority
	// acquisition that ran before any directory admission.
	errDirectoryAuthorityOrder = errors.New(
		"sqlstore: directory authority must precede directory admission, authority and audit locks",
	)
)

// directoryAuthorityUserBudget bounds the SUPPLIED User references of THIS
// interface only, before equal duplicates are removed. The tenant-scope bundle
// deliberately keeps its larger acceptance; narrowing it here would change a
// ratified contract other callers already rely on. It matches ATA1's budget
// because both express the same human-authority question.
const directoryAuthorityUserBudget = 64

// directoryAuthorityEntryReadTestHook fails the ENTRY presentation read. It is
// nil in every build that does not set it from a test in this package: there is
// no runtime configuration, no exported symbol and no way for a consumer to
// reach it.
var directoryAuthorityEntryReadTestHook func() error

// LockDirectoryAuthoritySnapshot implements store.DirectoryAuthoritySnapshotLocker.
//
// ONE original ordinary Mutate *sql.Tx owns every step and no authority or row
// payload escapes. The order is: global directory writer admission, canonical
// User authority rows, then the complete tenant authority facts through the
// UNCHANGED bundle algorithm. Nothing here bumps H, a directory epoch or a
// directory source row, and the engine-owned writer generation, lineage prelude
// and transaction clock are the existing ones throughout.
//
// There is no borrowed tenant. Unlike ATA1, this scope's logical tenant and the
// tracker's permanent presentation ARE the business tenant the facts belong to,
// so the bundle's own binding discipline applies directly.
//
// FIRST-STAGE ORDER. The ACTUAL SQL presentation is read and required to equal
// the bound business tenant before anything that could normalize it. That
// ordering is the contract, not a style choice: directoryWriteTracker.prepare
// rebinds the transaction to the writer's permanent presentation before arming
// its generation, so a scope whose logical field says tenant T while its real
// SQL presentation is some other partition would otherwise be silently REPAIRED
// by admission and then pass every later check. A presentation that is merely
// valid on exit proves cleanup; it never proves the transaction entered in a
// valid state. The check is required even for a token-shaped bundle with zero
// User references, because the complete bundle only re-verifies the
// presentation inside withUserAuthorityBinding, which that shape never reaches.
//
// REFUSALS THAT POISON VERSUS REFUSALS THAT MERELY RETURN.
//
//   - Entry-state defects POISON the original transaction: a logical scope that
//     is not a canonical non-SYSTEM tenant, a missing directory tracker, a
//     tracker whose permanent presentation is not that tenant, an unreadable
//     actual SQL presentation, and an actual SQL presentation that is not that
//     tenant. These say the transaction is not in the state this capability
//     requires, so it must not be able to commit at all — including when the
//     callback discards the returned error.
//   - Order and input defects do NOT poison: repeated acquisition, prior
//     directory/authority/audit order and the whole input grammar return a
//     precise error with no source write, so a caller that recovers from
//     malformed input ON A VALID PRESENTATION keeps a usable transaction. That
//     distinction is only meaningful because the presentation was already
//     established above.
//   - A read-only scope returns ErrReadOnly and poisons nothing: there is no
//     write to contain.
//
// From the moment admission starts (the prepare call in admitDirectoryAuthority)
// every failure again poisons the original directory tracker and leaves
// authorityLocked set, so a discarded error can neither commit nor retry under
// the old authority value.
//
// WHAT IT CANNOT SEE. sc.readOnly, bindingPoison, the tracker's presence,
// poison, locked, auditBeforeDirectory and authorityBeforeDirectory flags and
// sc.authorityLocked are the complete enforceable set. A preceding raw row lock
// leaves no tracker state, and store.TransactionLocker.LockTransaction is a
// public, confinement-forwarded, lock-bearing operation with a caller-chosen
// key that leaves none either — PostgreSQL advisory transaction locks are
// reentrant, so a caller that spells the store's internal directory key through
// it produces untracked exclusion that acquireDirectoryWriter will re-take
// without noticing. Both remain caller obligations for the future consumer's own
// contract tests. This method does not claim to detect them.
func (sc *tenantScope) LockDirectoryAuthoritySnapshot(
	ctx context.Context,
	bundle store.AuthoritySnapshotBundle,
) error {
	// --- Stage 0: nothing to contain. ---
	if sc.readOnly {
		return store.ErrReadOnly
	}
	if sc.bindingPoison != nil {
		return sc.bindingPoison
	}
	t := sc.directoryWriter
	if t == nil {
		return sc.poisonDirectoryAuthorityEntry(
			directoryUnavailable("directory authority requires a directory writer", nil))
	}
	if t.poisoned != nil {
		// Already contained, and reading the presentation would touch SQL on a
		// transaction that is known bad.
		return fmt.Errorf("directory writer transaction is poisoned: %w", t.poisoned)
	}

	// --- Stage 1: entry state, BEFORE anything that could normalize it. ---
	// Reuse the directory writer's own canonicalizer so this refusal cannot
	// drift from the one the writer would apply to an affected tenant: SYSTEM,
	// zero and non-canonical UUIDv7 tenants are refused on identical terms.
	if _, err := canonicalDirectoryTenants([]model.TenantID{sc.tenant}); err != nil {
		return sc.poisonDirectoryAuthorityEntry(err)
	}
	if t.presentationTenant != sc.tenant {
		return sc.poisonDirectoryAuthorityEntry(directoryUnavailable(
			"directory authority tracker presentation does not match the bound tenant", nil))
	}
	if err := sc.readDirectoryAuthorityEntryPresentation(ctx); err != nil {
		return err
	}

	// --- Stage 2: order refusals on an established presentation. No poison. ---
	if sc.authorityLocked {
		return errDirectoryAuthorityRepeated
	}
	// A used writer means some earlier wrapper already admitted itself; the
	// barrier must come first. auditBeforeDirectory is the tracker's own record
	// of the known inverse order. authorityBeforeDirectory covers a tenant
	// authority acquisition that ran before any directory admission.
	if t.locked || t.auditBeforeDirectory || t.authorityBeforeDirectory {
		return errDirectoryAuthorityOrder
	}

	// --- Stage 3: input grammar on an established presentation. No poison. ---
	// Copy the caller-owned slices before validating them. The caller must not
	// mutate either slice concurrently with this call, including during the copy.
	// Later validation and locking use only the copies; changes after the call
	// returns cannot change the transaction question that was checked and locked.
	pinned := store.AuthoritySnapshotBundle{
		Facts:           append([]store.AuthorizationFactRef(nil), bundle.Facts...),
		UserAuthorities: append([]store.UserAuthorityFactRef(nil), bundle.UserAuthorities...),
	}
	if len(pinned.UserAuthorities) > directoryAuthorityUserBudget {
		return directoryUnavailable(
			fmt.Sprintf("directory authority accepts at most %d supplied User references",
				directoryAuthorityUserBudget), nil)
	}
	// Validate the COMPLETE fact grammar for this scope's own tenant before
	// taking any lock. The returned order is discarded on purpose: the bundle
	// method recomputes it, and keeping a second copy would be a second source
	// of truth.
	if _, _, err := sc.prepareAuthoritySnapshot(pinned.Facts); err != nil {
		return err
	}
	users, err := canonicalUserAuthorities(pinned.UserAuthorities)
	if err != nil {
		return err
	}

	// --- Stage 4: admission. Every failure below poisons. ---
	return sc.admitDirectoryAuthority(ctx, pinned, users)
}

// admitDirectoryAuthority owns the post-admission half. It is separate so the
// poisoning defer cannot accidentally cover the pre-admission refusals above.
func (sc *tenantScope) admitDirectoryAuthority(
	ctx context.Context,
	pinned store.AuthoritySnapshotBundle,
	users []store.UserAuthorityFactRef,
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
	// prepare has restored this tenant's permanent presentation and re-armed the
	// existing generation proof. That exact generation and tracker stay in force;
	// no new marker protocol exists.
	//
	// The complete, UNCHANGED bundle algorithm follows. It performs its own
	// authorityLocked guard and assignment, re-acquires the already-held H with
	// full version comparison, and applies the canonical tenant facts — including
	// the identity-table predicate barrier and the required leased-fact OCC
	// touches. Because the tracker is already locked, it cannot record
	// authorityBeforeDirectory, and because this scope is already pinned to the
	// business tenant, no borrowed callback is needed.
	return sc.LockAuthoritySnapshotBundle(ctx, pinned)
}

// readDirectoryAuthorityEntryPresentation reads the ACTUAL SQL tenant
// presentation of the transaction before any admission or rebind can change it.
// It performs no write. A read failure and a value that is not this scope's own
// business tenant are both entry-state defects, so both poison the original
// transaction: the caller asked this capability to act on a transaction whose
// real partition it could not establish.
//
// readAuthTenantEntryPresentation is deliberately NOT reused: it hard-requires
// SYSTEM, which is the exact opposite of this barrier's entry state.
func (sc *tenantScope) readDirectoryAuthorityEntryPresentation(ctx context.Context) error {
	if directoryAuthorityEntryReadTestHook != nil {
		if err := directoryAuthorityEntryReadTestHook(); err != nil {
			return sc.poisonDirectoryAuthorityEntry(
				directoryUnavailable("read directory authority entry presentation", err))
		}
	}
	presented, err := readUserAuthorityPresentation(ctx, sc.tx, sc.s.dia)
	if err != nil {
		return sc.poisonDirectoryAuthorityEntry(
			directoryUnavailable("read directory authority entry presentation", err))
	}
	// Compared against BOTH the logical pin and the tracker's permanent
	// presentation. They are equal by the check above, and asserting both here
	// keeps the guarantee stated where it is relied upon rather than inferred.
	if presented != sc.tenant.String() ||
		presented != sc.directoryWriter.presentationTenant.String() {
		return sc.poisonDirectoryAuthorityEntry(directoryUnavailable(
			"directory authority entry presentation is not the bound tenant", nil))
	}
	return nil
}

// poisonDirectoryAuthorityEntry records an entry-state defect in the ORIGINAL
// scope and tracker and returns the same error, so a callback that discards it
// still cannot commit. It only ever fills an EMPTY binding poison: an earlier,
// more precise cause must survive.
func (sc *tenantScope) poisonDirectoryAuthorityEntry(err error) error {
	if sc.bindingPoison == nil {
		sc.bindingPoison = err
	}
	if sc.directoryWriter != nil {
		sc.directoryWriter.poison(err)
	}
	return err
}
