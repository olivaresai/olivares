// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/eventing"
	"github.com/olivaresai/olivares/modules/finops"
)

// retirementSubject is the account a retirement case seeds rows for.
type retirementSubject struct {
	id         model.ID
	email      string
	externalID string
	// token is the account's own stepped-up session, for a case whose row the
	// account writes itself; empty otherwise.
	token string
}

// retirementOutcome is what a declared module's step must do with a seeded row.
type retirementOutcome string

const (
	// retireDeletes: the step deletes the row and the retirement completes.
	retireDeletes retirementOutcome = "deletes"
	// retireBlocks: the step keeps the row and the record lists it as blocking.
	retireBlocks retirementOutcome = "blocks"
	// retireKeeps: the step keeps the row and the retirement completes.
	retireKeeps retirementOutcome = "keeps"
	// retireRevokes: the step revokes the row, which the schema never deletes,
	// and the retirement completes.
	retireRevokes retirementOutcome = "revokes"
)

// retirementSeeder writes one row naming the subject in the tenant.
type retirementSeeder struct {
	kind    model.Kind
	outcome retirementOutcome
	seed    func(e *consentEstate, s retirementSubject) model.ID
}

// seedFencedWith is seedFenced with a step run inside the transaction after the
// barrier and before the write.
func (e *consentEstate) seedFencedWith(tenant model.TenantID, kind model.Kind, rec model.Record, before func(context.Context, store.Scope) error, users ...model.ID) model.ID {
	e.t.Helper()
	ctx := context.Background()
	refs := e.authorityRefs(users...)
	var id model.ID
	if err := e.eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
		fact, err := directoryEpochFact(ctx, sc)
		if err != nil {
			return err
		}
		locker, ok := sc.(store.DirectoryAuthoritySnapshotLocker)
		if !ok {
			return errors.New("the scope exposes no directory authority barrier")
		}
		if err := locker.LockDirectoryAuthoritySnapshot(ctx, store.AuthoritySnapshotBundle{
			Facts: []store.AuthorizationFactRef{fact}, UserAuthorities: refs,
		}); err != nil {
			return err
		}
		if before != nil {
			if err := before(ctx, sc); err != nil {
				return err
			}
		}
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		created, err := repo.Create(ctx, rec)
		if err != nil {
			return err
		}
		id = model.ID(created.String(model.ColID))
		return nil
	}); err != nil {
		e.t.Fatalf("seed %s: %v", kind, err)
	}
	return id
}

// policyExists reports whether policy id is still stored in tenant.
func (e *consentEstate) policyExists(tenant model.TenantID, id model.ID) bool {
	e.t.Helper()
	ctx := context.Background()
	found := false
	if err := e.eng.store.View(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Policies().Get(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		found = err == nil
		return err
	}); err != nil {
		e.t.Fatalf("read policy: %v", err)
	}
	return found
}

// subscriptionRecord is a raw webhook subscription owned by user:<id>.
func subscriptionRecord(name string, owner model.ID) model.Record {
	return model.Record{
		"name": name, "enabled": true, "event_types": "workflow.signal", "endpoint": "https://hooks.example.test/in",
		"secret_sealed": "sealed", "secret_hint": "hint", "role": "viewer",
		"owner_actor": "user:" + owner.String(), "owner_actor_kind": "user",
	}
}

// seedSubscription writes a subscription owned by the subject with the writer
// proof the subscription table requires.
func (e *consentEstate) seedSubscription(tenant model.TenantID, name string, owner model.ID) model.ID {
	e.t.Helper()
	ctx := context.Background()
	fence, ok := newEventingWriterFence(e.eng.store)
	if !ok {
		e.t.Fatal("the store exposes no rollout state for the subscription writer fence")
	}
	gen, err := eventing.FenceGeneration(ctx, fence)
	if err != nil {
		e.t.Fatalf("read the writer fence: %v", err)
	}
	rec := subscriptionRecord(name, owner)
	return e.seedFencedWith(tenant, "eventing.subscription", rec, func(ctx context.Context, sc store.Scope) error {
		return eventing.StampWriterProof(ctx, sc, rec, gen)
	}, owner)
}

func scheduleRecord(name string, owner model.ID) model.Record {
	return model.Record{
		"name": name, "subject_kind": "agent", "subject_ref": "agent-" + name, "trigger_kind": "manual",
		"expected_interval_seconds": int64(0), "grace_factor": int64(2), "desired_status": "active",
		"owner_actor": "user:" + owner.String(), "owner_actor_kind": "user", "owner_user_ref": owner.String(),
	}
}

func runRecord(actor model.ID) model.Record {
	return model.Record{
		"workflow_ref": model.NewID().String(), "status": "running", "plan_hash": "sha256:seeded", "steps": "[]",
		"actor": "user:" + actor.String(), "actor_kind": "user", "actor_user_identity": actor.String(),
		"started_at": model.NewTimestamp(time.Now()).String(),
	}
}

// runNaming is a running run that names actor only through column: its
// initiator reference, its initiator's bare account id, or a work-create step
// frozen into the run that makes the account the work's owner.
func runNaming(t *testing.T, column string, actor model.ID) model.Record {
	rec := model.Record{
		"workflow_ref": model.NewID().String(), "status": "running", "plan_hash": "sha256:seeded", "steps": "[]",
		"actor": "system:seeded", "actor_kind": "system",
		"started_at": model.NewTimestamp(time.Now()).String(),
	}
	switch column {
	case "actor":
		rec["actor"], rec["actor_kind"] = "user:"+actor.String(), "user"
	case "actor_user_identity":
		rec["actor_user_identity"] = actor.String()
	case "steps":
		rec["steps"] = frozenSteps(t, actor)
	default:
		t.Fatalf("a run names no account through %s", column)
	}
	return rec
}

// frozenSteps is a run's step state holding a pending work-create step that
// makes owner the work's owner.
func frozenSteps(t *testing.T, owner model.ID) string {
	t.Helper()
	var steps []map[string]any
	if err := json.Unmarshal([]byte(workCreateSteps(t, owner)), &steps); err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		step["status"] = "pending"
	}
	b, err := json.Marshal(steps)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// scheduleNaming is a schedule that names owner only through column: its owner
// reference, or its owner's bare account id.
func scheduleNaming(t *testing.T, name, column string, owner model.ID) model.Record {
	rec := scheduleRecord(name, owner)
	switch column {
	case "owner_actor":
		delete(rec, "owner_user_ref")
	case "owner_user_ref":
		rec["owner_actor"], rec["owner_actor_kind"] = "system:seeded", "system"
	default:
		t.Fatalf("a schedule names no account through %s", column)
	}
	return rec
}

// created returns the id a product route answered 201 with.
func (e *consentEstate) created(r consentResp, what string) model.ID {
	e.t.Helper()
	id, _ := r.body["id"].(string)
	if r.code != http.StatusCreated || id == "" {
		e.t.Fatalf("create %s = %d %s", what, r.code, r.raw)
	}
	return model.ID(id)
}

// attemptBinding is a spend attempt's resolved binding whose subject is actor.
func attemptBinding(actor model.ID) string {
	return `{"status":"resolved","request_ref":"seeded","subject":{"actor_ref":"user:` + actor.String() +
		`","actor_kind":"user","user_id":"` + actor.String() + `"},"attribution":{},"apply_seat_limits":true,"authority_refs":[]}`
}

// attemptRecord is a prepared spend attempt with binding and targets.
func attemptRecord(binding, targets string) model.Record {
	ref := strings.ReplaceAll(model.NewID().String(), "-", "")
	return model.Record{
		"contract_version": int64(1), "attempt_ref": ref, "request_ref": ref, "handle": model.NewID().String(),
		"phase": "prepared", "binding_digest": strings.Repeat("a", 64), "reservation_digest": strings.Repeat("b", 64),
		"binding": binding, "targets": targets, "review_after": model.NewTimestamp(time.Now().Add(time.Hour)).String(),
		"owner_ref": "test", "owner_epoch": int64(1), "publication_state": "none",
		"accounting_basis": `{"kind":"attempt_admission","evidence":[]}`,
	}
}

// retirementSeeders maps every counted declaration, as "<kind>.<column>", to the
// row that exercises it and what the declared module's step must do with it.
var retirementSeeders = map[string]retirementSeeder{
	"governance.scoped_grant.subject_ref": {"governance.scoped_grant", retireDeletes, func(e *consentEstate, s retirementSubject) model.ID {
		return model.ID(e.grantUser(e.tT, s.id, "editor"))
	}},
	"governance.policy_revision.content": {"governance.policy_revision", retireBlocks, func(e *consentEstate, s retirementSubject) model.ID {
		return e.seedFenced(e.tT, "governance.policy_revision", model.Record{
			"surface": "cedar", "revision": int64(900), "content": `permit(principal in User::"` + s.id.String() + `", action, resource);`,
			"author": "test", "validated": true, "active": true,
		}, s.id)
	}},
	"governance.nhi_lifecycle.sponsor_ref": {"governance.nhi_lifecycle", retireBlocks, func(e *consentEstate, s retirementSubject) model.ID {
		return e.seedFenced(e.tT, "governance.nhi_lifecycle", nhiRecord("agent-sponsored", "sponsor_ref", s.externalID), s.id)
	}},
	"governance.nhi_lifecycle.owner_ref": {"governance.nhi_lifecycle", retireBlocks, func(e *consentEstate, s retirementSubject) model.ID {
		return e.seedFenced(e.tT, "governance.nhi_lifecycle", nhiRecord("agent-owned", "owner_ref", s.externalID), s.id)
	}},
	"sourcescope.binding.scope_ref": {"sourcescope.binding", retireDeletes, func(e *consentEstate, s retirementSubject) model.ID {
		return e.created(e.do("POST", "/v1/m/sourcescope/bindings", e.admin, e.tT, map[string]any{
			"source_type": "mcp", "source_ref": "seeded-" + s.id.String(), "scope_tree": "user", "scope_ref": s.id.String(),
			"cred_name": "C", "cred_ref_kind": "env", "cred_ref": "SOURCE_C", "enabled": true,
		}), "the user binding")
	}},
	"models.model_access.subject_ref": {"models.model_access", retireDeletes, func(e *consentEstate, s retirementSubject) model.ID {
		return e.created(e.do("POST", "/v1/m/models/model-access", e.admin, e.tT, map[string]any{
			"subject_kind": "user", "subject_ref": s.id.String(), "target_kind": "model", "target_ref": "claude-opus-4-8", "effect": "allow",
		}), "the model-access allow")
	}},
	"orchestration.workflow.steps": {"orchestration.workflow", retireBlocks, func(e *consentEstate, s retirementSubject) model.ID {
		var steps []map[string]any
		if err := json.Unmarshal([]byte(workCreateSteps(e.t, s.id)), &steps); err != nil {
			e.t.Fatal(err)
		}
		return e.created(e.do("POST", "/v1/m/orchestration/workflows", e.admin, e.tT, map[string]any{
			"name": "seeded-" + s.id.String(), "steps": steps,
		}), "the workflow")
	}},
	// A run has no product writer that names another account: its initiator is
	// the caller. Each column is seeded alone through the guarded store seam.
	"orchestration.workflow_run.actor": {"orchestration.workflow_run", retireBlocks, func(e *consentEstate, s retirementSubject) model.ID {
		return e.seedFenced(e.tT, "orchestration.workflow_run", runNaming(e.t, "actor", s.id), s.id)
	}},
	"orchestration.workflow_run.actor_user_identity": {"orchestration.workflow_run", retireBlocks, func(e *consentEstate, s retirementSubject) model.ID {
		return e.seedFenced(e.tT, "orchestration.workflow_run", runNaming(e.t, "actor_user_identity", s.id), s.id)
	}},
	"orchestration.workflow_run.steps": {"orchestration.workflow_run", retireBlocks, func(e *consentEstate, s retirementSubject) model.ID {
		return e.seedFenced(e.tT, "orchestration.workflow_run", runNaming(e.t, "steps", s.id), s.id)
	}},
	// A schedule's product writer makes its caller the owner; the subject has no
	// credential here, so each owner column is seeded alone through the guarded
	// store seam.
	"orchestration.schedule.owner_user_ref": {"orchestration.schedule", retireBlocks, func(e *consentEstate, s retirementSubject) model.ID {
		return e.seedFenced(e.tT, "orchestration.schedule", scheduleNaming(e.t, "owned-"+s.id.String(), "owner_user_ref", s.id), s.id)
	}},
	"orchestration.schedule.owner_actor": {"orchestration.schedule", retireBlocks, func(e *consentEstate, s retirementSubject) model.ID {
		return e.seedFenced(e.tT, "orchestration.schedule", scheduleNaming(e.t, "actor-"+s.id.String(), "owner_actor", s.id), s.id)
	}},
	"eventing.subscription.owner_actor": {"eventing.subscription", retireBlocks, func(e *consentEstate, s retirementSubject) model.ID {
		return e.seedSubscription(e.tT, "owned-"+s.id.String(), s.id)
	}},
	// A user spend cap is written by the gateway's spend-limit route.
	"core.policy.spec": {"core.policy", retireBlocks, func(e *consentEstate, s retirementSubject) model.ID {
		r := e.putSpendCap(e.spendLimitGateway(), s.id)
		wire, _ := r.body["id"].(string)
		id, err := finops.ParseSpendLimitID(wire)
		if r.code != http.StatusOK || err != nil {
			e.t.Fatalf("set the account's spend cap = %d %s", r.code, r.raw)
		}
		return id
	}},
	"finops.attempt.binding": {"finops.attempt", retireBlocks, func(e *consentEstate, s retirementSubject) model.ID {
		return e.seedFenced(e.tT, "finops.attempt", attemptRecord(attemptBinding(s.id), "[]"), s.id)
	}},
	"finops.attempt.targets": {"finops.attempt", retireBlocks, func(e *consentEstate, s retirementSubject) model.ID {
		targets := `[{"child_id":"` + model.NewID().String() + `","policy_id":"` + model.NewID().String() +
			`","policy_kind":"spend_limit","dimension":"spend_limit","scope_key":"user:` + s.id.String() +
			`","period":"monthly","period_start":"","period_end":"","has_period_bounds":false,"action":"block","membership":[]}]`
		return e.seedFenced(e.tT, "finops.attempt", attemptRecord(attemptBinding(model.NewID()), targets), s.id)
	}},
	"compliance.legal_hold.subject_ref": {"compliance.legal_hold", retireBlocks, func(e *consentEstate, s retirementSubject) model.ID {
		return e.seedFenced(e.tT, "compliance.legal_hold", model.Record{
			"matter_ref": "matter-" + s.id.String(), "scope_kind": "subject", "subject_kind": "user",
			"subject_ref": "user:" + s.id.String(), "reason": "test", "status": "active", "created_by": "test",
		}, s.id)
	}},
}

// queuedRunNaming retains the account through exactly one queued-launch column.
// A still-pending launch blocks retirement until it is declined or stopped.
func queuedRunNaming(e *consentEstate, column string, subject retirementSubject) model.Record {
	e.t.Helper()
	rec := model.Record{
		"run_ref": model.NewID().String(), "transport": "stream-json", "permission_mode": "default",
		"isolation": "native", "state": "waiting_approval", "last_event_seq": int64(0),
	}
	switch column {
	case "queued_user_id":
		rec[column] = subject.id.String()
	case "queued_actor":
		rec[column], rec["queued_actor_kind"] = "user:"+subject.id.String(), "user"
	case "queued_credential_id":
		credential, err := auth.NewCredential(auth.PrefixToken)
		if err != nil {
			e.t.Fatal(err)
		}
		ctx := context.Background()
		if err := e.eng.store.AuthMutate(ctx, func(as store.AuthScope) error {
			token, err := as.Tokens().Create(ctx, model.APIToken{
				Name: "queued-launch", UserID: subject.id, BoundTenantID: e.tT,
				Role: auth.RoleViewer, Selector: credential.Selector, SecretHash: credential.SecretHash,
			})
			if err == nil {
				rec[column], rec["queued_credential_kind"] = token.ID.String(), "token"
			}
			return err
		}); err != nil {
			e.t.Fatalf("seed the queued launch credential: %v", err)
		}
	default:
		e.t.Fatalf("a queued launch names no account through %s", column)
	}
	return rec
}

// sessionsSeeders are the communication rows a cmd test can seed without a full
// message graph; messagingSeeders holds the rest, which need an activated
// communication kernel.
var sessionsSeeders = map[string]retirementSeeder{
	"sessions.run.queued_user_id": {"sessions.run", retireBlocks, func(e *consentEstate, s retirementSubject) model.ID {
		return e.seedFenced(e.tT, "sessions.run", queuedRunNaming(e, "queued_user_id", s), s.id)
	}},
	"sessions.run.queued_actor": {"sessions.run", retireBlocks, func(e *consentEstate, s retirementSubject) model.ID {
		return e.seedFenced(e.tT, "sessions.run", queuedRunNaming(e, "queued_actor", s), s.id)
	}},
	"sessions.run.queued_credential_id": {"sessions.run", retireBlocks, func(e *consentEstate, s retirementSubject) model.ID {
		return e.seedFenced(e.tT, "sessions.run", queuedRunNaming(e, "queued_credential_id", s), s.id)
	}},
	"sessions.channel_grant.subject_ref": {"sessions.channel_grant", retireRevokes, func(e *consentEstate, s retirementSubject) model.ID {
		workspace, channel := e.seedChannel(e.tT, "grant-"+s.id.String())
		return e.seedFenced(e.tT, "sessions.channel_grant", model.Record{
			"workspace_id": workspace.String(), "channel_id": channel.String(),
			"subject_kind": "user", "subject_ref": s.id.String(), "generation": int64(1),
			"can_read": true, "can_write": false, "can_admin": false, "state": "active",
			"granted_by_kind": "system", "granted_by_ref": model.NewID().String(),
		}, s.id)
	}},
	"sessions.channel_subscription.subscriber_ref": {"sessions.channel_subscription", retireBlocks, func(e *consentEstate, s retirementSubject) model.ID {
		workspace, channel := e.seedChannel(e.tT, "subscription-"+s.id.String())
		return e.seedFenced(e.tT, "sessions.channel_subscription", model.Record{
			"workspace_id": workspace.String(), "channel_id": channel.String(),
			"subscriber_kind": "user", "subscriber_ref": s.id.String(), "generation": int64(1),
			"mode": "all", "wake": "none", "required_for_critical": false, "state": "active",
		}, s.id)
	}},
	"sessions.communication_endpoint.owner_ref": {"sessions.communication_endpoint", retireBlocks, func(e *consentEstate, s retirementSubject) model.ID {
		return e.seedFenced(e.tT, "sessions.communication_endpoint", model.Record{
			"workspace_id": e.defaultWorkspace(e.tT).String(), "owner_kind": "user", "owner_ref": s.id.String(),
			"provider_key": "mcp", "transport": "https", "endpoint_ref": "endpoint-" + s.id.String(),
			"capabilities_json": "{}", "support_level": "stable", "priority": int64(0), "state": "active",
			"generation": int64(1),
		}, s.id)
	}},
	"sessions.communication_binding.mcp_task_json": {"sessions.communication_binding", retireBlocks, func(e *consentEstate, s retirementSubject) model.ID {
		task := `{"owner":{"subject":"` + s.id.String() + `","is_delegated":false,"issuer":"https://issuer.test",` +
			`"client_id":"client"},"tool":"tool","required_scope":"scope","destructive":false,` +
			`"created_at":"2026-01-01T00:00:00Z","initial_status":"working","upstream_descriptor":"upstream",` +
			`"protocol_revision":"revision","origin_operation_id":"operation","origin_effect_digest":"digest"}`
		return e.seedFenced(e.tT, "sessions.communication_binding", model.Record{
			"workspace_id": e.defaultWorkspace(e.tT).String(), "binding_spec_id": model.NewID().String(),
			"binding_spec_generation": int64(1), "pinned_spec_hash": make([]byte, 32),
			"pinned_mapping_hash": make([]byte, 32), "pinned_losses_hash": make([]byte, 32),
			"protocol": "mcp", "protocol_version": "2025-11-25", "direction": "outbound",
			"peer_authority": "peer.test", "remote_resource_ref": "tasks", "attempt_id": model.NewID().String(),
			"dispatch_key_hash": make([]byte, 32), "reservation_hash": make([]byte, 32), "generation": int64(1),
			"synthetic_sid": "seeded", "owner_epoch": int64(1), "lease_fence": int64(1), "external_kind": "task",
			"local_state": "reserved", "remote_state": "working", "observation_verdict": "UNKNOWN",
			"observation_code": "seeded", "terminal": false, "cancel_requested": false,
			"mcp_task_json": task, "mcp_task_hash": make([]byte, 32),
			"last_command_id": model.NewID().String(), "last_event_id": model.NewID().String(),
			"last_event_seq": int64(1),
		}, s.id)
	}},
	"sessions.work_item.owner_ref": {"sessions.work_item", retireKeeps, func(e *consentEstate, s retirementSubject) model.ID {
		workspace := e.defaultWorkspace(e.tT)
		return e.seedFenced(e.tT, "sessions.work_item", model.Record{
			"workspace_id": workspace.String(), "work_kind": "task", "title": "T", "brief_md": "b",
			"brief_hash": make([]byte, 32), "context_refs": "[]", "status": "draft", "priority": "p2",
			"owner_kind": "user", "owner_ref": s.id.String(), "owner_epoch": int64(1),
			"provenance_kind": "human", "provenance_ref": "test", "acceptance_revision": int64(1), "last_event_seq": int64(1),
		}, s.id)
	}},
	"sessions.protocol_interrupt.recipient_user_id": {"sessions.protocol_interrupt", retireBlocks, func(e *consentEstate, s retirementSubject) model.ID {
		workspace := e.defaultWorkspace(e.tT)
		return e.seedFenced(e.tT, "sessions.protocol_interrupt", model.Record{
			"workspace_id": workspace.String(), "binding_id": model.NewID().String(), "binding_generation": int64(1),
			"work_item_id": model.NewID().String(), "protocol": "mcp", "remote_state": "input_required",
			"request_key_hash": make([]byte, 32), "request_content_hash": make([]byte, 32), "route_hash": make([]byte, 32),
			"channel_id": model.NewID().String(), "sender_user_id": model.NewID().String(),
			"recipient_user_id": s.id.String(), "interrupt_message_id": model.NewID().String(),
			"interrupt_delivery_id": model.NewID().String(), "interrupt_state": "pending",
		}, s.id)
	}},
}

func init() {
	for _, set := range []map[string]retirementSeeder{sessionsSeeders, messagingSeeders} {
		for k, v := range set {
			retirementSeeders[k] = v
		}
	}
}

// defaultWorkspace returns tenant's default workspace id.
func (e *consentEstate) defaultWorkspace(tenant model.TenantID) model.ID {
	e.t.Helper()
	ctx := context.Background()
	var id model.ID
	if err := e.eng.store.View(ctx, tenant, func(sc store.Scope) error {
		ws, err := sc.DefaultWorkspace(ctx)
		id = ws.ID
		return err
	}); err != nil {
		e.t.Fatalf("default workspace: %v", err)
	}
	return id
}

// seedChannel writes an active coordination channel into tenant's default
// workspace and returns the workspace and channel ids.
func (e *consentEstate) seedChannel(tenant model.TenantID, slug string) (model.ID, model.ID) {
	e.t.Helper()
	workspace := e.defaultWorkspace(tenant)
	id := e.seedFenced(tenant, "sessions.channel", model.Record{
		"workspace_id": workspace.String(), "slug": strings.ToLower(strings.ReplaceAll(slug, "_", "-"))[:32], "name": slug,
		"kind": "coordination", "state": "active", "sensitivity": "internal", "content_protection": "storage",
		"protection_generation": int64(1), "default_ack_policy": "none", "default_ack_timeout_ms": int64(0),
		"default_wake": "none", "max_fanout": int64(8), "max_automation_depth": int64(0),
		"acl_revision": int64(1), "route_revision": int64(1), "subscription_revision": int64(1),
	})
	return workspace, id
}

func nhiRecord(identityRef, refColumn, ref string) model.Record {
	return model.Record{
		"identity_ref": identityRef, "criticality": "medium", "max_age_seconds": int64(0),
		"staleness_status": "unknown", "enforcement": "monitor", "orphaned": false, "offboard_state": "none",
		"kind": "agent", refColumn: ref,
	}
}

// retirementSubjectIn provisions a tenant-created account with an external id.
func (e *consentEstate) retirementSubjectIn(tenant model.TenantID, label string) retirementSubject {
	e.t.Helper()
	email := label + "@kinds.test"
	ext := "ext-" + label
	id := model.ID(e.scimCreateExternal(tenant, email, ext))
	return retirementSubject{id: id, email: email, externalID: ext}
}

// rowState returns the state column of a stored row, or "" when it is absent.
func (e *consentEstate) rowState(tenant model.TenantID, kind model.Kind, id model.ID) string {
	e.t.Helper()
	return e.rowColumn(tenant, kind, id, "state")
}

// rowColumn returns column of a stored row, or "" when it is absent.
func (e *consentEstate) rowColumn(tenant model.TenantID, kind model.Kind, id model.ID, column string) string {
	e.t.Helper()
	ctx := context.Background()
	value := ""
	if err := e.eng.store.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		value = rec.String(column)
		return err
	}); err != nil {
		e.t.Fatalf("read %s: %v", kind, err)
	}
	return value
}

// seededRowExists reports whether a seeded row is still stored.
func (e *consentEstate) seededRowExists(kind model.Kind, id model.ID) bool {
	if kind == "core.policy" {
		return e.policyExists(e.tT, id)
	}
	return e.rowExists(e.tT, kind, id)
}

// TestTheTStepRetiresEveryDeclaredKind: for every counted declaration the
// composition's census finds, a row naming the retiring account is deleted, or
// kept and listed as blocking, by the declared module's step; a counted
// declaration with no case fails. The rows only the activated communication
// kernel writes run last, on an activated estate of their own, so every other
// case keeps the estate it always had.
func TestTheTStepRetiresEveryDeclaredKind(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		// Without the census there is no list of declarations to cover, and an
		// empty list is never a covered one.
		if e.eng.census == nil {
			t.Fatal("the composed store exposes no census")
		}
		for _, d := range e.eng.census.CensusDescriptors() {
			// The account's own rows in the auth partition are removed by the
			// offboard itself; the module steps retire what tenants store.
			if d.Kind.Namespace() == model.CoreNamespace && d.Kind != "core.policy" {
				continue
			}
			for _, col := range d.CountedColumns() {
				if _, ok := retirementSeeders[string(d.Kind)+"."+col]; !ok {
					t.Errorf("counted declaration %s.%s has no retirement case", d.Kind, col)
				}
			}
		}
		var keys, messaging []string
		for k := range retirementSeeders {
			if _, ok := messagingSeeders[k]; ok {
				messaging = append(messaging, k)
			} else {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		sort.Strings(messaging)
		for i, key := range keys {
			t.Run(key, func(t *testing.T) {
				sub := e.forSubtest(t)
				sub.retireSeeded(t, key, sub.retirementSubjectIn(sub.tT, fmt.Sprintf("kind-%d", i)))
			})
		}
		if len(messaging) == 0 {
			return
		}
		m := bootMessagingEstate(t, e.engine)
		for i, key := range messaging {
			t.Run(key, func(t *testing.T) {
				sub := m.forSubtest(t)
				sub.retireSeeded(t, key, sub.messagingSubject(fmt.Sprintf("messaging-%d", i)))
			})
		}
	})
}

// retireSeeded seeds the case key for subject, checks that the row names the
// subject through the case's column and no other counted column, removes the
// subject from T and checks what the declared module's step did with the row. On
// the messaging estate a two-party row must name the sender as its other party,
// and last the case's control row, which names another member, is untouched.
func (e *consentEstate) retireSeeded(t *testing.T, key string, subject retirementSubject) {
	t.Helper()
	s := retirementSeeders[key]
	row := s.seed(e, subject)
	column := strings.TrimPrefix(key, string(s.kind)+".")
	if s.kind.Namespace() != model.CoreNamespace {
		e.wantNamesOnlyThrough(t, s.kind, column, row, subject.id)
	}
	if sibling, ok := messagingSiblings[key]; ok && e.comm != nil {
		e.wantNames(t, s.kind, sibling, row, e.comm.sender.id)
	}
	e.scimDelete(e.tT, subject.id)
	e.runPump()
	present := e.seededRowExists(s.kind, row)
	switch s.outcome {
	case retireDeletes:
		if present {
			t.Errorf("the retirement left the %s row naming the account", s.kind)
		}
		e.wantRetired(t, subject.id, e.tT)
	case retireBlocks:
		if !present {
			t.Errorf("the retirement deleted the %s row the tenant must resolve", s.kind)
		}
		e.wantBlocked(t, subject.id, e.tT, row.String())
	case retireKeeps:
		if !present {
			t.Errorf("the retirement deleted the %s row", s.kind)
		}
		e.wantRetired(t, subject.id, e.tT)
	case retireRevokes:
		if state := e.rowState(e.tT, s.kind, row); state != "revoked" {
			t.Errorf("the %s row naming the account is %q after the retirement, want revoked", s.kind, state)
		}
		e.wantRetired(t, subject.id, e.tT)
	}
	if e.comm != nil {
		e.wantControlUntouched(t, key, subject.id)
	}
}

// TestOwnedObligationsBlockReadmissionUntilResolved: what the account owns in
// the tenant blocks its retirement, listed by id, until the tenant resolves it.
func TestOwnedObligationsBlockReadmissionUntilResolved(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		subject := e.retirementSubjectIn(e.tT, "obligations")
		schedule := e.seedFenced(e.tT, "orchestration.schedule", scheduleRecord("obligations", subject.id), subject.id)
		run := e.seedFenced(e.tT, "orchestration.workflow_run", runRecord(subject.id), subject.id)
		sub := e.seedSubscription(e.tT, "obligations", subject.id)
		e.scimDelete(e.tT, subject.id)
		e.runPump()
		e.wantBlocked(t, subject.id, e.tT, schedule.String(), run.String(), sub.String())
		if r := e.readmit(e.tT, subject.email, "viewer"); r.code != http.StatusConflict {
			t.Errorf("re-admission while the account owns obligations = %d %s, want 409", r.code, r.raw)
		}
		ctx := context.Background()
		if err := e.eng.store.Mutate(ctx, e.tT, func(sc store.Scope) error {
			for _, row := range []struct {
				kind model.Kind
				id   model.ID
			}{{"orchestration.schedule", schedule}, {"eventing.subscription", sub}} {
				repo, err := sc.Ext(row.kind)
				if err != nil {
					return err
				}
				if err := repo.Delete(ctx, row.id); err != nil {
					return err
				}
			}
			repo, err := sc.Ext("orchestration.workflow_run")
			if err != nil {
				return err
			}
			rec, err := repo.Get(ctx, run)
			if err != nil {
				return err
			}
			rec["status"] = "completed"
			rec["finished_at"] = model.NewTimestamp(time.Now()).String()
			_, err = repo.Update(ctx, rec)
			return err
		}); err != nil {
			t.Fatalf("resolve the obligations: %v", err)
		}
		e.runPump()
		e.wantRetired(t, subject.id, e.tT)
	})
}

// restoreRow writes a row straight into its table, past every writer, the way a
// restore re-materializes a snapshot. It returns the row id.
func (e *consentEstate) restoreRow(tenant model.TenantID, kind model.Kind, values model.Record) model.ID {
	e.t.Helper()
	var desc model.EntityDescriptor
	found := false
	if e.eng.census != nil {
		for _, d := range e.eng.census.CensusDescriptors() {
			if d.Kind == kind {
				desc, found = d, true
			}
		}
	}
	if !found {
		e.t.Fatalf("no registered descriptor for %s", kind)
	}
	id := model.NewID()
	now := model.NewTimestamp(time.Now()).String()
	row := model.Record{
		model.ColID: id.String(), model.ColTenantID: tenant.String(),
		model.ColCreatedAt: now, model.ColUpdatedAt: now, model.ColVersion: int64(1),
	}
	for k, v := range values {
		row[k] = v
	}
	cols := desc.AllColumns()
	args := make([]any, len(cols))
	for i, c := range cols {
		args[i] = row[c]
	}
	ctx := context.Background()
	postgres := e.engine == "postgres"
	var db *sql.DB
	var err error
	if postgres {
		db, err = sql.Open("pgx", consentAppDSN(e.t, e))
	} else {
		db, err = sql.Open("sqlite", "file:"+filepath.Join(e.eng.dataDir, "olivares.db")+"?_pragma=busy_timeout(10000)")
	}
	if err != nil {
		e.t.Fatalf("open the store directly: %v", err)
	}
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	placeholders := make([]string, len(cols))
	for i := range cols {
		if postgres {
			placeholders[i] = fmt.Sprintf("$%d", i+1)
		} else {
			placeholders[i] = "?"
		}
	}
	if postgres {
		if _, err := tx.ExecContext(ctx, "SELECT pg_catalog.set_config('app.tenant_id', $1, true)", tenant.String()); err != nil {
			e.t.Fatalf("bind the tenant: %v", err)
		}
	} else {
		if _, err := tx.ExecContext(ctx, "DELETE FROM _scope_tenant"); err != nil {
			e.t.Fatalf("clear the pin: %v", err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO _scope_tenant(tenant_id) VALUES(?)", tenant.String()); err != nil {
			e.t.Fatalf("pin the tenant: %v", err)
		}
	}
	q := "INSERT INTO " + desc.Table + " (" + strings.Join(cols, ", ") + ") VALUES (" + strings.Join(placeholders, ", ") + ")"
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		e.t.Fatalf("restore a %s row: %v", kind, err)
	}
	if err := tx.Commit(); err != nil {
		e.t.Fatalf("commit the restored row: %v", err)
	}
	return id
}

// TestAnUnknownStoredKindBlocksRetirement: a stored row whose kind no registry
// knows, however it got there, keeps the retirement from completing.
func TestAnUnknownStoredKindBlocksRetirement(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		subject := e.retirementSubjectIn(e.tT, "unknown-kind")
		steps := strings.Replace(workCreateSteps(t, subject.id), `"work-create"`, `"work-escalate"`, 1)
		e.restoreRow(e.tT, "orchestration.workflow", model.Record{
			"name": "restored-escalate", "enabled": true, "steps": steps, "owner_actor": "restore", "owner_actor_kind": "system",
		})
		e.restoreRow(e.tT, "core.policy", model.Record{
			"name": "restored-guardrail", "kind": "guardrail", "spec": `{"user":"` + subject.id.String() + `"}`, "enabled": true,
		})
		e.restoreRow(e.tT, "governance.policy_revision", model.Record{
			"surface": "future-surface", "revision": int64(1), "content": subject.email, "author": "restore",
			"validated": true, "active": true,
		})
		e.scimDelete(e.tT, subject.id)
		e.runPump()
		e.wantBlocked(t, subject.id, e.tT, "unknown_kind")
		rec, _ := e.record(subject.id, e.tT)
		for _, raw := range []string{"work-escalate", "guardrail", "future-surface"} {
			if strings.Contains(rec.BlockingRefs, raw) || strings.Contains(rec.ModuleResults, raw) {
				t.Errorf("the tenant-visible record carries the raw unknown value %q", raw)
			}
		}
		if r := e.readmit(e.tT, subject.email, "viewer"); r.code != http.StatusConflict {
			t.Errorf("re-admission while an unknown stored kind remains = %d %s, want 409", r.code, r.raw)
		}
	})
}

// TestAStoredActivationNoWriterDerivesIsUnknown: only the policy editor's
// engines are activated. A stored activation of the managed projection or of
// the adopted snapshot, however it got there, selects nothing: a publish that
// would build on the selection it names is refused, and the retirement treats it
// as a row of a kind no registry knows. Each surface has a tenant of its own, so
// neither case sees the other's row.
func TestAStoredActivationNoWriterDerivesIsUnknown(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		for _, surface := range []string{"cedar-managed", "cedar-ddil"} {
			t.Run(surface, func(t *testing.T) {
				e := e.forSubtest(t)
				tenant := e.createOrg("activation-" + surface)
				subject := e.retirementSubjectIn(tenant, "activation-"+surface)
				e.restoreRow(tenant, "governance.policy_revision", model.Record{
					"surface": surface, "revision": int64(1),
					"content": `permit(principal == User::"` + subject.id.String() + `", action, resource);`,
					"author":  "restore", "validated": true, "active": false,
				})
				e.restoreRow(tenant, "governance.policy_revision", model.Record{
					"surface": surface + "-activation", "revision": int64(1), "content": "1",
					"author": "restore", "validated": true, "active": true,
				})
				if r := e.do("POST", "/v1/m/governance/pdp/publish", e.admin, tenant, map[string]any{
					"engine": "cedar", "source": `permit(principal in Role::"viewer", action == Action::"agent:read", resource);`,
				}); r.code == http.StatusOK {
					t.Errorf("a publish over the selection a stored %s activation names = 200, want it refused", surface)
				}
				e.scimDelete(tenant, subject.id)
				e.runPump()
				e.wantBlocked(t, subject.id, tenant, "unknown_kind")
			})
		}
	})
}

// TestATokenOwnedSubscriptionBlocksReadmissionUntilResolved: a subscription the
// account created through the product route with one of its tokens is owned by
// the token, and blocks the account's retirement like one the account owns
// itself, until the tenant deletes it.
func TestATokenOwnedSubscriptionBlocksReadmissionUntilResolved(t *testing.T) {
	t.Setenv(eventingAllowLoopbackEnv, "1")
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer collector.Close()
	t.Setenv(envEventingEgressPolicy, writeLoopbackEgressPolicy(t, collector.URL))
	for _, engineName := range consentEngines {
		t.Run(engineName, func(t *testing.T) {
			e := bootConsentEstate(t, engineName)
			const email = "token-owner@consent.test"
			user := e.onboard(e.tT, email, "admin")
			sess := e.login(email, consentMemberPassword)
			issued := e.do("POST", "/v1/tokens", sess, e.tT, map[string]any{
				"name": "automation", "tenant": e.tT.String(), "role": "admin",
			})
			token, _ := issued.body["token"].(string)
			if issued.code != http.StatusCreated || token == "" {
				t.Fatalf("issue the account's token = %d %s", issued.code, issued.raw)
			}
			created := e.do("POST", "/v1/m/eventing/subscriptions", token, e.tT, map[string]any{
				"name": "token-owned", "endpoint": collector.URL,
				"event_types": []string{"finding.reported"}, "role": "viewer",
			})
			subID, _ := created.body["id"].(string)
			if created.code != http.StatusCreated || subID == "" {
				t.Fatalf("create the subscription with the token = %d %s", created.code, created.raw)
			}
			if owner := e.rowColumn(e.tT, "eventing.subscription", model.ID(subID), "owner_actor"); !strings.HasPrefix(owner, "token:") {
				t.Fatalf("the subscription's owner = %q, want the token that created it", owner)
			}

			e.scimDelete(e.tT, user)
			e.runPump()
			e.wantBlocked(t, user, e.tT, "eventing.subscription:"+subID)
			if r := e.readmit(e.tT, email, "viewer"); r.code != http.StatusConflict {
				t.Errorf("re-admission while the account's token owns a subscription = %d %s, want 409", r.code, r.raw)
			}
			if r := e.do("DELETE", "/v1/m/eventing/subscriptions/"+subID, e.admin, e.tT, nil); r.code >= 300 {
				t.Fatalf("delete the subscription = %d %s", r.code, r.raw)
			}
			e.runPump()
			e.wantRetired(t, user, e.tT)
		})
	}
}

// wantNamesOnlyThrough fails unless the row id of kind names user through
// column's counted declaration and through no other counted column of the
// table, so the case sees only what the step reads of that column.
func (e *consentEstate) wantNamesOnlyThrough(t *testing.T, kind model.Kind, column string, id, user model.ID) {
	t.Helper()
	e.wantNames(t, kind, column, id, user)
	account := e.aliasesOf(t, user)
	ctx := context.Background()
	if err := e.eng.store.View(ctx, e.tT, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, id)
		if err != nil {
			return err
		}
		for _, f := range repo.Descriptor().Fields {
			if f.Name == column || f.Principal == nil || !f.Principal.Counted() {
				continue
			}
			named, err := namesThrough(f.Principal, rec, f.Name, account)
			if err != nil {
				return err
			}
			if named {
				t.Errorf("the %s row also names the account through %s, so the %s case cannot see %s alone", kind, f.Name, kind, column)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("read the %s row: %v", kind, err)
	}
}
