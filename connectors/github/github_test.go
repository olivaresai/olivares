// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// collectSink captures emitted observations (race-safe).
type collectSink struct {
	mu  sync.Mutex
	obs []model.Observation
}

func (c *collectSink) Emit(_ context.Context, o model.Observation) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.obs = append(c.obs, o)
	return nil
}

func (c *collectSink) edges() []model.EdgeObservation {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []model.EdgeObservation
	for _, o := range c.obs {
		if e, ok := o.(model.EdgeObservation); ok {
			out = append(out, e)
		}
	}
	return out
}

func signPayload(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func validConfig() map[string]string {
	return map[string]string{
		"org":            "acme-corp",
		"webhook_secret": "test-secret",
		"pat":            "ghp_test_token",
	}
}

// --- Descriptor ---

func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name {
		t.Errorf("Name = %q, want %q", d.Name, Name)
	}
	if d.Type != sdk.TypeSource {
		t.Errorf("Type = %q, want source", d.Type)
	}
	if d.APIVersion != sdk.APIVersion {
		t.Errorf("APIVersion = %q, want %q", d.APIVersion, sdk.APIVersion)
	}
	if len(d.ConfigFields) == 0 {
		t.Error("ConfigFields should not be empty")
	}
	// Verify secret fields are marked.
	secretKeys := map[string]bool{"webhook_secret": false, "private_key": false, "pat": false}
	for _, f := range d.ConfigFields {
		if _, want := secretKeys[f.Key]; want {
			if !f.Secret {
				t.Errorf("ConfigField %q should be Secret", f.Key)
			}
			secretKeys[f.Key] = true
		}
	}
	for k, found := range secretKeys {
		if !found {
			t.Errorf("missing secret ConfigField %q", k)
		}
	}
}

// --- Open validation ---

func TestOpenMissingOrg(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"webhook_secret": "s",
		"pat":            "t",
	}})
	if err == nil {
		t.Error("expected error for missing org")
	}
}

func TestOpenMissingAuth(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"org":            "acme",
		"webhook_secret": "s",
	}})
	if err == nil {
		t.Error("expected error for missing auth method")
	}
}

func TestOpenValid(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: validConfig()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.org != "acme-corp" {
		t.Errorf("org = %q", s.org)
	}
	// Changed this default from the WILDCARD ":9800" to loopback: an
	// operator who configures nothing must not get a plaintext receiver on every
	// interface. TestGitHubDefaultWebhookAddressIsLoopback owns that property;
	// this line only tracks the constant.
	if s.webhookAddr != defaultWebhookAddr {
		t.Errorf("webhookAddr = %q, want %q", s.webhookAddr, defaultWebhookAddr)
	}
}

// --- HMAC verification ---

func TestVerifySignature(t *testing.T) {
	secret := "my-secret"
	payload := []byte(`{"action":"push"}`)

	t.Run("valid", func(t *testing.T) {
		sig := signPayload(payload, secret)
		if !verifySignature(payload, sig, secret) {
			t.Error("valid signature rejected")
		}
	})

	t.Run("invalid", func(t *testing.T) {
		if verifySignature(payload, "sha256=deadbeef", secret) {
			t.Error("invalid signature accepted")
		}
	})

	t.Run("missing_prefix", func(t *testing.T) {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(payload)
		bare := hex.EncodeToString(mac.Sum(nil))
		if verifySignature(payload, bare, secret) {
			t.Error("signature without sha256= prefix accepted")
		}
	})
}

// --- Webhook push ---

func TestWebhookPush(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: validConfig()}); err != nil {
		t.Fatal(err)
	}

	payload, err := os.ReadFile("testdata/push.json")
	if err != nil {
		t.Fatal(err)
	}
	sig := signPayload(payload, "test-secret")

	sink := &collectSink{}
	handler := s.handleWebhook(sink)

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Body = newReadCloser(payload)
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("X-GitHub-Event", "push")
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	edges := sink.edges()
	if len(edges) != 1 {
		t.Fatalf("got %d edges, want 1", len(edges))
	}
	e := edges[0]
	if e.ResourceKind != "github.repo" {
		t.Errorf("ResourceKind = %q", e.ResourceKind)
	}
	if e.ResourceRef != "acme-corp/web-app" {
		t.Errorf("ResourceRef = %q", e.ResourceRef)
	}
	if e.Mode != model.ModeWrite {
		t.Errorf("Mode = %q, want write", e.Mode)
	}
	if e.Source != model.SignalGitHub {
		t.Errorf("Source = %q, want github", e.Source)
	}
	if e.Labels["branch"] != "main" {
		t.Errorf("branch label = %q", e.Labels["branch"])
	}
}

// --- Webhook push with agent marker ---

func TestWebhookPushWithAgentMarker(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: validConfig()}); err != nil {
		t.Fatal(err)
	}

	payload, err := os.ReadFile("testdata/push.json")
	if err != nil {
		t.Fatal(err)
	}
	sig := signPayload(payload, "test-secret")

	sink := &collectSink{}
	handler := s.handleWebhook(sink)

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Body = newReadCloser(payload)
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("X-GitHub-Event", "push")
	w := httptest.NewRecorder()

	handler(w, req)

	edges := sink.edges()
	if len(edges) != 1 {
		t.Fatalf("got %d edges, want 1", len(edges))
	}
	e := edges[0]
	// The push.json fixture has a Co-Authored-By: Claude trailer.
	if e.OriginKind != "agent" {
		t.Errorf("OriginKind = %q, want agent", e.OriginKind)
	}
	if e.OriginRef != "claude" {
		t.Errorf("OriginRef = %q, want claude", e.OriginRef)
	}
	if e.Confidence != model.ConfidenceApproximate {
		t.Errorf("Confidence = %q, want approximate", e.Confidence)
	}
}

// --- Webhook push with bot account ---

func TestWebhookPushBotAccount(t *testing.T) {
	cfg := validConfig()
	cfg["bot_accounts"] = "fran-dev"
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
		t.Fatal(err)
	}

	payload, err := os.ReadFile("testdata/push.json")
	if err != nil {
		t.Fatal(err)
	}
	sig := signPayload(payload, "test-secret")

	sink := &collectSink{}
	handler := s.handleWebhook(sink)

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Body = newReadCloser(payload)
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("X-GitHub-Event", "push")
	w := httptest.NewRecorder()

	handler(w, req)

	edges := sink.edges()
	if len(edges) != 1 {
		t.Fatalf("got %d edges, want 1", len(edges))
	}
	e := edges[0]
	if e.OriginKind != "agent" {
		t.Errorf("OriginKind = %q, want agent", e.OriginKind)
	}
	if e.OriginRef != "fran-dev" {
		t.Errorf("OriginRef = %q, want fran-dev", e.OriginRef)
	}
	// Bot account gets attributed confidence (not approximate).
	if e.Confidence != model.ConfidenceAttributed {
		t.Errorf("Confidence = %q, want attributed", e.Confidence)
	}
}

// --- Webhook pull request ---

func TestWebhookPullRequest(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: validConfig()}); err != nil {
		t.Fatal(err)
	}

	payload, err := os.ReadFile("testdata/pull_request.json")
	if err != nil {
		t.Fatal(err)
	}
	sig := signPayload(payload, "test-secret")

	sink := &collectSink{}
	handler := s.handleWebhook(sink)

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Body = newReadCloser(payload)
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("X-GitHub-Event", "pull_request")
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	edges := sink.edges()
	if len(edges) != 1 {
		t.Fatalf("got %d edges, want 1", len(edges))
	}
	e := edges[0]
	if e.Mode != model.ModeWrite {
		t.Errorf("Mode = %q, want write", e.Mode)
	}
	// Merged PR: branch should be the base (target) branch.
	if e.Labels["branch"] != "main" {
		t.Errorf("branch = %q, want main", e.Labels["branch"])
	}
	if e.OriginRef != "fran-dev" {
		t.Errorf("OriginRef = %q", e.OriginRef)
	}
}

// --- Webhook invalid signature ---

func TestWebhookInvalidSignature(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: validConfig()}); err != nil {
		t.Fatal(err)
	}

	sink := &collectSink{}
	handler := s.handleWebhook(sink)

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Body = newReadCloser([]byte(`{}`))
	req.Header.Set("X-Hub-Signature-256", "sha256=bad")
	req.Header.Set("X-GitHub-Event", "push")
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
	if len(sink.edges()) != 0 {
		t.Error("should not emit edges on invalid signature")
	}
}

// --- Webhook unknown event ---

func TestWebhookUnknownEvent(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: validConfig()}); err != nil {
		t.Fatal(err)
	}

	payload := []byte(`{"action":"completed"}`)
	sig := signPayload(payload, "test-secret")

	sink := &collectSink{}
	handler := s.handleWebhook(sink)

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Body = newReadCloser(payload)
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("X-GitHub-Event", "check_run")
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for unknown event", w.Code)
	}
	if len(sink.edges()) != 0 {
		t.Error("should not emit edges for unknown event type")
	}
}

// --- parseCoAuthors ---

func TestParseCoAuthors(t *testing.T) {
	cases := []struct {
		name    string
		message string
		want    []string
	}{
		{
			name:    "single",
			message: "feat: add feature\n\nCo-Authored-By: Claude <noreply@anthropic.com>",
			want:    []string{"Claude"},
		},
		{
			name:    "multiple",
			message: "fix: update\n\nCo-Authored-By: Claude <a@b.com>\nCo-authored-by: Copilot <c@d.com>",
			want:    []string{"Claude", "Copilot"},
		},
		{
			name:    "none",
			message: "chore: update deps",
			want:    nil,
		},
		{
			name:    "no_email",
			message: "feat: stuff\n\nCo-Authored-By: Devin",
			want:    []string{"Devin"},
		},
		{
			name:    "case_insensitive_prefix",
			message: "fix: x\n\nco-authored-by: Aider <x@y.com>",
			want:    []string{"Aider"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseCoAuthors(tc.message)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// --- branchFromRef ---

func TestBranchFromRef(t *testing.T) {
	cases := []struct {
		ref, want string
	}{
		{"refs/heads/main", "main"},
		{"refs/heads/feature/webhook-sync", "feature/webhook-sync"},
		{"refs/tags/v1.0.0", ""},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			got := branchFromRef(tc.ref)
			if got != tc.want {
				t.Errorf("branchFromRef(%q) = %q, want %q", tc.ref, got, tc.want)
			}
		})
	}
}

// --- permissionToMode ---

func TestPermissionToMode(t *testing.T) {
	cases := []struct {
		name string
		p    permissionSet
		want model.AccessMode
	}{
		{"admin", permissionSet{Admin: true, Push: true, Pull: true}, model.ModeReadWrite},
		{"push", permissionSet{Push: true, Pull: true}, model.ModeWrite},
		{"pull", permissionSet{Pull: true}, model.ModeRead},
		{"none", permissionSet{}, model.ModeUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := permissionToMode(tc.p)
			if got != tc.want {
				t.Errorf("permissionToMode = %q, want %q", got, tc.want)
			}
		})
	}
}

// --- ACL collaborator edges ---

func TestACLCollaboratorEdges(t *testing.T) {
	fixture, err := os.ReadFile("testdata/collaborators.json")
	if err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/orgs/acme-corp/repos":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `[{"full_name":"acme-corp/web-app","private":true,"default_branch":"main"}]`)
		case "/repos/acme-corp/web-app/collaborators":
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixture)
		case "/orgs/acme-corp/teams":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	cfg := validConfig()
	cfg["api_base"] = ts.URL
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
		t.Fatal(err)
	}
	s.client = ts.Client()

	sink := &collectSink{}
	if err := s.syncACL(context.Background(), sink); err != nil {
		t.Fatal(err)
	}

	edges := sink.edges()
	if len(edges) != 3 {
		t.Fatalf("got %d edges, want 3", len(edges))
	}

	// fran-dev (admin) -> readwrite
	if edges[0].OriginRef != "user:fran-dev" || edges[0].Mode != model.ModeReadWrite {
		t.Errorf("edge[0] = {%q, %q}", edges[0].OriginRef, edges[0].Mode)
	}
	// alice (push) -> write
	if edges[1].OriginRef != "user:alice" || edges[1].Mode != model.ModeWrite {
		t.Errorf("edge[1] = {%q, %q}", edges[1].OriginRef, edges[1].Mode)
	}
	// bob-readonly (pull) -> read
	if edges[2].OriginRef != "user:bob-readonly" || edges[2].Mode != model.ModeRead {
		t.Errorf("edge[2] = {%q, %q}", edges[2].OriginRef, edges[2].Mode)
	}

	// All should be SignalPolicy.
	for i, e := range edges {
		if e.Source != model.SignalPolicy {
			t.Errorf("edge[%d] Source = %q, want policy", i, e.Source)
		}
		if e.Confidence != model.ConfidenceAttributed {
			t.Errorf("edge[%d] Confidence = %q, want attributed", i, e.Confidence)
		}
	}
}

// --- ACL team edges ---

func TestACLTeamEdges(t *testing.T) {
	teamsFixture, err := os.ReadFile("testdata/teams.json")
	if err != nil {
		t.Fatal(err)
	}
	teamReposFixture, err := os.ReadFile("testdata/team_repos.json")
	if err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/orgs/acme-corp/repos":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `[{"full_name":"acme-corp/web-app","private":true,"default_branch":"main"}]`)
		case "/repos/acme-corp/web-app/collaborators":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `[]`)
		case "/orgs/acme-corp/teams":
			w.Header().Set("Content-Type", "application/json")
			w.Write(teamsFixture)
		case "/orgs/acme-corp/teams/engineering/repos",
			"/orgs/acme-corp/teams/security/repos",
			"/orgs/acme-corp/teams/docs-team/repos":
			w.Header().Set("Content-Type", "application/json")
			w.Write(teamReposFixture)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	cfg := validConfig()
	cfg["api_base"] = ts.URL
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
		t.Fatal(err)
	}
	s.client = ts.Client()

	sink := &collectSink{}
	if err := s.syncACL(context.Background(), sink); err != nil {
		t.Fatal(err)
	}

	edges := sink.edges()
	// 3 teams x 2 repos each = 6 team edges + 0 collaborator edges.
	if len(edges) != 6 {
		t.Fatalf("got %d edges, want 6", len(edges))
	}

	// All team edges should have "team:" prefix in OriginRef.
	for i, e := range edges {
		if e.Source != model.SignalPolicy {
			t.Errorf("edge[%d] Source = %q, want policy", i, e.Source)
		}
		if e.OriginKind != "identity" {
			t.Errorf("edge[%d] OriginKind = %q, want identity", i, e.OriginKind)
		}
	}
	// First two should be engineering team.
	if edges[0].OriginRef != "team:engineering" {
		t.Errorf("edge[0] OriginRef = %q, want team:engineering", edges[0].OriginRef)
	}
}

// --- parseLinkNext ---

func TestParseLinkNext(t *testing.T) {
	cases := []struct {
		name string
		link string
		want string
	}{
		{
			"has_next",
			`<https://api.github.com/orgs/acme/repos?page=2>; rel="next", <https://api.github.com/orgs/acme/repos?page=5>; rel="last"`,
			"https://api.github.com/orgs/acme/repos?page=2",
		},
		{
			"no_next",
			`<https://api.github.com/orgs/acme/repos?page=5>; rel="last"`,
			"",
		},
		{
			"empty",
			"",
			"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseLinkNext(tc.link)
			if got != tc.want {
				t.Errorf("parseLinkNext = %q, want %q", got, tc.want)
			}
		})
	}
}

// --- API list repos with httptest ---

func TestAPIListRepos(t *testing.T) {
	fixture, err := os.ReadFile("testdata/repos.json")
	if err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/orgs/acme-corp/repos" {
			http.NotFound(w, r)
			return
		}
		// Verify auth header is present.
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixture)
	}))
	defer ts.Close()

	cfg := validConfig()
	cfg["api_base"] = ts.URL
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
		t.Fatal(err)
	}
	s.client = ts.Client()

	repos, err := s.listRepos(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 3 {
		t.Fatalf("got %d repos, want 3", len(repos))
	}
	if repos[0].FullName != "acme-corp/web-app" {
		t.Errorf("repos[0].FullName = %q", repos[0].FullName)
	}
}

// --- Webhook PR (not merged, action=closed) should not emit ---

func TestWebhookPRClosedNotMerged(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: validConfig()}); err != nil {
		t.Fatal(err)
	}

	ev := pullRequestEvent{
		Action: "closed",
		PullRequest: prRef{
			Merged: false,
			Base:   branchRef{Ref: "main"},
			Head:   branchRef{Ref: "feature/x"},
		},
		Repository: repoRef{FullName: "acme-corp/web-app"},
		Sender:     userRef{Login: "fran-dev"},
	}
	payload, _ := json.Marshal(ev)
	sig := signPayload(payload, "test-secret")

	sink := &collectSink{}
	handler := s.handleWebhook(sink)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Body = newReadCloser(payload)
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("X-GitHub-Event", "pull_request")
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if len(sink.edges()) != 0 {
		t.Error("closed but not merged PR should not emit edges")
	}
}

// newReadCloser wraps a byte slice as an io.ReadCloser for test requests.
func newReadCloser(b []byte) io.ReadCloser {
	return io.NopCloser(bytes.NewReader(b))
}
