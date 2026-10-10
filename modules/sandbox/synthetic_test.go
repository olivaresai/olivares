// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandbox

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestSyntheticSamplesBecomeRunnableScenario(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "synthetic")
	editor := h.roleToken(admin, tenant, "generator@example.test", "editor")
	viewer := h.roleToken(admin, tenant, "reader@example.test", "viewer")
	const path = "/v1/m/sandbox/synthetic-data"
	request := map[string]any{"subject_kind": "agent", "count": 2, "seed": "{{subject_kind}}:user{{index}}@example.test"}
	first := h.do("POST", path, editor, request, tenantHdr(tenant))
	if first.code != http.StatusOK {
		t.Fatalf("generate = %d %s", first.code, first.raw)
	}
	want := []any{
		map[string]any{"key": "sample-0001", "input": "agent:user1@example.test"},
		map[string]any{"key": "sample-0002", "input": "agent:user2@example.test"},
	}
	if !reflect.DeepEqual(first.body["samples"], want) {
		t.Fatalf("samples = %#v, want %#v", first.body["samples"], want)
	}
	second := h.do("POST", path, editor, request, tenantHdr(tenant))
	if second.code != http.StatusOK || !reflect.DeepEqual(first.body, second.body) {
		t.Fatalf("repeated generation changed: %d %s", second.code, second.raw)
	}
	steps := []map[string]any{want[0].(map[string]any), want[1].(map[string]any)}
	id := h.createScenario(editor, tenant, "generated-users", steps, []map[string]any{
		{"resource": "agent:user1@example.test", "response": "first account"},
		{"resource": "agent:user2@example.test", "response": "second account"},
	})
	run := h.do("POST", "/v1/m/sandbox/scenarios/"+id+"/run", editor, map[string]any{}, tenantHdr(tenant))
	if run.code != http.StatusCreated || run.body["steps_ok"] != float64(2) || run.body["isolated"] != true || run.body["destroyed"] != true {
		t.Fatalf("generated scenario = %d %s", run.code, run.raw)
	}
	if r := h.do("POST", path, viewer, request, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("viewer generate = %d", r.code)
	}
	for name, body := range map[string]map[string]any{
		"negative count":           {"count": -1},
		"too many samples":         {"count": 101},
		"oversized seed":           {"seed": strings.Repeat("x", 8193)},
		"oversized subject":        {"subject_kind": strings.Repeat("x", 201)},
		"expanded input too large": {"subject_kind": strings.Repeat("x", 200), "seed": strings.Repeat("{{subject_kind}}", 42)},
		"unknown field":            {"prompt": "unused"},
	} {
		t.Run(name, func(t *testing.T) {
			if r := h.do("POST", path, editor, body, tenantHdr(tenant)); r.code != http.StatusBadRequest {
				t.Fatalf("invalid request = %d %s", r.code, r.raw)
			}
		})
	}
	defaults := h.do("POST", path, editor, map[string]any{}, tenantHdr(tenant))
	samples, _ := defaults.body["samples"].([]any)
	if defaults.code != http.StatusOK || len(samples) != 10 || samples[0].(map[string]any)["input"] != "agent-sample-1" {
		t.Fatalf("defaults = %d %s", defaults.code, defaults.raw)
	}
	t.Run("escaped batch must fit scenario request", func(t *testing.T) {
		tooLarge := h.do("POST", path, editor, map[string]any{"count": 100, "seed": strings.Repeat("\"", 8192)}, tenantHdr(tenant))
		if tooLarge.code != http.StatusBadRequest {
			t.Fatalf("oversized encoded batch = %d, want 400", tooLarge.code)
		}
		valid := h.do("POST", path, editor, map[string]any{"count": 100, "seed": strings.Repeat("\"", 5180)}, tenantHdr(tenant))
		if valid.code != http.StatusOK {
			t.Fatalf("bounded escaped batch = %d %s", valid.code, valid.raw)
		}
		saved := h.do("POST", "/v1/m/sandbox/scenarios", editor, map[string]any{
			"name": strings.Repeat("\x00", 200), "description": strings.Repeat("\x00", 1024),
			"subject_kind": strings.Repeat("\x00", 200), "steps": valid.body["samples"],
		}, tenantHdr(tenant))
		if saved.code != http.StatusCreated || !reflect.DeepEqual(saved.body["steps"], valid.body["samples"]) {
			t.Fatalf("escaped batch save = %d, want 201 with unchanged steps", saved.code)
		}
	})
}
