// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/sessions"
)

const hookSecretCanary = "synthetic-vault-value-without-token-pattern"

type hookSecretVault struct {
	mu    sync.Mutex
	value string
	opens int
}

func (v *hookSecretVault) Seal(context.Context, auth.Principal, model.TenantID, string, []byte) (string, error) {
	panic("not used")
}
func (v *hookSecretVault) Open(context.Context, model.TenantID, string) ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.opens++
	return []byte(v.value), nil
}
func (v *hookSecretVault) Revoke(context.Context, auth.Principal, model.TenantID, string) error {
	panic("not used")
}

type hookSecretTestRun struct {
	h      *harness
	tenant model.TenantID
	vault  *hookSecretVault
	token  string
	runRef string
	server *http.Server
	logs   *bytes.Buffer
}

func newHookSecretTestRun(t *testing.T, value string, wrap func(auth.PolicyEvaluator) auth.PolicyEvaluator, configure ...func(*engine)) *hookSecretTestRun {
	t.Helper()
	h := newHarness(t)
	tenant := model.TenantID(h.tenantA)
	m := h.set.sessions
	authorizer := auth.NewAuthorizer(h.set.gov.RequestEvaluator(), auth.WithScopedGrants(h.set.gov.ScopedGrants()))
	m.UseWorkAuthorizer(authorizer)
	sessions.WithRunner(approvalProjectionRunner{})(m)
	m.EnableProfiledLaunches()
	m.UseExecutionEnvironmentRef("secret-review-test")
	vault := &hookSecretVault{value: value}
	fixture := &hookSecretTestRun{h: h, tenant: tenant, vault: vault}
	m.UseProviderSecretVault(vault)
	credentials := newSessionHookCredentials(h.authr, h.st, m, h.set.gov)
	m.UseLaunchGate(approvalProjectionLaunchGate(func(ctx context.Context, tenant model.TenantID, intent sessions.LaunchIntent) (sessions.LaunchDecision, error) {
		var err error
		fixture.token, err = credentials.mint(ctx, tenant, intent)
		return sessions.LaunchDecision{Allowed: err == nil}, err
	}))
	var profile struct {
		Ref string `json:"profile_ref"`
	}
	if code := h.reqInto(http.MethodPost, "/v1/m/sessions/provider-profiles", h.adminToken, h.tenantA, map[string]any{"driver": "claude", "auth_source": "provider_account_home", "config_home": t.TempDir(), "user_home": t.TempDir()}, &profile); code != http.StatusCreated {
		t.Fatalf("profile=%d", code)
	}
	var run struct {
		Ref string `json:"run_ref"`
	}
	if code := h.reqInto(http.MethodPost, "/v1/m/sessions/runs", h.adminToken, h.tenantA, map[string]any{"transport": "stream-json", "permission_mode": "default", "isolation": "native", "provider_profile_ref": profile.Ref, "secret_env": []sessions.SecretEnvRef{{Env: "TEST_SECRET", Secret: "env/test"}}}, &run); code != http.StatusCreated {
		t.Fatalf("launch=%d", code)
	}
	fixture.runRef = run.Ref
	t.Cleanup(func() { h.req(http.MethodPost, "/v1/m/sessions/runs/"+run.Ref+"/stop", h.adminToken, h.tenantA, nil) })
	service := h.set.gov.EngineApprovals()
	h.set.gov.UseApprovalCapacity(h.authr.ApprovalCapacity)
	h.set.gov.UseApprovalAuthority(h.authr, authorizer)
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	apiSrv, err := api.New(api.Options{Store: h.st, Authenticator: h.authr, Authorizer: authorizer, Signer: signer, SetupToken: secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token")), Logger: logger, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	eng := &engine{sessionHooks: credentials, authr: h.authr, store: h.st, sessionsMod: m, engineApprovals: service, policyEval: h.set.gov.Evaluator(), scopedGrants: h.set.gov.ScopedGrants(), api: apiSrv}
	if wrap != nil {
		eng.policyEval = wrap(eng.policyEval)
	}
	for _, option := range configure {
		option(eng)
	}
	t.Setenv("OLIVARES_HOOK_PEP_CONFIG", "")
	server, err := buildClaudeHookPEPServer(eng, logger)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.logs = server, &logs
	return fixture
}

func TestSessionClaudeHookWithholdsExactVaultValues(t *testing.T) {
	fixture := newHookSecretTestRun(t, hookSecretCanary, nil)
	h, tenant, vault, token := fixture.h, fixture.tenant, fixture.vault, fixture.token
	server, logs := fixture.server, fixture.logs
	service := h.set.gov.EngineApprovals()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	command := "PASSWORD=" + hookSecretCanary + " printf visible"
	raw, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "session_id": "vendor-session", "tool_name": "Bash", "tool_input": map[string]any{"command": command}})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); server.Handler.ServeHTTP(rec, req) }()
	defer func() { cancel(); <-done }()
	var pending governance.Approval
	for pending.ID == "" {
		items, _, err := service.List(ctx, tenant, hookActionCapability, "pending", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(items) > 0 {
			pending = items[0]
			break
		}
		select {
		case <-done:
			t.Fatal("secret-bearing literal command never reached safe review")
		case <-ctx.Done():
			t.Fatal("review did not arrive")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if strings.Contains(pending.Reason, hookSecretCanary) || pending.Reason != "Claude Code requests Bash\ncommand: PASSWORD=[secret env/test] printf visible" {
		t.Error("approval did not apply the named per-launch redactor before pattern masking")
	}
	human, err := h.authr.Authenticate(ctx, h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Decide(ctx, tenant, human, pending.ID, governance.ApprovalDecisionRequest{Decision: "approve"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("safe review did not finish")
	}
	if !strings.Contains(rec.Body.String(), `"permissionDecision":"allow"`) {
		t.Fatal("literal secret command was not allowed after safe review")
	}
	// A path can carry a vault value too; it is retained in audit and the SOC log.
	raw, _ = json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Read", "tool_input": map[string]any{"file_path": "/tmp/" + hookSecretCanary}})
	req = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	server.Handler.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"permissionDecision":"allow"`) {
		t.Fatal("ordinary read was denied")
	}
	if strings.Contains(logs.String(), hookSecretCanary) {
		t.Fatal("hook log retained the exact vault value")
	}
	for _, event := range canonicalLedgerEventsFrom(t, h.st, tenant, 0) {
		encoded, _ := json.Marshal(event.meta)
		if bytes.Contains(encoded, []byte(hookSecretCanary)) {
			t.Fatal("audit retained the exact vault value")
		}
	}
	if vault.openCount() != 1 {
		t.Fatal("hook read the vault again instead of the per-launch redactor")
	}
}

func (v *hookSecretVault) openCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.opens
}

func (v *hookSecretVault) replace(value string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.value = value
}

type hookSecretEvaluator func(context.Context, auth.Request) (auth.Decision, error)

func (f hookSecretEvaluator) Evaluate(ctx context.Context, in auth.Request) (auth.Decision, error) {
	return f(ctx, in)
}

func TestSessionClaudeHookRefusesRetentionAcrossCredentialGeneration(t *testing.T) {
	entered := make(chan auth.Request, 1)
	release := make(chan struct{})
	var once, releaseOnce sync.Once
	fixture := newHookSecretTestRun(t, hookSecretCanary, func(inner auth.PolicyEvaluator) auth.PolicyEvaluator {
		return hookSecretEvaluator(func(ctx context.Context, in auth.Request) (auth.Decision, error) {
			once.Do(func() {
				entered <- in
				select {
				case <-release:
				case <-ctx.Done():
				}
			})
			return inner.Evaluate(ctx, in)
		})
	})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	raw, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Read", "tool_input": map[string]any{"file_path": "/tmp/" + hookSecretCanary}})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+fixture.token)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); fixture.server.Handler.ServeHTTP(rec, req) }()
	defer func() { releaseOnce.Do(func() { close(release) }); cancel(); <-done }()
	var original auth.Request
	select {
	case original = <-entered:
	case <-done:
		t.Fatal("hook did not enter its live policy question")
	case <-ctx.Done():
		t.Fatal("live policy question did not arrive")
	}
	if !strings.Contains(original.Resource.ID, hookSecretCanary) {
		t.Fatal("live policy input was replaced by retained display text")
	}
	h := fixture.h
	if code, _ := h.req(http.MethodPost, "/v1/m/sessions/runs/"+fixture.runRef+"/stop", h.adminToken, h.tenantA, nil); code != http.StatusOK {
		t.Fatalf("stop=%d", code)
	}
	fixture.vault.replace("new-generation-synthetic-vault-value")
	if code, _ := h.req(http.MethodPost, "/v1/m/sessions/runs/"+fixture.runRef+"/resume", h.adminToken, h.tenantA, nil); code != http.StatusOK {
		t.Fatalf("resume=%d", code)
	}
	if original.EvidenceRedactor == nil {
		t.Error("session policy question has no request-scoped evidence redactor")
	} else if retained := original.EvidenceRedactor(original.Resource.ID); strings.Contains(retained, hookSecretCanary) {
		t.Error("new generation redactor retained the old request's vault value")
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("old request did not finish")
	}
	if strings.Contains(rec.Body.String(), `"permissionDecision":"allow"`) {
		t.Error("old generation authorized a tool after resume")
	}
	if strings.Contains(fixture.logs.String(), hookSecretCanary) {
		t.Error("old generation vault value reached the hook log")
	}
	for _, entry := range canonicalLedgerEventsFrom(t, h.st, fixture.tenant, 0) {
		encoded, _ := json.Marshal(entry.meta)
		if bytes.Contains(encoded, []byte(hookSecretCanary)) {
			t.Error("old generation vault value reached the canonical audit")
		}
	}
	pending, _, err := h.set.gov.EngineApprovals().List(ctx, fixture.tenant, hookActionCapability, "pending", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Error("old generation left an approval request pending")
	}
	if fixture.vault.openCount() != 2 {
		t.Error("retention reopened the vault outside launch/resume")
	}
}

func TestSessionClaudeHookWithholdsSecretInTruncatedResourceReference(t *testing.T) {
	const recognizable = "recognizable-vault-fragment"
	value := recognizable + strings.Repeat("x", 800) + "?secret-tail"
	observed := make(chan auth.Request, 4)
	fixture := newHookSecretTestRun(t, value, func(inner auth.PolicyEvaluator) auth.PolicyEvaluator {
		return hookSecretEvaluator(func(ctx context.Context, in auth.Request) (auth.Decision, error) {
			select {
			case observed <- in:
			default:
			}
			return inner.Evaluate(ctx, in)
		})
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	raw, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "WebFetch", "tool_input": map[string]any{"url": "https://service.test/" + value}})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+fixture.token)
	rec := httptest.NewRecorder()
	fixture.server.Handler.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"permissionDecision":"allow"`) {
		t.Fatal("ordinary URL read was denied")
	}
	var original auth.Request
	select {
	case original = <-observed:
	default:
		t.Fatal("live policy question was not evaluated")
	}
	// URL classification removes the query: exact matching against this derived
	// reference alone cannot find the original full vault value.
	if !strings.Contains(original.Resource.ID, recognizable) || strings.Contains(original.Resource.ID, "?secret-tail") {
		t.Fatal("test did not exercise the real URL query projection")
	}
	if original.EvidenceRedactor == nil {
		t.Error("session policy question has no request-scoped evidence redactor")
	} else if retained := original.EvidenceRedactor(original.Resource.ID); strings.Contains(retained, recognizable) {
		t.Error("PDP evidence retained a partially projected secret")
	}
	if strings.Contains(fixture.logs.String(), recognizable) {
		t.Error("hook log retained a partially projected secret")
	}
	for _, entry := range canonicalLedgerEventsFrom(t, fixture.h.st, fixture.tenant, 0) {
		encoded, _ := json.Marshal(entry.meta)
		if bytes.Contains(encoded, []byte(recognizable)) {
			t.Error("canonical audit retained a partially projected secret")
		}
	}
	if fixture.vault.openCount() != 1 {
		t.Error("retention reopened the per-launch vault")
	}
}

func TestSessionClaudeHookWithholdsWithoutItsSecretRedactor(t *testing.T) {
	fixture := newHookSecretTestRun(t, hookSecretCanary, nil, func(eng *engine) { eng.sessionsMod = nil })
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	raw, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": "PASSWORD=" + hookSecretCanary + " printf visible"}})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+fixture.token)
	rec := httptest.NewRecorder()
	fixture.server.Handler.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"permissionDecision":"deny"`) || !strings.Contains(rec.Body.String(), "tool input withheld") {
		t.Error("secret-bearing request without supervision was not explicitly withheld")
	}
	if strings.Contains(fixture.logs.String(), hookSecretCanary) {
		t.Error("unsupervised input reached the hook log")
	}
	all, _, err := fixture.h.set.gov.EngineApprovals().List(t.Context(), fixture.tenant, hookActionCapability, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Error("unreviewable secret-bearing input entered the human queue")
	}
	for _, entry := range canonicalLedgerEventsFrom(t, fixture.h.st, fixture.tenant, 0) {
		encoded, _ := json.Marshal(entry.meta)
		if bytes.Contains(encoded, []byte(hookSecretCanary)) {
			t.Error("unsupervised input reached canonical audit")
		}
	}
}

func TestSessionClaudeHookKeepsDeadlineReasonWithSecrets(t *testing.T) {
	fixture := newHookSecretTestRun(t, hookSecretCanary, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
	defer cancel()
	raw, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": "printf waiting-for-review"}})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+fixture.token)
	rec := httptest.NewRecorder()
	fixture.server.Handler.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"permissionDecision":"deny"`) || !strings.Contains(rec.Body.String(), "deadline") {
		t.Error("canceled retention changed the ordinary deadline reason")
	}
	if strings.Contains(fixture.logs.String(), "evidence gap") {
		t.Error("deadline lost its terminal deny anchor")
	}
	pending, _, err := fixture.h.set.gov.EngineApprovals().List(t.Context(), fixture.tenant, hookActionCapability, "pending", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Error("deadline left a pending approval")
	}
	if fixture.vault.openCount() != 1 {
		t.Error("deadline retention reopened the vault")
	}
}

type hookSecretScopedAuthorizer func(context.Context, auth.Request) (auth.ScopedDecision, error)

func (f hookSecretScopedAuthorizer) Scoped(ctx context.Context, in auth.Request) (auth.ScopedDecision, error) {
	return f(ctx, in)
}

func TestSessionClaudeHookWithholdsSecretInScopedResourceProjection(t *testing.T) {
	const recognizable = "recognizable-vault-fragment"
	value := recognizable + "__secret-tail"
	type retainedQuestion struct {
		raw     auth.Request
		preview string
	}
	observed := make(chan retainedQuestion, 4)
	fixture := newHookSecretTestRun(t, value, nil, func(eng *engine) {
		inner := eng.scopedGrants
		eng.scopedGrants = hookSecretScopedAuthorizer(func(ctx context.Context, in auth.Request) (auth.ScopedDecision, error) {
			preview := ""
			if in.EvidenceRedactor != nil {
				preview = in.EvidenceRedactor(in.Resource.ID)
			}
			select {
			case observed <- retainedQuestion{raw: in, preview: preview}:
			default:
			}
			return inner.Scoped(ctx, in)
		})
	})
	ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
	defer cancel()
	raw, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "mcp__" + value, "tool_input": map[string]any{}})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+fixture.token)
	rec := httptest.NewRecorder()
	fixture.server.Handler.ServeHTTP(rec, req)
	var observation retainedQuestion
	select {
	case observation = <-observed:
	default:
		t.Fatal("actual scoped projection was not evaluated")
	}
	original := observation.raw
	if original.Resource.Kind != "mcp_server" || original.Resource.ID != recognizable {
		t.Fatal("live scoped question did not retain its original server projection")
	}
	if original.EvidenceRedactor == nil {
		t.Error("scoped question has no request-specific evidence adapter")
	} else if strings.Contains(observation.preview, recognizable) {
		t.Error("scoped evidence retained a projected secret fragment")
	}
	all, _, err := fixture.h.set.gov.EngineApprovals().List(t.Context(), fixture.tenant, hookActionCapability, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, approval := range all {
		if strings.Contains(approval.Reason, recognizable) {
			t.Error("approval retained a projected secret fragment")
		}
	}
}

func TestSessionClaudeHookWithholdsProjectedSecretsInPolicyReasons(t *testing.T) {
	const recognizable = "recognizable-vault-fragment"
	value := recognizable + "__secret-tail"
	fixture := newHookSecretTestRun(t, value, nil, func(eng *engine) {
		eng.scopedGrants = hookSecretScopedAuthorizer(func(_ context.Context, in auth.Request) (auth.ScopedDecision, error) {
			return auth.ScopedDecision{Effect: auth.EffectForbid, Class: auth.ClassPolicy, Reason: "scoped forbid: mcp_server " + in.Resource.ID}, nil
		})
	})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	raw, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "mcp__" + value, "tool_input": map[string]any{}})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+fixture.token)
	rec := httptest.NewRecorder()
	fixture.server.Handler.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"permissionDecision":"deny"`) || !strings.Contains(rec.Body.String(), "scoped forbid") {
		t.Fatal("scoped forbid did not survive retention")
	}
	if strings.Contains(rec.Body.String(), recognizable) {
		t.Error("terminal refusal retained the projected secret in its reason")
	}
	if strings.Contains(fixture.logs.String(), recognizable) {
		t.Error("SOC refusal retained the projected secret in its reason")
	}
	for _, entry := range canonicalLedgerEventsFrom(t, fixture.h.st, fixture.tenant, 0) {
		encoded, _ := json.Marshal(entry.meta)
		if bytes.Contains(encoded, []byte(recognizable)) {
			t.Error("canonical refusal retained the projected secret in its reason")
		}
	}
}

func TestSessionClaudeHookWithholdsUnauthenticatedToolInput(t *testing.T) {
	for _, kind := range []string{"corrupt session bearer", "unknown bearer"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newHookSecretTestRun(t, hookSecretCanary, nil)
			token := "not-a-credential"
			if kind == "corrupt session bearer" {
				token = fixture.token + "corrupt"
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			raw, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Read", "tool_input": map[string]any{"file_path": "/tmp/" + hookSecretCanary}})
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			fixture.server.Handler.ServeHTTP(rec, req)
			if !strings.Contains(rec.Body.String(), `"permissionDecision":"deny"`) {
				t.Fatal("unauthenticated input was not refused")
			}
			if strings.Contains(fixture.logs.String(), hookSecretCanary) {
				t.Error("unauthenticated caller retained vault-bearing input in SOC")
			}
			pending, _, err := fixture.h.set.gov.EngineApprovals().List(t.Context(), fixture.tenant, hookActionCapability, "pending", "")
			if err != nil {
				t.Fatal(err)
			}
			if len(pending) != 0 {
				t.Error("unauthenticated input entered review")
			}
			if fixture.vault.openCount() != 1 {
				t.Error("unauthenticated input reopened vault")
			}
		})
	}
}

func TestSessionClaudeHookReviewPreservesGeneratedMaskPositions(t *testing.T) {
	for _, scenario := range []struct{ name, command, display string }{
		{"token equals", "curl --token=" + hookSecretCanary + " https://service.test", "curl --token=[secret env/test] https://service.test"},
		{"token space", "curl --token " + hookSecretCanary + " https://service.test", "curl --token [secret env/test] https://service.test"},
		{"userinfo", "curl https://" + hookSecretCanary + "@service.test", "curl https://[secret env/test]@service.test"},
		{"earlier length changing scrub", "PASSWORD=other-literal TOKEN=" + hookSecretCanary + " printf visible", "PASSWORD=[REDACTED] TOKEN=[secret env/test] printf visible"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := newHookSecretTestRun(t, hookSecretCanary, nil)
			reviewHookSecretInput(t, fixture, "Bash", map[string]any{"command": scenario.command}, "Claude Code requests Bash\ncommand: "+scenario.display)
		})
	}
}

func TestSessionClaudeHookRefusesMaskedSecretOperations(t *testing.T) {
	for _, command := range []string{
		"printf " + hookSecretCanary,
		"xargs " + hookSecretCanary,
		`python -c "import ` + hookSecretCanary + `"`,
		"PASSWORD=$(printf " + hookSecretCanary + ") printf visible",
		"PASSWORD=[secret attacker] TOKEN=" + hookSecretCanary + " printf visible",
	} {
		t.Run(command, func(t *testing.T) {
			fixture := newHookSecretTestRun(t, hookSecretCanary, nil)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			raw, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": command}})
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+fixture.token)
			rec := httptest.NewRecorder()
			fixture.server.Handler.ServeHTTP(rec, req)
			if !strings.Contains(rec.Body.String(), `"permissionDecision":"deny"`) {
				t.Error("hidden operation was not refused before human review")
			}
			all, _, err := fixture.h.set.gov.EngineApprovals().List(t.Context(), fixture.tenant, hookActionCapability, "", "")
			if err != nil || len(all) != 0 {
				t.Error("unreviewable operation entered the human queue")
			}
			assertHookSecretNotRetained(t, fixture, hookSecretCanary)
		})
	}
}

func TestSessionClaudeHookRedactsEscapedPathAndOmitsFileContents(t *testing.T) {
	const value = "synthetic-vault-\"quoted\nvalue"
	fixture := newHookSecretTestRun(t, value, nil)
	reviewHookSecretInput(t, fixture, "Write", map[string]any{"file_path": "/tmp/" + value, "content": "contents must not be reviewed: " + value}, "Claude Code requests Write\npath: /tmp/[secret env/test]")
	assertHookSecretNotRetained(t, fixture, value)
	encoded, _ := json.Marshal(value)
	assertHookSecretNotRetained(t, fixture, string(encoded[1:len(encoded)-1]))
}

func reviewHookSecretInput(t *testing.T, fixture *hookSecretTestRun, tool string, input map[string]any, expectedReason string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	raw, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": tool, "tool_input": input})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+fixture.token)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); fixture.server.Handler.ServeHTTP(rec, req) }()
	defer func() { cancel(); <-done }()
	service := fixture.h.set.gov.EngineApprovals()
	var pending governance.Approval
	for pending.ID == "" {
		items, _, err := service.List(ctx, fixture.tenant, hookActionCapability, "pending", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(items) == 1 {
			pending = items[0]
			break
		}
		select {
		case <-done:
			t.Fatal("reviewable literal did not reach the approval queue")
		case <-ctx.Done():
			t.Fatal("approval did not arrive")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if pending.Reason != expectedReason {
		t.Error("human preview differs from the proven masked input")
	}
	human, err := fixture.h.authr.Authenticate(ctx, fixture.h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Decide(ctx, fixture.tenant, human, pending.ID, governance.ApprovalDecisionRequest{Decision: "approve"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("approved literal did not finish")
	}
	if !strings.Contains(rec.Body.String(), `"permissionDecision":"allow"`) {
		t.Error("safe input was not allowed after review")
	}
	assertHookSecretNotRetained(t, fixture, hookSecretCanary)
	if fixture.vault.openCount() != 1 {
		t.Error("hook reopened the vault")
	}
}

func assertHookSecretNotRetained(t *testing.T, fixture *hookSecretTestRun, value string) {
	t.Helper()
	if strings.Contains(fixture.logs.String(), value) {
		t.Error("hook log retained the vault value")
	}
	all, _, err := fixture.h.set.gov.EngineApprovals().List(t.Context(), fixture.tenant, hookActionCapability, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, approval := range all {
		if strings.Contains(approval.Reason, value) {
			t.Error("approval retained the vault value")
		}
	}
	for _, entry := range canonicalLedgerEventsFrom(t, fixture.h.st, fixture.tenant, 0) {
		encoded, _ := json.Marshal(entry.meta)
		if bytes.Contains(encoded, []byte(value)) {
			t.Error("canonical audit retained the vault value")
		}
	}
}
