// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/modules/internal/oastest"
)

func TestClaudePolicyRequestBodyCensus(t *testing.T) {
	t.Parallel()
	patterns := []string{"/{surface}/validate", "/{surface}/dry-run", "/{surface}/publish", "/{surface}/checkin"}
	for _, pattern := range patterns {
		route := operation{method: http.MethodPost, pattern: pattern}
		decl, ok := claudePolicyRequestBodyDeclarationFor(route.method, route.pattern)
		if !ok || decl.kind != claudePolicyBodyful {
			t.Fatalf("POST %s = (%#v, %t), want bodyful", pattern, decl, ok)
		}
		body, hasBody := claudePolicyRequestBody(route.method, route.pattern)
		if !hasBody || body["required"] != true {
			t.Fatalf("POST %s requestBody = (%#v, %t)", pattern, body, hasBody)
		}
	}
}

func TestClaudePolicySchemasMatchHandlerSemantics(t *testing.T) {
	t.Parallel()
	validate := claudePolicyContentSchema(false, false)
	if _, required := validate["required"]; required {
		t.Fatal("validate accepts empty content and returns diagnostics")
	}
	for _, schema := range []map[string]any{
		claudePolicyContentSchema(true, false),
		claudePolicyContentSchema(true, true),
	} {
		if got := oastest.Strings(schema["required"]); !reflect.DeepEqual(got, []string{"content"}) {
			t.Fatalf("content required = %v", got)
		}
		if schema["additionalProperties"] != false {
			t.Fatal("content decoder is strict")
		}
	}
	checkin := claudePolicyCheckinSchema()
	if got := oastest.Strings(checkin["required"]); !reflect.DeepEqual(got, []string{"scope"}) {
		t.Fatalf("checkin required = %v", got)
	}
	if got := len(checkin["properties"].(map[string]any)); got != 6 {
		t.Fatalf("checkin property count = %d, want 6", got)
	}
}

// The workstation inventory rides on the check-in: tools[] carries the driver, its
// version and its sign-in state, and nothing that identifies the account or could
// be a secret. The state set is the one `olivares tool ls` and the provider
// snapshot already publish.
func TestClaudePolicyCheckinCarriesTheToolInventory(t *testing.T) {
	t.Parallel()
	props := oastest.Map(t, claudePolicyCheckinSchema()["properties"], "checkin properties")
	tools := oastest.Map(t, props["tools"], "tools")
	variants, _ := tools["anyOf"].([]any)
	if len(variants) != 2 {
		t.Fatalf("tools = %#v, want an array or null", tools)
	}
	array := oastest.Map(t, variants[0], "tools array")
	if array["type"] != "array" || array["maxItems"] != 32 {
		t.Fatalf("tools array = %#v, want type array and maxItems 32", array)
	}
	if null := oastest.Map(t, variants[1], "tools null"); null["type"] != "null" {
		t.Fatalf("tools second variant = %#v, want null", null)
	}
	item := oastest.Map(t, array["items"], "tools item")
	if item["additionalProperties"] != false {
		t.Fatal("a tool entry is strict: an unknown field is refused, so no account detail rides along")
	}
	if got, want := oastest.Keys(oastest.Map(t, item["properties"], "tool properties")), []string{"driver", "state", "version"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tool fields = %v, want %v", got, want)
	}
	if got := oastest.Strings(item["required"]); !reflect.DeepEqual(got, []string{"driver", "state"}) {
		t.Fatalf("tool required = %v", got)
	}
	itemProps := oastest.Map(t, item["properties"], "tool properties")
	driver := oastest.Map(t, itemProps["driver"], "driver")
	if driver["pattern"] != `^[a-z0-9][a-z0-9._-]{0,63}$` {
		t.Fatalf("driver pattern = %v, want a lowercase driver key of at most 64 characters", driver["pattern"])
	}
	version := oastest.Map(t, itemProps["version"], "version")
	versionVariants, _ := version["anyOf"].([]any)
	if len(versionVariants) != 2 || oastest.Map(t, versionVariants[0], "version string")["pattern"] != `^[0-9A-Za-z][0-9A-Za-z.+_~-]{0,63}$` {
		t.Fatalf("version = %#v, want a nullable version-shaped string of at most 64 characters", version)
	}
	state := oastest.Map(t, itemProps["state"], "state")
	want := []string{"error", "not_installed", "not_signed_in", "ready", "unknown"}
	if got := oastest.Strings(state["enum"]); !reflect.DeepEqual(got, want) {
		t.Fatalf("state enum = %v, want %v", got, want)
	}
}

// The inventory read route is documented with the shape ent#289 implements.
func TestClaudePolicyInventoryReadIsDocumented(t *testing.T) {
	t.Parallel()
	op := oastest.Op(t, NewPolicyConsole(), "get", "/v1/m/claude-policy/inventory")
	responses := oastest.Map(t, op["responses"], "responses")
	if _, ok := responses["501"]; !ok {
		t.Fatal("the Community refusal is part of the contract: 501 is documented")
	}
	ok := oastest.Map(t, responses["200"], "200")
	schema := oastest.Map(t, oastest.Map(t, oastest.Map(t, ok["content"], "content")["application/json"], "json")["schema"], "schema")
	hosts := oastest.Map(t, oastest.Map(t, schema["properties"], "properties")["hosts"], "hosts")
	if got := oastest.Strings(schema["required"]); !reflect.DeepEqual(got, []string{"hosts"}) {
		t.Fatalf("inventory required = %v", got)
	}
	host := oastest.Map(t, hosts["items"], "host")
	if got := oastest.Strings(host["required"]); !reflect.DeepEqual(got, []string{"checked_in_at", "scope", "tools"}) {
		t.Fatalf("host required = %v", got)
	}
	if got, want := oastest.Keys(oastest.Map(t, host["properties"], "host properties")), []string{"checked_in_at", "scope", "tools"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("host fields = %v, want %v", got, want)
	}
	hostProps := oastest.Map(t, host["properties"], "host properties")
	if stamp := oastest.Map(t, hostProps["checked_in_at"], "checked_in_at"); stamp["format"] != "date-time" {
		t.Fatalf("checked_in_at = %#v, want date-time", stamp)
	}
	// The tools of the read are the tools of the check-in: one shape, one source.
	tools := oastest.Map(t, hostProps["tools"], "tools")
	if !reflect.DeepEqual(tools["items"], claudePolicyToolSchema()) {
		t.Fatalf("read tool = %#v, want the check-in tool entry %#v", tools["items"], claudePolicyToolSchema())
	}
}
