// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudemanagedagents

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// cmaFixtureServer serves canned CMA API responses on the real resource paths. Every
// response is keyed by the exact path the connector requests, so the test doubles as a
// contract check that the connector hits the documented endpoints. The /v1/sessions
// route additionally discriminates the memory_store_id filter (the dream attach probe)
// from the statuses list (the active-session poll).
func cmaFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	expired := testTime.Add(-time.Hour).Format(time.RFC3339)
	redactedAt := testTime.Add(-2 * time.Hour).Format(time.RFC3339)
	created := testTime.Add(-3 * time.Hour).Format(time.RFC3339)
	activeSession := `{"id":"sesn_act","status":"running","created_at":"` + created + `",
		"agent":{"id":"agent_1","version":1,"multiagent":{"type":"coordinator","agents":[{"type":"agent","id":"agent_sub"}]}},
		"resources":[{"type":"memory_store","memory_store_id":"memstore_1","access":"read_write"}],
		"vault_ids":["vlt_1"],
		"outcome_evaluations":[{"outcome_id":"outc_1","result":"satisfied","iteration":0,"completed_at":"` + created + `"}]}`
	attachedSession := `{"id":"sesn_other","status":"running","created_at":"` + created + `",
		"resources":[{"type":"memory_store","memory_store_id":"memstore_out","access":"read_write"}]}`
	routes := map[string]string{
		"/v1/vaults": `{"data":[{"id":"vlt_1","display_name":"prod","workspace_id":"ws_1"}],"has_more":false}`,
		"/v1/vaults/vlt_1/credentials": `{"data":[
			{"id":"vcrd_oauth","auth":{"type":"mcp_oauth","mcp_server_url":"https://mcp.example/api","expires_at":"` + expired + `"}},
			{"id":"vcrd_bearer","auth":{"type":"static_bearer","mcp_server_url":"https://mcp2.example/api"}}
		],"has_more":false}`,
		"/v1/memory_stores": `{"data":[{"id":"memstore_1","name":"prefs"}],"has_more":false}`,
		"/v1/memory_stores/memstore_1/memory_versions": `{"data":[
			{"id":"memver_r","memory_id":"m1","operation":"update","redacted_at":"` + redactedAt + `","created_at":"` + redactedAt + `"},
			{"id":"memver_c","memory_id":"m1","operation":"create","created_at":"` + redactedAt + `"}
		]}`,
		"/v1/environments": `{"data":[
			{"id":"env_1","name":"self","config":{"type":"self_hosted"}},
			{"id":"env_2","name":"cloud","config":{"type":"cloud"}}
		],"has_more":false}`,
		"/v1/environments/env_1/work/stats": `{"type":"work_queue_stats","depth":3,"pending":0,"workers_polling":0}`,
		"/v1/environments/env_1/work":       `{"data":[{"id":"work_1","data":{"id":"sesn_1","type":"session"},"environment_id":"env_1","state":"queued"}]}`,
		"/v1/agents": `{"data":[{"id":"agent_1","skills":[
			{"type":"custom","skill_id":"skill_1","version":"latest"},
			{"type":"anthropic","skill_id":"xlsx"}
		],"tools":[
			{"type":"mcp_toolset","mcp_server_name":"github","default_config":{"permission_policy":{"type":"always_ask"}},"configs":[{"name":"create_issue"}]}
		],"multiagent":{"type":"coordinator","agents":[{"type":"agent","id":"agent_sub"}]}}],"has_more":false}`,
		"/v1/sessions/sesn_act/threads": `{"data":[
			{"id":"sthr_1","session_id":"sesn_act","status":"running","agent":{"id":"agent_1"},"created_at":"` + created + `"},
			{"id":"sthr_2","session_id":"sesn_act","status":"running","parent_thread_id":"sthr_1","agent":{"id":"agent_sub"},"created_at":"` + created + `"}
		]}`,
		"/v1/dreams": `{"data":[{"id":"drm_1","status":"completed","model":{"id":"claude-opus-4-8"},
			"inputs":[{"type":"memory_store","memory_store_id":"memstore_1"},{"type":"sessions","session_ids":["sesn_1","sesn_2"]}],
			"outputs":[{"type":"memory_store","memory_store_id":"memstore_out"}],
			"session_id":"sesn_dream","created_at":"` + created + `","ended_at":"` + redactedAt + `",
			"usage":{"input_tokens":1200,"output_tokens":300}}]}`,
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantBeta := defaultBetaHeader
		if r.URL.Path == "/v1/dreams" {
			// Dreams require BOTH betas, comma-separated (verified 2026-06-10).
			wantBeta = defaultBetaHeader + "," + dreamsBetaSuffix
		}
		if got := r.Header.Get("anthropic-beta"); got != wantBeta {
			t.Errorf("missing/wrong anthropic-beta header on %s: got %q want %q", r.URL.Path, got, wantBeta)
		}
		path := r.URL.Path
		body, ok := routes[path]
		if path == "/v1/sessions" {
			switch {
			case r.URL.Query().Get("memory_store_id") == "memstore_out":
				// The dream attach probe: one foreign session mounts the unadmitted output.
				body, ok = `{"data":[`+attachedSession+`]}`, true
			case r.URL.Query()["statuses"] != nil:
				body, ok = `{"data":[`+activeSession+`]}`, true
			}
		}
		if !ok {
			t.Errorf("unexpected request path %q", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

func TestRefreshOnceEmitsCMASurface(t *testing.T) {
	srv := cmaFixtureServer(t)
	defer srv.Close()

	s := openTestSource(t, map[string]string{
		cfgAPIKey:  "sk-ant-test",
		cfgBaseURL: srv.URL,
		cfgRefresh: "1h",
	})
	sink := &fakeSink{}
	s.refreshOnce(context.Background(), sink)

	// Edges land on BOTH sides of the diff: SignalCMA observations (inventory carriers,
	// session mounts, threads, dream provenance) and SignalPolicy grants (vault
	// credentials, skills, declared agent tools, roster). Anything else is a bug.
	wantEdgeKinds := map[string]bool{
		kindVault: false, kindVaultCred: false, mcpServerResourceKind: false,
		kindMemoryStore: false, kindEnvironment: false, kindManagedAgent: false,
		kindSkill: false, "mcp.tool": false, kindAgentDef: false, kindDream: false,
	}
	permittedKinds := map[string]bool{}
	for _, e := range sink.edges() {
		switch e.Source {
		case model.SignalCMA:
		case model.SignalPolicy:
			permittedKinds[e.ResourceKind] = true
		default:
			t.Errorf("edge %+v carries an unexpected signal source", e)
		}
		if _, want := wantEdgeKinds[e.ResourceKind]; want {
			wantEdgeKinds[e.ResourceKind] = true
		}
	}
	for kind, seen := range wantEdgeKinds {
		if !seen {
			t.Errorf("missing expected edge ResourceKind %q", kind)
		}
	}
	// The grant-shaped kinds MUST be on the permitted side.
	for _, kind := range []string{kindSkill, "mcp.tool", kindAgentDef} {
		if !permittedKinds[kind] {
			t.Errorf("expected %q edges on the PERMITTED side (SignalPolicy)", kind)
		}
	}

	// Findings: vault lateral (posture/Medium), credential expired (Medium) + static_bearer
	// (Low), memory redaction (governance), work-queue backlog (High), skill unpinned,
	// read_write mount posture, outcome verdict, dream admission pending (Medium) +
	// unadmitted attach (High).
	bySubject := map[string][]model.FindingReport{}
	for _, f := range sink.findings() {
		bySubject[f.SubjectKind] = append(bySubject[f.SubjectKind], f)
		if f.DetailHash == "" {
			t.Errorf("finding %q has no hashed detail", f.Title)
		}
	}
	if fs := bySubject[kindVault]; len(fs) == 0 || fs[0].Severity != model.SeverityMedium {
		t.Errorf("expected a Medium vault lateral finding, got %+v", fs)
	}
	if fs := bySubject[kindVaultCred]; len(fs) < 2 {
		t.Errorf("expected an expired + static_bearer credential finding, got %d", len(fs))
	}
	if _, ok := sink.findingBySubjectKind(kindMemoryVersion); !ok {
		t.Error("expected a memory-version redaction finding")
	}
	if fs := bySubject[kindEnvironment]; len(fs) != 1 || fs[0].Severity != model.SeverityHigh {
		t.Errorf("expected one High work-queue backlog finding, got %+v", fs)
	}
	if _, ok := sink.findingBySubjectKind(kindSkill); !ok {
		t.Error("expected an unpinned-skill finding")
	}
	if _, ok := sink.findingBySubjectKind(kindOutcome); !ok {
		t.Error("expected a terminal outcome verdict finding")
	}
	// Dreams: the output store carries BOTH the pending-admission (Medium) and the
	// unadmitted-attach drift (High) findings; the rw-mount posture also lands on
	// memory-store subjects.
	var admissionPending, unadmittedAttach, rwMount bool
	for _, f := range bySubject[kindMemoryStore] {
		switch {
		case f.Severity == model.SeverityMedium && strings.Contains(f.Title, "awaiting HITL admission"):
			admissionPending = true
		case f.Severity == model.SeverityHigh && strings.Contains(f.Title, "WITHOUT HITL admission"):
			unadmittedAttach = true
		case f.Kind == findingPosture && strings.Contains(f.Title, "read_write"):
			rwMount = true
		}
	}
	if !admissionPending || !unadmittedAttach || !rwMount {
		t.Errorf("memory-store findings incomplete: admissionPending=%v unadmittedAttach=%v rwMount=%v (%+v)",
			admissionPending, unadmittedAttach, rwMount, bySubject[kindMemoryStore])
	}

	// The terminal dream emits its token usage as a CostSample segmented by CostType.
	costs := sink.costs()
	if len(costs) != 1 || costs[0].CostType != dreamCostType || costs[0].InputTokens != 1200 {
		t.Errorf("expected one dream cost sample (1200 in-tokens), got %+v", costs)
	}
}

// TestRefreshDreamsAdmittedOutputIsAcknowledged proves the operator-recorded admission
// flips the output store from pending-admission to the Info acknowledgment and silences
// the attach drift (deny-closed inverted by an explicit record, never by default).
func TestRefreshDreamsAdmittedOutputIsAcknowledged(t *testing.T) {
	srv := cmaFixtureServer(t)
	defer srv.Close()

	s := openTestSource(t, map[string]string{
		cfgAPIKey:           "sk-ant-test",
		cfgBaseURL:          srv.URL,
		cfgAdmittedOutputs:  "memstore_out",
		cfgObserveVaults:    "false",
		cfgObserveMemory:    "false",
		cfgObserveWorkQueue: "false",
		cfgObserveSkills:    "false",
		cfgObserveSessions:  "false",
	})
	sink := &fakeSink{}
	s.refreshOnce(context.Background(), sink)

	var ack, pending, attach bool
	for _, f := range sink.findings() {
		switch {
		case strings.Contains(f.Title, "ADMITTED for productive attach"):
			ack = f.Severity == model.SeverityInfo
		case strings.Contains(f.Title, "awaiting HITL admission"):
			pending = true
		case strings.Contains(f.Title, "WITHOUT HITL admission"):
			attach = true
		}
	}
	if !ack || pending || attach {
		t.Errorf("admitted output: ack=%v pending=%v attach=%v, want ack only (%+v)", ack, pending, attach, sink.findings())
	}
}

// TestRefreshDreamsGatedDegradesOnce proves the GATED research preview degrades to ONE
// declared posture finding (403/404) and the poller stops asking — honest absence,
// never fabricated data, never a finding per refresh.
func TestRefreshDreamsGatedDegradesOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/dreams" {
			http.Error(w, `{"type":"error","error":{"type":"permission_error"}}`, http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[],"has_more":false}`))
	}))
	defer srv.Close()

	s := openTestSource(t, map[string]string{
		cfgAPIKey:           "sk-ant-test",
		cfgBaseURL:          srv.URL,
		cfgObserveVaults:    "false",
		cfgObserveMemory:    "false",
		cfgObserveWorkQueue: "false",
		cfgObserveSkills:    "false",
		cfgObserveSessions:  "false",
	})
	sink := &fakeSink{}
	s.refreshOnce(context.Background(), sink)
	s.refreshOnce(context.Background(), sink) // second pass must NOT re-emit

	var gated []model.FindingReport
	for _, f := range sink.findings() {
		if f.SubjectRef == "dreams" {
			gated = append(gated, f)
		}
	}
	if len(gated) != 1 || gated[0].Kind != findingPosture {
		t.Fatalf("expected exactly ONE gated-preview posture finding across polls, got %+v", gated)
	}
	if !strings.Contains(gated[0].Title, "GATED") {
		t.Errorf("gated finding must declare the surface uncovered, got %q", gated[0].Title)
	}
}

// TestRefreshDegradesHonestly proves a fetch failure on one surface yields a self-audit
// finding rather than a silent gap, and does not abort the others.
func TestRefreshDegradesHonestly(t *testing.T) {
	// A server that 500s vaults but serves an empty page for everything else.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/vaults") {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[],"has_more":false}`))
	}))
	defer srv.Close()

	s := openTestSource(t, map[string]string{
		cfgAPIKey:           "sk-ant-test",
		cfgBaseURL:          srv.URL,
		cfgObserveMemory:    "false",
		cfgObserveWorkQueue: "false",
		cfgObserveSkills:    "false",
		cfgObserveSessions:  "false",
		cfgObserveDreams:    "false",
	})
	sink := &fakeSink{}
	s.refreshOnce(context.Background(), sink)

	f, ok := sink.findingBySubjectKind(connectorSubject)
	if !ok || f.Kind != findingSelfAudit {
		t.Fatalf("a failed vault fetch must emit a self-audit degrade finding, got %+v", sink.findings())
	}
}

// TestRefreshWorkQueuePinnedEnvironmentsAreInventoried proves operator-pinned
// environment_ids still produce the workspace→environment inventory edge (fix).
func TestRefreshWorkQueuePinnedEnvironmentsAreInventoried(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/environments/env_pin/work/stats":
			_, _ = w.Write([]byte(`{"type":"work_queue_stats","depth":0,"pending":0,"workers_polling":1}`))
		case "/v1/environments/env_pin/work":
			_, _ = w.Write([]byte(`{"data":[]}`))
		default:
			t.Errorf("unexpected request path %q (discovery must be skipped when pinned)", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	s := openTestSource(t, map[string]string{
		cfgAPIKey:          "sk-ant-test",
		cfgBaseURL:         srv.URL,
		cfgEnvironmentIDs:  "env_pin",
		cfgObserveVaults:   "false",
		cfgObserveMemory:   "false",
		cfgObserveSkills:   "false",
		cfgObserveSessions: "false",
		cfgObserveDreams:   "false",
	})
	sink := &fakeSink{}
	s.refreshOnce(context.Background(), sink)

	var envEdge bool
	for _, e := range sink.edges() {
		if e.ResourceKind == kindEnvironment && e.ResourceRef == "env_pin" {
			envEdge = true
		}
	}
	if !envEdge {
		t.Errorf("pinned environment must still be inventoried, got %+v", sink.edges())
	}
}
