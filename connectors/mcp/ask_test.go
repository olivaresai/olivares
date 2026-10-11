// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4/jwt"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// approvalDesk is an ApprovalGate with the bridge's single-use semantics: a call
// is pending until a human approves; a round trip (ConsumerID) spends the
// approval, and another consumer on a spent approval is rejected.
type approvalDesk struct {
	mu        sync.Mutex
	approved  bool
	spentBy   string
	consumers []string
}

func (d *approvalDesk) approve() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.approved = true
}

func (d *approvalDesk) Authorize(_ context.Context, req ToolApprovalRequest) (GateDecision, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.consumers = append(d.consumers, req.ConsumerID)
	dec := GateDecision{ApprovalRef: "appr-7", Status: StatusPending, PlanHash: req.PlanHash}
	if !d.approved {
		return dec, nil
	}
	dec.Status = StatusApproved
	if req.ConsumerID != "" {
		if d.spentBy != "" && d.spentBy != req.ConsumerID {
			dec.Status = StatusRejected
			return dec, nil
		}
		d.spentBy = req.ConsumerID
		dec.Spent = true
	}
	return dec, nil
}

// methodUpstream answers server/discover like a 2026-07-28 server and records
// every tools/call it receives.
type methodUpstream struct {
	mu    sync.Mutex
	calls []json.RawMessage
}

func (u *methodUpstream) Forward(_ context.Context, req UpstreamRequest) (UpstreamResult, error) {
	switch req.Method {
	case methodServerDiscover:
		return UpstreamResult{State: DispatchCompleted, Result: json.RawMessage(`{"resultType":"complete","ttlMs":0,"cacheScope":"public",` +
			`"supportedVersions":["2026-07-28"],"capabilities":{"tools":{}},"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"upstream","version":"1"}}}`)}, nil
	case "tools/call":
		u.mu.Lock()
		u.calls = append(u.calls, append(json.RawMessage(nil), req.Params...))
		u.mu.Unlock()
		return UpstreamResult{State: DispatchCompleted, Result: json.RawMessage(`{"content":[{"type":"text","text":"deleted"}]}`)}, nil
	}
	return UpstreamResult{State: DispatchCompleted, Result: json.RawMessage(`{}`)}, nil
}

func newAskRS(t *testing.T, jwks []byte, gate ApprovalGate, up Upstream, aud GateAuditor) *ResourceServer {
	t.Helper()
	ts, err := NewToolset([]ToolPolicy{
		{Name: "search", RequiredScope: "tools:read"},
		{Name: "delete_db", RequiredScope: "tools:admin", Destructive: true},
		{Name: "drop_db", RequiredScope: "tools:admin", Destructive: true},
	})
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	rs, err := NewResourceServer(ResourceServerConfig{
		Resource:             rsResource,
		AuthorizationServers: []string{rsIssuer},
		Issuer:               rsIssuer,
		IssuerJWKS:           jwks,
		Toolset:              ts,
		Gate:                 gate,
		Upstream:             up,
		DurableTaskStore:     newMemoryDurableTaskStore(),
		Auditor:              aud,
		Clock:                rsClock,
		RevisionMode:         revisionModeDual,
	})
	if err != nil {
		t.Fatalf("new rs: %v", err)
	}
	return rs
}

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// TestAskRoundTripWithOfficialClient: the official go-sdk client calls a
// destructive tool through the gateway, gets the approval round trip, the human
// approves while the client shows the form, and the SDK's own retry completes
// the call — with the approval spent once and nothing of the round trip
// forwarded upstream.
func TestAskRoundTripWithOfficialClient(t *testing.T) {
	sg := newSigner(t)
	desk := &approvalDesk{}
	up := &methodUpstream{}
	aud := &capturingAuditor{}
	srv := httptest.NewServer(newAskRS(t, sg.jwks, desk, up, aud))
	defer srv.Close()

	var prompts []string
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "agent", Version: "1"}, &mcpsdk.ClientOptions{
		ElicitationHandler: func(_ context.Context, req *mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error) {
			prompts = append(prompts, req.Params.Message)
			desk.approve() // the human approves in Olivares while the client asks
			return &mcpsdk.ElicitResult{Action: "accept"}, nil
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint:   srv.URL,
		HTTPClient: &http.Client{Transport: bearerTransport{sg.mint(t, rsResource, "tools:admin", validExp())}},
	}, nil)
	if err != nil {
		t.Fatalf("official client could not connect: %v", err)
	}
	defer session.Close()

	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "delete_db", Arguments: map[string]any{"db": "prod"}})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError || len(res.Content) != 1 || res.Content[0].(*mcpsdk.TextContent).Text != "deleted" {
		t.Fatalf("result = %+v, want the upstream's answer", res)
	}
	if len(prompts) != 1 || !strings.Contains(prompts[0], "appr-7") || !strings.Contains(prompts[0], "delete_db") || strings.Contains(prompts[0], "olivares governance") {
		t.Errorf("the user was asked %q, want one approval prompt naming the tool and the request, and no command", prompts)
	}
	if len(desk.consumers) != 2 || desk.consumers[0] != "" || desk.consumers[1] == "" || desk.spentBy != desk.consumers[1] {
		t.Errorf("gate consumers = %q, spent by %q; want the retry alone to spend the approval", desk.consumers, desk.spentBy)
	}
	if len(up.calls) != 1 {
		t.Fatalf("upstream tools/call count = %d, want 1", len(up.calls))
	}
	if forwarded := string(up.calls[0]); strings.Contains(forwarded, "requestState") || strings.Contains(forwarded, "inputResponses") || strings.Contains(forwarded, askStatePrefix) {
		t.Errorf("the round trip reached the upstream: %s", forwarded)
	}
	if d := lastDecision(t, aud); !d.Allowed || d.Decision != DecisionAsk || d.GrantMode != "round_trip" {
		t.Errorf("allow decision = %+v, want an ask granted by the round trip", d)
	}
}

// rcToolsCall is a 2026-07-28 tools/call with the given client capabilities and
// optional round-trip members.
func rcToolsCall(token, tool, caps, extra string) *http.Request {
	return rcToolsCallArgs(token, tool, `{"db":"prod"}`, caps, extra)
}

func rcToolsCallArgs(token, tool, args, caps, extra string) *http.Request {
	body := `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"` + tool + `","arguments":` + args + extra +
		`,"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":` + caps + `}}}`
	return nextReqRaw(token, "tools/call", tool, body)
}

// mintFor mints a tools:admin access token for a subject and OAuth client.
func mintFor(t *testing.T, sg *signer, subject, clientID string) string {
	t.Helper()
	raw, err := jwt.Signed(sg.js).Claims(jwt.Claims{
		Issuer: rsIssuer, Subject: subject, Audience: jwt.Audience{rsResource},
		IssuedAt: jwt.NewNumericDate(rsClock().Add(-time.Minute)), Expiry: jwt.NewNumericDate(validExp()),
	}).Claims(map[string]any{"scope": "tools:admin", "client_id": clientID}).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return raw
}

func askAnswer(state, action string) string {
	return `,"requestState":"` + state + `","inputResponses":{"` + askInputKey + `":{"action":"` + action + `"}}`
}

// askStateFrom extracts the sealed state from an input_required answer, checking
// that the official SDK reads the answer as one.
func askStateFrom(t *testing.T, body []byte) string {
	t.Helper()
	var resp struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || len(resp.Result) == 0 {
		t.Fatalf("not a JSON-RPC result: %s", body)
	}
	var res mcpsdk.CallToolResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("the official SDK cannot read the answer: %v; %s", err, resp.Result)
	}
	if !res.NeedsInput() || !strings.HasPrefix(res.RequestState, askStatePrefix) {
		t.Fatalf("answer is not an input_required round trip: %s", resp.Result)
	}
	if _, ok := res.InputRequests[askInputKey].(*mcpsdk.ElicitParams); !ok {
		t.Fatalf("input requests = %v, want an elicitation under %q", res.InputRequests, askInputKey)
	}
	return res.RequestState
}

// TestAskRoundTripOnlyForCapableClients: the round trip replaces the refusal
// only for a 2026-07-28 request that declares form elicitation; every other
// client keeps the published 403.
func TestAskRoundTripOnlyForCapableClients(t *testing.T) {
	sg := newSigner(t)
	token := sg.mint(t, rsResource, "tools:admin", validExp())
	aud := &capturingAuditor{}
	rs := newAskRS(t, sg.jwks, &approvalDesk{}, &methodUpstream{}, aud)
	for name, caps := range map[string]string{
		"no capabilities":  `{}`,
		"url mode only":    `{"elicitation":{"url":{}}}`,
		"case-variant key": `{"Elicitation":{}}`,
	} {
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, rcToolsCall(token, "delete_db", caps, ""))
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "destructive tool requires human approval (pending)") {
			t.Errorf("%s: %d %s, want the published 403", name, w.Code, w.Body.String())
		}
	}
	// A legacy request keeps the refusal even when it carries the same metadata.
	legacy := httptest.NewRequest(http.MethodPost, rsResource, strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delete_db","arguments":{},"_meta":{"io.modelcontextprotocol/clientCapabilities":{"elicitation":{}}}}}`))
	legacy.Header.Set("Authorization", "Bearer "+token)
	legacy.Header.Set(headerMCPProtocolVersion, "2025-11-25")
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, legacy)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "destructive tool requires human approval (pending)") {
		t.Errorf("legacy request: %d %s, want the published 403", w.Code, w.Body.String())
	}
	for _, caps := range []string{`{"elicitation":{}}`, `{"elicitation":{"form":{},"url":{}}}`} {
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, rcToolsCall(token, "delete_db", caps, ""))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s, want the round trip", caps, w.Code, w.Body.String())
		}
		askStateFrom(t, w.Body.Bytes())
		if reason := lastDecision(t, aud).Reason; reason != "destructive tool not approved (pending); approval round trip offered" {
			t.Errorf("%s: audit reason %q, want the offered round trip recorded", caps, reason)
		}
	}
	// A round trip that cannot be sealed keeps the published refusal and says why.
	rs.asks = nil
	w = httptest.NewRecorder()
	rs.ServeHTTP(w, rcToolsCall(token, "delete_db", `{"elicitation":{}}`, ""))
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "destructive tool requires human approval (pending)") {
		t.Errorf("unsealable round trip: %d %s, want the published 403", w.Code, w.Body.String())
	}
	if reason := lastDecision(t, aud).Reason; !strings.Contains(reason, "approval round trip unavailable") {
		t.Errorf("unsealable round trip audit reason %q, want why it was unavailable", reason)
	}
}

// TestAskRoundTripStateIsSealedBoundAndSingleUse: the state authorizes nothing
// alone, belongs to one call, expires, and is spent on first use.
func TestAskRoundTripStateIsSealedBoundAndSingleUse(t *testing.T) {
	sg := newSigner(t)
	token := sg.mint(t, rsResource, "tools:admin", validExp())
	desk := &approvalDesk{}
	up := &methodUpstream{}
	now := rsClock()
	rs := newAskRS(t, sg.jwks, desk, up, &capturingAuditor{})
	rs.now = func() time.Time { return now }
	caps := `{"elicitation":{}}`
	ask := func() string {
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, rcToolsCall(token, "delete_db", caps, ""))
		return askStateFrom(t, w.Body.Bytes())
	}
	retryOn := func(server *ResourceServer, tool, state, action string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		extra := `,"requestState":"` + state + `","inputResponses":{"` + askInputKey + `":{"action":"` + action + `"}}`
		server.ServeHTTP(w, rcToolsCall(token, tool, caps, extra))
		return w
	}
	retry := func(tool, state, action string) *httptest.ResponseRecorder { return retryOn(rs, tool, state, action) }
	refused := func(name string, w *httptest.ResponseRecorder, want string) {
		t.Helper()
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: %d %s, want 403 %q", name, w.Code, w.Body.String(), want)
		}
	}
	const invalid = "the approval round trip is invalid, expired or already used"

	// Every state is issued while the approval is pending: once approved, a call
	// without a state takes the published retry path and is not asked.
	first := ask()
	w := retry("delete_db", first, "accept")
	if w.Code != http.StatusOK {
		t.Fatalf("retry before the human approved: %d %s", w.Code, w.Body.String())
	}
	second := askStateFrom(t, w.Body.Bytes())
	if second == first {
		t.Error("a still-pending retry must get a fresh state")
	}
	refused("spent pending state", retry("delete_db", first, "accept"), invalid)
	declined, tampered, expired, valid := ask(), ask(), ask(), ask()

	desk.approve()
	refused("declined", retry("delete_db", declined, "decline"), "not confirmed in the client")
	refused("state of a declined round trip", retry("delete_db", declined, "accept"), invalid)
	mid := len(tampered) / 2
	flipped := byte('A')
	if tampered[mid] == 'A' {
		flipped = 'B'
	}
	refused("tampered", retry("delete_db", tampered[:mid]+string(flipped)+tampered[mid+1:], "accept"), invalid)
	refused("another gateway process", retryOn(newAskRS(t, sg.jwks, desk, up, &capturingAuditor{}), "delete_db", tampered, "accept"), invalid)
	now = now.Add(askStateTTL)
	refused("expired", retry("delete_db", expired, "accept"), invalid)
	now = rsClock()
	if desk.spentBy != "" || len(up.calls) != 0 {
		t.Fatalf("a refused round trip spent the approval (%q) or reached the upstream (%d calls)", desk.spentBy, len(up.calls))
	}

	if w := retry("delete_db", valid, "accept"); w.Code != http.StatusOK || len(up.calls) != 1 || desk.spentBy == "" {
		t.Fatalf("valid round trip: %d %s, upstream calls %d, spent by %q", w.Code, w.Body.String(), len(up.calls), desk.spentBy)
	}
	refused("replay", retry("delete_db", valid, "accept"), invalid)
	// Another unspent state of the same call cannot reuse the spent approval.
	refused("second round trip on a spent approval", retry("delete_db", second, "accept"), "destructive tool requires human approval (rejected)")
	if len(up.calls) != 1 {
		t.Fatalf("one human approval ran the call %d times through round trips", len(up.calls))
	}
}

// TestAskRoundTripStateRefusedForOtherCall: through the gateway, a state does not
// complete other arguments, another destructive tool, another subject or another
// OAuth client — and stays usable by the call it was issued for.
func TestAskRoundTripStateRefusedForOtherCall(t *testing.T) {
	sg := newSigner(t)
	desk := &approvalDesk{}
	up := &methodUpstream{}
	aud := &capturingAuditor{}
	rs := newAskRS(t, sg.jwks, desk, up, aud)
	caps := `{"elicitation":{}}`
	owner := mintFor(t, sg, "agent:claude", "client-a")
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, rcToolsCallArgs(owner, "delete_db", `{"db":"prod"}`, caps, ""))
	state := askStateFrom(t, w.Body.Bytes())
	desk.approve()
	answer := askAnswer(state, "accept")
	for name, req := range map[string]*http.Request{
		"other arguments": rcToolsCallArgs(owner, "delete_db", `{"db":"staging"}`, caps, answer),
		"other tool":      rcToolsCallArgs(owner, "drop_db", `{"db":"prod"}`, caps, answer),
		"other subject":   rcToolsCallArgs(mintFor(t, sg, "agent:other", "client-a"), "delete_db", `{"db":"prod"}`, caps, answer),
		"other client":    rcToolsCallArgs(mintFor(t, sg, "agent:claude", "client-b"), "delete_db", `{"db":"prod"}`, caps, answer),
	} {
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "the approval round trip is invalid") {
			t.Errorf("%s: %d %s, want the round-trip refusal", name, w.Code, w.Body.String())
		}
	}
	if desk.spentBy != "" || len(up.calls) != 0 {
		t.Fatalf("another call spent the approval (%q) or reached the upstream (%d)", desk.spentBy, len(up.calls))
	}
	w = httptest.NewRecorder()
	rs.ServeHTTP(w, rcToolsCallArgs(owner, "delete_db", `{"db":"prod"}`, caps, answer))
	if w.Code != http.StatusOK || len(up.calls) != 1 || desk.spentBy == "" {
		t.Fatalf("the owner's round trip: %d %s, upstream %d, spent by %q", w.Code, w.Body.String(), len(up.calls), desk.spentBy)
	}
	if d := lastDecision(t, aud); d.ClientID != "client-a" || d.GrantMode != "round_trip" {
		t.Errorf("allow decision client %q grant %q, want client-a round_trip", d.ClientID, d.GrantMode)
	}
}

// TestAskRoundTripOnlyWhilePending: a rejected or expired approval keeps the
// refusal; there is nothing left to ask.
func TestAskRoundTripOnlyWhilePending(t *testing.T) {
	sg := newSigner(t)
	token := sg.mint(t, rsResource, "tools:admin", validExp())
	for _, status := range []GateStatus{StatusRejected, StatusExpired, StatusNoGate} {
		rs := newAskRS(t, sg.jwks, &countingGate{status: status}, &methodUpstream{}, &capturingAuditor{})
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, rcToolsCall(token, "delete_db", `{"elicitation":{}}`, ""))
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "destructive tool requires human approval ("+string(status)+")") {
			t.Errorf("%s: %d %s, want the published 403", status, w.Code, w.Body.String())
		}
	}
}

// TestAskRoundTripRequiresTheSpend: a gate that approves a round trip without
// spending the approval does not complete it.
func TestAskRoundTripRequiresTheSpend(t *testing.T) {
	sg := newSigner(t)
	token := sg.mint(t, rsResource, "tools:admin", validExp())
	gate := &countingGate{status: StatusPending}
	up := &methodUpstream{}
	rs := newAskRS(t, sg.jwks, gate, up, &capturingAuditor{})
	caps := `{"elicitation":{}}`
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, rcToolsCall(token, "delete_db", caps, ""))
	state := askStateFrom(t, w.Body.Bytes())
	gate.status = StatusApproved // approves, never spends
	w = httptest.NewRecorder()
	rs.ServeHTTP(w, rcToolsCall(token, "delete_db", caps, askAnswer(state, "accept")))
	if w.Code != http.StatusForbidden || len(up.calls) != 0 || gate.last.ConsumerID == "" {
		t.Fatalf("unspent round trip: %d %s, upstream %d, consumer %q; want 403 before the upstream", w.Code, w.Body.String(), len(up.calls), gate.last.ConsumerID)
	}
}

// TestAskRoundTripTooManyInFlight: a subject whose round-trip share is full gets
// 429, not the invalid-state refusal, and the gate is not asked.
func TestAskRoundTripTooManyInFlight(t *testing.T) {
	sg := newSigner(t)
	token := sg.mint(t, rsResource, "tools:admin", validExp())
	gate := &countingGate{status: StatusPending}
	rs := newAskRS(t, sg.jwks, gate, &methodUpstream{}, &capturingAuditor{})
	caps := `{"elicitation":{}}`
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, rcToolsCall(token, "delete_db", caps, ""))
	state := askStateFrom(t, w.Body.Bytes())
	rs.asks.shares[rs.tenant+"\x00agent:claude"] = maxSpentAskStatesPerSubject
	calls := gate.calls
	w = httptest.NewRecorder()
	rs.ServeHTTP(w, rcToolsCall(token, "delete_db", caps, askAnswer(state, "accept")))
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "too many approval round trips") || gate.calls != calls {
		t.Fatalf("full share: %d %s, gate asked %d more times; want 429 before the gate", w.Code, w.Body.String(), gate.calls-calls)
	}
}

// TestAskRoundTripAnswerMustAccept: a retry without the user's answer, or with a
// cancel, is refused before the gate is asked.
func TestAskRoundTripAnswerMustAccept(t *testing.T) {
	sg := newSigner(t)
	token := sg.mint(t, rsResource, "tools:admin", validExp())
	gate := &countingGate{status: StatusPending}
	aud := &capturingAuditor{}
	rs := newAskRS(t, sg.jwks, gate, &methodUpstream{}, aud)
	caps := `{"elicitation":{}}`
	issue := func() string {
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, rcToolsCall(token, "delete_db", caps, ""))
		return askStateFrom(t, w.Body.Bytes())
	}
	clientText := strings.Repeat("x", 4096)
	for name, tc := range map[string]struct{ extra, label string }{
		"no answer":    {`,"requestState":"` + issue() + `"`, "(missing)"},
		"cancel":       {askAnswer(issue(), "cancel"), "(cancel)"},
		"other answer": {`,"requestState":"` + issue() + `","inputResponses":{"other":{"action":"accept"}}`, "(missing)"},
		"client text":  {askAnswer(issue(), clientText), "(unrecognized)"},
	} {
		calls := gate.calls
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, rcToolsCall(token, "delete_db", caps, tc.extra))
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "not confirmed in the client") || gate.calls != calls {
			t.Errorf("%s: %d %s (gate asked %d more times), want the refusal before the gate", name, w.Code, w.Body.String(), gate.calls-calls)
		}
		if reason := lastDecision(t, aud).Reason; !strings.HasSuffix(reason, tc.label) || strings.Contains(reason, clientText) {
			t.Errorf("%s: audit reason %.80q, want a fixed label %s", name, reason, tc.label)
		}
	}
}

// TestAskStatesLedger: the single-use ledger bounds each subject's share and
// its total, frees expired entries, and lets exactly one of concurrent retries
// spend a state.
func TestAskStatesLedger(t *testing.T) {
	call := askBinding{Tenant: "t", Resource: rsResource, Subject: "agent:claude", Tool: "delete_db", Plan: "p"}
	other := call
	other.Subject = "agent:other"
	now := rsClock()
	a, err := newAskStates()
	if err != nil {
		t.Fatal(err)
	}
	redeem := func(b askBinding, at time.Time) error {
		state, err := a.seal(b, at)
		if err != nil {
			t.Fatal(err)
		}
		_, err = a.redeem(state, b, at)
		return err
	}
	for i := 0; i < maxSpentAskStatesPerSubject; i++ {
		if err := redeem(call, now); err != nil {
			t.Fatalf("redeem %d within the share: %v", i, err)
		}
	}
	if err := redeem(call, now); !errors.Is(err, errAskStateFull) {
		t.Fatalf("a subject past its share: %v, want %v", err, errAskStateFull)
	}
	if err := redeem(other, now); err != nil {
		t.Fatalf("another subject was starved by a full share: %v", err)
	}
	if err := redeem(call, now.Add(askStateTTL)); err != nil {
		t.Fatalf("expired entries were not freed: %v", err)
	}
	for i := 0; len(a.spent) < maxSpentAskStates; i++ {
		a.spent[fmt.Sprint("filler-", i)] = spentAskState{expires: now.Add(askStateTTL).Unix(), owner: fmt.Sprint("owner-", i)}
	}
	if err := redeem(other, now); !errors.Is(err, errAskStateFull) {
		t.Fatalf("a full ledger: %v, want %v", err, errAskStateFull)
	}

	a, err = newAskStates()
	if err != nil {
		t.Fatal(err)
	}
	state, err := a.seal(call, now)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := a.redeem(state, call, now); err == nil {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if won != 1 {
		t.Fatalf("%d concurrent retries spent one state, want 1", won)
	}
}

// TestAskRoundTripStateBelongsToOneCall: a state issued for one call does not
// complete another tool or other arguments.
func TestAskRoundTripStateBelongsToOneCall(t *testing.T) {
	a, err := newAskStates()
	if err != nil {
		t.Fatal(err)
	}
	call := askBinding{Tenant: "t", Resource: rsResource, Subject: "agent:claude", ClientID: "c", Tool: "delete_db", Plan: "p1"}
	for name, other := range map[string]askBinding{
		"tool":     {Tenant: "t", Resource: rsResource, Subject: "agent:claude", ClientID: "c", Tool: "drop_db", Plan: "p1"},
		"plan":     {Tenant: "t", Resource: rsResource, Subject: "agent:claude", ClientID: "c", Tool: "delete_db", Plan: "p2"},
		"subject":  {Tenant: "t", Resource: rsResource, Subject: "agent:other", ClientID: "c", Tool: "delete_db", Plan: "p1"},
		"client":   {Tenant: "t", Resource: rsResource, Subject: "agent:claude", ClientID: "d", Tool: "delete_db", Plan: "p1"},
		"resource": {Tenant: "t", Resource: "https://other.example", Subject: "agent:claude", ClientID: "c", Tool: "delete_db", Plan: "p1"},
		"tenant":   {Tenant: "u", Resource: rsResource, Subject: "agent:claude", ClientID: "c", Tool: "delete_db", Plan: "p1"},
	} {
		state, err := a.seal(call, rsClock())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.redeem(state, other, rsClock()); !errors.Is(err, errAskStateCall) {
			t.Errorf("%s: redeem = %v, want %v", name, err, errAskStateCall)
		}
		if _, err := a.redeem(state, call, rsClock()); err != nil {
			t.Errorf("%s: the state was spent by a refused redeem: %v", name, err)
		}
	}
	var unwired *askStates
	if _, err := unwired.redeem(askStatePrefix+"x", call, rsClock()); err == nil {
		t.Error("a server without the sealer accepted a state")
	}
}

// TestAskRoundTripStateKeyIsReserved: a case-variant alias of requestState is
// refused, so no state the gateway did not read can pass it.
func TestAskRoundTripStateKeyIsReserved(t *testing.T) {
	for _, params := range []string{
		`{"name":"t","arguments":{},"RequestState":"` + askStatePrefix + `x"}`,
		`{"name":"t","arguments":{},"requestState":"u","requeststate":"` + askStatePrefix + `x"}`,
	} {
		if _, err := canonicalizeToolCallParams(json.RawMessage(params)); err == nil {
			t.Errorf("accepted a case-variant requestState: %s", params)
		}
	}
}

// TestAskRoundTripOnlyStripsItsOwnState: an upstream's own MRTR state is
// forwarded untouched.
func TestAskRoundTripOnlyStripsItsOwnState(t *testing.T) {
	canon, err := canonicalizeToolCallParams(json.RawMessage(`{"name":"t","arguments":{},"requestState":"upstream-1","inputResponses":{"q":{"action":"accept"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if canon.AskState != "" || !strings.Contains(string(canon.Forward), `"requestState":"upstream-1"`) || len(canon.InputResponses) != 1 {
		t.Errorf("upstream state was touched: ask=%q forward=%s", canon.AskState, canon.Forward)
	}
	canon, err = canonicalizeToolCallParams(json.RawMessage(`{"name":"t","arguments":{},"requestState":"` + askStatePrefix + `x","inputResponses":{"` + askInputKey + `":{"action":"accept"},"q":{"action":"decline"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if canon.AskState != askStatePrefix+"x" || string(canon.AskResponse) != `{"action":"accept"}` ||
		strings.Contains(string(canon.Forward), "requestState") || strings.Contains(string(canon.Forward), askInputKey) ||
		len(canon.InputResponses) != 1 {
		t.Errorf("own state not isolated: ask=%q response=%s forward=%s", canon.AskState, canon.AskResponse, canon.Forward)
	}
}
