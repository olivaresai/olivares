// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

// issueByDetail returns the first issue whose detailKey carries the given prefix.
func issueByDetail(issues []postureIssue, prefix string) (postureIssue, bool) {
	for _, is := range issues {
		if strings.HasPrefix(is.detailKey, prefix) {
			return is, true
		}
	}
	return postureIssue{}, false
}

func TestDeprecationSSETransportConfig(t *testing.T) {
	issues := deprecationIssues(serverSpec{Name: "legacy", Transport: transportSSE, URL: "https://legacy.example/mcp"}, catalog{})
	is, ok := issueByDetail(issues, "deprecated-transport-config")
	if !ok {
		t.Fatalf("an sse-typed spec must raise the deprecated HTTP+SSE transport issue, got %+v", issues)
	}
	if is.severity != model.SeverityMedium || is.mcp != "MCP04" {
		t.Errorf("HTTP+SSE config issue = %q/%q, want Medium/MCP04", is.severity, is.mcp)
	}
	if !strings.Contains(is.title, "SEP-2596") {
		t.Errorf("title should cite SEP-2596: %q", is.title)
	}
}

func TestDeprecationHTTPSSEEraRevision(t *testing.T) {
	cat := catalog{server: InitializeResult{ProtocolVersion: revision20241105}}
	// HTTP server on 2024-11-05: the HTTP+SSE-era inference fires.
	if _, ok := issueByDetail(deprecationIssues(serverSpec{Name: "s", URL: "https://x.example"}, cat), "deprecated-transport-era"); !ok {
		t.Errorf("an HTTP server negotiating 2024-11-05 must raise the HTTP+SSE-era issue")
	}
	// stdio server on 2024-11-05: merely stale (revision.go), NOT transport-deprecated.
	if _, ok := issueByDetail(deprecationIssues(serverSpec{Name: "s", Command: "srv"}, cat), "deprecated-transport-era"); ok {
		t.Errorf("a stdio server on 2024-11-05 must not be flagged as HTTP+SSE-era")
	}
}

func TestDeprecationAdvertisedCapabilities(t *testing.T) {
	cat := catalog{server: InitializeResult{Capabilities: map[string]any{
		"logging":  map[string]any{},
		"sampling": map[string]any{},
		"roots":    map[string]any{},
		"tools":    map[string]any{}, // not deprecated — must not flag
	}}}
	issues := deprecationIssues(serverSpec{Name: "s"}, cat)
	for _, key := range []string{"logging", "sampling", "roots"} {
		is, ok := issueByDetail(issues, "deprecated-capability key="+key)
		if !ok {
			t.Errorf("advertised %s capability must raise a deprecation issue", key)
			continue
		}
		if is.severity != model.SeverityLow || is.mcp != "MCP04" || !strings.Contains(is.title, "SEP-2577") {
			t.Errorf("%s issue = %+v, want Low/MCP04 citing SEP-2577", key, is)
		}
	}
	if _, ok := issueByDetail(issues, "deprecated-capability key=tools"); ok {
		t.Errorf("the tools capability is not deprecated and must not be flagged")
	}
}

func TestDeprecationDCRRegistration(t *testing.T) {
	// DCR while the AS advertises CIMD: migratable today → Medium.
	cat := catalog{authReg: &authRegistrationObservation{method: identityDCR, cimdSupported: true, registrationEndpoint: true}}
	is, ok := issueByDetail(deprecationIssues(serverSpec{Name: "s"}, cat), "dcr-despite-cimd")
	if !ok || is.severity != model.SeverityMedium {
		t.Errorf("DCR despite CIMD must be a Medium issue, got %+v ok=%v", is, ok)
	}
	// DCR-only AS: upstream debt → Low.
	cat = catalog{authReg: &authRegistrationObservation{method: identityDCR, registrationEndpoint: true}}
	is, ok = issueByDetail(deprecationIssues(serverSpec{Name: "s"}, cat), "dcr-only-as")
	if !ok || is.severity != model.SeverityLow {
		t.Errorf("a DCR-only AS must be a Low issue, got %+v ok=%v", is, ok)
	}
	// CIMD path taken: nothing deprecated.
	cat = catalog{authReg: &authRegistrationObservation{method: identityCIMD, cimdSupported: true}}
	if issues := registrationIssues(serverSpec{Name: "s"}, cat); len(issues) != 0 {
		t.Errorf("a CIMD registration must raise nothing, got %+v", issues)
	}
	// No runtime observation, DCR-only fleet config: declared intent → Low.
	spec := serverSpec{Name: "s", Auth: &serverAuth{DynamicRegistration: true}}
	if _, ok := issueByDetail(deprecationIssues(spec, catalog{}), "dcr-only-config"); !ok {
		t.Errorf("a DCR-only fleet config must raise the declared-intent issue")
	}
	// Config that ALSO carries CIMD material is not DCR-only.
	spec.Auth.ClientIDMetadataURL = "https://ops.example/client.json"
	if _, ok := issueByDetail(deprecationIssues(spec, catalog{}), "dcr-only-config"); ok {
		t.Errorf("a config with a CIMD URL must not be flagged DCR-only")
	}
	// Nor one with pre-registered credentials or an out-of-band bearer.
	spec = serverSpec{Name: "s", Auth: &serverAuth{DynamicRegistration: true, ClientID: "cid"}}
	if _, ok := issueByDetail(deprecationIssues(spec, catalog{}), "dcr-only-config"); ok {
		t.Errorf("a config with pre-registered credentials must not be flagged DCR-only")
	}
	spec = serverSpec{Name: "s", Auth: &serverAuth{DynamicRegistration: true, BearerToken: "tok"}}
	if _, ok := issueByDetail(deprecationIssues(spec, catalog{}), "dcr-only-config"); ok {
		t.Errorf("a config with an operator bearer must not be flagged DCR-only")
	}
	// A pre-registered runtime observation raises nothing.
	if issues := registrationIssues(serverSpec{Name: "s"}, catalog{authReg: &authRegistrationObservation{method: identityPreRegistered}}); len(issues) != 0 {
		t.Errorf("a pre-registered registration must raise nothing, got %+v", issues)
	}
}

func TestDeprecationObservedRequests(t *testing.T) {
	cat := catalog{observed: []serverRequestObservation{
		{method: methodSamplingCreate, includeContext: "allServers"},
		{method: methodRootsList},
		{method: methodElicitationCreate},
		{method: notifRootsListChanged},
	}}
	issues := deprecationIssues(serverSpec{Name: "s"}, cat)

	if is, ok := issueByDetail(issues, "observed-deprecated method="+methodSamplingCreate); !ok || is.severity != model.SeverityMedium {
		t.Errorf("observed sampling/createMessage must be Medium, got %+v ok=%v", is, ok)
	}
	if is, ok := issueByDetail(issues, "observed-includecontext value=allServers"); !ok || is.severity != model.SeverityHigh || is.mcp != "MCP10" {
		t.Errorf("includeContext=allServers must be High/MCP10, got %+v ok=%v", is, ok)
	}
	if is, ok := issueByDetail(issues, "observed-deprecated method="+methodRootsList); !ok || is.severity != model.SeverityMedium {
		t.Errorf("observed roots/list must be Medium, got %+v ok=%v", is, ok)
	}
	if is, ok := issueByDetail(issues, "observed-violation method="+methodElicitationCreate); !ok || is.severity != model.SeverityLow || is.mcp != "MCP10" {
		t.Errorf("observed elicitation/create must be Low/MCP10, got %+v ok=%v", is, ok)
	}
	if is, ok := issueByDetail(issues, "observed-deprecated method="+notifRootsListChanged); !ok || is.severity != model.SeverityLow {
		t.Errorf("observed roots/list_changed must be Low, got %+v ok=%v", is, ok)
	}

	// thisServer grades Medium, not High.
	cat = catalog{observed: []serverRequestObservation{{method: methodSamplingCreate, includeContext: "thisServer"}}}
	if is, ok := issueByDetail(deprecationIssues(serverSpec{Name: "s"}, cat), "observed-includecontext value=thisServer"); !ok || is.severity != model.SeverityMedium {
		t.Errorf("includeContext=thisServer must be Medium, got %+v ok=%v", is, ok)
	}

	// Two sampling observations with DISTINCT includeContext values count the
	// METHOD-level deprecation once; the per-value MCP10 issues stay per value.
	cat = catalog{observed: []serverRequestObservation{
		{method: methodSamplingCreate, includeContext: ""},
		{method: methodSamplingCreate, includeContext: "thisServer"},
	}}
	issues = deprecationIssues(serverSpec{Name: "s"}, cat)
	methodCount := 0
	for _, is := range issues {
		if is.detailKey == "observed-deprecated method="+methodSamplingCreate {
			methodCount++
		}
	}
	if methodCount != 1 {
		t.Errorf("the method-level sampling issue must count once across includeContext variants, got %d (%+v)", methodCount, issues)
	}
	if _, ok := issueByDetail(issues, "observed-includecontext value=thisServer"); !ok {
		t.Errorf("the per-value MCP10 issue must still fire, got %+v", issues)
	}
}

// TestDeprecationDegradesScore: deprecated reliance must drop the posture grade —
// the whole point of deprecation-aware posture.
func TestDeprecationDegradesScore(t *testing.T) {
	cat := catalog{
		server: InitializeResult{
			ServerInfo:   serverInfo{Name: "legacy", Version: "1.0.0"},
			Capabilities: map[string]any{"logging": map[string]any{}, "sampling": map[string]any{}},
		},
		observed: []serverRequestObservation{{method: methodSamplingCreate, includeContext: "allServers"}},
	}
	fs := postureFindings(serverSpec{Name: "legacy", Transport: transportSSE, URL: "https://x"}, cat, nil, fixedTime())
	sc := scoreFinding(t, fs)
	if strings.Contains(sc.Title, "grade A") {
		t.Errorf("a deprecation-laden server must not grade A: %q", sc.Title)
	}
	if sc.Severity != model.SeverityHigh { // worst issue = includeContext=allServers (High)
		t.Errorf("score severity should reflect the worst (High) issue, got %q", sc.Severity)
	}
	if _, ok := findByTag(fs, "MCP04"); !ok {
		t.Errorf("deprecation findings must carry the MCP04 tag: %s", titles(fs))
	}
}

// TestObserverRecordsAndBounds: the requestObserver records only allow-listed
// server-initiated methods, dedups, parses includeContext, and ignores responses.
func TestObserverRecordsAndBounds(t *testing.T) {
	var o requestObserver
	id := int64(7)
	o.observe(rpcMessage{ID: &id})                      // a response — ignored
	o.observe(rpcMessage{Method: "tools/list_changed"}) // not allow-listed — ignored
	o.observe(rpcMessage{Method: methodSamplingCreate, Params: []byte(`{"includeContext":"allServers"}`)})
	o.observe(rpcMessage{Method: methodSamplingCreate, Params: []byte(`{"includeContext":"allServers"}`)}) // dup
	o.observe(rpcMessage{Method: methodSamplingCreate, Params: []byte(`{"includeContext":"bogus"}`)})      // unrecognized enum → ""
	o.observe(rpcMessage{Method: methodRootsList})

	obs := o.observations()
	if len(obs) != 3 {
		t.Fatalf("want 3 deduplicated observations, got %d: %+v", len(obs), obs)
	}
	if obs[0].includeContext != "allServers" {
		t.Errorf("includeContext should parse the closed enum, got %q", obs[0].includeContext)
	}
	if obs[1].includeContext != "" {
		t.Errorf("an unrecognized includeContext must be recorded as empty, got %q", obs[1].includeContext)
	}
}

// TestSSEStreamObservation: a server-initiated request interleaved on a response
// SSE stream is observed while the response is still resolved correctly.
func TestSSEStreamObservation(t *testing.T) {
	stream := "data: {\"jsonrpc\":\"2.0\",\"id\":99,\"method\":\"sampling/createMessage\",\"params\":{\"includeContext\":\"thisServer\"}}\n\n" +
		"data: {\"jsonrpc\":\"2.0\",\"id\":9,\"result\":{\"ok\":true}}\n\n"
	var o requestObserver
	res, err := responseFromSSE(strings.NewReader(stream), 9, &o)
	if err != nil || res == nil {
		t.Fatalf("responseFromSSE: %v (%s)", err, res)
	}
	obs := o.observations()
	if len(obs) != 1 || obs[0].method != methodSamplingCreate || obs[0].includeContext != "thisServer" {
		t.Errorf("the interleaved sampling request must be observed, got %+v", obs)
	}
}

// --- deprecated-features feed (drift detection) -------------------------------

// feedFixture mirrors the live registry MDX structure (verified 2026-06-10).
const feedFixture = `---
title: Deprecated Features
---

## Deprecated

| Feature | Deprecation SEP | Deprecated in | Migration path | Earliest removal |
| ------- | --------------- | ------------- | -------------- | ---------------- |
| [Roots](/specification/draft/client/roots) | [SEP-2577](https://github.com/x/pull/2577) | ` + "`2026-07-28`" + ` | Tool parameters | First revision released on or after 2027-07-28 |
| [Sampling](/specification/draft/client/sampling) | [SEP-2577](https://github.com/x/pull/2577) | ` + "`2026-07-28`" + ` | Direct LLM APIs | First revision released on or after 2027-07-28 |
| [Logging](/specification/draft/server/utilities/logging) | [SEP-2577](https://github.com/x/pull/2577) | ` + "`2026-07-28`" + ` | stderr / OTel | First revision released on or after 2027-07-28 |
| [HTTP+SSE transport](/specification/2024-11-05/basic/transports#http-with-sse) | [SEP-2596](https://github.com/x/pull/2596) | ` + "`2025-03-26`" + ` | Streamable HTTP | Three months after SEP-2596 reaches Final |
| ` + "`includeContext: \"thisServer\"` / `\"allServers\"`" + ` ([Sampling](/spec)) | [SEP-2596](https://github.com/x/pull/2596) | ` + "`2025-11-25`" + ` | Omit or "none" | Follows Sampling |
| [Dynamic Client Registration](/specification/draft/basic/authorization) | [PR #2858](https://github.com/x/pull/2858) | ` + "`2026-07-28`" + ` | CIMD | First revision released on or after 2027-07-28 |

## Removed

No features have been removed under this policy yet.
`

func TestParseDeprecationRegistryMatchesKnownRules(t *testing.T) {
	deprecated, removed := parseDeprecationRegistry(feedFixture)
	if len(deprecated) != 6 {
		t.Fatalf("want 6 Deprecated rows, got %d: %v", len(deprecated), deprecated)
	}
	if len(removed) != 0 {
		t.Errorf("want 0 Removed rows, got %v", removed)
	}
	for _, feature := range deprecated {
		if _, known := knownDeprecation(feature); !known {
			t.Errorf("live registry row %q has no compiled rule — knownDeprecations is stale", feature)
		}
	}
	// The includeContext row must match its own rule EXACTLY, not Sampling's.
	d, ok := knownDeprecation(normalizeFeatureCell("`includeContext: \"thisServer\"` / `\"allServers\"` ([Sampling](/x))"))
	if !ok || !strings.HasPrefix(d.token, "includecontext") {
		t.Errorf("includeContext row matched %q ok=%v — exact matching broken", d.token, ok)
	}
	// Matching is EXACT: a NEW upstream row that merely mentions a known feature
	// name must NOT be swallowed as known (the drift detector's whole purpose).
	if _, ok := knownDeprecation(normalizeFeatureCell("[Sampling preferences](/spec/sampling-preferences)")); ok {
		t.Errorf("a new feature mentioning a known name must be UNKNOWN to the drift detector")
	}
}

// TestObserverCapBound: the dedup over the closed key space is the real memory
// bound; the cap is defense-in-depth that must hold if the allow-list widens.
func TestObserverCapBound(t *testing.T) {
	var o requestObserver
	o.seen = map[string]struct{}{}
	for i := 0; i < maxObservedRequests; i++ {
		key := string(rune('a'+i)) + "|"
		o.seen[key] = struct{}{}
		o.obs = append(o.obs, serverRequestObservation{method: string(rune('a' + i))})
	}
	o.observe(rpcMessage{Method: methodRootsList}) // unique key, but the cap is reached
	if got := len(o.observations()); got != maxObservedRequests {
		t.Errorf("the cap must drop the %dth unique observation, got %d", maxObservedRequests+1, got)
	}
}

func deprecationFeedSource(t *testing.T, body string, status int) *Source {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &Source{cfg: config{deprecationFeed: true, deprecationFeedURL: srv.URL, timeout: defaultTimeout}}
}

func TestDeprecationFeedDriftDetection(t *testing.T) {
	// In-sync feed: no drift findings.
	s := deprecationFeedSource(t, feedFixture, http.StatusOK)
	if fs := s.deprecationFeedFindings(t.Context(), fixedTime()); len(fs) != 0 {
		t.Errorf("an in-sync feed must yield no drift findings, got %s", titles(fs))
	}

	// A NEW deprecated feature → one Info drift finding (sanitized name).
	withNew := strings.Replace(feedFixture, "## Removed",
		"| [Completions](/spec/completions) | [SEP-9999](https://github.com/x/pull/9999) | `2027-01-01` | n/a | 2028-01-01 |\n\n## Removed", 1)
	s = deprecationFeedSource(t, withNew, http.StatusOK)
	fs := s.deprecationFeedFindings(t.Context(), fixedTime())
	if len(fs) != 1 || fs[0].Severity != model.SeverityInfo || !strings.Contains(fs[0].Title, "no posture rule") {
		t.Errorf("a new upstream deprecation must yield one Info drift finding, got %s", titles(fs))
	}

	// A known feature moved to Removed → Low staleness finding.
	withRemoved := strings.Replace(feedFixture, "No features have been removed under this policy yet.",
		"| [HTTP+SSE transport](/spec) | [SEP-2596](https://github.com/x/pull/2596) | removal entry |\n", 1)
	s = deprecationFeedSource(t, withRemoved, http.StatusOK)
	fs = s.deprecationFeedFindings(t.Context(), fixedTime())
	if len(fs) != 1 || fs[0].Severity != model.SeverityLow || !strings.Contains(fs[0].Title, "REMOVED") {
		t.Errorf("a known feature moved to Removed must yield one Low staleness finding, got %s", titles(fs))
	}

	// Unreachable feed → one Info unavailable finding (rules unaffected).
	s = deprecationFeedSource(t, "nope", http.StatusInternalServerError)
	fs = s.deprecationFeedFindings(t.Context(), fixedTime())
	if len(fs) != 1 || fs[0].Severity != model.SeverityInfo || !strings.Contains(fs[0].Title, "unavailable") {
		t.Errorf("a failing feed must degrade to one Info finding, got %s", titles(fs))
	}

	// A page that restructured (no rows parse) is unavailability, not "all clear".
	s = deprecationFeedSource(t, "totally different page", http.StatusOK)
	fs = s.deprecationFeedFindings(t.Context(), fixedTime())
	if len(fs) != 1 || !strings.Contains(fs[0].Title, "unavailable") {
		t.Errorf("a restructured page must degrade to unavailability, got %s", titles(fs))
	}

	// Feed disabled: no network, no findings.
	off := &Source{cfg: config{}}
	if fs := off.deprecationFeedFindings(t.Context(), fixedTime()); fs != nil {
		t.Errorf("a disabled feed must emit nothing, got %s", titles(fs))
	}
}
