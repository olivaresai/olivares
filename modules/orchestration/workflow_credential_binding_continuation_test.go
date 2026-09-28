// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// setMemberRole moves the initiator's membership in the tenant to role and
// signs the initiator in again, since a role change may retire its sessions.
func (f *bindingRunFixture) setMemberRole(role string) {
	f.t.Helper()
	user := f.principal(f.member).UserID
	ctx := context.Background()
	if err := f.h.st.AuthMutate(ctx, func(as store.AuthScope) error {
		rows, _, err := as.Memberships().List(ctx, model.Query{Filters: []model.Filter{
			{Column: "user_id", Op: model.OpEq, Value: user.String()},
			{Column: "target_tenant_id", Op: model.OpEq, Value: f.tenant.String()},
		}, Limit: 2})
		if err != nil || len(rows) != 1 {
			f.t.Fatalf("membership rows = %d, %v", len(rows), err)
		}
		rows[0].Role = role
		_, err = as.Memberships().Update(ctx, rows[0])
		return err
	}); err != nil {
		f.t.Fatalf("set member role: %v", err)
	}
	r := f.h.do("POST", "/v1/auth/login", "", map[string]any{"email": f.email, "password": "memberpass1"}, nil)
	if r.code != http.StatusOK {
		f.t.Fatalf("login = %d %s", r.code, r.raw)
	}
	f.member = r.body["token"].(string)
}

// F6 and the pre-W0 half of F3: a reauthorization that cannot continue the run
// — another account's credential, or the initiator's own credential wider than
// the run's recorded ceiling — is refused before W0. The run keeps its version,
// its gate and its handle, and no successor row exists for the next generation.
func TestReauthorizationRefusesAnIncompatibleContinuationBeforeReserving(t *testing.T) {
	f := newBindingRunFixture(t)
	wfID, run, _ := f.startPausedAfterRefresh()
	f.requireGated(run, "setup")
	rec, _ := f.row(run)
	plan, version, original := rec.String(colWrPlanHash), rec.Int(model.ColVersion), f.handle(run)

	untouched := func(what string) {
		t.Helper()
		f.requireGated(run, what)
		rec, _ := f.row(run)
		if got := rec.Int(model.ColVersion); got != version {
			t.Fatalf("%s: run version %d, want %d: the refusal wrote the run", what, got, version)
		}
		if f.handle(run) != original || f.liveBindings(run) != 1 {
			t.Fatalf("%s: the refusal changed the run's binding", what)
		}
	}

	other := f.h.roleToken(f.root, f.tenant, "other-"+model.NewID().String()[:8]+"@binding.test", auth.RoleAdmin)
	if r := f.reauthorize(other, wfID, run, plan); r.code != http.StatusForbidden ||
		!strings.Contains(r.raw, "credential of the run's initiator") {
		t.Fatalf("another account = %d %s, want 403", r.code, r.raw)
	}
	untouched("another account")

	f.setMemberRole(auth.RoleOwner)
	if r := f.reauthorize(f.member, wfID, run, plan); r.code != http.StatusForbidden ||
		!strings.Contains(r.raw, "exceeds the authority the run was started with") {
		t.Fatalf("initiator wider than the run's ceiling = %d %s, want 403", r.code, r.raw)
	}
	untouched("wider credential")

	f.setMemberRole(auth.RoleAdmin)
	if r := f.reauthorize(f.member, wfID, run, plan); r.code != http.StatusOK {
		t.Fatalf("compatible continuation = %d %s, want 200", r.code, r.raw)
	}
}

// countingBinder counts the bindings a run start writes.
type countingBinder struct {
	WorkflowCredentialBinder
	binds atomic.Int32
}

func (b *countingBinder) BindCredential(
	ctx context.Context,
	p auth.Principal,
	s auth.CredentialBindingSubject,
) (auth.CredentialBinding, error) {
	b.binds.Add(1)
	return b.WorkflowCredentialBinder.BindCredential(ctx, p, s)
}

// F7: a start whose approval already started a run is recognized as a replay
// before anything is bound, so it writes no binding row for a run that will
// never exist.
func TestRecognizedStartReplayBindsNothing(t *testing.T) {
	f := newBindingRunFixture(t)
	counter := &countingBinder{WorkflowCredentialBinder: f.authr}
	f.mod.UseWorkflowCredentialBinder(counter)
	wf := f.h.createWorkflow(f.member, f.tenant, "wf-"+model.NewID().String(), []map[string]any{emitStep("emit")})
	started := f.h.runToPhase2(f.gate, f.member, f.tenant, wf["id"].(string))
	if got := counter.binds.Load(); got != 1 {
		t.Fatalf("binds after the start = %d, want 1", got)
	}
	replay := f.h.do("POST", "/v1/m/orchestration/workflows/"+wf["id"].(string)+"/run", f.member,
		map[string]any{"approval_ref": started.body["approval_ref"]}, tenantHdr(f.tenant))
	if replay.code != http.StatusConflict {
		t.Fatalf("start replay = %d %s, want 409", replay.code, replay.raw)
	}
	if got := counter.binds.Load(); got != 1 {
		t.Fatalf("binds after the replay = %d, want 1: a recognized replay wrote a binding", got)
	}
}

// R2 at the route: a run that was never bound — started by a binary without
// credential bindings (no binder wired, as before v15) — has no authoritative
// record of the authority it started with. Its initiator's own session cannot
// continue it: reauthorization is refused before W0, the run keeps its version
// and its gate, and no binding is written. A fresh run bound at its start
// stays reauthorizable.
func TestNeverBoundRunIsNotReauthorized(t *testing.T) {
	f := newBindingRunFixture(t)
	f.mod.UseWorkflowCredentialBinder(nil)
	wfID, run, _ := f.start(f.member, f.messageStep("msg"))
	f.mod.UseWorkflowCredentialBinder(f.authr)
	f.requireGated(run, "never-bound run at its first effect")
	rec, _ := f.row(run)
	plan, version := rec.String(colWrPlanHash), rec.Int(model.ColVersion)
	if !f.handle(run).IsZero() || f.liveBindings(run) != 0 {
		t.Fatal("setup: the run was bound at its start")
	}
	if r := f.reauthorize(f.member, wfID, run, plan); r.code != http.StatusForbidden ||
		!strings.Contains(r.raw, "cannot be proved") {
		t.Fatalf("initiator's session on a never-bound run = %d %s, want 403 (original authority unproven)", r.code, r.raw)
	}
	f.requireGated(run, "after the refusal")
	rec, _ = f.row(run)
	if got := rec.Int(model.ColVersion); got != version || !f.handle(run).IsZero() || f.liveBindings(run) != 0 {
		t.Fatalf("the refusal wrote the run (version %d, want %d) or a binding", got, version)
	}

	// A fresh run under a properly bound credential is still reauthorized.
	wfID, run, _ = f.startPausedAfterRefresh()
	f.requireGated(run, "fresh bound run")
	rec, _ = f.row(run)
	if r := f.reauthorize(f.member, wfID, run, rec.String(colWrPlanHash)); r.code != http.StatusOK {
		t.Fatalf("fresh bound run = %d %s, want 200", r.code, r.raw)
	}
}
