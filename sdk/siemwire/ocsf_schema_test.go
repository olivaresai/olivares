// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package siemwire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file validates the encoder's output against the OFFICIAL OCSF v1.8.0
// class schemas (testdata/ocsf/*.schema.json — the JSON Schema export of
// schema.ocsf.io/schema/1.8.0/classes/<class>?profiles=ai_operation, vendored
// verbatim). The generated class schemas set additionalProperties:false,
// so this is a REAL conformance check: an un-profiled field (e.g. the old
// `cloud` object), an ai_model missing ai_provider, or a message_context
// naming neither application nor service all FAIL here.
//
// The validator below implements exactly the JSON Schema subset those files
// use ($defs/$ref, type, enum, const, required, properties,
// additionalProperties, items, anyOf, oneOf+not.required) — verified by
// surveying the vendored files; an unknown construct fails the test rather
// than passing silently.

// schemaValidator validates a decoded JSON instance against one vendored
// OCSF class schema.
type schemaValidator struct {
	root map[string]any
	defs map[string]any
}

func loadOCSFSchema(t *testing.T, class string) *schemaValidator {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "ocsf", class+".schema.json"))
	if err != nil {
		t.Fatalf("load vendored OCSF schema: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatalf("parse vendored OCSF schema: %v", err)
	}
	defs, _ := root["$defs"].(map[string]any)
	return &schemaValidator{root: root, defs: defs}
}

// validate checks instance against the root schema, returning every violation.
func (v *schemaValidator) validate(instance any) []string {
	return v.check(v.root, instance, "$")
}

// resolve follows a #/$defs/<name> reference.
func (v *schemaValidator) resolve(ref string) (map[string]any, error) {
	const p = "#/$defs/"
	if !strings.HasPrefix(ref, p) {
		return nil, fmt.Errorf("unsupported $ref %q", ref)
	}
	d, ok := v.defs[strings.TrimPrefix(ref, p)].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unresolved $ref %q", ref)
	}
	return d, nil
}

func (v *schemaValidator) check(schema map[string]any, instance any, path string) []string {
	var errs []string
	if ref, ok := schema["$ref"].(string); ok {
		d, err := v.resolve(ref)
		if err != nil {
			return []string{path + ": " + err.Error()}
		}
		return v.check(d, instance, path)
	}

	if typ, ok := schema["type"].(string); ok {
		if msg := checkType(typ, instance); msg != "" {
			// A type mismatch makes the structural checks below meaningless.
			return []string{path + ": " + msg}
		}
	}
	if c, ok := schema["const"]; ok && !jsonEqual(c, instance) {
		errs = append(errs, fmt.Sprintf("%s: const mismatch (want %v, got %v)", path, c, instance))
	}
	if enum, ok := schema["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			if jsonEqual(e, instance) {
				found = true
				break
			}
		}
		if !found {
			errs = append(errs, fmt.Sprintf("%s: value %v not in enum", path, instance))
		}
	}

	switch inst := instance.(type) {
	case map[string]any:
		if req, ok := schema["required"].([]any); ok {
			for _, r := range req {
				if _, present := inst[r.(string)]; !present {
					errs = append(errs, fmt.Sprintf("%s: missing required %q", path, r))
				}
			}
		}
		props, _ := schema["properties"].(map[string]any)
		addl, hasAddl := schema["additionalProperties"]
		for k, val := range inst {
			ps, known := props[k].(map[string]any)
			if !known {
				if hasAddl && addl == false {
					errs = append(errs, fmt.Sprintf("%s: additional property %q not allowed", path, k))
				}
				continue
			}
			errs = append(errs, v.check(ps, val, path+"."+k)...)
		}
		if branches, ok := schema["anyOf"].([]any); ok {
			if !v.someBranch(branches, inst) {
				errs = append(errs, fmt.Sprintf("%s: no anyOf branch satisfied", path))
			}
		}
		if branches, ok := schema["oneOf"].([]any); ok {
			if !v.someBranch(branches, inst) {
				errs = append(errs, fmt.Sprintf("%s: no oneOf branch satisfied", path))
			}
		}
	case []any:
		if items, ok := schema["items"].(map[string]any); ok {
			for i, e := range inst {
				errs = append(errs, v.check(items, e, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
	}
	return errs
}

// someBranch reports whether at least one anyOf/oneOf branch is satisfied. The
// vendored schemas use branches of the form {required:[...]} optionally with
// {not:{required:[...]}} (the at_least_one / exactly-one constraints).
func (v *schemaValidator) someBranch(branches []any, inst map[string]any) bool {
	for _, b := range branches {
		bm, ok := b.(map[string]any)
		if !ok {
			continue
		}
		ok = true
		if req, has := bm["required"].([]any); has {
			for _, r := range req {
				if _, present := inst[r.(string)]; !present {
					ok = false
					break
				}
			}
		}
		if not, has := bm["not"].(map[string]any); ok && has {
			if req, has := not["required"].([]any); has {
				all := true
				for _, r := range req {
					if _, present := inst[r.(string)]; !present {
						all = false
						break
					}
				}
				if all {
					ok = false
				}
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func checkType(typ string, instance any) string {
	switch typ {
	case "object":
		if _, ok := instance.(map[string]any); !ok {
			return fmt.Sprintf("want object, got %T", instance)
		}
	case "array":
		if _, ok := instance.([]any); !ok {
			return fmt.Sprintf("want array, got %T", instance)
		}
	case "string":
		if _, ok := instance.(string); !ok {
			return fmt.Sprintf("want string, got %T", instance)
		}
	case "boolean":
		if _, ok := instance.(bool); !ok {
			return fmt.Sprintf("want boolean, got %T", instance)
		}
	case "integer":
		f, ok := instance.(float64)
		if !ok || f != math.Trunc(f) {
			return fmt.Sprintf("want integer, got %v (%T)", instance, instance)
		}
	case "number":
		if _, ok := instance.(float64); !ok {
			return fmt.Sprintf("want number, got %T", instance)
		}
	default:
		return fmt.Sprintf("unsupported schema type %q", typ)
	}
	return ""
}

func jsonEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

// mustValidate encodes in, decodes it back and validates it against the given
// vendored class schema, failing the test on any violation.
func mustValidate(t *testing.T, class string, in OCSFInput) map[string]any {
	t.Helper()
	b, err := OCSF(in)
	if err != nil {
		t.Fatalf("OCSF(%s): %v", class, err)
	}
	var ev map[string]any
	if err := json.Unmarshal(b, &ev); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if errs := loadOCSFSchema(t, class).validate(ev); len(errs) > 0 {
		t.Fatalf("event does not validate against official %s schema:\n  %s\nevent: %s",
			class, strings.Join(errs, "\n  "), b)
	}
	return ev
}

var ocsfTestTime = time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)

func i64(v int64) *int64 { return &v }

// TestOCSFAPIActivityValidatesWithProfile pins the findings-feed shape: an API
// Activity (6003) event carrying the full ai_operation profile (ai_model +
// message_context) validates against the official 1.8.0 class schema.
func TestOCSFAPIActivityValidatesWithProfile(t *testing.T) {
	ev := mustValidate(t, "api_activity", OCSFInput{
		ActivityID:   2,
		ActivityName: "Read",
		SeverityID:   3,
		StatusID:     1,
		Time:         ocsfTestTime,
		Message:      "agent read a resource",
		Device:       Device{Vendor: "Olivares", Product: "ControlPlane", Version: "1.0"},
		Operation:    "Read",
		ActorAppName: "research-agent",
		SrcName:      "sess-1",
		AIModel:      &OCSFAIModel{Name: "claude-sonnet-4-6", AIProvider: "anthropic", Version: "2026-01"},
		MessageContext: &OCSFMessageContext{
			UID: "conv-1", AIRole: "Agent", AIRoleID: OCSFRoleAgent,
			Application:  &OCSFApplication{Name: "research-agent"},
			Service:      &OCSFService{Name: "anthropic"},
			PromptTokens: i64(1200), CompletionTokens: i64(80),
		},
		Unmapped: map[string]any{"ai.olivares.tenant.id": "t1"},
	})
	if ev["class_uid"].(float64) != 6003 || ev["type_uid"].(float64) != 600302 {
		t.Fatalf("class/type uid wrong: %v / %v", ev["class_uid"], ev["type_uid"])
	}
	md := ev["metadata"].(map[string]any)
	profs, _ := md["profiles"].([]any)
	if len(profs) != 1 || profs[0] != "ai_operation" {
		t.Fatalf("metadata.profiles must declare ai_operation, got %v", md["profiles"])
	}
	if _, hasCloud := ev["cloud"]; hasCloud {
		t.Fatal("cloud must not be emitted (it belongs to the cloud profile)")
	}
}

// TestOCSFLedgerShapeValidates pins the audit-ledger shape: no profile
// attributes, integrity fields under unmapped — and no profiles declaration.
func TestOCSFLedgerShapeValidates(t *testing.T) {
	ev := mustValidate(t, "api_activity", OCSFInput{
		ActivityID:   1,
		ActivityName: "Create",
		SeverityID:   1,
		StatusID:     1,
		Time:         ocsfTestTime,
		Message:      "policy.create",
		Device:       Device{Vendor: "Olivares", Product: "ControlPlane", Version: "1.0"},
		Operation:    "policy.create",
		ActorAppName: "admin@example",
		SrcName:      "admin@example",
		Unmapped:     map[string]any{"ai.olivares.audit.seq": 7, "ai.olivares.audit.hash": "ab12"},
	})
	if _, has := ev["metadata"].(map[string]any)["profiles"]; has {
		t.Fatal("an event with no profile attributes must not declare ai_operation")
	}
}

// TestOCSFProcessActivityValidates pins the process_activity (1007) shape the
// 1.8.0 release registered the profile on: System Activity category, required
// device + process, no api/src_endpoint properties.
func TestOCSFProcessActivityValidates(t *testing.T) {
	ev := mustValidate(t, "process_activity", OCSFInput{
		Class:        OCSFClassProcessActivity,
		ActivityID:   1, // Launch
		ActivityName: "Launch",
		SeverityID:   1,
		Time:         ocsfTestTime,
		Message:      "agent launched a subprocess",
		Device:       Device{Vendor: "Olivares", Product: "ControlPlane", Version: "1.0"},
		Operation:    "subprocess",
		ActorAppName: "coding-agent",
		Process:      &OCSFProcess{Name: "rg", PID: i64(4242)},
		HostDevice:   &OCSFDevice{TypeID: 3, Type: "Server", Hostname: "dev-box"},
		AIModel:      &OCSFAIModel{Name: "claude-sonnet-4-6", AIProvider: "anthropic"},
	})
	if ev["class_uid"].(float64) != 1007 || ev["category_uid"].(float64) != 1 {
		t.Fatalf("process_activity must be 1007 / category 1, got %v / %v", ev["class_uid"], ev["category_uid"])
	}
	if _, has := ev["api"]; has {
		t.Fatal("api is not a property of process_activity")
	}
	if _, has := ev["src_endpoint"]; has {
		t.Fatal("src_endpoint is not a property of process_activity")
	}
}

// TestOCSFProcessActivityFailsClosed pins the fail-closed contract: selecting
// process_activity without its REQUIRED process/device yields an error, never
// an invalid event.
func TestOCSFProcessActivityFailsClosed(t *testing.T) {
	base := OCSFInput{Class: OCSFClassProcessActivity, ActivityID: 1, SeverityID: 1, Time: ocsfTestTime}
	if _, err := OCSF(base); err == nil {
		t.Fatal("process_activity without process must error")
	}
	in := base
	in.Process = &OCSFProcess{Name: "rg"} // name alone does NOT satisfy at_least_one(cpid|pid|uid)
	in.HostDevice = &OCSFDevice{TypeID: 3, Hostname: "h"}
	if _, err := OCSF(in); err == nil {
		t.Fatal("process without pid/uid must error (verified 1.8.0 constraint)")
	}
	in.Process = &OCSFProcess{Name: "rg", PID: i64(1)}
	in.HostDevice = &OCSFDevice{Hostname: "h"} // missing type_id
	if _, err := OCSF(in); err == nil {
		t.Fatal("device without type_id must error (verified 1.8.0 constraint)")
	}
}

// TestOCSFDatastoreActivityValidates pins the third registered class (6005).
func TestOCSFDatastoreActivityValidates(t *testing.T) {
	ev := mustValidate(t, "datastore_activity", OCSFInput{
		Class:        OCSFClassDatastoreActivity,
		ActivityID:   2, // Read
		ActivityName: "Read",
		SeverityID:   1,
		Time:         ocsfTestTime,
		Message:      "retrieval against a vector store",
		Device:       Device{Vendor: "Olivares", Product: "ControlPlane", Version: "1.0"},
		Operation:    "retrieval",
		ActorAppName: "rag-agent",
		SrcName:      "sess-9",
		AIModel:      &OCSFAIModel{Name: "text-embedding-3-large", AIProvider: "openai"},
		Database:     &OCSFDatabase{TypeID: 7, Type: "Vector", Name: "kb-embeddings"},
	})
	if ev["class_uid"].(float64) != 6005 {
		t.Fatalf("datastore_activity must be 6005, got %v", ev["class_uid"])
	}
	if _, has := ev["api"]; has {
		t.Fatal("api is not a property of datastore_activity")
	}
}

// TestOCSFIncompleteProfileObjectsParkedUnderUnmapped pins the guard: an
// ai_model without its REQUIRED ai_provider, or a message_context naming
// neither application nor service, is parked under unmapped — schema-valid,
// nothing silently dropped, INCLUDING the role fields and every token count.
func TestOCSFIncompleteProfileObjectsParkedUnderUnmapped(t *testing.T) {
	ev := mustValidate(t, "api_activity", OCSFInput{
		ActivityID: 99, ActivityName: "Other", SeverityID: 1, Time: ocsfTestTime,
		Device:  Device{Vendor: "Olivares", Product: "ControlPlane", Version: "1.0"},
		AIModel: &OCSFAIModel{Name: "mystery-model"}, // no provider
		MessageContext: &OCSFMessageContext{
			UID: "conv-2", AIRole: "Agent", AIRoleID: OCSFRoleAgent,
			PromptTokens: i64(5), CompletionTokens: i64(3), TotalTokens: i64(8),
		},
	})
	if _, has := ev["ai_model"]; has {
		t.Fatal("an ai_model without ai_provider must not be emitted (required in 1.8.0)")
	}
	if _, has := ev["message_context"]; has {
		t.Fatal("a message_context without application/service must not be emitted (at_least_one in 1.8.0)")
	}
	un := ev["unmapped"].(map[string]any)
	if un["ai_model.name"] != "mystery-model" {
		t.Fatalf("parked ai_model.name missing from unmapped: %v", un)
	}
	if un["message_context.uid"] != "conv-2" || un["message_context.prompt_tokens"].(float64) != 5 {
		t.Fatalf("parked message_context fields missing from unmapped: %v", un)
	}
	if un["message_context.completion_tokens"].(float64) != 3 || un["message_context.total_tokens"].(float64) != 8 {
		t.Fatalf("parked completion/total tokens missing from unmapped: %v", un)
	}
	if un["message_context.ai_role"] != "Agent" || un["message_context.ai_role_id"].(float64) != 4 {
		t.Fatalf("parked ai_role fields missing from unmapped: %v", un)
	}
	if _, has := ev["metadata"].(map[string]any)["profiles"]; has {
		t.Fatal("nothing of the profile was emitted, so it must not be declared")
	}
}

// TestOCSFInvalidSubObjectScrubbedFromValidContext pins the sub-object guard: a
// VALID message_context (service named) carrying a Version-only application —
// which violates the application object's own name|uid constraint — has that
// sub-object scrubbed and parked, while the context itself is still emitted.
func TestOCSFInvalidSubObjectScrubbedFromValidContext(t *testing.T) {
	ev := mustValidate(t, "api_activity", OCSFInput{
		ActivityID: 2, ActivityName: "Read", SeverityID: 1, Time: ocsfTestTime,
		Device: Device{Vendor: "Olivares", Product: "ControlPlane", Version: "1.0"},
		MessageContext: &OCSFMessageContext{
			UID:         "conv-3",
			Application: &OCSFApplication{Version: "2.1"}, // no name/uid: invalid alone
			Service:     &OCSFService{Name: "anthropic"},
		},
	})
	mc := ev["message_context"].(map[string]any)
	if _, has := mc["application"]; has {
		t.Fatal("a name/uid-less application must be scrubbed from the emitted context")
	}
	if svc := mc["service"].(map[string]any); svc["name"] != "anthropic" {
		t.Fatalf("the valid service must survive: %v", mc)
	}
	if un := ev["unmapped"].(map[string]any); un["message_context.application.version"] != "2.1" {
		t.Fatalf("the scrubbed application content must be parked: %v", un)
	}
}

// TestOCSFPartialDeviceStillValidates pins the per-field device default: a
// vendor-only Device must not cascade into an empty actor{}/src_endpoint{}
// (both REQUIRED with at_least_one constraints on 6003).
func TestOCSFPartialDeviceStillValidates(t *testing.T) {
	ev := mustValidate(t, "api_activity", OCSFInput{
		ActivityID: 2, ActivityName: "Read", SeverityID: 1, Time: ocsfTestTime,
		Device: Device{Vendor: "Acme"}, // product empty; no actor/src names
	})
	if app := ev["actor"].(map[string]any)["app_name"]; app == "" || app == nil {
		t.Fatalf("actor.app_name must be defaulted for a partial device: %v", ev["actor"])
	}
}

// TestOCSFEnumClampsParkOriginal pins the enum clamps: an activity_id outside
// the SELECTED class's enum, an unknown severity_id and an unknown status_id
// are clamped to 99 with the original parked under unmapped.
func TestOCSFEnumClampsParkOriginal(t *testing.T) {
	// 5 is Set User ID on 1007 but OUT of the 6003 enum [0..4,99].
	ev := mustValidate(t, "api_activity", OCSFInput{
		ActivityID: 5, ActivityName: "Set User ID", SeverityID: 42, StatusID: 7, Time: ocsfTestTime,
		Device: Device{Vendor: "Olivares", Product: "ControlPlane", Version: "1.0"},
	})
	if ev["activity_id"].(float64) != 99 || ev["type_uid"].(float64) != 600399 {
		t.Fatalf("out-of-enum activity must clamp to 99/600399: %v / %v", ev["activity_id"], ev["type_uid"])
	}
	if ev["severity_id"].(float64) != 99 || ev["status_id"].(float64) != 99 {
		t.Fatalf("out-of-enum severity/status must clamp to 99: %v / %v", ev["severity_id"], ev["status_id"])
	}
	un := ev["unmapped"].(map[string]any)
	if un["activity_id.original"].(float64) != 5 || un["severity_id.original"].(float64) != 42 || un["status_id.original"].(float64) != 7 {
		t.Fatalf("clamped originals must be parked: %v", un)
	}
	// The same id 5 IS valid on process_activity — no clamp there.
	pev := mustValidate(t, "process_activity", OCSFInput{
		Class: OCSFClassProcessActivity, ActivityID: 5, ActivityName: "Set User ID", SeverityID: 1, Time: ocsfTestTime,
		Device:     Device{Vendor: "Olivares", Product: "ControlPlane", Version: "1.0"},
		Process:    &OCSFProcess{Name: "sudo", PID: i64(7)},
		HostDevice: &OCSFDevice{TypeID: 3, Hostname: "h"},
	})
	if pev["activity_id"].(float64) != 5 {
		t.Fatalf("activity 5 is valid on 1007 and must not clamp: %v", pev["activity_id"])
	}
}

// TestOCSFDatastoreActivityFailsClosed pins the third class's fail-closed
// contract (process_activity has its own above).
func TestOCSFDatastoreActivityFailsClosed(t *testing.T) {
	base := OCSFInput{Class: OCSFClassDatastoreActivity, ActivityID: 2, SeverityID: 1, Time: ocsfTestTime}
	if _, err := OCSF(base); err == nil {
		t.Fatal("datastore_activity without a database must error")
	}
	in := base
	in.Database = &OCSFDatabase{Name: "kb"} // missing type_id
	if _, err := OCSF(in); err == nil {
		t.Fatal("a database without type_id must error (required in 1.8.0)")
	}
	in.Database = &OCSFDatabase{TypeID: 7} // missing name/uid
	if _, err := OCSF(in); err == nil {
		t.Fatal("a database without name/uid must error (at_least_one in 1.8.0)")
	}
}

// TestOCSFSchemaValidatorCatchesViolations proves the validator (and so every
// test above) actually bites: known-invalid shapes must FAIL validation.
func TestOCSFSchemaValidatorCatchesViolations(t *testing.T) {
	v := loadOCSFSchema(t, "api_activity")
	valid, err := OCSF(OCSFInput{
		ActivityID: 2, ActivityName: "Read", SeverityID: 1, Time: ocsfTestTime,
		Device:  Device{Vendor: "V", Product: "P", Version: "1"},
		AIModel: &OCSFAIModel{Name: "m", AIProvider: "p"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mutate := func(f func(map[string]any)) map[string]any {
		var ev map[string]any
		if err := json.Unmarshal(valid, &ev); err != nil {
			t.Fatal(err)
		}
		f(ev)
		return ev
	}
	cases := map[string]map[string]any{
		// The pre encoder always emitted this; it must be rejected.
		"unprofiled cloud field": mutate(func(ev map[string]any) {
			ev["cloud"] = map[string]any{"provider": "anthropic"}
		}),
		"ai_model missing ai_provider": mutate(func(ev map[string]any) {
			ev["ai_model"] = map[string]any{"name": "m"}
		}),
		"message_context without application/service": mutate(func(ev map[string]any) {
			ev["message_context"] = map[string]any{"uid": "c", "ai_role_id": float64(4)}
		}),
		"missing required src_endpoint": mutate(func(ev map[string]any) {
			delete(ev, "src_endpoint")
		}),
		"activity_id outside enum": mutate(func(ev map[string]any) {
			ev["activity_id"] = float64(7)
		}),
		"class_uid const violated": mutate(func(ev map[string]any) {
			ev["class_uid"] = float64(1007)
		}),
	}
	for name, ev := range cases {
		if errs := v.validate(ev); len(errs) == 0 {
			t.Errorf("%s: validator did not catch the violation", name)
		}
	}
}

// TestOCSFVersionPinMatchesVendoredSchemas closes the one gap the schema files
// cannot close themselves: they type metadata.version as a plain string, so
// bumping OCSFVersion without re-vendoring would leave every test passing while
// the encoder claims conformance to a version it was never checked against.
// testdata/ocsf/VERSION records what the vendored schemas actually are.
func TestOCSFVersionPinMatchesVendoredSchemas(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "ocsf", "VERSION"))
	if err != nil {
		t.Fatalf("read vendored schema version: %v", err)
	}
	vendored := strings.TrimSpace(string(raw))
	if vendored != OCSFVersion {
		t.Fatalf("OCSFVersion = %q but the vendored schemas are %q — re-vendor "+
			"testdata/ocsf/*.schema.json for the new version (see its README) "+
			"instead of claiming a conformance nobody checked", OCSFVersion, vendored)
	}

	// And the version the encoder puts on the wire is that same pin, not a
	// separate literal that could drift from it.
	ev, err := OCSF(OCSFInput{
		ActivityID: 2, ActivityName: "Read", SeverityID: 1, Time: ocsfTestTime,
		Device: Device{Vendor: "V", Product: "P", Version: "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Metadata struct {
			Version string `json:"version"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(ev, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Metadata.Version != vendored {
		t.Errorf("emitted metadata.version = %q, want the vendored %q", doc.Metadata.Version, vendored)
	}
}

// TestOCSFOutputIsByteDeterministic pins what a SIEM de-duplicates on. The event
// carries maps (unmapped, and the AOS extension under it), and a map that
// marshaled in iteration order would give the same event a different body on
// every render — turning one governance event into N alerts downstream.
func TestOCSFOutputIsByteDeterministic(t *testing.T) {
	in := OCSFInput{
		ActivityID: 2, ActivityName: "Read", SeverityID: 3, Time: ocsfTestTime,
		Device:  Device{Vendor: "Olivares.AI", Product: "ControlPlane", Version: "1"},
		AIModel: &OCSFAIModel{Name: "claude", AIProvider: "anthropic"},
		Unmapped: map[string]any{
			"olv_seq":    "42",
			"olv_tenant": "acme",
			"olv_hash":   "0a0b0c",
			"a_first":    "1",
			"z_last":     "26",
		},
	}
	first, err := OCSF(in)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		again, err := OCSF(in)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("render %d differs from the first:\n got: %s\nwant: %s", i, again, first)
		}
	}
	// The caller's unmapped keys are emitted in sorted order, which is what makes
	// the above hold rather than being luck of the map iteration.
	body := string(first)
	for _, pair := range [][2]string{
		{"a_first", "olv_hash"}, {"olv_hash", "olv_seq"}, {"olv_seq", "olv_tenant"},
		{"olv_tenant", "z_last"},
	} {
		if strings.Index(body, `"`+pair[0]+`"`) > strings.Index(body, `"`+pair[1]+`"`) {
			t.Errorf("unmapped keys are not in sorted order: %q precedes %q\n%s", pair[1], pair[0], body)
		}
	}
}
