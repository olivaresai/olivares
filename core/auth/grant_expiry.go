// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// PolicyKindGrantExpiry is the per-tenant default and maximum lifetime for new
// scoped grants and source-scope bindings. An absent policy preserves the
// pre-expiry behavior: grants without expires_at never lapse.
const PolicyKindGrantExpiry = "grant_expiry"

const maxGrantExpirySeconds = int64(365 * 24 * 3600)

// GrantExpiryPolicy bounds new grants. Zero Default means no default; zero
// Maximum means no maximum. A nonzero Maximum requires an expiry on every new grant.
type GrantExpiryPolicy struct {
	Default time.Duration
	Maximum time.Duration
}

// LoadGrantExpiryPolicy reads the one enabled tenant policy. More than one is
// ambiguous and therefore refuses closed rather than selecting a lifetime by accident.
func LoadGrantExpiryPolicy(ctx context.Context, sc store.Scope) (GrantExpiryPolicy, error) {
	policies, _, err := sc.Policies().List(ctx, model.Query{
		Filters: []model.Filter{
			{Column: "kind", Op: model.OpEq, Value: PolicyKindGrantExpiry},
			{Column: "enabled", Op: model.OpEq, Value: true},
		},
		Limit: 2,
	})
	if err != nil {
		return GrantExpiryPolicy{}, err
	}
	if len(policies) == 0 {
		return GrantExpiryPolicy{}, nil
	}
	if len(policies) != 1 {
		return GrantExpiryPolicy{}, errors.New("more than one grant_expiry policy is enabled")
	}
	return parseGrantExpiryPolicy(policies[0].Spec)
}

func parseGrantExpiryPolicy(spec map[string]any) (GrantExpiryPolicy, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return GrantExpiryPolicy{}, err
	}
	var in struct {
		Default int64 `json:"default_seconds"`
		Maximum int64 `json:"maximum_seconds"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return GrantExpiryPolicy{}, fmt.Errorf("invalid grant_expiry policy: %w", err)
	}
	if in.Default < 0 || in.Default > maxGrantExpirySeconds || in.Maximum < 0 || in.Maximum > maxGrantExpirySeconds {
		return GrantExpiryPolicy{}, errors.New("grant_expiry seconds must be between 0 and 31536000")
	}
	if in.Maximum > 0 && in.Default > in.Maximum {
		return GrantExpiryPolicy{}, errors.New("grant_expiry default exceeds its maximum")
	}
	return GrantExpiryPolicy{Default: time.Duration(in.Default) * time.Second, Maximum: time.Duration(in.Maximum) * time.Second}, nil
}

// GrantExpiryRequestError is a refusal of the expiry the caller asked for, as opposed to
// a failure to read the tenant policy: the first is a client error, the second is not.
type GrantExpiryRequestError struct{ Msg string }

func (e *GrantExpiryRequestError) Error() string { return e.Msg }

// ResolveGrantExpiry validates and canonicalizes one requested expiry under policy.
func ResolveGrantExpiry(raw string, now time.Time, policy GrantExpiryPolicy) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if policy.Default == 0 {
			if policy.Maximum > 0 {
				return "", &GrantExpiryRequestError{Msg: "expires_at is required by tenant policy"}
			}
			return "", nil
		}
		return model.NewTimestamp(now.Add(policy.Default)).String(), nil
	}
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return "", &GrantExpiryRequestError{Msg: "expires_at must be an RFC3339 timestamp"}
	}
	if !at.After(now) {
		return "", &GrantExpiryRequestError{Msg: "expires_at must be in the future"}
	}
	if policy.Maximum > 0 && at.After(now.Add(policy.Maximum)) {
		return "", &GrantExpiryRequestError{Msg: "expires_at exceeds the tenant maximum"}
	}
	canonical := model.NewTimestamp(at).String()
	if _, err := model.ParseTimestamp(canonical); err != nil {
		return "", &GrantExpiryRequestError{Msg: "expires_at must be representable as a canonical UTC timestamp"}
	}
	return canonical, nil
}

// ResolveGrantExpiryForTenant applies the tenant's stored policy to one grant.
func ResolveGrantExpiryForTenant(ctx context.Context, sc store.Scope, raw string, now time.Time) (string, error) {
	policy, err := LoadGrantExpiryPolicy(ctx, sc)
	if err != nil {
		return "", err
	}
	return ResolveGrantExpiry(raw, now, policy)
}

// RequesterWorkspace returns the current confined membership workspace. A zero
// workspace denotes tenant-wide authority, never a department chosen by input.
func (a *Authenticator) RequesterWorkspace(ctx context.Context, tenant model.TenantID, user model.ID) (model.ID, error) {
	var workspace model.ID
	err := a.st.AuthView(ctx, func(sc store.AuthScope) error {
		u, err := sc.Users().Get(ctx, user)
		if err != nil {
			return err
		}
		if u.Status != model.StatusActive || u.DeletedAt != nil {
			return ErrInvalidToken
		}
		grants, _, confined, err := loadGrants(ctx, sc, user, u.IsSuperadmin)
		if err != nil {
			return err
		}
		if grants[tenant] == "" && !u.IsSuperadmin {
			return ErrInvalidToken
		}
		standing, err := loadStanding(ctx, sc, user, "")
		if err != nil {
			return err
		}
		if _, excluded := standing.excluded[tenant]; excluded {
			return ErrInvalidToken
		}
		workspace = confined[tenant]
		return nil
	})
	return workspace, err
}
