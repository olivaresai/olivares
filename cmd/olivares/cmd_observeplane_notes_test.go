// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

// Witnesses for eleven guards of the observe-and-report lane that were BLIND.
//
// Each one was found the same way: the guard was mutated into its opposite, the
// lot's whole suite was run, and NOTHING went red. A guard no test can see is
// not a weak guard — it is a guard that will be deleted, inverted or refactored
// away by someone who has no way to learn that it mattered, and the suite will
// certify the result. That is the failure mode the lot's own first mutation
// round found three of; this file closes the ones that survived a second and
// third round.
//
// The eleven, by what the mutant made the CLI do while the suite stayed green:
//
//   inventory summary   a capped scan stopped saying every count is a FLOOR
//   inventory summary   an unreported catalog printed no table and no reason
//   adoption (x5)       a capped aggregation stopped saying every figure is a FLOOR
//   observability       a count the engine OMITTED rendered as 0 ("none arrived")
//   accessmap graph     the ATTRIB column carried CONFIDENCE, showing an
//                       approximate attribution as firm — against a standing
//                       instruction in the engine's own DTO (dto.go:36-40)
//   accessmap graph     the human-readable ref was dropped for the opaque id
//   notify routes get   a disabled route stopped saying it cannot fire
//   lane-wide           a truncated page with NO cursor stopped warning that it
//                       is not the whole list
//   consoleviews        an over-cap params document was SENT instead of refused
//   health checks report a negative latency was SENT instead of refused
//   observability       a trace with no usable span_id rendered as an empty table
//
// The last three matter beyond their text: two are LOCAL REFUSALS, the half of
// this lane's exit contract that promises exit 2 at zero requests, and they had
// no test at all. So each of those carries a request counter AND a paired
// positive control, per the rule at the top of cmd_observeplane_test.go: without
// the control, "it refused" is also satisfied by a command that refuses
// everything.
//
// EVERY TEST HERE ASSERTS BOTH DIRECTIONS. A note that always prints is as
// useless as one that never does: "TRUNCATED" under a complete answer teaches an
// operator to ignore the word.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// observeRowContaining returns the first output line containing needle, so a
// column assertion is made against ONE row rather than against the whole screen
// (where a neighboring row can satisfy it by accident).
func observeRowContaining(t *testing.T, out, needle string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	t.Fatalf("no output row contains %q, got:\n%s", needle, out)
	return ""
}

// TestACappedScanIsAnnouncedAsAFloor is the witness for the lot's own headline
// class reached from the aggregate side: `--cursor` protects a LIST from being
// read short, and this protects a COUNT from being read as a total.
//
// It covers the six verbs whose engine sets `truncated`: inventory's summary and
// adoption's five views. Adoption is one shared helper with five call sites, and
// a call site is where it goes wrong — passing a constant false there is
// invisible to a test that only exercises the helper.
func TestACappedScanIsAnnouncedAsAFloor(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		// body is the capped answer; the same body with "truncated" removed is
		// the counterfactual.
		body string
		want string
	}{
		{"inventory-summary", []string{"inventory", "summary"},
			`{"total":7,"by_kind":{"agent":{"active":7,"stale":0,"total":7}},"by_source":{"otel":7},"truncated":%s}`,
			"FLOOR"},
		{"adoption-summary", []string{"adoption", "summary"},
			`{"developers":3,"teams":1,"analytics":{"totals":{"sessions":10}},"telemetry":{"totals":{"sessions":7}},
			  "boundary":{"claude_api_only":true},"truncated":%s}`,
			"FLOOR"},
		{"adoption-trend", []string{"adoption", "trend"},
			`{"lens":"telemetry","days":[{"day":"2026-08-01","totals":{"sessions":2}}],
			  "boundary":{"claude_api_only":true},"truncated":%s}`,
			"FLOOR"},
		{"adoption-teams", []string{"adoption", "teams"},
			`{"teams":[{"team":"core","totals":{"sessions":2}}],
			  "boundary":{"claude_api_only":true},"truncated":%s}`,
			"FLOOR"},
		{"adoption-discrepancy", []string{"adoption", "discrepancy"},
			`{"days":[{"day":"2026-08-01","material":true,"metrics":[{"name":"sessions","analytics":10,
			  "telemetry":2,"ratio":5,"direction":"analytics_high","material":true}]}],
			  "boundary":{"claude_api_only":true},"truncated":%s}`,
			"FLOOR"},
		{"adoption-developers", []string{"adoption", "developers"},
			`{"developers":[{"developer":"ana","totals":{"sessions":2}}],
			  "boundary":{"claude_api_only":true},"truncated":%s}`,
			"FLOOR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// CAPPED: the floor must be named.
			spy := newObserveSpy(t, http.StatusOK, strings.Replace(tc.body, "%s", "true", 1))
			out, _, err := execRoot(t, observeArgs(spy.srv.URL, tc.args...)...)
			if err != nil {
				t.Fatalf("a capped answer is still an answer and must exit 0, got %v", err)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("a capped scan must say every figure is a FLOOR, got:\n%s", out)
			}

			// COMPLETE: it must NOT. A warning printed under a complete answer
			// trains the operator to stop reading it.
			spy2 := newObserveSpy(t, http.StatusOK, strings.Replace(tc.body, "%s", "false", 1))
			out2, _, err := execRoot(t, observeArgs(spy2.srv.URL, tc.args...)...)
			if err != nil {
				t.Fatalf("a complete answer must exit 0, got %v", err)
			}
			if strings.Contains(out2, tc.want) {
				t.Errorf("a COMPLETE answer must not be announced as a floor, got:\n%s", out2)
			}
		})
	}
}

// TestInventorySummaryNamesAnUnreportedCatalog: "no rows" and "no source has
// ever reported" are different facts, and the second is the one that means the
// ingestion path is not wired. Printing an empty table says neither.
func TestInventorySummaryNamesAnUnreportedCatalog(t *testing.T) {
	t.Run("nothing reported", func(t *testing.T) {
		spy := newObserveSpy(t, http.StatusOK, `{"total":0,"by_kind":{},"by_source":{}}`)
		out, _, err := execRoot(t, observeArgs(spy.srv.URL, "inventory", "summary")...)
		if err != nil {
			t.Fatalf("an empty catalog must exit 0, got %v", err)
		}
		if !strings.Contains(out, "no signal source has reported an entity yet") {
			t.Errorf("an unreported catalog must say so, got:\n%s", out)
		}
	})
	t.Run("something reported", func(t *testing.T) {
		spy := newObserveSpy(t, http.StatusOK,
			`{"total":1,"by_kind":{"agent":{"active":1,"stale":0,"total":1}},"by_source":{"otel":1}}`)
		out, _, err := execRoot(t, observeArgs(spy.srv.URL, "inventory", "summary")...)
		if err != nil {
			t.Fatalf("verb failed: %v", err)
		}
		if strings.Contains(out, "no signal source has reported an entity yet") {
			t.Errorf("a catalog WITH a source must not claim there is none, got:\n%s", out)
		}
		if !strings.Contains(out, "otel") {
			t.Errorf("the reporting source must be listed, got:\n%s", out)
		}
	})
}

// TestAnOmittedCountIsNotRenderedAsZero: the engine OMITS records_total when no
// count is attributable to a standard, and sends 0 when the count really is
// zero. Collapsing the two says "nothing arrived through this standard" about a
// standard nobody measured.
func TestAnOmittedCountIsNotRenderedAsZero(t *testing.T) {
	spy := newObserveSpy(t, http.StatusOK, `{
	  "since":"2026-08-01T00:00:00Z","engine_scope":true,"sources":[],
	  "standards":[
	    {"id":"unattributed","direction":"in","version":"1","status":"available"},
	    {"id":"measured-zero","direction":"in","version":"1","status":"active","records_total":0}
	  ]}`)
	out, _, err := execRoot(t, observeArgs(spy.srv.URL, "observability", "ingestion-health")...)
	if err != nil {
		t.Fatalf("verb failed: %v", err)
	}
	unattributed := observeRowContaining(t, out, "unattributed")
	if !strings.Contains(unattributed, "-") {
		t.Errorf("an OMITTED count must render as '-', not as a number, got row:\n%s", unattributed)
	}
	if strings.Contains(unattributed, "0") {
		t.Errorf("an omitted count rendered as 0 claims nothing arrived, got row:\n%s", unattributed)
	}
	measured := observeRowContaining(t, out, "measured-zero")
	if !strings.Contains(measured, "0") {
		t.Errorf("a MEASURED zero must still render as 0, got row:\n%s", measured)
	}
}

// TestTheGraphRendersAttributionFirmnessNotConfidence guards the one column in
// this lane the engine attaches a standing instruction to: attribution_tier is
// how firm the origin→identity attribution is, and the DTO says in as many words
// that approximate/unknown must not be rendered as if it were firm
// (modules/access-map/dto.go:36-40). CONF measures something else entirely, so a
// row that shows confidence in both columns reports an approximate attribution
// as a high-confidence one.
func TestTheGraphRendersAttributionFirmnessNotConfidence(t *testing.T) {
	spy := newObserveSpy(t, http.StatusOK, `{"nodes":[],"edges":[{
	  "id":"e1","origin_kind":"agent","origin_id":"ag-7","origin_ref":"planner",
	  "resource_id":"res-1","resource_ref":"payments-db","mode":"rw",
	  "confidence":"high","attribution_tier":"approximate","signal_source":"otel",
	  "occurrence_count":3,"last_seen":"2026-08-01T00:00:00Z"}]}`)
	out, _, err := execRoot(t, observeArgs(spy.srv.URL, "accessmap", "graph")...)
	if err != nil {
		t.Fatalf("verb failed: %v", err)
	}
	row := observeRowContaining(t, out, "payments-db")
	if !strings.Contains(row, "approximate") {
		t.Errorf("the ATTRIB column must carry the attribution tier, got row:\n%s", row)
	}
	if strings.Count(row, "high") != 1 {
		t.Errorf("confidence must appear ONCE (in CONF): an approximate attribution shown as firm "+
			"is the misreading the engine's DTO forbids; got row:\n%s", row)
	}
}

// TestTheGraphPrefersTheHumanRefOverTheOpaqueID: both ends of an edge fall back
// to the id, and the fallback is the point — it must be a FALLBACK. A graph
// printed entirely in opaque ids is unreadable at the moment it is needed.
func TestTheGraphPrefersTheHumanRefOverTheOpaqueID(t *testing.T) {
	spy := newObserveSpy(t, http.StatusOK, `{"nodes":[],"edges":[
	  {"id":"e1","origin_kind":"agent","origin_id":"ag-7","origin_ref":"planner",
	   "resource_id":"res-1","resource_ref":"payments-db","mode":"rw","confidence":"high",
	   "attribution_tier":"firm","signal_source":"otel","occurrence_count":1,"last_seen":"t"},
	  {"id":"e2","origin_kind":"agent","origin_id":"ag-9",
	   "resource_id":"res-2","mode":"r","confidence":"low",
	   "attribution_tier":"unknown","signal_source":"otel","occurrence_count":1,"last_seen":"t"}]}`)
	out, _, err := execRoot(t, observeArgs(spy.srv.URL, "accessmap", "graph")...)
	if err != nil {
		t.Fatalf("verb failed: %v", err)
	}
	named := observeRowContaining(t, out, "planner")
	if strings.Contains(named, "ag-7") {
		t.Errorf("with a ref present the opaque id must not be what is shown, got row:\n%s", named)
	}
	// And the fallback still fires for the edge that has no ref, so this is not
	// satisfied by a renderer that prints nothing.
	unnamed := observeRowContaining(t, out, "ag-9")
	if !strings.Contains(unnamed, "res-2") {
		t.Errorf("with no ref the id must be shown rather than a blank cell, got row:\n%s", unnamed)
	}
}

// TestRouteGetSaysADisabledRouteCannotFire: `notify routes ls` prints a bare
// "NO" in a column, which is fine in a table; the single-route view is what an
// operator opens when a notification did not arrive, and there the answer to
// their actual question has to be in words.
func TestRouteGetSaysADisabledRouteCannotFire(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		spy := newObserveSpy(t, http.StatusOK,
			`{"id":"rt-1","name":"pager","enabled":false,"destination":"pd","match_types":[]}`)
		out, _, err := execRoot(t, observeArgs(spy.srv.URL, "notify", "routes", "get", "rt-1")...)
		if err != nil {
			t.Fatalf("verb failed: %v", err)
		}
		if !strings.Contains(out, "cannot fire") {
			t.Errorf("a disabled route must say it cannot fire, got:\n%s", out)
		}
	})
	t.Run("enabled", func(t *testing.T) {
		spy := newObserveSpy(t, http.StatusOK,
			`{"id":"rt-1","name":"pager","enabled":true,"destination":"pd","match_types":[]}`)
		out, _, err := execRoot(t, observeArgs(spy.srv.URL, "notify", "routes", "get", "rt-1")...)
		if err != nil {
			t.Fatalf("verb failed: %v", err)
		}
		if strings.Contains(out, "cannot fire") {
			t.Errorf("an ENABLED route must not be reported as unable to fire, got:\n%s", out)
		}
	})
}

// TestATruncatedPageWithoutACursorSaysItIsNotTheWholeList covers all three
// states of the lane's shared truncation note, because the middle one is the
// only one anything measured.
//
// has_more with NO cursor is the state that cannot be recovered from: there is
// more and the engine did not say where to continue. Saying nothing there is the
// worst of the three, since the operator's screen is indistinguishable from a
// complete answer.
func TestATruncatedPageWithoutACursorSaysItIsNotTheWholeList(t *testing.T) {
	const row = `{"kind":"agent","entity_id":"ag-7","name":"planner","status":"active",
	              "signal_sources":["otel"],"first_seen":"t","last_seen":"t","occurrence_count":1}`
	for _, tc := range []struct {
		name    string
		body    string
		want    []string
		notWant []string
	}{
		{"complete", `{"items":[` + row + `]}`,
			nil, []string{"more rows exist"}},
		{"more with a cursor", `{"items":[` + row + `],"has_more":true,"cursor":"PAGE-2"}`,
			[]string{"more rows exist", "--cursor PAGE-2"}, []string{"NOT the whole list"}},
		{"more with NO cursor", `{"items":[` + row + `],"has_more":true}`,
			[]string{"NOT the whole list"}, []string{"--cursor "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := newObserveSpy(t, http.StatusOK, tc.body)
			out, _, err := execRoot(t, observeArgs(spy.srv.URL, "inventory", "entities", "ls")...)
			if err != nil {
				t.Fatalf("verb failed: %v", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("want %q in the output, got:\n%s", w, out)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(out, w) {
					t.Errorf("did not want %q in the output, got:\n%s", w, out)
				}
			}
		})
	}
}

// TestOverCapParamsAreRefusedBeforeTheRequest and
// TestANegativeLatencyIsRefusedBeforeTheRequest are the two local refusals of
// this lane that had no witness at all. Both carry the request counter and a
// paired positive control, because a refusal with neither is also what a
// completely broken command looks like.
func TestOverCapParamsAreRefusedBeforeTheRequest(t *testing.T) {
	oversize := `{"a":"` + strings.Repeat("x", consoleViewMaxParamsBytes) + `"}`
	if len(oversize) <= consoleViewMaxParamsBytes {
		t.Fatalf("the fixture is %d bytes, which is not over the %d-byte cap it exists to exceed",
			len(oversize), consoleViewMaxParamsBytes)
	}

	t.Run("over the cap", func(t *testing.T) {
		spy := newObserveSpy(t, http.StatusOK, `{"id":"sv-1"}`)
		_, _, err := execRoot(t, observeArgs(spy.srv.URL,
			"consoleviews", "create", "--feature-id", "findings", "--name", "n", "--params", oversize)...)
		if err == nil {
			t.Fatal("a params document over the engine's cap must be refused")
		}
		if got := exitcode.From(err); got != exitcode.Usage {
			t.Errorf("exit = %d, want %d (usage): the caller's own argument is what is wrong", got, exitcode.Usage)
		}
		if n := spy.count(); n != 0 {
			t.Errorf("%d request(s) reached the wire; a locally decidable refusal costs zero", n)
		}
	})

	t.Run("at the cap", func(t *testing.T) {
		// PERMIT: the largest document the engine accepts still goes through, so
		// the refusal above is the bound and not a blanket no.
		// `{"a":"` + `"}` is 8 bytes of envelope, so the payload is cap-8.
		fits := `{"a":"` + strings.Repeat("x", consoleViewMaxParamsBytes-8) + `"}`
		if len(fits) != consoleViewMaxParamsBytes {
			t.Fatalf("the fixture is %d bytes, want exactly the %d-byte cap", len(fits), consoleViewMaxParamsBytes)
		}
		spy := newObserveSpy(t, http.StatusOK, `{"id":"sv-1"}`)
		if _, _, err := execRoot(t, observeArgs(spy.srv.URL,
			"consoleviews", "create", "--feature-id", "findings", "--name", "n", "--params", fits)...); err != nil {
			t.Fatalf("a params document AT the cap must be accepted, got %v", err)
		}
		if n := spy.count(); n != 1 {
			t.Fatalf("%d request(s) reached the wire, want 1", n)
		}
		if body := spy.last(t).body; !strings.Contains(body, strings.Repeat("x", 32)) {
			t.Error("the params document must reach the engine byte for byte")
		}
	})
}

func TestANegativeLatencyIsRefusedBeforeTheRequest(t *testing.T) {
	t.Run("negative", func(t *testing.T) {
		spy := newObserveSpy(t, http.StatusOK, `{"checks":[]}`)
		_, _, err := execRoot(t, observeArgs(spy.srv.URL,
			"health", "checks", "report", "chk-1", "--state", "healthy", "--latency=-1")...)
		if err == nil {
			t.Fatal("a negative observed latency is not a measurement and must be refused")
		}
		if got := exitcode.From(err); got != exitcode.Usage {
			t.Errorf("exit = %d, want %d (usage)", got, exitcode.Usage)
		}
		if n := spy.count(); n != 0 {
			t.Errorf("%d request(s) reached the wire; a locally decidable refusal costs zero", n)
		}
	})

	t.Run("zero and positive", func(t *testing.T) {
		// PERMIT, and zero explicitly: 0 ms is a legitimate reading, so the
		// guard must be `< 0` and not `<= 0`.
		for _, v := range []string{"--latency=0", "--latency=42"} {
			spy := newObserveSpy(t, http.StatusOK, `{"checks":[]}`)
			if _, _, err := execRoot(t, observeArgs(spy.srv.URL,
				"health", "checks", "report", "chk-1", "--state", "healthy", v)...); err != nil {
				t.Fatalf("%s must be accepted, got %v", v, err)
			}
			if n := spy.count(); n != 1 {
				t.Fatalf("%s: %d request(s) reached the wire, want 1", v, n)
			}
		}
	})
}

// TestATraceWithNoUsableSpanIDSaysSo: the trace exists and its window is real,
// but no event in it carried a span_id the engine could use. An empty span table
// under a populated header reads as "this trace did nothing".
func TestATraceWithNoUsableSpanIDSaysSo(t *testing.T) {
	t.Run("no usable span id", func(t *testing.T) {
		spy := newObserveSpy(t, http.StatusOK,
			`{"trace_id":"tr-1","started_at":"2026-08-01T00:00:00Z","duration_ms":5,"spans":[]}`)
		out, _, err := execRoot(t, observeArgs(spy.srv.URL, "observability", "traces", "get", "tr-1")...)
		if err != nil {
			t.Fatalf("verb failed: %v", err)
		}
		if !strings.Contains(out, "no span carried a usable span_id") {
			t.Errorf("a trace with no usable span_id must say so rather than show an empty table, got:\n%s", out)
		}
	})
	t.Run("with spans", func(t *testing.T) {
		spy := newObserveSpy(t, http.StatusOK,
			`{"trace_id":"tr-1","started_at":"2026-08-01T00:00:00Z","duration_ms":5,
			  "spans":[{"span_id":"sp-1","name":"plan","kind":"ledger","start_ms":0,"duration_ms":5}]}`)
		out, _, err := execRoot(t, observeArgs(spy.srv.URL, "observability", "traces", "get", "tr-1")...)
		if err != nil {
			t.Fatalf("verb failed: %v", err)
		}
		if strings.Contains(out, "no span carried a usable span_id") {
			t.Errorf("a trace WITH spans must not claim it has none, got:\n%s", out)
		}
		if !strings.Contains(out, "sp-1") {
			t.Errorf("the span must be listed, got:\n%s", out)
		}
	})
}
