// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"reflect"
	"sort"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/modules/internal/oastest"
)

func TestSessionsWorkOpenAPIIsInvocableAndClassifiesWorkStream(t *testing.T) {
	doc := api.ModuleOpenAPIDocument([]api.Module{New()})
	paths := doc["paths"].(map[string]any)

	create := oastest.PathOp(t, paths, "post", "/v1/m/sessions/work-items")
	for _, want := range []struct{ name, in string }{
		{name: "mode", in: "query"}, {name: "Idempotency-Key", in: "header"},
		{name: "If-Match", in: "header"}, {name: "If-Plan-Hash", in: "header"},
	} {
		if !hasParameter(create, want.name, want.in) {
			t.Errorf("work mutation missing %s %s parameter", want.in, want.name)
		}
	}
	if _, ok := create["requestBody"].(map[string]any); !ok {
		t.Error("work mutation has no JSON WorkCommand requestBody")
	}

	items := oastest.PathOp(t, paths, "get", "/v1/m/sessions/work-items")
	for _, name := range []string{"limit", "cursor", "status", "archived", "updated_after"} {
		if !hasParameter(items, name, "query") {
			t.Errorf("work item list missing query parameter %s", name)
		}
	}
	decisions := oastest.PathOp(t, paths, "get", "/v1/m/sessions/decisions")
	for _, name := range []string{"work_item_id", "effective", "revoked"} {
		if !hasParameter(decisions, name, "query") {
			t.Errorf("decision list missing query parameter %s", name)
		}
	}
	leases := oastest.PathOp(t, paths, "get", "/v1/m/sessions/leases")
	for _, name := range []string{"limit", "cursor", "work_item_id", "holder_sid", "state", "expires_before"} {
		if !hasParameter(leases, name, "query") {
			t.Errorf("lease list missing query parameter %s", name)
		}
	}
	if leases["x-required-permission"] != "sessions:lease:read" {
		t.Errorf("lease list permission = %v", leases["x-required-permission"])
	}

	lease := oastest.PathOp(t, paths, "get", "/v1/m/sessions/work-items/{id}/lease")
	if !hasPathParam(lease, "id") || lease["x-required-permission"] != "sessions:lease:read" {
		t.Errorf("nested lease get contract = %#v", lease)
	}
	for _, tc := range []struct {
		path, permission string
	}{
		{path: "/v1/m/sessions/work-items/{id}/lease/acquire", permission: "sessions:lease:write"},
		{path: "/v1/m/sessions/work-items/{id}/lease/renew", permission: "sessions:lease:write"},
		{path: "/v1/m/sessions/work-items/{id}/lease/release", permission: "sessions:lease:write"},
		{path: "/v1/m/sessions/work-items/{id}/lease/takeover", permission: "sessions:lease:admin"},
		{path: "/v1/m/sessions/work-items/{id}/lease/revoke", permission: "sessions:lease:admin"},
		{path: "/v1/m/sessions/work-items/{id}/lease/clock-rebase", permission: "sessions:lease:admin"},
	} {
		op := oastest.PathOp(t, paths, "post", tc.path)
		if op["x-required-permission"] != tc.permission || !hasPathParam(op, "id") ||
			!hasParameter(op, "mode", "query") || !hasParameter(op, "If-Match", "header") {
			t.Errorf("lease mutation %s contract = %#v", tc.path, op)
		}
		if _, ok := op["requestBody"].(map[string]any); !ok {
			t.Errorf("lease mutation %s has no WorkCommand body", tc.path)
		}
	}
	acquire := oastest.PathOp(t, paths, "post", "/v1/m/sessions/work-items/{id}/lease/acquire")
	leaseSchema := requestBodySchema(t, acquire)
	if leaseSchema["additionalProperties"] != false {
		t.Errorf("lease WorkCommand accepts undeclared fields: %#v", leaseSchema)
	}
	leaseFields := requestBodyProperties(t, acquire)
	for name, wantType := range map[string]string{
		"holder_sid": "string", "holder_run_ref": "string", "holder_agent_ref": "string",
		"ttl_seconds": "integer", "fence": "integer", "force": "boolean",
		"unblock": "boolean", "changes_requested": "boolean",
		"reason": "string", "decision_id": "string", "evidence_ref": "string", "plan_hash": "string",
	} {
		field, ok := leaseFields[name].(map[string]any)
		if !ok || field["type"] != wantType {
			t.Errorf("lease WorkCommand field %s = %#v, want type %s", name, leaseFields[name], wantType)
		}
	}

	replay := oastest.PathOp(t, paths, "post", "/v1/m/sessions/work-events/{event_id}/replay")
	if replay["x-required-permission"] != "sessions:work:admin" ||
		!hasPathParam(replay, "event_id") || !hasParameter(replay, "mode", "query") ||
		!hasParameter(replay, "Idempotency-Key", "header") ||
		!hasParameter(replay, "If-Match", "header") ||
		!hasParameter(replay, "If-Plan-Hash", "header") {
		t.Errorf("outbox replay command envelope = %#v", replay)
	}
	replaySchema := requestBodySchema(t, replay)
	if replaySchema["additionalProperties"] != false {
		t.Errorf("outbox replay accepts undeclared fields: %#v", replaySchema)
	}
	replayFields := requestBodyProperties(t, replay)
	if len(replayFields) != 1 || replayFields["plan_hash"] == nil {
		t.Errorf("outbox replay body fields = %#v, want only optional plan_hash", replayFields)
	}

	stream := oastest.PathOp(t, paths, "get", "/v1/m/sessions/work-stream")
	content := stream["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)
	if _, ok := content["text/event-stream"]; !ok {
		t.Fatalf("work-stream 200 content = %v, want text/event-stream", content)
	}
	if !hasParameter(stream, "Last-Event-ID", "header") {
		t.Error("work-stream missing Last-Event-ID resume header")
	}
}

func TestSessionsRuntimeWorkControlOpenAPI(t *testing.T) {
	doc := api.ModuleOpenAPIDocument([]api.Module{New()})
	paths := doc["paths"].(map[string]any)

	input := oastest.PathOp(t, paths, "post", "/v1/m/sessions/runs/{ref}/input")
	inputBody := input["requestBody"].(map[string]any)
	if inputBody["required"] != true || !hasPathParam(input, "ref") {
		t.Fatalf("run input request contract = %#v", input)
	}
	inputSchema := requestBodySchema(t, input)
	if inputSchema["additionalProperties"] != false {
		t.Errorf("run input accepts undeclared fields: %#v", inputSchema)
	}
	inputFields := requestBodyProperties(t, input)
	fence, _ := inputFields["work_lease_fence"].(map[string]any)
	if fence["type"] != "integer" || fence["format"] != "int64" || fence["minimum"] != 1 {
		t.Errorf("run input fence = %#v", fence)
	}

	// The third run control. Its body is OPTIONAL, like stop's and unlike input's:
	// an absent body is the legacy non-work interrupt, which must keep working.
	interrupt := oastest.PathOp(t, paths, "post", "/v1/m/sessions/runs/{ref}/interrupt")
	interruptBody, ok := interrupt["requestBody"].(map[string]any)
	if !ok || interruptBody["required"] != false || !hasPathParam(interrupt, "ref") {
		t.Fatalf("run interrupt request contract = %#v", interrupt)
	}
	interruptSchema := requestBodySchema(t, interrupt)
	if interruptSchema["additionalProperties"] != false {
		t.Errorf("run interrupt accepts undeclared fields: %#v", interruptSchema)
	}
	interruptFields := requestBodyProperties(t, interrupt)
	interruptFence, _ := interruptFields["work_lease_fence"].(map[string]any)
	if interruptFence["type"] != "integer" || interruptFence["format"] != "int64" ||
		interruptFence["minimum"] != 1 {
		t.Errorf("run interrupt fence = %#v", interruptFence)
	}
	// And it carries NOTHING else: a reason belongs to a terminal stop, and a text
	// or line would make the turn-cancelling control a second input door.
	if len(interruptFields) != 1 {
		t.Errorf("run interrupt body fields = %#v, want only work_lease_fence", interruptFields)
	}

	stop := oastest.PathOp(t, paths, "post", "/v1/m/sessions/runs/{ref}/stop")
	stopBody := stop["requestBody"].(map[string]any)
	if stopBody["required"] != false || !hasPathParam(stop, "ref") {
		t.Fatalf("run stop request contract = %#v", stop)
	}
	stopSchema := requestBodySchema(t, stop)
	if stopSchema["additionalProperties"] != false {
		t.Errorf("run stop accepts undeclared fields: %#v", stopSchema)
	}
	stopFields := requestBodyProperties(t, stop)
	stopFence, _ := stopFields["work_lease_fence"].(map[string]any)
	if stopFence["type"] != "integer" || stopFence["format"] != "int64" || stopFence["minimum"] != 1 {
		t.Errorf("run stop fence = %#v", stopFence)
	}
	reason, _ := stopFields["reason"].(map[string]any)
	if reason["type"] != "string" || reason["maxLength"] != 512 {
		t.Errorf("run stop reason = %#v", reason)
	}

	// ⛔ THE RESPONSE HALF. Until an independent review read the published document
	// against the handlers, all three controls advertised success as 200 with an
	// unspecified object and none of them published 503 — while input actually
	// answers 202 and all three can answer 503/UNKNOWN. Both artifacts were
	// drift-clean the whole time, because the generator and the snapshot agreed on
	// the same wrong answer.
	if got := responseStatuses(t, input); !reflect.DeepEqual(got,
		[]string{"202", "400", "401", "403", "404", "409", "429", "503"}) {
		t.Errorf("run input statuses = %v", got)
	}
	accepted := responseSchema(t, input, "202")
	if accepted["additionalProperties"] != false ||
		!reflect.DeepEqual(sortedSchemaStrings(accepted["required"]), []string{"accepted"}) {
		t.Errorf("run input 202 schema = %#v", accepted)
	}
	if props, _ := accepted["properties"].(map[string]any); props["accepted"].(map[string]any)["type"] != "boolean" {
		t.Errorf("run input 202 accepted field = %#v", props)
	}

	for _, op := range []struct {
		name string
		doc  map[string]any
	}{{"interrupt", interrupt}, {"stop", stop}} {
		if got := responseStatuses(t, op.doc); !reflect.DeepEqual(got,
			[]string{"200", "400", "401", "403", "404", "409", "429", "503"}) {
			t.Errorf("run %s statuses = %v", op.name, got)
		}
		resource := responseSchema(t, op.doc, "200")
		// OPEN on purpose: the run resource grows, and a frozen field list here
		// would be a published contract that is wrong the next time it does.
		if resource["additionalProperties"] != true ||
			!reflect.DeepEqual(sortedSchemaStrings(resource["required"]), []string{"run_ref", "state"}) {
			t.Errorf("run %s 200 schema = %#v", op.name, resource)
		}
	}

	for name, op := range map[string]map[string]any{
		"input": input, "interrupt": interrupt, "stop": stop,
	} {
		unknown := responseSchema(t, op, "503")
		if !reflect.DeepEqual(sortedSchemaStrings(unknown["required"]),
			[]string{"code", "error", "verdict"}) {
			t.Errorf("run %s 503 schema = %#v", name, unknown)
		}
		props, _ := unknown["properties"].(map[string]any)
		verdict, _ := props["verdict"].(map[string]any)
		if !reflect.DeepEqual(sortedSchemaStrings(verdict["enum"]), []string{"NO_HE_PODIDO_MIRAR"}) {
			t.Errorf("run %s 503 verdict = %#v", name, verdict)
		}
	}
}

func TestSessionsProtocolBindingOpenAPI(t *testing.T) {
	doc := api.ModuleOpenAPIDocument([]api.Module{New()})
	paths := doc["paths"].(map[string]any)

	list := oastest.PathOp(t, paths, "get", "/v1/m/sessions/protocol-bindings")
	if list["x-required-permission"] != "sessions:protocol-binding:read" {
		t.Errorf("protocol binding list permission = %v", list["x-required-permission"])
	}
	for _, name := range []string{
		"workspace_id", "binding_spec_id", "work_item_id", "protocol", "peer_authority",
		"owner_kind", "owner_ref", "external_kind", "external_id", "verdict", "terminal",
		"limit", "cursor",
	} {
		if !hasParameter(list, name, "query") {
			t.Errorf("protocol binding list missing query parameter %s", name)
		}
	}
	assertParameterEnum(t, list, "protocol", "query", []string{"a2a", "mcp"})
	assertParameterEnum(t, list, "verdict", "query", []string{"CLEAN", "BROKEN", "UNKNOWN"})
	terminalSchema := parameterOf(t, list, "terminal", "query")["schema"].(map[string]any)
	if terminalSchema["type"] != "boolean" {
		t.Errorf("protocol binding terminal filter schema = %#v", terminalSchema)
	}
	assertPageParameterSchemas(t, list)
	get := oastest.PathOp(t, paths, "get", "/v1/m/sessions/protocol-bindings/{id}")
	if get["x-required-permission"] != "sessions:protocol-binding:read" || !hasPathParam(get, "id") {
		t.Errorf("protocol binding get contract = %#v", get)
	}
	assertProtocolBindingIDPathSchema(t, get)

	reconcile := oastest.PathOp(t, paths, "post", "/v1/m/sessions/protocol-bindings/{id}/reconcile")
	if reconcile["x-required-permission"] != "sessions:protocol-binding:write" || !hasPathParam(reconcile, "id") {
		t.Fatalf("protocol binding reconcile contract = %#v", reconcile)
	}
	for _, header := range []string{"Idempotency-Key", "If-Match", "If-Plan-Hash"} {
		if !hasParameter(reconcile, header, "header") {
			t.Errorf("protocol binding reconcile missing header %s", header)
		}
	}
	if schema := parameterOf(t, reconcile, "Idempotency-Key", "header")["schema"].(map[string]any); schema["format"] != "uuid" {
		t.Errorf("protocol binding reconcile idempotency schema = %#v", schema)
	}
	for _, header := range []string{"If-Match", "If-Plan-Hash"} {
		schema := parameterOf(t, reconcile, header, "header")["schema"].(map[string]any)
		if schema["pattern"] == nil {
			t.Errorf("protocol binding reconcile %s has no lexical precondition schema: %#v", header, schema)
		}
	}
	assertParameterEnum(t, reconcile, "mode", "query", []string{"validate", "plan", "test", "apply"})

	body := reconcile["requestBody"].(map[string]any)
	if body["required"] != false {
		t.Errorf("protocol binding reconcile body required = %v, want false", body["required"])
	}
	schema := requestBodySchema(t, reconcile)
	if schema["additionalProperties"] != false {
		t.Errorf("protocol binding reconcile accepts undeclared fields: %#v", schema)
	}
	fields := requestBodyProperties(t, reconcile)
	if len(fields) != 1 || fields["plan_hash"] == nil {
		t.Errorf("protocol binding reconcile body fields = %#v, want only plan_hash", fields)
	}
	responses := reconcile["responses"].(map[string]any)
	for _, status := range []string{"412", "428", "503"} {
		if responses[status] == nil {
			t.Errorf("protocol binding reconcile missing response %s", status)
		}
	}

	specList := oastest.PathOp(t, paths, "get", "/v1/m/sessions/protocol-binding-specs")
	if specList["x-required-permission"] != "sessions:protocol-binding:read" {
		t.Errorf("protocol binding spec list permission = %v", specList["x-required-permission"])
	}
	for _, name := range []string{
		"workspace_id", "binding_key", "generation", "protocol", "direction", "local_kind",
		"peer_authority", "state", "limit", "cursor",
	} {
		if !hasParameter(specList, name, "query") {
			t.Errorf("protocol binding spec list missing query parameter %s", name)
		}
	}
	assertParameterEnum(t, specList, "protocol", "query", []string{"a2a", "mcp"})
	assertParameterEnum(t, specList, "direction", "query", []string{"inbound", "outbound", "bidirectional"})
	assertParameterEnum(t, specList, "local_kind", "query", []string{"work_item", "agent", "model", "channel"})
	assertParameterEnum(t, specList, "state", "query", []string{"draft", "active", "disabled", "superseded"})
	assertPageParameterSchemas(t, specList)
	specGet := oastest.PathOp(t, paths, "get", "/v1/m/sessions/protocol-binding-specs/{id}")
	if specGet["x-required-permission"] != "sessions:protocol-binding:read" || !hasPathParam(specGet, "id") {
		t.Errorf("protocol binding spec get contract = %#v", specGet)
	}
	assertProtocolBindingIDPathSchema(t, specGet)

	create := oastest.PathOp(t, paths, "post", "/v1/m/sessions/protocol-binding-specs")
	if create["x-required-permission"] != "sessions:protocol-binding:write" ||
		!hasParameter(create, "Idempotency-Key", "header") ||
		!hasParameter(create, "If-Plan-Hash", "header") ||
		hasParameter(create, "If-Match", "header") {
		t.Errorf("protocol binding spec create contract = %#v", create)
	}
	assertParameterEnum(t, create, "mode", "query", []string{"validate", "plan", "apply"})
	createBody := create["requestBody"].(map[string]any)
	if createBody["required"] != true {
		t.Errorf("protocol binding spec create body required = %v, want true", createBody["required"])
	}
	createSchema := requestBodySchema(t, create)
	if createSchema["additionalProperties"] != false {
		t.Errorf("protocol binding spec create accepts undeclared fields: %#v", createSchema)
	}
	assertSchemaRequires(t, createSchema,
		"workspace_id", "binding_key", "generation", "protocol", "protocol_version",
		"direction", "local_kind", "local_selector", "peer_authority",
		"remote_resource_kind", "remote_resource_ref", "mapping_schema", "mapping",
		"permission_profile_ref", "currency_policy",
	)
	if schemaRequires(createSchema, "validation") {
		t.Errorf("protocol binding create must not require server-derived validation: %#v", createSchema["required"])
	}
	createFields := requestBodyProperties(t, create)
	assertSchemaEnum(t, createFields["protocol"], []string{"a2a", "mcp"})
	assertSchemaEnum(t, createFields["direction"], []string{"inbound", "outbound", "bidirectional"})
	assertSchemaEnum(t, createFields["local_kind"], []string{"work_item", "agent", "model", "channel"})
	assertSchemaEnum(t, createFields["currency_policy"], []string{"pinned"})
	if selector := createFields["local_selector"].(map[string]any); selector["type"] != "object" {
		t.Errorf("protocol binding local_selector schema = %#v", selector)
	}
	mapping := createFields["mapping"].(map[string]any)
	if mapping["minItems"] != 1 || mapping["maxItems"] != 128 {
		t.Errorf("protocol binding mapping bounds = %#v", mapping)
	}
	mappingItem := mapping["items"].(map[string]any)
	if mappingItem["additionalProperties"] != false {
		t.Errorf("protocol binding mapping rule accepts undeclared fields: %#v", mappingItem)
	}
	assertSchemaRequires(t, mappingItem, "source", "target", "cardinality", "transform")
	validation := createFields["validation"].(map[string]any)
	if validation["additionalProperties"] != false {
		t.Errorf("protocol binding validation accepts undeclared fields: %#v", validation)
	}
	if validation["readOnly"] != true {
		t.Errorf("protocol binding validation must be server-derived/read-only: %#v", validation)
	}
	assertSchemaRequires(t, validation, "verdict", "code")
	validationFields := validation["properties"].(map[string]any)
	assertSchemaEnum(t, validationFields["verdict"], []string{"CLEAN", "BROKEN", "UNKNOWN"})
	createResponses := create["responses"].(map[string]any)
	if createResponses["201"] == nil || createResponses["428"] == nil {
		t.Errorf("protocol binding spec create responses = %#v", createResponses)
	}

	for _, action := range []string{"activate", "disable"} {
		path := "/v1/m/sessions/protocol-binding-specs/{id}/" + action
		op := oastest.PathOp(t, paths, "post", path)
		if op["x-required-permission"] != "sessions:protocol-binding:admin" || !hasPathParam(op, "id") {
			t.Errorf("protocol binding spec %s contract = %#v", action, op)
		}
		for _, header := range []string{"Idempotency-Key", "If-Plan-Hash", "If-Match"} {
			if !hasParameter(op, header, "header") {
				t.Errorf("protocol binding spec %s missing header %s", action, header)
			}
		}
		if schema := parameterOf(t, op, "If-Match", "header")["schema"].(map[string]any); schema["pattern"] == nil {
			t.Errorf("protocol binding spec %s If-Match has no strong ETag schema: %#v", action, schema)
		}
		assertParameterEnum(t, op, "mode", "query", []string{"validate", "plan", "apply"})
		body := op["requestBody"].(map[string]any)
		if body["required"] != false {
			t.Errorf("protocol binding spec %s body required = %v, want false", action, body["required"])
		}
		transitionFields := requestBodyProperties(t, op)
		if len(transitionFields) != 1 || transitionFields["plan_hash"] == nil {
			t.Errorf("protocol binding spec %s body fields = %#v, want only plan_hash", action, transitionFields)
		}
		responses := op["responses"].(map[string]any)
		for _, status := range []string{"412", "428", "503"} {
			if responses[status] == nil {
				t.Errorf("protocol binding spec %s missing response %s", action, status)
			}
		}
	}
}

func paramsOf(op map[string]any) []any {
	p, _ := op["parameters"].([]any)
	return p
}

func hasPathParam(op map[string]any, name string) bool {
	for _, p := range paramsOf(op) {
		m := p.(map[string]any)
		if m["name"] == name && m["in"] == "path" && m["required"] == true {
			return true
		}
	}
	return false
}

func hasParameter(op map[string]any, name, in string) bool {
	for _, p := range paramsOf(op) {
		m := p.(map[string]any)
		if m["name"] == name && m["in"] == in {
			return true
		}
	}
	return false
}

func parameterOf(t *testing.T, op map[string]any, name, in string) map[string]any {
	t.Helper()
	for _, p := range paramsOf(op) {
		parameter := p.(map[string]any)
		if parameter["name"] == name && parameter["in"] == in {
			return parameter
		}
	}
	t.Fatalf("operation has no %s parameter %s", in, name)
	return nil
}

func assertParameterEnum(t *testing.T, op map[string]any, name, in string, want []string) {
	t.Helper()
	parameter := parameterOf(t, op, name, in)
	schema, ok := parameter["schema"].(map[string]any)
	if !ok {
		t.Fatalf("%s %s parameter schema = %#v", in, name, parameter["schema"])
	}
	got, ok := schema["enum"].([]any)
	if !ok || len(got) != len(want) {
		t.Fatalf("%s %s parameter enum = %#v", in, name, schema["enum"])
	}
	for i, value := range want {
		if got[i] != value {
			t.Errorf("%s %s parameter enum[%d] = %v, want %s", in, name, i, got[i], value)
		}
	}
}

func assertPageParameterSchemas(t *testing.T, op map[string]any) {
	t.Helper()
	limit := parameterOf(t, op, "limit", "query")["schema"].(map[string]any)
	if limit["type"] != "integer" || limit["minimum"] != 1 || limit["maximum"] != 200 || limit["default"] != 100 {
		t.Errorf("page limit schema = %#v", limit)
	}
	cursor := parameterOf(t, op, "cursor", "query")["schema"].(map[string]any)
	if cursor["type"] != "string" || cursor["format"] != "uuid" {
		t.Errorf("page cursor schema = %#v", cursor)
	}
}

func assertProtocolBindingIDPathSchema(t *testing.T, op map[string]any) {
	t.Helper()
	schema := parameterOf(t, op, "id", "path")["schema"].(map[string]any)
	if schema["format"] != "uuid" || schema["pattern"] == nil {
		t.Errorf("protocol binding id path schema = %#v", schema)
	}
}

func assertSchemaEnum(t *testing.T, raw any, want []string) {
	t.Helper()
	schema, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("schema = %#v", raw)
	}
	got, ok := schema["enum"].([]any)
	if !ok || len(got) != len(want) {
		t.Fatalf("schema enum = %#v, want %v", schema["enum"], want)
	}
	for i, value := range want {
		if got[i] != value {
			t.Errorf("schema enum[%d] = %v, want %s", i, got[i], value)
		}
	}
}

func assertSchemaRequires(t *testing.T, schema map[string]any, want ...string) {
	t.Helper()
	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("schema required = %#v", schema["required"])
	}
	set := make(map[string]bool, len(required))
	for _, name := range required {
		value, ok := name.(string)
		if !ok {
			t.Fatalf("non-string schema required entry = %#v", name)
		}
		set[value] = true
	}
	for _, name := range want {
		if !set[name] {
			t.Errorf("schema does not require %s: %#v", name, required)
		}
	}
}

func schemaRequires(schema map[string]any, name string) bool {
	required, ok := schema["required"].([]any)
	if !ok {
		return false
	}
	for _, entry := range required {
		if entry == name {
			return true
		}
	}
	return false
}

// sortedSchemaStrings reads a schema's `required` / `enum` list. It lives here
// because the identical helper next door is in package `api` and this file is
// `api_test`; duplicating six lines beats widening a package boundary for them.
func sortedSchemaStrings(value any) []string {
	values, _ := value.([]any)
	out := make([]string, 0, len(values))
	for _, v := range values {
		s, _ := v.(string)
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// responseSchema returns the application/json schema an operation publishes for
// one status, or fails naming the status — an absent response and an unspecified
// one are different defects and must not read the same.
func responseSchema(t *testing.T, op map[string]any, status string) map[string]any {
	t.Helper()
	responses, ok := op["responses"].(map[string]any)
	if !ok {
		t.Fatal("operation has no responses")
	}
	resp, ok := responses[status].(map[string]any)
	if !ok {
		t.Fatalf("operation publishes no %s response", status)
	}
	content, ok := resp["content"].(map[string]any)
	if !ok {
		t.Fatalf("the %s response has no content", status)
	}
	media, ok := content["application/json"].(map[string]any)
	if !ok {
		t.Fatalf("the %s response has no application/json content", status)
	}
	schema, ok := media["schema"].(map[string]any)
	if !ok {
		t.Fatalf("the %s response has no schema", status)
	}
	return schema
}

func responseStatuses(t *testing.T, op map[string]any) []string {
	t.Helper()
	responses, ok := op["responses"].(map[string]any)
	if !ok {
		t.Fatal("operation has no responses")
	}
	out := make([]string, 0, len(responses))
	for status := range responses {
		out = append(out, status)
	}
	sort.Strings(out)
	return out
}

func requestBodyProperties(t *testing.T, op map[string]any) map[string]any {
	t.Helper()
	schema := requestBodySchema(t, op)
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("requestBody schema has no properties")
	}
	return properties
}

func requestBodySchema(t *testing.T, op map[string]any) map[string]any {
	t.Helper()
	body, ok := op["requestBody"].(map[string]any)
	if !ok {
		t.Fatal("operation has no requestBody")
	}
	content, ok := body["content"].(map[string]any)
	if !ok {
		t.Fatal("requestBody has no content")
	}
	media, ok := content["application/json"].(map[string]any)
	if !ok {
		t.Fatal("requestBody has no application/json content")
	}
	schema, ok := media["schema"].(map[string]any)
	if !ok {
		t.Fatal("requestBody has no schema")
	}
	return schema
}

// TestOperationDocumentationDispositionSentinels pins the request-body kind
// this module publishes for representative mutations in the beta document.
func TestOperationDocumentationDispositionSentinels(t *testing.T) {
	t.Parallel()
	module := New()
	for _, test := range []struct{ name, method, pattern, want string }{
		{"raw workspace file", "put", "/workspaces/{ref}/files/raw", "schema-published"},
		{"protocol binding reconcile", "post", "/protocol-bindings/{id}/reconcile", "schema-published"},
		{"run input", "post", "/runs/{ref}/input", "schema-published"},
		{"work mutation", "post", "/work-items", "schema-published"},
	} {
		op := oastest.Op(t, module, test.method, "/v1/m/sessions"+test.pattern)
		if got := op["x-olivares-request-body-disposition"]; got != test.want {
			t.Errorf("%s: disposition = %v, want %s", test.name, got, test.want)
		}
	}
}
