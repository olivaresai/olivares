// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// inferenceproxy_mcp_contract_test.go completes the Community real-proxy oracle of
// inferenceproxy_mcp_egress_test.go with the controls that need the MCP coverage interface:
// an origin-granting fake gate that reads descriptors and acknowledges coverage, the
// request-only approval notification with its dedup policy (Root correction m3), and the
// actor/tenant/origin-bound approval identity (m2).

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk/event"
)

// Independent known answers (evidence kat.py): the approval subject is
// "mcp-origin:" + hex(SHA-256(origin)); the plan is SHA-256 over the length-framed domain
// "olivares.mcp.approval.v1", tenant, actor, ActorRef and origin.
const (
	mcpApprovalSubject = "mcp-origin:af2dd40319b6924a1da609ee3566fd77eb13836fbcc5fdbda50d18e5bc410814"
	mcpPlanU1          = "74f1a6bb9ac9c41377d1c22445611535fe070d6366b29c348eb987467a5c15e2"
	mcpPlanU2          = "1b34619f2f468752c9317120347a600e2f87f19541e1bac04b2833606b86b861"
	mcpPlanU1Port8444  = "f86c740f06f2460f09d210aa4d87315ff1e720280b66ae5d708f8eadf364596d"
)

// ---- an origin-granting gate --------------------------------------------------------------

// originGate behaves like the private adapter's accepted contract at its interface: every
// destination origin granted → Forward with the exact copied acknowledgment; otherwise
// mcp_origin_not_granted naming the first denied destination. tamper, when set, edits the
// decision afterwards (a faulty or stale adapter).
type originGate struct {
	mu      sync.Mutex
	granted map[string]bool
	tamper  func(call int, in claudeapi.ServerToolEgressInput, dec *claudeapi.ServerToolEgressDecision)
	ins     []claudeapi.ServerToolEgressInput
}

func (g *originGate) GovernEgress(_ context.Context, in claudeapi.ServerToolEgressInput) claudeapi.ServerToolEgressDecision {
	g.mu.Lock()
	call := len(g.ins)
	g.ins = append(g.ins, in)
	g.mu.Unlock()
	dec := claudeapi.ServerToolEgressDecision{Forward: true}
	if in.MCP != nil {
		for _, dst := range in.MCP.Destinations {
			if !g.granted[dst.Origin] {
				dec = claudeapi.ServerToolEgressDecision{
					Status: http.StatusForbidden, ErrorType: "permission_error", Reason: "origin not granted",
					MCPDeny: claudeapi.MCPDenyOriginNotGranted,
					ApprovalIntent: &claudeapi.ServerToolEgressApprovalIntent{
						Action: "inference.servertool.egress", Family: "mcp", ToolType: "mcp_toolset",
						MCP: &claudeapi.MCPApprovalTarget{ServerIndex: dst.ServerIndex},
					},
				}
				break
			}
		}
		if dec.Forward {
			ack := in.MCP.Coverage
			dec.MCPAck = &ack
		}
	}
	if g.tamper != nil {
		g.tamper(call, in, &dec)
	}
	return dec
}

func (g *originGate) inputs() []claudeapi.ServerToolEgressInput {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]claudeapi.ServerToolEgressInput(nil), g.ins...)
}

func grantMCPTestOrigin() *originGate {
	return &originGate{granted: map[string]bool{mcpTestOrigin: true}}
}

func mcpParamsURL(rawURL string) map[string]any {
	p := mcpParams(false)
	p["mcp_servers"] = []any{mcpServerMap(mcpCanaryName, rawURL)}
	return p
}

// ---- denied before upstream ----------------------------------------------------------------

// TestProxyMCPEgressDeniedBeforeUpstream proves an unknown origin or port is refused with
// mcp_origin_not_granted and ZERO upstream requests — count_tokens included — on the
// blocking, streaming and batch routes; a later denied batch entry means nothing is sized.
// Mutant: skip egress before sizing, or size entries before the whole-batch barrier.
func TestProxyMCPEgressDeniedBeforeUpstream(t *testing.T) {
	stream := mcpParams(true)
	rows := []struct {
		name, path string
		granted    string
		body       any
		message    string
	}{
		{"unknown host", "/v1/messages", "https://other.example.com:8443", mcpParams(false), "mcp_origin_not_granted"},
		{"unknown port", "/v1/messages", "https://mcp.example.com:443", mcpParams(false), "mcp_origin_not_granted"},
		{"stream", "/v1/messages", "https://other.example.com:8443", stream, "mcp_origin_not_granted"},
		{"later batch entry", "/v1/messages/batches", mcpTestOrigin,
			batchBody(mcpParams(false), mcpParamsURL("https://mcp.example.com:9443/"+mcpCanaryPath)),
			"batch entry c1 denied: mcp_origin_not_granted"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			d, inf, up := mcpProxyDecider(true)
			gate := &originGate{granted: map[string]bool{r.granted: true}}
			d.egress = gate
			rec := serveProxy(t, claudeapi.NewMessagesProxy(inf, d, nil, nil), r.path, r.body)
			requireProxyRefusal(t, rec, http.StatusForbidden, "permission_error", r.message)
			if len(gate.inputs()) == 0 {
				t.Fatal("the egress gate was never consulted")
			}
			up.requireNone(t)
		})
	}
}

// ---- allowed wire ------------------------------------------------------------------------------

// TestProxyMCPEgressAllowedWire proves an exact origin grant forwards the ORIGINAL URL,
// token, name, configuration and positions to count_tokens and to the blocking, streaming
// and batch endpoints, with the current beta exactly once, and that the gate saw only the
// canonical origin. Mutant: a descriptor-only allow that forwards a changed URL or token.
func TestProxyMCPEgressAllowedWire(t *testing.T) {
	for _, rt := range []struct {
		name, path string
		body       any
		want       []string
	}{
		{"blocking", "/v1/messages", mcpParams(false), []string{"/v1/messages/count_tokens", "/v1/messages"}},
		{"stream", "/v1/messages", mcpParams(true), []string{"/v1/messages/count_tokens", "/v1/messages"}},
		{"batch", "/v1/messages/batches", batchBody(mcpParams(false), mcpParams(false)),
			[]string{"/v1/messages/count_tokens", "/v1/messages/count_tokens", "/v1/messages/batches"}},
	} {
		t.Run(rt.name, func(t *testing.T) {
			d, inf, up := mcpProxyDecider(true)
			gate := grantMCPTestOrigin()
			d.egress = gate
			rec := serveProxy(t, claudeapi.NewMessagesProxy(inf, d, nil, nil), rt.path, rt.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("granted origin refused: status=%d body=%s", rec.Code, rec.Body.String())
			}
			calls := up.recorded()
			if len(calls) != len(rt.want) {
				t.Fatalf("upstream calls = %d, want %v", len(calls), rt.want)
			}
			for i, c := range calls {
				if c.path != rt.want[i] {
					t.Fatalf("upstream call %d = %s, want %s", i, c.path, rt.want[i])
				}
				requireMCPBetaOnce(t, c)
				if c.path != "/v1/messages/batches" {
					requireMCPDeclarationForwarded(t, c.body)
					continue
				}
				var env struct {
					Requests []struct {
						Params json.RawMessage `json:"params"`
					} `json:"requests"`
				}
				if err := json.Unmarshal(c.body, &env); err != nil || len(env.Requests) != 2 {
					t.Fatalf("batch envelope: %v", err)
				}
				for _, e := range env.Requests {
					requireMCPDeclarationForwarded(t, e.Params)
				}
			}
			for _, in := range gate.inputs() {
				if in.MCP == nil || !reflect.DeepEqual(in.MCP.Destinations,
					[]claudeapi.MCPDestination{{ServerIndex: 0, ToolIndex: 0, Origin: mcpTestOrigin}}) {
					t.Errorf("gate destinations = %#v", in.MCP)
				}
				if digest, err := claudeapi.MCPCoverageDigest(in); err != nil || digest != in.MCP.Coverage.Digest {
					t.Errorf("issued coverage does not verify: %v", err)
				}
			}
		})
	}
}

// ---- coverage --------------------------------------------------------------------------------

// TestProxyMCPEgressCoverageRequiresExactAcknowledgment proves a wrong version, digest or
// nonce, an acknowledgment from a prior admission, a forward that carries a denial code and
// an unknown denial code all refuse with mcp_coverage_unavailable and zero upstream; a deny
// without a code stays a deny. Mutant: optional or ignored coverage.
func TestProxyMCPEgressCoverageRequiresExactAcknowledgment(t *testing.T) {
	var prior *claudeapi.MCPEgressCoverage
	rows := []struct {
		name    string
		tamper  func(call int, in claudeapi.ServerToolEgressInput, dec *claudeapi.ServerToolEgressDecision)
		status  int
		errType string
		message string
	}{
		{"wrong version", func(_ int, _ claudeapi.ServerToolEgressInput, d *claudeapi.ServerToolEgressDecision) {
			if d.MCPAck != nil {
				d.MCPAck.Version++
			}
		},
			http.StatusServiceUnavailable, "api_error", "mcp_coverage_unavailable"},
		{"wrong digest", func(_ int, _ claudeapi.ServerToolEgressInput, d *claudeapi.ServerToolEgressDecision) {
			if d.MCPAck != nil {
				d.MCPAck.Digest[0] ^= 0xff
			}
		},
			http.StatusServiceUnavailable, "api_error", "mcp_coverage_unavailable"},
		{"wrong nonce", func(_ int, _ claudeapi.ServerToolEgressInput, d *claudeapi.ServerToolEgressDecision) {
			if d.MCPAck != nil {
				d.MCPAck.Nonce[0] ^= 0xff
			}
		},
			http.StatusServiceUnavailable, "api_error", "mcp_coverage_unavailable"},
		{"prior admission", func(_ int, _ claudeapi.ServerToolEgressInput, d *claudeapi.ServerToolEgressDecision) {
			d.MCPAck = prior
		},
			http.StatusServiceUnavailable, "api_error", "mcp_coverage_unavailable"},
		{"forward with a denial code", func(_ int, _ claudeapi.ServerToolEgressInput, d *claudeapi.ServerToolEgressDecision) {
			d.MCPDeny = claudeapi.MCPDenyOriginNotGranted
		}, http.StatusServiceUnavailable, "api_error", "mcp_coverage_unavailable"},
		{"unknown denial code", func(_ int, _ claudeapi.ServerToolEgressInput, d *claudeapi.ServerToolEgressDecision) {
			*d = claudeapi.ServerToolEgressDecision{Status: http.StatusForbidden, MCPDeny: "mcp_something_else"}
		}, http.StatusServiceUnavailable, "api_error", "mcp_coverage_unavailable"},
		{"deny without a code", func(_ int, _ claudeapi.ServerToolEgressInput, d *claudeapi.ServerToolEgressDecision) {
			*d = claudeapi.ServerToolEgressDecision{Status: http.StatusForbidden, ErrorType: "permission_error"}
		}, http.StatusForbidden, "permission_error", "server-tool egress denied by policy"},
		{"policy denied", func(_ int, _ claudeapi.ServerToolEgressInput, d *claudeapi.ServerToolEgressDecision) {
			*d = claudeapi.ServerToolEgressDecision{Status: http.StatusTeapot, ErrorType: "overloaded_error", MCPDeny: claudeapi.MCPDenyPolicyDenied}
		}, http.StatusForbidden, "permission_error", "mcp_policy_denied"},
	}
	// The prior admission's exact acknowledgment, from an allowed request.
	{
		d, inf, _ := mcpProxyDecider(false)
		gate := grantMCPTestOrigin()
		d.egress = gate
		if rec := serveProxy(t, claudeapi.NewMessagesProxy(inf, d, nil, nil), "/v1/messages", mcpParams(false)); rec.Code != http.StatusOK {
			t.Fatalf("positive control refused: %d %s", rec.Code, rec.Body.String())
		}
		ins := gate.inputs()
		if len(ins) == 0 || ins[0].MCP == nil {
			t.Fatal("the positive control issued no MCP coverage to acknowledge")
		}
		ack := ins[0].MCP.Coverage
		prior = &ack
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			d, inf, up := mcpProxyDecider(true)
			gate := grantMCPTestOrigin()
			gate.tamper = r.tamper
			d.egress = gate
			rec := serveProxy(t, claudeapi.NewMessagesProxy(inf, d, nil, nil), "/v1/messages", mcpParams(false))
			requireProxyRefusal(t, rec, r.status, r.errType, r.message)
			up.requireNone(t)
		})
	}
	t.Run("batch entry reuses entry 0 acknowledgment", func(t *testing.T) {
		d, inf, up := mcpProxyDecider(true)
		gate := grantMCPTestOrigin()
		var first *claudeapi.MCPEgressCoverage
		gate.tamper = func(call int, _ claudeapi.ServerToolEgressInput, dec *claudeapi.ServerToolEgressDecision) {
			if call == 0 {
				first = dec.MCPAck
				return
			}
			dec.MCPAck = first
		}
		d.egress = gate
		rec := serveProxy(t, claudeapi.NewMessagesProxy(inf, d, nil, nil), "/v1/messages/batches", batchBody(mcpParams(false), mcpParams(false)))
		requireProxyRefusal(t, rec, http.StatusServiceUnavailable, "api_error", "batch entry c1 denied: mcp_coverage_unavailable")
		up.requireNone(t)
	})
}

// ---- binding ---------------------------------------------------------------------------------

// TestProxyMCPEgressBindingRefusesIllegalRewrites proves a gate may not rewrite, restore,
// reorder or insert an MCP slot (only the marker may stand there), while its own mutation of
// its input, and a later gate's mutation of its view, cannot reach the wire.
func TestProxyMCPEgressBindingRefusesIllegalRewrites(t *testing.T) {
	webSearch := map[string]any{"type": "web_search_20260209", "name": "web_search"}
	mixed := mcpParams(false)
	mixed["tools"] = []any{webSearch, mcpToolsetMap(mcpCanaryName)}
	rows := []struct {
		name     string
		body     map[string]any
		governed []any
	}{
		{"MCP slot rewritten", mcpParams(false), []any{mcpToolsetMap("other-server")}},
		{"MCP slot restored by the gate", mcpParams(false), []any{mcpToolsetMap(mcpCanaryName)}},
		{"MCP slot inserted", mcpParams(false), []any{mcpMarker(), mcpToolsetMap(mcpCanaryName)}},
		{"reordered", mixed, []any{mcpMarker(), webSearch}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			d, inf, up := mcpProxyDecider(true)
			gate := grantMCPTestOrigin()
			governed := r.governed
			gate.tamper = func(_ int, _ claudeapi.ServerToolEgressInput, dec *claudeapi.ServerToolEgressDecision) {
				dec.Rewritten, dec.GovernedTools = true, governed
			}
			d.egress = gate
			rec := serveProxy(t, claudeapi.NewMessagesProxy(inf, d, nil, nil), "/v1/messages", r.body)
			requireProxyRefusal(t, rec, http.StatusInternalServerError, "api_error", "mcp_binding_changed")
			up.requireNone(t)
		})
	}
	t.Run("gate and later-gate aliases", func(t *testing.T) {
		d, inf, up := mcpProxyDecider(true)
		gate := grantMCPTestOrigin()
		gate.tamper = func(_ int, in claudeapi.ServerToolEgressInput, _ *claudeapi.ServerToolEgressDecision) {
			for _, tool := range in.Tools {
				if m, ok := tool.(map[string]any); ok && m["type"] == "mcp_toolset" {
					m["mcp_server_name"] = "evil"
				}
			}
			if in.MCP != nil && len(in.MCP.Destinations) > 0 {
				in.MCP.Destinations[0].Origin = "https://evil.example:443"
			}
		}
		d.egress = gate
		cu := &mutatingComputerUseGate{}
		d.computerUse = cu
		body := mcpParams(false)
		body["tools"] = []any{computerTool(), mcpToolsetMap(mcpCanaryName)}
		rec := serveProxy(t, claudeapi.NewMessagesProxy(inf, d, nil, nil), "/v1/messages", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		if cu.calls != 1 {
			t.Fatalf("computer-use gate calls = %d", cu.calls)
		}
		for _, c := range up.recorded() {
			if bytes.Contains(c.body, []byte("evil")) {
				t.Fatalf("a gate alias reached %s: %s", c.path, c.body)
			}
			requireMCPDeclarationForwarded(t, c.body)
		}
	})
}

// flipMarshaler serializes as an inert custom tool except on its flipAt-th serialization,
// where it emits an MCP toolset: a caller value that changes between the binding checks
// and one serializer.
type flipMarshaler struct {
	calls  *int
	flipAt int
}

func (f flipMarshaler) MarshalJSON() ([]byte, error) {
	*f.calls++
	if *f.calls == f.flipAt {
		return json.Marshal(mcpToolsetMap("flipped"))
	}
	return []byte(`{"name":"lookup","input_schema":{"type":"object"}}`), nil
}

// TestProxyMCPEgressBindingRefusesSizingFailOpen proves a typed MCP refusal from
// count_tokens is a deny and never the sizing pre-flight's non-blocking failure. The value
// emits MCP only on its 5th serialization — the count_tokens body, after the inbound digest,
// the capture, the post-gate check and the pre-count check — and would pass every later
// check again, so only the typed-refusal branch before sizing fail-open can refuse. Added
// after the production commit: its red checkpoint is the named mutant that drops that
// branch (the request is then allowed), not a baseline run.
func TestProxyMCPEgressBindingRefusesSizingFailOpen(t *testing.T) {
	d, _, up := mcpProxyDecider(true)
	d.egress = &fakeEgressGate{dec: claudeapi.ServerToolEgressDecision{Forward: true}}
	calls := 0
	req := userReq("hi", false)
	req.Tools = []any{flipMarshaler{calls: &calls, flipAt: 5}}
	dec := d.Authorize(context.Background(), req, "bearer")
	if dec.Allow || dec.Status != http.StatusInternalServerError || dec.ErrorType != "api_error" || dec.Reason != "mcp_binding_changed" {
		t.Fatalf("count_tokens refusal must deny: allow=%v status=%d type=%q reason=%q", dec.Allow, dec.Status, dec.ErrorType, dec.Reason)
	}
	if calls != 5 {
		t.Fatalf("tool serializations = %d, want 5 (the refused one must be the count_tokens body)", calls)
	}
	up.requireNone(t)
}

// mutatingComputerUseGate allows, after trying to rewrite every map it was handed.
type mutatingComputerUseGate struct{ calls int }

func (g *mutatingComputerUseGate) GovernComputerUse(_ context.Context, in claudeapi.ComputerUseInput) claudeapi.ComputerUseDecision {
	g.calls++
	for _, tool := range in.Tools {
		if m, ok := tool.(map[string]any); ok && m["type"] == "mcp_toolset" {
			m["mcp_server_name"] = "evil"
			m["configs"] = map[string]any{"evil": map[string]any{"enabled": true}}
		}
	}
	return claudeapi.ComputerUseDecision{Forward: true}
}

// ---- compatibility ---------------------------------------------------------------------------

// TestProxyMCPEgressCompatibilityMixedRewrite proves the rewrite freedom that remains: a gate
// clamps a web-search slot of an MCP-bearing request and returns the marker at the MCP slot;
// the clamp is forwarded and the MCP declaration is forwarded exactly. Mutant: a blanket
// tool-rewrite ban or an unconditional MCP refusal.
func TestProxyMCPEgressCompatibilityMixedRewrite(t *testing.T) {
	d, inf, up := mcpProxyDecider(true)
	webSearch := map[string]any{"type": "web_search_20260209", "name": "web_search"}
	clamped := map[string]any{"type": "web_search_20260209", "name": "web_search", "allowed_domains": []any{"example.com"}, "max_uses": 5}
	gate := grantMCPTestOrigin()
	gate.tamper = func(_ int, _ claudeapi.ServerToolEgressInput, dec *claudeapi.ServerToolEgressDecision) {
		dec.Rewritten, dec.GovernedTools = true, []any{clamped, mcpMarker()}
	}
	d.egress = gate
	body := mcpParams(false)
	body["tools"] = []any{webSearch, mcpToolsetMap(mcpCanaryName)}
	rec := serveProxy(t, claudeapi.NewMessagesProxy(inf, d, nil, nil), "/v1/messages", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid mixed rewrite refused: %d %s", rec.Code, rec.Body.String())
	}
	calls := up.recorded()
	if len(calls) != 2 {
		t.Fatalf("upstream calls = %d, want count_tokens and messages", len(calls))
	}
	for _, c := range calls {
		var sent struct {
			Tools []map[string]any `json:"tools"`
		}
		if err := json.Unmarshal(c.body, &sent); err != nil || len(sent.Tools) != 2 {
			t.Fatalf("%s body: %v", c.path, err)
		}
		if sent.Tools[0]["allowed_domains"] == nil {
			t.Errorf("%s lost the gate's web-search clamp: %#v", c.path, sent.Tools[0])
		}
		requireMCPDeclarationForwarded(t, c.body)
	}
	// A request without MCP keeps the legacy rewrite freedom under a Forward-only gate.
	d2, inf2, _ := mcpProxyDecider(false)
	d2.egress = &fakeEgressGate{dec: claudeapi.ServerToolEgressDecision{Forward: true, Rewritten: true, GovernedTools: []any{clamped}}}
	plain := plainParams(false)
	plain["tools"] = []any{webSearch}
	if rec := serveProxy(t, claudeapi.NewMessagesProxy(inf2, d2, nil, nil), "/v1/messages", plain); rec.Code != http.StatusOK {
		t.Fatalf("legacy non-MCP rewrite refused: %d %s", rec.Code, rec.Body.String())
	}
}

// ---- approval and privacy --------------------------------------------------------------------

// fakeGovernance is the approvals API surface the bridge speaks, in memory: create,
// list (by status and action), read, and break-glass consume (which would GRANT if it were
// ever called, so a zero count is meaningful).
type fakeGovernance struct {
	mu        sync.Mutex
	approvals []*fakeApproval
	creates   int
	consumes  int
	lookupErr bool
}

type fakeApproval struct {
	// Tenant and Token record the service credential that created the row (never served).
	Tenant      string `json:"-"`
	Token       string `json:"-"`
	ID          string `json:"id"`
	Action      string `json:"action"`
	SubjectKind string `json:"subject_kind"`
	SubjectRef  string `json:"subject_ref"`
	Reason      string `json:"reason"`
	Status      string `json:"status"`
	DecidedAt   string `json:"decided_at"`
}

func (g *fakeGovernance) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	write := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	const base = "/v1/m/governance/approvals"
	switch {
	case r.Method == http.MethodPost && r.URL.Path == base:
		var in fakeApproval
		_ = json.NewDecoder(r.Body).Decode(&in)
		in.Tenant = r.Header.Get("X-Olivares-Tenant")
		in.Token = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		g.creates++
		in.ID = "appr-" + string(rune('a'+len(g.approvals)))
		in.Status = nbPending
		g.approvals = append(g.approvals, &in)
		write(http.StatusCreated, map[string]string{"id": in.ID, "status": in.Status})
	case r.Method == http.MethodGet && r.URL.Path == base:
		if g.lookupErr {
			write(http.StatusInternalServerError, map[string]string{"error": "down"})
			return
		}
		q := r.URL.Query()
		items := []*fakeApproval{}
		for _, a := range g.approvals {
			if a.Tenant == r.Header.Get("X-Olivares-Tenant") && a.Status == q.Get("status") && a.Action == q.Get("action") {
				items = append(items, a)
			}
		}
		write(http.StatusOK, map[string]any{"items": items, "has_more": false})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, base+"/"):
		if g.lookupErr {
			write(http.StatusInternalServerError, map[string]string{"error": "down"})
			return
		}
		id, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, base+"/"))
		for _, a := range g.approvals {
			if a.ID == id && a.Tenant == r.Header.Get("X-Olivares-Tenant") {
				write(http.StatusOK, a)
				return
			}
		}
		write(http.StatusNotFound, map[string]string{})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/m/governance/breakglass/consume":
		g.consumes++
		write(http.StatusOK, map[string]any{"granted": true, "grant": "bg-would-authorize"})
	default:
		write(http.StatusNotFound, map[string]string{})
	}
}

func (g *fakeGovernance) decide(i int, status string, decidedAt time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.approvals[i].Status = status
	g.approvals[i].DecidedAt = model.NewTimestamp(decidedAt).String()
	if decidedAt.IsZero() {
		g.approvals[i].DecidedAt = ""
	}
}

func (g *fakeGovernance) counts() (creates, consumes int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.creates, g.consumes
}

func (g *fakeGovernance) approval(i int) fakeApproval {
	g.mu.Lock()
	defer g.mu.Unlock()
	return *g.approvals[i]
}

// fakeBridge is a real approvalBridge over fakeGovernance for one tenant, with the given
// decision window and a fixed clock.
func fakeBridge(g *fakeGovernance, tenant model.TenantID, window int64, now time.Time) *approvalBridge {
	return fakeBridgeFor(g, window, now, tenant)
}

// fakeBridgeFor configures one service credential per tenant ("svc-" + tenant).
func fakeBridgeFor(g *fakeGovernance, window int64, now time.Time, tenants ...model.TenantID) *approvalBridge {
	creds := map[model.TenantID]serviceCred{}
	for _, tenant := range tenants {
		creds[tenant] = serviceCred{tenant: tenant, tenantStr: tenant.String(), token: "svc-" + tenant.String(), expiresIn: window}
	}
	b := &approvalBridge{creds: creds, log: discardLog(), clock: func() time.Time { return now }, memo: map[string]string{}}
	b.useHandler(g)
	return b
}

var notifyNow = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

// TestProxyMCPEgressApprovalPrivacy proves the MCP approval notification end to end through
// the real decider: Community derives subject, plan and reason itself (ignoring adapter
// text), the plan binds tenant, actual actor, ActorRef and origin, a pending retry does not
// duplicate, a prior human approval or an active break-glass grant never allows the call,
// and no path/query/token/name canary reaches the approval, findings, logs or HTTP body.
// Mutant: gateOnce, or a subject/plan taken from the adapter or the raw URL.
func TestProxyMCPEgressApprovalPrivacy(t *testing.T) {
	const adapterCanary = "ADAPTER-APPROVAL-CANARY"
	canaries := append(mcpWireCanaries(), adapterCanary)
	gov := &fakeGovernance{}
	bridge := fakeBridge(gov, proxyTestTenant, 3600, notifyNow)
	gate := &originGate{granted: map[string]bool{}, tamper: func(_ int, _ claudeapi.ServerToolEgressInput, dec *claudeapi.ServerToolEgressDecision) {
		if dec.ApprovalIntent != nil {
			dec.ApprovalIntent.Subject = adapterCanary
			dec.ApprovalIntent.PlanHash = adapterCanary
			dec.ApprovalIntent.Reason = adapterCanary
			dec.Reason = adapterCanary
		}
	}}
	bus := &fakeObservationBus{}
	aud := &recordingAuditor{}
	var logs bytes.Buffer
	run := func(t *testing.T, p auth.Principal, params map[string]any) {
		t.Helper()
		d, inf, up := mcpProxyDecider(true)
		d.authr = fakeProxyAuthr{p: p}
		d.approvals, d.egress, d.bus = bridge, gate, bus
		d.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		rec := serveProxy(t, claudeapi.NewMessagesProxy(inf, d, aud, nil), "/v1/messages", params)
		requireProxyRefusal(t, rec, http.StatusForbidden, "permission_error", "mcp_origin_not_granted")
		requireNoCanary(t, "HTTP refusal", rec.Body.Bytes(), canaries)
		up.requireNone(t)
	}
	u1 := proxyTestPrincipal()
	u2 := auth.ScopedPrincipal(model.ID("u2"), "user two", proxyTestTenant, "editor")

	run(t, u1, mcpParams(false))
	if creates, _ := gov.counts(); creates != 1 {
		t.Fatalf("first denial opened %d approval notification(s), want 1", creates)
	}
	a := gov.approval(0)
	if a.Action != "inference.servertool.egress" || a.SubjectKind != "anthropic.server_tool" ||
		a.SubjectRef != mcpApprovalSubject+planBindingMarker+mcpPlanU1 {
		t.Fatalf("approval identity = %s %s %s", a.Action, a.SubjectKind, a.SubjectRef)
	}
	if !strings.Contains(a.Reason, mcpTestOrigin) {
		t.Errorf("approval reason must name the canonical origin for the approver: %q", a.Reason)
	}
	requireNoCanary(t, "approval reason", []byte(a.Reason+a.SubjectRef), canaries)

	run(t, u1, mcpParams(false)) // pending retry: deduplicated
	gov.decide(0, nbApproved, notifyNow.Add(-10*time.Minute))
	run(t, u1, mcpParams(false)) // an approved notification is not an origin grant
	if creates, _ := gov.counts(); creates != 1 {
		t.Fatalf("retries opened %d notifications, want 1 (pending and recent decision dedup)", creates)
	}
	run(t, u2, mcpParams(false)) // another actor is another plan identity
	run(t, u1, mcpParamsURL("https://mcp.example.com:8444/"+mcpCanaryPath))
	creates, consumes := gov.counts()
	if creates != 3 || consumes != 0 {
		t.Fatalf("creates=%d consumes=%d, want 3 notifications and zero break-glass consumption", creates, consumes)
	}
	if got := gov.approval(1).SubjectRef; !strings.HasSuffix(got, planBindingMarker+mcpPlanU2) {
		t.Errorf("second actor plan = %q, want %s", got, mcpPlanU2)
	}
	if got := gov.approval(2).SubjectRef; !strings.HasSuffix(got, planBindingMarker+mcpPlanU1Port8444) {
		t.Errorf("other-port plan = %q, want %s", got, mcpPlanU1Port8444)
	}
	for _, e := range bus.events {
		if f, ok := event.FindingOf(e); ok {
			blob, _ := json.Marshal(f)
			requireNoCanary(t, "published finding", blob, canaries)
		}
	}
	requireNoCanary(t, "decider log", logs.Bytes(), canaries)
	requireAuditClean(t, aud, canaries)
}

// TestProxyMCPEgressApprovalOnlyForGrantableTarget proves only mcp_origin_not_granted with a
// target Community can resolve opens a notification: an out-of-range target, a policy denial
// and a coverage refusal open none.
func TestProxyMCPEgressApprovalOnlyForGrantableTarget(t *testing.T) {
	rows := map[string]func(int, claudeapi.ServerToolEgressInput, *claudeapi.ServerToolEgressDecision){
		"target out of range": func(_ int, _ claudeapi.ServerToolEgressInput, d *claudeapi.ServerToolEgressDecision) {
			if d.ApprovalIntent != nil && d.ApprovalIntent.MCP != nil {
				d.ApprovalIntent.MCP.ServerIndex = 7
			}
		},
		"policy denied": func(_ int, _ claudeapi.ServerToolEgressInput, d *claudeapi.ServerToolEgressDecision) {
			d.MCPDeny = claudeapi.MCPDenyPolicyDenied
		},
		"missing target": func(_ int, _ claudeapi.ServerToolEgressInput, d *claudeapi.ServerToolEgressDecision) {
			if d.ApprovalIntent != nil {
				d.ApprovalIntent.MCP = nil
				d.ApprovalIntent.Subject, d.ApprovalIntent.PlanHash = "legacy-subject", "legacy-plan"
			}
		},
	}
	for name, tamper := range rows {
		t.Run(name, func(t *testing.T) {
			gov := &fakeGovernance{}
			d, inf, _ := mcpProxyDecider(false)
			d.approvals = fakeBridge(gov, proxyTestTenant, 3600, notifyNow)
			d.egress = &originGate{granted: map[string]bool{}, tamper: tamper}
			rec := serveProxy(t, claudeapi.NewMessagesProxy(inf, d, nil, nil), "/v1/messages", mcpParams(false))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d", rec.Code)
			}
			if creates, consumes := gov.counts(); creates != 0 || consumes != 0 {
				t.Fatalf("creates=%d consumes=%d, want no notification", creates, consumes)
			}
		})
	}
}

// ---- the notification policy (m3) ----------------------------------------------------------

func notifyOnce(t *testing.T, b *approvalBridge) error {
	t.Helper()
	return b.notify(context.Background(), proxyTestTenant, "inference.servertool.egress", "anthropic.server_tool",
		mcpApprovalSubject, mcpPlanU1, "MCP destination origin is not granted: "+mcpTestOrigin, "token:u1")
}

// TestApprovalBridgeNotificationDedupPolicy pins the request-only notification lookup: a
// pending notification and an approved/rejected/canceled one inside the positive decision
// window deduplicate — in memory and after a restart — while expired, timestamp-less,
// unparseable and out-of-window rows reopen, and a zero window never deduplicates a
// decision. Break-glass is never consulted.
func TestApprovalBridgeNotificationDedupPolicy(t *testing.T) {
	type row struct {
		status    string
		decidedAt time.Time
		raw       string
		window    int64
		dedup     bool
	}
	rows := map[string]row{
		"pending":                   {status: nbPending, window: 3600, dedup: true},
		"approved in window":        {status: nbApproved, decidedAt: notifyNow.Add(-10 * time.Minute), window: 3600, dedup: true},
		"rejected in window":        {status: nbRejected, decidedAt: notifyNow.Add(-10 * time.Minute), window: 3600, dedup: true},
		"canceled in window":        {status: nbCanceled, decidedAt: notifyNow.Add(-10 * time.Minute), window: 3600, dedup: true},
		"approved out of window":    {status: nbApproved, decidedAt: notifyNow.Add(-2 * time.Hour), window: 3600},
		"rejected out of window":    {status: nbRejected, decidedAt: notifyNow.Add(-2 * time.Hour), window: 3600},
		"expired":                   {status: nbExpired, decidedAt: notifyNow.Add(-time.Minute), window: 3600},
		"unknown status":            {status: "garbage", decidedAt: notifyNow.Add(-time.Minute), window: 3600},
		"approved without time":     {status: nbApproved, window: 3600},
		"approved unparseable":      {status: nbApproved, raw: "not-a-time", window: 3600},
		"approved with zero window": {status: nbApproved, decidedAt: notifyNow.Add(-time.Minute), window: 0},
	}
	for name, r := range rows {
		for _, restart := range []bool{false, true} {
			label := name
			if restart {
				label += " after restart"
			}
			t.Run(label, func(t *testing.T) {
				gov := &fakeGovernance{}
				b := fakeBridge(gov, proxyTestTenant, r.window, notifyNow)
				if err := notifyOnce(t, b); err != nil {
					t.Fatalf("first notification: %v", err)
				}
				if r.status != nbPending {
					gov.decide(0, r.status, r.decidedAt)
					if r.raw != "" {
						gov.mu.Lock()
						gov.approvals[0].DecidedAt = r.raw
						gov.mu.Unlock()
					}
				}
				if restart {
					b = fakeBridge(gov, proxyTestTenant, r.window, notifyNow)
				}
				if err := notifyOnce(t, b); err != nil {
					t.Fatalf("second notification: %v", err)
				}
				want := 2
				if r.dedup {
					want = 1
				}
				if creates, consumes := gov.counts(); creates != want || consumes != 0 {
					t.Fatalf("creates=%d consumes=%d, want %d and zero break-glass", creates, consumes, want)
				}
			})
		}
	}
}

// TestApprovalBridgeNotificationLookupErrorCreatesNothing proves an unavailable durable
// lookup reports notification-unavailable and never opens a duplicate, from a cold process
// (list) and from a warm memo (read).
func TestApprovalBridgeNotificationLookupErrorCreatesNothing(t *testing.T) {
	gov := &fakeGovernance{lookupErr: true}
	if err := notifyOnce(t, fakeBridge(gov, proxyTestTenant, 3600, notifyNow)); err == nil {
		t.Fatal("a failed durable lookup must be reported")
	}
	if creates, _ := gov.counts(); creates != 0 {
		t.Fatalf("a failed lookup opened %d notification(s)", creates)
	}
	gov.lookupErr = false
	b := fakeBridge(gov, proxyTestTenant, 3600, notifyNow)
	if err := notifyOnce(t, b); err != nil {
		t.Fatalf("notification: %v", err)
	}
	gov.mu.Lock()
	gov.lookupErr = true
	gov.mu.Unlock()
	if err := notifyOnce(t, b); err == nil {
		t.Fatal("a failed memo read must be reported")
	}
	if creates, _ := gov.counts(); creates != 1 {
		t.Fatalf("a failed memo read opened a duplicate (%d creates)", creates)
	}
}

// TestApprovalBridgeNotificationUnconfiguredTenant proves a tenant without a service
// credential opens nothing and reports nothing to authorize.
func TestApprovalBridgeNotificationUnconfiguredTenant(t *testing.T) {
	gov := &fakeGovernance{}
	b := fakeBridge(gov, model.TenantID("other-tenant"), 3600, notifyNow)
	if err := notifyOnce(t, b); err != nil {
		t.Fatalf("unconfigured tenant: %v", err)
	}
	if creates, consumes := gov.counts(); creates != 0 || consumes != 0 {
		t.Fatalf("creates=%d consumes=%d for an unconfigured tenant", creates, consumes)
	}
}
