// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fixture exercises every shape the emitters must handle: parameterless ops,
// collection + by-id ops, a body-carrying method, a deprecated operation with
// full metadata, and a path with a literal segment AFTER a parameter.
const fixture = `{
  "openapi": "3.1.0",
  "info": {"title": "t", "version": "v1", "x-stability-policy": "https://docs.olivares.invalid/reference/api-stability/"},
  "paths": {
    "/healthz": {"get": {"summary": "Liveness probe", "x-stability": "stable"}},
    "/v1/widgets": {
      "get": {"summary": "List widgets", "x-stability": "stable"},
      "post": {"summary": "Create a widget", "x-stability": "stable"}
    },
    "/v1/widgets/{id}": {
      "get": {"summary": "Get a widget", "x-stability": "stable"},
      "patch": {"summary": "Update a widget", "x-stability": "beta", "deprecated": true,
        "x-deprecated-at": "2026-06-01T00:00:00Z", "x-sunset-at": "2027-06-01T00:00:00Z",
        "x-migration-guide": "https://docs.olivares.invalid/how-to/migrate-widgets/"},
      "delete": {"summary": "Delete a widget", "x-stability": "stable"}
    },
    "/v1/system/orgs/{tenant}/region": {
      "put": {"summary": "Pin a tenant region", "x-stability": "stable"}
    },
    "/textish": {
      "get": {"summary": "A non-JSON surface", "x-stability": "stable",
        "responses": {"200": {"description": "text", "content": {"text/plain": {"schema": {"type": "string"}}}}}}
    },
    "/archive": {
      "post": {"summary": "Build an archive", "x-stability": "stable",
        "responses": {"200": {"description": "archive", "content": {"application/octet-stream": {"schema": {"type": "string", "format": "binary"}}}}}}
    }
  }
}`

func writeFixture(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "openapi.json")
	if err := os.WriteFile(p, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNaming(t *testing.T) {
	cases := []struct {
		method, path         string
		goN, pyN, tsN, javaN string
	}{
		{"GET", "/healthz", "GetHealthz", "get_healthz", "getHealthz", "getHealthz"},
		{"GET", "/v1/server-info", "GetV1ServerInfo", "get_v1_server_info", "getV1ServerInfo", "getV1ServerInfo"},
		{"GET", "/openapi.json", "GetOpenapiJSON", "get_openapi_json", "getOpenapiJson", "getOpenapiJson"},
		{"PATCH", "/v1/agents/{id}", "PatchV1AgentsByID", "patch_v1_agents_by_id", "patchV1AgentsById", "patchV1AgentsById"},
		{"PUT", "/v1/system/orgs/{tenant}/region", "PutV1SystemOrgsByTenantRegion",
			"put_v1_system_orgs_by_tenant_region", "putV1SystemOrgsByTenantRegion", "putV1SystemOrgsByTenantRegion"},
		{"GET", "/v1/access-edges", "GetV1AccessEdges", "get_v1_access_edges", "getV1AccessEdges", "getV1AccessEdges"},
	}
	for _, c := range cases {
		op := Operation{Method: c.method, Path: c.path}
		if got := op.goName(); got != c.goN {
			t.Errorf("goName(%s %s) = %s, want %s", c.method, c.path, got, c.goN)
		}
		if got := op.pyName(); got != c.pyN {
			t.Errorf("pyName(%s %s) = %s, want %s", c.method, c.path, got, c.pyN)
		}
		if got := op.tsName(); got != c.tsN {
			t.Errorf("tsName(%s %s) = %s, want %s", c.method, c.path, got, c.tsN)
		}
		// Java methods are lowerCamelCase like JS/TS — the derived name matches tsName.
		if got := op.javaName(); got != c.javaN {
			t.Errorf("javaName(%s %s) = %s, want %s", c.method, c.path, got, c.javaN)
		}
	}
}

func TestPathExprs(t *testing.T) {
	cases := []struct{ path, goE, pyE, tsE, javaE string }{
		{"/v1/agents", `"/v1/agents"`, `"/v1/agents"`, `"/v1/agents"`, `"/v1/agents"`},
		{"/v1/agents/{id}", `"/v1/agents/"+pathEscape(id)`,
			`"/v1/agents/" + quote(str(id), safe="")`,
			"`/v1/agents/${encodeURIComponent(id)}`",
			`"/v1/agents/" + escapePath(id)`},
		{"/v1/system/orgs/{tenant}/region",
			`"/v1/system/orgs/"+pathEscape(tenant)+"/region"`,
			`"/v1/system/orgs/" + quote(str(tenant), safe="") + "/region"`,
			"`/v1/system/orgs/${encodeURIComponent(tenant)}/region`",
			`"/v1/system/orgs/" + escapePath(tenant) + "/region"`},
	}
	for _, c := range cases {
		if got := goPathExpr(c.path); got != c.goE {
			t.Errorf("goPathExpr(%s) = %s, want %s", c.path, got, c.goE)
		}
		if got := pyPathExpr(c.path); got != c.pyE {
			t.Errorf("pyPathExpr(%s) = %s, want %s", c.path, got, c.pyE)
		}
		if got := tsPathExpr(c.path); got != c.tsE {
			t.Errorf("tsPathExpr(%s) = %s, want %s", c.path, got, c.tsE)
		}
		if got := javaPathExpr(c.path); got != c.javaE {
			t.Errorf("javaPathExpr(%s) = %s, want %s", c.path, got, c.javaE)
		}
	}
}

func TestGenerateFixture(t *testing.T) {
	spec := writeFixture(t)
	out := t.TempDir()
	if err := run(spec, "", out); err != nil {
		t.Fatal(err)
	}

	goOps, err := os.ReadFile(filepath.Join(out, "go", "operations.gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	// The emitted Go must parse (it is committed and built by the gate).
	if _, err := parser.ParseFile(token.NewFileSet(), "operations.gen.go", goOps, parser.ParseComments); err != nil {
		t.Fatalf("generated Go does not parse: %v", err)
	}
	for _, want := range []string{
		"func (c *Client) GetV1Widgets(ctx context.Context, opts ...RequestOption)",
		"func (c *Client) PostV1Widgets(ctx context.Context, body any, opts ...RequestOption)",
		`return c.do(ctx, "PUT", "/v1/system/orgs/{tenant}/region", "/v1/system/orgs/"+pathEscape(tenant)+"/region", body, opts...)`,
		"// Deprecated: deprecated since 2026-06-01T00:00:00Z, sunset 2027-06-01T00:00:00Z; migration guide: https://docs.olivares.invalid/how-to/migrate-widgets/.",
		"func (c *Client) GetTextish(ctx context.Context, opts ...RequestOption) ([]byte, error)",
		`return c.doRaw(ctx, "GET", "/textish", "/textish", opts...)`,
		"func (c *Client) PostArchive(ctx context.Context, opts ...RequestOption) ([]byte, error)",
		`return c.doRaw(ctx, "POST", "/archive", "/archive", opts...)`,
		"Code generated by clients/generator",
	} {
		if !strings.Contains(string(goOps), want) {
			t.Errorf("generated Go missing %q", want)
		}
	}
	if strings.Contains(string(goOps), "doJSONRequired") {
		t.Error("legacy stable Go unexpectedly uses the classified required-body seam")
	}

	py, err := os.ReadFile(filepath.Join(out, "python", "src", "olivares_client", "_operations.py"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"def get_v1_widgets(self, *, tenant=None, **query):",
		"def patch_v1_widgets_by_id(self, id, body=None, *, tenant=None, **query):",
		`return self._do("PATCH", "/v1/widgets/{id}", "/v1/widgets/" + quote(str(id), safe=""), body=body, query=query, tenant=tenant)`,
		`return self._do_raw("GET", "/textish", "/textish", query=query, tenant=tenant)`,
		`return self._do_raw("POST", "/archive", "/archive", query=query, tenant=tenant)`,
		`.. deprecated:: deprecated since 2026-06-01T00:00:00Z`,
		`API_VERSION = "v1"`,
	} {
		if !strings.Contains(string(py), want) {
			t.Errorf("generated Python missing %q", want)
		}
	}
	if strings.Contains(string(py), "_do_json_required") {
		t.Error("legacy stable Python unexpectedly uses the classified required-body seam")
	}

	ts, err := os.ReadFile(filepath.Join(out, "typescript", "src", "operations.gen.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"export class Client extends ClientCore {",
		"patchV1WidgetsById(id: string, body?: JsonInput, opts?: RequestOptions): Promise<Json>",
		"getTextish(opts?: RequestOptions): Promise<string>",
		`return this.doRaw("GET", "/textish", "/textish", opts);`,
		"postArchive(opts?: RequestOptions): Promise<string>",
		`return this.doRaw("POST", "/archive", "/archive", opts);`,
		"@deprecated deprecated since 2026-06-01T00:00:00Z",
	} {
		if !strings.Contains(string(ts), want) {
			t.Errorf("generated TypeScript missing %q", want)
		}
	}
	if strings.Contains(string(ts), "doJsonRequired") {
		t.Error("legacy stable TypeScript unexpectedly uses the classified required-body seam")
	}

	tsv, err := os.ReadFile(filepath.Join(out, "typescript", "src", "version.gen.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tsv), `export const API_VERSION = "v1";`) {
		t.Error("version.gen.ts missing API_VERSION")
	}

	javaDir := filepath.Join(out, "java", "src", "main", "java", "ai", "olivares", "client")
	java := mustRead(t, filepath.Join(javaDir, "Client.java"))
	for _, want := range []string{
		"public final class Client extends ClientCore {",
		"public static Client of(String endpoint, String token) {",
		// The convenience overload (no options) delegates to the full form.
		"public Map<String, Object> getV1Widgets() {",
		"return getV1Widgets(RequestOptions.NONE);",
		`return doJson("GET", "/v1/widgets", "/v1/widgets", null, options);`,
		`return doJson("POST", "/v1/widgets", "/v1/widgets", body, options);`,
		// Path-param escaping + a deprecated, beta, body-carrying op.
		"public Map<String, Object> patchV1WidgetsById(String id, Object body, RequestOptions options) {",
		`return doJson("PATCH", "/v1/widgets/{id}", "/v1/widgets/" + escapePath(id), body, options);`,
		`return doJson("PUT", "/v1/system/orgs/{tenant}/region", "/v1/system/orgs/" + escapePath(tenant) + "/region", body, options);`,
		"    @Deprecated\n",
		"* @deprecated deprecated since 2026-06-01T00:00:00Z, sunset 2027-06-01T00:00:00Z; migration guide: https://docs.olivares.invalid/how-to/migrate-widgets/.",
		"<p>Stability: beta.",
		// A non-JSON (raw) response operation returns String, not a JSON map.
		"public String getTextish() {",
		`return doRaw("GET", "/textish", "/textish", options);`,
		"public String postArchive() {",
		`return doRaw("POST", "/archive", "/archive", options);`,
		"Code generated by clients/generator",
	} {
		if !strings.Contains(java, want) {
			t.Errorf("generated Java missing %q", want)
		}
	}
	if strings.Contains(java, "doJsonRequired") {
		t.Error("legacy stable Java unexpectedly uses the classified required-body seam")
	}
	meta := mustRead(t, filepath.Join(javaDir, "ApiMetadata.java"))
	if !strings.Contains(meta, `public static final String API_VERSION = "v1";`) {
		t.Error("ApiMetadata.java missing API_VERSION")
	}
}

// TestLoadFailsClosed pins every validation the emitters depend on: anything
// they cannot express or that would corrupt generated code must be a load
// error, never exit-0 output.
func TestLoadFailsClosed(t *testing.T) {
	mk := func(pathsJSON string) string {
		p := filepath.Join(t.TempDir(), "openapi.json")
		doc := `{"openapi":"3.1.0","info":{"title":"t","version":"v1"},"paths":{` + pathsJSON + `}}`
		if err := os.WriteFile(p, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := []struct{ name, paths, wantErr string }{
		{"name collision",
			`"/v1/server-info":{"get":{"summary":"a"}},"/v1/server/info":{"get":{"summary":"b"}}`,
			"operation name collision"},
		{"partial segment templating",
			`"/v1/files/{name}.json":{"get":{"summary":"a"}}`,
			"path templating the generator cannot express"},
		{"empty braces",
			`"/v1/x/{}":{"get":{"summary":"a"}}`,
			"path templating the generator cannot express"},
		{"unknown method",
			`"/v1/widgets":{"head":{"summary":"a"}}`,
			"unsupported path-item key"},
		{"docstring breaker",
			`"/v1/widgets":{"get":{"summary":"a \"\"\" b"}}`,
			"would corrupt generated comments"},
		{"jsdoc breaker",
			`"/v1/widgets":{"get":{"summary":"a */ b"}}`,
			"would corrupt generated comments"},
		{"jsdoc breaker in stability metadata (not just summary)",
			`"/v1/widgets":{"get":{"summary":"ok","x-migration-guide":"https://x/ */ bad"}}`,
			"would corrupt generated comments"},
		{"unsafe segment",
			"\"/v1/wid`gets\":{\"get\":{\"summary\":\"a\"}}",
			"outside the safe set"},
		{"raw response on a body-carrying method",
			`"/v1/widgets":{"post":{"summary":"a","requestBody":{"content":{"application/json":{"schema":{"type":"object"}}}},"responses":{"200":{"description":"d","content":{"text/plain":{"schema":{"type":"string"}}}}}}}`,
			"not expressible"},
	}
	for _, c := range cases {
		if _, err := load(mk(c.paths)); err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: err = %v, want substring %q", c.name, err, c.wantErr)
		}
	}
}

func TestParamIdentReservedWords(t *testing.T) {
	for in, want := range map[string]string{
		"with": "with_", "new": "new_", "range": "range_", "type": "type_",
		"await": "await_", "static": "static_", "id": "id", "tenant": "tenant",
	} {
		if got := paramIdent(in); got != want {
			t.Errorf("paramIdent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGenerateDeterministic(t *testing.T) {
	spec := writeFixture(t)
	out1, out2 := t.TempDir(), t.TempDir()
	if err := run(spec, "", out1); err != nil {
		t.Fatal(err)
	}
	if err := run(spec, "", out2); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"go/operations.gen.go", "go/version.gen.go",
		"python/src/olivares_client/_operations.py",
		"typescript/src/operations.gen.ts", "typescript/src/version.gen.ts",
		"java/src/main/java/ai/olivares/client/Client.java",
		"java/src/main/java/ai/olivares/client/ApiMetadata.java",
	} {
		a, err := os.ReadFile(filepath.Join(out1, rel))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(out2, rel))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a, b) {
			t.Errorf("%s: two runs over the same spec differ", rel)
		}
	}
}

// TestRealSpec pins the generator to the committed snapshot: the real artifact
// must parse and carry the stability metadata core/api stamps on it.
func TestRealSpec(t *testing.T) {
	doc, err := load(filepath.Join("..", "..", "web", "openapi", "openapi.json"))
	if err != nil {
		t.Fatalf("committed snapshot does not load: %v", err)
	}
	if doc.APIVersion != "v1" {
		t.Errorf("info.version = %q, want v1", doc.APIVersion)
	}
	if doc.StabilityPolicy == "" {
		t.Error("info.x-stability-policy missing from the committed snapshot")
	}
	if len(doc.Operations) < 25 {
		t.Errorf("suspiciously few operations in the committed snapshot: %d", len(doc.Operations))
	}
	for _, op := range doc.Operations {
		if op.Stability == "" {
			t.Errorf("%s %s: missing x-stability", op.Method, op.Path)
		}
	}
}

func TestSessionsCommunicationTypedFamilyGeneration(t *testing.T) {
	doc, err := loadUnion(
		filepath.Join("..", "..", "web", "openapi", "openapi.json"),
		filepath.Join("..", "..", "web", "openapi", "openapi.beta.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	// ⛔ THE CENSUS IS AN EXPLICIT SET AND IT DOES NOT ABORT THE TEST.
	//
	// It used to be `if typed != 15 { t.Fatalf(...) }`, and that had two faults
	// which the K3 administrative read model made visible at once. A bare NUMBER
	// says nothing about WHICH operations are typed, so an operation could be
	// swapped for another and the gate would still be green; and because it was
	// FATAL and stood before them, the four language-specific nested-contract
	// assertions below never ran once the family legitimately grew — the census
	// failure hid every question that actually describes the generated code.
	//
	// So: name the operations, report a loss and an undeclared gain separately,
	// and keep going. The test still fails — every mismatch is an Errorf — but it
	// fails having checked the contracts too.
	preexistingCommunicationOperations := []string{
		"GET /v1/m/sessions/channels",
		"POST /v1/m/sessions/channels",
		"PATCH /v1/m/sessions/channels",
		"GET /v1/m/sessions/channels/{id}",
		"POST /v1/m/sessions/channels/{id}/grants",
		"POST /v1/m/sessions/channels/{id}/grants/{grant_id}/revoke",
		"POST /v1/m/sessions/messages/send",
		"GET /v1/m/sessions/messages/{id}",
		"GET /v1/m/sessions/deliveries/{id}",
		"POST /v1/m/sessions/deliveries/{id}/ack",
		"GET /v1/m/sessions/inbox",
		"GET /v1/m/sessions/inbox/cursors/personal/{recipient}",
		"PUT /v1/m/sessions/inbox/cursors/personal/{recipient}",
		"POST /v1/m/sessions/handoffs",
		"POST /v1/m/sessions/handoffs/{id}/responses",
	}
	// The incoming-handoff read surface added exactly these two.
	handoffCommunicationOperations := []string{
		"GET /v1/m/sessions/inbox/handoffs",
		"GET /v1/m/sessions/deliveries/{id}/handoff",
	}
	// The K3 administrative read model added exactly these two.
	administrativeCommunicationOperations := []string{
		"GET /v1/m/sessions/channels/administration",
		"GET /v1/m/sessions/channels/{id}/grants",
	}
	// ⛔ THE THREE GROUPS ARE NAMED SEPARATELY BECAUSE THEY ARRIVED SEPARATELY.
	// The handoff pair and the administrative pair were developed and reviewed as
	// independent increments and composed here. A single flat list would have let
	// this composition resolve its merge by keeping one side's operations and
	// dropping the other's while a count stayed plausible; naming the groups makes
	// a dropped surface read as a LOSS of exactly two named operations.
	censusGroups := []struct {
		name       string
		operations []string
	}{
		{"pre-existing", preexistingCommunicationOperations},
		{"incoming-handoff", handoffCommunicationOperations},
		{"administrative", administrativeCommunicationOperations},
	}
	declared := map[string]bool{}
	for _, group := range censusGroups {
		for _, key := range group.operations {
			if declared[key] {
				t.Errorf("census declares %s in more than one group", key)
			}
			declared[key] = true
		}
	}
	typedOperations := map[string]bool{}
	for _, op := range doc.Operations {
		// ⛔ THIS CENSUS IS THE COMMUNICATION FAMILY'S, so it filters by that family
		// rather than by "is typed at all". A second typed family joined the same
		// machinery (auth-capabilities-v1, censused just below), and counting it here
		// would report a correct addition as a communication surface that grew — which
		// is the opposite of what this census exists to detect.
		if op.SDKFamily != sessionsCommunicationFamily {
			continue
		}
		key := op.Method + " " + op.Path
		if typedOperations[key] {
			t.Errorf("typed communication operation %s is published twice", key)
		}
		typedOperations[key] = true
		if !declared[key] {
			t.Errorf("typed communication family gained an undeclared operation %s; "+
				"add it to this census and to the nested-contract expectations below", key)
		}
		if len(op.ResponseSchema) == 0 || op.sessionsCommunicationResultType() == "" {
			t.Errorf("%s %s lacks a concrete typed result", op.Method, op.Path)
		}
	}
	for _, group := range censusGroups {
		for _, key := range group.operations {
			if !typedOperations[key] {
				t.Errorf("typed communication family LOST %s operation %s", group.name, key)
			}
		}
	}
	if len(typedOperations) != len(declared) {
		sizes := make([]string, 0, len(censusGroups))
		for _, group := range censusGroups {
			sizes = append(sizes, fmt.Sprintf("%d %s", len(group.operations), group.name))
		}
		t.Errorf("typed communication operation census = %d, want %d (%s)",
			len(typedOperations), len(declared), strings.Join(sizes, " + "))
	}
	goSource, err := emitGo(doc)
	if err != nil {
		t.Fatal(err)
	}
	pythonSource := string(emitPython(doc))
	typeScriptSource := string(emitTypeScript(doc))
	javaSource := string(emitJava(doc))
	assertSessionsCommunicationDTOContract(t, "Go", doc,
		parseGoSessionsCommunicationTypes(t, string(goSource)))
	assertSessionsCommunicationDTOContract(t, "Python", doc,
		parsePythonSessionsCommunicationTypes(t, pythonSource))
	assertSessionsCommunicationDTOContract(t, "TypeScript", doc,
		parseTypeScriptSessionsCommunicationTypes(t, typeScriptSource))
	assertSessionsCommunicationDTOContract(t, "Java", doc,
		parseJavaSessionsCommunicationTypes(t, javaSource))
	assertSessionsCommunicationInputContract(t, doc,
		parseTypeScriptSessionsCommunicationTypes(t, typeScriptSource))

	// The DTO comparison above is derived from the SAME document, so it would be
	// vacuously green if the added operations disappeared from it. These
	// expectations are written out by hand for exactly that reason: they name the
	// nested contracts each composed increment introduced and the fields each one
	// must carry, in every emitted language.
	//
	// Both increments are listed. The nested SHAPES are what a merge of two
	// generator sources can silently lose: `items` and `handoff` resolve by
	// PARENT in sessionsCommunicationNestedType, so keeping one side's rows and
	// dropping the other's leaves the census at the right number while the
	// dropped side's items collapse onto a reused type.
	addedCommunicationContracts := map[string][]string{
		"SessionsCommunicationChannelAdministrationPage": {
			"items", "has_more", "continuation",
		},
		"SessionsCommunicationChannelAdministrationItem": {
			"channel", "etag",
		},
		"SessionsCommunicationChannelGrantAdministrationPage": {
			"channel", "etag", "observed_at", "items", "has_more", "continuation",
		},
		"SessionsCommunicationChannelGrantAdministrationItem": {
			"grant", "temporal_state",
		},
		"SessionsCommunicationIncomingHandoffPage": {
			"items", "has_more", "continuation",
		},
		"SessionsCommunicationIncomingHandoffSummary": {
			"carrier", "deadline_elapsed", "handoff", "observed_at", "work_item",
		},
		"SessionsCommunicationIncomingHandoffReadResult": {
			"carrier", "content", "deadline_elapsed", "handoff", "observed_at",
			"offer_context", "terminal_reason", "work_item",
		},
		"SessionsCommunicationIncomingHandoffOffer": {
			"ack_deadline", "created_at", "etag", "from", "id", "state",
			"terminal_at", "terminal_code", "to", "version",
		},
		"SessionsCommunicationIncomingHandoffCarrier": {
			"channel_id", "message_id", "delivery_id", "delivery_version",
		},
		"SessionsCommunicationIncomingHandoffWorkItem": {
			"id", "presentation",
		},
	}
	for language, parsed := range map[string]generatedSessionsCommunicationTypes{
		"Go":         parseGoSessionsCommunicationTypes(t, string(goSource)),
		"Python":     parsePythonSessionsCommunicationTypes(t, pythonSource),
		"TypeScript": parseTypeScriptSessionsCommunicationTypes(t, typeScriptSource),
		"Java":       parseJavaSessionsCommunicationTypes(t, javaSource),
	} {
		for name, fields := range addedCommunicationContracts {
			generated, present := parsed[name]
			if !present {
				t.Errorf("%s generated layer lacks the added nested contract %s", language, name)
				continue
			}
			for _, field := range fields {
				if _, ok := generated[field]; !ok {
					t.Errorf("%s %s lacks field %q", language, name, field)
				}
			}
		}
	}
	for _, op := range doc.Operations {
		if !op.sessionsCommunicationTyped() {
			continue
		}
		result := op.sessionsCommunicationResultType()
		methodContracts := map[string]struct {
			source, method, resultMarker string
		}{
			"Go":         {string(goSource), "func (c *Client) " + op.goName() + "(", ") (" + result + ", error) {"},
			"Python":     {pythonSource, "    def " + op.pyName() + "(", ") -> " + result + ":"},
			"TypeScript": {typeScriptSource, "  " + op.tsName() + "(", "): Promise<" + result + "> {"},
			"Java":       {javaSource, "    public " + result + " " + op.javaName() + "(", ""},
		}
		for language, contract := range methodContracts {
			start := strings.Index(contract.source, contract.method)
			if start < 0 {
				t.Errorf("%s lacks generated method for %s %s", language, op.Method, op.Path)
				continue
			}
			if contract.resultMarker != "" {
				lineEnd := strings.Index(contract.source[start:], "\n")
				if lineEnd < 0 || !strings.Contains(contract.source[start:start+lineEnd], contract.resultMarker) {
					t.Errorf("%s method for %s %s does not return %s", language, op.Method, op.Path, result)
				}
			}
		}
	}

	// These mutations prove the discriminator is tied to each marked operation
	// and nested schema, rather than a global search for a name somewhere in the
	// generated file. Each faulty candidate must be rejected independently.
	mutations := []struct {
		name, typeName, old, replacement string
	}{
		{"missing required body", "PostV1MSessionsChannelsInput",
			"  body: SessionsCommunicationChannelCreateBody;\n", ""},
		{"missing required header", "PatchV1MSessionsChannelsInput",
			"  if_match: string;\n", ""},
		{"optionalized required header", "PatchV1MSessionsChannelsInput",
			"  if_match: string;\n", "  if_match?: string;\n"},
		{"missing required query", "GetV1MSessionsInboxInput",
			"  workspace_id: string;\n", ""},
		{"missing nested result property", "SessionsCommunicationDeliveryView",
			"  available_at: string;\n", ""},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			mutated := mutateTypeScriptInterface(t, typeScriptSource, mutation.typeName,
				mutation.old, mutation.replacement)
			actual := parseTypeScriptSessionsCommunicationTypes(t, mutated)
			var err error
			if strings.HasSuffix(mutation.typeName, "Input") {
				err = compareSessionsCommunicationInputs(doc, actual)
			} else {
				err = compareSessionsCommunicationDTOs("TypeScript", doc, actual)
			}
			if err == nil {
				t.Fatal("schema-aware discriminator accepted the mutated generated contract")
			}
		})
	}
}

type generatedSessionsCommunicationField struct {
	Type     string
	Required bool
}

type generatedSessionsCommunicationTypes map[string]map[string]generatedSessionsCommunicationField

func assertSessionsCommunicationDTOContract(
	t *testing.T,
	language string,
	doc *Document,
	actual generatedSessionsCommunicationTypes,
) {
	t.Helper()
	if err := compareSessionsCommunicationDTOs(language, doc, actual); err != nil {
		t.Fatal(err)
	}
}

func compareSessionsCommunicationDTOs(
	language string,
	doc *Document,
	actual generatedSessionsCommunicationTypes,
) error {
	for _, object := range doc.sessionsCommunicationSchemas.Types {
		got, ok := actual[object.Name]
		if !ok {
			return fmt.Errorf("%s generated layer lacks schema %s", language, object.Name)
		}
		want := make(map[string]generatedSessionsCommunicationField, len(object.Fields))
		for _, field := range object.Fields {
			var typ string
			switch language {
			case "Go":
				typ = goSessionsCommunicationType(field.Type, field.Required)
			case "Python":
				typ = pythonSessionsCommunicationType(field.Type)
			case "TypeScript":
				typ = typeScriptSessionsCommunicationType(field.Type)
			case "Java":
				typ = javaSessionsCommunicationType(field.Type, field.Required)
			default:
				return fmt.Errorf("unknown language %q", language)
			}
			want[field.Name] = generatedSessionsCommunicationField{Type: typ, Required: field.Required}
		}
		if err := compareSessionsCommunicationFields(object.Name, want, got); err != nil {
			return fmt.Errorf("%s: %w", language, err)
		}
	}
	return nil
}

func assertSessionsCommunicationInputContract(
	t *testing.T,
	doc *Document,
	actual generatedSessionsCommunicationTypes,
) {
	t.Helper()
	if err := compareSessionsCommunicationInputs(doc, actual); err != nil {
		t.Fatal(err)
	}
}

func compareSessionsCommunicationInputs(
	doc *Document,
	actual generatedSessionsCommunicationTypes,
) error {
	for _, op := range doc.Operations {
		if !op.sessionsCommunicationTyped() || !op.sessionsCommunicationHasInput() {
			continue
		}
		want := make(map[string]generatedSessionsCommunicationField, len(op.Parameters)+1)
		if body := op.sessionsCommunicationBodyType(); body != "" {
			want["body"] = generatedSessionsCommunicationField{Type: body, Required: op.BodyRequired}
		}
		for _, parameter := range op.Parameters {
			typ := "string"
			if parameter.Type == "integer" {
				typ = "number"
			}
			want[paramIdent(parameter.Name)] = generatedSessionsCommunicationField{
				Type: typ, Required: parameter.Required,
			}
		}
		got, ok := actual[op.sessionsCommunicationInputType()]
		if !ok {
			return fmt.Errorf("TypeScript generated layer lacks operation input %s", op.sessionsCommunicationInputType())
		}
		if err := compareSessionsCommunicationFields(op.sessionsCommunicationInputType(), want, got); err != nil {
			return fmt.Errorf("TypeScript: %w", err)
		}
	}
	return nil
}

func compareSessionsCommunicationFields(
	typeName string,
	want, got map[string]generatedSessionsCommunicationField,
) error {
	if len(got) != len(want) {
		return fmt.Errorf("%s fields = %d, want %d", typeName, len(got), len(want))
	}
	for name, expected := range want {
		actual, ok := got[name]
		if !ok {
			return fmt.Errorf("%s lacks field %s", typeName, name)
		}
		if actual != expected {
			return fmt.Errorf("%s.%s = %+v, want %+v", typeName, name, actual, expected)
		}
	}
	return nil
}

// generatedTypedFamilyDTO reports whether a generated type name belongs to a typed SDK
// family. The Go emitter writes its DTOs beside the untyped operation structs, so the
// parser has to select them by name; the other three languages emit their DTOs in their
// own block and need no filter.
//
// It names both families rather than one prefix, because that is what the assertion
// below is FOR: a DTO that vanished from one language would otherwise be read as "this
// parser does not select it" instead of "the SDK lost it".
func generatedTypedFamilyDTO(name string) bool {
	return strings.HasPrefix(name, "SessionsCommunication") ||
		strings.HasPrefix(name, "AuthCapability")
}

func parseGoSessionsCommunicationTypes(t *testing.T, source string) generatedSessionsCommunicationTypes {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "operations.gen.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := make(generatedSessionsCommunicationTypes)
	for _, declaration := range file.Decls {
		generic, ok := declaration.(*ast.GenDecl)
		if !ok || generic.Tok != token.TYPE {
			continue
		}
		for _, spec := range generic.Specs {
			typeSpec := spec.(*ast.TypeSpec)
			if !generatedTypedFamilyDTO(typeSpec.Name.Name) {
				continue
			}
			structure, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				continue
			}
			fields := make(map[string]generatedSessionsCommunicationField)
			for _, field := range structure.Fields.List {
				if len(field.Names) != 1 || field.Tag == nil {
					continue
				}
				tagLiteral, unquoteErr := strconv.Unquote(field.Tag.Value)
				if unquoteErr != nil {
					t.Fatal(unquoteErr)
				}
				jsonTag := reflect.StructTag(tagLiteral).Get("json")
				parts := strings.Split(jsonTag, ",")
				var rendered bytes.Buffer
				if printErr := printer.Fprint(&rendered, fset, field.Type); printErr != nil {
					t.Fatal(printErr)
				}
				fields[parts[0]] = generatedSessionsCommunicationField{
					Type: rendered.String(), Required: len(parts) == 1,
				}
			}
			out[typeSpec.Name.Name] = fields
		}
	}
	return out
}

var typeScriptInterfacePattern = regexp.MustCompile(`^export interface ((?:SessionsCommunication|AuthCapability)\w+|\w+V1MSessions\w+Input|PostV1AuthCapabilitiesInput) \{$`)
var typeScriptFieldPattern = regexp.MustCompile(`^  ([a-zA-Z0-9_]+)(\?)?: (.+);$`)

func parseTypeScriptSessionsCommunicationTypes(t *testing.T, source string) generatedSessionsCommunicationTypes {
	t.Helper()
	out := make(generatedSessionsCommunicationTypes)
	current := ""
	for _, line := range strings.Split(source, "\n") {
		if match := typeScriptInterfacePattern.FindStringSubmatch(line); match != nil {
			current = match[1]
			out[current] = make(map[string]generatedSessionsCommunicationField)
			continue
		}
		if current == "" {
			continue
		}
		if line == "}" {
			current = ""
			continue
		}
		match := typeScriptFieldPattern.FindStringSubmatch(line)
		if match == nil {
			t.Fatalf("unrecognized TypeScript field in %s: %q", current, line)
		}
		out[current][match[1]] = generatedSessionsCommunicationField{
			Type: match[3], Required: match[2] == "",
		}
	}
	return out
}

type generatedPythonClass struct {
	base       string
	bases      []string
	totalFalse bool
	fields     map[string]string
}

var pythonClassPattern = regexp.MustCompile(`^class (_?(?:SessionsCommunication|AuthCapability)\w+)\((.+)\):$`)
var pythonFieldPattern = regexp.MustCompile(`^    ([a-zA-Z0-9_]+): (.+)$`)

// A published field name may be a Python keyword ("from"), which the class syntax
// cannot express at all. Those objects are emitted with the functional TypedDict
// form, so the parser reads both shapes; a reader that only understood the class
// form would report the functional bases as missing instead of checking them.
var pythonFunctionalPattern = regexp.MustCompile(
	`^(_?(?:SessionsCommunication|AuthCapability)\w+) = TypedDict\($`,
)
var pythonFunctionalNamePattern = regexp.MustCompile(`^    "(_?(?:SessionsCommunication|AuthCapability)\w+)",$`)
var pythonFunctionalFieldPattern = regexp.MustCompile(`^        "([^"]+)": "([^"]+)",$`)

func parsePythonSessionsCommunicationTypes(t *testing.T, source string) generatedSessionsCommunicationTypes {
	t.Helper()
	classes := make(map[string]generatedPythonClass)
	current := ""
	functional := ""
	for _, line := range strings.Split(source, "\n") {
		if match := pythonClassPattern.FindStringSubmatch(line); match != nil {
			current, functional = match[1], ""
			var bases []string
			for _, raw := range strings.Split(match[2], ",") {
				raw = strings.TrimSpace(raw)
				if raw == "TypedDict" || raw == "total=False" || raw == "" {
					continue
				}
				bases = append(bases, raw)
			}
			classes[current] = generatedPythonClass{
				base: strings.TrimSpace(strings.Split(match[2], ",")[0]), bases: bases,
				totalFalse: strings.Contains(match[2], "total=False"),
				fields:     make(map[string]string),
			}
			continue
		}
		if pythonFunctionalPattern.MatchString(line) {
			current, functional = "", "?"
			continue
		}
		if functional == "?" {
			match := pythonFunctionalNamePattern.FindStringSubmatch(line)
			if match == nil {
				t.Fatalf("functional TypedDict is not followed by its name: %q", line)
			}
			functional = match[1]
			classes[functional] = generatedPythonClass{
				base: "TypedDict", fields: make(map[string]string),
			}
			continue
		}
		if functional != "" {
			if line == ")" {
				functional = ""
				continue
			}
			if line == "    total=False," {
				class := classes[functional]
				class.totalFalse = true
				classes[functional] = class
				continue
			}
			if match := pythonFunctionalFieldPattern.FindStringSubmatch(line); match != nil {
				class := classes[functional]
				class.fields[match[1]] = match[2]
				classes[functional] = class
			}
			continue
		}
		if current == "" {
			continue
		}
		if line == "" || !strings.HasPrefix(line, "    ") {
			current = ""
			continue
		}
		if match := pythonFieldPattern.FindStringSubmatch(line); match != nil {
			class := classes[current]
			class.fields[match[1]] = match[2]
			classes[current] = class
		}
	}
	out := make(generatedSessionsCommunicationTypes)
	for name, class := range classes {
		if strings.HasPrefix(name, "_") {
			continue
		}
		fields := make(map[string]generatedSessionsCommunicationField)
		for _, baseName := range class.bases {
			base, ok := classes[baseName]
			if !ok {
				t.Fatalf("Python schema %s has unknown base %s", name, baseName)
			}
			for field, typ := range base.fields {
				fields[field] = generatedSessionsCommunicationField{Type: typ, Required: !base.totalFalse}
			}
		}
		for field, typ := range class.fields {
			fields[field] = generatedSessionsCommunicationField{Type: typ, Required: !class.totalFalse}
		}
		out[name] = fields
	}
	return out
}

var javaRecordPattern = regexp.MustCompile(`^    public record ((?:SessionsCommunication|AuthCapability)\w+|\w+V1MSessions\w+Input|PostV1AuthCapabilitiesInput)\($`)
var javaFieldPattern = regexp.MustCompile(`^            /\* (required|optional) \*/ (.+) ([a-zA-Z0-9_]+),?$`)

func parseJavaSessionsCommunicationTypes(t *testing.T, source string) generatedSessionsCommunicationTypes {
	t.Helper()
	out := make(generatedSessionsCommunicationTypes)
	current := ""
	for _, line := range strings.Split(source, "\n") {
		if match := javaRecordPattern.FindStringSubmatch(line); match != nil {
			current = match[1]
			out[current] = make(map[string]generatedSessionsCommunicationField)
			continue
		}
		if current == "" {
			continue
		}
		if strings.HasPrefix(line, "    )") {
			current = ""
			continue
		}
		match := javaFieldPattern.FindStringSubmatch(line)
		if match == nil {
			t.Fatalf("unrecognized Java field in %s: %q", current, line)
		}
		out[current][match[3]] = generatedSessionsCommunicationField{
			Type: match[2], Required: match[1] == "required",
		}
	}
	return out
}

func mutateTypeScriptInterface(t *testing.T, source, typeName, old, replacement string) string {
	t.Helper()
	startMarker := "export interface " + typeName + " {\n"
	start := strings.Index(source, startMarker)
	if start < 0 {
		t.Fatalf("missing TypeScript interface %s", typeName)
	}
	endRelative := strings.Index(source[start:], "\n}\n")
	if endRelative < 0 {
		t.Fatalf("unterminated TypeScript interface %s", typeName)
	}
	end := start + endRelative + len("\n}\n")
	section := source[start:end]
	if strings.Count(section, old) != 1 {
		t.Fatalf("%s mutation match count = %d, want 1", typeName, strings.Count(section, old))
	}
	return source[:start] + strings.Replace(section, old, replacement, 1) + source[end:]
}

// betaFixture is a minimal beta module-route document: one JSON route and one SSE
// (raw) route, both beta, mirroring what buildModuleOpenAPI emits.
const betaFixture = `{
  "openapi": "3.1.0",
  "info": {"title": "beta", "version": "v1", "x-stability-policy": "https://docs.olivares.invalid/reference/api-stability/"},
  "paths": {
    "/v1/m/finops/spend": {"get": {"summary": "finops module route (requires finops:spend:read)",
      "x-stability": "beta", "x-required-permission": "finops:spend:read"}},
    "/v1/m/finops/recalculate": {"post": {"summary": "recalculate",
      "x-stability": "beta", "x-olivares-request-body-disposition": "bodyless"}},
    "/v1/m/evals/runs/{id}/stream": {"get": {"summary": "evals module route (requires evals:run:read)",
      "x-stability": "beta", "x-required-permission": "evals:run:read",
      "responses": {"200": {"description": "OK", "content": {"text/event-stream": {"schema": {"type": "string"}}}}}}},
    "/v1/m/sessions/workspaces/{ref}/files/raw": {"put": {"summary": "sessions module route (requires sessions:workspace:write)",
      "x-stability": "beta", "x-required-permission": "sessions:workspace:write",
      "x-olivares-request-body-disposition": "schema-published",
      "requestBody": {"required": true, "content": {"application/octet-stream": {"schema": {"type": "string", "format": "binary"}}}}}},
    "/v1/m/knowledge/memory/import": {"post": {"summary": "memory import", "x-stability": "beta",
      "x-olivares-request-body-disposition": "opaque-body",
      "requestBody": {"required": true, "content": {"application/x-ndjson": {"schema": {"type": "string"}}}}}},
    "/v1/m/compliance/oscal/profiles": {"post": {"summary": "OSCAL import", "x-stability": "beta",
      "x-olivares-request-body-disposition": "opaque-body",
      "requestBody": {"required": true, "content": {"application/json": {"schema": {}}}}}},
    "/v1/m/test/optional": {"post": {"summary": "optional body", "x-stability": "beta",
      "x-olivares-request-body-disposition": "schema-published",
      "requestBody": {"required": false, "content": {"application/json": {"schema": {"type": "object"}}}}}},
    "/v1/m/test/required": {"post": {"summary": "required body", "x-stability": "beta",
      "x-olivares-request-body-disposition": "schema-published",
      "requestBody": {"required": true, "content": {"application/json": {"schema": {"type": ["object", "null"]}}}}}}
  }
}`

func writeBetaFixture(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "openapi.beta.json")
	if err := os.WriteFile(p, []byte(betaFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func requestBodyDispositionFixture(operation string) string {
	return `{"openapi":"3.1.0","info":{"title":"t","version":"v1"},"paths":{` +
		`"/v1/m/test/action":{"post":` + operation + `}}}`
}

func TestRequestBodyDispositionControlsSDKSignature(t *testing.T) {
	p := filepath.Join(t.TempDir(), "openapi.beta.json")
	raw := `{
  "openapi": "3.1.0",
  "info": {"title": "t", "version": "v1"},
  "paths": {
    "/v1/m/test/schema": {"post": {"summary": "schema", "x-olivares-request-body-disposition": "schema-published",
      "requestBody": {"required": true, "content": {"application/json": {"schema": {"type": "object"}}}}}},
    "/v1/m/test/bodyless": {"post": {"summary": "bodyless", "x-olivares-request-body-disposition": "bodyless"}},
    "/v1/m/test/opaque": {"delete": {"summary": "opaque", "x-olivares-request-body-disposition": "opaque-body",
      "requestBody": {"required": true, "content": {"application/x-ndjson": {"schema": {"type": "string"}}}}}},
    "/v1/m/test/opaque-json": {"post": {"summary": "opaque JSON", "x-olivares-request-body-disposition": "opaque-body",
      "requestBody": {"required": true, "content": {"application/json": {"schema": {}}}}}},
    "/v1/m/test/optional": {"post": {"summary": "optional", "x-olivares-request-body-disposition": "schema-published",
      "requestBody": {"required": false, "content": {"application/json": {"schema": {"type": "object"}}}}}}
  }
}`
	if err := os.WriteFile(p, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := load(p)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Operation{}
	for _, op := range doc.Operations {
		got[op.Path] = op
	}
	if !got["/v1/m/test/schema"].HasBody || got["/v1/m/test/schema"].RawReqBody {
		t.Errorf("schema-published operation = %#v", got["/v1/m/test/schema"])
	}
	if !got["/v1/m/test/schema"].BodyRequired ||
		got["/v1/m/test/schema"].RequestBodyDisposition != "schema-published" ||
		got["/v1/m/test/schema"].RequestContentType != "application/json" {
		t.Errorf("schema-published metadata = %#v", got["/v1/m/test/schema"])
	}
	if got["/v1/m/test/bodyless"].HasBody {
		t.Errorf("bodyless operation = %#v", got["/v1/m/test/bodyless"])
	}
	if got["/v1/m/test/bodyless"].RequestBodyDisposition != "bodyless" {
		t.Errorf("bodyless metadata = %#v", got["/v1/m/test/bodyless"])
	}
	if !got["/v1/m/test/opaque"].HasBody || !got["/v1/m/test/opaque"].RawReqBody {
		t.Errorf("opaque-body operation = %#v", got["/v1/m/test/opaque"])
	}
	if !got["/v1/m/test/opaque"].BodyRequired ||
		got["/v1/m/test/opaque"].RequestBodyDisposition != "opaque-body" ||
		got["/v1/m/test/opaque"].RequestContentType != "application/x-ndjson" {
		t.Errorf("opaque-body metadata = %#v", got["/v1/m/test/opaque"])
	}
	if !got["/v1/m/test/opaque-json"].HasBody ||
		!got["/v1/m/test/opaque-json"].RawReqBody ||
		got["/v1/m/test/opaque-json"].RequestContentType != "application/json" {
		t.Errorf("opaque JSON metadata = %#v", got["/v1/m/test/opaque-json"])
	}
	if got["/v1/m/test/optional"].BodyRequired ||
		!got["/v1/m/test/optional"].HasBody {
		t.Errorf("optional request body metadata = %#v", got["/v1/m/test/optional"])
	}
}

func TestOpaqueJSONDispositionMutantChangesTransport(t *testing.T) {
	const operation = `{"summary":"opaque JSON","x-olivares-request-body-disposition":"opaque-body",` +
		`"requestBody":{"required":true,"content":{"application/json":{"schema":{}}}}}`
	if got := strings.Count(operation, `"opaque-body"`); got != 1 {
		t.Fatalf("opaque JSON mutant anchor count = %d, want exactly 1", got)
	}
	mutant := strings.Replace(operation, `"opaque-body"`, `"schema-published"`, 1)

	for _, tc := range []struct {
		name string
		raw  string
		want bool
	}{
		{name: "opaque-body", raw: operation, want: true},
		{name: "schema-published mutant", raw: mutant, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "openapi.json")
			if err := os.WriteFile(
				p, []byte(requestBodyDispositionFixture(tc.raw)), 0o644,
			); err != nil {
				t.Fatal(err)
			}
			doc, err := load(p)
			if err != nil {
				t.Fatal(err)
			}
			if got := doc.Operations[0].RawReqBody; got != tc.want {
				t.Fatalf("RawReqBody = %t, want %t for %s", got, tc.want, tc.name)
			}
			if got := doc.Operations[0].RequestContentType; got != "application/json" {
				t.Fatalf("RequestContentType = %q, want application/json", got)
			}
		})
	}
}

func TestRequestBodyDispositionRejectsContradictions(t *testing.T) {
	cases := []struct {
		name      string
		operation string
		want      string
	}{
		{
			name:      "schema without declaration",
			operation: `{"summary":"x","x-olivares-request-body-disposition":"schema-published"}`,
			want:      `POST /v1/m/test/action: x-olivares-request-body-disposition="schema-published" requires requestBody content`,
		},
		{
			name:      "opaque without declaration",
			operation: `{"summary":"x","x-olivares-request-body-disposition":"opaque-body"}`,
			want:      `POST /v1/m/test/action: x-olivares-request-body-disposition="opaque-body" requires requestBody content`,
		},
		{
			name:      "bodyless with declaration",
			operation: `{"summary":"x","x-olivares-request-body-disposition":"bodyless","requestBody":{"content":{"application/json":{}}}}`,
			want:      `POST /v1/m/test/action: x-olivares-request-body-disposition="bodyless" forbids requestBody`,
		},
		{
			name:      "bodyless with empty declaration",
			operation: `{"summary":"x","x-olivares-request-body-disposition":"bodyless","requestBody":{}}`,
			want:      `POST /v1/m/test/action: x-olivares-request-body-disposition="bodyless" forbids requestBody`,
		},
		{
			name:      "bodyless with null declaration",
			operation: `{"summary":"x","x-olivares-request-body-disposition":"bodyless","requestBody":null}`,
			want:      `POST /v1/m/test/action: x-olivares-request-body-disposition="bodyless" forbids requestBody`,
		},
		{
			name:      "unclassified",
			operation: `{"summary":"x","x-olivares-request-body-disposition":"unclassified"}`,
			want:      `POST /v1/m/test/action: x-olivares-request-body-disposition="unclassified" cannot produce an exact SDK signature`,
		},
		{
			name:      "unknown",
			operation: `{"summary":"x","x-olivares-request-body-disposition":"future"}`,
			want:      `POST /v1/m/test/action: unknown x-olivares-request-body-disposition="future"`,
		},
		{
			name:      "null token",
			operation: `{"summary":"x","x-olivares-request-body-disposition":null}`,
			want:      `POST /v1/m/test/action: x-olivares-request-body-disposition must be a non-empty string, got null`,
		},
		{
			name:      "empty token",
			operation: `{"summary":"x","x-olivares-request-body-disposition":""}`,
			want:      `POST /v1/m/test/action: x-olivares-request-body-disposition must be a non-empty string, got ""`,
		},
		{
			name:      "non-string token",
			operation: `{"summary":"x","x-olivares-request-body-disposition":true}`,
			want:      `POST /v1/m/test/action: x-olivares-request-body-disposition must be a string`,
		},
		{
			name: "multiple media types",
			operation: `{"summary":"x","x-olivares-request-body-disposition":"schema-published",` +
				`"requestBody":{"content":{"application/json":{},"application/x-ndjson":{}}}}`,
			want: `POST /v1/m/test/action: x-olivares-request-body-disposition="schema-published" requires exactly one requestBody media type, got 2`,
		},
		{
			name: "empty media type",
			operation: `{"summary":"x","x-olivares-request-body-disposition":"opaque-body",` +
				`"requestBody":{"content":{"":{}}}}`,
			want: `POST /v1/m/test/action: requestBody media type must not be empty`,
		},
		{
			name: "media type with CRLF",
			operation: `{"summary":"x","x-olivares-request-body-disposition":"opaque-body",` +
				`"requestBody":{"content":{"application/x-ndjson\r\nX-Evil: yes":{}}}}`,
			want: `POST /v1/m/test/action: requestBody media type "application/x-ndjson\r\nX-Evil: yes" must be canonical and contain no parameters`,
		},
		{
			name: "media type with parameters",
			operation: `{"summary":"x","x-olivares-request-body-disposition":"opaque-body",` +
				`"requestBody":{"content":{"application/x-ndjson; charset=utf-8":{}}}}`,
			want: `POST /v1/m/test/action: requestBody media type "application/x-ndjson; charset=utf-8" must be canonical and contain no parameters`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "openapi.beta.json")
			if err := os.WriteFile(p, []byte(requestBodyDispositionFixture(tc.operation)), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := load(p)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v, want exact %q", err, tc.want)
			}
		})
	}
}

func TestLoadUnionRequiresBetaMutationDispositions(t *testing.T) {
	stable := filepath.Join(t.TempDir(), "stable.json")
	beta := filepath.Join(t.TempDir(), "beta.json")
	if err := os.WriteFile(stable, []byte(`{"openapi":"3.1.0","info":{"title":"s","version":"v1"},
		"paths":{"/healthz":{"get":{"summary":"ok"}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(beta, []byte(`{"openapi":"3.1.0","info":{"title":"b","version":"v1"},
		"paths":{"/v1/m/test/action":{"post":{"summary":"missing"}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadUnion(stable, beta)
	want := "POST /v1/m/test/action: x-olivares-request-body-disposition is required in the beta document"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want exact %q", err, want)
	}
}

func TestStableWithoutDispositionsKeepsLegacyBodyInference(t *testing.T) {
	p := filepath.Join(t.TempDir(), "stable.json")
	raw := `{"openapi":"3.1.0","info":{"title":"s","version":"v1"},"paths":{
		"/legacy-post":{"post":{"summary":"legacy"}},
		"/legacy-delete":{"delete":{"summary":"legacy delete","requestBody":{"required":true,
			"content":{"application/json":{"schema":{"type":"object"}}}}}}
	}}`
	if err := os.WriteFile(p, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := load(p)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Operation{}
	for _, op := range doc.Operations {
		got[op.Path] = op
	}
	if !got["/legacy-post"].HasBody || got["/legacy-post"].RequestBodyDisposition != "" {
		t.Errorf("legacy POST = %#v", got["/legacy-post"])
	}
	if got["/legacy-delete"].HasBody || !got["/legacy-delete"].BodyRequired ||
		got["/legacy-delete"].RequestContentType != "application/json" {
		t.Errorf("legacy DELETE = %#v", got["/legacy-delete"])
	}
}

// TestGenerateUnion pins that the SDKs cover the UNION (stable + beta) and that
// beta operations carry the language-native stability annotation, never folded
// silently into the stable surface.
func TestGenerateUnion(t *testing.T) {
	out := t.TempDir()
	if err := run(writeFixture(t), writeBetaFixture(t), out); err != nil {
		t.Fatal(err)
	}
	goOps := mustRead(t, filepath.Join(out, "go", "operations.gen.go"))
	for _, want := range []string{
		// A stable core op and a beta module op coexist in one client.
		"func (c *Client) GetV1Widgets(ctx context.Context, opts ...RequestOption)",
		"func (c *Client) GetV1MFinopsSpend(ctx context.Context, opts ...RequestOption)",
		"// Stability: beta.",
		// The SSE module route is raw (bytes), not a JSON decoder.
		"func (c *Client) GetV1MEvalsRunsByIDStream(ctx context.Context, id string, opts ...RequestOption) ([]byte, error)",
		// A raw REQUEST body (octet-stream) → []byte param + doReqRaw, never JSON.
		"func (c *Client) PutV1MSessionsWorkspacesByRefFilesRaw(ctx context.Context, ref string, body []byte, opts ...RequestOption) (map[string]any, error)",
		`return c.doReqRawWithType(ctx, "PUT", "/v1/m/sessions/workspaces/{ref}/files/raw",`,
		`body, "application/octet-stream", opts...)`,
		"func (c *Client) PostV1MKnowledgeMemoryImport(ctx context.Context, body []byte, opts ...RequestOption)",
		`return c.doReqRawWithType(ctx, "POST", "/v1/m/knowledge/memory/import",`,
		`body, "application/x-ndjson", opts...)`,
		"func (c *Client) PostV1MComplianceOscalProfiles(ctx context.Context, body []byte, opts ...RequestOption)",
		`return c.doReqRawWithType(ctx, "POST", "/v1/m/compliance/oscal/profiles",`,
		`body, "application/json", opts...)`,
		"// The request body is optional; pass nil to omit it.",
		"func (c *Client) PostV1MTestRequired(ctx context.Context, body any, opts ...RequestOption)",
		`return c.doJSONRequired(ctx, "POST", "/v1/m/test/required", "/v1/m/test/required", body, opts...)`,
		`return c.do(ctx, "POST", "/v1/m/test/optional", "/v1/m/test/optional", body, opts...)`,
	} {
		if !strings.Contains(goOps, want) {
			t.Errorf("union Go missing %q", want)
		}
	}
	py := mustRead(t, filepath.Join(out, "python", "src", "olivares_client", "_operations.py"))
	if !strings.Contains(py, "def get_v1_m_finops_spend(") || !strings.Contains(py, "Stability: beta.") {
		t.Error("union Python missing beta module op or stability marker")
	}
	for _, want := range []string{
		`body=body, raw_request_content_type="application/octet-stream",`,
		`body=body, raw_request_content_type="application/x-ndjson",`,
		`body=body, raw_request_content_type="application/json",`,
		"def post_v1_m_compliance_oscal_profiles(self, body, *, tenant=None, **query):",
		"def post_v1_m_test_optional(self, body=None, *, tenant=None, **query):",
		"def post_v1_m_test_required(self, body, *, tenant=None, **query):",
		`return self._do_json_required("POST", "/v1/m/test/required", "/v1/m/test/required", body=body, query=query, tenant=tenant)`,
		`return self._do("POST", "/v1/m/test/optional", "/v1/m/test/optional", body=body, query=query, tenant=tenant)`,
		"def post_v1_m_finops_recalculate(self, *, tenant=None, **query):",
	} {
		if !strings.Contains(py, want) {
			t.Errorf("union Python missing %q", want)
		}
	}
	ts := mustRead(t, filepath.Join(out, "typescript", "src", "operations.gen.ts"))
	if !strings.Contains(ts, "getV1MFinopsSpend(") || !strings.Contains(ts, "Stability: beta.") {
		t.Error("union TypeScript missing beta module op or stability marker")
	}
	if !strings.Contains(ts, "putV1MSessionsWorkspacesByRefFilesRaw(ref: string, body: Uint8Array") ||
		!strings.Contains(ts, "this.doReqRawWithType(") ||
		!strings.Contains(ts, `body, "application/octet-stream", opts`) {
		t.Error("union TypeScript missing raw-request (Uint8Array/doReqRaw) for files/raw")
	}
	for _, want := range []string{
		"postV1MKnowledgeMemoryImport(body: Uint8Array, opts?: RequestOptions)",
		`body, "application/x-ndjson", opts`,
		"postV1MComplianceOscalProfiles(body: Uint8Array, opts?: RequestOptions)",
		`body, "application/json", opts`,
		"postV1MTestOptional(body?: JsonInput, opts?: RequestOptions)",
		"postV1MTestRequired(body: JsonInput, opts?: RequestOptions)",
		`return this.doJsonRequired("POST", "/v1/m/test/required", "/v1/m/test/required", body, opts);`,
		`return this.do("POST", "/v1/m/test/optional", "/v1/m/test/optional", body, opts);`,
		"postV1MFinopsRecalculate(opts?: RequestOptions)",
	} {
		if !strings.Contains(ts, want) {
			t.Errorf("union TypeScript missing %q", want)
		}
	}
	java := mustRead(t, filepath.Join(out, "java", "src", "main", "java", "ai", "olivares", "client", "Client.java"))
	for _, want := range []string{
		// A stable core op and a beta module op coexist in one client.
		"public Map<String, Object> getV1Widgets() {",
		"public Map<String, Object> getV1MFinopsSpend() {",
		"<p>Stability: beta.",
		// The SSE module route is raw (bytes-as-String), not a JSON decoder.
		"public String getV1MEvalsRunsByIdStream(String id, RequestOptions options) {",
		`return doRaw("GET", "/v1/m/evals/runs/{id}/stream",`,
		// A raw REQUEST body (octet-stream) → byte[] param + doReqRaw, never JSON.
		"public Map<String, Object> putV1MSessionsWorkspacesByRefFilesRaw(String ref, byte[] body, RequestOptions options) {",
		`return doReqRawWithType("PUT", "/v1/m/sessions/workspaces/{ref}/files/raw",`,
		`body, "application/octet-stream", options);`,
		"public Map<String, Object> postV1MKnowledgeMemoryImport(byte[] body, RequestOptions options) {",
		`return doReqRawWithType("POST", "/v1/m/knowledge/memory/import",`,
		`body, "application/x-ndjson", options);`,
		"public Map<String, Object> postV1MComplianceOscalProfiles(byte[] body, RequestOptions options) {",
		`return doReqRawWithType("POST", "/v1/m/compliance/oscal/profiles",`,
		`body, "application/json", options);`,
		"The request body is optional; pass {@code null} to omit it.",
		`return doJsonRequired("POST", "/v1/m/test/required", "/v1/m/test/required", body, options);`,
		`return doJson("POST", "/v1/m/test/optional", "/v1/m/test/optional", body, options);`,
	} {
		if !strings.Contains(java, want) {
			t.Errorf("union Java missing %q", want)
		}
	}
}

// TestUnionCrossSpecCollision fails closed when a beta route would derive the same
// operation name as a stable one (it would silently shadow it in Python/TS).
func TestUnionCrossSpecCollision(t *testing.T) {
	stable := filepath.Join(t.TempDir(), "openapi.json")
	beta := filepath.Join(t.TempDir(), "openapi.beta.json")
	if err := os.WriteFile(stable, []byte(`{"openapi":"3.1.0","info":{"title":"t","version":"v1"},
		"paths":{"/v1/m/finops/spend":{"get":{"summary":"a"}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(beta, []byte(`{"openapi":"3.1.0","info":{"title":"b","version":"v1"},
		"paths":{"/v1/m/finops/spend":{"get":{"summary":"b","x-stability":"beta"}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadUnion(stable, beta); err == nil || !strings.Contains(err.Error(), "collision across specs") {
		t.Errorf("union must reject a cross-spec name collision, got %v", err)
	}
}

// TestRealBetaSpec pins the generator to the committed beta snapshot: every
// operation must load and be marked beta.
func TestRealBetaSpec(t *testing.T) {
	doc, err := load(filepath.Join("..", "..", "web", "openapi", "openapi.beta.json"))
	if err != nil {
		t.Fatalf("committed beta snapshot does not load: %v", err)
	}
	if len(doc.Operations) == 0 {
		t.Fatal("beta snapshot has no operations")
	}
	for _, op := range doc.Operations {
		if op.Stability != "beta" {
			t.Errorf("%s %s: x-stability = %q, want beta", op.Method, op.Path, op.Stability)
		}
		if !strings.HasPrefix(op.Path, "/v1/m/") {
			t.Errorf("%s %s: beta doc must contain only /v1/m/ module routes", op.Method, op.Path)
		}
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestAuthCapabilitiesTypedFamilyGeneration is the permanent guard for finding R5 of the
// independent review of the first G1-A candidate
// (`an internal design note (not shipped)`, SHA-256
// 2e9875c2dcace018b43aea349dbbe82271deb9c730d04c3a40ed738878cf1ffa).
//
// ⛔ IT EXISTS BECAUSE GREEN GENERATION GATES SAID NOTHING. On the rejected candidate the
// endpoint was published, the snapshot regenerated and `sdk:check` passed — and all four
// SDKs carried `any`/`Object`/`Json`/untyped stubs, because an operation that is not
// marked for a typed family goes down the generic path in silence. A gate that only asks
// "is the committed output current?" cannot see a contract that was never emitted.
//
// So this asserts the CONTRACT, not the freshness: the operation is marked, its schemas
// resolve to concrete objects, and every finite field the wire contract publishes —
// state, code, budget, the selector maps — reaches all four languages with a real type.
func TestAuthCapabilitiesTypedFamilyGeneration(t *testing.T) {
	doc, err := load(filepath.Join("..", "..", "web", "openapi", "openapi.json"))
	if err != nil {
		t.Fatalf("committed snapshot does not load: %v", err)
	}
	const key = "POST /v1/auth/capabilities"
	var operation *Operation
	for index := range doc.Operations {
		if doc.Operations[index].Method+" "+doc.Operations[index].Path == key {
			operation = &doc.Operations[index]
		}
	}
	if operation == nil {
		t.Fatalf("%s is absent from the committed snapshot", key)
	}
	if operation.SDKFamily != authCapabilitiesFamily {
		t.Fatalf("%s x-olivares-sdk-family = %q, want %q — unmarked means untyped stubs "+
			"in all four SDKs, with every generation gate still green",
			key, operation.SDKFamily, authCapabilitiesFamily)
	}
	// The schemas are named by $ref in the document so ONE definition serves both the
	// published components and these DTOs; they must still resolve to concrete objects.
	if operation.sessionsCommunicationBodyType() != "AuthCapabilityQuestions" ||
		operation.sessionsCommunicationResultType() != "AuthCapabilityResults" {
		t.Fatalf("%s typed body/result = %q/%q", key,
			operation.sessionsCommunicationBodyType(), operation.sessionsCommunicationResultType())
	}
	// The finite contract, per DTO. Losing any one of these is the regression R5 names:
	// the SDK would still compile and still say nothing about what it may return.
	want := map[string][]string{
		"AuthCapabilityQuestions": {"schema_version", "questions"},
		"AuthCapabilityQuestion":  {"id", "kind", "operation", "workspace_id", "selectors"},
		"AuthCapabilitySelectors": {"path", "body"},
		"AuthCapabilityResults":   {"schema_version", "results"},
		"AuthCapabilityResult":    {"id", "kind", "state", "code", "observed_at", "refresh_after_ms"},
	}
	catalog := doc.sessionsCommunicationSchemas
	if catalog == nil {
		t.Fatal("the typed catalog is absent from the committed snapshot")
	}
	for name, fields := range want {
		object, ok := catalog.byName[name]
		if !ok {
			t.Errorf("the typed catalog has no %s", name)
			continue
		}
		present := make(map[string]bool, len(object.Fields))
		for _, field := range object.Fields {
			present[field.Name] = true
		}
		for _, field := range fields {
			if !present[field] {
				t.Errorf("%s lacks the published field %q", name, field)
			}
		}
	}
	// The two selector maps are the reason the type model learned a map kind at all:
	// their keys belong to the ROUTE, so they cannot be struct fields.
	if selectors, ok := catalog.byName["AuthCapabilitySelectors"]; ok {
		for _, field := range selectors.Fields {
			if field.Type.Kind != "map" {
				t.Errorf("AuthCapabilitySelectors.%s kind = %q, want map — a declared "+
					"string map is what the ratified wire contract publishes", field.Name, field.Type.Kind)
			}
		}
	}
	goSource, err := emitGo(doc)
	if err != nil {
		t.Fatal(err)
	}
	// Every language, from the SAME catalog. The per-language parsers are the ones the
	// communication family already uses, so a DTO that reached only one SDK is a failure
	// here rather than a difference nobody compares.
	for language, parsed := range map[string]generatedSessionsCommunicationTypes{
		"Go":         parseGoSessionsCommunicationTypes(t, string(goSource)),
		"Python":     parsePythonSessionsCommunicationTypes(t, string(emitPython(doc))),
		"TypeScript": parseTypeScriptSessionsCommunicationTypes(t, string(emitTypeScript(doc))),
		"Java":       parseJavaSessionsCommunicationTypes(t, string(emitJava(doc))),
	} {
		for name := range want {
			if _, ok := parsed[name]; !ok {
				t.Errorf("%s generated layer lacks %s", language, name)
			}
		}
	}
	// And the untyped shape the review measured must be gone from the emitted operation.
	if strings.Contains(string(goSource), "func (c *Client) PostV1AuthCapabilities(ctx context.Context, input PostV1AuthCapabilitiesInput, opts ...RequestOption) (map[string]any, error)") {
		t.Error("the Go capability operation still returns map[string]any")
	}
}

// ── F2/F4 regressions ────────────────────────────────────────────────────────────
//
// Permanent home of two defects found by the R2 independent review of the capability
// SDK increment (`an internal design note (not shipped)
// REPORT.md`, findings F2 and F4). Both were invisible to the existing suite: the
// generated artifacts reproduced byte-for-byte and every typed-contract assertion
// passed while a recursive document killed the process and a stable operation shipped
// labelled beta.

// capabilityCycleFixture writes a copy of the published document with ONE mutation, so
// what the fixture proves is attributable to that mutation and nothing else.
func capabilityCycleFixture(t *testing.T, mutate func(schemas map[string]any)) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "openapi", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	mutate(doc["components"].(map[string]any)["schemas"].(map[string]any))
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fixture.json")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// capabilityCycleMutations are the recursive shapes that must all be refused. They are
// named because they fail in DIFFERENT places: a self reference at the top level is one
// dereference, a nested self reference is caught only while walking properties, and a
// mutual cycle needs the trail to survive across two components.
var capabilityCycleMutations = map[string]func(map[string]any){
	"top-level self": func(schemas map[string]any) {
		schemas["CapabilityQuestion"] = map[string]any{"$ref": "#/components/schemas/CapabilityQuestion"}
	},
	"nested self through a property": func(schemas map[string]any) {
		schemas["CapabilityQuestion"].(map[string]any)["properties"].(map[string]any)["selectors"] =
			map[string]any{"$ref": "#/components/schemas/CapabilityQuestion"}
	},
	"mutual across two components": func(schemas map[string]any) {
		schemas["CapabilityQuestion"].(map[string]any)["properties"].(map[string]any)["selectors"] =
			map[string]any{"$ref": "#/components/schemas/CapabilitySelectors"}
		schemas["CapabilitySelectors"].(map[string]any)["properties"].(map[string]any)["path"] =
			map[string]any{"$ref": "#/components/schemas/CapabilityQuestion"}
	},
	"deep nested self below the top level": func(schemas map[string]any) {
		schemas["CapabilityResults"].(map[string]any)["properties"].(map[string]any)["results"] =
			map[string]any{"type": "array", "items": map[string]any{"$ref": "#/components/schemas/CapabilityResults"}}
	},
}

// TestGeneratorRefCyclesAreControlledErrors is the PARENT. It runs each recursive
// fixture in a child process whose stack is capped, so that a regression is reported as
// a failed subtest instead of taking the whole package down with it.
//
// ⛔ THE BOUNDED CHILD IS THE REVIEWER'S TECHNIQUE, ADOPTED DELIBERATELY. Before the
// fix, `load` on the nested fixture recursed until the stack was gone: the reviewer
// measured `fatal error: stack overflow` from a child capped at 128 KiB. Running these
// in-process would make a future regression crash the test binary — still loud, but it
// would take every other assertion in this package with it, and an unbounded recursion
// is a poor thing to leave in a permanent suite.
func TestGeneratorRefCyclesAreControlledErrors(t *testing.T) {
	for name := range capabilityCycleMutations {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0],
				"-test.run=^TestGeneratorRefCycleChild$", "-test.v")
			cmd.Env = append(os.Environ(), "OLIVARES_GENERATOR_CYCLE_CHILD="+name)
			output, err := cmd.CombinedOutput()
			if err != nil {
				head := string(output)
				if len(head) > 1200 {
					head = head[:1200]
				}
				t.Fatalf("recursive schema %q did not produce a controlled error "+
					"(child err=%v): a cycle must be refused, never recursed into\n%s",
					name, err, head)
			}
		})
	}
}

// TestGeneratorRefCycleChild runs ONE recursive fixture with a capped stack. It is inert
// unless the parent selects it, so an ordinary package run never executes it directly.
func TestGeneratorRefCycleChild(t *testing.T) {
	name := os.Getenv("OLIVARES_GENERATOR_CYCLE_CHILD")
	if name == "" {
		t.Skip("child fixture: selected by TestGeneratorRefCyclesAreControlledErrors")
	}
	mutate, ok := capabilityCycleMutations[name]
	if !ok {
		t.Fatalf("unknown cycle fixture %q", name)
	}
	debug.SetMaxStack(128 << 10) // Bound this owned negative fixture before calling product code.
	_, err := load(capabilityCycleFixture(t, mutate))
	if err == nil {
		t.Fatalf("recursive schema %q was accepted", name)
	}
	t.Logf("CONTROLLED_CYCLE_ERROR %s: %v", name, err)
}

// TestGeneratorAcyclicRepeatedReferencesStillGenerate is the POSITIVE CONTROL, and it is
// the assertion that makes the four above mean something. A cycle guard that also
// rejected an ordinary repeated reference would pass every negative case here and break
// generation of correct documents — which is why the guard is a stack of components
// currently being expanded, not a set of components ever seen.
func TestGeneratorAcyclicRepeatedReferencesStillGenerate(t *testing.T) {
	// The published document already shares CapabilitySelectors and repeats primitive
	// schemas; this adds a SECOND, independent reference to the same component from a
	// different parent, which a visited-set guard would refuse.
	path := capabilityCycleFixture(t, func(schemas map[string]any) {
		schemas["CapabilityResult"].(map[string]any)["properties"].(map[string]any)["selectors"] =
			map[string]any{"$ref": "#/components/schemas/CapabilitySelectors"}
	})
	doc, err := load(path)
	if err != nil {
		t.Fatalf("an acyclic repeated reference was refused: %v", err)
	}
	catalog := doc.sessionsCommunicationSchemas
	if catalog == nil {
		t.Fatal("the typed catalog is absent")
	}
	if _, ok := catalog.byName["AuthCapabilitySelectors"]; !ok {
		t.Error("the shared component did not reach the catalog")
	}
	// And the unmutated published document must still generate, which is the broadest
	// positive control available here.
	if _, err := load(filepath.Join("..", "..", "web", "openapi", "openapi.json")); err != nil {
		t.Fatalf("the published document no longer generates: %v", err)
	}
}

// TestTypedEmittersPublishParsedStability covers F4 across BOTH typed families, because
// the defect was a constant that was true for one family and false for the next.
func TestTypedEmittersPublishParsedStability(t *testing.T) {
	stable, err := load(filepath.Join("..", "..", "web", "openapi", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	beta, err := load(filepath.Join("..", "..", "web", "openapi", "openapi.beta.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, probe := range []struct {
		name, path string
		doc        *Document
		want       string
	}{
		{"capabilities stays stable", "/v1/auth/capabilities", stable, "stable"},
		{"communication stays beta", "/v1/m/sessions/channels", beta, "beta"},
	} {
		t.Run(probe.name, func(t *testing.T) {
			var operation *Operation
			for index := range probe.doc.Operations {
				if probe.doc.Operations[index].Path == probe.path &&
					probe.doc.Operations[index].sessionsCommunicationTyped() {
					operation = &probe.doc.Operations[index]
					break
				}
			}
			if operation == nil {
				t.Fatalf("%s has no typed operation in this document", probe.path)
			}
			if operation.Stability != probe.want {
				t.Fatalf("published x-stability = %q, want %q — the fixture, not the "+
					"emitter, has moved", operation.Stability, probe.want)
			}
			emitted := string(emitTypeScript(probe.doc))
			marker := operation.Method + " " + probe.path + " —"
			start := strings.Index(emitted, marker)
			if start < 0 {
				t.Fatalf("the emitted client documents no %s", marker)
			}
			comment := emitted[start : start+strings.Index(emitted[start:], "*/")]
			if !strings.Contains(comment, "Stability: "+probe.want+".") {
				t.Errorf("the typed emitter relabels %s: source=%s, emitted comment=%q",
					probe.path, probe.want, comment)
			}
		})
	}
}

// ── N3-A: the schema 2 wire vocabulary reaches the generated surfaces ───────────────
//
// The N2 non-disclosure amendment (docs/contracts/CAPABILITY-PROJECTION.md) moved the
// endpoint to `schema_version` 2, added the `undisclosed` state and replaced the public
// `not_available` code with `not_disclosed`. The SDK DTOs type `state`/`code` as open
// strings and `schema_version` as an integer by this generator's convention, so the only
// generated places that can CONTRADICT the wire contract are the committed snapshot the
// emitters read and the strict web types derived from it. This pins the snapshot: the
// document the four emitters and `openapi-typescript` consume must publish exactly the
// ratified vocabulary, with no schema 1 fallback.
//
// It also keeps the two versions apart on purpose: `x-olivares-sdk-family` is the
// emitter discriminator (`auth-capabilities-v1`, which selects the DTO family and its
// names) and `schema_version` is the wire contract version (2). Bumping one must never
// be mistaken for bumping the other.
func TestAuthCapabilitiesSchema2VocabularyIsPublished(t *testing.T) {
	stable, err := load(filepath.Join("..", "..", "web", "openapi", "openapi.json"))
	if err != nil {
		t.Fatalf("committed stable snapshot does not load: %v", err)
	}
	beta, err := load(filepath.Join("..", "..", "web", "openapi", "openapi.beta.json"))
	if err != nil {
		t.Fatalf("committed beta snapshot does not load: %v", err)
	}
	const key = "POST /v1/auth/capabilities"
	var operation *Operation
	for index := range stable.Operations {
		if stable.Operations[index].Method+" "+stable.Operations[index].Path == key {
			operation = &stable.Operations[index]
		}
	}
	if operation == nil {
		t.Fatalf("%s is absent from the committed stable snapshot", key)
	}
	for _, op := range beta.Operations {
		if op.Method+" "+op.Path == key {
			t.Fatalf("%s is also published in the beta snapshot; the stable core owns it", key)
		}
	}
	if operation.SDKFamily != "auth-capabilities-v1" {
		t.Fatalf("x-olivares-sdk-family = %q, want auth-capabilities-v1: the emitter family "+
			"annotation is independent of the wire schema version and must not move with it",
			operation.SDKFamily)
	}

	property := func(component, field string) map[string]any {
		t.Helper()
		raw, ok := stable.componentSchemas[component]
		if !ok {
			t.Fatalf("#/components/schemas/%s is absent", component)
		}
		var schema struct {
			Properties map[string]map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("%s does not parse: %v", component, err)
		}
		prop, ok := schema.Properties[field]
		if !ok {
			t.Fatalf("%s.%s is absent", component, field)
		}
		return prop
	}
	enumOf := func(component, field string) []any {
		t.Helper()
		values, ok := property(component, field)["enum"].([]any)
		if !ok {
			t.Fatalf("%s.%s publishes no enum: an open value here would let a client send "+
				"or read a version/vocabulary the endpoint never accepts", component, field)
		}
		return values
	}

	// Wire version 2 on the request AND the result, as a closed enum. Schema 1 has no
	// fallback: it is rejected with 400 before any question lookup.
	for _, component := range []string{"CapabilityQuestions", "CapabilityResults"} {
		values := enumOf(component, "schema_version")
		if len(values) != 1 || values[0] != float64(2) {
			t.Errorf("%s.schema_version enum = %v, want [2]", component, values)
		}
	}
	contains := func(values []any, want string) bool {
		for _, v := range values {
			if v == want {
				return true
			}
		}
		return false
	}
	state := enumOf("CapabilityResult", "state")
	if !contains(state, "undisclosed") {
		t.Errorf("CapabilityResult.state enum %v lacks undisclosed", state)
	}
	for _, retained := range []string{"allowed", "denied", "unknown", "reachable", "not_reachable"} {
		if !contains(state, retained) {
			t.Errorf("CapabilityResult.state enum %v lost %s", state, retained)
		}
	}
	code := enumOf("CapabilityResult", "code")
	if !contains(code, "not_disclosed") {
		t.Errorf("CapabilityResult.code enum %v lacks not_disclosed", code)
	}
	if contains(code, "not_available") || contains(state, "not_available") {
		t.Errorf("the schema 1 code not_available survives in state=%v code=%v", state, code)
	}
	for _, retained := range []string{"authorized", "admitted", "not_permitted", "not_supported",
		"inputs_required", "engine_unready", "evidence_unavailable", "step_up_required", "stale"} {
		if !contains(code, retained) {
			t.Errorf("CapabilityResult.code enum %v lost %s", code, retained)
		}
	}
	if property("CapabilityResult", "refresh_after_ms")["type"] != "integer" {
		t.Errorf("CapabilityResult.refresh_after_ms is no longer an integer budget")
	}

	// The emitters bind their output to the snapshot bytes; the constants the four
	// SDKs ship must be the hash of THIS document pair, or a consumer cannot tell which
	// vocabulary its client was generated from.
	union, err := loadUnion(
		filepath.Join("..", "..", "web", "openapi", "openapi.json"),
		filepath.Join("..", "..", "web", "openapi", "openapi.beta.json"))
	if err != nil {
		t.Fatalf("the snapshot pair does not load as a union: %v", err)
	}
	if union.SpecHash == "" {
		t.Fatal("the union carries no spec hash")
	}
	for path, want := range map[string]string{
		filepath.Join("..", "go", "version.gen.go"):                                                        `SpecHash = "` + union.SpecHash + `"`,
		filepath.Join("..", "python", "src", "olivares_client", "_operations.py"):                          `SPEC_HASH = "` + union.SpecHash + `"`,
		filepath.Join("..", "typescript", "src", "version.gen.ts"):                                         `SPEC_HASH = "` + union.SpecHash + `"`,
		filepath.Join("..", "java", "src", "main", "java", "ai", "olivares", "client", "ApiMetadata.java"): `SPEC_HASH = "` + union.SpecHash + `"`,
	} {
		if !strings.Contains(mustRead(t, path), want) {
			t.Errorf("%s does not bind the committed snapshot pair (want %s): the SDK was "+
				"generated from another vocabulary", path, want)
		}
	}
}

// ── Java request-body documentation ──────────────────────────────────────────────
//
// Permanent guard for the inherited finding of the N3-A independent review
// (`an internal design note (not shipped)`,
// "Java required-body javadoc"): `Client.postV1AuthCapabilities` said its request body was
// optional and could be null, while OpenAPI declares it required, the generated input record
// calls `Objects.requireNonNull(body, "body")` and the seam is `doJsonRequired`. The prose
// was keyed on `bodyRequiredInSignature`, a SIGNATURE convention that is false for every
// stable operation — so it described a different contract than the code beneath it.
//
// Two halves. The committed snapshots pin the operation the review measured (typed, stable,
// no disposition) and the typed communication family; a synthetic union then covers every
// body class the Java emitter can produce — typed required, JSON required, raw required,
// contract-required behind the stable nullable signature, optional, legacy-inferred
// optional, bodyless and no-body — demanding the exact paragraph on BOTH overloads and none
// on operations without a body. A file-wide count closes the door on prose landing on any
// operation the map does not name.

// javaMethodPattern matches the operation layer's own declarations (four-space indent);
// record accessors sit deeper and are not collected.
var javaMethodPattern = regexp.MustCompile(`^    public (?:static )?[\w<>, \[\]]+ (\w+)\(`)

// javaOperationDocs maps each generated top-level method name to the Javadoc text of every
// overload emitted for it, in emission order.
func javaOperationDocs(source string) map[string][]string {
	out := map[string][]string{}
	var doc []string
	inDoc, pending := false, false
	for _, line := range strings.Split(source, "\n") {
		switch {
		case line == "    /**":
			inDoc, pending, doc = true, false, nil
		case inDoc && line == "     */":
			inDoc, pending = false, true
		case inDoc:
			doc = append(doc, line)
		case pending && strings.HasPrefix(line, "    @"):
			// an annotation such as @Deprecated between the Javadoc and its declaration
		case pending:
			pending = false
			if match := javaMethodPattern.FindStringSubmatch(line); match != nil {
				out[match[1]] = append(out[match[1]], strings.Join(doc, "\n"))
			}
		}
	}
	return out
}

const (
	javaDocBodyOptional         = "The request body is optional; pass {@code null} to omit it."
	javaDocBodyTypedRequired    = "The request body is required: the {@code input} record rejects a {@code null} body."
	javaDocBodyJSONRequired     = "The request body is required; {@code null} is sent as the JSON value {@code null},\n     * never omitted."
	javaDocBodyRawRequired      = "The request body is required and travels as raw bytes; {@code null} sends no body."
	javaDocBodyDeclaredRequired = "The contract declares the request body required. This stable signature keeps its\n     * historical nullable parameter, so {@code null} omits the body instead of satisfying it."
)

// assertJavaBodyDoc demands that every overload of name carries exactly the paragraph
// want — or, when want is empty, mentions no request body at all.
func assertJavaBodyDoc(t *testing.T, docs map[string][]string, name, want string) {
	t.Helper()
	overloads := docs[name]
	if len(overloads) != 2 {
		t.Errorf("%s: %d documented overloads, want 2 (convenience + RequestOptions)", name, len(overloads))
		return
	}
	for index, doc := range overloads {
		mentions := strings.Count(strings.ToLower(doc), "request body")
		switch {
		case want == "" && mentions != 0:
			t.Errorf("%s overload %d documents a request body it does not carry:\n%s", name, index, doc)
		case want != "" && !strings.Contains(doc, want):
			t.Errorf("%s overload %d lacks the paragraph %q; Javadoc was:\n%s", name, index, want, doc)
		case want != "" && mentions != 1:
			t.Errorf("%s overload %d mentions the request body %d times, want exactly once:\n%s", name, index, mentions, doc)
		}
	}
}

func TestJavaRequestBodyDocumentationMatchesTheEmittedSeam(t *testing.T) {
	// Half one: the committed snapshots. The stable document carries the operation the
	// review measured; the beta document carries the typed communication family, whose
	// body-bearing operations use the same required seam and whose reads carry no body.
	stable, err := load(filepath.Join("..", "..", "web", "openapi", "openapi.json"))
	if err != nil {
		t.Fatalf("committed stable snapshot does not load: %v", err)
	}
	stableDocs := javaOperationDocs(string(emitJava(stable)))
	assertJavaBodyDoc(t, stableDocs, "postV1AuthCapabilities", javaDocBodyTypedRequired)
	beta, err := load(filepath.Join("..", "..", "web", "openapi", "openapi.beta.json"))
	if err != nil {
		t.Fatalf("committed beta snapshot does not load: %v", err)
	}
	betaDocs := javaOperationDocs(string(emitJava(beta)))
	for _, op := range beta.Operations {
		if !op.sessionsCommunicationTyped() {
			continue
		}
		want := ""
		if op.sessionsCommunicationBodyType() != "" {
			want = javaDocBodyTypedRequired
		}
		assertJavaBodyDoc(t, betaDocs, op.javaName(), want)
	}

	// Half two: a synthetic union with one operation per body class. The stable half has no
	// dispositions (as the published stable document has none), so its declared-required body
	// keeps the historical nullable signature; the beta half is the shared classified fixture.
	stableFixture := filepath.Join(t.TempDir(), "openapi.json")
	if err := os.WriteFile(stableFixture, []byte(`{
  "openapi": "3.1.0",
  "info": {"title": "t", "version": "v1"},
  "paths": {
    "/v1/things": {
      "get": {"summary": "List things", "x-stability": "stable"},
      "post": {"summary": "Create a thing", "x-stability": "stable",
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"type": "object"}}}}}
    },
    "/v1/things/{id}/rotate": {
      "post": {"summary": "Rotate a thing (nothing declared)", "x-stability": "stable"}
    },
    "/v1/things/{id}/archive": {
      "post": {"summary": "Archive a thing (raw response)", "x-stability": "stable",
        "responses": {"200": {"description": "archive", "content": {"application/octet-stream": {"schema": {"type": "string", "format": "binary"}}}}}}
    }
  }
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if err := run(stableFixture, writeBetaFixture(t), out); err != nil {
		t.Fatal(err)
	}
	java := mustRead(t, filepath.Join(out, "java", "src", "main", "java", "ai", "olivares", "client", "Client.java"))
	docs := javaOperationDocs(java)
	want := map[string]string{
		// stable, no dispositions
		"getV1Things":             "",                          // no body
		"postV1Things":            javaDocBodyDeclaredRequired, // contract-required, nullable signature, doJson
		"postV1ThingsByIdRotate":  javaDocBodyOptional,         // legacy verb inference, nothing declared
		"postV1ThingsByIdArchive": "",                          // raw response: doRaw has no body slot
		// beta, classified
		"getV1MFinopsSpend":                     "",                      // no body
		"postV1MFinopsRecalculate":              "",                      // bodyless disposition
		"getV1MEvalsRunsByIdStream":             "",                      // raw response
		"putV1MSessionsWorkspacesByRefFilesRaw": javaDocBodyRawRequired,  // octet-stream, required
		"postV1MKnowledgeMemoryImport":          javaDocBodyRawRequired,  // x-ndjson, required
		"postV1MComplianceOscalProfiles":        javaDocBodyRawRequired,  // opaque JSON, required
		"postV1MTestRequired":                   javaDocBodyJSONRequired, // schema-published, required
		"postV1MTestOptional":                   javaDocBodyOptional,     // schema-published, optional
	}
	documented := 0
	for name, paragraph := range want {
		assertJavaBodyDoc(t, docs, name, paragraph)
		if paragraph != "" {
			documented++
		}
	}
	// Every mention in the file belongs to one of the named overloads: prose that lands on an
	// operation this map does not list is a defect the per-name checks cannot see.
	if got, wantCount := strings.Count(strings.ToLower(java), "request body"), 2*documented; got != wantCount {
		t.Errorf("generated Java mentions the request body %d times, want %d (two overloads × %d documented operations)", got, wantCount, documented)
	}
}

// statementExportFixture is the statement export beside its JSON sibling: the same
// resource, one segment apart, so the emitters are measured on DISCRIMINATION and not
// on a document where everything happens to be raw.
const statementExportFixture = `{
  "openapi": "3.1.0",
  "info": {"title": "beta", "version": "v1", "x-stability-policy": "https://docs.olivares.invalid/reference/api-stability/"},
  "paths": {
    "/v1/m/finops/statements/{id}": {"get": {"summary": "finops module route (requires finops:spend:read)",
      "x-stability": "beta", "x-required-permission": "finops:spend:read"}},
    "/v1/m/finops/statements/{id}/export": {"get": {"summary": "finops module route (requires finops:spend:read)",
      "x-stability": "beta", "x-required-permission": "finops:spend:read",
      "responses": {"200": {"description": "OK (text/csv)", "content": {"text/csv": {"schema": {"type": "string"}}}}}}}
  }
}`

// TestStatementExportCSVGeneratesRawSeams pins what a 200 declared `text/csv` must
// become in all four emitters: a raw signature ([]byte / bytes / string / String) and a
// raw transport seam, never a JSON decoder.
//
// ⛔ WHY IT EXISTS. The generator ALREADY implements this branch — isRawBody() is true
// for any 200 whose content has no application/json entry. What was wrong was the
// INPUT: the document declared this operation's 200 as a JSON object, so the correct
// branch was never selected and four SDKs shipped a JSON decode over a CSV body. This
// test pins the branch to the media type so a future document change cannot silently
// move the operation back onto the JSON seam without a red test.
func TestStatementExportCSVGeneratesRawSeams(t *testing.T) {
	beta := filepath.Join(t.TempDir(), "openapi.beta.json")
	if err := os.WriteFile(beta, []byte(statementExportFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if err := run(writeFixture(t), beta, out); err != nil {
		t.Fatal(err)
	}

	const exportPath = `"/v1/m/finops/statements/{id}/export"`
	for _, emitter := range []struct {
		lang     string
		file     string
		rawSeam  string   // the transport call the CSV operation MUST use
		jsonSeam []string // the seams it must NOT use
		want     []string // exact signature + exact raw call
		sibling  string   // the JSON sibling's call, proving the classifier discriminates
	}{
		{
			lang: "Go", file: filepath.Join("go", "operations.gen.go"),
			rawSeam: "c.doRaw(", jsonSeam: []string{"c.do(", "c.doJSONRequired("},
			want: []string{
				"func (c *Client) GetV1MFinopsStatementsByIDExport(ctx context.Context, id string, opts ...RequestOption) ([]byte, error)",
				`return c.doRaw(ctx, "GET", "/v1/m/finops/statements/{id}/export", "/v1/m/finops/statements/"+pathEscape(id)+"/export", opts...)`,
			},
			sibling: `return c.do(ctx, "GET", "/v1/m/finops/statements/{id}", "/v1/m/finops/statements/"+pathEscape(id), nil, opts...)`,
		},
		{
			lang: "Python", file: filepath.Join("python", "src", "olivares_client", "_operations.py"),
			rawSeam: "self._do_raw(", jsonSeam: []string{"self._do(", "self._do_json_required("},
			want: []string{
				"def get_v1_m_finops_statements_by_id_export(self, id, *, tenant=None, **query):",
				`return self._do_raw("GET", "/v1/m/finops/statements/{id}/export", "/v1/m/finops/statements/" + quote(str(id), safe="") + "/export", query=query, tenant=tenant)`,
			},
			sibling: `return self._do("GET", "/v1/m/finops/statements/{id}", "/v1/m/finops/statements/" + quote(str(id), safe=""), query=query, tenant=tenant)`,
		},
		{
			lang: "TypeScript", file: filepath.Join("typescript", "src", "operations.gen.ts"),
			rawSeam: "this.doRaw(", jsonSeam: []string{"this.do(", "this.doJsonRequired("},
			want: []string{
				"getV1MFinopsStatementsByIdExport(id: string, opts?: RequestOptions): Promise<string>",
				"return this.doRaw(\"GET\", \"/v1/m/finops/statements/{id}/export\", `/v1/m/finops/statements/${encodeURIComponent(id)}/export`, opts);",
			},
			sibling: "return this.do(\"GET\", \"/v1/m/finops/statements/{id}\", `/v1/m/finops/statements/${encodeURIComponent(id)}`, undefined, opts);",
		},
		{
			lang: "Java", file: filepath.Join("java", "src", "main", "java", "ai", "olivares", "client", "Client.java"),
			rawSeam: "doRaw(", jsonSeam: []string{"doJson(", "doJsonRequired("},
			want: []string{
				"public String getV1MFinopsStatementsByIdExport(String id, RequestOptions options) {",
				`return doRaw("GET", "/v1/m/finops/statements/{id}/export", "/v1/m/finops/statements/" + escapePath(id) + "/export", options);`,
			},
			sibling: `return doJson("GET", "/v1/m/finops/statements/{id}", "/v1/m/finops/statements/" + escapePath(id), null, options);`,
		},
	} {
		t.Run(emitter.lang, func(t *testing.T) {
			source := mustRead(t, filepath.Join(out, emitter.file))
			for _, want := range emitter.want {
				if !strings.Contains(source, want) {
					t.Errorf("%s missing %q", emitter.lang, want)
				}
			}
			if !strings.Contains(source, emitter.sibling) {
				t.Errorf("%s: the JSON sibling GET /statements/{id} lost its JSON seam; the classifier must not be by suffix: want %q",
					emitter.lang, emitter.sibling)
			}
			// Every line that names the export operation must be on the raw seam. This
			// is what makes the assertion about the OPERATION and not about the file:
			// a JSON call re-appearing for this path fails here even if the raw one
			// also survives somewhere.
			for _, line := range strings.Split(source, "\n") {
				if !strings.Contains(line, exportPath) || !strings.Contains(line, "(") {
					continue
				}
				if !strings.Contains(line, emitter.rawSeam) {
					t.Errorf("%s: export call does not use %s: %s", emitter.lang, emitter.rawSeam, strings.TrimSpace(line))
				}
				for _, bad := range emitter.jsonSeam {
					if strings.Contains(line, bad) {
						t.Errorf("%s: export call uses the JSON seam %s: %s", emitter.lang, bad, strings.TrimSpace(line))
					}
				}
			}
		})
	}
}
