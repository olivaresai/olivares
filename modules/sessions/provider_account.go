// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions/accountname"
)

// Provider accounts.
//
// An account is a provider profile that carries a NAME. It is not a second
// object: the account reference is the profile's own ppf_ reference, the homes
// are the profile's homes, and the launch path is the profile's launch path. What
// the account adds is eight nullable columns on the profile row, and NULL is the
// meaning — a row with no account_name is a profile and not an account. Nothing
// reads a legacy profile as an account by implication: `adopt` is the only way an
// existing profile becomes one.
//
// ⛔ THE DATABASE DECIDES A NAME, NOT THE WRITER. A name is unique per (tenant,
// execution environment) across every driver and every state, and that is one
// expression index, (tenant_id, environment_ref, COALESCE(account_name,
// profile_ref)), in the module migrations. The generator (package accountname)
// only PROPOSES the first name it does not see in use; a proposal that loses to a
// concurrent writer is refused by the index, and what happens next depends on who
// chose the name:
//
//   - a GENERATED name retries with the next free suffix, at most
//     maxAccountNameAttempts times, then answers 409 naming the exhaustion;
//   - an OPERATOR-GIVEN name answers 409 with that name. It is never replaced by
//     another one: an operator who asked for claude-b and was handed claude-c
//     would address the wrong account for the rest of its life.
//
// ⛔ NO ACCOUNT COLUMN REACHES ProviderHomeSnapshot. The snapshot's JSON names are
// the K4 launch digest, so a column added there would move the digest of every
// launch the moment a profile is adopted. The account columns are read by the
// account plane and by nothing on the launch path.

// sessions.provider_profile ACCOUNT columns. All nullable (provider_profile.go
// declares them on the profile descriptor, so the reconciler adds them to an
// existing table at the next boot).
const (
	// colPPAccountName is the account's name; NULL means the row is not an account.
	colPPAccountName = "account_name"
	// colPPHomeMode says who made the home: the product (managed) or the operator,
	// whose existing home was adopted (adopted).
	colPPHomeMode = "home_mode"
	// colPPHomeGeneration counts the product-managed recreations of the home. An
	// adopted home has none.
	colPPHomeGeneration = "home_generation"
	colPPOwnerRef       = "owner_ref"
	colPPReleaseRef     = "release_ref"
	colPPPendingRelease = "pending_release"
	// colPPIsolationLevel is the boundary the account's child actually runs
	// behind: shared (the engine's own service user) or dedicated (a locked user of
	// its own). It is stated, never inferred.
	colPPIsolationLevel = "isolation_level"
	// colPPOSUser is the dedicated user's name, set only when the isolation is
	// dedicated.
	colPPOSUser = "os_user"
)

// Home modes.
const (
	// AccountHomeManaged is a home the product created for the account.
	AccountHomeManaged = "managed"
	// AccountHomeAdopted is a home the operator already had, named by adopt. The
	// product does not own its contents and never archives or recreates it.
	AccountHomeAdopted = "adopted"
)

// Isolation levels.
const (
	// AccountIsolationShared runs the child as the engine's own service user: the
	// home is private by file mode, and any process of that user can read it.
	AccountIsolationShared = "shared"
	// AccountIsolationDedicated runs the child as a locked system user of its own:
	// the boundary is the kernel's.
	AccountIsolationDedicated = "dedicated"
)

// maxAccountNameAttempts bounds how many names one adopt proposes before it
// reports that concurrent writers took every one of them.
const maxAccountNameAttempts = 16

// accountNamePageSize is the page the generator reads the environment's names in.
const accountNamePageSize = 1000

// Account-plane errors. Each carries the status the API answers with; none carries
// a path.
var (
	// ErrAccountNotFound answers a reference that is not an account: unknown, or a
	// profile nobody has named.
	ErrAccountNotFound = &runErr{http.StatusNotFound, "provider account not found"}
	// ErrAccountAlreadyNamed refuses to adopt a profile that is already an account.
	// A name changes only through its own verb, never as a side effect of adopt.
	ErrAccountAlreadyNamed = &runErr{http.StatusConflict, "this provider profile is already an account; adopt names a profile once"}
	// errAccountAdoptContended is the explicit-name answer when the profile row
	// itself changed under every attempt: the name was not the cause, so it is not
	// reported as taken.
	errAccountAdoptContended = &runErr{http.StatusConflict, "the provider profile changed during every attempt to adopt it; retry"}
)

func accountNameTaken(name string) error {
	return &runErr{http.StatusConflict, fmt.Sprintf(
		"account name %q is already taken in this environment (by an account of any driver or state; "+
			"an archived account keeps its name); choose another name", name)}
}

func accountNamesExhausted(driver string) error {
	return &runErr{http.StatusConflict, fmt.Sprintf(
		"generated account names for driver %q were exhausted: %d attempts each lost the name they proposed "+
			"to a concurrent writer; retry, or name the account explicitly", driver, maxAccountNameAttempts)}
}

// accountNameRefusal maps the naming rule's errors onto the API's answers.
func accountNameRefusal(err error) error {
	if errors.Is(err, accountname.ErrNoFreeName) {
		return &runErr{http.StatusConflict, err.Error()}
	}
	return &runErr{http.StatusUnprocessableEntity, err.Error()}
}

// ProviderAccount is the module's view of one account: the profile it is, plus
// the account columns.
type ProviderAccount struct {
	ProviderProfile
	Name           string
	HomeMode       string
	HomeGeneration int64
	OwnerRef       string
	ReleaseRef     string
	PendingRelease string
	IsolationLevel string
	OSUser         string
}

// ProviderAccountFilter narrows a list. Empty fields do not narrow.
type ProviderAccountFilter struct {
	EnvironmentRef string
	Driver         string
	State          string
}

func accountNamed(rec model.Record) bool { return rec.String(colPPAccountName) != "" }

func accountFromRecord(rec model.Record) ProviderAccount {
	return ProviderAccount{
		ProviderProfile: profileFromRecord(rec),
		Name:            rec.String(colPPAccountName),
		HomeMode:        rec.String(colPPHomeMode),
		HomeGeneration:  rec.Int(colPPHomeGeneration),
		OwnerRef:        rec.String(colPPOwnerRef),
		ReleaseRef:      rec.String(colPPReleaseRef),
		PendingRelease:  rec.String(colPPPendingRelease),
		IsolationLevel:  rec.String(colPPIsolationLevel),
		OSUser:          rec.String(colPPOSUser),
	}
}

// ListProviderAccounts returns the tenant's accounts, oldest first (the store
// orders by id, and ids are time-ordered): NAMED rows only, in every state unless
// the filter narrows it. A profile with no name is not an account and is never
// listed as one.
func (m *Module) ListProviderAccounts(ctx context.Context, tenant model.TenantID, f ProviderAccountFilter, q model.Query) ([]ProviderAccount, model.Page, error) {
	if m.data == nil {
		return nil, model.Page{}, errNoData
	}
	q.Filters = append(q.Filters, model.Filter{Column: colPPAccountName, Op: model.OpNotNull})
	if f.EnvironmentRef != "" {
		q.Filters = append(q.Filters, eq(colPPEnvRef, f.EnvironmentRef))
	}
	if f.Driver != "" {
		q.Filters = append(q.Filters, eq(colPPDriver, f.Driver))
	}
	if f.State != "" {
		q.Filters = append(q.Filters, eq(colPPState, f.State))
	}
	var out []ProviderAccount
	var page model.Page
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(providerProfileKind)
		if err != nil {
			return err
		}
		recs, p, err := repo.List(ctx, q)
		if err != nil {
			return err
		}
		out = make([]ProviderAccount, 0, len(recs))
		for _, rec := range recs {
			out = append(out, accountFromRecord(rec))
		}
		page = p
		return nil
	})
	return out, page, err
}

// GetProviderAccount reads one account by its reference, which is its profile's
// reference. A profile nobody has named answers exactly like an unknown one.
func (m *Module) GetProviderAccount(ctx context.Context, tenant model.TenantID, ref string) (ProviderAccount, error) {
	if m.data == nil {
		return ProviderAccount{}, errNoData
	}
	if !validProfileRef(ref) {
		return ProviderAccount{}, ErrAccountNotFound
	}
	var out ProviderAccount
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		rec, err := findProfileRec(ctx, sc, ref)
		if errors.Is(err, ErrProfileNotFound) {
			return ErrAccountNotFound
		}
		if err != nil {
			return err
		}
		if !accountNamed(rec) {
			return ErrAccountNotFound
		}
		out = accountFromRecord(rec)
		return nil
	})
	return out, err
}

// adoptAttempt is what one attempt learned before its write: the profile's driver
// and environment, read in the attempt's own transaction, and the name it wrote.
type adoptAttempt struct {
	driver, environmentRef, proposed string
}

// AdoptProviderAccount makes an existing provider profile an account named name,
// or under a generated name when name is empty. It is the ONLY way an existing
// profile becomes an account.
//
// The home is the operator's own, so the account records home_mode adopted and
// isolation_level shared: an adopted home runs under the engine's service user,
// and it is never labelled dedicated. Adopt transfers no ownership of anything on
// disk and touches no file. The write and its audit event are one transaction,
// and the unique index — not this function — decides whether the name is free.
func (m *Module) AdoptProviderAccount(ctx context.Context, actor auth.Principal, tenant model.TenantID, profileRef, name string) (ProviderAccount, error) {
	if m.data == nil {
		return ProviderAccount{}, errNoData
	}
	if !validProfileRef(profileRef) {
		return ProviderAccount{}, ErrProfileNotFound
	}
	if name != "" {
		// Checked exactly as given: a name is never trimmed or lowercased into one
		// the operator did not type.
		if err := accountname.Validate(name); err != nil {
			return ProviderAccount{}, accountNameRefusal(err)
		}
	}
	var lost map[string]bool
	var last adoptAttempt
	for attempt := 0; attempt < maxAccountNameAttempts; attempt++ {
		out, at, err := m.adoptOnce(ctx, actor, tenant, profileRef, name, lost)
		if err == nil {
			return out, nil
		}
		if !errors.Is(err, store.ErrConflict) {
			return ProviderAccount{}, err
		}
		last = at
		named, held, cerr := m.adoptLossCause(ctx, tenant, profileRef, at.environmentRef, name)
		if cerr != nil {
			return ProviderAccount{}, cerr
		}
		switch {
		case named:
			return ProviderAccount{}, ErrAccountAlreadyNamed
		case name != "" && held:
			return ProviderAccount{}, accountNameTaken(name)
		case name == "":
			if lost == nil {
				lost = map[string]bool{}
			}
			lost[at.proposed] = true
		}
	}
	if name != "" {
		return ProviderAccount{}, errAccountAdoptContended
	}
	return ProviderAccount{}, accountNamesExhausted(last.driver)
}

// adoptOnce is one attempt: read the profile, choose the name, write it and its
// audit event, in one transaction. lost holds the generated names earlier
// attempts proposed and lost, so a retry proposes the next one even when its read
// cannot see the writer that took them.
func (m *Module) adoptOnce(ctx context.Context, actor auth.Principal, tenant model.TenantID, ref, name string, lost map[string]bool) (ProviderAccount, adoptAttempt, error) {
	var out ProviderAccount
	var at adoptAttempt
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(providerProfileKind)
		if err != nil {
			return err
		}
		rec, err := findProfileRec(ctx, sc, ref)
		if err != nil {
			return err
		}
		at.driver, at.environmentRef = rec.String(colPPDriver), rec.String(colPPEnvRef)
		if rec.String(colPPState) == ProfileRetired {
			return ErrProfileRetired
		}
		if accountNamed(rec) {
			return ErrAccountAlreadyNamed
		}
		at.proposed = name
		if at.proposed == "" {
			taken, err := accountNamesIn(ctx, repo, at.environmentRef)
			if err != nil {
				return err
			}
			for n := range lost {
				taken[n] = true
			}
			proposed, err := accountname.NextName(at.driver, taken)
			if err != nil {
				return accountNameRefusal(err)
			}
			at.proposed = proposed
		}
		rec[colPPAccountName] = at.proposed
		rec[colPPHomeMode] = AccountHomeAdopted
		rec[colPPIsolationLevel] = AccountIsolationShared
		updated, err := repo.Update(ctx, rec)
		if err != nil {
			return err
		}
		out = accountFromRecord(updated)
		return appendAccountAudit(ctx, sc, actor, "adopt", out)
	})
	return out, at, err
}

// accountNamesIn reads every name in use in one environment of the scope's
// tenant: every driver and every state, archived included, because an archived
// account keeps its name.
func accountNamesIn(ctx context.Context, repo store.GenericRepo, environmentRef string) (map[string]bool, error) {
	taken := map[string]bool{}
	q := model.Query{
		Filters: []model.Filter{
			eq(colPPEnvRef, environmentRef),
			{Column: colPPAccountName, Op: model.OpNotNull},
		},
		Limit: accountNamePageSize,
	}
	for {
		recs, page, err := repo.List(ctx, q)
		if err != nil {
			return nil, err
		}
		for _, rec := range recs {
			taken[rec.String(colPPAccountName)] = true
		}
		if !page.HasMore || page.Cursor == "" {
			return taken, nil
		}
		q.Cursor = page.Cursor
	}
}

// adoptLossCause reads, after the index refused a write, what refused it: the
// profile was named meanwhile by another adopt, or name is now held by another
// row of the environment. Neither means the row itself moved under the attempt.
func (m *Module) adoptLossCause(ctx context.Context, tenant model.TenantID, ref, environmentRef, name string) (named, held bool, err error) {
	err = m.data.View(ctx, tenant, func(sc store.Scope) error {
		rec, err := findProfileRec(ctx, sc, ref)
		if err != nil {
			return err
		}
		if accountNamed(rec) {
			named = true
			return nil
		}
		if name == "" {
			return nil
		}
		repo, err := sc.Ext(providerProfileKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(ctx, model.Query{
			Filters: []model.Filter{eq(colPPEnvRef, environmentRef), eq(colPPAccountName, name)},
			Limit:   1,
		})
		if err != nil {
			return err
		}
		held = len(recs) > 0
		return nil
	})
	return named, held, err
}

// appendAccountAudit seals one account act in the caller's transaction, so the
// row and its evidence commit or roll back together.
func appendAccountAudit(ctx context.Context, sc store.Scope, actor auth.Principal, verb string, a ProviderAccount) error {
	meta := map[string]any{
		"profile_ref":     a.Ref,
		"name":            a.Name,
		"driver":          a.Driver,
		"environment_ref": a.EnvironmentRef,
		"home_mode":       a.HomeMode,
		"isolation_level": a.IsolationLevel,
		// The absolute homes go to the ledger, the one reader entitled to them, and
		// never to a list or a DTO.
		"config_home": a.ConfigHome,
		"user_home":   a.UserHome,
	}
	if a.OSUser != "" {
		meta["os_user"] = a.OSUser
	}
	_, err := sc.Audit().Append(ctx, model.AuditDraft{
		Actor:      orSystem(actor.Actor()),
		ActorKind:  orSystemKind(actor.ActorKind()),
		Action:     "sessions.provider_account." + verb,
		TargetKind: providerProfileKind,
		TargetID:   a.ID,
		Meta:       meta,
	})
	return err
}
