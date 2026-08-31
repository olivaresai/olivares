// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func k4RawConfig(t *testing.T, in map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func k4ValidConfigs() map[string]map[string]any {
	workID := model.NewID().String()
	channelID := model.NewID().String()
	bindingID := model.NewID().String()
	targetID := model.NewID().String()
	deadline := model.NewTimestamp(time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)).String()
	return map[string]map[string]any{
		stepWorkCreate: {
			"workspace_id": model.NewID().String(), "work_kind": "implementation",
			"title": "Implement K4", "brief_ref": "briefs/k4", "priority": "p1",
			"owner": map[string]any{"kind": "session", "ref": "osn_worker"},
			"criteria": []map[string]any{{
				"key": "tests", "ordinal": 1, "statement": "The normal tests pass.", "required": true,
			}},
			"provenance": map[string]any{"kind": "workflow", "ref": "kernel-k4"},
		},
		stepWorkAssign: {
			"work_item_id": workID, "expected_owner_epoch": 1,
			"target": map[string]any{"kind": "agent", "ref": "agent:builder"}, "require_ack": false,
		},
		stepWorkClaim: {
			"work_item_id": workID, "sid": "osn_worker", "ttl_seconds": 300,
		},
		stepSessionLaunch: {
			"work_item_id": workID, "owner_epoch": 1, "fence": 2,
			"runtime_profile_ref": "runtime/default",
		},
		stepWorkMessage: {
			"work_item_id": workID, "channel_id": channelID,
			"recipient": map[string]any{"kind": "session", "ref": "osn_worker"},
			"body":      "Continue the governed work.", "ack_due_at": deadline, "urgency": "high",
		},
		stepWorkWaitAck: {
			"target_kind": "message", "target_id": targetID, "deadline": deadline,
			"after_event_seq": 4,
		},
		stepWorkHandoff: {
			"work_item_id": workID, "channel_id": channelID,
			"target":      map[string]any{"kind": "session", "ref": "osn_successor"},
			"context_ref": "work-events/4", "ack_deadline": deadline,
		},
		stepWorkTransition: {
			"work_item_id": workID, "target_state": "review", "evidence_ref": "run:42",
		},
		stepWorkCancel: {
			"work_item_id": workID, "binding_id": bindingID, "reason": "Operator canceled the run.",
		},
		stepWorkReconcile: {"binding_id": bindingID},
	}
}

func TestK4WorkStepConfigsCanonicalize(t *testing.T) {
	for kind, config := range k4ValidConfigs() {
		t.Run(kind, func(t *testing.T) {
			canonical, ge := canonicalStepConfig(kind, k4RawConfig(t, config))
			if ge != nil {
				t.Fatalf("canonicalStepConfig: %v", ge)
			}
			if !json.Valid(canonical) {
				t.Fatalf("canonical config is not JSON: %s", canonical)
			}
			replayed, ge := canonicalStepConfig(kind, canonical)
			if ge != nil || string(replayed) != string(canonical) {
				t.Fatalf("canonical replay = %s, %v; want %s", replayed, ge, canonical)
			}
			if kind == stepSessionLaunch && !jsonContainsString(t, canonical, "attempt_kind", "lease-bind") {
				t.Fatalf("session-launch did not materialize the default attempt_kind: %s", canonical)
			}
		})
	}
}

func jsonContainsString(t *testing.T, raw json.RawMessage, key, want string) bool {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	return got[key] == want
}

func TestK4WorkStepConfigsRejectIncompleteInput(t *testing.T) {
	valid := k4ValidConfigs()
	cases := map[string]map[string]any{
		stepWorkCreate:     {"workspace_id": model.NewID().String()},
		stepWorkAssign:     {"work_item_id": model.NewID().String(), "expected_owner_epoch": 0},
		stepWorkClaim:      {"work_item_id": model.NewID().String(), "sid": "osn_x", "ttl_seconds": 0},
		stepSessionLaunch:  {"work_item_id": model.NewID().String(), "owner_epoch": 1, "fence": -1, "runtime_profile_ref": "default"},
		stepWorkMessage:    {"work_item_id": model.NewID().String(), "channel_id": model.NewID().String(), "recipient": map[string]any{"kind": "session", "ref": "osn_x"}},
		stepWorkWaitAck:    {"target_kind": "delivery", "target_id": model.NewID().String(), "deadline": "tomorrow"},
		stepWorkHandoff:    {"work_item_id": model.NewID().String(), "channel_id": model.NewID().String(), "target": map[string]any{"kind": "session", "ref": "osn_x"}},
		stepWorkTransition: {"work_item_id": model.NewID().String(), "target_state": "active"},
		stepWorkCancel:     {"work_item_id": model.NewID().String()},
		stepWorkReconcile:  {},
	}
	for kind, config := range cases {
		t.Run(kind, func(t *testing.T) {
			if _, ge := canonicalStepConfig(kind, k4RawConfig(t, config)); ge == nil {
				t.Fatal("incomplete config was accepted")
			}
		})
	}

	withUnknown := valid[stepWorkClaim]
	withUnknown["transport_retry"] = 7
	if _, ge := canonicalStepConfig(stepWorkClaim, k4RawConfig(t, withUnknown)); ge == nil {
		t.Fatal("unknown transport field was accepted")
	}
}

func TestK4WorkAssignAcknowledgementRequiresCompleteHandoff(t *testing.T) {
	config := k4ValidConfigs()[stepWorkAssign]
	config["require_ack"] = true
	if _, ge := canonicalStepConfig(stepWorkAssign, k4RawConfig(t, config)); ge == nil {
		t.Fatal("require_ack without handoff fields was accepted")
	}
	config["channel_id"] = model.NewID().String()
	config["context_ref"] = "work-context:transfer"
	config["ack_deadline"] = model.NewTimestamp(time.Now().Add(time.Hour)).String()
	if _, ge := canonicalStepConfig(stepWorkAssign, k4RawConfig(t, config)); ge != nil {
		t.Fatalf("complete acknowledged assignment rejected: %v", ge)
	}
}

func TestK4WorkTransitionRequiresReasonForExceptionalStates(t *testing.T) {
	workID := model.NewID().String()
	for _, state := range []string{"blocked", "failed", "canceled"} {
		t.Run(state, func(t *testing.T) {
			config := map[string]any{"work_item_id": workID, "target_state": state}
			if _, ge := canonicalStepConfig(stepWorkTransition, k4RawConfig(t, config)); ge == nil {
				t.Fatal("transition without reason was accepted")
			}
			config["reason"] = "The workflow observed an exceptional terminal condition."
			if _, ge := canonicalStepConfig(stepWorkTransition, k4RawConfig(t, config)); ge != nil {
				t.Fatalf("transition with reason rejected: %v", ge)
			}
		})
	}
	for _, state := range []string{"ready", "review", "completed"} {
		t.Run(state, func(t *testing.T) {
			config := map[string]any{"work_item_id": workID, "target_state": state}
			if _, ge := canonicalStepConfig(stepWorkTransition, k4RawConfig(t, config)); ge != nil {
				t.Fatalf("transition without optional reason rejected: %v", ge)
			}
		})
	}
}

func TestK4WorkStepsPersistInWorkflowAndRunSnapshot(t *testing.T) {
	h, _ := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "k4-model")

	configs := k4ValidConfigs()
	steps := make([]map[string]any, 0, len(configs))
	for _, kind := range []string{
		stepWorkCreate, stepWorkAssign, stepWorkClaim, stepSessionLaunch, stepWorkMessage,
		stepWorkWaitAck, stepWorkHandoff, stepWorkTransition, stepWorkCancel, stepWorkReconcile,
	} {
		steps = append(steps, step(kind, kind, configs[kind]))
	}
	created := h.createWorkflow(admin, tenant, "k4-work-kernel", steps)
	if got := int(created["step_count"].(float64)); got != len(steps) {
		t.Fatalf("step_count = %d, want %d", got, len(steps))
	}

	workID := configs[stepWorkAssign]["work_item_id"].(string)
	waitCfg := configs[stepWorkWaitAck]
	snapshot := []runStepState{
		{
			Ref: "work-assign", Kind: stepWorkAssign, Config: k4RawConfig(t, configs[stepWorkAssign]),
			DependsOn: []string{}, Status: stepStatusPending, WorkItemID: workID,
			AttemptSemantic: "primary",
		},
		{
			Ref: "wait", Kind: stepWorkWaitAck, Config: k4RawConfig(t, waitCfg),
			DependsOn: []string{"work-assign"}, Status: stepStatusPending,
			AttemptSemantic: "primary", WaitingTargetKind: waitCfg["target_kind"].(string),
			WaitingTargetID:      waitCfg["target_id"].(string),
			WaitingAfterEventSeq: int64(waitCfg["after_event_seq"].(int)),
			WaitingDeadline:      waitCfg["deadline"].(string),
		},
	}

	ctx := context.Background()
	var runID model.ID
	if err := h.moduleCtx(tenant).Data.Mutate(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(wfRunKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(ctx, model.Record{
			colWrWorkflow: created["id"].(string), colWrRootWork: workID,
			colWrStatus: runStatusRunning, colWrPlanHash: created["plan_hash"].(string),
			colWrApproval: nil, colWrPaused: nil, colWrSteps: encodeRunSteps(snapshot),
			colWrActor: "test", colWrActorKind: "user",
			colWrStartedAt: model.NewTimestamp(time.Now()).String(), colWrFinished: nil,
		})
		if err == nil {
			runID = model.ID(rec.String(model.ColID))
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if err := h.moduleCtx(tenant).Data.View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(wfRunKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, runID)
		if err != nil {
			return err
		}
		decoded, err := decodeRunSteps(rec.String(colWrSteps))
		if err != nil {
			return err
		}
		got := toRunDTO(rec, decoded)
		if got.RootWorkItemID != workID || len(got.Steps) != 2 ||
			got.Steps[1].WaitingTargetID != snapshot[1].WaitingTargetID ||
			got.Steps[1].WaitingAfterEventSeq != 4 || got.Steps[1].AttemptSemantic != "primary" {
			t.Fatalf("durable K4 snapshot mismatch: %+v", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
