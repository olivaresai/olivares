// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// The census of the model stack, as MEASURED from the three modules' route
// tables on this tree:
//
//	modules/models/api.go:82-197              67 routes  GET 32 POST 15 PUT 11 DELETE 9
//	modules/finops/api.go:52-113              42 routes  GET 27 POST  8 PUT  3 DELETE 4
//	modules/inferenceproxy/…:135-140           6 routes  GET  2 POST  1 PUT  2 DELETE 1
//
// The table below is the CLI's answer to that census. The constants are checked
// per module and per verb by TestModelstackTableMatchesTheMeasuredCensus, and the
// table's (method, path) SET is checked against the modules' own source by
// TestTheCLITableIsTheEnginesRouteSetNotJustItsSize.
//
// It takes both, and the second was added after the first was measured to be
// insufficient. Counts alone are a weak referent: a table that drops
// GET /spend/trend and names GET /spend/summary twice keeps all 27 finops GETs,
// so the count assertion passes — and so does the permit sweep, because every row
// it does name still reaches the stub. A dropped route is invisible to both.
// Only comparing the two sets against the engine's route tables sees it.
const (
	modelsRouteCount         = 67
	finopsRouteCount         = 42
	inferenceProxyRouteCount = 6
	modelstackRouteCount     = modelsRouteCount + finopsRouteCount + inferenceProxyRouteCount
)

// modelstackRoute pairs one CLI invocation with the request it must produce.
type modelstackRoute struct {
	// args is the invocation without the connection flags.
	args []string
	// method and path are what the control plane must receive — exactly once.
	method string
	path   string
	// module is the census bucket this row belongs to.
	module string
}

const (
	testID  = "id-1"
	testID2 = "id-2"
)

func modelstackRoutes() []modelstackRoute {
	get := func(module, path string, args ...string) modelstackRoute {
		return modelstackRoute{args: args, method: http.MethodGet, path: path, module: module}
	}
	post := func(module, path string, args ...string) modelstackRoute {
		return modelstackRoute{args: args, method: http.MethodPost, path: path, module: module}
	}
	put := func(module, path string, args ...string) modelstackRoute {
		return modelstackRoute{args: args, method: http.MethodPut, path: path, module: module}
	}
	del := func(module, path string, args ...string) modelstackRoute {
		return modelstackRoute{args: args, method: http.MethodDelete, path: path, module: module}
	}
	const (
		m = "models"
		f = "finops"
		p = "inferenceproxy"
	)
	mb := modelsAPIBase
	fb := finopsAPIBase
	pb := inferenceProxyAPIBase
	return []modelstackRoute{
		// --- models: estate and declared references (8) ---
		get(m, mb+"/models", "models", "ls"),
		get(m, mb+"/models/"+testID, "models", "get", testID),
		get(m, mb+"/catalog", "models", "catalog"),
		get(m, mb+"/features", "models", "features"),
		get(m, mb+"/data-governance", "models", "data-governance"),
		get(m, mb+"/tool-types", "models", "tool-types"),
		get(m, mb+"/rate-limits", "models", "rate-limits"),
		get(m, mb+"/platforms", "models", "platforms"),
		// --- models: routing (7) ---
		get(m, mb+"/routing-policies", "models", "routing", "ls"),
		get(m, mb+"/routing-policies/"+testID, "models", "routing", "get", testID),
		post(m, mb+"/routing-policies", "models", "routing", "create", "--data", "{}"),
		put(m, mb+"/routing-policies/"+testID, "models", "routing", "update", testID, "--data", "{}"),
		del(m, mb+"/routing-policies/"+testID, "models", "routing", "rm", testID, "--yes"),
		post(m, mb+"/routing-policies/"+testID+"/resolve", "models", "routing", "resolve", testID),
		post(m, mb+"/routing-policies/"+testID+"/execute", "models", "routing", "execute", testID, "--data", "{}"),
		// --- models: keys, residency, entitlements (8) ---
		get(m, mb+"/keys", "models", "keys", "ls"),
		post(m, mb+"/keys", "models", "keys", "create", "--data", "{}"),
		put(m, mb+"/keys/"+testID, "models", "keys", "update", testID, "--data", "{}"),
		del(m, mb+"/keys/"+testID, "models", "keys", "rm", testID, "--yes"),
		get(m, mb+"/workspace-residency", "models", "residency", "ls"),
		put(m, mb+"/workspace-residency", "models", "residency", "set", "--data", "{}"),
		get(m, mb+"/access-tier-entitlements", "models", "entitlements", "ls"),
		put(m, mb+"/access-tier-entitlements", "models", "entitlements", "set", "--data", "{}"),
		// --- models: own-model registry (17) ---
		get(m, mb+"/owned-models", "models", "owned", "ls"),
		get(m, mb+"/owned-models/"+testID, "models", "owned", "get", testID),
		post(m, mb+"/owned-models", "models", "owned", "create", "--data", "{}"),
		put(m, mb+"/owned-models/"+testID, "models", "owned", "update", testID, "--data", "{}"),
		del(m, mb+"/owned-models/"+testID, "models", "owned", "rm", testID, "--yes"),
		get(m, mb+"/model-versions", "models", "versions", "ls"),
		post(m, mb+"/model-versions", "models", "versions", "create", "--data", "{}"),
		post(m, mb+"/model-versions/"+testID+"/admit", "models", "versions", "admit", testID, "--data", "{}"),
		del(m, mb+"/model-versions/"+testID, "models", "versions", "rm", testID, "--yes"),
		get(m, mb+"/inference-deployments", "models", "deployments", "ls"),
		post(m, mb+"/inference-deployments", "models", "deployments", "create", "--data", "{}"),
		put(m, mb+"/inference-deployments/"+testID, "models", "deployments", "update", testID, "--data", "{}"),
		del(m, mb+"/inference-deployments/"+testID, "models", "deployments", "rm", testID, "--yes"),
		get(m, mb+"/finetune-jobs", "models", "finetune", "ls"),
		get(m, mb+"/finetune-jobs/"+testID, "models", "finetune", "get", testID),
		post(m, mb+"/finetune-jobs", "models", "finetune", "create", "--data", "{}"),
		put(m, mb+"/finetune-jobs/"+testID, "models", "finetune", "update", testID, "--data", "{}"),
		// --- models: posture and admission (5) ---
		get(m, mb+"/gpai-posture", "models", "gpai", "ls"),
		put(m, mb+"/gpai-posture", "models", "gpai", "attest", "--data", "{}"),
		get(m, mb+"/admission-policy", "models", "admission", "policy"),
		put(m, mb+"/admission-policy", "models", "admission", "set-policy", "--data", "{}"),
		get(m, mb+"/model-admissions", "models", "admission", "ls"),
		// --- models: lineage (13) ---
		get(m, mb+"/datasets", "models", "datasets", "ls"),
		post(m, mb+"/datasets", "models", "datasets", "create", "--data", "{}"),
		del(m, mb+"/datasets/"+testID, "models", "datasets", "rm", testID, "--yes"),
		get(m, mb+"/owned-models/"+testID+"/aibom", "models", "aibom", "get", testID),
		post(m, mb+"/owned-models/"+testID+"/aibom", "models", "aibom", "seal", testID),
		get(m, mb+"/aiboms", "models", "aibom", "ls"),
		get(m, mb+"/owned-models/"+testID+"/model-card", "models", "aibom", "card", testID),
		get(m, mb+"/agent-artifacts", "models", "agent-artifacts", "ls"),
		post(m, mb+"/agent-artifacts", "models", "agent-artifacts", "create", "--data", "{}"),
		del(m, mb+"/agent-artifacts/"+testID, "models", "agent-artifacts", "rm", testID, "--yes"),
		get(m, mb+"/agent-artifacts/aibom", "models", "agent-artifacts", "aibom"),
		post(m, mb+"/agent-artifacts/aibom", "models", "agent-artifacts", "seal"),
		get(m, mb+"/agent-artifacts/aiboms", "models", "agent-artifacts", "seals"),
		// --- models: model-access governance (9) ---
		get(m, mb+"/model-groups", "models", "groups", "ls"),
		get(m, mb+"/model-groups/"+testID, "models", "groups", "get", testID),
		post(m, mb+"/model-groups", "models", "groups", "create", "--data", "{}"),
		put(m, mb+"/model-groups/"+testID, "models", "groups", "update", testID, "--data", "{}"),
		del(m, mb+"/model-groups/"+testID, "models", "groups", "rm", testID, "--yes"),
		get(m, mb+"/model-access", "models", "access", "ls"),
		post(m, mb+"/model-access", "models", "access", "create", "--data", "{}"),
		put(m, mb+"/model-access/"+testID, "models", "access", "update", testID, "--data", "{}"),
		del(m, mb+"/model-access/"+testID, "models", "access", "rm", testID, "--yes"),

		// --- finops: spend and value (9) ---
		get(f, fb+"/spend", "finops", "spend", "ls"),
		get(f, fb+"/spend/summary", "finops", "spend", "summary"),
		get(f, fb+"/spend/trend", "finops", "spend", "trend"),
		get(f, fb+"/spend/reconciliation", "finops", "spend", "reconciliation"),
		get(f, fb+"/spend/allocation", "finops", "spend", "allocation"),
		get(f, fb+"/spend/unified", "finops", "spend", "unified"),
		get(f, fb+"/spend/export", "finops", "spend", "export"),
		get(f, fb+"/value", "finops", "value", "ls"),
		get(f, fb+"/value/summary", "finops", "value", "summary"),
		// --- finops: ingest (4) ---
		get(f, fb+"/outcomes", "finops", "outcomes", "ls"),
		post(f, fb+"/outcomes", "finops", "outcomes", "ingest", "--data", "{}"),
		post(f, fb+"/seats", "finops", "seats", "ingest", "--data", "{}"),
		get(f, fb+"/seats/utilization", "finops", "seats", "utilization"),
		post(f, fb+"/cost", "finops", "cost", "ingest", "--data", "{}"),
		// --- finops: budgets and alerts (7) ---
		get(f, fb+"/budgets", "finops", "budgets", "ls"),
		get(f, fb+"/budgets/"+testID, "finops", "budgets", "get", testID),
		post(f, fb+"/budgets", "finops", "budgets", "create", "--data", "{}"),
		put(f, fb+"/budgets/"+testID, "finops", "budgets", "update", testID, "--data", "{}"),
		del(f, fb+"/budgets/"+testID, "finops", "budgets", "rm", testID, "--yes"),
		get(f, fb+"/budgets/"+testID+"/status", "finops", "budgets", "status", testID),
		get(f, fb+"/alerts", "finops", "alerts"),
		// --- finops: cost centers (8) ---
		get(f, fb+"/cost-centers", "finops", "cost-centers", "ls"),
		get(f, fb+"/cost-centers/"+testID, "finops", "cost-centers", "get", testID),
		post(f, fb+"/cost-centers", "finops", "cost-centers", "create", "--data", "{}"),
		put(f, fb+"/cost-centers/"+testID, "finops", "cost-centers", "update", testID, "--data", "{}"),
		del(f, fb+"/cost-centers/"+testID, "finops", "cost-centers", "rm", testID, "--yes"),
		get(f, fb+"/cost-centers/"+testID+"/mappings", "finops", "cost-centers", "mappings", "ls", testID),
		post(f, fb+"/cost-centers/"+testID+"/mappings", "finops", "cost-centers", "mappings", "add", testID, "--data", "{}"),
		del(f, fb+"/cost-centers/"+testID+"/mappings/"+testID2, "finops", "cost-centers", "mappings", "rm", testID, testID2, "--yes"),
		// --- finops: rates (5) ---
		get(f, fb+"/model-rates", "finops", "rates", "ls"),
		get(f, fb+"/model-rates/"+testID, "finops", "rates", "get", testID),
		post(f, fb+"/model-rates", "finops", "rates", "create", "--data", "{}"),
		put(f, fb+"/model-rates/"+testID, "finops", "rates", "update", testID, "--data", "{}"),
		del(f, fb+"/model-rates/"+testID, "finops", "rates", "rm", testID, "--yes"),
		// --- finops: statements and analysis (9) ---
		post(f, fb+"/statements/generate", "finops", "statements", "generate", "--data", "{}"),
		get(f, fb+"/statements", "finops", "statements", "ls"),
		get(f, fb+"/statements/"+testID, "finops", "statements", "get", testID),
		get(f, fb+"/statements/"+testID+"/export", "finops", "statements", "export", testID),
		get(f, fb+"/forecast", "finops", "forecast"),
		get(f, fb+"/recommendations", "finops", "recommendations"),
		get(f, fb+"/analytics/team-summary", "finops", "team-summary"),
		get(f, fb+"/comparison", "finops", "comparison"),

		// --- inference proxy (6) ---
		get(p, pb+"/config", "inference-proxy", "config", "get"),
		put(p, pb+"/config", "inference-proxy", "config", "set", "--data", "{}"),
		post(p, pb+"/device/approve", "inference-proxy", "device", "approve", "--user-code", "ABCD-EFGH", "--yes"),
		get(p, pb+"/dlp/rules", "inference-proxy", "dlp", "ls"),
		put(p, pb+"/dlp/rules", "inference-proxy", "dlp", "set", "--data", "{}"),
		del(p, pb+"/dlp/rules/"+testID, "inference-proxy", "dlp", "rm", testID, "--yes"),
	}
}

// prepareModelstackCLITest isolates the client configuration so a real
// ~/.config/olivares on the machine running the suite cannot supply a server, a
// token or a tenant. Without this the negative cases below are worthless: a test
// asserting "refused without a credential" passes for the wrong reason on a
// laptop that has one.
func prepareModelstackCLITest(t *testing.T) {
	t.Helper()
	t.Setenv(cliConfigOverrideEnv, filepath.Join(t.TempDir(), "missing-config.yaml"))
	t.Setenv("OLIVARES_SERVER_URL", "")
	t.Setenv("OLIVARES_TOKEN", "")
	t.Setenv("OLIVARES_TENANT", "")
}

// modelstackStub answers every request with a shape the CLI can render, and
// records what it was asked for.
type modelstackStub struct {
	requests []string
	methods  []string
	paths    []string
	// rawPaths is the ESCAPED path, which is what the engine's router actually
	// matches on: chi reads r.URL.RawPath when it is non-empty and only falls
	// back to the decoded r.URL.Path when it is empty (chi v5 mux.go:452-458).
	// The decoded Path is therefore the WRONG instrument for an escaping
	// witness — net/http decodes %2F back into "/" there, so a correctly escaped
	// identifier looks like a traversal in it and an incorrectly escaped one
	// looks the same.
	rawPaths    []string
	requestURIs []string
	queries     []string
	bodies      []string
}

func newModelstackStub(t *testing.T, rec *modelstackStub) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 0)
		if r.Body != nil {
			buf := make([]byte, 4096)
			n, _ := r.Body.Read(buf)
			body = buf[:n]
		}
		rec.requests = append(rec.requests, r.Method+" "+r.URL.Path)
		rec.methods = append(rec.methods, r.Method)
		rec.paths = append(rec.paths, r.URL.Path)
		rec.rawPaths = append(rec.rawPaths, r.URL.RawPath)
		rec.requestURIs = append(rec.requestURIs, r.RequestURI)
		rec.queries = append(rec.queries, r.URL.RawQuery)
		rec.bodies = append(rec.bodies, string(body))
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "has_more": false})
	}))
}

// TestEveryCensusedRouteHasACommandThatReachesIt is the PERMIT half of the
// contrafactual, done on the wire rather than on the exit code: for each of the
// 115 routes the census measured, one CLI invocation produces EXACTLY that
// method and path, exactly once.
//
// "Exactly once" is part of the claim. A command that made a second, unasked-for
// request (a lookup, a whoami, a retry) would still exit 0 and still print
// something; the request log is what makes that visible.
func TestEveryCensusedRouteHasACommandThatReachesIt(t *testing.T) {
	routes := modelstackRoutes()
	if len(routes) != modelstackRouteCount {
		t.Fatalf("the route table has %d rows, but the measured census is %d routes",
			len(routes), modelstackRouteCount)
	}
	for _, route := range routes {
		name := strings.Join(route.args, " ")
		t.Run(name, func(t *testing.T) {
			prepareModelstackCLITest(t)
			rec := &modelstackStub{}
			srv := newModelstackStub(t, rec)
			defer srv.Close()
			args := append(append([]string(nil), route.args...),
				"--server", srv.URL, "--token", "secret-token", "--tenant", "tenant-a", "-o", "json")
			_, stderr, err := execRoot(t, args...)
			if err != nil {
				t.Fatalf("olivares %s: %v (stderr %q)", name, err, stderr)
			}
			if len(rec.requests) != 1 {
				t.Fatalf("olivares %s made %d requests (%v), want exactly 1", name, len(rec.requests), rec.requests)
			}
			if rec.methods[0] != route.method || rec.paths[0] != route.path {
				t.Fatalf("olivares %s reached %s %s, want %s %s",
					name, rec.methods[0], rec.paths[0], route.method, route.path)
			}
		})
	}
}

// TestModelstackTableMatchesTheMeasuredCensus checks the TABLE against the
// figures measured from the three route tables, per module and per verb.
//
// It is the control on the test above. That one proves every row of this table
// reaches the route the row names; only this one connects the table to the API's
// actual surface, so a route dropped from the table cannot pass unnoticed as
// "not in the table, therefore not tested".
func TestModelstackTableMatchesTheMeasuredCensus(t *testing.T) {
	type counts struct{ get, post, put, del int }
	byModule := map[string]*counts{}
	for _, route := range modelstackRoutes() {
		c, ok := byModule[route.module]
		if !ok {
			c = &counts{}
			byModule[route.module] = c
		}
		switch route.method {
		case http.MethodGet:
			c.get++
		case http.MethodPost:
			c.post++
		case http.MethodPut:
			c.put++
		case http.MethodDelete:
			c.del++
		}
	}
	for _, want := range []struct {
		module string
		counts
	}{
		{"models", counts{get: 32, post: 15, put: 11, del: 9}},
		{"finops", counts{get: 27, post: 8, put: 3, del: 4}},
		{"inferenceproxy", counts{get: 2, post: 1, put: 2, del: 1}},
	} {
		got, ok := byModule[want.module]
		if !ok {
			t.Errorf("module %s has no rows at all", want.module)
			continue
		}
		if *got != want.counts {
			t.Errorf("%s: table has GET %d POST %d PUT %d DELETE %d, the measured census is GET %d POST %d PUT %d DELETE %d",
				want.module, got.get, got.post, got.put, got.del,
				want.get, want.post, want.put, want.del)
		}
	}
	if len(byModule) != 3 {
		t.Errorf("the table names %d modules, want exactly the 3 of this lot", len(byModule))
	}
}

// routeKey is one route as a comparable value. modelstackRoute cannot be used:
// it carries the invocation's []string args, so it is not a valid map key.
type routeKey struct{ module, method, path string }

// engineRouteCensus reads the three modules' route tables from their SOURCE and
// returns the (module, method, path) set they register.
//
// It parses rather than greps: reg.Handle("GET", "/models/{id}", …) is an AST
// call whose first two arguments are string literals, and a regex over Go source
// gets the concatenated and the commented-out forms wrong. Measured that way
// once already — the first version of this census parser truncated
// mb+"/models/"+testID to "/models/" and reported 43 phantom mismatches.
//
// A module source that cannot be read is a FAILURE, never a skip: this is the
// only thing standing between the table below and a number somebody typed, and a
// census that quietly measures nothing is worse than no census.
func engineRouteCensus(t *testing.T) map[routeKey]bool {
	t.Helper()
	sources := map[string]string{
		"models":         filepath.Join("..", "..", "modules", "models", "api.go"),
		"finops":         filepath.Join("..", "..", "modules", "finops", "api.go"),
		"inferenceproxy": filepath.Join("..", "..", "modules", "inferenceproxy", "inferenceproxy.go"),
	}
	bases := map[string]string{
		"models":         modelsAPIBase,
		"finops":         finopsAPIBase,
		"inferenceproxy": inferenceProxyAPIBase,
	}
	out := map[routeKey]bool{}
	for module, path := range sources {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		var decl *ast.FuncDecl
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if ok && fn.Name.Name == "APIRoutes" && fn.Recv != nil {
				decl = fn
				break
			}
		}
		if decl == nil {
			t.Fatalf("%s declares no APIRoutes method", path)
		}
		before := len(out)
		ast.Inspect(decl.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Handle" {
				return true
			}
			method, ok1 := stringLit(call.Args[0])
			route, ok2 := stringLit(call.Args[1])
			if !ok1 || !ok2 {
				t.Errorf("%s: reg.Handle with a non-literal method or path; the census cannot read it", path)
				return true
			}
			out[routeKey{module: module, method: method,
				path: bases[module] + placeholderPath.ReplaceAllString(route, testID)}] = true
			return true
		})
		if len(out) == before {
			t.Fatalf("%s: APIRoutes registered no routes the census could read", path)
		}
	}
	return out
}

// placeholderPath rewrites a chi path parameter ({id}, {mappingID}) into the
// identifier the CLI table uses, so the two sets are comparable. Both positions
// of a two-identifier route collapse to the same token, which is why the table
// uses testID for the first and testID2 for the second only in the ARGS.
var placeholderPath = regexp.MustCompile(`\{[^}]+\}`)

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return v, true
}

// TestTheCLITableIsTheEnginesRouteSetNotJustItsSize is the control the count
// assertion above cannot be.
//
// Counts are a WEAK referent: a table that invents /v1/m/finops/spend/fictional
// and drops /v1/m/finops/spend/trend has the same 27 GETs and passes
// TestModelstackTableMatchesTheMeasuredCensus unchanged, while
// TestEveryCensusedRouteHasACommandThatReachesIt happily proves the invented row
// reaches the stub — because the stub answers everything. Only comparing the
// two SETS, against the engine's own source, can tell those apart.
func TestTheCLITableIsTheEnginesRouteSetNotJustItsSize(t *testing.T) {
	engine := engineRouteCensus(t)
	cli := map[routeKey]bool{}
	for _, route := range modelstackRoutes() {
		// The second identifier of a nested route is testID2 in the args but the
		// path is built from the same placeholder, so normalise it away here.
		key := routeKey{module: route.module, method: route.method,
			path: strings.ReplaceAll(route.path, testID2, testID)}
		if cli[key] {
			t.Errorf("the CLI table names %s %s twice", key.method, key.path)
		}
		cli[key] = true
	}
	for route := range engine {
		if !cli[route] {
			t.Errorf("the engine serves %s %s and no CLI row reaches it", route.method, route.path)
		}
	}
	for route := range cli {
		if !engine[route] {
			t.Errorf("the CLI table names %s %s, which no module registers", route.method, route.path)
		}
	}
	if len(engine) != modelstackRouteCount {
		t.Errorf("the engine registers %d routes across the three modules, but the census constant says %d",
			len(engine), modelstackRouteCount)
	}
}

// TestNoCensusedRouteIsReachableWithoutACredential is the DENY half of the same
// sweep, and it is paired with the permit half above ON PURPOSE: "zero requests"
// is satisfied just as well by a command that is simply broken, so the two
// halves only mean something together.
func TestNoCensusedRouteIsReachableWithoutACredential(t *testing.T) {
	for _, route := range modelstackRoutes() {
		name := strings.Join(route.args, " ")
		t.Run(name, func(t *testing.T) {
			prepareModelstackCLITest(t)
			rec := &modelstackStub{}
			srv := newModelstackStub(t, rec)
			defer srv.Close()
			args := append(append([]string(nil), route.args...), "--server", srv.URL)
			_, _, err := execRoot(t, args...)
			if err == nil {
				t.Fatalf("olivares %s succeeded with no credential at all", name)
			}
			assertExitCode(t, err, 2, "olivares "+name+" without a credential")
			if len(rec.requests) != 0 {
				t.Fatalf("olivares %s opened a connection before refusing: %v", name, rec.requests)
			}
		})
	}
}

// assertExitCode is the one place the suite reads an exit code, so a change in
// how the CLI carries it cannot quietly make half these assertions vacuous.
func assertExitCode(t *testing.T, err error, want int, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected a failure with exit %d, got success", what, want)
	}
	if got := exitcode.From(err); got != want {
		t.Fatalf("%s: exit = %d, want %d (error: %v)", what, got, want, err)
	}
}
