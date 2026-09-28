// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This journal refers to a provider profile, not a human user. It stores no
// actor, credential, secret or human ownership inference. The existing audit
// writer records the authorized actor of the completed account operation.
const (
	accountHomeOperationKind  model.Kind = "sessions.provider_account_home_operation"
	accountHomeOperationTable            = "sessions_provider_account_home_operation"
	colHOKey                             = "retry_key"
	colHOEnv                             = "environment_ref"
	colHODriver                          = "driver"
	colHORequested                       = "requested_name"
	colHOName                            = "account_name"
	colHORef                             = "profile_ref"
	colHORoot                            = "canonical_root"
	colHOToken                           = "custody_token"
	colHOState                           = "state"
)

func registerAccountHomeOperation(reg store.ExtensionRegistry) error {
	fields := []model.FieldSpec{
		{Name: colHOKey, Kind: model.KindText, Principal: model.None("a caller's bounded retry identity, matched only to the same operation: provider_account_home.go:89-134, provider_account_operation.go:73-89")},
		{Name: colHOEnv, Kind: model.KindText, Principal: pdeclNoneEnvRef},
		{Name: colHODriver, Kind: model.KindText, Principal: pdeclNoneDriverKey},
		{Name: colHORequested, Kind: model.KindText, Principal: model.None("an optional account label compared only for retry intent: provider_account_home.go:81-87, provider_account_operation.go:84-89")},
		{Name: colHOName, Kind: model.KindText, Principal: model.None("the allocated provider label, reserved to avoid a second allocation: provider_account_home.go:120-134, provider_account_operation.go:103-111")},
		{Name: colHORef, Kind: model.KindText, Principal: pdeclNoneProfileRef},
		{Name: colHORoot, Kind: model.KindText, Principal: model.None("the canonical filesystem root planned for this operation: provider_account_files.go:188")},
		{Name: colHOToken, Kind: model.KindText, Principal: model.None("a newly minted filesystem custody nonce, never an authentication credential: provider_account_home.go:133, provider_account_files.go:188")},
		{Name: colHOState, Kind: model.KindText, Principal: model.None("reserved or complete, validated by the retirement reader: provider_account_home.go:177-185, provider_account_retirement.go:65-97")},
	}
	return reg.Register(model.EntityDescriptor{Kind: accountHomeOperationKind, Table: accountHomeOperationTable, Fields: fields, Indexes: []model.IndexSpec{
		{Name: "home_operation_key", Columns: []string{model.ColTenantID, colHOEnv, colHOKey}, Unique: true},
		{Name: "home_operation_name", Columns: []string{model.ColTenantID, colHOEnv, colHOName}, Unique: true},
		{Name: "home_operation_ref", Columns: []string{model.ColTenantID, colHORef}, Unique: true},
	}})
}

// All supported profile/name writers use this admission BEFORE any host effect
// and in the transaction that changes registration. Ext rejects confinement;
// the transaction lock orders reservations with manual registration and adopt.
func accountHomeAdmission(ctx context.Context, sc store.Scope, tenant model.TenantID) (store.GenericRepo, store.GenericRepo, error) {
	profiles, err := sc.Ext(providerProfileKind)
	if err != nil {
		return nil, nil, err
	}
	ops, err := sc.Ext(accountHomeOperationKind)
	if err != nil {
		return nil, nil, err
	}
	locker, ok := sc.(store.TransactionLocker)
	if !ok {
		return nil, nil, errAccountHomeRetry
	}
	if err := locker.LockTransaction(ctx, "sessions.provider_account_home:"+tenant.String()); err != nil {
		return nil, nil, err
	}
	return profiles, ops, nil
}

func accountOperationByKey(ctx context.Context, repo store.GenericRepo, env, key string) (model.Record, bool, error) {
	rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colHOEnv, env), eq(colHOKey, key)}, Limit: 1})
	if err != nil {
		return nil, false, err
	}
	if len(rows) == 0 {
		return nil, false, nil
	}
	return rows[0], true, nil
}
func accountOperationIntent(op model.Record, driver, name string) error {
	if op.String(colHODriver) != driver || op.String(colHORequested) != name {
		return errAccountIntentChanged
	}
	return nil
}
func accountReservedNames(ctx context.Context, sc store.Scope, profiles store.GenericRepo, env string) (map[string]bool, error) {
	names, err := accountNamesIn(ctx, profiles, env)
	if err != nil {
		return nil, err
	}
	repo, err := sc.Ext(accountHomeOperationKind)
	if err != nil {
		return nil, err
	}
	q := model.Query{Filters: []model.Filter{eq(colHOEnv, env)}, Limit: accountNamePageSize}
	for {
		rows, page, err := repo.List(ctx, q)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			names[row.String(colHOName)] = true
		}
		if !page.HasMore || page.Cursor == "" {
			return names, nil
		}
		q.Cursor = page.Cursor
	}
}

// rejectManagedRegistration is reused by manual create and adoption. Canonical
// homes resolve alias spellings before this test. A configured but unavailable
// root is not interpreted as "no reserved namespace".
func (m *Module) rejectManagedRegistration(homes ...string) error {
	// Existing custody remains reserved even if an operator changes the root
	// configuration on a later boot. Marker presence is a refusal, not authority.
	for _, home := range homes {
		for dir := home; ; dir = filepath.Dir(dir) {
			if _, err := os.Lstat(filepath.Join(dir, accountCustodyMarker)); err == nil {
				return errManagedHomeRegistration
			} else if !os.IsNotExist(err) {
				return errManagedHomeRegistration
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	if m.accountsRoot == "" {
		return nil
	}
	root, err := plannedAccountRoot(m.accountsRoot)
	if err != nil {
		return ErrAccountsRootUnavailable
	}
	for _, home := range homes {
		rel, err := filepath.Rel(root, home)
		if err != nil {
			return errManagedHomeRegistration
		}
		if rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errManagedHomeRegistration
		}
	}
	return nil
}
