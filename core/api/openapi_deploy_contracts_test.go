// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestDeployMutationRequestBodyCensus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method       string
		pattern      string
		kind         deployRequestBodyKind
		bodyRequired bool
		fields       []string
		required     []string
	}{
		{
			method: http.MethodPost, pattern: "/definitions", kind: deployBodyful,
			bodyRequired: true,
			fields:       []string{"environment", "name", "runtime", "source_ref", "spec", "subject_kind", "subject_ref", "target"},
			required:     []string{"environment", "name", "spec", "subject_kind", "subject_ref", "target"},
		},
		{
			method: http.MethodPut, pattern: "/definitions/{id}", kind: deployBodyful,
			bodyRequired: true,
			fields:       []string{"note", "source_ref", "spec", "target"},
			required:     []string{"spec"},
		},
		{method: http.MethodDelete, pattern: "/definitions/{id}", kind: deployBodyless},
		{
			method: http.MethodPost, pattern: "/definitions/{id}/rollback", kind: deployBodyful,
			bodyRequired: true,
			fields:       []string{"note", "to_version"},
			required:     []string{"to_version"},
		},
		{method: http.MethodPost, pattern: "/definitions/{id}/plan", kind: deployBodyless},
		{method: http.MethodPost, pattern: "/definitions/{id}/verify", kind: deployBodyless},
		{
			method: http.MethodPost, pattern: "/definitions/{id}/apply", kind: deployBodyful,
			fields: []string{"approval_ref"},
		},
		{
			method: http.MethodPost, pattern: "/definitions/{id}/retire", kind: deployBodyful,
			fields: []string{"approval_ref"},
		},
	}

	counts := map[deployRequestBodyKind]int{
		deployBodyful:         0,
		deployBodyless:        0,
		deployBodyNoDerivable: 0,
		deployBodyPending:     0,
	}
	for _, test := range tests {
		test := test
		t.Run(test.method+" "+test.pattern, func(t *testing.T) {
			t.Parallel()
			route := moduleRoute{ns: "deploy", method: test.method, pattern: test.pattern}
			decl, ok := deployRequestBodyDeclarationFor(route)
			if !ok {
				t.Fatal("mutation is absent from Deploy request-body census")
			}
			if decl.kind != test.kind {
				t.Fatalf("kind = %v, want %v", decl.kind, test.kind)
			}
			body, found := deployRequestBody(route)
			if test.kind != deployBodyful {
				if found || body != nil {
					t.Fatalf("non-bodyful mutation produced requestBody %#v", body)
				}
				return
			}
			if !found {
				t.Fatal("bodyful mutation has no requestBody")
			}
			if body["required"] != test.bodyRequired {
				t.Fatalf("requestBody.required = %#v, want %v", body["required"], test.bodyRequired)
			}
			schema := deployObjectSchema(t, deployBodySchema(t, body))
			properties := deployMustMap(t, schema["properties"], "schema.properties")
			if got := deploySortedMapKeys(properties); !reflect.DeepEqual(got, test.fields) {
				t.Fatalf("property names = %v, want %v", got, test.fields)
			}
			if got := deploySortedStrings(schema["required"]); !reflect.DeepEqual(got, test.required) {
				t.Fatalf("required = %v, want %v", got, test.required)
			}
		})
		counts[test.kind]++
	}

	wantCounts := map[deployRequestBodyKind]int{
		deployBodyful:         5,
		deployBodyless:        3,
		deployBodyNoDerivable: 0,
		deployBodyPending:     0,
	}
	if !reflect.DeepEqual(counts, wantCounts) {
		t.Fatalf("Deploy mutation census = %#v, want %#v", counts, wantCounts)
	}
}

func TestDeployDefinitionSpecValidationShape(t *testing.T) {
	t.Parallel()

	create := deployCreateDefinitionSchema()
	createProperties := deployMustMap(t, create["properties"], "create.properties")
	subjectKind := deployMustMap(t, createProperties["subject_kind"], "subject_kind")
	if _, narrowed := subjectKind["enum"]; narrowed {
		t.Fatal("subject_kind must not exclude trim/case variants accepted by the handler")
	}
	if description, _ := subjectKind["description"].(string); !strings.Contains(description, "agent") || !strings.Contains(description, "mcp_server") {
		t.Fatalf("subject_kind normalization is not documented: %q", description)
	}

	spec := deployObjectSchema(t, deployMustMap(t, createProperties["spec"], "create.spec"))
	if got := deploySortedMapKeys(deployMustMap(t, spec["properties"], "spec.properties")); !reflect.DeepEqual(got, []string{"command", "env_refs", "identity", "image", "replicas", "resources", "wirings"}) {
		t.Fatalf("spec property names = %v", got)
	}
	specProperties := deployMustMap(t, spec["properties"], "spec.properties")
	replicas := deployNonNullBranch(t, deployMustMap(t, specProperties["replicas"], "spec.replicas"))
	if replicas["minimum"] != 0 || replicas["maximum"] != 10000 {
		t.Fatalf("replicas bounds = %#v", replicas)
	}
	envRefs := deployNonNullBranch(t, deployMustMap(t, specProperties["env_refs"], "spec.env_refs"))
	if envRefs["maxItems"] != 200 {
		t.Fatalf("env_refs maxItems = %#v, want 200", envRefs["maxItems"])
	}
	wirings := deployNonNullBranch(t, deployMustMap(t, specProperties["wirings"], "spec.wirings"))
	if wirings["maxItems"] != 200 {
		t.Fatalf("wirings maxItems = %#v, want 200", wirings["maxItems"])
	}
	wiring := deployMustMap(t, wirings["items"], "wirings.items")
	if got := deploySortedStrings(wiring["required"]); !reflect.DeepEqual(got,
		[]string{"mode", "resource_kind", "resource_ref"}) {
		t.Fatalf("wiring required = %v", got)
	}
	wiringProperties := deployMustMap(t, wiring["properties"], "wiring.properties")
	mode := deployMustMap(t, wiringProperties["mode"], "wiring.mode")
	if _, narrowed := mode["enum"]; narrowed {
		t.Fatal("wiring mode must not exclude trim/case variants accepted by the handler")
	}
	if description, _ := mode["description"].(string); !strings.Contains(description, "read") || !strings.Contains(description, "write") {
		t.Fatalf("wiring mode normalization is not documented: %q", description)
	}
}

func TestDeployNullAcceptanceMirrorsJSONDecoder(t *testing.T) {
	t.Parallel()

	create := deployCreateDefinitionSchema()
	createProperties := deployMustMap(t, create["properties"], "create.properties")
	for _, field := range []string{"runtime", "source_ref", "spec"} {
		deployRequireNullBranch(t, deployMustMap(t, createProperties[field], "create."+field), "create."+field)
	}

	update := deployUpdateDefinitionSchema()
	updateProperties := deployMustMap(t, update["properties"], "update.properties")
	for _, field := range []string{"target", "source_ref", "note", "spec"} {
		deployRequireNullBranch(t, deployMustMap(t, updateProperties[field], "update."+field), "update."+field)
	}

	spec := deployObjectSchema(t, deployMustMap(t, createProperties["spec"], "create.spec"))
	specProperties := deployMustMap(t, spec["properties"], "spec.properties")
	for _, field := range []string{"image", "command", "replicas", "resources", "env_refs", "wirings", "identity"} {
		deployRequireNullBranch(t, deployMustMap(t, specProperties[field], "spec."+field), "spec."+field)
	}

	resources := deployObjectSchema(t, deployMustMap(t, specProperties["resources"], "spec.resources"))
	deployRequireNullBranch(t, deployMustMap(t, resources["additionalProperties"], "resources.values"), "resources.values")

	envRefs := deployNonNullBranch(t, deployMustMap(t, specProperties["env_refs"], "spec.env_refs"))
	envRef := deployMustMap(t, envRefs["items"], "env_refs.items")
	envProperties := deployMustMap(t, envRef["properties"], "env_ref.properties")
	deployRequireNullBranch(t, deployMustMap(t, envProperties["secret_ref"], "env_ref.secret_ref"), "env_ref.secret_ref")

	wirings := deployNonNullBranch(t, deployMustMap(t, specProperties["wirings"], "spec.wirings"))
	wiring := deployMustMap(t, wirings["items"], "wirings.items")
	wiringProperties := deployMustMap(t, wiring["properties"], "wiring.properties")
	deployRequireNullBranch(t, deployMustMap(t, wiringProperties["secret_ref"], "wiring.secret_ref"), "wiring.secret_ref")

	identity := deployObjectSchema(t, deployMustMap(t, specProperties["identity"], "spec.identity"))
	identityProperties := deployMustMap(t, identity["properties"], "identity.properties")
	for _, field := range []string{"identity_ref", "mint"} {
		deployRequireNullBranch(t, deployMustMap(t, identityProperties[field], "identity."+field), "identity."+field)
	}

	rollbackProperties := deployMustMap(t, deployRollbackSchema()["properties"], "rollback.properties")
	deployRequireNullBranch(t, deployMustMap(t, rollbackProperties["note"], "rollback.note"), "rollback.note")

	approval := deployObjectSchema(t, deployApprovalMutationSchema())
	approvalProperties := deployMustMap(t, approval["properties"], "approval.properties")
	deployRequireNullBranch(t, deployMustMap(t, approvalProperties["approval_ref"], "approval_ref"), "approval_ref")
}

func TestDeployOptionalApprovalBodyShape(t *testing.T) {
	t.Parallel()

	for _, pattern := range []string{"/definitions/{id}/apply", "/definitions/{id}/retire"} {
		body, ok := deployRequestBody(moduleRoute{ns: "deploy", method: http.MethodPost, pattern: pattern})
		if !ok {
			t.Fatalf("POST %s requestBody not found", pattern)
		}
		if body["required"] != false {
			t.Fatalf("POST %s requestBody.required = %#v, want false", pattern, body["required"])
		}
		schema := deployBodySchema(t, body)
		branches, ok := schema["anyOf"].([]any)
		if !ok || len(branches) != 2 {
			t.Fatalf("POST %s schema = %#v, want object|null", pattern, schema)
		}
		foundNull := false
		for _, branch := range branches {
			candidate, ok := branch.(map[string]any)
			if ok && candidate["type"] == "null" {
				foundNull = true
			}
		}
		if !foundNull {
			t.Fatalf("POST %s schema lacks the decoded null-body branch", pattern)
		}
	}
}

func TestDeployRequestBodyRegistryIsScopedAndFresh(t *testing.T) {
	t.Parallel()

	known := moduleRoute{ns: "deploy", method: http.MethodPost, pattern: "/definitions"}
	first, ok := deployRequestBody(known)
	if !ok {
		t.Fatal("known Deploy request body not found")
	}
	firstSchema := deployObjectSchema(t, deployBodySchema(t, first))
	firstProperties := deployMustMap(t, firstSchema["properties"], "properties")
	firstProperties["not_a_real_field"] = oaObj("type", "string")
	second, ok := deployRequestBody(known)
	if !ok {
		t.Fatal("known Deploy request body disappeared")
	}
	secondSchema := deployObjectSchema(t, deployBodySchema(t, second))
	secondProperties := deployMustMap(t, secondSchema["properties"], "properties")
	if _, leaked := secondProperties["not_a_real_field"]; leaked {
		t.Fatal("request schema builders share mutable property maps")
	}

	for _, route := range []moduleRoute{
		{ns: "catalog", method: http.MethodPost, pattern: "/definitions"},
		{ns: "deploy", method: http.MethodGet, pattern: "/definitions"},
		{ns: "deploy", method: http.MethodPost, pattern: "/unknown"},
	} {
		if decl, found := deployRequestBodyDeclarationFor(route); found {
			t.Fatalf("unexpected Deploy declaration for %#v: %#v", route, decl)
		}
		if body, found := deployRequestBody(route); found || body != nil {
			t.Fatalf("unexpected requestBody for %#v: found=%v body=%#v", route, found, body)
		}
	}
}

func deployBodySchema(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	content := deployMustMap(t, body["content"], "requestBody.content")
	media := deployMustMap(t, content["application/json"], "application/json")
	return deployMustMap(t, media["schema"], "application/json.schema")
}

func deployObjectSchema(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	if schema["type"] == "object" {
		return schema
	}
	branches, ok := schema["anyOf"].([]any)
	if !ok {
		t.Fatalf("schema = %#v, want object or object|null", schema)
	}
	for _, branch := range branches {
		candidate, ok := branch.(map[string]any)
		if ok && candidate["type"] == "object" {
			return candidate
		}
	}
	t.Fatalf("schema = %#v, object branch not found", schema)
	return nil
}

func deployNonNullBranch(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	branches, ok := schema["anyOf"].([]any)
	if !ok {
		t.Fatalf("schema = %#v, want nullable schema", schema)
	}
	for _, branch := range branches {
		candidate, ok := branch.(map[string]any)
		if ok && candidate["type"] != "null" {
			return candidate
		}
	}
	t.Fatalf("schema = %#v, non-null branch not found", schema)
	return nil
}

func deployRequireNullBranch(t *testing.T, schema map[string]any, label string) {
	t.Helper()
	branches, ok := schema["anyOf"].([]any)
	if !ok {
		t.Fatalf("%s = %#v, want nullable schema", label, schema)
	}
	for _, branch := range branches {
		candidate, ok := branch.(map[string]any)
		if ok && candidate["type"] == "null" {
			return
		}
	}
	t.Fatalf("%s = %#v, null branch not found", label, schema)
}

func deployMustMap(t *testing.T, value any, label string) map[string]any {
	t.Helper()
	got, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v, want map[string]any", label, value)
	}
	return got
}

func deploySortedMapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func deploySortedStrings(value any) []string {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if item, ok := value.(string); ok {
			out = append(out, item)
		}
	}
	sort.Strings(out)
	return out
}
