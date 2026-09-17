// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"net/http"
	"sort"
	"strconv"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

// The published contract of the three run controls, checked against what the
// HANDLERS ACTUALLY ANSWER.
//
// ⛔ WHY A DRIFT-CLEAN GENERATOR IS NOT THIS. `openapi:check` and `sdk:check`
// compare the committed artifacts to the generator that produced them, so they
// are both green whenever the generator is self-consistent — including when it is
// self-consistently wrong. An independent review read the published document
// against the handlers and found exactly that: `/runs/{ref}/input` advertised 200
// while both of its success paths answer 202, and all three controls could answer
// 503 with an UNKNOWN verdict while none of them published 503. Nothing was red.
//
// So this test does not inspect the generator. It drives the real routes over the
// authenticated server, with the real store and an owned Codex child, and asks
// one question per response: is THIS status and THIS body something the published
// operation declares? A schema field that no reachable response matches would pass
// a generator test and fail here.

// runControlOperation is the published operation for one control route.
func runControlOperation(t *testing.T, doc map[string]any, pattern string) map[string]any {
	t.Helper()
	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		t.Fatal("the module document has no paths")
	}
	item, ok := paths["/v1/m/sessions"+pattern].(map[string]any)
	if !ok {
		t.Fatalf("the module document publishes no %s", pattern)
	}
	op, ok := item["post"].(map[string]any)
	if !ok {
		t.Fatalf("%s has no POST operation", pattern)
	}
	return op
}

// assertPublished proves the observed response is DECLARED and that its body
// satisfies the declared schema. The validation is deliberately bounded — object
// shape, required keys, closed-object membership, declared scalar types and
// enumerations — because the point is to catch a contract that describes a
// different response, not to reimplement JSON Schema.
func assertPublished(t *testing.T, what string, op map[string]any, got resp) {
	t.Helper()
	status := strconv.Itoa(got.code)
	responses, ok := op["responses"].(map[string]any)
	if !ok {
		t.Fatalf("%s: the operation publishes no responses at all", what)
	}
	declared, ok := responses[status].(map[string]any)
	if !ok {
		t.Fatalf("%s: the handler answered %s, which the operation does not publish (published: %v)",
			what, status, publishedStatuses(responses))
	}
	content, ok := declared["content"].(map[string]any)
	if !ok {
		t.Fatalf("%s: the published %s response has no content", what, status)
	}
	media, ok := content["application/json"].(map[string]any)
	if !ok {
		t.Fatalf("%s: the published %s response is not application/json", what, status)
	}
	schema, ok := media["schema"].(map[string]any)
	if !ok {
		t.Fatalf("%s: the published %s response has no schema", what, status)
	}
	if got.body == nil {
		t.Fatalf("%s: the handler answered %s with a body this test could not parse: %s",
			what, status, got.raw)
	}
	assertSatisfies(t, what+" "+status, schema, got.body)
}

func assertSatisfies(t *testing.T, what string, schema map[string]any, body map[string]any) {
	t.Helper()
	if schema["type"] != "object" {
		t.Fatalf("%s: the published schema is %v, not an object", what, schema["type"])
	}
	properties, _ := schema["properties"].(map[string]any)
	for _, key := range sortedSchemaList(schema["required"]) {
		if _, present := body[key]; !present {
			t.Errorf("%s: the published schema requires %q, and the real body has no such key: %v",
				what, key, sortedKeys(body))
		}
	}
	if schema["additionalProperties"] == false {
		for _, key := range sortedKeys(body) {
			if _, declared := properties[key]; !declared {
				t.Errorf("%s: the real body carries %q, which the CLOSED published schema does not declare",
					what, key)
			}
		}
	}
	for key, value := range body {
		declared, ok := properties[key].(map[string]any)
		if !ok {
			continue
		}
		switch declared["type"] {
		case "string":
			s, isString := value.(string)
			if !isString {
				t.Errorf("%s: %q is published as a string and the real body has %T", what, key, value)
				continue
			}
			if enum := sortedSchemaList(declared["enum"]); len(enum) > 0 && !listHas(enum, s) {
				t.Errorf("%s: %q is published as one of %v and the real body has %q", what, key, enum, s)
			}
		case "boolean":
			if _, isBool := value.(bool); !isBool {
				t.Errorf("%s: %q is published as a boolean and the real body has %T", what, key, value)
			}
		case "object":
			nested, isObject := value.(map[string]any)
			if !isObject {
				t.Errorf("%s: %q is published as an object and the real body has %T", what, key, value)
				continue
			}
			assertSatisfies(t, what+"."+key, declared, nested)
		}
	}
}

func publishedStatuses(responses map[string]any) []string {
	out := make([]string, 0, len(responses))
	for status := range responses {
		out = append(out, status)
	}
	sort.Strings(out)
	return out
}

func sortedKeys(body map[string]any) []string {
	out := make([]string, 0, len(body))
	for key := range body {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func listHas(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func sortedSchemaList(value any) []string {
	values, _ := value.([]any)
	out := make([]string, 0, len(values))
	for _, v := range values {
		s, _ := v.(string)
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// TestRunControlPublishedContractMatchesTheRealResponses is the whole finding in
// one test: every reachable answer of the three controls, compared to what the
// document promises about it.
func TestRunControlPublishedContractMatchesTheRealResponses(t *testing.T) {
	m, h, admin, tenant, prof := codexHTTPHarness(t, "run-control-contract")
	doc := api.ModuleOpenAPIDocument([]api.Module{m})
	input := runControlOperation(t, doc, "/runs/{ref}/input")
	interrupt := runControlOperation(t, doc, "/runs/{ref}/interrupt")
	stop := runControlOperation(t, doc, "/runs/{ref}/stop")

	// The false success, pinned from the handler's side: nothing this route can do
	// answers 200, so publishing 200 would be advertising an unreachable outcome.
	if responses, _ := input["responses"].(map[string]any); responses["200"] != nil {
		t.Error("POST /runs/{ref}/input publishes a 200 success it never returns")
	}

	t.Run("the three successful answers", func(t *testing.T) {
		setCodexFixture(t, prof, codexFixture{ThreadID: "thread-contract-ok", Account: "apikey"})
		runRef, live := codexHTTPRun(t, m, h, admin, tenant, prof)
		fence := bindRunToFreshWorkLease(t, m, h, tenant, runRef, live.claim.SID)
		base := "/v1/m/sessions/runs/" + runRef

		accepted := h.doJSON(http.MethodPost, base+"/input", admin, map[string]any{
			"text": "open a turn", "work_lease_fence": fence,
		}, tenantHdr(tenant))
		if accepted.code != http.StatusAccepted {
			t.Fatalf("fenced input = %d %s, want 202", accepted.code, accepted.raw)
		}
		assertPublished(t, "input success", input, accepted)

		interrupted := h.doJSON(http.MethodPost, base+"/interrupt", admin, map[string]any{
			"work_lease_fence": fence,
		}, tenantHdr(tenant))
		if interrupted.code != http.StatusOK {
			t.Fatalf("fenced interrupt = %d %s, want 200", interrupted.code, interrupted.raw)
		}
		assertPublished(t, "interrupt success", interrupt, interrupted)

		stopped := h.doJSON(http.MethodPost, base+"/stop", admin, map[string]any{
			"work_lease_fence": fence, "reason": "contract check",
		}, tenantHdr(tenant))
		if stopped.code != http.StatusOK {
			t.Fatalf("fenced stop = %d %s, want 200", stopped.code, stopped.raw)
		}
		assertPublished(t, "stop success", stop, stopped)
	})

	t.Run("the UNKNOWN answer all three can give", func(t *testing.T) {
		setCodexFixture(t, prof, codexFixture{ThreadID: "thread-contract-unknown", Account: "apikey"})
		runRef, live := codexHTTPRun(t, m, h, admin, tenant, prof)
		fence := bindRunToFreshWorkLease(t, m, h, tenant, runRef, live.claim.SID)
		base := "/v1/m/sessions/runs/" + runRef

		opened := h.doJSON(http.MethodPost, base+"/input", admin, map[string]any{
			"text": "open a turn", "work_lease_fence": fence,
		}, tenantHdr(tenant))
		if opened.code != http.StatusAccepted {
			t.Fatalf("positive fenced input = %d %s", opened.code, opened.raw)
		}
		// One member of the four-column K2 authority stamp removed: the reviewer's
		// reachable UNKNOWN, which is a real state a restore or a partial write can
		// leave behind — not a mood the test puts the handler in.
		if err := mutateRunForWorkTest(m, tenant, runRef, func(rec model.Record) {
			rec[colRunWorkDispatchKey] = nil
		}); err != nil {
			t.Fatalf("remove one K2 authority member: %v", err)
		}
		for _, control := range []struct {
			name string
			path string
			op   map[string]any
			body map[string]any
		}{
			{"input", "/input", input, map[string]any{"text": "x", "work_lease_fence": fence}},
			{"interrupt", "/interrupt", interrupt, map[string]any{"work_lease_fence": fence}},
			{"stop", "/stop", stop, map[string]any{"work_lease_fence": fence, "reason": "x"}},
		} {
			got := h.doJSON(http.MethodPost, base+control.path, admin, control.body, tenantHdr(tenant))
			if got.code != http.StatusServiceUnavailable {
				t.Fatalf("%s under a partial K2 stamp = %d %s, want 503", control.name, got.code, got.raw)
			}
			assertPublished(t, control.name+" UNKNOWN", control.op, got)
		}
		if !processRunning(live.proc.PID()) {
			t.Error("an UNKNOWN refusal ended the owned process")
		}
	})
}
