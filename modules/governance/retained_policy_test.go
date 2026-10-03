// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// A supported legacy policy adapter need not provide immutable replay inputs.
type unretainedReplayPolicy struct{}

func (unretainedReplayPolicy) Evaluate(context.Context, auth.Request) (auth.Decision, error) {
	return auth.Decision{Allow: true}, nil
}

func TestRetainedTypedCleanForbidDominatesUnrelatedPermitDiagnostic(t *testing.T) {
	f := newTypedEvidenceFixture(t)
	principal := f.confinedPrincipal(t, "")
	var agent model.Agent
	if err := f.st.Mutate(t.Context(), f.tenant, func(sc store.Scope) error {
		var err error
		agent, err = sc.Agents().Create(t.Context(), model.Agent{Name: "retained", Kind: "test", Status: model.StatusActive})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scoped, _ := typedEvidenceScopedEngine(t, f, f.data(typedEvidenceNow, nil), `
forbid(principal, action, resource);
permit(principal, action, resource) when { resource.owner == "alice" };
`, 0, FreshnessRecord{})
	req := typedEvidenceRequest(f.tenant)
	req.Principal, req.Resource.ID = principal, agent.ID.String()
	var records []auth.AuthorizationRecord
	ctx := auth.WithAuthorizationRecording(typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour)), func(r auth.AuthorizationRecord) { records = append(records, r) })
	az := auth.NewAuthorizer(nil, auth.WithScopedGrants(scoped), auth.WithClock(func() time.Time { return typedEvidenceNow.Add(time.Second) }))
	live := az.AuthorizeEvidence(ctx, req)
	if live.Outcome != auth.EvidenceDeny || len(records) != 1 || !records[0].Snapshot.Complete || records[0].Snapshot.Typed == nil {
		t.Fatalf("clean typed forbid live = %v, records = %+v", live.Outcome, records)
	}
	writer := New()
	writer.UseData(api.NewModuleData(f.st))
	if err := writer.RecordAuthorization(t.Context(), records[0]); err != nil {
		t.Fatal(err)
	}
	// A fresh reader has neither the live authorizer nor a compiled policy.
	reader := New()
	reader.UseData(api.NewModuleData(f.st))
	got, err := reader.Reconstruct(t.Context(), f.tenant, ReconstructRequest{
		At: typedEvidenceNow.Add(2 * time.Second), Principal: principal.UserID.String(), Resource: agent.ID.String(), ResourceKind: "agent",
		Action: "agent:read", SourceInstance: "olivares", ActionVocabulary: "olivares.permission.v1",
	})
	if err != nil || got.Status != ReconstructReconstructed || got.Outcome != sdk.AccessOutcomeDeny || got.RecordedOutcome != sdk.AccessOutcomeDeny || got.UsedLivePolicy {
		t.Fatalf("clean typed forbid replay = %+v, %v; want reconstructed deny", got, err)
	}
}

func TestRetainedAuthorizationRefusesAnArtifactForAnotherQuestion(t *testing.T) {
	f := newTypedEvidenceFixture(t)
	p := f.confinedPrincipal(t, "")
	req := auth.Request{Principal: p, Tenant: f.tenant, Permission: "agent:write",
		Resource: auth.ResourceAttrs{Kind: "agent", ID: model.NewID().String()}}
	var record auth.AuthorizationRecord
	ctx := auth.WithAuthorizationRecording(t.Context(), func(r auth.AuthorizationRecord) { record = r })
	if !auth.NewAuthorizer(nil, auth.WithClock(func() time.Time { return typedEvidenceNow })).Authorize(ctx, req).Allow {
		t.Fatal("editor write was refused")
	}
	writer := New()
	writer.UseData(api.NewModuleData(f.st))
	if err := writer.RecordAuthorization(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	question := QuestionForReplay(p.UserID.String(), "olivares", "agent", req.Resource.ID, "agent:write", "olivares.permission.v1")
	digest, err := question.Digest()
	if err != nil {
		t.Fatal(err)
	}
	var original model.AuthorizationDecision
	if err := f.st.View(t.Context(), f.tenant, func(sc store.Scope) error {
		rows, err := sc.AccessEvidence().AuthorizationDecisionsForQuestion(t.Context(), digest)
		if err == nil && len(rows) != 1 {
			t.Fatalf("original answers = %d, want one", len(rows))
		}
		if err == nil {
			original = rows[0]
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	reader := New()
	reader.UseData(api.NewModuleData(f.st))
	if got, err := reader.Reconstruct(t.Context(), f.tenant, ReconstructRequest{DecisionID: original.ID}); err != nil || got.Status != ReconstructReconstructed || got.Outcome != sdk.AccessOutcomeAllow {
		t.Fatalf("original retained question = %+v, %v", got, err)
	}
	for name, change := range map[string]func(*sdk.AccessQuestion){
		"principal": func(q *sdk.AccessQuestion) {
			q.PrincipalRef, q.ActorRef = model.NewID().String(), model.NewID().String()
		},
		"resource kind": func(q *sdk.AccessQuestion) { q.ResourceKind = "session" },
		"resource":      func(q *sdk.AccessQuestion) { q.ResourceRef = model.NewID().String() },
		"action":        func(q *sdk.AccessQuestion) { q.Action = "agent:admin" },
		"source":        func(q *sdk.AccessQuestion) { q.SourceInstance = "another-engine" },
		"vocabulary":    func(q *sdk.AccessQuestion) { q.ActionVocabulary = "another.permission.v1" },
		"context":       func(q *sdk.AccessQuestion) { q.Context = map[string]string{"workspace": "another-workspace"} },
	} {
		t.Run(name, func(t *testing.T) {
			decision := original.Decision
			change(&decision.Question)
			decision, err := sdk.StampDecisionReconstructionFields(decision)
			if err != nil {
				t.Fatal(err)
			}
			var appended model.AuthorizationDecision
			if err := f.st.Mutate(t.Context(), f.tenant, func(sc store.Scope) error {
				got, err := sc.AccessEvidence().AppendAuthorizationDecision(t.Context(), store.AuthorizationDecisionAppend{
					AccessEvidenceAppend: store.AccessEvidenceAppend{Actor: record.Actor, ActorKind: record.ActorKind,
						Envelope: sdk.AccessEvidenceEnvelope{SchemaVersion: sdk.AccessEvidenceSchemaVersion,
							ProducerInstance: "question-test", SourceEventID: model.NewID().String(),
							EventType: sdk.EventTypeAuthorizationDecision, AdapterVersion: "1", OccurredAt: sdk.FormatEvidenceTime(typedEvidenceNow)}},
					Decision: decision,
				})
				appended = got.Record
				return err
			}); err != nil {
				t.Fatal(err)
			}
			got, err := reader.Reconstruct(t.Context(), f.tenant, ReconstructRequest{DecisionID: appended.ID})
			if err != nil || got.Status != ReconstructInsufficient || got.Missing != MissingQuestion || !got.CouldNotReconstruct || got.UsedLivePolicy {
				t.Fatalf("artifact attached to another question = %+v, %v; want missing question", got, err)
			}
		})
	}
}

func TestRetainedAuthorizationReportsUnavailableInputs(t *testing.T) {
	for _, stepUp := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy policy", true: "authentication precondition"}[stepUp], func(t *testing.T) {
			f := newTypedEvidenceFixture(t)
			p := f.confinedPrincipal(t, "")
			req := auth.Request{Principal: p, Tenant: f.tenant, Permission: "agent:write",
				Resource: auth.ResourceAttrs{Kind: "agent", ID: model.NewID().String()}}
			var record auth.AuthorizationRecord
			ctx := auth.WithAuthorizationRecording(t.Context(), func(r auth.AuthorizationRecord) { record = r })
			az := auth.NewAuthorizer(unretainedReplayPolicy{}, auth.WithClock(func() time.Time { return typedEvidenceNow }))
			want := sdk.AccessOutcomeAllow
			if stepUp {
				login := auth.NewAuthenticator(f.st, nil)
				token, _, err := login.Login(t.Context(), "typed-evidence-confined@example.test", "strong-password-2", "127.0.0.1")
				if err != nil {
					t.Fatal(err)
				}
				req.Principal, err = login.Authenticate(t.Context(), token)
				if err != nil {
					t.Fatal(err)
				}
				req.Route.MinimumAAL = auth.AAL3
				if _, err := az.AuthorizeRoute(ctx, req); !errors.Is(err, auth.ErrStepUpRequired) {
					t.Fatalf("real password session step-up = %v", err)
				}
				want = sdk.AccessOutcomeDeny
			} else if !az.Authorize(ctx, req).Allow {
				t.Fatal("legacy adapter's live allow changed")
			}
			if record.Snapshot.Complete {
				t.Fatal("missing evaluator/precondition inputs claimed complete")
			}
			writer := New()
			writer.UseData(api.NewModuleData(f.st))
			if err := writer.RecordAuthorization(t.Context(), record); err != nil {
				t.Fatal(err)
			}
			reader := New()
			reader.UseData(api.NewModuleData(f.st))
			got, err := reader.Reconstruct(t.Context(), f.tenant, ReconstructRequest{
				At: typedEvidenceNow.Add(time.Second), Principal: p.UserID.String(), Resource: req.Resource.ID,
				ResourceKind: "agent", Action: "agent:write", SourceInstance: "olivares", ActionVocabulary: "olivares.permission.v1",
			})
			if err != nil || got.Status != ReconstructInsufficient || !got.CouldNotReconstruct || got.ReasonCode != CouldNotReconstruct || got.RecordedOutcome != want || got.UsedLivePolicy {
				t.Fatalf("unavailable inputs = %+v, %v; want explicit insufficient with recorded %s", got, err, want)
			}
		})
	}
}
