// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A writer that stores a reference to an account in a tenant, where the
// reference would let the account act or bind it to a duty there, first reads
// the account's standing in that tenant through this port, refuses while a
// retirement is in progress, and then opens its transaction by pinning the
// account's authority version it read. An offboard that commits in between moves
// that version, so the writer conflicts and reads the new standing.

// Writer refusals for a referenced account's standing.
var (
	// ErrSubjectRetirementActive means a referenced account is being removed from
	// the tenant. Answered 409 subject_retirement_active.
	ErrSubjectRetirementActive = errors.New("auth: a referenced account is being removed from this organization")
	// ErrSubjectErasing means a referenced account is being erased. Answered 409
	// subject_erasing.
	ErrSubjectErasing = errors.New("auth: a referenced account is being erased")
	// ErrSubjectErased means a referenced account was erased. Answered 409
	// subject_erased.
	ErrSubjectErased = errors.New("auth: a referenced account was erased")
)

// Standing is what a writer must know about one referenced account in one
// tenant.
type Standing struct {
	// User is the account.
	User model.ID
	// Exists is false for an id that names no account; such an id can never name
	// one later, because account ids are allocated by the engine.
	Exists bool
	// Retirement is the account's retirement state in the tenant, or empty.
	Retirement model.RetirementState
	// Generation is the retirement generation.
	Generation int64
	// ErasureState is the account's global erasure state, or empty.
	ErasureState string
	// AuthorityVersion is the account's authority version the writer must pin.
	AuthorityVersion int64
}

// Refusal returns the error a fenced writer answers for this standing, or nil.
func (s Standing) Refusal() error {
	switch {
	case s.ErasureState == "erased":
		return ErrSubjectErased
	case s.ErasureState == "erasing":
		return ErrSubjectErasing
	case s.Retirement == model.RetirementRetiring, s.Retirement == model.RetirementBlocked:
		return ErrSubjectRetirementActive
	}
	return nil
}

// StandingReader is the port fenced writers read standing through.
type StandingReader interface {
	// Standing returns the standing of every account in users within tenant.
	Standing(ctx context.Context, tenant model.TenantID, users []model.ID) (map[model.ID]Standing, error)
}

// StandingConsumer is a module that writes counted references outside a
// request, where no request carries a standing port: the composition hands it
// one at boot, before the module starts.
type StandingConsumer interface {
	// UseStanding receives the standing port.
	UseStanding(StandingReader)
}

// Standing implements StandingReader. It reads, in one auth-partition view,
// whether each account exists, its retirement record in tenant and its current
// authority version. The global erasure state stays empty until the erasure
// operations are introduced.
func (a *Authenticator) Standing(ctx context.Context, tenant model.TenantID, users []model.ID) (map[model.ID]Standing, error) {
	out := make(map[model.ID]Standing, len(users))
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		authority, ok := as.(store.AuthUserAuthorityEvidenceScope)
		if !ok {
			return errNoAuthorityReader
		}
		for _, id := range users {
			if _, done := out[id]; done {
				continue
			}
			s := Standing{User: id}
			if _, err := as.Users().Get(ctx, id); err != nil {
				if !errors.Is(err, store.ErrNotFound) {
					return err
				}
				out[id] = s
				continue
			}
			s.Exists = true
			rec, found, err := retirementRecord(ctx, as, id, tenant)
			if err != nil {
				return err
			}
			if found {
				s.Retirement, s.Generation = rec.RetirementState, rec.RetirementGeneration
			}
			ref, err := authority.ReadUserAuthorityFact(ctx, id)
			if err != nil {
				return err
			}
			s.AuthorityVersion = ref.Version
			out[id] = s
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// errNoAuthorityReader means the store's auth scope cannot read an account's
// authority version, so no standing can be pinned.
var errNoAuthorityReader = errors.New("auth: the auth scope exposes no account authority reader")
