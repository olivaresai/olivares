// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// uiEngine is an engine with state, for the journeys of the tools screens: a tool
// that is installed once its install job runs, a sign-in that takes a code or a
// device flow, a provider that is registered and tested. It records every request
// that changes something, in order, because those are the events the audit ledger
// would hold: the screens and the commands must leave the same ones.
type uiEngine struct {
	*httptest.Server
	mu        sync.Mutex
	writes    []string
	reads     []string
	installed map[string]bool
	signedIn  map[string]bool
	providers []map[string]any
	// keys are the api_key values the engine received.
	keys []string
	// planned is the driver of the last plan; the job installs it.
	planned   string
	deleted   chan struct{}
	deletedOn sync.Once
	// accounts are the named provider accounts the engine lists.
	accounts []map[string]any
	// runs are the sessions the engine lists.
	runs []map[string]any
	// extraDrivers are driver ids the inventory names besides the five tools.
	extraDrivers []string
	// jobFails makes the install job end failed; rejectKey makes the engine
	// refuse a key, echoing it as a careless engine would; inventoryDown and
	// providersDown make those routes answer 500.
	jobFails, rejectKey, inventoryDown, providersDown bool
	// sealThenFail registers a key and then answers 502, as a proxy that drops the
	// answer does; jobHangs keeps the install job running; deleteFails makes the
	// engine refuse to end a sign-in.
	sealThenFail, jobHangs, deleteFails bool
	// planDelay is how long the engine takes to resolve and verify a release.
	planDelay time.Duration
	// readinessDown makes the readiness route answer 500; signInURL is the link a
	// device sign-in offers instead of the OpenAI one.
	readinessDown bool
	signInURL     string
	// userCode is the code a device sign-in asks the person to enter, instead of ABCD-1234.
	userCode string
	// signInStatusDown makes the tool's sign-in status route answer 500.
	signInStatusDown bool
	gates            map[string]*uiGate
	// providerRef is the reference a new provider gets instead of prv_01. A route
	// for a provider that nothing registered answers 404, so a test sees whether
	// a screen asked for one.
	providerRef string
	// jobPollDown makes the install job's status route answer 500; jobPolled is
	// closed when the job's status was first asked for, which is when the caller
	// holds the job's id.
	jobPollDown bool
	jobPolled   chan struct{}
	jobPolledOn sync.Once
	// signInGarbage makes the engine accept a sign-in and answer with a body no
	// client can read, as a proxy that cuts the answer does.
	signInGarbage bool
	// deviceState is what the device sign-in poll answers instead of "waiting":
	// "signed_in" completes it, "failed" ends it with the tool's reason.
	deviceState string
	// keyLogin makes OpenCode list its own provider first among its login methods
	// and wait for a key on it, as the real engine does; loginKeys are the keys
	// that login received.
	keyLogin  bool
	loginKeys []string
	// keyLoginNoLink makes that login wait for the key without naming a page.
	keyLoginNoLink bool
}

// uiGate holds the requests to one route until the test lets them go, so a
// journey can act while the engine has not answered yet.
type uiGate struct {
	arrived     chan struct{}
	release     chan struct{}
	once        sync.Once
	releaseOnce sync.Once
}

// open lets the held requests go.
func (g *uiGate) open() { g.releaseOnce.Do(func() { close(g.release) }) }

// hold makes the engine keep every request to method and path unanswered until
// the returned gate is released.
func (e *uiEngine) hold(method, path string) *uiGate {
	g := &uiGate{arrived: make(chan struct{}), release: make(chan struct{})}
	e.set(func() {
		if e.gates == nil {
			e.gates = map[string]*uiGate{}
		}
		e.gates[method+" "+path] = g
	})
	return g
}

func (e *uiEngine) gateFor(r *http.Request) *uiGate {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.gates[r.Method+" "+r.URL.Path]
}

const uiTestToken = "test-token"

// uiClaudeLink is longer than any terminal row, as a real authorize link is.
const uiClaudeLink = "https://claude.ai/oauth/authorize?code=true&client_id=00000000-0000-0000-0000-000000000000" +
	"&response_type=code&redirect_uri=https%3A%2F%2Fconsole.example.com%2Foauth%2Fcode%2Fcallback" +
	"&scope=org%3Acreate_api_key+user%3Aprofile+user%3Ainference&code_challenge=abcdefghijklmnopqrstuvwxyz0123456789ABC&state=xyz"

func newUIEngine(t *testing.T) *uiEngine {
	t.Helper()
	e := &uiEngine{installed: map[string]bool{}, signedIn: map[string]bool{}, deleted: make(chan struct{}), jobPolled: make(chan struct{})}
	e.Server = httptest.NewServer(http.HandlerFunc(e.serve))
	t.Cleanup(e.Close)
	return e
}

// set changes the engine's state under its lock: the process under test is
// already talking to it.
func (e *uiEngine) set(change func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	change()
}

func (e *uiEngine) serve(w http.ResponseWriter, r *http.Request) {
	if g := e.gateFor(r); g != nil {
		g.once.Do(func() { close(g.arrived) })
		<-g.release
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	var body map[string]any
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	if r.Method == http.MethodGet {
		e.reads = append(e.reads, r.URL.Path)
	} else {
		e.writes = append(e.writes, r.Method+" "+r.URL.Path+" "+redactedBody(body))
	}
	p := strings.TrimPrefix(r.URL.Path, agentToolsPath)
	if p == "/jobs/job-1" {
		e.jobPolledOn.Do(func() { close(e.jobPolled) })
	}
	switch {
	case r.URL.Path == "/livez":
		reply(200, map[string]any{"status": "ok"})
	case r.URL.Path == "/v1/auth/whoami":
		reply(200, map[string]any{"display_name": "Rehema", "user_id": "u1"})
	case r.URL.Path == profilesPath+"/readiness" && e.readinessDown:
		reply(500, map[string]any{"error": map[string]any{"code": "internal", "message": "The readiness check is down."}})
	case r.URL.Path == profilesPath+"/readiness":
		reply(200, e.readiness())
	case r.URL.Path == sessionRunsPath:
		reply(200, map[string]any{"items": append([]map[string]any{}, e.runs...)})
	case r.URL.Path == providerAccountsPath:
		reply(200, map[string]any{"items": append([]map[string]any{}, e.accounts...)})
	case r.URL.Path == providersPath && r.Method == "GET" && e.providersDown:
		reply(500, map[string]any{"error": map[string]any{"code": "internal", "message": "The provider list is down."}})
	case r.URL.Path == providersPath && r.Method == "GET":
		reply(200, map[string]any{"items": append([]map[string]any{}, e.providers...)})
	case r.URL.Path == providersPath && r.Method == "POST":
		key, _ := body["api_key"].(string)
		e.keys = append(e.keys, key)
		if e.rejectKey {
			reply(400, map[string]any{"error": map[string]any{"code": "invalid_key", "message": "The key " + key + " is not valid for this provider."}})
			return
		}
		ref := "prv_01"
		if e.providerRef != "" {
			ref = e.providerRef
		}
		rec := map[string]any{
			"provider_ref": ref, "kind": body["kind"], "display_name": body["display_name"],
			"key_hint": "…wxyz", "state": "active", "probe_state": "",
		}
		e.providers = append(e.providers, rec)
		if e.sealThenFail {
			reply(502, map[string]any{"error": map[string]any{"code": "bad_gateway", "message": "The gateway dropped the answer."}})
			return
		}
		reply(201, rec)
	case r.URL.Path == providersPath+"/prv_01/test":
		e.providers[0]["probe_state"] = "ok"
		reply(200, e.providers[0])
	case p == "/inventory" && e.inventoryDown:
		reply(500, map[string]any{"error": map[string]any{"code": "internal", "message": "The inventory is down."}})
	case p == "/inventory":
		var installed []map[string]any
		for d := range e.installed {
			installed = append(installed, map[string]any{"driver": d, "version": "2.1.286", "state": "installed", "installed_at": "2026-10-01T09:45:19Z"})
		}
		reply(200, map[string]any{
			"drivers":   append([]string{"claude", "codex", "grok", "opencode", "gemini-cli"}, e.extraDrivers...),
			"inventory": map[string]any{"installed": installed},
		})
	case p == "/plans":
		time.Sleep(e.planDelay)
		e.planned, _ = body["driver"].(string)
		reply(200, map[string]any{"digest": "d1", "driver": body["driver"], "version": "2.1.286", "verification": "signature"})
	case p == "/installs":
		reply(202, map[string]any{"id": "job-1", "driver": e.planned, "version": "2.1.286", "state": "running"})
	case p == "/jobs/job-1" && e.jobPollDown:
		reply(500, map[string]any{"error": map[string]any{"code": "internal", "message": "The job store is down."}})
	case p == "/jobs/job-1" && e.jobHangs:
		reply(200, map[string]any{"id": "job-1", "driver": e.planned, "state": "running"})
	case p == "/jobs/job-1" && e.jobFails:
		reply(200, map[string]any{"id": "job-1", "driver": e.planned, "state": "failed", "error": "No space left on the device."})
	case p == "/jobs/job-1":
		e.installed[e.planned] = true
		reply(200, map[string]any{"id": "job-1", "driver": e.planned, "version": "2.1.286", "state": "succeeded"})
	case p == "/sign-in" && r.Method == "GET" && e.signInStatusDown:
		reply(500, map[string]any{"error": map[string]any{"code": "internal", "message": "The sign-in status is down."}})
	case p == "/sign-in" && r.Method == "GET":
		d := r.URL.Query().Get("driver")
		account := ""
		if e.signedIn[d] {
			account = "ana@example.com"
		}
		status := map[string]any{"driver": d, "installed": e.installed[d], "signed_in": e.installed[d] && e.signedIn[d], "account": account}
		if d == "opencode" && e.keyLogin {
			status["methods"] = []map[string]any{{"id": "opencode", "label": "OpenCode", "key": true, "default": true}, {"id": "chatgpt", "label": "ChatGPT"}}
		}
		reply(200, status)
	case p == "/sign-in" && r.Method == "POST" && e.signInGarbage:
		w.WriteHeader(202)
		_, _ = w.Write([]byte("<html>not the engine"))
	case p == "/sign-in" && r.Method == "POST" && body["driver"] == "opencode" && body["method"] == "opencode":
		login := map[string]any{"id": "s3", "driver": "opencode", "state": "needs_key", "url": "https://opencode.ai/auth"}
		if e.keyLoginNoLink {
			delete(login, "url")
		}
		reply(202, login)
	case p == "/sign-in/s3/code":
		key, _ := body["code"].(string)
		e.loginKeys = append(e.loginKeys, key)
		e.signedIn["opencode"] = true
		reply(202, map[string]any{"id": "s3", "state": "signed_in"})
	case p == "/sign-in" && r.Method == "POST":
		if body["driver"] == "claude" {
			reply(202, map[string]any{"id": "s1", "driver": "claude", "state": "needs_code", "url": uiClaudeLink})
			return
		}
		link := "https://auth.openai.com/codex/device"
		if e.signInURL != "" {
			link = e.signInURL
		}
		code := "ABCD-1234"
		if e.userCode != "" {
			code = e.userCode
		}
		reply(202, map[string]any{"id": "s2", "driver": body["driver"], "state": "waiting", "url": link, "user_code": code})
	case p == "/sign-in/s1/code":
		if body["code"] != "good-code" {
			reply(202, map[string]any{"id": "s1", "state": "needs_code", "url": uiClaudeLink, "message": "That code was not accepted. Paste the newest code from the sign-in page."})
			return
		}
		e.signedIn["claude"] = true
		reply(202, map[string]any{"id": "s1", "state": "signed_in"})
	case p == "/sign-in/s2" && r.Method == "GET":
		switch e.deviceState {
		case "signed_in":
			e.signedIn["codex"] = true
			reply(200, map[string]any{"id": "s2", "state": "signed_in"})
		case "failed":
			reply(200, map[string]any{"id": "s2", "state": "failed", "message": "The device code expired. Start the sign-in again."})
		default:
			reply(200, map[string]any{"id": "s2", "state": "waiting"})
		}
	case strings.HasPrefix(p, "/sign-in/") && r.Method == "DELETE":
		e.deletedOn.Do(func() { close(e.deleted) })
		if e.deleteFails {
			reply(500, map[string]any{"error": map[string]any{"code": "internal", "message": "The login could not be ended."}})
			return
		}
		reply(200, map[string]any{})
	default:
		reply(404, map[string]any{"error": map[string]any{"code": "not_found", "message": "no route"}})
	}
}

// readiness says a tool can run a session once it is signed in, or a key for its
// provider is registered.
func (e *uiEngine) readiness() map[string]any {
	keyed := map[string]bool{}
	for _, p := range e.providers {
		if p["kind"] == "anthropic" {
			keyed["claude"] = true
		}
	}
	var tools []map[string]any
	for _, d := range sessionToolOrder {
		tools = append(tools, map[string]any{"driver": d, "ready": e.installed[d] && (e.signedIn[d] || keyed[d])})
	}
	return map[string]any{"tools": tools}
}

// redactedBody is a request body as the ledger would hold it, without what must
// never be kept: the key, the code and the random request id.
func redactedBody(body map[string]any) string {
	clean := map[string]any{}
	for k, v := range body {
		switch k {
		case "api_key", "code", "request_id":
			v = "<" + k + ">"
		}
		clean[k] = v
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // the placeholders are <request_id>, not \u003crequest_id\u003e
	_ = enc.Encode(clean)
	return strings.TrimSuffix(b.String(), "\n")
}

func (e *uiEngine) ledger() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.writes...)
}

func (e *uiEngine) receivedKeys() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.keys...)
}

func (e *uiEngine) receivedLoginKeys() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.loginKeys...)
}
