// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
