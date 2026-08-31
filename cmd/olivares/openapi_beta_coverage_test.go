// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/ed25519"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
)

// TestBetaOpenAPICoversEveryMountedModuleRoute is the anti-drift guard for the
// published beta document: it walks the REAL router the production module
// set mounts and asserts the document and the mounted /v1/m/ surface are the same
// set — no mounted route is undocumented, and the document invents no phantom
// route. This is what keeps the SDKs honest as modules add routes over time.
//
// It mounts a MINIMAL server (no runtime, no per-module schema registration):
// route-walking never invokes a handler or touches module data, so building the
// full e2e harness here would only burn -race time (the package is timeout-bound).
func TestBetaOpenAPICoversEveryMountedModuleRoute(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	set, err := buildModules(signer, nil, nil, nil, nil, sourcesConfig{}, log)
	if err != nil {
		t.Fatalf("build modules: %v", err)
	}

	// The document, built from the same module set the server mounts.
	doc := api.ModuleOpenAPIDocument(set.all)
	documented := map[string]bool{}
	for path, item := range doc["paths"].(map[string]any) {
		for method := range item.(map[string]any) {
			documented[strings.ToUpper(method)+" "+path] = true
		}
	}

	// A minimal server: mounts the production module routes with no schema
	// registration (a no-op extension registrar) — enough to build the router.
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"},
		func(store.ExtensionRegistry) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error {
		_, e := sys.EnsureSystemTenant(ctx)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(api.Options{
		Store: st, Authenticator: auth.NewAuthenticator(st, nil), Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token")),
		Logger: log, Version: "test", Modules: set.all,
	})
	if err != nil {
		t.Fatal(err)
	}

	// The live mounted /v1/m/ surface, walked straight off the chi router — an
	// INDEPENDENT path from collectModuleRoutes, so the two can only agree if the
	// document genuinely reflects what is mounted.
	router, ok := srv.Handler().(chi.Routes)
	if !ok {
		t.Fatal("handler is not a chi router")
	}
	mounted := map[string]bool{}
	err = chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		// Spec-canonical form: chi reports a collection route with a trailing
		// slash the OpenAPI path (and the beta builder) does not carry.
		canon := route
		if len(canon) > 1 && strings.HasSuffix(canon, "/") {
			canon = canon[:len(canon)-1]
		}
		if strings.HasPrefix(canon, "/v1/m/") {
			mounted[method+" "+canon] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(mounted) == 0 {
		t.Fatal("walked zero /v1/m/ routes — the harness mounted no module routes")
	}

	for key := range mounted {
		if !documented[key] {
			t.Errorf("mounted module route %q is NOT in the beta OpenAPI document (drift)", key)
		}
	}
	for key := range documented {
		if !mounted[key] {
			t.Errorf("beta OpenAPI documents %q which is not actually mounted (phantom)", key)
		}
	}
	t.Logf("beta document covers %d mounted module routes across %d paths",
		len(mounted), len(doc["paths"].(map[string]any)))
}

// TestBetaOpenAPIClassifiesEveryMutationRequestBody closes the request-body
// census over the production module set. It deliberately has no hand-maintained
// route list or count: a newly registered mutation joins this universe
// automatically and stays red until its handler-backed disposition is explicit.
func TestBetaOpenAPIClassifiesEveryMutationRequestBody(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	set, err := buildModules(signer, nil, nil, nil, nil, sourcesConfig{}, log)
	if err != nil {
		t.Fatalf("build modules: %v", err)
	}

	mutations := map[string]bool{"post": true, "put": true, "patch": true, "delete": true}
	doc := api.ModuleOpenAPIDocument(set.all)
	for path, itemValue := range doc["paths"].(map[string]any) {
		for method, operationValue := range itemValue.(map[string]any) {
			if !mutations[method] {
				continue
			}
			op := operationValue.(map[string]any)
			disposition, ok := op["x-olivares-request-body-disposition"].(string)
			if !ok {
				t.Errorf("%s %s: missing x-olivares-request-body-disposition", strings.ToUpper(method), path)
				continue
			}
			_, hasBody := op["requestBody"]
			switch disposition {
			case "schema-published", "opaque-body":
				if !hasBody {
					t.Errorf("%s %s: disposition %s requires requestBody", strings.ToUpper(method), path, disposition)
				}
			case "bodyless":
				if hasBody {
					t.Errorf("%s %s: bodyless operation publishes requestBody", strings.ToUpper(method), path)
				}
			case "unclassified":
				t.Errorf("%s %s: request-body disposition remains unclassified", strings.ToUpper(method), path)
			default:
				t.Errorf("%s %s: unknown request-body disposition %q", strings.ToUpper(method), path, disposition)
			}
		}
	}
}

// TestBetaModuleRouteSetIsDepIndependent enforces the invariant the whole
// anti-drift story rests on: a module registers the SAME routes regardless of the
// dependencies buildModules wires. The committed snapshot and the coverage guard
// above are both built from the NIL-deps module set; production boot (boot.go)
// uses REAL deps. If a future module gated a reg.Handle on a dep
// (`if m.x != nil { reg.Handle(...) }`), production would serve a route the
// published beta document and the SDKs do not cover, while every nil-deps gate
// stayed green. This test makes that invariant explicit by comparing the route
// set of the nil-deps set against a production-shaped (all-deps-non-nil) set.
func TestBetaModuleRouteSetIsDepIndependent(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Nil-deps shape: exactly what cmd_openapi.go dumps and newHarness mounts.
	_, k1, _ := ed25519.GenerateKey(nil)
	sg1, err := audit.NewSigner(k1)
	if err != nil {
		t.Fatal(err)
	}
	nilDeps, err := buildModules(sg1, nil, nil, nil, nil, sourcesConfig{}, log)
	if err != nil {
		t.Fatalf("build nil-dependency modules: %v", err)
	}

	// Production shape: every optional dep non-nil (signing keys, audit priors, the
	// inference HTTP doer). None of these may change which routes a module registers.
	_, k2, _ := ed25519.GenerateKey(nil)
	sg2, err := audit.NewSigner(k2)
	if err != nil {
		t.Fatal(err)
	}
	_, catalogKey, _ := ed25519.GenerateKey(nil)
	_, policyKey, _ := ed25519.GenerateKey(nil)
	priorPub, _, _ := ed25519.GenerateKey(nil)
	realDeps, err := buildModules(sg2, catalogKey, policyKey, []ed25519.PublicKey{priorPub},
		http.DefaultClient, sourcesConfig{}, log)
	if err != nil {
		t.Fatalf("build real-dependency modules: %v", err)
	}

	a, b := betaRouteKeys(nilDeps.all), betaRouteKeys(realDeps.all)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("module route set depends on construction deps — the nil-deps snapshot would drift from production.\n only nil-deps: %v\n only real-deps: %v",
			minus(a, b), minus(b, a))
	}
}

// betaRouteKeys returns the sorted "METHOD path" set documented for modules.
func betaRouteKeys(mods []api.Module) []string {
	doc := api.ModuleOpenAPIDocument(mods)
	var keys []string
	for path, item := range doc["paths"].(map[string]any) {
		for method := range item.(map[string]any) {
			keys = append(keys, strings.ToUpper(method)+" "+path)
		}
	}
	sort.Strings(keys)
	return keys
}

// minus returns the elements of a not present in b.
func minus(a, b []string) []string {
	set := map[string]bool{}
	for _, x := range b {
		set[x] = true
	}
	var out []string
	for _, x := range a {
		if !set[x] {
			out = append(out, x)
		}
	}
	return out
}
