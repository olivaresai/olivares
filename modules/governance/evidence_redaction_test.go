// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

func TestPDPSecretInputsAreRedactedFromLogsAndRetainedHistory(t *testing.T) {
	for _, tc := range []struct {
		name, secret, command string
		custom                bool
	}{
		{"pattern default", "ar-pattern-fixture-credential", "run password=ar-pattern-fixture-credential", false},
		{"short quoted password", "a bc", `run password="a bc"`, false},
		{"quoted password suffix", "ar-tail-fixture", `run password="ar-prefix-fixture ar-tail-fixture"`, false},
		{"run vault redactor", "ar-vault-fixture-line\nquoted-\"value", "run ar-vault-fixture-line\nquoted-\"value", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTypedEvidenceFixture(t)
			principal := f.confinedPrincipal(t, "")
			secret, command := tc.secret, tc.command
			req := auth.Request{Principal: principal, Tenant: f.tenant, Permission: "agent:write",
				Resource: auth.ResourceAttrs{Kind: "shell", ID: command, Sensitivity: command,
					Extra: map[string]string{command: command}}}
			if tc.custom {
				req.EvidenceRedactor = func(value string) string {
					return strings.ReplaceAll(value, secret, "[REDACTED:session-secret]")
				}
			}
			// Real compiled Cedar matches the RAW command. Redacting evaluation
			// instead of persistence would silently remove this forbid.
			scoped, _ := typedEvidenceScopedEngine(t, f, f.data(typedEvidenceNow, nil),
				`forbid(principal, action, resource) when { resource == Resource::`+strconv.Quote(command)+` };`, 0, FreshnessRecord{})
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			scoped.log = logger
			m := New()
			m.UseData(api.NewModuleData(f.st))
			m.log = logger
			m.eval.cache = map[model.TenantID]*compiledSet{f.tenant: {rules: []abacRule{{Deny: true, Permission: "agent:write"}}}}
			abac, err := m.Evaluator().Evaluate(t.Context(), req)
			if err != nil || abac.Allow {
				t.Fatal("real native policy did not deny")
			}
			var records []auth.AuthorizationRecord
			ctx := auth.WithAuthorizationRecording(t.Context(), func(r auth.AuthorizationRecord) { records = append(records, r) })
			az := auth.NewAuthorizer(nil, auth.WithScopedGrants(scoped), auth.WithClock(func() time.Time { return typedEvidenceNow }))
			if dec := az.Authorize(ctx, req); dec.Allow || dec.Class != auth.ClassPolicy {
				t.Fatal("compiled command forbid was changed by evidence redaction")
			}
			if len(records) != 1 {
				t.Fatalf("final answers = %d, want one", len(records))
			}
			if !strings.Contains(logs.String(), "abac policy restriction") || !strings.Contains(logs.String(), "cedar scoped forbid") {
				t.Fatal("decision/effect log was silenced")
			}
			assertNoPDPSecret(t, secret, logs.Bytes(), "decision/effect logs")
			// Persistence follows the final answer; no nested transaction.
			if err := m.RecordAuthorization(t.Context(), records[0]); err != nil {
				t.Fatal(err)
			}
			var decisions []model.AuthorizationDecision
			if err := f.st.View(t.Context(), f.tenant, func(sc store.Scope) error {
				return sc.Audit().Walk(t.Context(), 0, func(event model.AuditEvent) error {
					encoded, err := json.Marshal(event)
					if err != nil {
						return err
					}
					assertNoPDPSecret(t, secret, encoded, "audit ledger")
					if event.TargetKind != model.AuthorizationDecisionKind {
						return nil
					}
					decision, err := sc.AccessEvidence().AuthorizationDecision(t.Context(), event.TargetID)
					if err != nil {
						return err
					}
					encoded, err = json.Marshal(decision)
					if err != nil {
						return err
					}
					assertNoPDPSecret(t, secret, encoded, "retained decision")
					artifact, err := sc.AccessEvidence().PolicyArtifact(t.Context(), model.ID(decision.Decision.PolicyVersionID))
					if err != nil {
						return err
					}
					assertNoPDPSecret(t, secret, []byte(artifact.Artifact.Content), "retained policy inputs")
					if decision.Decision.ReplayCompleteness != sdk.ReplayIncomplete {
						t.Error("redacted inputs claim complete replay")
					}
					decisions = append(decisions, decision)
					return nil
				})
			}); err != nil {
				t.Fatal(err)
			}
			if len(decisions) != 1 {
				t.Fatalf("stored final answers = %d, want one", len(decisions))
			}
			reader := New()
			reader.UseData(api.NewModuleData(f.st))
			got, err := reader.Reconstruct(t.Context(), f.tenant, ReconstructRequest{DecisionID: decisions[0].ID})
			if err != nil || got.Status != ReconstructInsufficient || got.Missing != "authorization_inputs.redacted" ||
				got.ReasonCode != "input_redacted" || !got.CouldNotReconstruct || got.UsedLivePolicy || got.Outcome != "" {
				t.Fatal("redacted history must report insufficient input_redacted, without evaluation")
			}
			if req.Resource.ID != command || req.Resource.Extra[command] != command {
				t.Fatal("evidence persistence mutated the live question")
			}
			if got.RecordedOutcome != sdk.AccessOutcomeDeny {
				t.Fatal("redaction lost the observed refusal")
			}
		})
	}
}

func TestPDPOversizedHistoryRefusesBeforeRedactionOrPersistence(t *testing.T) {
	f := newTypedEvidenceFixture(t)
	principal := f.confinedPrincipal(t, "")
	redactorCalled := false
	req := auth.Request{Principal: principal, Tenant: f.tenant, Permission: "agent:write",
		Resource: auth.ResourceAttrs{Kind: "shell", ID: strings.Repeat("x", model.MaxPolicyArtifactBytes+1)},
		EvidenceRedactor: func(text string) string {
			redactorCalled = true
			return text
		}}
	var record auth.AuthorizationRecord
	ctx := auth.WithAuthorizationRecording(t.Context(), func(r auth.AuthorizationRecord) { record = r })
	if !auth.NewAuthorizer(nil).Authorize(ctx, req).Allow {
		t.Fatal("evidence bound changed the raw live RBAC answer")
	}
	m := New()
	m.UseData(api.NewModuleData(f.st))
	if err := m.RecordAuthorization(t.Context(), record); err == nil || redactorCalled {
		t.Fatal("oversized history was not refused before parsing/redaction")
	}
	if len(storedPDPDecisions(t, f)) != 0 {
		t.Fatal("refused history wrote an observed-answer row")
	}
	if len(req.Resource.ID) != model.MaxPolicyArtifactBytes+1 {
		t.Fatal("history bound truncated the live resource")
	}
}

func TestPDPExpandedHistoryRefusesBeyondTheArtifactBound(t *testing.T) {
	snapshot := auth.RetainedAuthorization{Version: 1, Complete: true,
		Resource: auth.ResourceAttrs{Kind: "shell", ID: "ar-expanded-fixture", Sensitivity: "ar-expanded-fixture",
			Extra: map[string]string{"path": "ar-expanded-fixture", "argument": "ar-expanded-fixture"}}}
	marker := strings.Repeat("x", model.MaxPolicyArtifactBytes/3)
	_, err := redactRetainedAuthorization(snapshot, func(text string) string {
		if text == "ar-expanded-fixture" {
			return marker
		}
		return text
	})
	if err == nil {
		t.Fatal("expanded retained inputs exceeded the aggregate parsing bound")
	}
	if snapshot.Resource.ID != "ar-expanded-fixture" || snapshot.Resource.Extra["path"] != "ar-expanded-fixture" {
		t.Fatal("expansion refusal changed the live inputs")
	}
}

func assertNoPDPSecret(t *testing.T, secret string, encoded []byte, surface string) {
	t.Helper()
	quoted := strconv.Quote(secret)
	if bytes.Contains(encoded, []byte(secret)) || bytes.Contains(encoded, []byte(quoted[1:len(quoted)-1])) {
		t.Error(surface + " retained a fixture secret") // Never print the leaking payload.
	}
}

func TestPDPRedactedStepUpRefusalReportsRedactedInput(t *testing.T) {
	f := newTypedEvidenceFixture(t)
	f.confinedPrincipal(t, "")
	authn := auth.NewAuthenticator(f.st, nil)
	token, _, err := authn.Login(t.Context(), "typed-evidence-confined@example.test", "strong-password-2", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := authn.Authenticate(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	req := auth.Request{Principal: principal, Tenant: f.tenant, Permission: "agent:write",
		Resource: auth.ResourceAttrs{Kind: "shell", ID: "password=ar-stepup-fixture-credential"},
		Route:    auth.RouteMetadata{MinimumAAL: auth.AAL3}}
	var records []auth.AuthorizationRecord
	ctx := auth.WithAuthorizationRecording(t.Context(), func(r auth.AuthorizationRecord) { records = append(records, r) })
	if _, err := auth.NewAuthorizer(nil).AuthorizeRoute(ctx, req); !errors.Is(err, auth.ErrStepUpRequired) {
		t.Fatal("real password session did not require step-up")
	}
	if len(records) != 1 {
		t.Fatal("real step-up refusal was not retained")
	}
	m := New()
	m.UseData(api.NewModuleData(f.st))
	if err := m.RecordAuthorization(t.Context(), records[0]); err != nil {
		t.Fatal(err)
	}
	decisions := storedPDPDecisions(t, f)
	if len(decisions) != 1 {
		t.Fatal("redacted step-up refusal lost its observed answer")
	}
	got, err := m.Reconstruct(t.Context(), f.tenant, ReconstructRequest{DecisionID: decisions[0].ID})
	if err != nil || got.Status != ReconstructInsufficient || got.Missing != "authorization_inputs.redacted" ||
		got.ReasonCode != "input_redacted" || got.RecordedOutcome != sdk.AccessOutcomeDeny || got.UsedLivePolicy {
		t.Fatal("step-up metadata hid the redacted-input explanation")
	}
}

func storedPDPDecisions(t *testing.T, f *typedEvidenceFixture) []model.AuthorizationDecision {
	t.Helper()
	var rows []model.AuthorizationDecision
	if err := f.st.View(t.Context(), f.tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(t.Context(), 0, func(event model.AuditEvent) error {
			if event.TargetKind != model.AuthorizationDecisionKind {
				return nil
			}
			decision, err := sc.AccessEvidence().AuthorizationDecision(t.Context(), event.TargetID)
			if err == nil {
				rows = append(rows, decision)
			}
			return err
		})
	}); err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestPDPHistoryRedactsSensitiveJSONValuesWithoutChangingTheLiveAnswer(t *testing.T) {
	f := newTypedEvidenceFixture(t)
	principal := f.confinedPrincipal(t, "")
	const secret = "ar-unpatterned-fixture-credential"
	req := auth.Request{Principal: principal, Tenant: f.tenant, Permission: "agent:write",
		Resource: auth.ResourceAttrs{Kind: "shell", ID: model.NewID().String(), Extra: map[string]string{"password": secret}}}
	var record auth.AuthorizationRecord
	ctx := auth.WithAuthorizationRecording(t.Context(), func(r auth.AuthorizationRecord) { record = r })
	if !auth.NewAuthorizer(nil).Authorize(ctx, req).Allow {
		t.Fatal("RBAC editor answer changed")
	}
	m := New()
	m.UseData(api.NewModuleData(f.st))
	if err := m.RecordAuthorization(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	decisions := storedPDPDecisions(t, f)
	if len(decisions) != 1 {
		t.Fatal("redaction lost the observed answer")
	}
	var artifact model.PolicyArtifact
	if err := f.st.View(t.Context(), f.tenant, func(sc store.Scope) error {
		var err error
		artifact, err = sc.AccessEvidence().PolicyArtifact(t.Context(), model.ID(decisions[0].Decision.PolicyVersionID))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	assertNoPDPSecret(t, secret, []byte(artifact.Artifact.Content), "structured sensitive value")
	got, err := m.Reconstruct(t.Context(), f.tenant, ReconstructRequest{DecisionID: decisions[0].ID})
	if err != nil || got.Status != ReconstructInsufficient || got.ReasonCode != "input_redacted" || got.RecordedOutcome != sdk.AccessOutcomeAllow {
		t.Fatal("sensitive JSON value was re-evaluated or its observed allow lost")
	}
	if req.Resource.Extra["password"] != secret {
		t.Fatal("persistence redactor changed the caller's live input")
	}
}

func TestPDPNonSecretHistoryRemainsReconstructibleWithARunRedactor(t *testing.T) {
	f := newTypedEvidenceFixture(t)
	principal := f.confinedPrincipal(t, "")
	req := auth.Request{Principal: principal, Tenant: f.tenant, Permission: "agent:write",
		Resource: auth.ResourceAttrs{Kind: "shell", ID: model.NewID().String()},
		EvidenceRedactor: func(text string) string {
			return strings.ReplaceAll(text, "ar-never-present-vault-fixture", "[REDACTED]")
		}}
	scoped, _ := typedEvidenceScopedEngine(t, f, f.data(typedEvidenceNow, nil), `forbid(principal, action, resource);`, 0, FreshnessRecord{})
	var record auth.AuthorizationRecord
	ctx := auth.WithAuthorizationRecording(t.Context(), func(r auth.AuthorizationRecord) { record = r })
	if auth.NewAuthorizer(nil, auth.WithScopedGrants(scoped)).Authorize(ctx, req).Allow {
		t.Fatal("Cedar forbid changed")
	}
	m := New()
	m.UseData(api.NewModuleData(f.st))
	if err := m.RecordAuthorization(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	decisions := storedPDPDecisions(t, f)
	if len(decisions) != 1 || decisions[0].Decision.ReplayCompleteness != sdk.ReplayComplete {
		t.Fatal("unchanged inputs lost complete replay")
	}
	got, err := m.Reconstruct(t.Context(), f.tenant, ReconstructRequest{DecisionID: decisions[0].ID})
	if err != nil || got.Status != ReconstructReconstructed || got.Outcome != sdk.AccessOutcomeDeny || got.UsedLivePolicy {
		t.Fatal("non-secret retained Cedar answer could not reconstruct")
	}
}
