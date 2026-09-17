// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"testing"

	"github.com/olivaresai/olivares/core/api"
)

// The PUBLISHED contract of the HC1 host-tools read, checked against the code that
// serves it.
//
// core/api may not import this module, so its published enums are a RESTATEMENT of
// the vocabulary in host_tools.go. A restatement drifts unless something compares
// the two, and that is this file's whole job: every assertion below reads the REAL
// registered beta document on one side and a REAL handler response or the REAL DTO
// on the other. Nothing here mirrors the implementation — a test that re-derived the
// enum from the same constants it is checking would pass on any drift.
//
// The names live in the Launch family on purpose: these tests share the readiness
// fixture and the same registered surface, and that family already has a shard.

const hostToolsSpecPath = "/v1/m/sessions/provider-profiles/{ref}/host-tools"

func hostToolsOperation(t *testing.T) map[string]any {
	t.Helper()
	doc := api.ModuleOpenAPIDocument([]api.Module{New()})
	paths, _ := doc["paths"].(map[string]any)
	item, ok := paths[hostToolsSpecPath].(map[string]any)
	if !ok {
		t.Fatalf("the beta document does not publish %s", hostToolsSpecPath)
	}
	op, ok := item["get"].(map[string]any)
	if !ok {
		t.Fatalf("no GET operation at %s: %+v", hostToolsSpecPath, item)
	}
	return op
}

func hostToolsResponseSchema(t *testing.T) map[string]any {
	t.Helper()
	op := hostToolsOperation(t)
	responses, _ := op["responses"].(map[string]any)
	ok200, _ := responses["200"].(map[string]any)
	content, _ := ok200["content"].(map[string]any)
	js, _ := content["application/json"].(map[string]any)
	schema, _ := js["schema"].(map[string]any)
	if schema == nil {
		t.Fatalf("the 200 publishes no schema: %+v", ok200)
	}
	if schema["type"] != "object" || schema["properties"] == nil {
		t.Fatalf("the 200 is still the generic envelope, not a typed object: %+v", schema)
	}
	return schema
}

func schemaRequired(t *testing.T, schema map[string]any) []string {
	t.Helper()
	raw, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("schema declares no required set: %+v", schema)
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, _ := v.(string)
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// TestLaunchHostToolsPublishedKeysAreTheRealResponseKeys proves the published
// required set is exactly the set of keys a REAL 200 carries.
//
// Both directions matter and both are checked. A published key the handler never
// emits makes a generated client's non-optional field nil at runtime; a key the
// handler emits and the document omits makes the console re-derive it from prose.
// The response here is produced by the real handler through the real fixture, not
// by marshalling a struct literal.
func TestLaunchHostToolsPublishedKeysAreTheRealResponseKeys(t *testing.T) {
	t.Parallel()
	f, _, _ := newHostToolsFixture(t, sqliteReadinessEngine(t), nil)
	cfgHome, userHome := readinessHomes(t)
	ref := f.createProfile("claude", cfgHome, userHome, AuthSourceAccountHome)
	_, r := f.hostTools(f.viewer, f.tenant, ref, "")
	if r.code != http.StatusOK {
		t.Fatalf("host-tools = %d %s", r.code, r.raw)
	}
	var live map[string]json.RawMessage
	if err := json.Unmarshal([]byte(r.raw), &live); err != nil {
		t.Fatalf("decode the real response: %v (%s)", err, r.raw)
	}
	got := make([]string, 0, len(live))
	for k := range live {
		got = append(got, k)
	}
	sort.Strings(got)

	schema := hostToolsResponseSchema(t)
	want := schemaRequired(t, schema)
	if !slices.Equal(got, want) {
		t.Fatalf("the real response carries %v, the document requires %v", got, want)
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("the 200 is not a closed object: additionalProperties = %v", schema["additionalProperties"])
	}
	props, _ := schema["properties"].(map[string]any)
	published := make([]string, 0, len(props))
	for k := range props {
		published = append(published, k)
	}
	sort.Strings(published)
	if !slices.Equal(published, want) {
		t.Fatalf("the document publishes properties %v but requires %v: every field of this read is required", published, want)
	}
}

// TestLaunchHostToolsPublishedStateCodesAreClosed proves the published state enum is
// the vocabulary the module can actually answer, read from the module's own
// constants rather than retyped here.
func TestLaunchHostToolsPublishedStateCodesAreClosed(t *testing.T) {
	t.Parallel()
	schema := hostToolsResponseSchema(t)
	props, _ := schema["properties"].(map[string]any)
	state, _ := props["state"].(map[string]any)
	got := enumStrings(t, state)
	sort.Strings(got)

	want := []string{
		HostToolsObserved, HostToolsNoneObserved, HostToolsUnsupportedDriver,
		HostToolsUnknown, HostToolsNotChecked,
	}
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Fatalf("published state enum %v, module states %v: core/api restates this vocabulary and it has drifted", got, want)
	}
}

// TestLaunchHostToolsPublishedGroupCodesAreClosed proves the three closed group
// dimensions and the counting floor reach the document, so a generated client
// cannot accept an origin or match code this read never emits.
func TestLaunchHostToolsPublishedGroupCodesAreClosed(t *testing.T) {
	t.Parallel()
	schema := hostToolsResponseSchema(t)
	props, _ := schema["properties"].(map[string]any)
	groups, _ := props["groups"].(map[string]any)
	if groups["type"] != "array" {
		t.Fatalf("groups is not an array: %+v", groups)
	}
	item, _ := groups["items"].(map[string]any)
	if item["additionalProperties"] != false {
		t.Fatalf("a group is not a closed object: %+v", item)
	}
	if !slices.Equal(schemaRequired(t, item), []string{"configured", "count", "executable", "match", "origin"}) {
		t.Fatalf("a group does not require every dimension: %v", schemaRequired(t, item))
	}
	gp, _ := item["properties"].(map[string]any)

	// Built from the MODULE's own constants, so this compares core/api's restatement
	// against the vocabulary the handler actually emits instead of against a retyped copy.
	for name, want := range map[string][]string{
		"origin": sortedCopy([]string{HostToolOriginManaged, HostToolOriginVendorDefault,
			HostToolOriginPath, HostToolOriginNamed, HostToolCodeUnknown}),
		"match": sortedCopy([]string{HostToolMatchRegistered, HostToolMatchManifestCorroborated,
			HostToolMatchUnregisteredObserved, HostToolMatchUnverified, HostToolMatchDamaged,
			HostToolCodeUnknown}),
		"configured": sortedCopy([]string{HostToolConfiguredSame, HostToolConfiguredDifferent,
			HostToolCodeUnknown}),
	} {
		field, _ := gp[name].(map[string]any)
		got := enumStrings(t, field)
		sort.Strings(got)
		if !slices.Equal(got, want) {
			t.Fatalf("published %s enum %v, want %v", name, got, want)
		}
	}
	if executable, _ := gp["executable"].(map[string]any); executable["type"] != "boolean" {
		t.Fatalf("executable is not a boolean: %+v", gp["executable"])
	}
	count, _ := gp["count"].(map[string]any)
	if count["type"] != "integer" {
		t.Fatalf("count is not an integer: %+v", count)
	}
	if min, ok := count["minimum"]; !ok || min != 1 {
		t.Fatalf("count publishes minimum %v, want 1: a group is only published when it counted a candidate", min)
	}
}

// TestLaunchHostToolsPublishesNoQueryAndKeepsTheEmptyEvaluatedIdentity proves the
// two properties the contract is easiest to get wrong.
//
// The route declares NO query parameter and answers 400 to a non-empty query, so a
// published parameter would invite a client to send one. And the evaluated identity
// is ALWAYS present, empty when this node has none: publishing it as optional would
// let a client treat the empty answer as a missing field and guess.
func TestLaunchHostToolsPublishesNoQueryAndKeepsTheEmptyEvaluatedIdentity(t *testing.T) {
	t.Parallel()
	op := hostToolsOperation(t)
	for _, raw := range func() []any { p, _ := op["parameters"].([]any); return p }() {
		p, _ := raw.(map[string]any)
		if p["in"] == "query" {
			t.Fatalf("the document publishes a query parameter %v; this route has none and answers 400 to a non-empty query", p["name"])
		}
	}
	if got := op["x-required-permission"]; got != string(permProfileRead) {
		t.Fatalf("x-required-permission = %v, want %s", got, permProfileRead)
	}

	f, _, _ := newHostToolsFixture(t, sqliteReadinessEngine(t), nil)
	cfgHome, userHome := readinessHomes(t)
	ref := f.createProfile("claude", cfgHome, userHome, AuthSourceAccountHome)
	_, bad := f.hostTools(f.viewer, f.tenant, ref, "anything=1")
	if bad.code != http.StatusBadRequest {
		t.Fatalf("a non-empty query answered %d, want 400", bad.code)
	}
	f.m.UseExecutionEnvironmentRef("")
	out, r := f.hostTools(f.viewer, f.tenant, ref, "")
	if r.code != http.StatusOK {
		t.Fatalf("host-tools = %d %s", r.code, r.raw)
	}
	var live map[string]json.RawMessage
	if err := json.Unmarshal([]byte(r.raw), &live); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, present := live["evaluated_environment_ref"]; !present {
		t.Fatalf("the real response omits evaluated_environment_ref; the contract publishes it as always present: %s", r.raw)
	}
	if out.EvaluatedEnvironmentRef != "" || out.State != HostToolsNotChecked {
		t.Fatalf("unbound node answered environment=%q state=%q", out.EvaluatedEnvironmentRef, out.State)
	}
	if out.State != HostToolsObserved && len(out.Groups) != 0 {
		t.Fatalf("state %q carries %d groups; only observed does", out.State, len(out.Groups))
	}
}

// TestLaunchHostToolsReusesTheTypedProfileChangedConflict proves the 409 body is the
// readiness sibling's ratified envelope with the SAME single-value code, so this
// route introduced no second conflict identifier.
func TestLaunchHostToolsReusesTheTypedProfileChangedConflict(t *testing.T) {
	t.Parallel()
	op := hostToolsOperation(t)
	responses, _ := op["responses"].(map[string]any)
	conflict, _ := responses["409"].(map[string]any)
	content, _ := conflict["content"].(map[string]any)
	js, _ := content["application/json"].(map[string]any)
	schema, _ := js["schema"].(map[string]any)
	if schema == nil {
		t.Fatalf("the 409 publishes no typed body: %+v", conflict)
	}
	props, _ := schema["properties"].(map[string]any)
	errObj, _ := props["error"].(map[string]any)
	errProps, _ := errObj["properties"].(map[string]any)
	code, _ := errProps["code"].(map[string]any)
	if got := enumStrings(t, code); !slices.Equal(got, []string{codeProfileChanged}) {
		t.Fatalf("the 409 code enum is %v, want exactly [%s]", got, codeProfileChanged)
	}
	for _, status := range []string{"200", "400", "401", "403", "404", "409", "429", "503"} {
		if _, ok := responses[status]; !ok {
			t.Fatalf("the document omits the %s this read can answer", status)
		}
	}
}

// sortedCopy keeps the vocabulary comparisons order-independent without mutating the
// module's own declaration order.
func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
