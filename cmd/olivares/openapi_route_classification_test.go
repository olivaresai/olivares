// SPDX-FileCopyrightText: 2026 Olivares AI
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
	"regexp"
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

// TestRoutedOperationsArePublishedOrClassified is the reverse direction of
// TestEveryPublishedOperationExistsInTheRouter (core/api), whose comment
// deliberately did NOT assert it: "there are many, they are a known and
// separately tracked gap". This test IS that tracking:
// after the 2026-10-06 census, EVERY operation the production router mounts
// is either published in one of the two OpenAPI documents (the Community
// public API) or carries a class and a reason in core/api's classification
// table (protocol endpoint, internal mechanism). A new route therefore
// cannot become a silent publication gap: it goes red until it is published
// or explicitly classified, with a reason a reviewer can check.
//
// It fails in three directions, each naming the route:
//   - a mounted operation is neither published nor classified (a gap);
//   - a classification row names an operation that exists in a published
//     document (the table hides a public route — the abuse the table's own
//     comment forbids);
//   - a classification row names an operation the router does not mount and
//     that is not marked optional (a stale row, same lesson as the
//     op-description catalog: a row whose route is gone is red).
func TestRoutedOperationsArePublishedOrClassified(t *testing.T) {
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
	set, err := buildModules(nil, signer, nil, nil, nil, nil, nil, sourcesConfig{}, EditionConfig{}, t.TempDir(), log)
	if err != nil {
		t.Fatalf("build modules: %v", err)
	}

	// The same minimal server the beta-coverage test mounts: the production
	// module set, no runtime. Route walking never invokes a handler, so an
	// unwired service (its routes answer 501) still mounts its routes.
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
	authr := auth.NewAuthenticator(st, nil)
	srv, err := api.New(api.Options{
		Store: st, Authenticator: authr, Authorizer: auth.NewAuthorizer(nil),
		PrincipalEvidenceProducer: authr,
		Signer:                    signer, SetupToken: secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token")),
		Logger: log, Version: "test", Modules: set.all,
	})
	if err != nil {
		t.Fatal(err)
	}

	// The mounted surface, walked straight off the chi router.
	router, ok := srv.Handler().(chi.Routes)
	if !ok {
		t.Fatal("handler is not a chi router")
	}
	routed := map[string]bool{}
	if err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routed[normaliseClassificationKey(method, route)] = true
		return nil
	}); err != nil {
		t.Fatalf("walking the router: %v", err)
	}
	// A sentinel, not a count: a walk that lost whole subtrees must not pass.
	for _, sentinel := range []string{"GET /livez", "GET /v1/agents", "POST /v1/auth/login"} {
		if !routed[sentinel] {
			t.Fatalf("the router walk did not find %s; the walk is partial and this test would pass vacuously", sentinel)
		}
	}

	// The published surface: both documents the product serves, built here
	// without a running server, exactly as `olivares openapi` builds them.
	beta, err := moduleOpenAPIDocument()
	if err != nil {
		t.Fatalf("beta document: %v", err)
	}
	published := map[string]bool{}
	for _, doc := range []map[string]any{api.OpenAPIDocument(), beta} {
		for path, item := range doc["paths"].(map[string]any) {
			for method := range item.(map[string]any) {
				m := strings.ToUpper(method)
				switch m {
				case "GET", "PUT", "POST", "DELETE", "PATCH", "HEAD", "OPTIONS":
				default:
					continue // path-item metadata, not an operation
				}
				published[normaliseClassificationKey(m, path)] = true
			}
		}
	}
	if len(published) == 0 {
		t.Fatal("no published operations found; this test would pass vacuously")
	}

	// The classification table, keyed in the SAME normalised shape as the
	// walk and the documents (the table itself spells parameter names).
	classified := map[string]api.RouteClassification{}
	for key, c := range api.ClassifiedRoutes() {
		method, path, ok := strings.Cut(key, " ")
		if !ok {
			t.Errorf("classification row %q is not \"METHOD /path\"", key)
			continue
		}
		classified[normaliseClassificationKey(method, path)] = c
	}

	gaps := make([]string, 0)
	for key := range routed {
		if published[key] {
			continue
		}
		if _, ok := classified[key]; ok {
			continue
		}
		// A method-agnostic row ("* /path") covers every method chi expands
		// it into: chi.Walk never reports "*" itself.
		if _, ok := classified["* "+key[strings.Index(key, " ")+1:]]; ok {
			continue
		}
		gaps = append(gaps, key)
	}
	sort.Strings(gaps)
	for _, key := range gaps {
		t.Errorf("mounted %s is neither published (stable/beta OpenAPI) nor classified in "+
			"core/api/openapi_route_classification.go — publish it if it is Community public API, "+
			"or classify it with a reason", key)
	}

	// The table itself must not rot nor hide: every row is either still
	// mounted (unpublished), or optional (conditionally mounted), or red.
	for key, c := range classified {
		method, path, _ := strings.Cut(key, " ")
		for pub := range published {
			pubMethod, pubPath, _ := strings.Cut(pub, " ")
			if pubPath == path && (method == "*" || pubMethod == method) {
				t.Errorf("classification row %s is ALSO published as %s: the table must not hide a Community public route", key, pub)
			}
		}
		if !routed[key] && !c.Optional {
			t.Errorf("classification row %s is not mounted by the router and is not marked optional: a stale row", key)
		}
	}
	if len(classified) == 0 {
		t.Fatal("the classification table is empty; every non-published route is unclassified")
	}
	t.Logf("%d routed operations, %d published, %d classification rows, %d gaps", len(routed), len(published), len(classified), len(gaps))
}

// paramName collapses chi's and OpenAPI's spellings of one route to the same
// shape: no trailing slash, and path parameters erased to {} (the two sides
// may name a parameter differently — {id} vs {agentID} — which must not
// decide whether a route is published).
var paramName = regexp.MustCompile(`\{[^}]*\}`)

func normaliseClassificationKey(method, path string) string {
	p := strings.TrimSuffix(path, "/")
	if p == "" {
		p = "/"
	}
	return strings.ToUpper(method) + " " + paramName.ReplaceAllString(p, "{}")
}
