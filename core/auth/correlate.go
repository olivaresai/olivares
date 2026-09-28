// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A correlated writer stores counted content, or an owner key, in a tenant. It
// first correlates what it stores with the accounts it names, in one read-only
// view that reads the tenant's directory fact D_T first, and then opens its
// transaction with the directory authority barrier over D_T as read, its own
// extra facts and each named account's authority version. A directory write
// that commits in between moves D_T, so the barrier conflicts and the writer
// correlates again, once.

// CensusLimits bounds one content correlation. Every limit is passed explicitly.
type CensusLimits struct {
	PageRows           int
	MaxDirectoryRows   int
	MaxAccounts        int
	MaxAliasBytes      int
	MaxTotalAliasBytes int
	Match              MatchLimits
}

// TenantCensusLimits returns the limits of a content correlation in one tenant:
// 500-row pages, 50,000 directory rows read over memberships and exclusions
// together, 50,000 accounts, 4,096 bytes per alias and 8 MiB of aliases, and
// the counted-content match limits.
func TenantCensusLimits() CensusLimits {
	return CensusLimits{
		PageRows:           500,
		MaxDirectoryRows:   50_000,
		MaxAccounts:        50_000,
		MaxAliasBytes:      4_096,
		MaxTotalAliasBytes: 8 << 20,
		Match:              CountedContentLimits(),
	}
}

// Correlation is what one correlation read: the tenant's directory fact, read
// first, and every existing account the input names.
type Correlation struct {
	// Directory is D_T, the first read of the correlation view.
	Directory store.AuthorizationFactRef
	// Accounts is every matched account, deduplicated and sorted.
	Accounts []model.ID
}

// Correlator is the part of the standing port a correlated writer uses.
type Correlator interface {
	// CorrelateContent returns the accounts of tenant that doc names.
	CorrelateContent(ctx context.Context, tenant model.TenantID, doc Document, lim CensusLimits) (Correlation, error)
	// CorrelateOwnerKeys returns the accounts bound to keys.
	CorrelateOwnerKeys(ctx context.Context, tenant model.TenantID, keys []QualifiedKey) (Correlation, error)
}

// Pinned is what a correlated write's barrier pinned.
type Pinned struct {
	// Directory is D_T as pinned, equal to the correlation's.
	Directory store.AuthorizationFactRef
	// Facts are the extra facts as pinned, in the order Facts returned them.
	Facts []store.AuthorizationFactRef
	// Refs are the matched accounts at their authority versions.
	Refs []store.UserAuthorityFactRef
}

// CorrelatedWriteSpec is one correlated write.
type CorrelatedWriteSpec struct {
	Tenant model.TenantID
	// Correlate runs outside Mutate, and again on the retry.
	Correlate func(ctx context.Context) (Correlation, error)
	// Restrict gives RestrictingWrite semantics: a named account's standing
	// never refuses the write.
	Restrict bool
	// Facts reads the extra facts to pin inside the transaction. It only reads.
	Facts func(ctx context.Context, sc store.Scope) ([]store.AuthorizationFactRef, error)
	// Mutate runs fn in one tenant transaction.
	Mutate func(ctx context.Context, fn func(store.Scope) error) error
	// Write is the write itself, after the barrier.
	Write func(ctx context.Context, sc store.Scope, pinned Pinned) error
}

// ErrAliasCensusIncomplete marks a census that stopped at one of its limits or
// found a directory row naming no account. It is not retryable: the directory
// or the limits must change first.
var ErrAliasCensusIncomplete = errors.New("auth: the alias census is incomplete")

// CensusIncompleteError names why a census is incomplete: directory_rows,
// accounts, alias_bytes, total_alias_bytes or missing_account.
type CensusIncompleteError struct {
	Dimension string
}

func (e CensusIncompleteError) Error() string {
	return ErrAliasCensusIncomplete.Error() + ": " + e.Dimension
}

// Unwrap makes errors.Is(err, ErrAliasCensusIncomplete) hold.
func (e CensusIncompleteError) Unwrap() error { return ErrAliasCensusIncomplete }

// The dimensions of a CensusIncompleteError. Each is a wire code,
// alias_census_incomplete:<dimension>, and none is retryable.
const (
	CensusDirectoryRows   = "directory_rows"
	CensusAccounts        = "accounts"
	CensusAliasBytes      = "alias_bytes"
	CensusTotalAliasBytes = "total_alias_bytes"
	CensusMissingAccount  = "missing_account"
)

// ErrAliasCensusUnavailable marks a census that could not read what it needs.
// It is retryable.
var ErrAliasCensusUnavailable = errors.New("auth: the alias census is unavailable")

// CensusUnavailableError names the read a census could not complete:
// directory_fact, continuation or page_failed. Err is the cause.
type CensusUnavailableError struct {
	Dimension string
	Err       error
}

func (e CensusUnavailableError) Error() string {
	if e.Err == nil {
		return ErrAliasCensusUnavailable.Error() + ": " + e.Dimension
	}
	return ErrAliasCensusUnavailable.Error() + ": " + e.Dimension + ": " + e.Err.Error()
}

// Unwrap makes errors.Is hold for ErrAliasCensusUnavailable and for the cause.
func (e CensusUnavailableError) Unwrap() []error { return []error{ErrAliasCensusUnavailable, e.Err} }

// The dimensions of a CensusUnavailableError. Each is a wire code,
// alias_census_unavailable:<dimension>, and each is retryable.
const (
	CensusDirectoryFact = "directory_fact"
	CensusContinuation  = "continuation"
	CensusPageFailed    = "page_failed"
)

// ErrDuplicateBinding refuses an owner key bound to more than one account. It
// is not retryable: no account is picked among them.
var ErrDuplicateBinding = errors.New("auth: an owner key is bound to more than one account")

// errNoCensusEvidence refuses a census over a scope that cannot read the
// directory fact or the transaction's time.
var errNoCensusEvidence = errors.New("auth: the auth scope exposes no directory fact reader")

// CorrelateContent implements Correlator in one repeatable-read auth view. It
// reads D_T first. It then resolves every canonical id doc carries by primary
// key: an account's own id names it, and a session or token id names its owners
// while the credential is live at the view's time. A live session without an
// owner refuses as missing_account; a token's owner and act-as account are
// optional, and an absent one names nobody. It walks the tenant's memberships
// and its offboard exclusions that are not retired or lifted, with one row
// budget over both, and matches doc against the normalized email and the exact
// external id of every account the walk admitted. Every account the canonical
// leg or the match finds is kept.
func (a *Authenticator) CorrelateContent(ctx context.Context, tenant model.TenantID, doc Document, lim CensusLimits) (Correlation, error) {
	var corr Correlation
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		evidence, ok := as.(store.AuthPrincipalEvidenceScope)
		if !ok {
			return CensusUnavailableError{Dimension: CensusDirectoryFact, Err: errNoCensusEvidence}
		}
		d0, err := evidence.ReadDirectoryEpochFact(ctx, tenant)
		if err != nil {
			return CensusUnavailableError{Dimension: CensusDirectoryFact, Err: err}
		}
		named, err := canonicalLeg(ctx, as, evidence, doc, lim.Match)
		if err != nil {
			return err
		}
		admitted, err := directoryCensus(ctx, as, tenant, lim)
		if err != nil {
			return err
		}
		hit, err := censusMatches(ctx, as, admitted, doc, lim)
		if err != nil {
			return err
		}
		corr = Correlation{Directory: d0, Accounts: sortedAccounts(named, hit)}
		return nil
	})
	if err != nil {
		return Correlation{}, err
	}
	return corr, nil
}

// canonicalLeg returns the accounts the canonical ids of doc name.
func canonicalLeg(ctx context.Context, as store.AuthScope, clock store.TransactionClock, doc Document, lim MatchLimits) ([]model.ID, error) {
	ids, err := CanonicalIDs(doc, lim)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	now, err := clock.TransactionNow(ctx)
	if err != nil {
		return nil, CensusUnavailableError{Dimension: CensusPageFailed, Err: err}
	}
	var out []model.ID
	for _, id := range ids {
		if _, err := as.Users().Get(ctx, id); err == nil {
			out = append(out, id)
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, CensusUnavailableError{Dimension: CensusPageFailed, Err: err}
		}
		if s, err := as.Sessions().Get(ctx, id); err == nil {
			owners, err := sessionOwners(now, s)
			if err != nil {
				return nil, err
			}
			out = append(out, owners...)
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, CensusUnavailableError{Dimension: CensusPageFailed, Err: err}
		}
		if t, err := as.Tokens().Get(ctx, id); err == nil {
			out = append(out, tokenOwners(now, t)...)
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, CensusUnavailableError{Dimension: CensusPageFailed, Err: err}
		}
	}
	return out, nil
}

// sessionOwners returns the account a session names: its owner, while it is
// live at now. A session always has an owner, so a live one without an owner
// refuses as missing_account.
func sessionOwners(now model.Timestamp, s model.AuthSession) ([]model.ID, error) {
	if s.Revoked || !now.Before(s.ExpiresAt) {
		return nil, nil
	}
	if s.UserID.IsZero() {
		return nil, CensusIncompleteError{Dimension: CensusMissingAccount}
	}
	return []model.ID{s.UserID}, nil
}

// tokenOwners returns the accounts a token names while it is live at now: its
// owner and the account it acts for, each when present. Both are optional.
func tokenOwners(now model.Timestamp, t model.APIToken) []model.ID {
	if t.Revoked || (t.ExpiresAt != nil && !now.Before(*t.ExpiresAt)) {
		return nil
	}
	var out []model.ID
	for _, id := range []model.ID{t.UserID, t.ActAsUserID} {
		if !id.IsZero() {
			out = append(out, id)
		}
	}
	return out
}

// directoryCensus returns, sorted, the accounts the tenant's directory admits:
// every membership, and every offboard exclusion that is not retired or lifted
// (an unknown state is admitted). The tenant filter is the query itself, and
// every row read counts toward one budget of MaxDirectoryRows. An admitted row
// whose account id is zero refuses as missing_account.
func directoryCensus(ctx context.Context, as store.AuthScope, tenant model.TenantID, lim CensusLimits) ([]model.ID, error) {
	admitted := make(map[model.ID]struct{})
	q := byEq("target_tenant_id", tenant.String(), lim.PageRows)
	read := 0
	err := store.WalkPages(ctx, as.Memberships().List, q, lim.MaxDirectoryRows, func(rows []model.Membership) error {
		read += len(rows)
		for _, m := range rows {
			if m.UserID.IsZero() {
				return CensusIncompleteError{Dimension: CensusMissingAccount}
			}
			admitted[m.UserID] = struct{}{}
		}
		return nil
	})
	if err == nil {
		err = store.WalkPages(ctx, as.TenantExclusions().List, q, lim.MaxDirectoryRows-read, func(rows []model.TenantExclusion) error {
			for _, x := range rows {
				if !censusAdmits(x) {
					continue
				}
				if x.UserID.IsZero() {
					return CensusIncompleteError{Dimension: CensusMissingAccount}
				}
				admitted[x.UserID] = struct{}{}
			}
			return nil
		})
	}
	var incomplete CensusIncompleteError
	switch {
	case errors.As(err, &incomplete):
		return nil, incomplete
	case errors.Is(err, store.ErrPageCapacity):
		return nil, CensusIncompleteError{Dimension: CensusDirectoryRows}
	case errors.Is(err, store.ErrPageContinuation):
		return nil, CensusUnavailableError{Dimension: CensusContinuation, Err: err}
	case err != nil:
		return nil, CensusUnavailableError{Dimension: CensusPageFailed, Err: err}
	}
	out := make([]model.ID, 0, len(admitted))
	for id := range admitted {
		out = append(out, id)
	}
	if len(out) > lim.MaxAccounts {
		return nil, CensusIncompleteError{Dimension: CensusAccounts}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// censusAdmits reports whether an exclusion row admits its account to the
// census: an offboard row that is neither retired nor lifted. An unknown state
// is admitted.
func censusAdmits(x model.TenantExclusion) bool {
	return x.Kind == model.ExclusionOffboard && x.RetirementState != model.RetirementRetired && x.RetirementState != model.RetirementLifted
}

// censusMatches reads each admitted account in id order and returns those
// whose normalized email or exact external id doc names, in one match. A row
// that is missing, and an alias above its limits, refuses the census: nothing
// is skipped.
func censusMatches(ctx context.Context, as store.AuthScope, accounts []model.ID, doc Document, lim CensusLimits) ([]model.ID, error) {
	var aliases []Alias
	var owners []model.ID
	total := 0
	for _, id := range accounts {
		u, err := as.Users().Get(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return nil, CensusIncompleteError{Dimension: CensusMissingAccount}
		}
		if err != nil {
			return nil, CensusUnavailableError{Dimension: CensusPageFailed, Err: err}
		}
		for _, alias := range []Alias{{Kind: AliasEmail, Value: normalizeEmail(u.Email)}, {Kind: AliasExternal, Value: u.ExternalID}} {
			if alias.Value == "" {
				continue
			}
			if len(alias.Value) > lim.MaxAliasBytes {
				return nil, CensusIncompleteError{Dimension: CensusAliasBytes}
			}
			total += len(alias.Value)
			if total > lim.MaxTotalAliasBytes {
				return nil, CensusIncompleteError{Dimension: CensusTotalAliasBytes}
			}
			aliases = append(aliases, alias)
			owners = append(owners, id)
		}
	}
	hits, err := MatchAliases(doc, aliases, lim.Match)
	if err != nil {
		return nil, err
	}
	var out []model.ID
	for i, hit := range hits {
		if hit {
			out = append(out, owners[i])
		}
	}
	return out, nil
}

// CorrelateOwnerKeys implements Correlator: it reads D_T first, then, for each
// key, at most two accounts bound to it through the unique subject index. Two
// accounts refuse with ErrDuplicateBinding.
func (a *Authenticator) CorrelateOwnerKeys(ctx context.Context, tenant model.TenantID, keys []QualifiedKey) (Correlation, error) {
	var corr Correlation
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		evidence, ok := as.(store.AuthPrincipalEvidenceScope)
		if !ok {
			return CensusUnavailableError{Dimension: CensusDirectoryFact, Err: errNoCensusEvidence}
		}
		d0, err := evidence.ReadDirectoryEpochFact(ctx, tenant)
		if err != nil {
			return CensusUnavailableError{Dimension: CensusDirectoryFact, Err: err}
		}
		var bound []model.ID
		for _, key := range keys {
			if key == "" {
				return OwnerUnkeyableError{Reason: UnkeyableEmpty}
			}
			users, _, err := as.Users().List(ctx, byEq("sso_subject", string(key), 2))
			if err != nil {
				return CensusUnavailableError{Dimension: CensusPageFailed, Err: err}
			}
			if len(users) > 1 {
				return ErrDuplicateBinding
			}
			for _, u := range users {
				bound = append(bound, u.ID)
			}
		}
		corr = Correlation{Directory: d0, Accounts: sortedAccounts(bound)}
		return nil
	})
	if err != nil {
		return Correlation{}, err
	}
	return corr, nil
}

var _ Correlator = (*Authenticator)(nil)

// sortedAccounts returns the distinct non-zero ids of every list, sorted.
func sortedAccounts(lists ...[]model.ID) []model.ID {
	seen := make(map[model.ID]struct{})
	var out []model.ID
	for _, list := range lists {
		for _, id := range list {
			if _, dup := seen[id]; dup || id.IsZero() {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// CorrelatedWrite runs one correlated write, and is its only retry owner. Each
// attempt correlates, refuses more than MaxFencedSubjects accounts before any
// standing read, and reads their standing once. An account that does not
// exist, or a zero account id, refuses as missing_account even for a
// restriction; otherwise the standing refuses unless spec.Restrict is set. It
// then runs spec.Mutate with a body that reads the extra facts, takes the
// directory authority barrier over the correlation's D_T, the extra facts and
// the accounts' authority versions, and then calls spec.Write. A correlation
// error returns at once. When the barrier finds a moved fact, or the body
// answers store.ErrConflict, the first attempt starts over with a fresh
// correlation and the second returns store.ErrConflict.
func CorrelatedWrite(ctx context.Context, r StandingReader, spec CorrelatedWriteSpec) error {
	for attempt := 0; ; attempt++ {
		corr, err := spec.Correlate(ctx)
		if err != nil {
			return err
		}
		if len(corr.Accounts) > MaxFencedSubjects {
			return ErrTooManySubjects
		}
		for _, id := range corr.Accounts {
			if id.IsZero() {
				return CensusIncompleteError{Dimension: CensusMissingAccount}
			}
		}
		var reader StandingReader
		if r != nil {
			reader = existingStanding{r: r}
		}
		refs, err := fenceSubjects(ctx, reader, spec.Tenant, corr.Accounts, !spec.Restrict)
		if err != nil {
			return err
		}
		err = spec.Mutate(ctx, func(sc store.Scope) error {
			var facts []store.AuthorizationFactRef
			if spec.Facts != nil {
				var err error
				if facts, err = spec.Facts(ctx, sc); err != nil {
					return err
				}
			}
			if err := pinCorrelation(ctx, sc, corr.Directory, facts, refs); err != nil {
				return err
			}
			return spec.Write(ctx, sc, Pinned{Directory: corr.Directory, Facts: facts, Refs: refs})
		})
		if !errors.Is(err, errFenceMoved) && !errors.Is(err, store.ErrConflict) {
			return err
		}
		if attempt == 1 {
			return store.ErrConflict
		}
	}
}

// existingStanding is the bracket's standing reader. It reads r once per call,
// so an attempt still has one standing snapshot, and refuses an account that
// does not exist as missing_account before fenceSubjects applies the standing
// rule, which stays its one owner. An account r omits is left to
// fenceSubjects, as a failed read.
type existingStanding struct {
	r StandingReader
}

// Standing implements StandingReader.
func (e existingStanding) Standing(ctx context.Context, tenant model.TenantID, users []model.ID) (map[model.ID]Standing, error) {
	out, err := e.r.Standing(ctx, tenant, users)
	if err != nil {
		return nil, err
	}
	for _, id := range users {
		if s, ok := out[id]; ok && !s.Exists {
			return nil, CensusIncompleteError{Dimension: CensusMissingAccount}
		}
	}
	return out, nil
}

// pinCorrelation takes the directory authority barrier over directory, facts
// and refs. It is the transaction's first lock-bearing call.
func pinCorrelation(ctx context.Context, sc store.Scope, directory store.AuthorizationFactRef, facts []store.AuthorizationFactRef, refs []store.UserAuthorityFactRef) error {
	locker, ok := sc.(store.DirectoryAuthoritySnapshotLocker)
	if !ok {
		return errors.New("auth: the scope exposes no directory authority barrier")
	}
	if err := locker.LockDirectoryAuthoritySnapshot(ctx, store.AuthoritySnapshotBundle{
		Facts: append([]store.AuthorizationFactRef{directory}, facts...), UserAuthorities: refs,
	}); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return fmt.Errorf("%w: %w", errFenceMoved, err)
		}
		return err
	}
	return nil
}
