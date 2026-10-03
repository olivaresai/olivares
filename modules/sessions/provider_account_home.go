// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions/accounthome"
	"github.com/olivaresai/olivares/modules/sessions/accountname"
)

var ErrAccountsRootUnavailable = &runErr{http.StatusServiceUnavailable, "this node has no usable accounts root; provider account homes cannot be created here"}
var errAccountHomeOccupied = &runErr{http.StatusConflict, "account home custody is unresolved; retain the same retry key for operator reconciliation; no existing directory was removed"}
var errAccountHomeRetry = &runErr{http.StatusServiceUnavailable, "account creation outcome is unresolved; retry with the same idempotency key; existing data was retained"}
var errManagedHomeRegistration = &runErr{http.StatusConflict, "managed account homes can only be registered by the account lifecycle"}
var errAccountIntentChanged = &runErr{http.StatusConflict, "idempotency key already names another account creation intention"}

// UseAccountsRoot late-binds the directory this node creates provider account
// homes under, resolved by the composition root from its data directory or from
// the root a deployment configured (accounthome.Root). Empty, or unusable,
// keeps the home-creation verb deny-closed: this node builds no home and says
// so, rather than falling back to a directory nobody chose.
func (m *Module) UseAccountsRoot(root string) {
	root = strings.TrimSpace(root)
	if root == "" {
		m.accountsRoot = ""
		return
	}
	resolved, err := accounthome.Root("", root)
	if err != nil {
		if m.log != nil {
			m.log.Warn("sessions: the accounts root is not usable; creating provider account homes stays deny-closed",
				slog.String("error", err.Error()))
		}
		m.accountsRoot = ""
		return
	}
	m.accountsRoot = resolved
}

// AccountsRoot reports the bound accounts root ("" = none, and no home can be
// created on this node).
func (m *Module) AccountsRoot() string { return m.accountsRoot }

// CreateProviderAccountInput names an intention. A retry key is not authority.
// Empty keeps the legacy one-request behavior; retrying clients must retain a key.
type CreateProviderAccountInput struct {
	Driver, Name, EnvironmentRef, IdempotencyKey string
}

// CreateProviderAccount commits a reservation BEFORE touching the filesystem.
// The second transaction repeats admission and serializes cooperating writers
// through publication and profile registration. A failed commit is not evidence
// of rollback: neither this path nor a later retry deletes any home.
func (m *Module) CreateProviderAccount(ctx context.Context, actor auth.Principal, tenant model.TenantID, in CreateProviderAccountInput) (ProviderAccount, error) {
	if _, confined := actor.ConfinedWorkspaceIn(tenant); confined {
		return ProviderAccount{}, forbiddenErr("workspace confined account writer")
	}
	if m.data == nil {
		return ProviderAccount{}, errNoData
	}
	env, err := m.localEnvironment()
	if err != nil {
		return ProviderAccount{}, err
	}
	if want := strings.TrimSpace(in.EnvironmentRef); want != "" && want != env {
		return ProviderAccount{}, &runErr{http.StatusUnprocessableEntity, "create an account on its own execution environment"}
	}
	driver, err := normalizeDriverKey(in.Driver)
	if err != nil {
		return ProviderAccount{}, err
	}
	name := in.Name
	if name != "" {
		if err := accountname.Validate(name); err != nil {
			return ProviderAccount{}, accountNameRefusal(err)
		}
	}
	key := in.IdempotencyKey
	if key == "" {
		key = string(model.NewID())
	}
	if !validAccountRetryKey(key) {
		return ProviderAccount{}, &runErr{http.StatusUnprocessableEntity, "idempotency_key must be 1 to 128 ASCII letters, digits, hyphens or underscores"}
	}
	if m.accountsRoot == "" {
		return ProviderAccount{}, ErrAccountsRootUnavailable
	}
	// This transaction's scope refuses confined callers before root resolution,
	// directory creation, mode changes, staging, or any other filesystem effect.
	err = m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		profiles, ops, err := accountHomeAdmission(ctx, sc, tenant)
		if err != nil {
			return err
		}
		previous, found, err := accountOperationByKey(ctx, ops, env, key)
		if err != nil {
			return err
		}
		if found {
			return accountOperationIntent(previous, driver, name)
		}
		root, err := plannedAccountRoot(m.accountsRoot)
		if err != nil {
			return ErrAccountsRootUnavailable
		}
		taken, err := accountReservedNames(ctx, sc, profiles, env)
		if err != nil {
			return err
		}
		allocated := name
		if allocated == "" {
			allocated, err = accountname.NextName(driver, taken)
			if err != nil {
				return accountNameRefusal(err)
			}
		} else if taken[allocated] {
			return accountNameTaken(allocated)
		}
		ref := newProfileRef()
		if _, err := (accounthome.Custody{Tenant: tenant.String(), Environment: env, AccountRef: ref}).Relative(); err != nil {
			return accountHomeComponentRefusal(err)
		}
		_, err = ops.Create(ctx, model.Record{colHOKey: key, colHOEnv: env, colHODriver: driver, colHORequested: name, colHOName: allocated, colHORef: ref, colHORoot: root, colHOToken: string(model.NewID()), colHOState: "reserved"})
		return err
	})
	if err != nil {
		var refusal *runErr
		if errors.As(err, &refusal) || errors.Is(err, store.ErrWorkspaceConfinement) {
			return ProviderAccount{}, err
		}
		return ProviderAccount{}, errAccountHomeRetry
	}
	if err := m.accountHomePoint("reserved"); err != nil {
		return ProviderAccount{}, errAccountHomeRetry
	}
	var out ProviderAccount
	err = m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		profiles, ops, err := accountHomeAdmission(ctx, sc, tenant)
		if err != nil {
			return err
		}
		op, found, err := accountOperationByKey(ctx, ops, env, key)
		if err != nil {
			return err
		}
		if !found {
			return errAccountHomeRetry
		}
		if err := accountOperationIntent(op, driver, name); err != nil {
			return err
		}
		if op.String(colHOState) == "complete" {
			rec, err := findProfileRec(ctx, sc, op.String(colHORef))
			if err != nil {
				return err
			}
			out = accountFromRecord(rec)
			return nil
		}
		if op.String(colHOState) != "reserved" {
			return errAccountHomeRetry
		}
		homes, err := m.materializeAccountHome(tenant, op)
		if err != nil {
			return err
		}
		rec := model.Record{colPPRef: op.String(colHORef), colPPDriver: driver, colPPEnvRef: env, colPPConfigHome: homes[0], colPPUserHome: homes[1], colPPDisplayName: "", colPPState: ProfileActive, colPPHomeSlot: activeHomeSlot(env, driver, homes[0]), colPPAccountName: op.String(colHOName), colPPHomeMode: AccountHomeManaged, colPPHomeGeneration: int64(0), colPPIsolationLevel: AccountIsolationShared,
			// The account's homes are where the tool's own login lives, so the account's
			// profile signs in with it, as a New session profile does. Without an auth
			// source the launch was refused (ARCH smoke, HU-01): one way to a ready profile.
			colPPAuthSource: AuthSourceAccountHome}
		created, err := profiles.Create(ctx, rec)
		if err != nil {
			return err
		}
		out = accountFromRecord(created)
		op[colHOState] = "complete"
		if _, err := ops.Update(ctx, op); err != nil {
			return err
		}
		return appendAccountAudit(ctx, sc, actor, "create", out)
	})
	if err != nil {
		// Do not translate an arbitrary conflict into "name taken" and never roll
		// back the filesystem. The operation reservation remains the recovery owner.
		var refusal *runErr
		if errors.As(err, &refusal) || errors.Is(err, store.ErrWorkspaceConfinement) {
			return ProviderAccount{}, err
		}
		return ProviderAccount{}, errAccountHomeRetry
	}
	if err := m.accountHomePoint("committed"); err != nil {
		return ProviderAccount{}, errAccountHomeRetry
	}
	return out, nil
}

func validAccountRetryKey(key string) bool {
	if len(key) == 0 || len(key) > 128 {
		return false
	}
	for _, c := range key {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// accountHomePoint is a narrow interruption seam. Production leaves it nil;
// tests stop at the same committed/physical boundaries used by recovery.
func (m *Module) accountHomePoint(phase string) error {
	if m.accountHomeCheckpoint != nil {
		return m.accountHomeCheckpoint(phase)
	}
	return nil
}

// accountHomeComponentRefusal answers a custody tuple this node cannot turn into
// a directory. It is the caller's identities that are unusable, not the request
// body, so the refusal says which and carries no path.
func accountHomeComponentRefusal(err error) error {
	return &runErr{http.StatusUnprocessableEntity,
		"this account's home cannot be placed on this node: " + err.Error()}
}

// accountHomeRelative is the home an account read may carry: the directory under
// the accounts root for a home the product made, and nothing at all for an
// adopted home, which is the operator's own directory somewhere else. A tuple
// that cannot be expressed as a path is reported as no home rather than as a
// guess, because a reader is never handed a path the product cannot stand behind.
func accountHomeRelative(a ProviderAccount) string {
	rel, err := accounthome.RelativeFor(a.HomeMode, accounthome.Custody{
		Tenant: a.Tenant, Environment: a.EnvironmentRef, AccountRef: a.Ref,
	})
	if err != nil {
		return ""
	}
	return rel
}
