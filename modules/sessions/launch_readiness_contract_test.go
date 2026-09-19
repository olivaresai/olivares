// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

// The PUBLISHED contract of the readiness read, checked against the code that
// serves it.
//
// A generated client is only as honest as the document it was generated from,
// and the two ways this document can lie are both checked here: it can describe
// a route the engine does not mount (the beta document is a reflector, so that
// cannot happen), and it can describe the route with a shape the handler does
// not produce — which CAN happen, and is what the enum closure below forbids.

const launchReadinessSpecPath = "/v1/m/sessions/provider-profiles/{ref}/launch-readiness"

func launchReadinessOperation(t *testing.T) map[string]any {
	t.Helper()
	doc := api.ModuleOpenAPIDocument([]api.Module{New()})
	paths, _ := doc["paths"].(map[string]any)
	item, ok := paths[launchReadinessSpecPath].(map[string]any)
	if !ok {
		t.Fatalf("the beta document does not publish %s", launchReadinessSpecPath)
	}
	op, ok := item["get"].(map[string]any)
	if !ok {
		t.Fatalf("no GET operation at %s: %+v", launchReadinessSpecPath, item)
	}
	return op
}

// TestLaunchReadinessIsPublishedWithItsPermission proves the route reaches the
// contract with the permission it actually requires. A console generated from
// this document asks for profile:read and learns, from the document, that the
// launch is a different authorization.
func TestLaunchReadinessIsPublishedWithItsPermission(t *testing.T) {
	t.Parallel()
	op := launchReadinessOperation(t)
	if got := op["x-required-permission"]; got != string(permProfileRead) {
		t.Fatalf("x-required-permission = %v, want %s", got, permProfileRead)
	}
	if desc, _ := op["description"].(string); desc != "" && strings.Contains(strings.ToLower(desc), "launchable") {
		t.Fatalf("the published description reintroduces the launchable claim: %q", desc)
	}
}

// TestLaunchReadinessPublishesAClosedQuery proves the two parameters and their
// enums reach the document, so a generated client cannot invent a third.
func TestLaunchReadinessPublishesAClosedQuery(t *testing.T) {
	t.Parallel()
	op := launchReadinessOperation(t)
	params, _ := op["parameters"].([]any)
	got := map[string][]string{}
	for _, raw := range params {
		p, _ := raw.(map[string]any)
		name, _ := p["name"].(string)
		if p["in"] != "query" {
			continue
		}
		if req, _ := p["required"].(bool); req {
			t.Fatalf("%s is published as required; both selectors have a documented default", name)
		}
		schema, _ := p["schema"].(map[string]any)
		got[name] = enumStrings(t, schema)
	}
	want := map[string][]string{
		"transport": {"stream-json", "remote-control"},
		"isolation": {"native", "container", "sandbox"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("published query = %v, want %v", got, want)
	}
}

// TestLaunchReadinessPublishesATypedBody is the anti-envelope check: the 200
// must carry the real schema, because the generic `{"type":"object"}` is what
// forces a browser to re-implement the server's rules in TypeScript.
func TestLaunchReadinessPublishesATypedBody(t *testing.T) {
	t.Parallel()
	op := launchReadinessOperation(t)
	schema := successSchema(t, op)
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		t.Fatalf("the 200 publishes the generic envelope, not the readiness document: %+v", schema)
	}
	if schema["additionalProperties"] != false {
		t.Fatal("the document is published open; a closed object is what lets a client trust its fields")
	}
	// The fields a console cannot render this correction without.
	for _, field := range []string{
		"profile_ref", "profile_version", "driver", "profile_state", "environment_ref",
		"observed_at", "selection", "configuration_state", "checks", "transport_capabilities",
		"provider_authentication", "launch_authorization", "remaining_checks",
	} {
		if _, ok := props[field]; !ok {
			t.Fatalf("the published document has no %q", field)
		}
	}
	// ⛔ The two pending statements publish a SINGLE-VALUE state enum, so no
	// generated client can be written that waits for either to become ready.
	for _, field := range []string{"provider_authentication", "launch_authorization"} {
		sub, _ := props[field].(map[string]any)
		subProps, _ := sub["properties"].(map[string]any)
		state, _ := subProps["state"].(map[string]any)
		if got := enumStrings(t, state); !reflect.DeepEqual(got, []string{"unknown"}) {
			t.Fatalf("%s.state enum = %v, want exactly [unknown]", field, got)
		}
	}
	// And every status this read can answer is published, including the two that
	// distinguish "I could not look" from "there is nothing here".
	responses, _ := op["responses"].(map[string]any)
	for _, status := range []string{"200", "400", "401", "403", "404", "409", "429", "503"} {
		if _, ok := responses[status]; !ok {
			t.Fatalf("the operation does not publish %s: %v", status, responses)
		}
	}
}

// TestLaunchReadinessPublishedVocabularyIsClosedOverTheCode is the parity check
// that matters most: EVERY state, dimension, cause and remedy the module can
// emit is in the published enum, and the published enum contains nothing the
// module cannot emit. Either drift makes the contract a lie in one direction or
// the other.
func TestLaunchReadinessPublishedVocabularyIsClosedOverTheCode(t *testing.T) {
	t.Parallel()
	op := launchReadinessOperation(t)
	schema := successSchema(t, op)
	props, _ := schema["properties"].(map[string]any)

	state, _ := props["configuration_state"].(map[string]any)
	assertSameSet(t, "configuration_state", enumStrings(t, state), readinessAggregateStateVocabulary())

	checks, _ := props["checks"].(map[string]any)
	item, _ := checks["items"].(map[string]any)
	itemProps, _ := item["properties"].(map[string]any)

	// ⛔ THE PER-DIMENSION STATE IS ASSERTED SEPARATELY FROM THE AGGREGATE, and it
	// has to be: they are DIFFERENT vocabularies now. One dimension can be
	// not_applicable; a whole configuration cannot. Comparing only one of them
	// would let the other drift silently in either direction.
	dimensionState, _ := itemProps["state"].(map[string]any)
	assertSameSet(t, "checks[].state", enumStrings(t, dimensionState), readinessStateVocabulary())

	kind, _ := itemProps["check"].(map[string]any)
	assertSameSet(t, "check", enumStrings(t, kind), readinessCheckVocabulary())

	code, _ := itemProps["code"].(map[string]any)
	assertSameSet(t, "code", enumStrings(t, code), readinessCodeVocabulary())

	remediation, _ := itemProps["remediation"].(map[string]any)
	assertSameSet(t, "remediation", enumStrings(t, remediation), readinessRemediationVocabulary())

	remaining, _ := props["remaining_checks"].(map[string]any)
	remainingItems, _ := remaining["items"].(map[string]any)
	assertSameSet(t, "remaining_checks", enumStrings(t, remainingItems), launchRemainingChecks())

	capabilities, _ := props["transport_capabilities"].(map[string]any)
	capProps, _ := capabilities["properties"].(map[string]any)
	protocol, _ := capProps["protocol"].(map[string]any)
	assertSameSet(t, "protocol", enumStrings(t, protocol), []string{
		protocolClaudeStreamJSON, protocolClaudeRemoteControl, protocolCodexAppServer,
		protocolGrokACP, protocolOpenCodeACP, protocolUnknown,
	})
	io, _ := capProps["io"].(map[string]any)
	assertSameSet(t, "io", enumStrings(t, io), []string{ioBidirectional, ioLifecycleOnly, ioUnknown})
	input, _ := capProps["input"].(map[string]any)
	assertSameSet(t, "input", enumStrings(t, input), []string{inputLine, inputText, inputUnavailable, inputUnknown})
}

// TestLaunchReadinessPublishesTheNarrowContract pins the three places where the
// published schema was WIDER than the document this server can produce. A
// contract that allows a state, a field or a value the API never emits is a
// contract that makes a client handle cases that cannot happen — and, worse,
// makes the ones that can happen indistinguishable from invention.
func TestLaunchReadinessPublishesTheNarrowContract(t *testing.T) {
	t.Parallel()
	op := launchReadinessOperation(t)
	schema := successSchema(t, op)
	props, _ := schema["properties"].(map[string]any)

	t.Run("configuration_state excludes not_applicable", func(t *testing.T) {
		got := enumStrings(t, mapAt(t, props, "configuration_state"))
		for _, v := range got {
			if v == string(ReadinessNotApplicable) {
				t.Fatalf("the aggregate publishes %q; not_applicable is a statement about ONE "+
					"requirement not applying, and there is no whole configuration that does "+
					"not apply", v)
			}
		}
		assertSameSet(t, "configuration_state", got, readinessAggregateStateVocabulary())
	})

	t.Run("launch_authorization names exactly the POST permission", func(t *testing.T) {
		la := mapAt(t, props, "launch_authorization")
		laProps, _ := la["properties"].(map[string]any)
		perm, ok := laProps["required_permission"].(map[string]any)
		if !ok {
			t.Fatal("launch_authorization does not publish required_permission")
		}
		if got := enumStrings(t, perm); len(got) != 1 || got[0] != string(permRunWrite) {
			t.Fatalf("required_permission enum = %v, want exactly [%s]", got, permRunWrite)
		}
		req := requiredFields(t, la)
		if !req["required_permission"] {
			t.Fatal("required_permission is published as optional, though the server always sends it")
		}
		for _, field := range []string{"state", "code"} {
			if !req[field] {
				t.Fatalf("launch_authorization does not require %q", field)
			}
		}
	})

	t.Run("provider_authentication cannot carry a permission", func(t *testing.T) {
		pa := mapAt(t, props, "provider_authentication")
		if pa["additionalProperties"] != false {
			t.Fatal("provider_authentication is published open, so any field is allowed on it")
		}
		paProps, _ := pa["properties"].(map[string]any)
		if _, leaked := paProps["required_permission"]; leaked {
			t.Fatal("provider_authentication publishes required_permission; there is no permission " +
				"that would make a provider authenticated, and the server never sends one there")
		}
	})

	t.Run("the 409 publishes the stable conflict code", func(t *testing.T) {
		responses, _ := op["responses"].(map[string]any)
		conflict, ok := responses["409"].(map[string]any)
		if !ok {
			t.Fatal("no 409 published")
		}
		content, _ := conflict["content"].(map[string]any)
		body, _ := content["application/json"].(map[string]any)
		body, _ = body["schema"].(map[string]any)
		if body == nil {
			t.Fatal("the 409 publishes no schema, so a client cannot know the code exists")
		}
		bodyProps, _ := body["properties"].(map[string]any)
		envelope := mapAt(t, bodyProps, "error")
		envProps, _ := envelope["properties"].(map[string]any)
		if got := enumStrings(t, mapAt(t, envProps, "code")); len(got) != 1 || got[0] != codeProfileChanged {
			t.Fatalf("409 error.code enum = %v, want exactly [%s]", got, codeProfileChanged)
		}
		if !requiredFields(t, envelope)["code"] {
			t.Fatal("409 error.code is optional, so a client would still have to match the prose")
		}
	})
}

// TestLaunchReadinessAggregateNeverReportsNotApplicable is the causal control
// behind the narrowed enum: it is narrow because the code CANNOT produce the
// fifth value, and this proves that over every state combination rather than by
// reading the function.
func TestLaunchReadinessAggregateNeverReportsNotApplicable(t *testing.T) {
	t.Parallel()
	all := []ReadinessState{
		ReadinessReady, ReadinessNotConfigured, ReadinessUnsupported,
		ReadinessUnknown, ReadinessNotApplicable,
	}
	allowed := map[string]bool{}
	for _, v := range readinessAggregateStateVocabulary() {
		allowed[v] = true
	}
	// Every ordered pair and every triple of the five dimension states.
	for _, a := range all {
		for _, b := range all {
			for _, c := range all {
				got := aggregateReadiness([]LaunchReadinessCheck{
					{Check: CheckProfile, State: a},
					{Check: CheckDriver, State: b},
					{Check: CheckHomes, State: c},
				})
				if !allowed[string(got)] {
					t.Fatalf("aggregate(%s,%s,%s) = %q, which the published enum does not allow",
						a, b, c, got)
				}
			}
		}
	}
	// And the all-not_applicable panel is ready, not not_applicable: a
	// configuration with no applicable requirement left has nothing outstanding.
	if got := aggregateReadiness([]LaunchReadinessCheck{
		{State: ReadinessNotApplicable}, {State: ReadinessNotApplicable},
	}); got != ReadinessReady {
		t.Fatalf("all-not_applicable aggregate = %q, want ready", got)
	}
}

// TestLaunchReadinessConflictErrorKeepsItsStatusForEverySerializer is the causal
// control behind wrapping instead of replacing. The coded writer must produce
// the identifier; the SHARED writer, which has never heard of this type, must
// still produce 409 rather than degrade a conflict into a 500.
func TestLaunchReadinessConflictErrorKeepsItsStatusForEverySerializer(t *testing.T) {
	t.Parallel()
	decode := func(rec *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %q: %v", rec.Body.String(), err)
		}
		envelope, _ := body["error"].(map[string]any)
		return envelope
	}

	coded := httptest.NewRecorder()
	writeReadinessErr(coded, errReadinessProfileChanged)
	if coded.Code != http.StatusConflict {
		t.Fatalf("coded writer status = %d, want 409", coded.Code)
	}
	if got, _ := decode(coded)["code"].(string); got != codeProfileChanged {
		t.Fatalf("coded writer error.code = %q, want %q", got, codeProfileChanged)
	}

	shared := httptest.NewRecorder()
	writeRunErr(shared, errReadinessProfileChanged)
	if shared.Code != http.StatusConflict {
		t.Fatalf("the shared writer answered %d for a wrapped conflict; wrapping exists so a "+
			"serializer that does not know this type still answers the right status", shared.Code)
	}
	// It legitimately does NOT carry the code — that is the scoping, not a defect:
	// the shared envelope is unchanged for every other route.
	if _, carries := decode(shared)["code"]; carries {
		t.Fatal("the shared writer grew a code field; this correction must not re-shape every run error")
	}

	// And a plain runErr is untouched by the new writer.
	plain := httptest.NewRecorder()
	writeReadinessErr(plain, badRequest("nope"))
	if plain.Code != http.StatusBadRequest {
		t.Fatalf("plain runErr through the readiness writer = %d, want 400", plain.Code)
	}
	if _, carries := decode(plain)["code"]; carries {
		t.Fatal("an uncoded error gained a code")
	}
}

func mapAt(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	out, ok := m[key].(map[string]any)
	if !ok {
		t.Fatalf("no object at %q: %+v", key, m[key])
	}
	return out
}

func requiredFields(t *testing.T, schema map[string]any) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	raw, ok := schema["required"].([]any)
	if !ok {
		return out
	}
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out[s] = true
		}
	}
	return out
}

// TestLaunchReadinessVocabularyCoversEveryDeclaredConstant closes the last hole
// the check above cannot: it compares the vocabulary FUNCTIONS against the
// constants declared in launch_readiness.go, by parsing the file. Without this,
// a cause added tomorrow could be emitted by the code, be absent from both the
// vocabulary and the published enum, and every test would still pass.
func TestLaunchReadinessVocabularyCoversEveryDeclaredConstant(t *testing.T) {
	t.Parallel()
	declared := declaredReadinessConstants(t, "launch_readiness.go")
	if len(declared["code"]) == 0 || len(declared["remediation"]) == 0 {
		t.Fatalf("the source scan found no constants; it is not measuring anything: %v", declared)
	}
	assertSameSet(t, "declared cause constants", declared["code"], readinessCodeVocabulary())
	assertSameSet(t, "declared remedy constants", declared["remediation"], readinessRemediationVocabulary())
}

// declaredReadinessConstants returns the VALUES of every `code…` and
// `remediation…` string constant declared in the given file of this package.
// The two statement codes are excluded by name: they belong to the pending
// statements, not to the check vocabulary.
func declaredReadinessConstants(t *testing.T, file string) map[string][]string {
	t.Helper()
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	// Three closed codes that are NOT check causes and therefore not in the check
	// vocabulary: two belong to the pending statements, and profile_changed
	// belongs to this route's 409 error envelope, which has its own published
	// enum (TestLaunchReadinessPublishesTheNarrowContract asserts it).
	excluded := map[string]bool{
		codeNotObservedForThisLaunch: true,
		codeEvaluatedOnSubmit:        true,
		codeProfileChanged:           true,
	}
	out := map[string][]string{}
	for _, decl := range parsed.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range value.Names {
				var group string
				switch {
				case strings.HasPrefix(name.Name, "code"):
					group = "code"
				case strings.HasPrefix(name.Name, "remediation"):
					group = "remediation"
				default:
					continue
				}
				if i >= len(value.Values) {
					continue
				}
				lit, ok := value.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				text, err := strconv.Unquote(lit.Value)
				if err != nil || excluded[text] {
					continue
				}
				out[group] = append(out[group], text)
			}
		}
	}
	return out
}

// TestLaunchReadinessWithoutAStoreCertifiesNothing is the 503 row: a module
// with no data handle answers with an ERROR, not with a document. A DTO here
// would certify requirements nobody looked at, which is the same defect as
// "Launchable" one layer down.
func TestLaunchReadinessWithoutAStoreCertifiesNothing(t *testing.T) {
	t.Parallel()
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(NewProcRunner()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	doc, err := m.EvaluateLaunchReadiness(context.Background(), model.TenantID(uuid.NewString()),
		"ppf_"+string(model.NewID()), LaunchReadinessSelection{
			Transport: string(TransportStreamJSON), Isolation: string(IsolationNative),
		})
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusServiceUnavailable {
		t.Fatalf("err = %v, want a 503 runErr", err)
	}
	if !reflect.DeepEqual(doc, SessionLaunchReadiness{}) {
		t.Fatalf("a document was produced without a store: %+v", doc)
	}
}

func successSchema(t *testing.T, op map[string]any) map[string]any {
	t.Helper()
	responses, _ := op["responses"].(map[string]any)
	ok, _ := responses["200"].(map[string]any)
	content, _ := ok["content"].(map[string]any)
	json, _ := content["application/json"].(map[string]any)
	schema, _ := json["schema"].(map[string]any)
	if schema == nil {
		t.Fatalf("the 200 publishes no schema: %+v", ok)
	}
	return schema
}

func enumStrings(t *testing.T, schema map[string]any) []string {
	t.Helper()
	if schema == nil {
		t.Fatal("no schema to read an enum from")
	}
	raw, ok := schema["enum"].([]any)
	if !ok {
		t.Fatalf("schema publishes no enum: %+v", schema)
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("non-string enum member %v", v)
		}
		out = append(out, s)
	}
	return out
}

// assertSameSet compares two closed vocabularies in BOTH directions and names
// which side is short, because the two failures mean opposite things: a value
// the code can produce and the contract omits is a client that will meet an
// unknown value, and a value the contract declares and the code cannot produce
// is a client written to handle a case that never arrives.
func assertSameSet(t *testing.T, what string, declared, emitted []string) {
	t.Helper()
	if undeclared := difference(emitted, declared); len(undeclared) != 0 {
		t.Errorf("%s: %v is emitted and NOT declared", what, undeclared)
	}
	if unreachable := difference(declared, emitted); len(unreachable) != 0 {
		t.Errorf("%s: %v is declared and NOT emitted", what, unreachable)
	}
}

func difference(a, b []string) []string {
	have := map[string]bool{}
	for _, s := range b {
		have[s] = true
	}
	var out []string
	for _, s := range a {
		if !have[s] {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
