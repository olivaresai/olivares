// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/redact"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// A credential naming injected secrets needs its live run's exact-value redactor.
// A node without that run must not evaluate partial evidence or create a review.
func TestProviderApprovalWithholdsInputWithoutItsSessionSecretRedactor(t *testing.T) {
	for _, driver := range []string{"codex", "grok", "opencode"} {
		t.Run(driver, func(t *testing.T) {
			h := newHarness(t)
			tenant := model.TenantID(h.tenantA)
			human, err := h.authr.Authenticate(t.Context(), h.adminToken)
			if err != nil {
				t.Fatal(err)
			}
			c := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
			intent := claimHookTestSession(t, h, human, tenant, "secrets-unavailable-"+driver)
			intent.SecretEnv = []sessions.SecretEnvRef{{Env: "GITHUB_TOKEN", Secret: "env/github"}}
			if _, err := c.mintForPrincipal(human, tenant, intent); err != nil {
				t.Fatal(err)
			}
			p, scope, err := c.ResolveRun(t.Context(), tenant, intent.RunRef)
			if err != nil {
				t.Fatal(err)
			}
			observed := &providerFactEvaluator{next: h.set.gov.Evaluator()}
			g := sessionProviderPolicy{credentials: c.SessionCredentials, eval: observed, scoped: h.set.gov.ScopedGrants(), approvals: h.set.gov.EngineApprovals(), store: h.st}
			const secret = "N1exactCanaryNoPattern0123456789"
			req := sessions.ProviderApprovalRequest{Driver: driver, RunRef: intent.RunRef, SessionRef: scope.SessionRef, Principal: p, Kind: "command_execution", Method: "item/commandExecution/requestApproval", CommandLine: "curl --user user:" + secret + " https://example.invalid", TurnID: secret, FactsComplete: true}
			out, err := g.Decide(context.Background(), tenant, req)
			if err != nil || out.Disposition != sessions.ProviderApprovalDeny || observed.question.Permission != "" {
				t.Error("missing run redactor did not refuse before the PDP")
			}
			for _, ev := range canonicalLedgerEventsFrom(t, h.st, tenant, 0) {
				data, err := json.Marshal(ev.meta)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(data), secret) {
					t.Error("unavailable redactor exposed input in the ledger")
				}
			}
			items, _, err := h.set.gov.EngineApprovals().List(t.Context(), tenant, "sessions.provider.approval", "pending", "")
			if err != nil || len(items) != 0 {
				t.Fatal("refused input created a human review")
			}
			if !strings.Contains(req.CommandLine, secret) {
				t.Fatal("display redaction changed the execution input")
			}
		})
	}
}

// Run the real vault-value redactor, live PDP, human queue and canonical ledger.
// The owned process seam supplies protocol frames and reads no vendor account.
func TestProviderApprovalRedactsSessionSecretsInQueueLedgerAndLogs(t *testing.T) {
	for _, driver := range []string{"codex", "grok", "opencode"} {
		t.Run(driver, func(t *testing.T) {
			var logs providerApprovalLogBuffer
			h := newHarnessWithRecorder(t, nil, slog.New(slog.NewTextHandler(&logs, nil)))
			tenant := model.TenantID(h.tenantA)
			m := h.set.sessions
			m.UseWorkAuthorizer(auth.NewAuthorizer(h.set.gov.RequestEvaluator(), auth.WithScopedGrants(h.set.gov.ScopedGrants())))
			sessions.WithRunner(approvalProjectionRunner{})(m)
			vault := &providerApprovalTestVault{values: map[string]string{"env/one": "N1exactCanaryNoPattern0123456789", "env/quoted": `N1quote"back\slash0123456789`, "env/pattern": "olvs_abcdefgh extra-segment"}}
			sessions.WithProviderSecretVault(vault)(m)
			m.EnableProfiledLaunches()
			m.UseExecutionEnvironmentRef("secret-review-test")
			credentials := newSessionHookCredentials(h.authr, h.st, m, h.set.gov)
			m.UseLaunchGate(approvalProjectionLaunchGate(func(ctx context.Context, tenant model.TenantID, in sessions.LaunchIntent) (sessions.LaunchDecision, error) {
				_, err := credentials.mint(ctx, tenant, in)
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
			if code := h.reqInto(http.MethodPost, "/v1/m/sessions/runs", h.adminToken, h.tenantA, map[string]any{"transport": "stream-json", "permission_mode": "acceptEdits", "isolation": "native", "provider_profile_ref": profile.Ref, "secret_env": []sessions.SecretEnvRef{{Env: "EXACT_KEY", Secret: "env/one"}, {Env: "OTHER_KEY", Secret: "env/quoted"}, {Env: "WHOLE_KEY", Secret: "env/pattern"}}}, &run); code != http.StatusCreated {
				t.Fatalf("launch=%d", code)
			}
			t.Cleanup(func() { h.req(http.MethodPost, "/v1/m/sessions/runs/"+run.Ref+"/stop", h.adminToken, h.tenantA, nil) })
			p, scope, err := credentials.ResolveRun(t.Context(), tenant, run.Ref)
			if err != nil {
				t.Fatal(err)
			}
			service := h.set.gov.EngineApprovals()
			h.set.gov.UseApprovalCapacity(h.authr.ApprovalCapacity)
			h.set.gov.UseApprovalAuthority(h.authr, auth.NewAuthorizer(h.set.gov.RequestEvaluator(), auth.WithScopedGrants(h.set.gov.ScopedGrants())))
			observed := &providerFactEvaluator{next: h.set.gov.Evaluator()}
			g := sessionProviderPolicy{credentials: credentials.SessionCredentials, eval: observed, scoped: h.set.gov.ScopedGrants(), approvals: service, store: h.st, redactSecrets: m.RedactSessionSecretsWithSpans}
			req := sessions.ProviderApprovalRequest{Driver: driver, RunRef: run.Ref, SessionRef: scope.SessionRef, Principal: p, TurnID: "exact-turn-" + vault.values["env/one"], Method: "item/fileChange/requestApproval", Kind: "file_change", FilePaths: []string{"/project/" + vault.values["env/one"] + "/one.txt", "/project/" + vault.values["env/quoted"] + "/two.txt", "/project/" + vault.values["env/pattern"] + "/three.txt"}, Requested: []string{"fs:write:/project"}, FactsComplete: true}
			req.EffectiveFilePaths = append([]string(nil), req.FilePaths...)
			for i, path := range req.FilePaths {
				req.FilePaths[i] = redact.Clean(path)
			}
			originalPaths := strings.Join(req.EffectiveFilePaths, "\n")
			original, _ := json.Marshal(req)
			out, err := g.Decide(t.Context(), tenant, req)
			if err != nil || out.Disposition != sessions.ProviderApprovalAllow {
				t.Fatalf("allowed-path fixture expected Allow, got %s (reason=%q, error=%v)", out.Disposition, out.Reason, err)
			}
			if observed.question.Resource.ID != originalPaths {
				t.Fatal("the PDP did not receive the original file paths")
			}
			if code, _ := h.req(http.MethodPost, "/v1/m/governance/policies", h.adminToken, h.tenantA, map[string]any{"name": "exact-review", "kind": "approval", "enabled": true, "spec": map[string]any{"required_approvals": 1, "expires_in_seconds": 60, "match": map[string]any{"action": "sessions.provider.approval", "subject_kind": "session_run"}}}); code != http.StatusCreated {
				t.Fatalf("review policy=%d", code)
			}
			out, err = g.Decide(t.Context(), tenant, req)
			if err != nil || out.Disposition != sessions.ProviderApprovalAsk {
				t.Fatal("authored ask did not reach human review")
			}
			b := newApprovalBridge(approvalBridgeConfig{}, slog.New(slog.NewTextHandler(&logs, nil)))
			b.localProposer = service
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			defer cancel()
			done := make(chan struct{})
			var answer sessions.ProviderApprovalDecision
			var answerErr error
			go func() {
				defer close(done)
				answer, answerErr = (providerApprovalAdapter{bridge: b, approvalWait: m.BeginApprovalWait, reviewFacts: g.reviewFacts, reviewReason: g.reviewReason}).Approve(ctx, tenant, req)
			}()
			defer func() { cancel(); <-done }()
			var ref string
			for ref == "" {
				items, _, err := service.List(ctx, tenant, "sessions.provider.approval", "pending", "")
				if err != nil {
					t.Fatal(err)
				}
				if len(items) > 0 {
					ref = items[0].ID
					data, _ := json.Marshal(items[0])
					assertProviderSecretsAbsent(t, vault, string(data))
					if !strings.Contains(items[0].Reason, "[secret env/one]") || !strings.Contains(items[0].Reason, "[secret env/quoted]") || !strings.Contains(items[0].Reason, "[secret env/pattern]") {
						t.Fatal("reviewer did not receive the exact-value projection")
					}
					break
				}
				select {
				case <-done:
					t.Fatalf("provider did not wait for the reviewer: reason=%q error=%v", answer.Reason, answerErr)
				case <-ctx.Done():
					t.Fatal("human review was not queued")
				case <-time.After(10 * time.Millisecond):
				}
			}
			if code, _ := h.decide(t, h.adminToken, ref, "approve"); code != http.StatusOK {
				t.Fatalf("one approval=%d", code)
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("provider did not receive the human decision")
			}
			if answerErr != nil || !answer.Allow || len(answer.Granted) != 1 || answer.Granted[0] != req.Requested[0] {
				t.Fatal("approval changed the requested authority")
			}
			current, _ := json.Marshal(req)
			if !bytes.Equal(original, current) || strings.Join(req.EffectiveFilePaths, "\n") != originalPaths {
				t.Fatal("review projection mutated the provider/execution input")
			}
			for _, ev := range canonicalLedgerEventsFrom(t, h.st, tenant, 0) {
				data, _ := json.Marshal(ev.meta)
				assertProviderSecretsAbsent(t, vault, string(data))
			}
			// Match the original paths with a real compiled Cedar forbid. The
			// evidence projection must not change what the live policy evaluates.
			if code, _ := h.req(http.MethodPost, "/v1/m/governance/pdp/publish", h.adminToken, h.tenantA, map[string]any{"engine": "cedar", "source": `forbid(principal, action, resource) when { resource == Resource::` + strconv.Quote(originalPaths) + ` };`}); code != http.StatusOK {
				t.Fatalf("scoped forbid=%d", code)
			}
			denied, err := g.Decide(t.Context(), tenant, req)
			if err != nil || denied.Disposition != sessions.ProviderApprovalDeny {
				t.Fatal("live Cedar forbid was bypassed")
			}
			// The deny overlay can refuse before Scoped runs. Exercise that
			// existing evaluator with the exact raw question from this decision.
			if scoped, err := h.set.gov.ScopedGrants().Scoped(t.Context(), observed.question); err != nil || scoped.Effect != auth.EffectForbid || !strings.Contains(logs.String(), "cedar scoped forbid") {
				t.Fatal("live scoped forbid was bypassed or its log was silenced")
			}
			// The native deny emits the other existing PDP log before scoped
			// evaluation; requiring both events prevents vacuous privacy checks.
			if code, _ := h.req(http.MethodPost, "/v1/m/governance/policies", h.adminToken, h.tenantA, map[string]any{"name": "deny-provider", "kind": "abac", "enabled": true, "spec": map[string]any{"rules": []any{map[string]any{"deny": true, "permission": driver + ".tool.use:write"}}}}); code != http.StatusCreated {
				t.Fatalf("deny policy=%d", code)
			}
			denied, err = g.Decide(t.Context(), tenant, req)
			if err != nil || denied.Disposition != sessions.ProviderApprovalDeny || !strings.Contains(logs.String(), "abac policy restriction") {
				t.Fatal("live deny was bypassed")
			}
			for _, ev := range canonicalLedgerEventsFrom(t, h.st, tenant, 0) {
				data, _ := json.Marshal(ev.meta)
				assertProviderSecretsAbsent(t, vault, string(data))
			}
			assertProviderSecretsAbsent(t, vault, logs.String())
			if vault.opens != 3 {
				t.Fatal("review performed another vault read")
			}
		})
	}
}

// A final redactor may match serialized field names or a value spanning fields.
// The human must receive the same operation projection that passed review.
func TestProviderApprovalRefusesWhenFinalMaskChangesReviewedScope(t *testing.T) {
	for _, scenario := range []string{"cross-field", "field-name", "audit-cross-field", "prefix"} {
		t.Run(scenario, func(t *testing.T) {
			h := newHarness(t)
			tenant := model.TenantID(h.tenantA)
			human, err := h.authr.Authenticate(t.Context(), h.adminToken)
			if err != nil {
				t.Fatal(err)
			}
			credentials := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
			intent := claimHookTestSession(t, h, human, tenant, "final-mask-"+scenario)
			intent.SecretEnv = []sessions.SecretEnvRef{{Env: "EXACT_KEY", Secret: "env/collision"}}
			if _, err := credentials.mintForPrincipal(human, tenant, intent); err != nil {
				t.Fatal(err)
			}
			p, scope, err := credentials.ResolveRun(t.Context(), tenant, intent.RunRef)
			if err != nil {
				t.Fatal(err)
			}
			secret := `"CommandLine":"rm -rf /important"`
			req := sessions.ProviderApprovalRequest{Driver: "codex", RunRef: intent.RunRef, SessionRef: scope.SessionRef, Principal: p, Kind: "command_execution", Method: "item/commandExecution/requestApproval", CommandLine: "rm -rf /important", FactsComplete: true}
			if scenario == "field-name" {
				secret = "FilePaths"
				req.Kind, req.CommandLine, req.FilePaths = "file_change", "", []string{"/project/FilePaths/proof"}
			}
			if scenario == "audit-cross-field" {
				secret = `"command_line":"rm -rf /important"`
			}
			if scenario == "prefix" {
				secret = "driver=codex method="
			}
			service := h.set.gov.EngineApprovals()
			h.set.gov.UseApprovalCapacity(h.authr.ApprovalCapacity)
			h.set.gov.UseApprovalAuthority(h.authr, auth.NewAuthorizer(h.set.gov.RequestEvaluator(), auth.WithScopedGrants(h.set.gov.ScopedGrants())))
			g := sessionProviderPolicy{credentials: credentials.SessionCredentials, redactSecrets: providerSecretTestMask(secret, "env/collision")}
			reviewed, err := g.reviewFacts(t.Context(), tenant, req)
			if err != nil || !reviewed.FactsComplete {
				t.Fatal("field projection was not reviewable")
			}
			if scenario == "field-name" && (len(reviewed.FilePaths) != 1 || strings.Contains(reviewed.FilePaths[0], secret)) {
				t.Fatal("field-shaped secret retained its raw value")
			}
			if scenario == "audit-cross-field" {
				g.eval, g.approvals, g.store = h.set.gov.Evaluator(), service, h.st
				answer, err := g.Decide(t.Context(), tenant, req)
				if err != nil || answer.Disposition != sessions.ProviderApprovalDeny {
					t.Error("altered final ledger projection was not refused")
				}
				for _, event := range canonicalLedgerEventsFrom(t, h.st, tenant, 0) {
					metadata, _ := json.Marshal(event.meta)
					if strings.Contains(string(metadata), secret) {
						t.Error("final ledger projection reconstructed a vault value")
					}
				}
				return
			}
			b := newApprovalBridge(approvalBridgeConfig{}, discardLog())
			b.localProposer = service
			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()
			answer, err := (providerApprovalAdapter{bridge: b, reviewFacts: g.reviewFacts, reviewReason: g.reviewReason}).Approve(ctx, tenant, req)
			if err != nil || answer.Allow || answer.Reason != "permission scope is not reviewable" {
				t.Errorf("altered final projection was not refused before review")
			}
			items, _, err := service.List(t.Context(), tenant, "sessions.provider.approval", "pending", "")
			if err != nil || len(items) != 0 {
				t.Fatal("altered final projection created a human review")
			}
		})
	}
}

// An exact-value marker is display text; the pattern floor must not split it
// into apparent extra shell operands in a valid credential-data context.
func TestProviderApprovalSessionSecretMaskKeepsLiteralCredentialContextsReviewable(t *testing.T) {
	const value = "N1literalCredentialNoPattern0123456789"
	for _, command := range []string{"PASSWORD=" + value + " curl https://example.invalid", "curl --token " + value + " https://example.invalid", "curl --token=" + value + " https://example.invalid"} {
		t.Run(command, func(t *testing.T) {
			h := newHarness(t)
			tenant := model.TenantID(h.tenantA)
			human, err := h.authr.Authenticate(t.Context(), h.adminToken)
			if err != nil {
				t.Fatal(err)
			}
			credentials := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
			intent := claimHookTestSession(t, h, human, tenant, "literal-mask")
			intent.SecretEnv = []sessions.SecretEnvRef{{Env: "EXACT_KEY", Secret: "env/literal"}}
			if _, err := credentials.mintForPrincipal(human, tenant, intent); err != nil {
				t.Fatal(err)
			}
			p, scope, err := credentials.ResolveRun(t.Context(), tenant, intent.RunRef)
			if err != nil {
				t.Fatal(err)
			}
			g := sessionProviderPolicy{credentials: credentials.SessionCredentials, redactSecrets: providerSecretTestMask(value, "env/literal")}
			reviewed, err := g.reviewFacts(t.Context(), tenant, sessions.ProviderApprovalRequest{Driver: "codex", RunRef: intent.RunRef, SessionRef: scope.SessionRef, Principal: p, Kind: "command_execution", CommandLine: command, FilePaths: []string{}, Requested: []string{}, FactsComplete: true})
			if err != nil || !reviewed.FactsComplete || strings.Contains(reviewed.CommandLine, value) {
				t.Fatal("literal credential masking made a proven data context unreviewable")
			}
			if reviewed.FilePaths == nil || reviewed.Requested == nil {
				t.Fatal("review projection changed an empty permission array to null")
			}
		})
	}
}

type providerApprovalTestVault struct {
	values map[string]string
	opens  int
}

func (v *providerApprovalTestVault) Open(_ context.Context, _ model.TenantID, name string) ([]byte, error) {
	v.opens++
	value, ok := v.values[name]
	if !ok {
		return nil, auth.ErrSecretNotFound
	}
	return []byte(value), nil
}
func (v *providerApprovalTestVault) Seal(context.Context, auth.Principal, model.TenantID, string, []byte) (string, error) {
	return "", errors.New("fixture must not reseal")
}
func (v *providerApprovalTestVault) Revoke(context.Context, auth.Principal, model.TenantID, string) error {
	return errors.New("fixture must not revoke")
}
func assertProviderSecretsAbsent(t *testing.T, v *providerApprovalTestVault, text string) {
	t.Helper()
	for _, value := range v.values {
		escaped, _ := json.Marshal(value)
		if strings.Contains(text, value) || strings.Contains(text, string(escaped[1:len(escaped)-1])) {
			t.Error("provider review retained a synthetic vault value")
		}
	}
}

// The runtime and the request can log concurrently; the capture obeys that seam.
type providerApprovalLogBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *providerApprovalLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(p)
}
func (b *providerApprovalLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

// The fixture records generated spans while replacing, like the production port.
// It never infers provenance from marker-looking bytes in the source or result.
func providerSecretTestMask(value, name string) func(model.TenantID, string, []byte) ([]byte, []sessions.SecretMaskSpan, bool) {
	return func(_ model.TenantID, _ string, data []byte) ([]byte, []sessions.SecretMaskSpan, bool) {
		marker := []byte("[secret " + name + "]")
		var out []byte
		var spans []sessions.SecretMaskSpan
		for {
			at := bytes.Index(data, []byte(value))
			if at < 0 {
				out = append(out, data...)
				return out, spans, true
			}
			out = append(out, data[:at]...)
			start := len(out)
			out = append(out, marker...)
			spans = append(spans, sessions.SecretMaskSpan{Start: start, End: len(out)})
			data = data[at+len(value):]
		}
	}
}

// A resumed/revoked generation cannot use the run's newer cache to project an
// older request. Revalidate after reading the cache and withhold the old facts.
func TestProviderApprovalWithholdsFactsIfAuthorityChangesDuringSecretMask(t *testing.T) {
	for _, change := range []string{"revoke", "secret-names"} {
		t.Run(change, func(t *testing.T) {
			h := newHarness(t)
			tenant := model.TenantID(h.tenantA)
			human, err := h.authr.Authenticate(t.Context(), h.adminToken)
			if err != nil {
				t.Fatal(err)
			}
			credentials := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
			intent := claimHookTestSession(t, h, human, tenant, "secret-cache-authority-change-"+change)
			intent.SecretEnv = []sessions.SecretEnvRef{{Env: "EXACT_KEY", Secret: "env/one"}}
			if _, err := credentials.mintForPrincipal(human, tenant, intent); err != nil {
				t.Fatal(err)
			}
			p, scope, err := credentials.ResolveRun(t.Context(), tenant, intent.RunRef)
			if err != nil {
				t.Fatal(err)
			}
			var once sync.Once
			g := sessionProviderPolicy{credentials: credentials.SessionCredentials, redactSecrets: func(_ model.TenantID, _ string, data []byte) ([]byte, []sessions.SecretMaskSpan, bool) {
				once.Do(func() {
					if change == "revoke" {
						credentials.Revoke(tenant, intent.RunRef)
						return
					}
					replacement := intent
					replacement.SecretEnv = []sessions.SecretEnvRef{{Env: "EXACT_KEY", Secret: "env/two"}}
					if _, err := credentials.mintForPrincipal(human, tenant, replacement); err != nil {
						t.Fatal(err)
					}
					_, current, err := credentials.ResolveRun(t.Context(), tenant, intent.RunRef)
					if err != nil || current.SecretEnv == scope.SecretEnv {
						t.Fatal("replacement did not retain a resolvable changed secret binding")
					}
				})
				// A live newer cache need not contain the old generation's value.
				return append([]byte(nil), data...), nil, true
			}}
			const value = "N1previousGenerationVaultValue0123456789"
			input := sessions.ProviderApprovalRequest{Driver: "codex", RunRef: intent.RunRef, SessionRef: scope.SessionRef, Principal: p, Kind: "file_change", Method: "item/fileChange/requestApproval", FilePaths: []string{"/project/" + value + "/proof.txt"}, FactsComplete: true}
			projected, err := g.reviewFacts(t.Context(), tenant, input)
			if err == nil || projected.FactsComplete || strings.Contains(strings.Join(projected.FilePaths, "\n"), value) {
				t.Fatal("a changed session retained reviewable old-generation secret input")
			}
			if len(input.FilePaths) != 1 || !strings.Contains(input.FilePaths[0], value) {
				t.Fatal("withholding the review mutated the execution input")
			}
		})
	}
}
