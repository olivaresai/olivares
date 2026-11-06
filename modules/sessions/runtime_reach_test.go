// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	accessmap "github.com/olivaresai/olivares/modules/access-map"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// reachEdge is one access edge a session's start left in the map, with the
// resource and origin references its metadata names.
type reachEdge struct {
	edge   model.AccessEdge
	kind   string
	ref    string
	origin string
	tool   string
}

func reachEdgesOf(t *testing.T, m *Module, tenant model.TenantID) []reachEdge {
	t.Helper()
	var out []reachEdge
	err := m.Data.View(context.Background(), tenant, func(sc store.Scope) error {
		edges, _, err := sc.AccessEdges().List(context.Background(), model.Query{Limit: 100})
		if err != nil {
			return err
		}
		for _, e := range edges {
			r := reachEdge{edge: e}
			r.kind, _ = e.Metadata["resource_kind"].(string)
			r.ref, _ = e.Metadata["resource_ref"].(string)
			r.origin, _ = e.Metadata["origin_ref"].(string)
			r.tool, _ = e.Metadata["tool_ref"].(string)
			out = append(out, r)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("list access edges: %v", err)
	}
	return out
}

func waitReachEdges(t *testing.T, m *Module, tenant model.TenantID, want int) []reachEdge {
	t.Helper()
	var got []reachEdge
	waitFor(t, fmt.Sprintf("%d access edges (last: %+v)", want, got), func() bool {
		got = reachEdgesOf(t, m, tenant)
		return len(got) == want
	})
	return got
}

func findReach(edges []reachEdge, kind, ref, origin string) (reachEdge, bool) {
	for _, e := range edges {
		if e.kind == kind && e.ref == ref && (origin == "" || e.origin == origin) {
			return e, true
		}
	}
	return reachEdge{}, false
}

func launchRefOf(t *testing.T, dto runDTO) string {
	t.Helper()
	identity, ok := dto.Completion.Identity()
	if !ok {
		t.Fatal("successful launch has no completion identity")
	}
	return identity.RuntimeLaunchID.String()
}

// reachHost connects the real sessions and access-map modules through the real
// in-process bus. No store write stands in for the publication being tested.
type reachHost struct{ bus eventbus.Bus }

func (h *reachHost) Publish(ctx context.Context, e event.Event) error { return h.bus.Publish(ctx, e) }
func (h *reachHost) Subscribe(types []event.Type, handler event.Handler) (func(), error) {
	sub, err := h.bus.Subscribe(types, handler)
	if err != nil {
		return nil, err
	}
	return sub.Unsubscribe, nil
}
func (h *reachHost) Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
func (h *reachHost) Config() sdk.Config { return sdk.Config{} }

func newReachRuntimeHarness(t *testing.T, opts ...Option) (*Module, store.Store, model.TenantID) {
	t.Helper()
	m, st, tenant, _ := newRuntimeHarness(t, opts...)
	m.UseExecutionEnvironmentRef(testEnvRef)
	host := &reachHost{bus: eventbus.NewInProc(eventbus.Options{})}
	am := accessmap.New()
	am.UseData(api.NewModuleData(st))
	if err := am.Init(context.Background(), host); err != nil {
		t.Fatalf("access-map init: %v", err)
	}
	t.Cleanup(func() { _ = am.Stop(context.Background()) })
	if err := m.Init(context.Background(), host); err != nil {
		t.Fatalf("sessions init: %v", err)
	}
	return m, st, tenant
}

type reachMCP struct{ names []string }

func (s reachMCP) ConfigureSessionMCP(_ context.Context, _ model.TenantID, _, _ string, spec *LaunchSpec) (func(), error) {
	spec.SessionMCPReach = append([]string(nil), s.names...)
	return func() {}, nil
}

// reachProc is a protocol-faithful Codex peer for the launch handshake. Unknown
// methods fail rather than supplying a catch-all success.
type reachProc struct{ *fakeProc }

func (p *reachProc) Send(ctx context.Context, line []byte) error {
	if err := p.fakeProc.Send(ctx, line); err != nil {
		return err
	}
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Model         string `json:"model"`
			ModelProvider string `json:"modelProvider"`
		} `json:"params"`
	}
	if err := json.Unmarshal(line, &req); err != nil {
		return err
	}
	if req.Method == "initialized" {
		return nil
	}
	var result any
	switch req.Method {
	case "initialize":
		result = map[string]any{"userAgent": "reach-fixture/1"}
	case "account/read":
		result = map[string]any{"account": map[string]any{"type": "chatgpt"}, "requiresOpenaiAuth": false}
	case "thread/start", "thread/resume":
		model := req.Params.Model
		if model == "" {
			model = "test-model"
		}
		result = map[string]any{
			"thread":        map[string]any{"id": "codex-reach", "parentThreadId": nil, "agentRole": nil},
			"model":         model,
			"modelProvider": req.Params.ModelProvider,
		}
	case "thread/unsubscribe":
		result = map[string]any{"status": "unsubscribed"}
	default:
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "unexpected method: " + req.Method}})
		p.out <- OutputFrame{Stream: streamStdout, Data: raw}
		return nil
	}
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	p.out <- OutputFrame{Stream: streamStdout, Data: raw}
	return nil
}

type reachRunner struct {
	mu    sync.Mutex
	procs []*reachProc
}

func (r *reachRunner) Launch(context.Context, LaunchSpec) (Process, error) {
	p := &reachProc{fakeProc: &fakeProc{out: make(chan OutputFrame, 16), stopped: make(chan struct{})}}
	r.mu.Lock()
	r.procs = append(r.procs, p)
	r.mu.Unlock()
	return p, nil
}

func createReachProfile(t *testing.T, m *Module, tenant model.TenantID, driver, kind string) ProviderProfile {
	t.Helper()
	record := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{
		Kind: kind, DisplayName: driver + " reach provider", APIKey: testProviderKey,
	})
	return mustCreateProfile(t, m, tenant, CreateProfileInput{
		Driver: driver, ConfigHome: t.TempDir(), UserHome: t.TempDir(), DisplayName: driver + " reach",
		AuthSource: AuthSourceManagedInjection, ProviderRecordRef: record.Ref,
	})
}

func startReachRun(t *testing.T, m *Module, tenant model.TenantID, profile ProviderProfile, workspace, mode string) runDTO {
	t.Helper()
	dto, err := createProfiledTestRun(t, m, context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, WorkspaceRef: workspace,
		PermissionMode: mode, Model: "test-model", ProviderProfileRef: profile.Ref,
		Actor: "user:u1", ActorKind: model.ActorUser,
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if dto.State != stateRunning {
		t.Fatalf("state = %q, want running", dto.State)
	}
	return dto
}

func stopReachRun(t *testing.T, m *Module, tenant model.TenantID, dto runDTO) {
	t.Helper()
	if _, err := m.stopRun(context.Background(), tenant, dto.RunRef, "user:u1", model.ActorUser); err != nil {
		t.Fatalf("stopRun: %v", err)
	}
}

// A session that starts reaches its folder, and the access map must say so with no
// connector. The folder edge carries the mode its confinement grants and leaves
// with the session.
func TestSessionStart_PublishesItsFolderReach(t *testing.T) {
	t.Parallel()

	m, _, tenant := newReachRuntimeHarness(t, WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()),
		WithConfinement([]string{t.TempDir()}, false))
	root := filepath.Join(t.TempDir(), "payments-api")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	ws := registerTestWorkspace(t, m, tenant, root)
	profile := mustCreateProfile(t, m, tenant, CreateProfileInput{
		Driver: "claude", ConfigHome: t.TempDir(), UserHome: t.TempDir(), DisplayName: "folder reach",
		AuthSource: AuthSourceAccountHome,
	})
	dto := startReachRun(t, m, tenant, profile, ws, "default")
	origin := launchRefOf(t, dto)

	got, ok := findReach(waitReachEdges(t, m, tenant, 1), "resource", "payments-api", origin)
	if !ok {
		t.Fatalf("the session's folder is not in the access map after start")
	}
	if got.edge.OriginKind != "session" || got.edge.OriginID.String() != dto.CoreSessionID {
		t.Errorf("origin = %s/%s, want session/%s", got.edge.OriginKind, got.edge.OriginID, dto.CoreSessionID)
	}
	folder, err := m.loadWorkspaceRec(t.Context(), tenant, ws)
	if err != nil {
		t.Fatal(err)
	}
	if got.edge.ResourceID.String() != folder.String(model.ColID) {
		t.Fatalf("folder edge points to %s, want registered folder %s", got.edge.ResourceID, folder.String(model.ColID))
	}
	if got.edge.Mode != sdkmodel.ModeReadWrite {
		t.Errorf("mode = %q, want readwrite: the folder is writable in the confinement", got.edge.Mode)
	}
	if got.edge.SignalSource != sdkmodel.SignalSessionLaunch || got.edge.Observed || !got.edge.Permitted {
		t.Errorf("source=%q observed=%v permitted=%v: a launch grant is permitted reach, never invented tool activity",
			got.edge.SignalSource, got.edge.Observed, got.edge.Permitted)
	}
	if got.tool != "claude" {
		t.Errorf("tool ref = %q, want claude", got.tool)
	}

	stopReachRun(t, m, tenant, dto)
	waitReachEdges(t, m, tenant, 0)
}

func TestSessionStart_PublishesTwoToolsReachAndExpires(t *testing.T) {
	t.Parallel()

	m, _, tenant := newReachRuntimeHarness(t, WithRunner(&reachRunner{}), WithProviderDriver(NewCodexDriver()),
		WithProviderSecretVault(newFakeVault()), WithProviderProbe(&fakeProbe{}), WithConfinement([]string{t.TempDir()}, false))
	m.SessionMCP = reachMCP{names: []string{"reach-fixture"}}
	m.UseExecutionEnvironmentRef(testEnvRef)
	claudeProfile := createReachProfile(t, m, tenant, "claude", ProviderKindAnthropic)
	codexProfile := createReachProfile(t, m, tenant, "codex", ProviderKindOpenAI)
	claudeRoot := filepath.Join(t.TempDir(), "claude-folder")
	codexRoot := filepath.Join(t.TempDir(), "codex-folder")
	for _, root := range []string{claudeRoot, codexRoot} {
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	claudeWS := registerTestWorkspace(t, m, tenant, claudeRoot)
	codexWS := registerTestWorkspace(t, m, tenant, codexRoot)

	claudeRun := startReachRun(t, m, tenant, claudeProfile, claudeWS, "default")
	codexRun := startReachRun(t, m, tenant, codexProfile, codexWS, "default")
	claudeOrigin, codexOrigin := launchRefOf(t, claudeRun), launchRefOf(t, codexRun)
	edges := waitReachEdges(t, m, tenant, 6)
	for _, tc := range []struct {
		origin, tool, folder, provider string
	}{
		{claudeOrigin, "claude", "claude-folder", "https://api.anthropic.com"},
		{codexOrigin, "codex", "codex-folder", "https://api.openai.com/v1"},
	} {
		for _, check := range []struct{ kind, ref string }{
			{"resource", tc.folder}, {"mcp_server", "reach-fixture"}, {"provider", tc.provider},
		} {
			got, ok := findReach(edges, check.kind, check.ref, tc.origin)
			if !ok {
				t.Errorf("missing %s %s for %s: %+v", check.kind, check.ref, tc.tool, edges)
				continue
			}
			if got.tool != tc.tool {
				t.Errorf("%s tool = %q, want %q", check.ref, got.tool, tc.tool)
			}
			if got.edge.SignalSource != sdkmodel.SignalSessionLaunch {
				t.Errorf("%s source = %q, want session_launch", check.ref, got.edge.SignalSource)
			}
		}
	}

	stopReachRun(t, m, tenant, claudeRun)
	edges = waitReachEdges(t, m, tenant, 3)
	for _, e := range edges {
		if e.origin == claudeOrigin {
			t.Errorf("stopped Claude session still has reach: %+v", e)
		}
	}
	stopReachRun(t, m, tenant, codexRun)
	waitReachEdges(t, m, tenant, 0)
}

func TestSessionStart_FolderReachUsesReadOnlyConfinement(t *testing.T) {
	t.Parallel()

	m, _, tenant := newReachRuntimeHarness(t, WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()),
		WithConfinement([]string{t.TempDir()}, true))
	root := filepath.Join(t.TempDir(), "read-only-folder")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	ws := registerTestWorkspace(t, m, tenant, root)
	profile := mustCreateProfile(t, m, tenant, CreateProfileInput{
		Driver: "claude", ConfigHome: t.TempDir(), UserHome: t.TempDir(), DisplayName: "read-only reach",
		AuthSource: AuthSourceAccountHome,
	})
	dto := startReachRun(t, m, tenant, profile, ws, "plan")
	got, ok := findReach(waitReachEdges(t, m, tenant, 1), "resource", "read-only-folder", launchRefOf(t, dto))
	if !ok {
		t.Fatal("read-only session's folder is missing")
	}
	if got.edge.Mode != sdkmodel.ModeRead {
		t.Errorf("mode = %q, want read from the final confinement policy", got.edge.Mode)
	}
	stopReachRun(t, m, tenant, dto)
	waitReachEdges(t, m, tenant, 0)
}
