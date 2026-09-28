// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A fenced writer is one that stores, in a tenant, a reference that lets an
// account act there or binds it to a duty. It reads the standing of every
// account it is about to name, refuses while any of them is being removed or
// erased, and then opens its transaction with the directory authority barrier:
// the tenant's fact and each account's authority version, as read. An offboard
// that commits in between moves the version, so the barrier conflicts and the
// writer reads the standing again, once. The retirement's own steps take the same
// barrier, so a writer and a retirement never both apply against one version.

// ErrTooManySubjects refuses a fenced write that names more accounts than one
// barrier may pin.
var ErrTooManySubjects = errors.New("auth: the write names more accounts than one fenced write may pin")

// MaxFencedSubjects bounds the accounts one fenced write may name.
const MaxFencedSubjects = 64

// errFenceMoved marks a barrier conflict: an account's authority version or the
// tenant's fact moved after the standing was read.
var errFenceMoved = errors.New("auth: a pinned authority moved after the standing read")

// FenceMoved reports whether err is a barrier conflict from PinFence: an
// account's authority version or the tenant's fact moved after the standing
// was read. A writer that owns its retry reads the standing again.
func FenceMoved(err error) bool { return errors.Is(err, errFenceMoved) }

// FenceFact selects the tenant fact a fenced writer pins with the accounts.
type FenceFact uint8

const (
	// FenceDirectory pins the tenant's directory epoch.
	FenceDirectory FenceFact = iota + 1
	// FenceAuthorization pins the tenant's authorization epoch.
	FenceAuthorization
)

// FenceSubjects reads the standing of users in tenant and returns the authority
// versions a fenced writer must pin, sorted. It answers the standing's refusal
// for an account being removed or erased, and skips an id that names no account
// (account ids are allocated by the engine, so it can never name one). The bound
// of MaxFencedSubjects counts accounts, once each, after that resolution: ids
// that name no account never count against it.
func FenceSubjects(ctx context.Context, r StandingReader, tenant model.TenantID, users []model.ID) ([]store.UserAuthorityFactRef, error) {
	return fenceSubjects(ctx, r, tenant, users, true)
}

// fenceSubjects is FenceSubjects; refuse is false for a restriction, which is
// never refused for a named account's standing.
func fenceSubjects(ctx context.Context, r StandingReader, tenant model.TenantID, users []model.ID, refuse bool) ([]store.UserAuthorityFactRef, error) {
	seen := make(map[model.ID]bool, len(users))
	ids := make([]model.ID, 0, len(users))
	for _, u := range users {
		if u.IsZero() || seen[u] {
			continue
		}
		seen[u] = true
		ids = append(ids, u)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if r == nil {
		return nil, errors.New("auth: no standing reader is wired for a fenced writer")
	}
	standing, err := r.Standing(ctx, tenant, ids)
	if err != nil {
		return nil, err
	}
	refs := make([]store.UserAuthorityFactRef, 0, len(ids))
	for _, id := range ids {
		s, ok := standing[id]
		if !ok {
			return nil, fmt.Errorf("auth: no standing was read for a named account")
		}
		if err := s.Refusal(); err != nil && refuse {
			return nil, err
		}
		if !s.Exists {
			continue
		}
		refs = append(refs, store.UserAuthorityFactRef{UserID: id, Version: s.AuthorityVersion})
	}
	if len(refs) > MaxFencedSubjects {
		return nil, ErrTooManySubjects
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].UserID < refs[j].UserID })
	return refs, nil
}

// PinFence is a fenced writer's first lock-bearing call inside its tenant
// transaction: the directory authority barrier over the tenant's fact and refs.
// It returns the pinned fact. A moved version answers an error that FencedWrite
// retries once.
func PinFence(ctx context.Context, sc store.Scope, fact FenceFact, refs []store.UserAuthorityFactRef) (store.AuthorizationFactRef, error) {
	var pinned store.AuthorizationFactRef
	switch fact {
	case FenceDirectory:
		reader, ok := sc.(store.DirectorySnapshotReader)
		if !ok {
			return pinned, errors.New("auth: the scope exposes no directory snapshot reader")
		}
		epoch, err := reader.ReadDirectoryEpoch(ctx)
		if err != nil {
			return pinned, err
		}
		pinned = store.AuthorizationFactRef{Kind: model.DirectoryEpochKind, ID: epoch.ID, Version: epoch.Version}
	case FenceAuthorization:
		reader, ok := sc.(store.AuthorizationEpochReader)
		if !ok {
			return pinned, errors.New("auth: the scope exposes no authorization epoch reader")
		}
		current, err := reader.ReadAuthorizationEpoch(ctx)
		if err != nil {
			return pinned, err
		}
		pinned = current
	default:
		return pinned, errors.New("auth: unknown fence fact")
	}
	locker, ok := sc.(store.DirectoryAuthoritySnapshotLocker)
	if !ok {
		return pinned, errors.New("auth: the scope exposes no directory authority barrier")
	}
	if err := locker.LockDirectoryAuthoritySnapshot(ctx, store.AuthoritySnapshotBundle{
		Facts: []store.AuthorizationFactRef{pinned}, UserAuthorities: refs,
	}); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return pinned, fmt.Errorf("%w: %w", errFenceMoved, err)
		}
		return pinned, err
	}
	return pinned, nil
}

// FencedWrite runs one fenced write: it reads the standing of users in tenant,
// runs mutate with a transaction body that pins the fence first and then calls
// write, and, when the fence moved after the standing read, reads the standing
// again and retries once: a standing that now refuses answers its refusal, and a
// second move answers store.ErrConflict. write learns whether a fence was
// pinned: when no named value is an existing account there is none, and the
// writer takes its own locks as before.
func FencedWrite(
	ctx context.Context,
	r StandingReader,
	tenant model.TenantID,
	users []model.ID,
	fact FenceFact,
	mutate func(func(store.Scope) error) error,
	write func(sc store.Scope, fenced bool) error,
) error {
	return fencedWrite(ctx, r, tenant, users, fact, mutate, write, true)
}

// RestrictingWrite runs one write whose reference to the named accounts only
// ever narrows what they may do, such as a rule that forbids them something. It
// takes the same barrier as FencedWrite, so the write seam holds every named
// account and an offboard orders against the write, but it never refuses for a
// named account's standing: a restriction written while an account is being
// removed or erased takes nothing from the removal, and the retirement keeps it.
func RestrictingWrite(
	ctx context.Context,
	r StandingReader,
	tenant model.TenantID,
	users []model.ID,
	fact FenceFact,
	mutate func(func(store.Scope) error) error,
	write func(sc store.Scope, fenced bool) error,
) error {
	return fencedWrite(ctx, r, tenant, users, fact, mutate, write, false)
}

func fencedWrite(
	ctx context.Context,
	r StandingReader,
	tenant model.TenantID,
	users []model.ID,
	fact FenceFact,
	mutate func(func(store.Scope) error) error,
	write func(sc store.Scope, fenced bool) error,
	refuse bool,
) error {
	for attempt := 0; ; attempt++ {
		refs, err := fenceSubjects(ctx, r, tenant, users, refuse)
		if err != nil {
			return err
		}
		err = mutate(func(sc store.Scope) error {
			if len(refs) > 0 {
				if _, err := PinFence(ctx, sc, fact, refs); err != nil {
					return err
				}
			}
			return write(sc, len(refs) > 0)
		})
		if !errors.Is(err, errFenceMoved) {
			return err
		}
		if attempt == 1 {
			return store.ErrConflict
		}
	}
}

// FenceRefusalCode returns the stable code a fenced writer answers, with 409,
// for one of the fence's refusals, and whether err is one.
func FenceRefusalCode(err error) (string, bool) {
	switch {
	case errors.Is(err, ErrSubjectRetirementActive):
		return "subject_retirement_active", true
	case errors.Is(err, ErrSubjectErasing):
		return "subject_erasing", true
	case errors.Is(err, ErrSubjectErased):
		return "subject_erased", true
	case errors.Is(err, ErrTooManySubjects):
		return "too_many_subjects", true
	}
	return "", false
}

// ExternalIDResolver is the part of the standing port a writer uses when it
// stores a directory external id rather than an account id: it maps each
// external id to the accounts that carry it, so the writer can fence them.
type ExternalIDResolver interface {
	// AccountsByExternalID returns every account whose external id is one of
	// externalIDs.
	AccountsByExternalID(ctx context.Context, externalIDs []string) ([]model.ID, error)
}

// AccountsByExternalID implements ExternalIDResolver.
func (a *Authenticator) AccountsByExternalID(ctx context.Context, externalIDs []string) ([]model.ID, error) {
	var out []model.ID
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		for _, ext := range externalIDs {
			if ext == "" {
				continue
			}
			users, err := drainList(ctx, as.Users().List, byEq("external_id", ext, 0))
			if err != nil {
				return err
			}
			for _, u := range users {
				out = append(out, u.ID)
			}
		}
		return nil
	})
	return out, err
}

var _ ExternalIDResolver = (*Authenticator)(nil)

// ResolveExternalIDs returns the accounts a write names through directory
// external ids, for FencedWrite to fence: r must also resolve external ids, and a
// port that cannot refuses the write.
func ResolveExternalIDs(ctx context.Context, r StandingReader, externalIDs []string) ([]model.ID, error) {
	var refs []string
	for _, ext := range externalIDs {
		if ext != "" {
			refs = append(refs, ext)
		}
	}
	if len(refs) == 0 {
		return nil, nil
	}
	resolver, ok := r.(ExternalIDResolver)
	if !ok {
		return nil, errors.New("auth: the standing port cannot resolve external ids")
	}
	return resolver.AccountsByExternalID(ctx, refs)
}

// Aliases are the values untyped content may name an account by, in the forms a
// writer resolves before it writes: account ids and credential ids (bare, or
// inside "user:<id>", User::"<id>" or Principal::"<id>"), and email addresses.
type Aliases struct {
	// IDs are canonical ids: an account's, or one of its sessions' or tokens'.
	IDs []model.ID
	// Emails are email addresses, compared normalized.
	Emails []string
}

// AliasResolver is the part of the standing port a writer of untyped content
// uses: it maps aliases to the accounts they name.
type AliasResolver interface {
	// AccountsByAlias returns every account one of aliases names: an id that is
	// an account's, the owner of a session or token whose id it is, and the
	// account holding an email address.
	AccountsByAlias(ctx context.Context, aliases Aliases) ([]model.ID, error)
}

// canonicalID is a canonical id anywhere in untyped content.
var canonicalID = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// emailAddress is an email address anywhere in untyped content.
var emailAddress = regexp.MustCompile(`[A-Za-z0-9._%+'-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+`)

// ContentAliases returns the aliases untyped content carries: every canonical
// id in it, whatever surrounds it, and every email address.
func ContentAliases(content string) Aliases {
	var out Aliases
	seen := map[string]bool{}
	for _, tok := range canonicalID.FindAllString(content, -1) {
		if id, err := model.ParseID(tok); err == nil && !id.IsZero() && !seen[id.String()] {
			seen[id.String()] = true
			out.IDs = append(out.IDs, id)
		}
	}
	for _, addr := range emailAddress.FindAllString(content, -1) {
		if e := normalizeEmail(addr); !seen[e] {
			seen[e] = true
			out.Emails = append(out.Emails, e)
		}
	}
	return out
}

// ResolveContentAccounts returns the accounts untyped content names, for
// FencedWrite or FenceSubjects to fence: r must also resolve aliases, and a port
// that cannot refuses the write.
func ResolveContentAccounts(ctx context.Context, r StandingReader, content string) ([]model.ID, error) {
	aliases := ContentAliases(content)
	if len(aliases.IDs) == 0 && len(aliases.Emails) == 0 {
		return nil, nil
	}
	resolver, ok := r.(AliasResolver)
	if !ok {
		return nil, errors.New("auth: the standing port cannot resolve the aliases content names accounts by")
	}
	return resolver.AccountsByAlias(ctx, aliases)
}

// AccountsByAlias implements AliasResolver, in one auth-partition view.
func (a *Authenticator) AccountsByAlias(ctx context.Context, aliases Aliases) ([]model.ID, error) {
	var out []model.ID
	seen := map[model.ID]bool{}
	add := func(id model.ID) {
		if !id.IsZero() && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		for _, id := range aliases.IDs {
			if _, err := as.Users().Get(ctx, id); err == nil {
				add(id)
				continue
			} else if !errors.Is(err, store.ErrNotFound) {
				return err
			}
			if s, err := as.Sessions().Get(ctx, id); err == nil {
				add(s.UserID)
				continue
			} else if !errors.Is(err, store.ErrNotFound) {
				return err
			}
			if t, err := as.Tokens().Get(ctx, id); err == nil {
				add(t.UserID)
				add(t.ActAsUserID)
			} else if !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
		for _, e := range aliases.Emails {
			users, err := drainList(ctx, as.Users().List, byEq("email", normalizeEmail(e), 0))
			if err != nil {
				return err
			}
			for _, u := range users {
				add(u.ID)
			}
		}
		return nil
	})
	return out, err
}

var _ AliasResolver = (*Authenticator)(nil)
