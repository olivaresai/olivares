// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/modules/sessions"
)

// fakeSessionEngine serves the session endpoints `olivares session` uses, with
// state a test can preset and requests a test can read back.
type fakeSessionEngine struct {
	*httptest.Server
	mu         sync.Mutex
	runs       []map[string]any
	workspaces []map[string]any
	profiles   []map[string]any
	posts      map[string][]map[string]any // path -> decoded bodies
	hits       []string
	runQueries []url.Values     // the query of every GET on the runs list
	accounts   []map[string]any // GET provider-accounts: account_ref, name, driver
	// ignoresRunFilters makes the runs list answer as an engine older than the
	// state and account filters does: every run, whatever the query.
	ignoresRunFilters bool
	// attachRefusal, when set, is the sentence the engine refuses an attached image with.
	attachRefusal string
	// replay is written on attach before any input; reply after the first input.
	replay, reply []string
	sent          chan struct{}
	// refuseAttach answers the stream with 403; endAfterInput ends it after the
	// first input with no turn result.
	refuseAttach, endAfterInput bool
	// failOnAttach ends the stream at once and records the run as failed for this
	// reason, as the engine does when the tool exits before its first turn.
	failOnAttach string
	failed       map[string]string // run_ref -> reason, once failOnAttach has fired
	// launchWaits answers a new run with 202: it waits for a launch approval.
	launchWaits  bool
	launchDriver string
	// launchRunMode, when set, is the permission_mode the started run reports instead of
	// the one asked for, as when the engine makes a tool without an automatic mode ask.
	launchRunMode string
	// launchRefusal, when set, answers a new run with 422 and this sentence, as the
	// engine does for a permission preset the tool cannot honour. launchRefusalStatus
	// replaces the 422 (403: the caller is not a run administrator).
	launchRefusal       string
	launchRefusalStatus int
	// launchRefusalCode is the refusal's error code; empty is "unsupported_permission".
	launchRefusalCode string
	// cleanupRefusal, when set, answers a release (POST …/cleanup) with 409 and this
	// sentence, as the engine does for a worktree it will not remove unconfirmed.
	cleanupRefusal string
	// resolved is what POST provider-profiles/resolve answers (first-hour check); resolveRefusal,
	// when set, is its 409 sentence. Unset: the first active own-login profile of the
	// driver, else a new one, like the engine.
	resolved       map[string]any
	resolveRefusal string
	// resolveStatus is the refusal's status (default 409; 503 when the node cannot read
	// the tool's sign-in status).
	resolveStatus int
	// resolveFails are the drivers POST provider-profiles/resolve refuses with this
	// status (409: the login folder is taken; 503: the sign-in cannot be read), before
	// ready is read.
	resolveFails map[string]int
	// resolveCodes supplies a stable code for an injected per-tool resolve refusal.
	resolveCodes map[string]string
	// templatesStatus, when set, answers the built-in template list with this status and
	// the sentence "template lookup unavailable"; noTemplates answers it with no items.
	templatesStatus int
	noTemplates     bool
	// ready are the tools GET and POST provider-profiles/resolve answer 200 for; the
	// others are refused with 409. Nil: the preview is not served (404) and POST resolves any tool.
	ready map[string]bool
	// refused are ready tools whose only key its provider refused at the last test:
	// the readiness answer says key_refused while their resolve still answers.
	refused map[string]bool
	// moveRefusal, when set, answers POST …/move with 409 and this sentence.
	// handoffPreview is what GET …/handoff answers; handoffState is the state the
	// started session reports for its first turn (default: delivered).
	moveRefusal    string
	handoffPreview map[string]any
	handoffState   string
	// pinSeq numbers the pins POST …/pin sets, so pinned_at orders them as it does.
	pinSeq int
	// messages, when set, is what GET …/messages answers for every run.
	messages map[string]any
	// steerRefusal, when set, answers POST …/input with 501 business_required and this
	// sentence, as a Community engine does for a session another person started.
	steerRefusal string
	// item is what GET source-items answers; itemRefusal, when set, is its refusal
	// sentence (itemStatus, default 422); itemQueries records every query it received.
	item        map[string]any
	itemRefusal string
	itemStatus  int
	itemQueries []url.Values
	// shipTargets are the publication targets GET /v1/m/gitpublish/targets lists;
	// shipStatus and shipAnswer are what POST …/ships answers.
	shipTargets []map[string]any
	shipStatus  int
	shipAnswer  map[string]any
}

// handoffStateOrDelivered is what POST …/handoff answers for the first turn.
func (f *fakeSessionEngine) handoffStateOrDelivered() string {
	if f.handoffState == "" {
		return "delivered"
	}
	return f.handoffState
}

// handoffFirstTurn is the text the engine returns only when it was not delivered.
func (f *fakeSessionEngine) handoffFirstTurn() any {
	if f.handoffStateOrDelivered() == "delivered" {
		return nil
	}
	return "Summary text.\n\nTranscript of the earlier session (tool calls and results shortened): /srv/acct/olivares-handoff/r1-ab.md"
}

// newFakeSessionEngine starts the engine after every setup has run. A test that
// drives the CLI as a child process must preset its fields here: once the server is
// up, a write races with serve and the child's request gives -race no ordering edge.
func newFakeSessionEngine(t *testing.T, setup ...func(*fakeSessionEngine)) *fakeSessionEngine {
	t.Helper()
	f := &fakeSessionEngine{posts: map[string][]map[string]any{}, sent: make(chan struct{})}
	for _, s := range setup {
		s(f)
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeSessionEngine) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.hits = append(f.hits, r.Method+" "+r.URL.Path)
	f.mu.Unlock()
	writeJSON := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	var body map[string]any
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.posts[r.URL.Path] = append(f.posts[r.URL.Path], body)
		f.mu.Unlock()
	}
	path := r.URL.Path
	switch {
	case r.Method == "GET" && path == sessionWorkspacesPath:
		writeJSON(200, map[string]any{"items": f.workspaces})
	case r.Method == "GET" && strings.HasPrefix(path, sessionWorkspacesPath+"/"):
		ref := strings.TrimPrefix(path, sessionWorkspacesPath+"/")
		for _, ws := range f.workspaces {
			if ws["workspace_ref"] == ref {
				writeJSON(200, ws)
				return
			}
		}
		writeJSON(404, map[string]any{"error": "workspace not found"})
	case r.Method == "GET" && path == sessionSourceItemsPath:
		f.mu.Lock()
		f.itemQueries = append(f.itemQueries, r.URL.Query())
		f.mu.Unlock()
		if f.itemRefusal != "" {
			status := f.itemStatus
			if status == 0 {
				status = http.StatusUnprocessableEntity
			}
			writeJSON(status, map[string]any{"error": map[string]any{"message": f.itemRefusal}})
			return
		}
		writeJSON(200, f.item)
	case r.Method == "POST" && strings.HasSuffix(path, "/wiki"):
		writeJSON(200, map[string]any{"workspace_ref": strings.TrimSuffix(strings.TrimPrefix(path, sessionWorkspacesPath+"/"), "/wiki")})
	case r.Method == "POST" && path == sessionWorkspacesPath:
		ws := map[string]any{"workspace_ref": "ws-new", "root_path": body["root_path"], "state": "active"}
		writeJSON(201, ws)
	case r.Method == "GET" && path == "/v1/m/sessions/templates" && f.templatesStatus != 0:
		writeJSON(f.templatesStatus, map[string]any{"error": map[string]any{"message": "template lookup unavailable"}})
	case r.Method == "GET" && path == "/v1/m/sessions/templates" && f.noTemplates:
		writeJSON(200, map[string]any{"items": []map[string]any{}})
	case r.Method == "GET" && path == "/v1/m/sessions/templates":
		writeJSON(200, map[string]any{"items": []map[string]any{
			{"id": "tpl-read", "name": "Read only", "builtin": true},
			{"id": "tpl-edit-run", "name": "Edits and commands", "builtin": true},
		}})
	case r.Method == "GET" && path == profilesPath:
		writeJSON(200, map[string]any{"items": f.profiles})
	case r.Method == "POST" && path == profilesPath:
		writeJSON(201, map[string]any{"profile_ref": "ppf-new", "driver": body["driver"]})
	case r.Method == "GET" && path == profilesPath+"/readiness" && f.ready != nil:
		answer := fakeReadiness(f.ready)
		for _, tool := range answer["tools"].([]map[string]any) {
			if f.refused[tool["driver"].(string)] {
				tool["ready"], tool["code"], tool["message"] = false, "key_refused", refusedKeySentence(tool["driver"].(string))
			}
		}
		writeJSON(200, answer)
	case r.Method == "GET" && path == profilesPath+"/resolve" && f.ready != nil:
		if f.ready[r.URL.Query().Get("driver")] {
			writeJSON(200, map[string]any{"reason": "api_key"})
		} else {
			writeJSON(409, map[string]any{"error": map[string]any{"message": "not ready"}})
		}
	case r.Method == "POST" && path == profilesPath+"/resolve":
		switch {
		case f.resolveCodes[str(body, "driver")] != "":
			code := f.resolveCodes[str(body, "driver")]
			writeJSON(f.resolveFails[str(body, "driver")], map[string]any{"error": map[string]any{"code": code, "message": "resolve refused: " + code}})
		case f.resolveFails[str(body, "driver")] == 409:
			// A tool that could run, whose profile cannot be made: no readiness code.
			writeJSON(409, map[string]any{"error": map[string]any{"message": "an active or disabled profile already owns this home on this environment"}})
		case f.resolveFails[str(body, "driver")] == 503:
			writeJSON(503, map[string]any{"error": map[string]any{"code": "tool_signin_unreadable", "message": "the sign-in status of the tool could not be read on this node"}})
		case f.ready != nil && !f.ready[str(body, "driver")]:
			writeJSON(409, map[string]any{"error": map[string]any{"code": "nothing_to_run_on", "message": "not ready: " + str(body, "driver")}})
		case f.resolveRefusal != "":
			status := f.resolveStatus
			if status == 0 {
				status = 409
			}
			writeJSON(status, map[string]any{"error": map[string]any{"message": f.resolveRefusal}})
		case f.resolved != nil:
			writeJSON(200, f.resolved)
		default:
			profile := map[string]any{"profile_ref": "ppf-new-" + str(body, "driver"), "driver": body["driver"], "display_name": "Claude Code",
				"auth_source": "provider_account_home"}
			created := true
			for _, p := range f.profiles {
				if p["driver"] == body["driver"] && p["auth_source"] == "provider_account_home" {
					profile, created = p, false
					break
				}
			}
			writeJSON(200, map[string]any{"profile": profile, "reason": "own_login", "created": created})
		}
	case r.Method == "GET" && strings.HasPrefix(path, profilesPath+"/") && !strings.Contains(strings.TrimPrefix(path, profilesPath+"/"), "/"):
		ref := strings.TrimPrefix(path, profilesPath+"/")
		for _, p := range f.profiles {
			if p["profile_ref"] == ref {
				writeJSON(200, p)
				return
			}
		}
		writeJSON(404, map[string]any{"error": map[string]any{"code": "not_found", "message": "profile not found"}})
	case r.Method == "GET" && path == "/v1/m/gitpublish/targets":
		writeJSON(200, map[string]any{"items": f.shipTargets})
	case r.Method == "POST" && strings.HasPrefix(path, "/v1/m/gitpublish/targets/") && strings.HasSuffix(path, "/ships"):
		writeJSON(f.shipStatus, f.shipAnswer)
	case r.Method == "POST" && strings.HasSuffix(path, "/move") && f.moveRefusal != "":
		writeJSON(409, map[string]any{"error": map[string]any{"code": "conflict", "message": f.moveRefusal}})
	case r.Method == "GET" && strings.HasSuffix(path, "/handoff"):
		writeJSON(200, f.handoffPreview)
	case r.Method == "POST" && strings.HasSuffix(path, "/handoff"):
		writeJSON(201, map[string]any{
			"run_ref": "r-handoff", "name": body["name"], "state": "running", "provider_driver": "codex",
			"handoff_of": "r1", "summary_state": f.handoffStateOrDelivered(), "first_turn": f.handoffFirstTurn(),
		})
	case r.Method == "GET" && path == providerAccountsPath:
		writeJSON(200, map[string]any{"items": f.accounts})
	case r.Method == "GET" && path == sessionRunsPath:
		// The engine applies state and provider_profile_ref before it pages; so does this.
		query := r.URL.Query()
		f.mu.Lock()
		f.runQueries = append(f.runQueries, query)
		f.mu.Unlock()
		items := []map[string]any{}
		for _, run := range f.runs {
			if f.ignoresRunFilters || ((query.Get("state") == "" || run["state"] == query.Get("state")) &&
				(query.Get("provider_profile_ref") == "" || run["provider_profile_ref"] == query.Get("provider_profile_ref"))) {
				items = append(items, run)
			}
		}
		writeJSON(200, map[string]any{"items": items})
	case r.Method == "POST" && path == sessionRunsPath && f.launchRefusal != "":
		status := http.StatusUnprocessableEntity
		if f.launchRefusalStatus != 0 {
			status = f.launchRefusalStatus
		}
		code := "unsupported_permission"
		if f.launchRefusalCode != "" {
			code = f.launchRefusalCode
		}
		writeJSON(status, map[string]any{"error": map[string]any{"code": code, "message": f.launchRefusal}})
	case r.Method == "POST" && path == sessionRunsPath && f.launchWaits:
		w.Header().Set("Location", "/v1/m/governance/approvals/apr-1")
		writeJSON(202, map[string]any{
			"run_ref": "01a0f6dc-0000-7000-8000-000000000009", "name": body["name"], "state": "waiting_approval",
			"provider_driver": "claude", "transport": "stream-json", "workspace_path": "/srv/demo",
			"approval_ref": "apr-1", "approval_url": "/v1/m/governance/approvals/apr-1",
		})
	case r.Method == "POST" && path == sessionRunsPath:
		driver := f.launchDriver
		if driver == "" {
			driver = "claude"
		}
		// The run reports the mode it was stored with: the one asked for, unless the
		// engine made the tool ask instead (launchRunMode).
		mode := body["permission_mode"]
		if f.launchRunMode != "" {
			mode = f.launchRunMode
		}
		writeJSON(201, map[string]any{
			"run_ref": "01a0f6dc-0000-7000-8000-000000000009", "name": body["name"], "state": "running",
			"provider_driver": driver, "transport": "stream-json", "workspace_path": "/srv/demo",
			"permission_mode": mode,
		})
	case r.Method == "POST" && strings.HasSuffix(path, "/cleanup") && f.cleanupRefusal != "":
		writeJSON(409, map[string]any{"error": map[string]any{"code": "conflict", "message": f.cleanupRefusal}})
	case strings.HasSuffix(path, "/attach"):
		f.attach(w, r)
	case r.Method == "POST" && strings.HasSuffix(path, "/attachments"):
		if f.attachRefusal != "" {
			writeJSON(422, map[string]any{"error": map[string]any{"message": f.attachRefusal}})
			return
		}
		f.mu.Lock()
		n := len(f.posts[path])
		f.mu.Unlock()
		writeJSON(201, map[string]any{"id": fmt.Sprintf("att_%d", n), "media_type": body["media_type"]})
	case r.Method == "GET" && strings.HasSuffix(path, "/messages") && f.messages != nil:
		writeJSON(200, f.messages)
	case strings.HasSuffix(path, "/input") && f.steerRefusal != "":
		writeJSON(501, map[string]any{"error": map[string]any{"code": "business_required", "message": f.steerRefusal}})
	case strings.HasSuffix(path, "/input"):
		select {
		case <-f.sent:
		default:
			close(f.sent)
		}
		writeJSON(202, map[string]any{"accepted": true})
	case strings.HasPrefix(path, sessionRunsPath+"/"):
		ref := strings.Split(strings.TrimPrefix(path, sessionRunsPath+"/"), "/")[0]
		for _, run := range f.runs {
			if run["run_ref"] == ref {
				out := map[string]any{}
				for k, v := range run {
					out[k] = v
				}
				f.mu.Lock()
				if reason, ok := f.failed[ref]; ok {
					out["state"], out["reason"] = "failed", reason
				}
				f.mu.Unlock()
				switch {
				// The engine's interrupt answers: a relayed run has none, Claude may refuse
				// (409) or not answer in time (504), any other running turn is interrupted.
				case strings.HasSuffix(path, "/interrupt") && run["transport"] == "remote-control":
					writeJSON(409, map[string]any{"error": map[string]any{"code": "conflict",
						"message": "this session's provider has no turn interruption; stop it instead"}})
					return
				case strings.HasSuffix(path, "/interrupt") && run["interrupt"] == "refuse":
					writeJSON(409, map[string]any{"error": map[string]any{"code": "conflict",
						"message": "Claude Code refused the interrupt: a tool call is running"}})
					return
				case strings.HasSuffix(path, "/interrupt") && run["interrupt"] == "silent":
					writeJSON(504, map[string]any{"error": map[string]any{"code": "gateway_timeout",
						"message": "Claude Code did not confirm the interrupt in time; the turn may still be running"}})
					return
				case strings.HasSuffix(path, "/interrupt") && run["state"] == "idle":
					writeJSON(409, map[string]any{"error": map[string]any{"code": "conflict",
						"message": "there is no active provider turn to interrupt"}})
					return
				case strings.HasSuffix(path, "/peers") && r.Method == http.MethodPut:
					// The engine keeps one choice: a peers list or the same-template rule.
					delete(run, "peers_rule")
					run["peers"] = []any{}
					if rule, ok := body["peers_rule"]; ok {
						run["peers_rule"] = rule
					} else {
						run["peers"] = body["peers"]
					}
					out["peers"], out["peers_rule"] = run["peers"], run["peers_rule"]
				// The session's name and its archived mark (#2795) are the run's own: each
				// call answers the run, and an empty name clears it.
				case strings.HasSuffix(path, "/name") && r.Method == http.MethodPut:
					f.mu.Lock()
					delete(run, "name")
					if name := strings.TrimSpace(str(body, "name")); name != "" {
						run["name"] = name
					}
					f.mu.Unlock()
					out["name"] = run["name"]
					if run["name"] == nil {
						delete(out, "name")
					}
				// The title the engine cuts from the first message is display-only: the fake
				// makes one that names the style asked, so a test sees which one was sent.
				case strings.HasSuffix(path, "/title") && r.Method == http.MethodPost:
					style := str(body, "style")
					if style == "" {
						style = "sentence"
					}
					f.mu.Lock()
					run["title"] = "made " + style
					f.mu.Unlock()
					out["title"] = run["title"]
				case strings.HasSuffix(path, "/archive"):
					f.mu.Lock()
					if r.Method == http.MethodPost && run["archived_at"] == nil {
						run["archived_at"] = "2026-10-21T10:00:00Z"
					} else if r.Method == http.MethodDelete {
						delete(run, "archived_at")
					}
					f.mu.Unlock()
					delete(out, "archived_at")
					if at, ok := run["archived_at"]; ok {
						out["archived_at"] = at
					}
				// The caller's own pin (#2130): POST sets it, DELETE clears it, both answer
				// the pin state, and the run reads carry it as the engine's do.
				case strings.HasSuffix(path, "/pin"):
					pinned := r.Method == http.MethodPost
					answer := map[string]any{"run_ref": ref, "pinned": pinned}
					f.mu.Lock()
					run["pinned"] = pinned
					delete(run, "pinned_at")
					if pinned {
						f.pinSeq++
						run["pinned_at"] = fmt.Sprintf("2026-10-14T10:00:%02dZ", f.pinSeq)
						answer["pinned_at"] = run["pinned_at"]
					}
					f.mu.Unlock()
					writeJSON(200, answer)
					return
				// The caller's own snooze and done marks (#2480): each route answers both
				// marks as they now stand, and the run reads carry them.
				case strings.HasSuffix(path, "/snooze"), strings.HasSuffix(path, "/done"):
					f.mu.Lock()
					switch {
					case strings.HasSuffix(path, "/done"):
						run["done"] = r.Method == http.MethodPost
					case r.Method == http.MethodPost:
						run["snoozed_until"] = body["until"]
					default:
						delete(run, "snoozed_until")
					}
					done, _ := run["done"].(bool)
					answer := map[string]any{"run_ref": ref, "done": done}
					if until, ok := run["snoozed_until"]; ok {
						answer["snoozed_until"] = until
					}
					f.mu.Unlock()
					writeJSON(200, answer)
					return
				case strings.HasSuffix(path, "/stop"):
					out["state"] = "stopped"
				case strings.HasSuffix(path, "/cleanup"):
					out["state"] = "cleaned"
				}
				writeJSON(200, out)
				return
			}
		}
		writeJSON(404, map[string]any{"error": map[string]any{"code": "not_found", "message": "run not found"}})
	default:
		writeJSON(404, map[string]any{"error": map[string]any{"code": "not_found", "message": "no route"}})
	}
}

func (f *fakeSessionEngine) attach(w http.ResponseWriter, r *http.Request) {
	// The engine refuses the stream of a run that is not live, as it did on refresh 04 for
	// a launch waiting for approval.
	ref := strings.Split(strings.TrimPrefix(r.URL.Path, sessionRunsPath+"/"), "/")[0]
	for _, run := range f.runs {
		if run["run_ref"] == ref && run["state"] == "waiting_approval" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":{"code":"conflict","message":"session output is not available: the session is `+
				`waiting_approval (session is not live on this node; no bridged I/O stream)"}}`)
			return
		}
	}
	if f.refuseAttach {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"code":"forbidden","message":"output not authorized"}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	if f.failOnAttach != "" {
		f.mu.Lock()
		if f.failed == nil {
			f.failed = map[string]string{}
		}
		f.failed[ref] = f.failOnAttach
		f.mu.Unlock()
		fmt.Fprint(w, ": connected\n\n")
		flusher.Flush()
		return
	}
	frame := func(seq int, line string) {
		b, _ := json.Marshal(map[string]any{"seq": seq, "stream": "stdout", "line": line})
		fmt.Fprintf(w, "event: output\ndata: %s\n\n", b)
	}
	fmt.Fprint(w, ": connected\n\n")
	for i, line := range f.replay {
		frame(i+1, line)
	}
	flusher.Flush()
	select {
	case <-f.sent:
	case <-r.Context().Done():
		return
	}
	for i, line := range f.reply {
		frame(len(f.replay)+i+1, line)
	}
	flusher.Flush()
	if f.endAfterInput {
		return
	}
	<-r.Context().Done()
}

func (f *fakeSessionEngine) postsTo(path string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.posts[path]
}

const (
	claudeInitFrame   = `{"type":"system","subtype":"init","cwd":"/srv/demo","model":"claude-opus-5-5","tools":[]}`
	claudeLoginFrame  = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Not logged in · Please run /login"}]},"error":"authentication_failed"}`
	claudeLoginResult = `{"type":"result","subtype":"success","is_error":true,"result":"Not logged in · Please run /login","total_cost_usd":0,"duration_ms":65}`
)

func TestSessionStartRegistersTheFolderWithoutDLPAndNamesTheSessionAfterIt(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.profiles = []map[string]any{{"profile_ref": "ppf-claude", "driver": "claude", "state": "active", "auth_source": "provider_account_home"}}
	f.runs = []map[string]any{{"run_ref": "r-old", "name": "demo", "state": "running"}}
	dir := t.TempDir() + "/demo"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	out, errb, err := execSessionCLI(t, nil, append([]string{"session", "start", dir}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("start: %v\nstderr=%s", err, errb)
	}
	ws := f.postsTo(sessionWorkspacesPath)
	if len(ws) != 1 || ws[0]["root_path"] != dir || ws[0]["dlp_mode"] != "off" || ws[0]["mount_mode"] != "rw" {
		t.Fatalf("folder registration = %v, want root_path=%s dlp_mode=off mount_mode=rw", ws, dir)
	}
	runs := f.postsTo(sessionRunsPath)
	if len(runs) != 1 {
		t.Fatalf("launches = %d, want 1", len(runs))
	}
	got := runs[0]
	if got["name"] != "demo-2" || got["workspace_ref"] != "ws-new" || got["provider_profile_ref"] != "ppf-claude" {
		t.Fatalf("launch body = %v, want name demo-2 (demo is taken), ws-new, ppf-claude", got)
	}
	if !strings.Contains(out, "Started demo-2 (claude) in /srv/demo") || !strings.Contains(out, "olivares session send demo-2") {
		t.Fatalf("output does not say what started and what to do next:\n%s", out)
	}
	if len(f.postsTo(profilesPath)) != 0 {
		t.Fatal("an existing claude profile must be reused, not registered again")
	}
}

func TestSessionStartReusesARegisteredFolder(t *testing.T) {
	f := newFakeSessionEngine(t)
	dir := t.TempDir()
	f.workspaces = []map[string]any{{"workspace_ref": "ws-known", "root_path": dir, "state": "active"}}
	f.profiles = []map[string]any{{"profile_ref": "ppf-claude", "driver": "claude", "auth_source": "provider_account_home"}}
	if _, errb, err := execSessionCLI(t, nil, append([]string{"session", "start", dir, "--name", "x"}, sessionCreds(f.URL)...)...); err != nil {
		t.Fatalf("start: %v\n%s", err, errb)
	}
	if n := len(f.postsTo(sessionWorkspacesPath)); n != 0 {
		t.Fatalf("a registered folder was registered again (%d POSTs)", n)
	}
	if got := f.postsTo(sessionRunsPath)[0]["workspace_ref"]; got != "ws-known" {
		t.Fatalf("workspace_ref = %v, want ws-known", got)
	}
}

func TestSessionStartSaysThePromptComesAfterTheFolder(t *testing.T) {
	f := newFakeSessionEngine(t)
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "start", "fix the failing test"}, sessionCreds(f.URL)...)...)
	if exitcode.From(err) != exitcode.Usage || !strings.Contains(err.Error(), "Give the folder first") {
		t.Fatalf("err = %v (code %d), want a usage error that names the order", err, exitcode.From(err))
	}
	if len(f.hits) != 0 {
		t.Fatalf("a usage error reached the engine: %v", f.hits)
	}
}

func TestSessionTurnBodyFollowsTheConsoleRule(t *testing.T) {
	claude, err := sessionTurnBody(map[string]any{"provider_driver": "claude"}, "hello")
	if err != nil {
		t.Fatal(err)
	}
	var frame struct {
		Type    string `json:"type"`
		Message struct{ Role, Content string }
	}
	if err := json.Unmarshal([]byte(claude["line"].(string)), &frame); err != nil ||
		frame.Type != "user" || frame.Message.Role != "user" || frame.Message.Content != "hello" {
		t.Fatalf("claude body = %v, want a stream-json user frame", claude)
	}
	legacy, _ := sessionTurnBody(map[string]any{}, "hello")
	if _, ok := legacy["line"]; !ok {
		t.Fatalf("a run with no driver is the historical Claude path, got %v", legacy)
	}
	codex, _ := sessionTurnBody(map[string]any{"provider_driver": "codex"}, "hello")
	if codex["text"] != "hello" || len(codex) != 1 {
		t.Fatalf("codex body = %v, want {text}", codex)
	}
}

func TestSessionNameResolvesToTheNewestSession(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{
		{"run_ref": "r-new", "name": "demo", "state": "running", "created_at": "2026-10-01T10:00:00Z"},
		{"run_ref": "r-old", "name": "demo", "state": "stopped", "created_at": "2026-10-01T09:00:00Z"},
	}
	out, errb, err := execSessionCLI(t, nil, append([]string{"session", "stop", "demo"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("stop: %v\n%s", err, errb)
	}
	if len(f.postsTo(sessionRunsPath+"/r-new/stop")) != 1 {
		t.Fatalf("stop went elsewhere: %v", f.hits)
	}
	if strings.TrimSpace(out) != "Stopped demo." {
		t.Fatalf("stop output = %q, want one sentence", out)
	}
}

func TestSessionUnknownNameIsNotFound(t *testing.T) {
	f := newFakeSessionEngine(t)
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "show", "nope"}, sessionCreds(f.URL)...)...)
	if exitcode.From(err) != exitcode.NotFound || !strings.Contains(err.Error(), "List them: olivares session ls") {
		t.Fatalf("err = %v, want not-found naming `session ls`", err)
	}
}

func TestSessionSendShowsOnlyTheReplyUntilTheTurnEnds(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "r1", "name": "demo", "state": "running", "provider_driver": "claude", "transport": "stream-json"}}
	f.replay = []string{claudeInitFrame, `{"type":"assistant","message":{"content":[{"type":"text","text":"an OLD answer"}]}}`}
	f.reply = []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls -la"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Two files: README and main.go."}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"Two files: README and main.go.","total_cost_usd":0.0123,"duration_ms":3200}`,
	}
	out, errb, err := execSessionCLI(t, nil, append([]string{"session", "send", "demo", "list the files"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("send: %v\n%s", err, errb)
	}
	input := f.postsTo(sessionRunsPath + "/r1/input")
	if len(input) != 1 || !strings.Contains(fmt.Sprint(input[0]["line"]), `"content":"list the files"`) {
		t.Fatalf("input body = %v, want the sentence as a user frame", input)
	}
	for _, want := range []string{"→ Bash ls -la", "Two files: README and main.go.", "— turn finished · $0.0123 · 3.2s"} {
		if !strings.Contains(out, want) {
			t.Fatalf("reply is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "OLD answer") {
		t.Fatalf("send replayed output from before the turn:\n%s", out)
	}
}

func TestSessionSendToAStoppedSessionSaysToResume(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "r1", "name": "demo", "state": "stopped"}}
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "send", "demo", "hi"}, sessionCreds(f.URL)...)...)
	if exitcode.From(err) != exitcode.Conflict || !strings.Contains(err.Error(), "Resume it first: olivares session resume demo") {
		t.Fatalf("err = %v, want a conflict that names resume", err)
	}
	if len(f.postsTo(sessionRunsPath+"/r1/input")) != 0 {
		t.Fatal("input was sent to a stopped session")
	}
}

func TestSessionRemoveRefusesARunningSession(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "r1", "name": "demo", "state": "running"}}
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "rm", "demo"}, sessionCreds(f.URL)...)...)
	if exitcode.From(err) != exitcode.Conflict || !strings.Contains(err.Error(), "Stop it first: olivares session stop demo") {
		t.Fatalf("err = %v, want a conflict that names stop", err)
	}
}

func TestSessionViewTellsTheTurnOnce(t *testing.T) {
	var b strings.Builder
	v := newSessionView(&b)
	v.driver = "claude"
	for _, line := range []string{claudeInitFrame, claudeLoginFrame} {
		if v.render(line) {
			t.Fatalf("%s ended the turn", line)
		}
	}
	if !v.render(claudeLoginResult) || !v.failed {
		t.Fatal("the result frame must end the turn, as a failure")
	}
	want := "● session ready · claude-opus-5-5 · /srv/demo\nNot logged in · Please run /login\n— turn failed · 65ms\n" +
		"Claude Code is not signed in. Sign it in: olivares tool login claude\n"
	if b.String() != want {
		t.Fatalf("view =\n%s\nwant\n%s", b.String(), want)
	}
}

// A refusal of a credential the engine supplies (a key from Providers, a
// workload identity, an adapter) is said honestly in one sentence; telling the person to sign the
// tool in would send them the wrong way.
func TestSessionViewNamesAnEngineSuppliedCredentialHonestly(t *testing.T) {
	var b strings.Builder
	v := newSessionView(&b)
	v.driver, v.engineSupplied = "claude", true
	for _, line := range []string{claudeInitFrame, claudeLoginFrame, claudeLoginResult} {
		v.render(line)
	}
	got := b.String()
	if !strings.Contains(got, "The credential the engine supplies for this session was refused by the provider. Ask the engine's operator to replace it (a key from Providers is replaced in Providers), then send again.") ||
		strings.Contains(got, "olivares tool login") {
		t.Fatalf("view =\n%s\nwant the engine-supplied credential named, not a sign-in", got)
	}
}

// The sentence is chosen from the run alone: a profile rebound after launch and a profile the
// reader may not read get the same answer, because nothing reads the profile.
func TestEngineSuppliedReadsOnlyTheRun(t *testing.T) {
	for name, tc := range map[string]struct {
		run  map[string]any
		want bool
	}{
		"profile rebound after launch": {map[string]any{"provider_auth_source": sessions.AuthSourceManagedInjection, "provider_profile_ref": "ppf_rebound"}, true},
		"profile read denied":          {map[string]any{"provider_auth_source": sessions.AuthSourceManagedInjection, "provider_profile_ref": "ppf_denied"}, true},
		"the tool's own sign-in":       {map[string]any{"provider_auth_source": sessions.AuthSourceAccountHome}, false},
	} {
		if got := engineSupplied(tc.run); got != tc.want {
			t.Errorf("%s: engineSupplied = %v, want %v", name, got, tc.want)
		}
	}
}

func TestSessionViewKeepsLinesItCannotName(t *testing.T) {
	var b strings.Builder
	v := newSessionView(&b)
	v.render("Claude configuration file not found")
	// turn/started is known progress and stays silent; an unknown method is one line.
	v.render(`{"jsonrpc":"2.0","method":"turn/started","params":{}}`)
	v.render(`{"jsonrpc":"2.0","method":"thing/happened","params":{}}`)
	if got := b.String(); got != "Claude configuration file not found\n· thing/happened\n" {
		t.Fatalf("view = %q", got)
	}
}

func TestSessionSendExitsOneWhenTheTurnFails(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "r1", "name": "demo", "state": "running", "provider_driver": "claude"}}
	f.reply = []string{claudeInitFrame, claudeLoginFrame, claudeLoginResult}
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "send", "demo", "hi"}, sessionCreds(f.URL)...)...)
	if exitcode.From(err) != exitcode.Err || !exitcode.Silent(err) {
		t.Fatalf("err = %v, want a silent exit 1 after the failure is shown", err)
	}
	if !strings.Contains(out, "Sign it in: olivares tool login claude") {
		t.Fatalf("a signed-out tool must say what to do:\n%s", out)
	}
}

func TestSessionViewShowsTheReadyLineOnce(t *testing.T) {
	var b strings.Builder
	v := newSessionView(&b)
	for _, line := range []string{claudeInitFrame, claudeLoginResult, claudeInitFrame, claudeLoginResult} {
		v.render(line)
	}
	if n := strings.Count(b.String(), "session ready"); n != 1 {
		t.Fatalf("ready line shown %d times:\n%s", n, b.String())
	}
}

// The three SR2 findings on 36e31097, kept as regressions.

func TestSessionSendNeverSendsWhenTheStreamIsRefused(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "r1", "name": "demo", "state": "running", "provider_driver": "claude"}}
	f.refuseAttach = true
	for i := 0; i < 24; i++ {
		if _, _, err := execSessionCLI(t, nil, append([]string{"session", "send", "demo", "hi"}, sessionCreds(f.URL)...)...); err == nil {
			t.Fatal("a refused output stream reported success")
		}
	}
	if n := len(f.postsTo(sessionRunsPath + "/r1/input")); n != 0 {
		t.Fatalf("input sent %d times behind a refused stream", n)
	}
}

func TestSessionSendFailsWhenTheStreamEndsBeforeTheTurn(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "r1", "name": "demo", "state": "running", "provider_driver": "claude"}}
	f.endAfterInput = true
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "send", "demo", "hi"}, sessionCreds(f.URL)...)...)
	if exitcode.From(err) != exitcode.Err || !strings.Contains(err.Error(), "ended before the turn finished") {
		t.Fatalf("err = %v, want the turn reported as unfinished", err)
	}
}

func TestSessionStartRefusesAnUnknownToolBeforeRegisteringAnything(t *testing.T) {
	f := newFakeSessionEngine(t)
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "--tool", "typo"}, sessionCreds(f.URL)...)...)
	if exitcode.From(err) != exitcode.Usage {
		t.Fatalf("err = %v, want usage", err)
	}
	if len(f.hits) != 0 {
		t.Fatalf("an unknown tool reached the engine: %v", f.hits)
	}
}

func TestSessionViewDropsTerminalControls(t *testing.T) {
	var b strings.Builder
	v := newSessionView(&b)
	frame, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{
		{"type": "text", "text": "normal\u001b]52;c;YXR0YWNr\u0007 text \u001b[31mred\u001b[0m\u009b2J end"},
	}}})
	v.render(string(frame))
	v.render("plain \u001b]0;title\u001b\\ line\twith tab")
	got := b.String()
	if strings.ContainsAny(got, "\x1b\x07\u009b") {
		t.Fatalf("control sequences reached the terminal: %q", got)
	}
	if !strings.Contains(got, "normal text red2J end") || !strings.Contains(got, "plain  line\twith tab") {
		t.Fatalf("the text around the sequences was lost: %q", got)
	}
	if termSafe("ok\nnext") != "ok\nnext" {
		t.Fatal("newlines are text")
	}
}

// TestSessionStartRefusesWhenTheEditsAndCommandsTemplateCannotBeResolved (SR2 report 194):
// edits-and-commands is the engine's built-in template, sent as template_id with an empty
// permission_mode. When the template list fails or has no such template, the CLI sent the
// start without it, and the engine could then apply the profile's stored mode. A session
// never starts without the preset the person chose (or the default they accepted): the
// CLI refuses before it registers the folder or posts the start, and keeps the engine's
// sentence.
func TestSessionStartRefusesWhenTheEditsAndCommandsTemplateCannotBeResolved(t *testing.T) {
	for _, tool := range []string{"claude", "codex", "grok", "opencode", "gemini-cli", "cursor"} {
		for _, tc := range []struct {
			name     string
			set      func(*fakeSessionEngine)
			code     int
			sentence string
		}{
			{"503", func(f *fakeSessionEngine) { f.templatesStatus = http.StatusServiceUnavailable },
				exitcode.Server, "template lookup unavailable"},
			{"missing", func(f *fakeSessionEngine) { f.noTemplates = true },
				exitcode.NotFound, "this engine has none"},
		} {
			for _, permission := range [][]string{{"--permission", "edits-and-commands"}, nil} {
				label := tool + "/" + tc.name + "/explicit"
				if permission == nil {
					label = tool + "/" + tc.name + "/default"
				}
				t.Run(label, func(t *testing.T) {
					f := newFakeSessionEngine(t)
					tc.set(f)
					args := append([]string{"session", "start", t.TempDir(), "--tool", tool}, permission...)
					out, errb, err := execSessionCLI(t, nil, append(args, sessionCreds(f.URL)...)...)
					if err == nil {
						t.Fatalf("start went on without the Edits and commands template\nstdout=%s\nstderr=%s", out, errb)
					}
					if got := exitcode.From(err); got != tc.code {
						t.Fatalf("exit code = %d, want %d (%v)", got, tc.code, err)
					}
					for _, want := range []string{"edits-and-commands", tc.sentence} {
						if !strings.Contains(err.Error(), want) {
							t.Fatalf("error %q does not say %q", err, want)
						}
					}
					if runs := f.postsTo(sessionRunsPath); len(runs) != 0 {
						t.Fatalf("the CLI posted a start without the preset: %v", runs)
					}
					if ws := f.postsTo(sessionWorkspacesPath); len(ws) != 0 {
						t.Fatalf("the CLI registered the folder before refusing: %v", ws)
					}
				})
			}
		}
	}
}

func TestSessionStartUsesTheConsolesDefaultsForAClaudeSession(t *testing.T) {
	f := newFakeSessionEngine(t)
	if _, errb, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir()}, sessionCreds(f.URL)...)...); err != nil {
		t.Fatalf("start: %v\n%s", err, errb)
	}
	// The profile is the engine's choice (provider-profiles/resolve, first-hour check), the same as
	// the console's; the CLI no longer creates one, nor sets session_tools.
	if prof := f.postsTo(profilesPath); len(prof) != 0 {
		t.Fatalf("the CLI created a profile itself: %v", prof)
	}
	if res := f.postsTo(profilesPath + "/resolve"); len(res) != 1 || res[0]["driver"] != "claude" || len(res[0]) != 1 {
		t.Fatalf("resolve requests = %v, want one {\"driver\":\"claude\"}", res)
	}
	run := f.postsTo(sessionRunsPath)[0]
	if run["template_id"] != "tpl-edit-run" || run["permission_mode"] != "" {
		t.Fatalf("launch = %v, want the built-in Edits and commands template", run)
	}
	if _, _, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "--permission", "read-only"}, sessionCreds(f.URL)...)...); err != nil {
		t.Fatal(err)
	}
	run = f.postsTo(sessionRunsPath)[1]
	if run["template_id"] != nil || run["permission_mode"] != "plan" {
		t.Fatalf("read-only launch = %v, want permission_mode plan and no template", run)
	}
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "--permission", "everything"}, sessionCreds(f.URL)...)...)
	if exitcode.From(err) != exitcode.Usage {
		t.Fatalf("unknown permission: err = %v, want usage", err)
	}
}

// A tiny PNG: the signature and an IHDR are all http.DetectContentType needs.
var sessionTestPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89")

func TestSessionSendAttachStoresTheImageThenSendsItsIdWithTheText(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "r1", "name": "demo", "state": "running", "provider_driver": "claude", "transport": "stream-json"}}
	f.replay = []string{claudeInitFrame}
	f.reply = []string{`{"type":"result","subtype":"success","is_error":false,"result":"ok","total_cost_usd":0,"duration_ms":1}`}
	shot := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(shot, sessionTestPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	_, errb, err := execSessionCLI(t, nil, append([]string{"session", "send", "demo", "--attach", shot, "what is wrong here?"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("send --attach: %v\n%s", err, errb)
	}
	up := f.postsTo(sessionRunsPath + "/r1/attachments")
	if len(up) != 1 || up[0]["media_type"] != "image/png" || up[0]["data"] != base64.StdEncoding.EncodeToString(sessionTestPNG) {
		t.Fatalf("upload = %v, want the PNG as base64 with its type read from the bytes", up)
	}
	in := f.postsTo(sessionRunsPath + "/r1/input")
	if len(in) != 1 || in[0]["text"] != "what is wrong here?" || fmt.Sprint(in[0]["attachments"]) != "[att_1]" {
		t.Fatalf("input = %v, want the text and the stored id", in)
	}
	if _, raw := in[0]["line"]; raw {
		t.Fatalf("a raw frame was sent with the image: %v", in[0])
	}
	var order []string
	for _, h := range f.hits {
		if strings.HasSuffix(h, "/attachments") || strings.HasSuffix(h, "/input") {
			order = append(order, h[strings.LastIndex(h, "/")+1:])
		}
	}
	if strings.Join(order, ",") != "attachments,input" {
		t.Fatalf("order = %v, want the image stored before the message", order)
	}
}

func TestSessionSendAttachSendsNothingWhenTheEngineRefusesTheImage(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "r1", "name": "demo", "state": "running", "provider_driver": "opencode", "transport": "stream-json"}}
	f.attachRefusal = "OpenCode does not take images"
	shot := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(shot, sessionTestPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "send", "demo", "--attach", shot, "look"}, sessionCreds(f.URL)...)...)
	if err == nil || !strings.Contains(err.Error(), "OpenCode does not take images") {
		t.Fatalf("err = %v, want the engine's sentence that names the tool", err)
	}
	if n := len(f.postsTo(sessionRunsPath + "/r1/input")); n != 0 {
		t.Fatalf("the message was sent after the image was refused (%d inputs)", n)
	}
}

func TestSessionSendAttachNamesAFileItCannotRead(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "r1", "name": "demo", "state": "running", "provider_driver": "claude", "transport": "stream-json"}}
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "send", "demo", "--attach", "/no/such/shot.png", "look"}, sessionCreds(f.URL)...)...)
	if err == nil || !strings.Contains(err.Error(), "Cannot read /no/such/shot.png: no such file or directory") {
		t.Fatalf("err = %v, want a sentence that names the file", err)
	}
	if n := len(f.postsTo(sessionRunsPath + "/r1/input")); n != 0 {
		t.Fatalf("the message was sent without its image (%d inputs)", n)
	}
}
