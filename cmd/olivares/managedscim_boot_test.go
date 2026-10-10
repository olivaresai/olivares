// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

func TestManagedSCIMCompositionRefusesInvalidPort(t *testing.T) {
	saved := thisEdition.managedSCIM
	defer func() { thisEdition.managedSCIM = saved }()
	thisEdition.managedSCIM = nil
	modules, err := managedSCIMModules()
	if err != nil || len(modules) != 0 {
		t.Fatalf("absent port: %v %v", modules, err)
	}
	for _, tc := range []struct {
		name string
		port any
	}{
		{"missing API", &noAPISCIMModule{}},
		{"missing lifecycle", &noLifecycleSCIMModule{}},
		{"missing binding", &unboundSCIMModule{}},
		{"typed nil", (*nilSCIMModule)(nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			thisEdition.managedSCIM = func() any { return tc.port }
			if _, err := managedSCIMModules(); err == nil {
				t.Fatal("incomplete managed SCIM port accepted")
			}
		})
	}
}

// Each signature double lacks exactly the contract named by its test case.
// Native boot integration exercises the actual module implementation separately.
type apiOnlySCIMModule struct{}

func (*apiOnlySCIMModule) APINamespace() string           { return "managed-scim" }
func (*apiOnlySCIMModule) APIRoutes(api.RouteRegistrar)   {}
func (*apiOnlySCIMModule) Permissions() []auth.Permission { return nil }

type lifecycleOnlySCIMModule struct{}

func (*lifecycleOnlySCIMModule) Descriptor() sdk.Descriptor           { return sdk.Descriptor{Name: "scim-test"} }
func (*lifecycleOnlySCIMModule) Init(context.Context, sdk.Host) error { return nil }
func (*lifecycleOnlySCIMModule) Start(context.Context) error          { return nil }
func (*lifecycleOnlySCIMModule) Stop(context.Context) error           { return nil }

type bindingOnlySCIMModule struct{}

func (*bindingOnlySCIMModule) BindManagedSCIM(store.Store, *auth.Authenticator, *auth.SecretStore, *auth.Authorizer) error {
	return nil
}

type noAPISCIMModule struct {
	lifecycleOnlySCIMModule
	bindingOnlySCIMModule
}
type noLifecycleSCIMModule struct {
	apiOnlySCIMModule
	bindingOnlySCIMModule
}
type unboundSCIMModule struct {
	apiOnlySCIMModule
	lifecycleOnlySCIMModule
}
type nilSCIMModule struct {
	apiOnlySCIMModule
	lifecycleOnlySCIMModule
	bindingOnlySCIMModule
}
