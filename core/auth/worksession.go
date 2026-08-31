// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Work-session credentials are deliberately narrower than any built-in role.
// They can take/renew/release the exact SID's lease and perform only the fenced
// execution mutations the sessions handler admits. They cannot read the
// backlog, control runs, administer leases, mint credentials, or reach another
// module.
const (
	WorkSessionCredentialPurpose               = "work-session"
	WorkSessionLeaseWrite           Permission = "sessions:lease:write"
	WorkSessionWorkWrite            Permission = "sessions:work:write"
	workSessionRole                            = "work-session"
	workSessionScope                           = "sessions:lease:write sessions:work:write"
	DefaultWorkSessionCredentialTTL            = 30 * time.Minute
)

// WorkSessionCredentialSpec is the trusted runtime input to the private product
// issuer. SessionRef is an Olivares canonical SID; RunRef is the exact supervised
// runtime generation; AgentRef is the independently authenticated agent identity
// driving it (optional for a human-operated run). All fields are server-derived.
type WorkSessionCredentialSpec struct {
	Tenant     model.TenantID
	SessionRef string
	RunRef     string
	AgentRef   string
	ClaimFence int64
}

// WorkSessionCredential is the show-once bearer plus its non-sensitive
// revocation handle and fixed expiry. The bearer is injected into the launched
// process and is never persisted outside the auth credential row's secret hash.
type WorkSessionCredential struct {
	Token      string
	ID         model.ID
	Tenant     model.TenantID
	SessionRef string
	RunRef     string
	AgentRef   string
	ClaimFence int64
	ExpiresAt  time.Time
}

// IssueWorkSessionCredential mints the only production credential that may
// populate Principal.SessionIdentity. It is intentionally unavailable to a
// human/admin principal: only a locally authenticated SYSTEM path may call it,
// after the sessions runtime has minted the canonical SID and acquired its live
// Claim. Public token issuance has no SessionRef input.
//
// Trust seam: core/auth cannot import the sessions module to re-read Claim
// liveness without reversing the composition dependency. The composition-root
// adapter is therefore the narrow in-process capability and calls this only
// after Claim acquisition; the method independently validates every binding
// shape and limits authority to its private purpose ceiling.
func (a *Authenticator) IssueWorkSessionCredential(
	ctx context.Context,
	actor Principal,
	spec WorkSessionCredentialSpec,
) (WorkSessionCredential, error) {
	if !actor.IsSystemOperator() {
		return WorkSessionCredential{}, ErrRoleCeiling
	}
	if err := validateWorkSessionCredentialSpec(spec); err != nil {
		return WorkSessionCredential{}, err
	}

	cred, err := NewCredential(PrefixToken)
	if err != nil {
		return WorkSessionCredential{}, err
	}
	var expiresAt time.Time
	var stored model.APIToken
	err = a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		now := a.clock.Now()
		// A resume reuses canonical SID+run_ref. If cleanup of the previous
		// process could not reach auth storage, its still-live bearer must not
		// regain authority when the successor Claim appears. The fence floor is
		// computed across every parseable historical row, including revoked and
		// expired ones: a delayed issuer at N must never revoke a successor at
		// N+1 and recreate stale work authority.
		previous, err := drainList(ctx, as.Tokens().List, model.Query{Filters: []model.Filter{
			{Column: "bound_tenant_id", Op: model.OpEq, Value: spec.Tenant.String()},
			{Column: "purpose", Op: model.OpEq, Value: WorkSessionCredentialPurpose},
			{Column: "session_ref", Op: model.OpEq, Value: spec.SessionRef},
		}})
		if err != nil {
			return err
		}
		var maxFence int64
		for _, old := range previous {
			oldRunRef, oldFence, ok := parseWorkSessionCredentialName(old.Name)
			if ok && oldRunRef == spec.RunRef && oldFence > maxFence {
				maxFence = oldFence
			}
		}
		if spec.ClaimFence < maxFence {
			return fmt.Errorf("%w: stale work-session claim fence", ErrUnauthenticated)
		}
		if spec.ClaimFence == maxFence {
			for _, old := range previous {
				oldRunRef, oldFence, ok := parseWorkSessionCredentialName(old.Name)
				if ok && oldRunRef == spec.RunRef && oldFence == maxFence &&
					!workSessionBindingMatches(old, spec) {
					return fmt.Errorf("%w: work-session binding changed without a new claim fence", ErrUnauthenticated)
				}
			}
		}
		for _, old := range previous {
			oldRunRef, _, ok := parseWorkSessionCredentialName(old.Name)
			if !ok || oldRunRef != spec.RunRef {
				continue
			}
			if old.Revoked || old.ExpiresAt == nil || workSessionCredentialExpired(*old.ExpiresAt, now) {
				continue
			}
			if err := revokeTokenTree(ctx, as, actor, old.ID); err != nil {
				return err
			}
		}
		expiresAt = now.Time().Add(DefaultWorkSessionCredentialTTL)
		expiresTS := model.NewTimestamp(expiresAt)
		token, err := as.Tokens().Create(ctx, model.APIToken{
			Name: workSessionCredentialName(spec.RunRef, spec.ClaimFence), Selector: cred.Selector, SecretHash: cred.SecretHash,
			BoundTenantID: spec.Tenant, Role: workSessionRole, ExpiresAt: &expiresTS,
			Purpose: WorkSessionCredentialPurpose, Scope: workSessionScope,
			AgentRef: spec.AgentRef, SessionRef: spec.SessionRef,
		})
		if err != nil {
			return err
		}
		stored = token
		return metaAudit(ctx, as, actor, "work_session_credential.issue", "core.api_token", token.ID,
			map[string]any{"purpose": WorkSessionCredentialPurpose})
	})
	if err != nil {
		return WorkSessionCredential{}, err
	}
	return WorkSessionCredential{
		Token: cred.Token, ID: stored.ID, Tenant: stored.BoundTenantID,
		SessionRef: stored.SessionRef,
		RunRef:     spec.RunRef,
		AgentRef:   stored.AgentRef,
		ClaimFence: spec.ClaimFence,
		ExpiresAt:  expiresAt,
	}, nil
}

// RevokeWorkSessionCredential revokes only the exact credential minted for the
// expected runtime binding. Purpose and all server-derived binding facts are
// checked in the same transaction, so a crossed sibling handle can neither
// revoke a work credential nor turn cleanup into a general token revoker.
func (a *Authenticator) RevokeWorkSessionCredential(
	ctx context.Context,
	actor Principal,
	id model.ID,
	expected WorkSessionCredentialSpec,
) error {
	if !actor.IsSystemOperator() || id.IsZero() {
		return ErrRoleCeiling
	}
	if err := validateWorkSessionCredentialSpec(expected); err != nil {
		return err
	}
	return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		token, err := as.Tokens().Get(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, ok := workSessionPrincipal(token); !ok || token.BoundTenantID != expected.Tenant ||
			token.SessionRef != expected.SessionRef || token.AgentRef != expected.AgentRef ||
			token.Name != workSessionCredentialName(expected.RunRef, expected.ClaimFence) {
			return ErrUnauthenticated
		}
		return revokeTokenTree(ctx, as, actor, id)
	})
}

// RenewWorkSessionCredential slides the SAME bearer expiry after the runtime
// successfully renews the canonical session Claim. It never rotates or returns
// secret material, never resurrects an expired/revoked token, and accepts only
// the dedicated purpose. Thus an active long session keeps one credential while
// a crashed runtime loses it after the short fixed window.
func (a *Authenticator) RenewWorkSessionCredential(
	ctx context.Context,
	actor Principal,
	id model.ID,
	expected WorkSessionCredentialSpec,
) (time.Time, error) {
	if !actor.IsSystemOperator() || id.IsZero() {
		return time.Time{}, ErrRoleCeiling
	}
	if err := validateWorkSessionCredentialSpec(expected); err != nil {
		return time.Time{}, err
	}
	var expiresAt time.Time
	err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		token, err := as.Tokens().Get(ctx, id)
		if err != nil {
			return err
		}
		now := a.clock.Now()
		if _, ok := workSessionPrincipal(token); !ok || token.Revoked ||
			token.ExpiresAt == nil || workSessionCredentialExpired(*token.ExpiresAt, now) ||
			token.BoundTenantID != expected.Tenant || token.SessionRef != expected.SessionRef ||
			token.AgentRef != expected.AgentRef ||
			token.Name != workSessionCredentialName(expected.RunRef, expected.ClaimFence) {
			return ErrUnauthenticated
		}
		expiresAt = now.Time().Add(DefaultWorkSessionCredentialTTL)
		expiresTS := model.NewTimestamp(expiresAt)
		token.ExpiresAt = &expiresTS
		updated, err := as.Tokens().Update(ctx, token)
		if err != nil {
			return err
		}
		return metaAudit(ctx, as, actor, "work_session_credential.renew", "core.api_token", updated.ID,
			map[string]any{"purpose": WorkSessionCredentialPurpose})
	})
	return expiresAt, err
}

func validateWorkSessionCredentialSpec(spec WorkSessionCredentialSpec) error {
	if spec.Tenant.IsZero() || spec.Tenant.IsSystem() || !validWorkSessionRef(spec.SessionRef) ||
		!validWorkSessionRunRef(spec.RunRef) || spec.ClaimFence < 1 {
		return fmt.Errorf("%w: business tenant, canonical session_ref, run_ref, and claim_fence required", ErrInvalidToken)
	}
	if strings.ContainsAny(spec.AgentRef, "\n\r\x00") || len(spec.AgentRef) > 512 {
		return fmt.Errorf("%w: invalid agent_ref", ErrInvalidToken)
	}
	return nil
}

func validWorkSessionRef(ref string) bool {
	const prefix = "osn_"
	if !strings.HasPrefix(ref, prefix) || len(ref) != len(prefix)+36 {
		return false
	}
	id, err := model.ParseID(strings.TrimPrefix(ref, prefix))
	return err == nil && ref == prefix+id.String()
}

func validWorkSessionRunRef(ref string) bool {
	id, err := model.ParseID(ref)
	return err == nil && ref == id.String() && !id.IsZero()
}

func workSessionCredentialName(runRef string, claimFence int64) string {
	return "sessions-runtime:" + runRef + ":" + strconv.FormatInt(claimFence, 10)
}

func parseWorkSessionCredentialName(name string) (string, int64, bool) {
	const prefix = "sessions-runtime:"
	if !strings.HasPrefix(name, prefix) {
		return "", 0, false
	}
	rest := strings.TrimPrefix(name, prefix)
	separator := strings.LastIndexByte(rest, ':')
	if separator < 0 {
		return "", 0, false
	}
	runRef := rest[:separator]
	fence, err := strconv.ParseInt(rest[separator+1:], 10, 64)
	if err != nil || !validWorkSessionRunRef(runRef) || fence < 1 ||
		name != workSessionCredentialName(runRef, fence) {
		return "", 0, false
	}
	return runRef, fence, true
}

func workSessionBindingMatches(token model.APIToken, expected WorkSessionCredentialSpec) bool {
	return token.BoundTenantID == expected.Tenant && token.SessionRef == expected.SessionRef &&
		token.AgentRef == expected.AgentRef &&
		token.Name == workSessionCredentialName(expected.RunRef, expected.ClaimFence)
}

// workSessionCredentialExpired uses a closed upper bound: a bearer is dead at
// its exact deadline, matching runtime WorkSessionCredential.Expired. Ordinary
// legacy tokens retain their historical strict-Before behavior.
func workSessionCredentialExpired(expiresAt, now model.Timestamp) bool {
	return !now.Time().Before(expiresAt.Time())
}

func workSessionPrincipal(token model.APIToken) (Principal, bool) {
	runRef, claimFence, validName := parseWorkSessionCredentialName(token.Name)
	if token.Purpose != WorkSessionCredentialPurpose || token.IsSuperadmin || token.UserID != "" ||
		token.BoundTenantID.IsZero() || token.BoundTenantID.IsSystem() ||
		token.Role != workSessionRole || token.Scope != workSessionScope ||
		token.ExpiresAt == nil || token.Audience != "" || !token.ActAsUserID.IsZero() ||
		!token.ParentTokenID.IsZero() || !validWorkSessionRef(token.SessionRef) || !validName ||
		!token.WorkspaceID.IsZero() || token.SessionRunRef != "" || token.SessionFence != 0 ||
		strings.ContainsAny(token.AgentRef, "\n\r\x00") || len(token.AgentRef) > 512 {
		return Principal{}, false
	}
	p := newPrincipal(KindToken, "", token.ID, false, token.Name,
		map[model.TenantID]string{token.BoundTenantID: workSessionRole}, nil)
	p.AgentIdentity = token.AgentRef
	p.SessionIdentity = token.SessionRef
	p.SessionRunRef = runRef
	p.SessionFence = claimFence
	return p.withRestrictedPermissions(token.BoundTenantID,
		WorkSessionLeaseWrite, WorkSessionWorkWrite), true
}
