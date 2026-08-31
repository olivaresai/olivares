// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

type collectSink struct {
	mu  sync.Mutex
	obs []model.Observation
}

func (s *collectSink) Emit(_ context.Context, o model.Observation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.obs = append(s.obs, o)
	return nil
}

func (s *collectSink) edges() []model.EdgeObservation {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.EdgeObservation
	for _, o := range s.obs {
		if e, ok := o.(model.EdgeObservation); ok {
			out = append(out, e)
		}
	}
	return out
}

func validConfig() sdk.Config {
	return sdk.Config{Settings: map[string]string{
		"group":          "mygroup",
		"token":          "glpat-xxxxxxxxxxxxxxxxxxxx",
		"webhook_secret": "whsec-test-secret",
		"api_base":       "https://gitlab.example.com",
	}}
}

// TestDescriptor validates Descriptor fields and that secret fields are marked.
func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name {
		t.Fatalf("name: got %q, want %q", d.Name, Name)
	}
	if d.Type != sdk.TypeSource {
		t.Fatalf("type: got %q, want %q", d.Type, sdk.TypeSource)
	}
	if d.APIVersion != sdk.APIVersion {
		t.Fatalf("apiversion: got %q, want %q", d.APIVersion, sdk.APIVersion)
	}

	secretKeys := map[string]bool{"token": true, "webhook_secret": true}
	for _, f := range d.ConfigFields {
		if secretKeys[f.Key] && !f.Secret {
			t.Fatalf("field %q must be Secret", f.Key)
		}
	}

	requiredKeys := map[string]bool{"group": true, "token": true, "webhook_secret": true}
	for _, f := range d.ConfigFields {
		if requiredKeys[f.Key] && !f.Required {
			t.Fatalf("field %q must be Required", f.Key)
		}
	}
}

// TestOpenMissingGroup proves Open rejects a config with no group.
func TestOpenMissingGroup(t *testing.T) {
	s := New()
	cfg := sdk.Config{Settings: map[string]string{
		"token":          "tok",
		"webhook_secret": "sec",
	}}
	err := s.Open(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error for missing group")
	}
	if !strings.Contains(err.Error(), "group") {
		t.Fatalf("error should mention group: %v", err)
	}
}

// TestOpenMissingToken proves Open rejects a config with no token.
func TestOpenMissingToken(t *testing.T) {
	s := New()
	cfg := sdk.Config{Settings: map[string]string{
		"group":          "mygroup",
		"webhook_secret": "sec",
	}}
	err := s.Open(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error for missing token")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Fatalf("error should mention token: %v", err)
	}
}

// TestOpenValid proves Open succeeds with valid config and applies defaults.
func TestOpenValid(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), validConfig()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.group != "mygroup" {
		t.Fatalf("group: got %q", s.group)
	}
	if s.apiBase != "https://gitlab.example.com" {
		t.Fatalf("apiBase: got %q", s.apiBase)
	}
	// Changed this default from the WILDCARD ":9801" to loopback: an
	// operator who configures nothing must not get a plaintext receiver on every
	// interface. TestGitLabDefaultWebhookAddressIsLoopback owns that property;
	// this line only tracks the constant.
	if s.webhookAddr != defaultWebhookAddr {
		t.Fatalf("webhookAddr: got %q, want %q", s.webhookAddr, defaultWebhookAddr)
	}
	if _, ok := s.agentMarkers["claude"]; !ok {
		t.Fatal("default agent markers should include claude (lowercased)")
	}
}

// TestVerifyToken proves constant-time comparison works.
func TestVerifyToken(t *testing.T) {
	if !verifyToken("secret123", "secret123") {
		t.Fatal("matching tokens should verify")
	}
	if verifyToken("secret123", "wrong") {
		t.Fatal("mismatched tokens should not verify")
	}
	if verifyToken("", "secret") {
		t.Fatal("empty got should not verify")
	}
	if verifyToken("secret", "") {
		t.Fatal("empty expected should not verify")
	}
}

// TestWebhookPush verifies that a push hook is parsed and edges are emitted.
func TestWebhookPush(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), validConfig()); err != nil {
		t.Fatal(err)
	}

	sink := &collectSink{}
	handler := s.handleWebhook(sink)

	body := loadFixture(t, "testdata/push.json")
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
	req.Header.Set("X-Gitlab-Token", "whsec-test-secret")
	req.Header.Set("X-Gitlab-Event", "Push Hook")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", w.Code, http.StatusOK)
	}

	edges := sink.edges()
	if len(edges) == 0 {
		t.Fatal("expected at least one edge from push hook")
	}

	// The fixture has a human user "fran", so we should see an identity edge.
	found := false
	for _, e := range edges {
		if e.OriginRef == "fran" && e.OriginKind == "identity" {
			found = true
			if e.ResourceKind != "gitlab.project" {
				t.Fatalf("resource kind: got %q", e.ResourceKind)
			}
			if e.ResourceRef != "mygroup/myproject" {
				t.Fatalf("resource ref: got %q", e.ResourceRef)
			}
			if e.Mode != model.ModeWrite {
				t.Fatalf("mode: got %q", e.Mode)
			}
			if e.Source != model.SignalGitLab {
				t.Fatalf("source: got %q", e.Source)
			}
			if e.Labels["branch"] != "main" {
				t.Fatalf("branch label: got %q", e.Labels["branch"])
			}
		}
	}
	if !found {
		t.Fatal("no identity edge for user fran")
	}
}

// TestWebhookPushWithAgentMarker verifies that a Co-Authored-By "Claude" triggers
// an agent-origin edge with approximate confidence.
func TestWebhookPushWithAgentMarker(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), validConfig()); err != nil {
		t.Fatal(err)
	}

	sink := &collectSink{}
	handler := s.handleWebhook(sink)

	body := loadFixture(t, "testdata/push.json")
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
	req.Header.Set("X-Gitlab-Token", "whsec-test-secret")
	req.Header.Set("X-Gitlab-Event", "Push Hook")
	w := httptest.NewRecorder()
	handler(w, req)

	edges := sink.edges()
	found := false
	for _, e := range edges {
		if e.OriginKind == "agent" && e.OriginRef == "claude" {
			found = true
			if e.Confidence != model.ConfidenceApproximate {
				t.Fatalf("agent marker confidence: got %q, want %q", e.Confidence, model.ConfidenceApproximate)
			}
			if e.Source != model.SignalGitLab {
				t.Fatalf("source: got %q", e.Source)
			}
		}
	}
	if !found {
		t.Fatal("no agent edge for Claude marker")
	}
}

// TestWebhookPushBotAccount verifies that a bot account maps to agent origin
// with attributed confidence.
func TestWebhookPushBotAccount(t *testing.T) {
	s := New()
	cfg := validConfig()
	cfg.Settings["bot_accounts"] = "ci-bot"
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	sink := &collectSink{}

	ev := pushHook{
		Ref:       "refs/heads/deploy",
		UserLogin: "ci-bot",
		UserName:  "CI Bot",
		Project:   projectRef{PathWithNamespace: "mygroup/infra"},
	}

	edges := s.buildPushEdges(ev)
	for _, e := range edges {
		if err := sink.Emit(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}

	found := false
	for _, e := range sink.edges() {
		if e.OriginKind == "agent" && e.OriginRef == "ci-bot" {
			found = true
			if e.Confidence != model.ConfidenceAttributed {
				t.Fatalf("bot confidence: got %q, want %q", e.Confidence, model.ConfidenceAttributed)
			}
		}
	}
	if !found {
		t.Fatal("no agent edge for bot account ci-bot")
	}
}

// TestWebhookMergeRequest verifies that a merged MR emits a write edge.
func TestWebhookMergeRequest(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), validConfig()); err != nil {
		t.Fatal(err)
	}

	sink := &collectSink{}
	handler := s.handleWebhook(sink)

	body := loadFixture(t, "testdata/merge_request.json")
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
	req.Header.Set("X-Gitlab-Token", "whsec-test-secret")
	req.Header.Set("X-Gitlab-Event", "Merge Request Hook")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d", w.Code)
	}

	edges := sink.edges()
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}

	e := edges[0]
	if e.OriginRef != "fran" || e.OriginKind != "identity" {
		t.Fatalf("origin: kind=%q ref=%q", e.OriginKind, e.OriginRef)
	}
	if e.Mode != model.ModeWrite {
		t.Fatalf("merged MR mode: got %q, want write", e.Mode)
	}
	if e.Labels["branch"] != "main" {
		t.Fatalf("branch: got %q", e.Labels["branch"])
	}
}

// TestWebhookInvalidToken proves a bad token returns 403.
func TestWebhookInvalidToken(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), validConfig()); err != nil {
		t.Fatal(err)
	}

	handler := s.handleWebhook(&collectSink{})

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	req.Header.Set("X-Gitlab-Token", "wrong-token")
	req.Header.Set("X-Gitlab-Event", "Push Hook")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

// TestWebhookUnknownEvent proves an unknown event type returns 200 (accepted
// but ignored).
func TestWebhookUnknownEvent(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), validConfig()); err != nil {
		t.Fatal(err)
	}

	sink := &collectSink{}
	handler := s.handleWebhook(sink)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	req.Header.Set("X-Gitlab-Token", "whsec-test-secret")
	req.Header.Set("X-Gitlab-Event", "Wiki Page Hook")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if len(sink.edges()) != 0 {
		t.Fatal("unknown event should not emit edges")
	}
}

// TestParseCoAuthors extracts names from commit messages.
func TestParseCoAuthors(t *testing.T) {
	tests := []struct {
		msg  string
		want []string
	}{
		{
			msg:  "feat: something\n\nCo-Authored-By: Claude <noreply@anthropic.com>",
			want: []string{"Claude"},
		},
		{
			msg:  "fix: thing\n\nco-authored-by: Copilot <copilot@github.com>\nCo-Authored-By: Cursor <noreply@cursor.com>",
			want: []string{"Copilot", "Cursor"},
		},
		{
			msg:  "no trailers here",
			want: nil,
		},
		{
			msg:  "Co-Authored-By: Devin AI <devin@cognition.ai>",
			want: []string{"Devin"},
		},
	}

	for _, tc := range tests {
		got := parseCoAuthors(tc.msg)
		if len(got) != len(tc.want) {
			t.Fatalf("parseCoAuthors(%q): got %v, want %v", tc.msg, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("parseCoAuthors(%q)[%d]: got %q, want %q", tc.msg, i, got[i], tc.want[i])
			}
		}
	}
}

// TestBranchFromRef strips refs/heads/ and refs/tags/ prefixes.
func TestBranchFromRef(t *testing.T) {
	tests := []struct {
		ref  string
		want string
	}{
		{"refs/heads/main", "main"},
		{"refs/heads/feature/new-api", "feature/new-api"},
		{"refs/tags/v1.0.0", "v1.0.0"},
		{"main", "main"},
		{"", ""},
	}

	for _, tc := range tests {
		got := branchFromRef(tc.ref)
		if got != tc.want {
			t.Fatalf("branchFromRef(%q): got %q, want %q", tc.ref, got, tc.want)
		}
	}
}

// TestACLMemberEdges verifies that member access levels produce correct edge modes.
func TestACLMemberEdges(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "glpat-xxxxxxxxxxxxxxxxxxxx" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		switch {
		case strings.Contains(r.URL.Path, "/projects") && !strings.Contains(r.URL.Path, "/members"):
			w.Header().Set("Content-Type", "application/json")
			data, _ := os.ReadFile("testdata/projects.json")
			w.Write(data)

		case strings.Contains(r.URL.Path, "/members"):
			w.Header().Set("Content-Type", "application/json")
			if strings.Contains(r.URL.Path, "/groups/") {
				data, _ := os.ReadFile("testdata/group_members.json")
				w.Write(data)
			} else {
				data, _ := os.ReadFile("testdata/members.json")
				w.Write(data)
			}

		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	s := New()
	cfg := sdk.Config{Settings: map[string]string{
		"group":          "mygroup",
		"token":          "glpat-xxxxxxxxxxxxxxxxxxxx",
		"webhook_secret": "sec",
		"api_base":       srv.URL,
	}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	sink := &collectSink{}
	if err := s.syncACL(context.Background(), sink); err != nil {
		t.Fatal(err)
	}

	edges := sink.edges()
	if len(edges) == 0 {
		t.Fatal("expected ACL edges")
	}

	// Verify specific access-level mappings from project members fixture.
	modeFor := func(username string) model.AccessMode {
		for _, e := range edges {
			if e.OriginRef == "user:"+username && e.Source == model.SignalPolicy {
				return e.Mode
			}
		}
		return ""
	}

	// fran is maintainer (40) → readwrite
	if m := modeFor("fran"); m != model.ModeReadWrite {
		t.Fatalf("fran (maintainer): got %q, want readwrite", m)
	}
	// dev-alice is developer (30) → write
	if m := modeFor("dev-alice"); m != model.ModeWrite {
		t.Fatalf("dev-alice (developer): got %q, want write", m)
	}
	// viewer-bob is reporter (20) → read
	if m := modeFor("viewer-bob"); m != model.ModeRead {
		t.Fatalf("viewer-bob (reporter): got %q, want read", m)
	}
	// org-admin is owner (50) → readwrite (from group members)
	if m := modeFor("org-admin"); m != model.ModeReadWrite {
		t.Fatalf("org-admin (owner): got %q, want readwrite", m)
	}
}

// TestAccessLevelToMode maps all GitLab access levels to the expected mode.
func TestAccessLevelToMode(t *testing.T) {
	tests := []struct {
		level int
		want  model.AccessMode
	}{
		{accessGuest, model.ModeRead},
		{accessReporter, model.ModeRead},
		{accessDeveloper, model.ModeWrite},
		{accessMaintainer, model.ModeReadWrite},
		{accessOwner, model.ModeReadWrite},
		{5, model.ModeRead},       // below guest
		{35, model.ModeWrite},     // between developer and maintainer
		{45, model.ModeReadWrite}, // between maintainer and owner
	}

	for _, tc := range tests {
		got := accessLevelToMode(tc.level)
		if got != tc.want {
			t.Fatalf("accessLevelToMode(%d): got %q, want %q", tc.level, got, tc.want)
		}
	}
}

// TestAPIListProjects verifies pagination via X-Next-Page.
func TestAPIListProjects(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			w.Header().Set("X-Next-Page", "2")
			fmt.Fprint(w, `[{"id":1,"path_with_namespace":"g/p1","default_branch":"main","visibility":"private"}]`)
		} else {
			fmt.Fprint(w, `[{"id":2,"path_with_namespace":"g/p2","default_branch":"main","visibility":"private"}]`)
		}
	}))
	defer srv.Close()

	s := New()
	s.apiBase = srv.URL
	s.token = "tok"
	s.group = "g"

	projects, err := s.listProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}
	if projects[0].PathWithNamespace != "g/p1" || projects[1].PathWithNamespace != "g/p2" {
		t.Fatalf("projects: %+v", projects)
	}
}

func loadFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("load fixture %s: %v", path, err)
	}
	// Validate JSON.
	var raw json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("invalid JSON in %s: %v", path, err)
	}
	return data
}
