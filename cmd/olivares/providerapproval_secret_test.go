// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

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
			g := sessionProviderPolicy{credentials: c.SessionCredentials, eval: observed, authz: harnessAuthz(h), scoped: h.set.gov.ScopedGrants(), approvals: h.set.gov.EngineApprovals(), store: h.st}
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
			b.LocalProposer = service
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
