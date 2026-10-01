// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/sdk"
)

// Exercise the real producers through the edition ports. No permit or posture
// is invented by the composition layer, and retained ports observe later writes.
func TestEditionRuntimePortsReadLiveAuthority(t *testing.T) {
	ctx := context.Background()
	gov := governance.New()
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, gov.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gov.UseData(api.NewModuleData(st))
	var tenants []model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		for _, slug := range []string{"edition-runtime-a", "edition-runtime-b"} {
			org, err := sys.CreateOrg(ctx, model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
			if err != nil {
				return err
			}
			tenants = append(tenants, org.TenantID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	credential, err := auth.NewCredential(auth.PrefixToken)
	if err != nil {
		t.Fatal(err)
	}
	var row model.APIToken
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		user, err := as.Users().Create(ctx, model.User{Email: "edition-runtime@example.test", Status: model.StatusActive})
		if err != nil {
			return err
		}
		expires := model.NewTimestamp(time.Now().Add(time.Hour))
		row, err = as.Tokens().Create(ctx, model.APIToken{Name: "edition-runtime", UserID: user.ID,
			Selector: credential.Selector, SecretHash: credential.SecretHash,
			BoundTenantID: tenants[0], Role: auth.RoleViewer, ExpiresAt: &expires})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	authr := auth.NewAuthenticator(st, nil)
	deps := EditionDependencies{Principals: authr, Governance: gov}
	principal, err := authr.Authenticate(ctx, credential.Token)
	if err != nil {
		t.Fatal(err)
	}
	ref, ok := principal.Ref()
	if !ok {
		t.Fatal("authenticated credential has no reference")
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resolved, err := deps.Principals.ResolvePrincipalScope(bounded, ref, tenants[0])
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := resolved.Ref(); !ok || got != ref {
		t.Fatal("resolver substituted credential identity")
	}
	if _, err := deps.Principals.ResolvePrincipalScope(bounded, ref, tenants[1]); !errors.Is(err, auth.ErrPrincipalEvidenceUnavailable) {
		t.Fatalf("foreign-tenant reconstruction = %v", err)
	}
	if _, err := deps.Principals.ResolvePrincipalScope(ctx, ref, tenants[0]); !errors.Is(err, auth.ErrPrincipalEvidenceUnavailable) {
		t.Fatalf("unbounded reconstruction = %v", err)
	}
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		current, err := as.Tokens().Get(ctx, row.ID)
		if err != nil {
			return err
		}
		current.Revoked = true
		_, err = as.Tokens().Update(ctx, current)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.Principals.ResolvePrincipalScope(bounded, ref, tenants[0]); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("retained port reused revoked authority: %v", err)
	}
	if posture, err := deps.Governance.KillSwitchState(ctx, tenants[0]); err != nil || posture.Any() {
		t.Fatalf("initial posture = %+v, %v", posture, err)
	}
	var stopID model.ID
	if err := st.Mutate(ctx, tenants[0], func(sc store.Scope) error {
		repo, err := sc.Ext(model.Kind("governance.killswitch"))
		if err != nil {
			return err
		}
		stop, err := repo.Create(ctx, model.Record{
			"scope_kind": "estate", "status": "active", "reason": "edition port fixture",
			"source": "operator", "engaged_by": "system", "engaged_aal": 1,
			// Stored as its string form, as the governance module writes it.
			"engaged_at": model.NewTimestamp(time.Now()).String(), "engage_audit_seq": 0,
			"revoked_approvals": 0, "reviewed": false,
		})
		stopID = model.ID(stop.String(model.ColID))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if posture, err := deps.Governance.KillSwitchState(ctx, tenants[0]); err != nil || !posture.EstateStopped || posture.EstateStopID != stopID {
		t.Fatalf("retained port missed live stop: %+v, %v", posture, err)
	}
	if posture, err := deps.Governance.KillSwitchState(ctx, tenants[1]); err != nil || posture.Any() {
		t.Fatalf("stop leaked to another tenant: %+v, %v", posture, err)
	}
	// The same boot port fences the actual caller transaction and reads legacy
	// stops while establishing its first generation domain.
	if err := st.Mutate(ctx, tenants[0], func(sc store.Scope) error {
		snapshot, err := deps.Governance.LockKillSwitchState(ctx, sc)
		if err != nil {
			return err
		}
		if snapshot.Tenant() != tenants[0] || snapshot.Generation() != 1 || snapshot.State().EstateStopID != stopID {
			t.Fatal("edition fence did not observe the actual scoped stop")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.View(ctx, tenants[0], func(sc store.Scope) error {
		_, err := deps.Governance.LockKillSwitchState(ctx, sc)
		return err
	}); !errors.Is(err, store.ErrReadOnly) {
		t.Fatalf("edition View claimed a transaction fence: %v", err)
	}
	deps.Governance = governance.New() // Same real producer, missing its data capability.
	if _, err := deps.Governance.KillSwitchState(ctx, tenants[0]); err == nil {
		t.Fatal("missing governance data was treated as a clear posture")
	}
	deps.Principals = auth.NewAuthenticator(nil, nil)
	if _, err := deps.Principals.ResolvePrincipalScope(bounded, ref, tenants[0]); !errors.Is(err, auth.ErrPrincipalEvidenceUnavailable) {
		t.Fatalf("missing authentication data was treated as evidence: %v", err)
	}
}

// Only the injected test lookup can return fixture values. No process environment,
// files, stored secrets or remote backend is consulted by this test.
func TestEditionSecretsUseTheConfiguredResolver(t *testing.T) {
	value, calls := "fixture-one", 0
	lookup := func(key string) string {
		if key == "EDITION_RUNTIME_SECRET_FIXTURE" {
			calls++
			return value
		}
		return ""
	}
	deps := EditionDependencies{Secrets: newSecretResolver(nil, lookup, discardLogger())}
	if calls != 0 {
		t.Fatal("binding eagerly resolved a secret")
	}
	desc := sdk.Descriptor{ConfigFields: []sdk.ConfigField{{Key: "token", Secret: true}}}
	cfg := sdk.Config{Settings: map[string]string{"token": "env:EDITION_RUNTIME_SECRET_FIXTURE"}}
	for _, next := range []string{"fixture-one", "fixture-two"} {
		value = next
		resolved, err := deps.Secrets.Resolve(context.Background(), desc, cfg)
		if err != nil || resolved.Settings["token"] != next {
			t.Fatal("configured resolver did not observe the live fixture lookup")
		}
		if cfg.Settings["token"] != "env:EDITION_RUNTIME_SECRET_FIXTURE" {
			t.Fatal("resolution rewrote the caller's reference")
		}
	}
	if calls != 2 {
		t.Fatal("configured handler was bypassed or called more than once per resolution")
	}
	value = ""
	if _, err := deps.Secrets.Resolve(context.Background(), desc, cfg); err == nil {
		t.Fatal("missing fixture value did not refuse")
	}
	_, err := deps.Secrets.Resolve(context.Background(), desc, sdk.Config{Settings: map[string]string{"token": "inline-fixture"}})
	var inline secret.ErrInlineSecret
	if !errors.As(err, &inline) {
		t.Fatal("configured resolver lost strict inline-secret refusal")
	}
	if _, err := deps.Secrets.Resolve(context.Background(), desc, sdk.Config{Settings: map[string]string{"token": "store:fixture"}}); err == nil {
		t.Fatal("unconfigured backend did not refuse")
	}
}

// This source guard ties the behavior above to the actual boot call. Typed
// fields alone cannot detect a nil or a different, unbound producer.
func TestBootBindsEditionRuntimeCapabilities(t *testing.T) {
	src, err := os.ReadFile("boot.go")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkEditionRuntimeBindings(src); err != nil {
		t.Fatal(err)
	}
	// The same producer names also appear in api.Options; target the edition
	// literal as a group so each mutation alters only the actual edition bind.
	const identityBindings = "Authenticator: authr, FederationService: fedSvc, SecretStore: secretStore,"
	for _, tc := range []struct{ name, from, to string }{
		{"missing resolver", "Principals: authr,", ""},
		{"nil resolver", "Principals: authr", "Principals: nil"},
		{"wrong resolver", "Principals: authr", "Principals: otherAuthr"},
		{"missing governance", "Governance: set.gov,", ""},
		{"nil governance", "Governance: set.gov", "Governance: nil"},
		{"wrong governance", "Governance: set.gov", "Governance: otherGov"},
		{"missing secrets", "Governance: set.gov, Secrets: secretResolver,", "Governance: set.gov,"},
		{"nil secrets", "Governance: set.gov, Secrets: secretResolver", "Governance: set.gov, Secrets: nil"},
		{"wrong secrets", "Governance: set.gov, Secrets: secretResolver", "Governance: set.gov, Secrets: otherResolver"},
		{"second resolver", "Governance: set.gov, Secrets: secretResolver", "Governance: set.gov, Secrets: newSecretResolver(secretStore, osGetenv, log)"},
		{"missing authenticator", identityBindings, "FederationService: fedSvc, SecretStore: secretStore,"},
		{"nil authenticator", identityBindings, "Authenticator: nil, FederationService: fedSvc, SecretStore: secretStore,"},
		{"second authenticator", identityBindings, "Authenticator: auth.NewAuthenticator(st, nil), FederationService: fedSvc, SecretStore: secretStore,"},
		{"missing federation service", identityBindings, "Authenticator: authr, SecretStore: secretStore,"},
		{"nil federation service", identityBindings, "Authenticator: authr, FederationService: nil, SecretStore: secretStore,"},
		{"second federation service", identityBindings, "Authenticator: authr, FederationService: auth.NewFederationService(st, fedSealer, nil, nil, nil), SecretStore: secretStore,"},
		{"missing secret store", identityBindings, "Authenticator: authr, FederationService: fedSvc,"},
		{"nil secret store", identityBindings, "Authenticator: authr, FederationService: fedSvc, SecretStore: nil,"},
		{"second secret store", identityBindings, "Authenticator: authr, FederationService: fedSvc, SecretStore: auth.NewSecretStore(st, secretSealer),"},
		{"missing binding", "editionBindModuleDependencies(", "unrelatedBinding("},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if bytes.Count(src, []byte(tc.from)) != 1 {
				t.Fatal("mutant target moved")
			}
			mutant := []byte(strings.Replace(string(src), tc.from, tc.to, 1))
			if err := checkEditionRuntimeBindings(mutant); err == nil {
				t.Fatal("disconnected or wrong capability passed the boot guard")
			}
		})
	}
	// Move the actual producer line beyond the binding. Parsing still works,
	// but the binding must not run before its secret capability exists.
	creation := "\tsecretResolver := newSecretResolver(secretStore, osGetenv, log)\n"
	mount := "\tapiSrv, err := api.New("
	if bytes.Count(src, []byte(creation)) != 1 || bytes.Count(src, []byte(mount)) != 1 {
		t.Fatal("boot order mutant targets moved")
	}
	late := strings.Replace(string(src), creation, "", 1)
	late = strings.Replace(late, mount, creation+mount, 1)
	if err := checkEditionRuntimeBindings([]byte(late)); err == nil {
		t.Fatal("binding before resolver creation passed")
	}
	// Move the API mount to before the binding in this syntax-only mutant.
	// The later renamed call prevents the guard from counting two api.New calls.
	earlyMount := strings.Replace(string(src), "api.New(", "otherAPI.New(", 1)
	earlyMount = strings.Replace(earlyMount, creation, creation+"\tapi.New(api.Options{})\n", 1)
	if err := checkEditionRuntimeBindings([]byte(earlyMount)); err == nil {
		t.Fatal("API mounted before edition binding passed")
	}
}

func checkEditionRuntimeBindings(src []byte) error {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "boot.go", src, 0)
	if err != nil {
		return err
	}
	var calls, resolverCalls, apiCalls []*ast.CallExpr
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "boot" || fn.Recv != nil {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok {
				if name, ok := call.Fun.(*ast.Ident); ok {
					switch name.Name {
					case "editionBindModuleDependencies":
						calls = append(calls, call)
					case "newSecretResolver":
						resolverCalls = append(resolverCalls, call)
					}
				}
				if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "New" {
					if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "api" {
						apiCalls = append(apiCalls, call)
					}
				}
			}
			return true
		})
	}
	if len(calls) != 1 || len(calls[0].Args) != 5 {
		return errors.New("boot must pass edition dependencies once")
	}
	if len(resolverCalls) != 1 || len(apiCalls) != 1 ||
		resolverCalls[0].Pos() >= calls[0].Pos() || calls[0].Pos() >= apiCalls[0].Pos() {
		return errors.New("boot must create one secret resolver, bind editions, then mount the API")
	}
	deps, ok := calls[0].Args[3].(*ast.CompositeLit)
	if !ok {
		return errors.New("boot must pass its live EditionDependencies")
	}
	want := map[string]string{"Store": "st", "Sessions": "set.sessions",
		"Rows": "api.NewReadRowAuthorizationPort(authz, authr)", "Mutations": "authz",
		"Principals": "authr", "Governance": "set.gov", "Secrets": "secretResolver",
		"Authenticator": "authr", "FederationService": "fedSvc", "SecretStore": "secretStore"}
	for _, elt := range deps.Elts {
		pair, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			return errors.New("edition dependency is not named")
		}
		key, ok := pair.Key.(*ast.Ident)
		if !ok {
			return errors.New("edition dependency has no identifier")
		}
		var value bytes.Buffer
		if err := format.Node(&value, fset, pair.Value); err != nil {
			return err
		}
		if expected, ok := want[key.Name]; ok {
			if value.String() != expected {
				return fmt.Errorf("edition dependency %s = %s, want %s", key.Name, value.String(), expected)
			}
			delete(want, key.Name)
		}
	}
	if len(want) != 0 {
		return fmt.Errorf("boot omitted edition dependencies: %v", want)
	}
	return nil
}
