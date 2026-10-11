// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/modules/internal/oastest"
)

func TestWorkspaceAPIDLPDefaultMatchesOpenAPI(t *testing.T) {
	m := New()
	op := oastest.Op(t, m, "post", "/v1/m/sessions/workspaces")
	body := oastest.Map(t, op["requestBody"], "request body")
	content := oastest.Map(t, body["content"], "content")
	jsonBody := oastest.Map(t, content["application/json"], "JSON body")
	schema := oastest.Map(t, jsonBody["schema"], "schema")
	props := oastest.Map(t, schema["properties"], "properties")
	dlp := oastest.Map(t, props["dlp_mode"], "dlp_mode")
	dlpString := oastest.Map(t, dlp["anyOf"].([]any)[0], "DLP string")

	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "workspace-dlp-contract")
	for _, variant := range []string{"omitted", "empty", "null"} {
		t.Run(variant, func(t *testing.T) {
			request := map[string]any{"root_path": t.TempDir()}
			switch variant {
			case "empty":
				request["dlp_mode"] = ""
			case "null":
				request["dlp_mode"] = nil
			}
			r := h.doJSON(http.MethodPost, "/v1/m/sessions/workspaces", admin, request, tenantHdr(tenant))
			if r.code != http.StatusCreated {
				t.Fatalf("register = %d %s", r.code, r.raw)
			}
			if got := r.body["dlp_mode"]; got != "off" {
				t.Fatalf("dlp_mode = %v; want off", got)
			}
			want := fmt.Sprintf("Empty/null defaults to %s.", r.body["dlp_mode"])
			if got := dlpString["description"]; got != want {
				t.Errorf("OpenAPI DLP description = %q; want %q to match the server", got, want)
			}
		})
	}
}

func TestSessionsClosureRequestBodyCensus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method, pattern string
		kind            sessionsClosureRequestBodyKind
		required        bool
	}{
		{http.MethodPost, "/runs", sessionsClosureBodyful, true},
		{http.MethodDelete, "/runs/{ref}", sessionsClosureBodyless, false},
		// Optional body since the worktree option: the confirmation to discard a session's
		// worktree and branch. A call with no body is the call it always was.
		{http.MethodPost, "/runs/{ref}/cleanup", sessionsClosureBodyful, false},
		{http.MethodPost, "/runs/{ref}/resume", sessionsClosureBodyless, false},
		{http.MethodPut, "/runs/{ref}/peers", sessionsClosureBodyful, true},
		{http.MethodPost, "/runs/{ref}/preview", sessionsClosureBodyful, true},
		{http.MethodPost, "/templates", sessionsClosureBodyful, true},
		{http.MethodDelete, "/templates/{id}", sessionsClosureBodyless, false},
		{http.MethodPut, "/templates/{id}", sessionsClosureBodyful, true},
		{http.MethodPost, "/templates/{id}/apply", sessionsClosureBodyful, false},
		{http.MethodPost, "/templates/{id}/duplicate", sessionsClosureBodyful, true},
		{http.MethodPost, "/workspaces", sessionsClosureBodyful, true},
		{http.MethodPatch, "/workspaces/{ref}", sessionsClosureBodyful, true},
		{http.MethodDelete, "/workspaces/{ref}", sessionsClosureBodyless, false},
		{http.MethodDelete, "/workspaces/{ref}/files", sessionsClosureBodyless, false},
		{http.MethodPost, "/workspaces/{ref}/files/dir", sessionsClosureBodyless, false},
		{http.MethodPost, "/workspaces/{ref}/files/move", sessionsClosureBodyful, true},
		{http.MethodPost, "/provider-profiles", sessionsClosureBodyful, true},
		{http.MethodPatch, "/provider-profiles/{ref}", sessionsClosureBodyful, true},
		{http.MethodPost, "/provider-profiles/resolve", sessionsClosureBodyful, true},
		{http.MethodPost, "/provider-profiles/{ref}/retire", sessionsClosureBodyless, false},
		{http.MethodPost, "/provider-source-bindings", sessionsClosureBodyful, true},
		{http.MethodPost, "/provider-source-bindings/{ref}/revoke", sessionsClosureBodyless, false},
	}
	counts := map[sessionsClosureRequestBodyKind]int{}
	for _, test := range tests {
		route := operation{method: test.method, pattern: test.pattern}
		decl, ok := sessionsClosureRequestBodyDeclarationFor(route.method, route.pattern)
		if !ok || decl.kind != test.kind || decl.required != test.required {
			t.Fatalf("%s %s = (%#v, %t)", test.method, test.pattern, decl, ok)
		}
		body, hasBody := sessionsClosureRequestBody(route.method, route.pattern)
		if hasBody != (test.kind == sessionsClosureBodyful) {
			t.Fatalf("%s %s requestBody presence = %t", test.method, test.pattern, hasBody)
		}
		if hasBody && body["required"] != test.required {
			t.Fatalf("%s %s required = %#v", test.method, test.pattern, body["required"])
		}
		counts[test.kind]++
	}
	want := map[sessionsClosureRequestBodyKind]int{sessionsClosureBodyful: 15, sessionsClosureBodyless: 8}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("census = %#v, want %#v", counts, want)
	}
	// /runs/{ref}/interrupt LEFT this census when it gained its optional fenced
	// body, and the departure is asserted rather than assumed. Declaring it bodyless
	// here would not have failed anything — sessionsClosureRequestBody publishes
	// nothing for a bodyless route and the run-control switch in openapi.go
	// would still have emitted the schema — so the census would have gone on
	// claiming "no body" about a route that has one. Its contract is pinned with its
	// siblings in TestSessionsRuntimeWorkControlOpenAPI.
	interrupt := operation{method: http.MethodPost, pattern: "/runs/{ref}/interrupt"}
	if decl, ok := sessionsClosureRequestBodyDeclarationFor(interrupt.method, interrupt.pattern); ok {
		t.Fatalf("interrupt is declared in the closure census as %#v; its body belongs with /input and /stop", decl)
	}
}

// The worktree option is additive on both routes: an optional boolean on the run
// create body, and an optional body on cleanup, each in a closed object like the
// strict decoders that read them (modules/sessions runtime_dto.go).
func TestSessionsWorktreeOptionIsDeclaredAndOptional(t *testing.T) {
	t.Parallel()
	// The create body is a nullable closed object: anyOf [object, null].
	create := sessionsCreateRunSchema()
	if branches, ok := create["anyOf"].([]any); ok {
		create, _ = branches[0].(map[string]any)
	}
	if create["additionalProperties"] != false {
		t.Fatal("the run create decoder rejects unknown fields")
	}
	props, _ := create["properties"].(map[string]any)
	if _, ok := props["worktree"]; !ok {
		t.Fatal("the run create schema does not declare the worktree option the decoder accepts")
	}
	if _, ok := props["worktree_from"]; !ok {
		t.Fatal("the run create schema does not declare the worktree start the decoder accepts")
	}
	for _, name := range oastest.Strings(create["required"]) {
		if name == "worktree" || name == "worktree_from" {
			t.Fatalf("%s must stay optional: a launch without it is the launch it always was", name)
		}
	}
	cleanup := sessionsCleanupRunSchema()
	if cleanup["additionalProperties"] != false {
		t.Fatal("the cleanup decoder rejects unknown fields")
	}
	props, _ = cleanup["properties"].(map[string]any)
	if _, ok := props["discard_worktree"]; !ok || cleanup["required"] != nil {
		t.Fatalf("cleanup schema = %#v, want a closed object with an optional discard_worktree", cleanup)
	}
}

// A handoff's optional branch and sha are published beside the fields it always had, and
// neither is required: an offer written before they existed is still a valid offer.
func TestSessionsHandoffContentDeclaresItsOptionalGitPosition(t *testing.T) {
	t.Parallel()
	schema := sessionsHandoffContentSchema()
	if schema["additionalProperties"] != false {
		t.Fatal("the handoff content decoder rejects unknown fields")
	}
	props, _ := schema["properties"].(map[string]any)
	sha, _ := props["sha"].(map[string]any)
	if sha == nil || sha["type"] != "string" || sha["pattern"] != "^([0-9a-f]{40}|[0-9a-f]{64})$" {
		t.Fatalf("sha = %#v, want a string matching a full lowercase git object id", sha)
	}
	branch, _ := props["branch"].(map[string]any)
	if branch == nil || branch["type"] != "string" || branch["minLength"] != 1 || branch["maxLength"] != 512 {
		t.Fatalf("branch = %#v, want a bounded non-empty string", branch)
	}
	if got := oastest.Strings(schema["required"]); len(got) != 2 || got[0] != "next_action" || got[1] != "summary" {
		t.Fatalf("required = %v, want exactly next_action and summary", got)
	}
}

func TestSessionsClosureSchemasMatchStrictDTOs(t *testing.T) {
	t.Parallel()
	createTemplate := sessionsCreateTemplateSchema()
	if createTemplate["additionalProperties"] != false {
		t.Fatal("template decoder rejects unknown fields")
	}
	if got := oastest.Strings(createTemplate["required"]); !reflect.DeepEqual(got, []string{"name"}) {
		t.Fatalf("template required = %v", got)
	}
	move := sessionsMoveFileSchema()
	if got := oastest.Strings(move["required"]); !reflect.DeepEqual(got, []string{"from", "to"}) {
		t.Fatalf("move required = %v", got)
	}
	workspace := sessionsCreateWorkspaceSchema()
	if got := oastest.Strings(workspace["required"]); !reflect.DeepEqual(got, []string{"root_path"}) {
		t.Fatalf("workspace required = %v", got)
	}
	profile := sessionsCreateProviderProfileSchema()
	if profile["additionalProperties"] != false {
		t.Fatal("provider profile decoder rejects unknown fields")
	}
	if got := oastest.Strings(profile["required"]); !reflect.DeepEqual(got, []string{"config_home", "driver", "user_home"}) {
		t.Fatalf("provider profile required = %v", got)
	}
	// The run peers body is exactly one of its two fields, as session_peers.go
	// accepts: {} and both together are refused by the handler and the schema.
	peers := sessionsSetRunPeersSchema()
	branches, _ := peers["oneOf"].([]any)
	if peers["additionalProperties"] != false || len(branches) != 2 {
		t.Fatalf("peers schema = %#v, want a closed object with two oneOf branches", peers)
	}
	var one []string
	for _, b := range branches {
		one = append(one, oastest.Strings(b.(map[string]any)["required"])...)
	}
	if !reflect.DeepEqual(one, []string{"peers", "peers_rule"}) {
		t.Fatalf("peers oneOf requires %v, want peers or peers_rule", one)
	}
	resolve := sessionsResolveProviderProfileSchema()
	if got := oastest.Strings(resolve["required"]); resolve["additionalProperties"] != false || !reflect.DeepEqual(got, []string{"driver"}) {
		t.Fatalf("resolve schema = %#v, want a closed object requiring driver", resolve)
	}
	preview := sessionsParameters(http.MethodGet, "/provider-profiles/resolve")
	if len(preview) != 1 || preview[0]["name"] != "driver" || preview[0]["required"] != true {
		t.Fatalf("resolve preview parameters = %#v, want the required driver", preview)
	}
	for name, provider := range map[string]map[string]any{"create": sessionsCreateProviderSchema(), "patch": sessionsPatchProviderSchema()} {
		properties := provider["properties"].(map[string]any)
		if properties["default_model"] == nil || provider["additionalProperties"] != false {
			t.Fatalf("%s provider schema omits the optional default model or opens its body", name)
		}
		for _, required := range oastest.Strings(provider["required"]) {
			if required == "default_model" {
				t.Fatal("a provider default became mandatory")
			}
		}
	}
	binding := sessionsCreateProviderBindingSchema()
	if got := oastest.Strings(binding["required"]); !reflect.DeepEqual(got, []string{"profile_ref", "source_id", "source_revision"}) {
		t.Fatalf("provider binding required = %v", got)
	}
}

func TestSessionsClosureSchemaDeclaresProviderService(t *testing.T) {
	schema := sessionsCreateProviderSchema()
	properties := schema["properties"].(map[string]any)
	if properties["service"] == nil || schema["additionalProperties"] != false {
		t.Fatal("the provider-create schema rejects the handler's optional service")
	}
	if got := oastest.Strings(schema["required"]); !reflect.DeepEqual(got, []string{"api_key", "display_name", "kind"}) {
		t.Fatalf("provider service changed existing required fields: %v", got)
	}
}

func TestSessionsClosureSchemaDeclaresStrictTruncationChoice(t *testing.T) {
	body := sessionsTemplateBodySchema()
	object := body["anyOf"].([]any)[0].(map[string]any)
	policies := object["properties"].(map[string]any)["policies"].(map[string]any)
	policy := policies["anyOf"].([]any)[0].(map[string]any)
	fields := policy["properties"].(map[string]any)
	if fields["require_truncate_protection"] == nil || policy["additionalProperties"] != false {
		t.Fatal("the template schema rejects the saved optional truncation choice")
	}
	if len(oastest.Strings(policy["required"])) != 0 {
		t.Fatal("the template's optional policy fields became mandatory")
	}
}

// operation is one route of this module: method and module-relative pattern.
type operation struct{ method, pattern string }

// The base_url description must promise exactly the endpoint rule the engine
// enforces: https for every kind, plain http only at a loopback or private-network
// address and only for the kinds of the engine's plain-http branch. The kinds are
// read from that branch (modules/sessions/provider_record.go,
// validProviderBaseURL) the way web/src/features/providers/kinds.test.ts reads it,
// so a change to the Go rule without this description fails here, and so does a
// description that names a kind the engine would refuse plain http for (#527: the
// published contract said https only).
func TestSessionsClosureSchemaBaseURLDescriptionStatesEnginePlainHTTPRule(t *testing.T) {
	all, plainHTTP := engineProviderPlainHTTPKinds(t)
	schema := sessionsCreateProviderSchema()
	baseURL := schema["properties"].(map[string]any)["base_url"].(map[string]any)["anyOf"].([]any)[0].(map[string]any)
	description := baseURL["description"].(string)
	for _, want := range []string{"https", "plain http", "loopback", "private-network"} {
		if !strings.Contains(description, want) {
			t.Errorf("base_url description %q does not state %q", description, want)
		}
	}
	for _, kind := range all {
		named := regexp.MustCompile(`\b` + regexp.QuoteMeta(kind) + `\b`).MatchString(description)
		if named != slices.Contains(plainHTTP, kind) {
			t.Errorf("base_url description %q names %q; the engine's plain-http kinds are %v", description, kind, plainHTTP)
		}
	}
}

// engineProviderPlainHTTPKinds reads the provider kind constants and the plain-http
// branch of validProviderBaseURL from the engine's source. A renamed branch or a
// moved constant block is "could not look", never a pass.
func engineProviderPlainHTTPKinds(t *testing.T) (all, plainHTTP []string) {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test's own file")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "provider_record.go"))
	if err != nil {
		t.Fatalf("read the engine rule: %v", err)
	}
	values := map[string]string{}
	for _, m := range regexp.MustCompile(`(ProviderKind\w+)\s*=\s*"([^"]+)"`).FindAllStringSubmatch(string(src), -1) {
		values[m[1]] = m[2]
		all = append(all, m[2])
	}
	if len(values) == 0 {
		t.Fatal("no ProviderKind constants found; the reader is stale, not the rule")
	}
	branches := regexp.MustCompile(`if \(([^)]*)\) && strings\.HasPrefix\(lower, "http://"\)`).FindAllStringSubmatch(string(src), -1)
	if len(branches) == 0 {
		t.Fatal("no plain-http branch in validProviderBaseURL; the reader is stale, not the rule")
	}
	// Every branch, not only the first: a rule split across two conditions must
	// widen, never shrink, the kinds the texts promise (the review of #527).
	for _, branch := range branches {
		for _, m := range regexp.MustCompile(`kind == (ProviderKind\w+)`).FindAllStringSubmatch(branch[1], -1) {
			v, ok := values[m[1]]
			if !ok {
				t.Fatalf("the plain-http branch names %s, which has no constant", m[1])
			}
			if !slices.Contains(plainHTTP, v) {
				plainHTTP = append(plainHTTP, v)
			}
		}
	}
	if len(plainHTTP) == 0 {
		t.Fatal("the plain-http branch names no kind; the reader is stale, not the rule")
	}
	slices.Sort(all)
	slices.Sort(plainHTTP)
	return all, plainHTTP
}
