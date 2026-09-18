// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/modules/knowledge"
)

// mcpgateway_revisionmode_test.go — the composed gateway states its MCP
// revision posture EXPLICITLY, without moving any default.
//
// Every case here runs through the REAL operator-config path — a JSON document on
// disk, named by OLIVARES_AGENT_GATEWAY_CONFIG and read by loadAgentGatewayConfig
// — and then through the production composition builder. A table proved only
// against the pure resolver would not establish the one structural fact this cut
// rests on: that the decode records whether the operator wrote a key at all.
//
// Nothing here is an interoperability claim. The dispatch assertions describe what
// THIS Resource Server does with a request; no counterparty is contacted.

const (
	mcpRevisionFinal  = "2026-07-28"
	mcpRevisionLegacy = "2025-11-25"

	// The wire codes these tests pin, from connectors/mcp/jsonrpc.go: a method
	// that does not exist in the legacy protocol (-32601) is a DIFFERENT answer
	// from a seam that is not wired (-31010) and from a revision this server does
	// not accept in the configured mode (-32022). Collapsing them would hide the
	// very distinction the read-back exists to make.
	rpcMethodNotFound            = -32601
	rpcEvidenceUnavailableCode   = -31010
	rpcUnsupportedProtocolCode   = -32022
	mcpGatewayReadBackMsgPrefix  = "mcp gateway: effective configuration read-back"
	mcpGatewayNotMountedLogEvent = "PROVISIONED BUT NOT MOUNTED"
)

// syncBuffer is a goroutine-safe log sink: the read-back is emitted on the
// building goroutine while the httptest server writes audit lines from another,
// and a bare bytes.Buffer would make the -race detector right.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func captureGatewayLogger() (*slog.Logger, *syncBuffer) {
	sink := &syncBuffer{}
	return slog.New(slog.NewJSONHandler(sink, &slog.HandlerOptions{Level: slog.LevelInfo})), sink
}

// gatewayDocWithRevisionFields renders a complete MCP operator document with the
// two revision keys supplied VERBATIM, so a case can express "the key is absent"
// as something different from "the key is present and false".
func gatewayDocWithRevisionFields(jwks []byte, revisionFields, extraFields string) string {
	doc := `{"mcp":{` +
		`"resource":"` + mcpReviewResource + `",` +
		`"authorization_servers":["https://auth.review.example"],` +
		`"issuer":"https://auth.review.example",` +
		`"issuer_jwks":` + string(jwks) + `,` +
		`"tools":[{"name":"search","required_scope":"tools:read"}]`
	for _, extra := range []string{revisionFields, extraFields} {
		if strings.TrimSpace(extra) != "" {
			doc += "," + extra
		}
	}
	return doc + `}}`
}

// loadGatewayMCPConfig writes the document to disk and reads it back through the
// production loader, returning the decoded MCP block.
func loadGatewayMCPConfig(t *testing.T, doc string) *mcpGatewayConfig {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent-gateway.json")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("write operator config: %v", err)
	}
	t.Setenv("OLIVARES_AGENT_GATEWAY_CONFIG", path)
	cfg, err := loadAgentGatewayConfig(discardLogger())
	if err != nil {
		t.Fatalf("load operator config: %v", err)
	}
	if cfg.MCP == nil {
		t.Fatalf("operator document decoded without an mcp block: %s", doc)
	}
	return cfg.MCP
}

// mcpListenRequest is a subscriptions/listen request that DECLARES version, in
// both the header and the body _meta the RC path validates.
func mcpListenRequest(token, version, target string) *http.Request {
	body := `{"jsonrpc":"2.0","id":7,"method":"subscriptions/listen","params":{"notifications":{"toolsListChanged":true},` +
		`"_meta":{"io.modelcontextprotocol/protocolVersion":"` + version + `",` +
		`"io.modelcontextprotocol/clientInfo":{"name":"revision-mode-test","version":"1"},` +
		`"io.modelcontextprotocol/clientCapabilities":{}}}}`
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("MCP-Protocol-Version", version)
	req.Header.Set("Mcp-Method", mcpc.SubscriptionListenMethod)
	return req
}

// mcpRPCErrorCode reads the JSON-RPC error code out of a response body.
func mcpRPCErrorCode(t *testing.T, body string) int {
	t.Helper()
	var envelope struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("decode JSON-RPC envelope %q: %v", body, err)
	}
	if envelope.Error == nil {
		t.Fatalf("response carries no JSON-RPC error: %s", body)
	}
	return envelope.Error.Code
}

// TestMCPGatewayRevisionModeResolutionTable is the full adjudicated table,
// including the two implicit rows (neither field, and next_revision_headers
// present-and-false) that only exist because the decode records presence.
func TestMCPGatewayRevisionModeResolutionTable(t *testing.T) {
	_, jwks := mintReviewToken(t, mcpReviewResource, "tools:read")

	cases := []struct {
		name     string
		fields   string
		wantMode string
		wantErr  []string
	}{
		// The default did not move: neither field, and the gateway is legacy.
		{name: "neither field", fields: "", wantMode: mcpGatewayRevisionModeLegacy},
		{name: "boolean explicit false", fields: `"next_revision_headers":false`, wantMode: mcpGatewayRevisionModeLegacy},
		{name: "boolean true is dual not rc-strict", fields: `"next_revision_headers":true`, wantMode: mcpGatewayRevisionModeDual},
		// An explicit mode with no boolean at all.
		{name: "explicit legacy", fields: `"revision_mode":"legacy"`, wantMode: mcpGatewayRevisionModeLegacy},
		{name: "explicit dual", fields: `"revision_mode":"dual"`, wantMode: mcpGatewayRevisionModeDual},
		{name: "explicit rc-strict", fields: `"revision_mode":"rc-strict"`, wantMode: mcpGatewayRevisionModeRCStrict},
		// An explicit mode with a boolean that AGREES with its header posture.
		{name: "legacy with false", fields: `"revision_mode":"legacy","next_revision_headers":false`, wantMode: mcpGatewayRevisionModeLegacy},
		{name: "dual with true", fields: `"revision_mode":"dual","next_revision_headers":true`, wantMode: mcpGatewayRevisionModeDual},
		{name: "rc-strict with true", fields: `"revision_mode":"rc-strict","next_revision_headers":true`, wantMode: mcpGatewayRevisionModeRCStrict},
		// An explicit mode with a boolean that CONTRADICTS it: refused, naming both.
		{name: "legacy with true", fields: `"revision_mode":"legacy","next_revision_headers":true`,
			wantErr: []string{"revision_mode", "next_revision_headers", "contradict"}},
		{name: "dual with false", fields: `"revision_mode":"dual","next_revision_headers":false`,
			wantErr: []string{"revision_mode", "next_revision_headers", "contradict"}},
		{name: "rc-strict with false", fields: `"revision_mode":"rc-strict","next_revision_headers":false`,
			wantErr: []string{"revision_mode", "next_revision_headers", "contradict"}},
		// Invalid explicit values, each naming what IS accepted.
		{name: "unknown value", fields: `"revision_mode":"surprise"`,
			wantErr: []string{`unknown revision_mode "surprise"`, "legacy, dual, rc-strict"}},
		{name: "empty value", fields: `"revision_mode":""`,
			wantErr: []string{"present but empty", "legacy, dual, rc-strict"}},
		{name: "whitespace-only value", fields: `"revision_mode":"   "`,
			wantErr: []string{"present but empty", "legacy, dual, rc-strict"}},
		{name: "null value", fields: `"revision_mode":null`,
			wantErr: []string{"present but empty", "legacy, dual, rc-strict"}},
		// A null BOOLEAN is not a supplied posture: this resolves exactly as the
		// field's absence always did, because changing it would move behaviour
		// for an existing field.
		{name: "null boolean is not a supplied posture", fields: `"next_revision_headers":null`,
			wantMode: mcpGatewayRevisionModeLegacy},
		{name: "null boolean does not contradict a mode", fields: `"revision_mode":"dual","next_revision_headers":null`,
			wantMode: mcpGatewayRevisionModeDual},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := loadGatewayMCPConfig(t, gatewayDocWithRevisionFields(jwks, tc.fields, ""))
			rs, _, err := buildMCPResourceServer(&engine{log: discardLogger()}, cfg, discardLogger())
			if len(tc.wantErr) > 0 {
				if err == nil || rs != nil {
					t.Fatalf("construction = (%v, %v), want refusal before serving", rs, err)
				}
				for _, want := range tc.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("refusal %q does not name %q", err.Error(), want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("construction: %v", err)
			}
			if got := rs.RevisionMode(); got != tc.wantMode {
				t.Fatalf("resolved revision mode = %q, want %q", got, tc.wantMode)
			}
		})
	}
}

// TestMCPGatewayRevisionModeCausalDispatch proves the resolved mode is the mode
// the served surface ENFORCES, with three signatures that cannot be confused:
// legacy answers -32601 for a method the legacy protocol does not have, dual
// admits the final revision and still routes a legacy-dated request down the
// legacy path, and rc-strict refuses the legacy-dated request outright.
func TestMCPGatewayRevisionModeCausalDispatch(t *testing.T) {
	token, jwks := mintReviewToken(t, mcpReviewResource, "tools:read")

	cases := []struct {
		name       string
		fields     string
		wantMode   string
		finalCode  int // subscriptions/listen declaring 2026-07-28
		finalHTTP  int
		legacyCode int // the same request declaring 2025-11-25
		legacyHTTP int
	}{
		{
			name: "neither field stays legacy", fields: "", wantMode: mcpGatewayRevisionModeLegacy,
			finalCode: rpcMethodNotFound, finalHTTP: http.StatusNotFound,
			legacyCode: rpcMethodNotFound, legacyHTTP: http.StatusNotFound,
		},
		{
			name: "boolean true is dual", fields: `"next_revision_headers":true`, wantMode: mcpGatewayRevisionModeDual,
			finalCode: rpcEvidenceUnavailableCode, finalHTTP: http.StatusServiceUnavailable,
			legacyCode: rpcMethodNotFound, legacyHTTP: http.StatusNotFound,
		},
		{
			name: "explicit dual matches the boolean", fields: `"revision_mode":"dual"`, wantMode: mcpGatewayRevisionModeDual,
			finalCode: rpcEvidenceUnavailableCode, finalHTTP: http.StatusServiceUnavailable,
			legacyCode: rpcMethodNotFound, legacyHTTP: http.StatusNotFound,
		},
		{
			name: "explicit rc-strict reaches the Resource Server", fields: `"revision_mode":"rc-strict"`, wantMode: mcpGatewayRevisionModeRCStrict,
			finalCode: rpcEvidenceUnavailableCode, finalHTTP: http.StatusServiceUnavailable,
			legacyCode: rpcUnsupportedProtocolCode, legacyHTTP: http.StatusBadRequest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := loadGatewayMCPConfig(t, gatewayDocWithRevisionFields(jwks, tc.fields, ""))
			rs, _, err := buildMCPResourceServer(&engine{log: discardLogger()}, cfg, discardLogger())
			if err != nil {
				t.Fatalf("construction: %v", err)
			}
			if got := rs.RevisionMode(); got != tc.wantMode {
				t.Fatalf("resolved revision mode = %q, want %q", got, tc.wantMode)
			}
			for _, probe := range []struct {
				version  string
				wantHTTP int
				wantCode int
			}{
				{mcpRevisionFinal, tc.finalHTTP, tc.finalCode},
				{mcpRevisionLegacy, tc.legacyHTTP, tc.legacyCode},
			} {
				w := httptest.NewRecorder()
				rs.ServeHTTP(w, mcpListenRequest(token, probe.version, mcpReviewResource))
				if w.Code != probe.wantHTTP {
					t.Fatalf("%s request: status %d, want %d (body %s)", probe.version, w.Code, probe.wantHTTP, w.Body.String())
				}
				if got := mcpRPCErrorCode(t, w.Body.String()); got != probe.wantCode {
					t.Fatalf("%s request: JSON-RPC code %d, want %d (body %s)", probe.version, got, probe.wantCode, w.Body.String())
				}
			}
		})
	}
}

// TestMCPGatewayRetrievalCompositionReportsNoSubscriptionUpstream is the one real
// local HTTP composition: the in-process governed retrieval upstream, served over
// a loopback listener, has NO subscription upstream, and both the wire answer and
// the read-back say so instead of implying readiness.
func TestMCPGatewayRetrievalCompositionReportsNoSubscriptionUpstream(t *testing.T) {
	sessionsModule, st, tenant := newSessionsStore(t)
	token, jwks := mintReviewToken(t, mcpReviewResource, "tools:read")
	doc := gatewayDocWithRevisionFields(jwks, `"revision_mode":"dual"`,
		`"tenant":"`+tenant.String()+`","retrieval":{"enabled":true}`)
	cfg := loadGatewayMCPConfig(t, doc)

	log, sink := captureGatewayLogger()
	eng := &engine{store: st, sessionsMod: sessionsModule, knowledgeMod: knowledge.New(), log: log}
	rs, _, err := buildMCPResourceServer(eng, cfg, log)
	if err != nil {
		t.Fatalf("construction: %v", err)
	}

	srv := httptest.NewServer(rs)
	defer srv.Close()
	req := mcpListenRequest(token, mcpRevisionFinal, srv.URL+"/mcp")
	live, err := http.NewRequest(req.Method, srv.URL+"/mcp", req.Body)
	if err != nil {
		t.Fatalf("build live request: %v", err)
	}
	live.Header = req.Header.Clone()
	resp, err := srv.Client().Do(live)
	if err != nil {
		t.Fatalf("live subscriptions/listen: %v", err)
	}
	defer resp.Body.Close()
	var body bytes.Buffer
	if _, err := body.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read live response: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("live status = %d, want 503 (body %s)", resp.StatusCode, body.String())
	}
	if got := mcpRPCErrorCode(t, body.String()); got != rpcEvidenceUnavailableCode {
		t.Fatalf("live JSON-RPC code = %d, want %d (body %s)", got, rpcEvidenceUnavailableCode, body.String())
	}

	record := mcpGatewayReadBack(t, sink.String())
	want := map[string]string{
		"revision_mode":         mcpGatewayRevisionModeDual,
		"revision_mode_source":  "revision_mode",
		"upstream":              "in-process:governed-retrieval",
		"subscription_upstream": "absent",
		"subscription_ledger":   "absent",
		"durable_task_store":    "absent",
	}
	for key, value := range want {
		if got, _ := record[key].(string); got != value {
			t.Fatalf("read-back %s = %v, want %q (record %v)", key, record[key], value, record)
		}
	}
	// The read-back must not fabricate a readiness word about a seam it only
	// configured. "configured"/"absent" are the whole vocabulary.
	for _, forbidden := range []string{"ready", "available", "healthy", "verified", "compatible"} {
		for key, value := range record {
			if text, ok := value.(string); ok && strings.Contains(strings.ToLower(text), forbidden) &&
				key != "msg" {
				t.Fatalf("read-back %s = %q claims %q about a configured seam", key, text, forbidden)
			}
		}
	}
}

// TestMCPGatewayReadBackNamesTheUpstreamKindNotItsSecrets: a forwarding upstream
// is reported by KIND. The configured URL can carry userinfo credentials and the
// upstream credential is a secret, so neither may reach a log line.
func TestMCPGatewayReadBackNamesTheUpstreamKindNotItsSecrets(t *testing.T) {
	const secretURL = "https://gateway-user:pa55phrase@upstream.invalid/mcp"
	const secretAuth = "Bearer upstream-only-credential"
	_, jwks := mintReviewToken(t, mcpReviewResource, "tools:read")
	doc := gatewayDocWithRevisionFields(jwks, `"next_revision_headers":true`,
		`"upstream_url":"`+secretURL+`","upstream_auth":"`+secretAuth+`"`)
	cfg := loadGatewayMCPConfig(t, doc)

	log, sink := captureGatewayLogger()
	if _, _, err := buildMCPResourceServer(&engine{log: log}, cfg, log); err != nil {
		t.Fatalf("construction: %v", err)
	}
	record := mcpGatewayReadBack(t, sink.String())
	if got, _ := record["upstream"].(string); got != "https-forward" {
		t.Fatalf("read-back upstream = %v, want the kind alone", record["upstream"])
	}
	if got, _ := record["subscription_upstream"].(string); got != "configured" {
		t.Fatalf("read-back subscription_upstream = %v, want configured", record["subscription_upstream"])
	}
	if got, _ := record["revision_mode_source"].(string); got != "next_revision_headers" {
		t.Fatalf("read-back revision_mode_source = %v, want next_revision_headers", record["revision_mode_source"])
	}
	for _, secret := range []string{"pa55phrase", "upstream-only-credential", "upstream.invalid"} {
		if strings.Contains(sink.String(), secret) {
			t.Fatalf("the gateway log carries %q", secret)
		}
	}
}

// TestMCPGatewayContradictoryRevisionConfigNeverListens takes the refusal all the
// way out to the caller that would create the socket: a contradictory pair leaves
// the agent gateway with nothing mounted and no HTTP server at all.
func TestMCPGatewayContradictoryRevisionConfigNeverListens(t *testing.T) {
	_, jwks := mintReviewToken(t, mcpReviewResource, "tools:read")
	doc := gatewayDocWithRevisionFields(jwks, `"revision_mode":"legacy","next_revision_headers":true`, "")
	path := filepath.Join(t.TempDir(), "agent-gateway.json")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("write operator config: %v", err)
	}
	t.Setenv("OLIVARES_AGENT_GATEWAY_CONFIG", path)

	log, sink := captureGatewayLogger()
	srv, err := buildAgentGatewayServer(&engine{log: log}, log)
	if err != nil {
		t.Fatalf("buildAgentGatewayServer = %v, want the deny-closed not-mounted path", err)
	}
	if srv != nil {
		t.Fatalf("a contradictory revision pair produced a listener: %#v", srv.Addr)
	}
	out := sink.String()
	if !strings.Contains(out, mcpGatewayNotMountedLogEvent) {
		t.Fatalf("the refusal was not reported as a deny-closed mount failure: %s", out)
	}
	for _, want := range []string{"revision_mode", "next_revision_headers", "contradict"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the mount failure does not name %q: %s", want, out)
		}
	}
	if strings.Contains(out, mcpGatewayReadBackMsgPrefix) {
		t.Fatalf("a refused gateway emitted a configuration read-back: %s", out)
	}
}

// mcpGatewayReadBack finds the single effective-configuration record in a JSON log
// stream. More than one would mean the composition emitted a posture twice.
func mcpGatewayReadBack(t *testing.T, out string) map[string]any {
	t.Helper()
	var found []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		record := map[string]any{}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		if msg, _ := record["msg"].(string); strings.HasPrefix(msg, mcpGatewayReadBackMsgPrefix) {
			found = append(found, record)
		}
	}
	if len(found) != 1 {
		t.Fatalf("effective-configuration records = %d, want exactly 1 (log %s)", len(found), out)
	}
	if msg, _ := found[0]["msg"].(string); !strings.Contains(msg, "not a readiness, conformance or interoperability claim") {
		t.Fatalf("the read-back does not say what it is not: %q", msg)
	}
	return found[0]
}

// TestMCPGatewayRevisionControlsPresenceMatchesValue is an independent review's correction:
// the PRESENCE of the two revision controls is derived from the same occurrence
// as their VALUE, so the two can no longer disagree.
//
// The defect this covers was a split reading of one document. The struct decode
// matches member names case-insensitively (encoding/json), while the presence
// scan looked up two lowercase names in a map, so `"REVISION_MODE":null` decoded
// to a value the presence flags never saw — an explicitly nulled mode read back
// as "the operator never mentioned it" — and `"NEXT_REVISION_HEADERS":false`
// contradicted an explicit rc-strict without the contradiction rule ever firing.
// A control written twice was resolved by "last one wins", across two different
// readings at that: the struct kept the last NON-null value while the map kept
// the last RAW one, so `"next_revision_headers":true` followed by null resolved
// as a true value with its presence withdrawn.
//
// Each case runs the REAL loader — a JSON document on disk named by
// OLIVARES_AGENT_GATEWAY_CONFIG — and then the production composition builder.
// The refusals below are refusals of the MCP COMPOSITION, not of JSON parsing:
// every case reaches buildMCPResourceServer, which is only possible because the
// document itself still decodes.
func TestMCPGatewayRevisionControlsPresenceMatchesValue(t *testing.T) {
	_, jwks := mintReviewToken(t, mcpReviewResource, "tools:read")

	cases := []struct {
		name string
		// fields is the verbatim JSON text of the two controls, so a case can
		// write a member name with any capitalization, and write it twice.
		fields string
		// The decoded state: what the operator document left in the config.
		wantModeField   string
		wantModePresent bool
		wantBool        bool
		wantBoolPresent bool
		// wantMode is the mode the built Resource Server must resolve; wantErr
		// names the substrings a refusal must carry instead.
		wantMode string
		wantErr  []string
	}{
		// ---- The five readings the independent review derived from the source.
		{
			name: "R1 noncanonical mode null is an explicit non-value",
			// Decoded to "" with presence false, this used to resolve LEGACY: an
			// explicitly nulled mode read exactly like an omitted one.
			fields:          `"REVISION_MODE":null`,
			wantModePresent: true,
			wantErr:         []string{"present but empty", "legacy, dual, rc-strict"},
		},
		{
			name:            "R1 noncanonical mode empty is an explicit non-value",
			fields:          `"REVISION_MODE":""`,
			wantModePresent: true,
			wantErr:         []string{"present but empty", "legacy, dual, rc-strict"},
		},
		{
			name: "R1 noncanonical false contradicts rc-strict",
			// The bool decoded to false while its presence stayed invisible, so
			// the contradiction rule had nothing to compare and rc-strict was
			// composed with the header gate explicitly turned off.
			fields:          `"revision_mode":"rc-strict","NEXT_REVISION_HEADERS":false`,
			wantModeField:   mcpGatewayRevisionModeRCStrict,
			wantModePresent: true,
			wantBoolPresent: true,
			wantErr:         []string{"revision_mode", "next_revision_headers", "contradict"},
		},
		{
			name:            "R1 noncanonical true contradicts legacy",
			fields:          `"revision_mode":"legacy","NEXT_REVISION_HEADERS":true`,
			wantModeField:   mcpGatewayRevisionModeLegacy,
			wantModePresent: true,
			wantBool:        true,
			wantBoolPresent: true,
			wantErr:         []string{"revision_mode", "next_revision_headers", "contradict"},
		},
		{
			name: "R1 mode supplied twice with a null",
			// An ambiguous control leaves NO value behind: recording one of the
			// two spellings as the operator's intent is the guess this refuses.
			fields:  `"revision_mode":"dual","revision_mode":null`,
			wantErr: []string{"revision_mode", "supplied 2 times", "ambiguous"},
		},
		{
			name:            "R1 boolean supplied twice with a null",
			fields:          `"revision_mode":"legacy","next_revision_headers":true,"next_revision_headers":null`,
			wantModeField:   mcpGatewayRevisionModeLegacy,
			wantModePresent: true,
			wantErr:         []string{"next_revision_headers", "supplied 2 times", "ambiguous"},
		},

		// ---- Duplicates that differ only in capitalization are the same control.
		{
			name:    "mode duplicated across capitalizations",
			fields:  `"revision_mode":"dual","REVISION_MODE":"legacy"`,
			wantErr: []string{`revision_mode is supplied 2 times`, `"revision_mode", "REVISION_MODE"`, "ambiguous"},
		},
		{
			name:    "mode alias first then a canonical null",
			fields:  `"REVISION_MODE":"dual","revision_mode":null`,
			wantErr: []string{`"REVISION_MODE", "revision_mode"`, "ambiguous"},
		},
		{
			name:    "boolean duplicated across capitalizations",
			fields:  `"Next_Revision_Headers":true,"next_revision_headers":false`,
			wantErr: []string{`next_revision_headers is supplied 2 times`, `"Next_Revision_Headers", "next_revision_headers"`},
		},
		{
			name: "duplicates agreeing in value are still ambiguous",
			// Nothing here reads the values to decide: a document that says the
			// same thing twice is still a document whose intent is stated twice,
			// and accepting it would make the refusal depend on comparing values
			// this composition has no rule for.
			fields: `"revision_mode":"dual","REVISION_MODE":"dual","next_revision_headers":true,"NEXT_REVISION_HEADERS":true`,
			wantErr: []string{
				"revision_mode is supplied 2 times", "next_revision_headers is supplied 2 times", "ambiguous",
			},
		},

		// ---- A SINGLE noncanonical spelling stays compatible: presence, null
		// and value now agree with the capitalization-insensitive decode.
		{
			name: "single alias mode", fields: `"REVISION_MODE":"dual"`,
			wantModeField: mcpGatewayRevisionModeDual, wantModePresent: true,
			wantMode: mcpGatewayRevisionModeDual,
		},
		{
			name: "single alias boolean true", fields: `"NEXT_REVISION_HEADERS":true`,
			wantBool: true, wantBoolPresent: true,
			wantMode: mcpGatewayRevisionModeDual,
		},
		{
			name: "single alias boolean false", fields: `"Next_Revision_Headers":false`,
			wantBoolPresent: true,
			wantMode:        mcpGatewayRevisionModeLegacy,
		},
		{
			name: "single alias pair agreeing", fields: `"Revision_Mode":"rc-strict","NEXT_REVISION_HEADERS":true`,
			wantModeField: mcpGatewayRevisionModeRCStrict, wantModePresent: true,
			wantBool: true, wantBoolPresent: true,
			wantMode: mcpGatewayRevisionModeRCStrict,
		},
		{
			name: "single alias boolean null stays compatible with absent",
			// The one null this cut may not turn into a refusal, in either
			// capitalization: it is an existing field whose null resolves legacy
			// today.
			fields:   `"NEXT_REVISION_HEADERS":null`,
			wantMode: mcpGatewayRevisionModeLegacy,
		},

		// ---- The canonical rows the correction may not move.
		{name: "neither control", fields: "", wantMode: mcpGatewayRevisionModeLegacy},
		{
			name: "canonical boolean true is dual", fields: `"next_revision_headers":true`,
			wantBool: true, wantBoolPresent: true,
			wantMode: mcpGatewayRevisionModeDual,
		},
		{
			name: "canonical explicit mode without a boolean", fields: `"revision_mode":"dual"`,
			wantModeField: mcpGatewayRevisionModeDual, wantModePresent: true,
			wantMode: mcpGatewayRevisionModeDual,
		},
		{
			name: "canonical rc-strict with an agreeing true", fields: `"revision_mode":"rc-strict","next_revision_headers":true`,
			wantModeField: mcpGatewayRevisionModeRCStrict, wantModePresent: true,
			wantBool: true, wantBoolPresent: true,
			wantMode: mcpGatewayRevisionModeRCStrict,
		},
		{
			name: "canonical single boolean null", fields: `"next_revision_headers":null`,
			wantMode: mcpGatewayRevisionModeLegacy,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := loadGatewayMCPConfig(t, gatewayDocWithRevisionFields(jwks, tc.fields, ""))
			if cfg.RevisionMode != tc.wantModeField || cfg.revisionModePresent != tc.wantModePresent {
				t.Errorf("decoded revision_mode = (%q, present %t), want (%q, present %t)",
					cfg.RevisionMode, cfg.revisionModePresent, tc.wantModeField, tc.wantModePresent)
			}
			if cfg.NextRevisionHeaders != tc.wantBool || cfg.nextRevisionHeadersPresent != tc.wantBoolPresent {
				t.Errorf("decoded next_revision_headers = (%t, present %t), want (%t, present %t)",
					cfg.NextRevisionHeaders, cfg.nextRevisionHeadersPresent, tc.wantBool, tc.wantBoolPresent)
			}
			rs, _, err := buildMCPResourceServer(&engine{log: discardLogger()}, cfg, discardLogger())
			if len(tc.wantErr) > 0 {
				if err == nil || rs != nil {
					t.Fatalf("construction = (%v, %v), want refusal before serving", rs, err)
				}
				for _, want := range tc.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("refusal %q does not name %q", err.Error(), want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("construction: %v", err)
			}
			if got := rs.RevisionMode(); got != tc.wantMode {
				t.Fatalf("resolved revision mode = %q, want %q", got, tc.wantMode)
			}
		})
	}
}

// TestMCPGatewayAmbiguousRevisionControlsRefuseBeforeComposition places the
// refusal exactly where the contradiction refusal already lives: the MCP
// composition. The operator document still DECODES — the loader returns no error
// — and the refusal happens when this gateway composes its Resource Server, so
// nothing is built and no effective-configuration read-back is emitted for a
// posture that was never resolved.
//
// The document below provisions ONLY an mcp block, so "no listener" is a fact
// about THIS document. It is not a claim that an independently provisioned A2A
// or registry surface would stop serving; those blocks are decoded and mounted
// by their own paths, which this correction does not touch.
func TestMCPGatewayAmbiguousRevisionControlsRefuseBeforeComposition(t *testing.T) {
	_, jwks := mintReviewToken(t, mcpReviewResource, "tools:read")
	doc := gatewayDocWithRevisionFields(jwks, `"revision_mode":"dual","REVISION_MODE":"legacy"`, "")
	path := filepath.Join(t.TempDir(), "agent-gateway.json")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("write operator config: %v", err)
	}
	t.Setenv("OLIVARES_AGENT_GATEWAY_CONFIG", path)

	// The document loads: an ambiguous control is a composition refusal, not a
	// parse error that would take the whole operator document down with it.
	loaded, err := loadAgentGatewayConfig(discardLogger())
	if err != nil {
		t.Fatalf("load operator config: %v", err)
	}
	if loaded.MCP == nil {
		t.Fatalf("operator document decoded without an mcp block: %s", doc)
	}

	buildLog, buildSink := captureGatewayLogger()
	rs, _, err := buildMCPResourceServer(&engine{log: buildLog}, loaded.MCP, buildLog)
	if err == nil || rs != nil {
		t.Fatalf("construction = (%v, %v), want refusal before serving", rs, err)
	}
	for _, want := range []string{"revision_mode", "supplied 2 times", "ambiguous"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not name %q", err.Error(), want)
		}
	}
	if strings.Contains(buildSink.String(), mcpGatewayReadBackMsgPrefix) {
		t.Fatalf("a refused composition emitted a configuration read-back: %s", buildSink.String())
	}

	mountLog, mountSink := captureGatewayLogger()
	srv, err := buildAgentGatewayServer(&engine{log: mountLog}, mountLog)
	if err != nil {
		t.Fatalf("buildAgentGatewayServer = %v, want the deny-closed not-mounted path", err)
	}
	if srv != nil {
		t.Fatalf("an ambiguous revision control produced a listener: %#v", srv.Addr)
	}
	out := mountSink.String()
	if !strings.Contains(out, mcpGatewayNotMountedLogEvent) {
		t.Fatalf("the refusal was not reported as a deny-closed mount failure: %s", out)
	}
	for _, want := range []string{"revision_mode", "ambiguous"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the mount failure does not name %q: %s", want, out)
		}
	}
	if strings.Contains(out, mcpGatewayReadBackMsgPrefix) {
		t.Fatalf("a refused gateway emitted a configuration read-back: %s", out)
	}
}

// TestMCPGatewayRevisionModeGoConstructionUnchanged keeps the Go-composition
// compatibility the presence flags were designed around: a struct literal
// supplies neither key, which is exactly the state "not explicitly supplied", and
// the correction must not start reading a literal as an ambiguous or nulled
// document. UnmarshalJSON remains the only writer of the decode state.
func TestMCPGatewayRevisionModeGoConstructionUnchanged(t *testing.T) {
	cases := []struct {
		name     string
		cfg      mcpGatewayConfig
		wantMode string
		wantErr  string
	}{
		{name: "zero literal states no mode", cfg: mcpGatewayConfig{}},
		{name: "literal mode alone", cfg: mcpGatewayConfig{RevisionMode: mcpGatewayRevisionModeDual}, wantMode: mcpGatewayRevisionModeDual},
		{
			name:     "literal mode with an agreeing boolean",
			cfg:      mcpGatewayConfig{RevisionMode: mcpGatewayRevisionModeRCStrict, NextRevisionHeaders: true},
			wantMode: mcpGatewayRevisionModeRCStrict,
		},
		{
			// A literal's boolean carries no PRESENCE, so it contradicts nothing
			// and the mode stands — the behaviour the presence flags were added
			// to preserve, and the reason this correction may not derive presence
			// from a value. A JSON document writing the same pair is refused
			// (see the table above); a Go composition is not a document.
			name:     "literal boolean does not contradict a literal mode",
			cfg:      mcpGatewayConfig{RevisionMode: mcpGatewayRevisionModeLegacy, NextRevisionHeaders: true},
			wantMode: mcpGatewayRevisionModeLegacy,
		},
		{name: "literal boolean alone leaves the connector's resolution", cfg: mcpGatewayConfig{NextRevisionHeaders: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			mode, err := resolveMCPGatewayRevisionMode(&cfg)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("resolution = (%q, %v), want a refusal naming %q", mode, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolution: %v", err)
			}
			if mode != tc.wantMode {
				t.Fatalf("resolved mode = %q, want %q", mode, tc.wantMode)
			}
		})
	}
}
