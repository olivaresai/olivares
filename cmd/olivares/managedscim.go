// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"reflect"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// managedSCIMBinding receives the same live owners as native identity operations.
// The implementation is private; Community's edition port remains nil.
type managedSCIMBinding interface {
	BindManagedSCIM(store.Store, *auth.Authenticator, *auth.SecretStore, *auth.Authorizer) error
}

func managedSCIMModules() ([]api.Module, error) {
	motor := thisEdition.managedSCIM.get()
	if motor == nil {
		return nil, nil
	}
	if v := reflect.ValueOf(motor); v.Kind() == reflect.Pointer && v.IsNil() {
		return nil, fmt.Errorf("managed SCIM: constructor returned a nil instance")
	}
	module, ok := motor.(api.Module)
	if !ok {
		return nil, fmt.Errorf("managed SCIM: constructor does not supply an API module")
	}
	if _, ok := motor.(sdk.Module); !ok {
		return nil, fmt.Errorf("managed SCIM: constructor does not supply lifecycle")
	}
	if _, ok := motor.(managedSCIMBinding); !ok {
		return nil, fmt.Errorf("managed SCIM: constructor does not accept live identity dependencies")
	}
	return []api.Module{module}, nil
}

func bindManagedSCIMModules(modules []api.Module, st store.Store, a *auth.Authenticator, secrets *auth.SecretStore, az *auth.Authorizer) error {
	for _, module := range modules {
		if binding, ok := module.(managedSCIMBinding); ok {
			if err := binding.BindManagedSCIM(st, a, secrets, az); err != nil {
				return fmt.Errorf("bind managed SCIM: %w", err)
			}
		}
	}
	return nil
}
