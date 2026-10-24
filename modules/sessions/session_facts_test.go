// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// factsSource is a SessionFactsSource with fixed answers.
type factsSource struct {
	name     string
	approved int64
	budget   SessionBudgetLimit
}

func (f factsSource) UserNames(_ context.Context, _ model.TenantID, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		out[id] = f.name
	}
	return out, nil
}

func (f factsSource) ApprovedActions(_ context.Context, _ model.TenantID, sids []string) (map[string]int64, error) {
	out := map[string]int64{}
	for _, sid := range sids {
		out[sid] = f.approved
	}
	return out, nil
}

func (f factsSource) BudgetLimits(_ context.Context, _ model.TenantID, subjects []SessionBudgetSubject) (map[string]SessionBudgetLimit, error) {
	out := map[string]SessionBudgetLimit{}
	if f.budget.LimitMicroUSD > 0 {
		for _, s := range subjects {
			out[s.Key] = f.budget
		}
	}
	return out, nil
}

func factsHTTP(t *testing.T, src SessionFactsSource) (*harness, string, model.TenantID, string, *testClock) {
	t.Helper()
	clk := &testClock{now: baseTime}
	opts := []Option{WithSessionWorkspaceRoot(t.TempDir()), WithRunner(&fakeRunner{initSID: dupID}), WithCredentialSource(staticCred()), WithClock(clk)}
	if src != nil {
		opts = append(opts, WithSessionFacts(src))
	}
	m := New(opts...)
	m.UseExecutionEnvironmentRef(testEnvRef)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	cfg, usr, _, _ := twoHomes(t)
	r := h.doJSON("POST", "/v1/m/sessions/provider-profiles", admin, map[string]any{"driver": "claude", "config_home": cfg, "user_home": usr, "display_name": "team-a"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("profile = %d %s", r.code, r.raw)
	}
	return h, admin, tenant, r.body["profile_ref"].(string), clk
}

// runLedgerCount counts the audit ledger entries that record the run, straight from
// the ledger and not from the read under test: an entry targets the run's row, names its
// run or session in its stored meta, or was written by the session's identity (a hook
// decision, claudehookpep.go anchorDecision, carries all three of the last).
func runLedgerCount(t *testing.T, h *harness, tenant model.TenantID, runRef string) int64 {
	t.Helper()
	var n int64
	err := h.st.View(t.Context(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := findRunRec(t.Context(), repo, runRef)
		if err != nil {
			return err
		}
		sid := rec.String(colRunClaimSID)
		return sc.Audit().(store.CanonicalWalker).WalkCanonical(t.Context(), 1, func(ev model.AuditEvent, canonical string, _ []byte) error {
			var meta map[string]any
			if err := json.Unmarshal([]byte(canonical), &meta); err != nil {
				return err
			}
			switch {
			case ev.TargetKind == runKind && ev.TargetID.String() == rec.String(model.ColID),
				meta["run_ref"] == runRef,
				sid != "" && (meta["session_ref"] == sid || ev.Actor == "session:"+sid):
				n++
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// appendHookDecision writes the ledger row the hook PEP writes for a session-scoped
// tool decision (claudehookpep.go anchorDecision: the session's actor, the tool target
// and the session and run in meta). The PEP package imports this one, so the row is
// written through the same ledger Append rather than by the PEP itself.
func appendHookDecision(t *testing.T, h *harness, tenant model.TenantID, sid, runRef string) {
	t.Helper()
	err := h.st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
		_, err := sc.Audit().Append(t.Context(), model.AuditDraft{
			Actor: "session:" + sid, ActorKind: model.ActorUser, Action: "hook.tool.allow",
			TargetKind: "claude.tool.use", TargetID: model.NewID(),
			Meta: map[string]any{"event": "PreToolUse", "tool": "Bash", "decision": "allow", "session_ref": sid, "run_ref": runRef},
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func factsOf(t *testing.T, h *harness, admin string, tenant model.TenantID, runRef string) (detail, listed map[string]any) {
	t.Helper()
	d := h.do(http.MethodGet, "/v1/m/sessions/runs/"+runRef, admin, tenantHdr(tenant))
	if d.code != http.StatusOK {
		t.Fatalf("detail = %d %s", d.code, d.raw)
	}
	rows := itemsOf(t, h.do(http.MethodGet, "/v1/m/sessions/runs", admin, tenantHdr(tenant)))
	if len(rows) != 1 {
		t.Fatalf("runs = %d, want 1", len(rows))
	}
	return d.body, rows[0]
}

func TestRunFactsWaitingApprovalAndLedgerCount(t *testing.T) {
	h, admin, tenant, profile, clk := factsHTTP(t, factsSource{name: "Lena Fischer", approved: 2})
	run, _ := launchProfiledHTTP(t, h, admin, tenant, profile)

	a := auth.NewAuthenticator(h.st, nil)
	human, err := a.Authenticate(t.Context(), admin)
	if err != nil {
		t.Fatal(err)
	}
	issuer := auth.NewSessionCredentials(a, func(ctx context.Context, scope auth.SessionScope) error {
		return h.m.Authority(ctx, scope.TenantID, scope.SessionRef, scope.Holder, scope.Fence)
	})
	lr, ok := h.m.rt.getLive(tenant, run)
	if !ok {
		t.Fatal("launched run is not supervised")
	}
	var workspace model.ID
	if err := h.st.View(t.Context(), tenant, func(sc store.Scope) error {
		ws, err := sc.DefaultWorkspace(t.Context())
		workspace = ws.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	token, err := issuer.Mint(t.Context(), human, auth.SessionScope{TenantID: tenant, WorkspaceID: workspace, FolderRef: run, SessionRef: lr.claim.SID, RunRef: run, Holder: lr.claim.Holder, Fence: lr.claim.Fence})
	if err != nil {
		t.Fatal(err)
	}
	session, err := issuer.Authenticate(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}

	detail, listed := factsOf(t, h, admin, tenant, run)
	for name, dto := range map[string]map[string]any{"detail": detail, "list": listed} {
		approvals, _ := dto["approvals"].(map[string]any)
		if approvals["waiting"] != float64(0) || approvals["approved"] != float64(2) {
			t.Fatalf("%s approvals before a wait = %v, want 0 waiting and 2 approved", name, approvals)
		}
	}

	end, err := h.m.BeginApprovalWait(t.Context(), session, model.NewID().String(), clk.get().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	defer end()
	targeted := runLedgerCount(t, h, tenant, run)
	appendHookDecision(t, h, tenant, lr.claim.SID, run)
	want := float64(runLedgerCount(t, h, tenant, run))
	if targeted < 1 || want != float64(targeted+1) {
		t.Fatalf("the ledger holds %d entries for the session before the hook decision and %v after, want at least 1 and one more", targeted, want)
	}
	detail, listed = factsOf(t, h, admin, tenant, run)
	for name, dto := range map[string]map[string]any{"detail": detail, "list": listed} {
		approvals, _ := dto["approvals"].(map[string]any)
		if approvals["waiting"] != float64(1) {
			t.Fatalf("%s waiting = %v, want 1", name, approvals["waiting"])
		}
		proof, _ := dto["proof"].(map[string]any)
		if proof["ledger_entries"] != want {
			t.Fatalf("%s ledger_entries = %v, want %v from the audit ledger", name, proof["ledger_entries"], want)
		}
		if proof["chain_verified"] != true {
			t.Fatalf("%s chain_verified = %v, want true", name, proof["chain_verified"])
		}
	}

	// A second entry in the ledger for the session moves the count with it.
	if r := h.do(http.MethodPost, "/v1/m/sessions/runs/"+run+"/stop", admin, tenantHdr(tenant)); r.code >= 300 {
		t.Fatalf("stop = %d %s", r.code, r.raw)
	}
	waitFor(t, "stopped session ledger", func() bool {
		return float64(runLedgerCount(t, h, tenant, run)) > want
	})
	detail, _ = factsOf(t, h, admin, tenant, run)
	proof, _ := detail["proof"].(map[string]any)
	if got, now := proof["ledger_entries"], float64(runLedgerCount(t, h, tenant, run)); got != now {
		t.Fatalf("ledger_entries after stop = %v, want %v", got, now)
	}
}

func TestRunFactsWhoRunsAsReachesAndCost(t *testing.T) {
	h, admin, tenant, profile, _ := factsHTTP(t, factsSource{name: "Lena Fischer"})
	run, _ := launchProfiledHTTP(t, h, admin, tenant, profile)
	detail, listed := factsOf(t, h, admin, tenant, run)
	for name, dto := range map[string]map[string]any{"detail": detail, "list": listed} {
		actsFor, _ := dto["acts_for"].(map[string]any)
		if actsFor["kind"] != "user" || actsFor["name"] != "Lena Fischer" || actsFor["user_id"] == "" {
			t.Fatalf("%s acts_for = %v", name, actsFor)
		}
		runsAs, _ := dto["runs_as"].(map[string]any)
		if runsAs["tool"] != "claude" || runsAs["account"] != "team-a" || runsAs["label"] != runsAs["tool_name"].(string)+" · team-a" {
			t.Fatalf("%s runs_as = %v", name, runsAs)
		}
		reaches, _ := dto["reaches"].(map[string]any)
		folders, _ := reaches["folders"].([]any)
		if len(folders) != 1 || folders[0].(map[string]any)["access"] != "write" || folders[0].(map[string]any)["path"] == "" {
			t.Fatalf("%s reaches.folders = %v, want the session folder with write", name, folders)
		}
		if hosts, ok := reaches["hosts"].([]any); !ok || len(hosts) != 0 {
			t.Fatalf("%s reaches.hosts = %v, want an empty list (the profile declares none)", name, reaches["hosts"])
		}
		// The tool reported no usage: the cost is null with its reason, never 0.
		cost, _ := dto["cost"].(map[string]any)
		if v, present := cost["micro_usd"]; !present || v != nil || cost["reason"] != factReasonNoCost {
			t.Fatalf("%s cost = %v, want micro_usd null with %q", name, cost, factReasonNoCost)
		}
		if _, ok := dto["cost_micro_usd"]; ok {
			t.Fatalf("%s grew a published cost_micro_usd for a session with no usage", name)
		}
	}
}

// A budget that applies adds its limit beside the cost; the cost stays null when the
// tool reported nothing, so a limit never reads as a price.
func TestRunFactsCarryTheBudgetLimitBesideTheCost(t *testing.T) {
	h, admin, tenant, profile, _ := factsHTTP(t, factsSource{name: "Lena Fischer", budget: SessionBudgetLimit{LimitMicroUSD: 25_000_000, Period: "total"}})
	run, _ := launchProfiledHTTP(t, h, admin, tenant, profile)
	detail, listed := factsOf(t, h, admin, tenant, run)
	for name, dto := range map[string]map[string]any{"detail": detail, "list": listed} {
		cost, _ := dto["cost"].(map[string]any)
		if cost["limit_micro_usd"] != float64(25_000_000) || cost["limit_period"] != "total" || cost["micro_usd"] != nil {
			t.Fatalf("%s cost = %v, want the limit with a null price", name, cost)
		}
	}
	h, admin, tenant, profile, _ = factsHTTP(t, factsSource{name: "Lena Fischer"})
	run, _ = launchProfiledHTTP(t, h, admin, tenant, profile)
	detail, _ = factsOf(t, h, admin, tenant, run)
	if cost, _ := detail["cost"].(map[string]any); cost["limit_micro_usd"] != nil {
		t.Fatalf("a session no budget scopes reports a limit: %v", cost)
	}
}

func TestRunFactsWithoutASourceSayWhy(t *testing.T) {
	h, admin, tenant, profile, _ := factsHTTP(t, nil)
	run, _ := launchProfiledHTTP(t, h, admin, tenant, profile)
	detail, _ := factsOf(t, h, admin, tenant, run)
	actsFor, _ := detail["acts_for"].(map[string]any)
	if actsFor["kind"] != "user" || actsFor["name"] != nil || actsFor["reason"] != factReasonNoFacts {
		t.Fatalf("acts_for without a source = %v", actsFor)
	}
	approvals, _ := detail["approvals"].(map[string]any)
	if v, present := approvals["approved"]; !present || v != nil || approvals["reason"] != factReasonNoFacts {
		t.Fatalf("approvals without a source = %v, want approved null with a reason", approvals)
	}
}

// The facts are additive: every field the run read published before them is still there.
func TestRunFactsKeepPublishedFields(t *testing.T) {
	h, admin, tenant, profile, _ := factsHTTP(t, factsSource{name: "Lena Fischer"})
	run, _ := launchProfiledHTTP(t, h, admin, tenant, profile)
	detail, listed := factsOf(t, h, admin, tenant, run)
	published := []string{
		"run_ref", "peers", "transport", "permission_mode", "isolation", "state", "process_state",
		"last_event_seq", "created_at", "pep_provisioned", "record_io", "critical", "provider_profile_ref",
		"provider_driver", "provider_conversation_id", "claude_session_id", "live_ref", "workspace_path",
		"canonical_sid", "core_session_id", "pinned",
	}
	facts := []string{"acts_for", "runs_as", "reaches", "cost", "approvals", "proof"}
	for name, dto := range map[string]map[string]any{"detail": detail, "list": listed} {
		for _, key := range slices.Concat(published, facts) {
			if _, ok := dto[key]; !ok {
				t.Errorf("%s lacks %q", name, key)
			}
		}
	}
	if detail["run_ref"] != run || listed["run_ref"] != run {
		t.Fatalf("run_ref changed: %v / %v", detail["run_ref"], listed["run_ref"])
	}
}

// storeFactsSource reads the store for every answer, as the engine's source does (core
// accounts, governance approvals, FinOps budgets each open their own transaction).
type storeFactsSource struct {
	st store.Store
}

func (s *storeFactsSource) read(ctx context.Context, tenant model.TenantID) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return s.st.View(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.DefaultWorkspace(ctx)
		return err
	})
}

func (s *storeFactsSource) UserNames(ctx context.Context, tenant model.TenantID, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		out[id] = "Lena Fischer"
	}
	return out, s.read(ctx, tenant)
}

func (s *storeFactsSource) ApprovedActions(ctx context.Context, tenant model.TenantID, _ []string) (map[string]int64, error) {
	return map[string]int64{}, s.read(ctx, tenant)
}

func (s *storeFactsSource) BudgetLimits(ctx context.Context, tenant model.TenantID, _ []SessionBudgetSubject) (map[string]SessionBudgetLimit, error) {
	return map[string]SessionBudgetLimit{}, s.read(ctx, tenant)
}

// The facts from outside the module are read after the run read's own transaction, so
// a source that opens a transaction of its own never waits on the read that asks it
// (on SQLite the store has one connection, and the wait never ended).
func TestRunFactsReadTheOutsideSourcesOutsideTheReadTransaction(t *testing.T) {
	src := &storeFactsSource{}
	h, admin, tenant, profile, _ := factsHTTP(t, src)
	src.st = h.st
	run, _ := launchProfiledHTTP(t, h, admin, tenant, profile)
	detail, listed := factsOf(t, h, admin, tenant, run)
	for name, dto := range map[string]map[string]any{"detail": detail, "list": listed} {
		actsFor, _ := dto["acts_for"].(map[string]any)
		approvals, _ := dto["approvals"].(map[string]any)
		cost, _ := dto["cost"].(map[string]any)
		if actsFor["name"] != "Lena Fischer" || approvals["approved"] != float64(0) || cost["reason"] != factReasonNoCost {
			t.Fatalf("%s facts read through the store = acts_for %v, approvals %v, cost %v", name, actsFor, approvals, cost)
		}
	}
}

// perRunFacts answers each user and session with values of its own, so a fact that
// landed on another row of the page shows.
type perRunFacts struct{ approved map[string]int64 }

func (perRunFacts) UserNames(_ context.Context, _ model.TenantID, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		out[id] = "name of " + id
	}
	return out, nil
}

func (f perRunFacts) ApprovedActions(_ context.Context, _ model.TenantID, sids []string) (map[string]int64, error) {
	out := map[string]int64{}
	for _, sid := range sids {
		if n, ok := f.approved[sid]; ok {
			out[sid] = n
		}
	}
	return out, nil
}

func (perRunFacts) BudgetLimits(context.Context, model.TenantID, []SessionBudgetSubject) (map[string]SessionBudgetLimit, error) {
	return map[string]SessionBudgetLimit{}, nil
}

// Each run of a list carries its own facts: who it acts for, its cost (a priced one, one
// with tokens and no price, one with nothing reported), its approvals and its ledger
// entries.
func TestRunFactsListGivesEachRunItsOwnFacts(t *testing.T) {
	h, admin, tenant, _, _ := factsHTTP(t, perRunFacts{approved: map[string]int64{"sid-a": 2, "sid-b": 5}})
	rows := map[string]model.Record{
		"run-a": {colRunLauncherUserID: "u-a", colRunClaimSID: "sid-a", colRunCostMicroUSD: int64(1_420_000)},
		"run-b": {colRunLauncherUserID: "u-b", colRunClaimSID: "sid-b", colRunInputTokens: int64(10)},
		"run-c": {colRunAgentRef: "agent-c"},
	}
	ids := map[string]string{}
	if err := h.st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		for ref, rec := range rows {
			rec[colRunRef], rec[colState], rec[colLastEventSeq] = ref, stateStopped, int64(0)
			rec[colTransport], rec[colPermissionMode], rec[colIsolation] = string(TransportStreamJSON), "default", string(IsolationNative)
			created, err := repo.Create(t.Context(), rec)
			if err != nil {
				return err
			}
			ids[ref] = created.String(model.ColID)
		}
		for _, d := range []model.AuditDraft{
			{Actor: "user:u-a", ActorKind: model.ActorUser, Action: "test.event", TargetKind: runKind, TargetID: model.ID(ids["run-a"])},
			{Actor: "user:u-a", ActorKind: model.ActorUser, Action: "test.event", TargetKind: runKind, TargetID: model.ID(ids["run-a"])},
			{Actor: "session:sid-b", ActorKind: model.ActorUser, Action: "hook.tool.allow", TargetKind: "claude.tool.use", TargetID: model.NewID(), Meta: map[string]any{"session_ref": "sid-b"}},
		} {
			if _, err := sc.Audit().Append(t.Context(), d); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	listed := runItemsOf(t, h.do(http.MethodGet, "/v1/m/sessions/runs", admin, tenantHdr(tenant)))
	want := map[string]struct {
		kind, user string
		cost       any
		costReason string
		approved   any
		entries    float64
	}{
		"run-a": {"user", "u-a", float64(1_420_000), "", float64(2), 2},
		"run-b": {"user", "u-b", nil, factReasonNoPrice, float64(5), 1},
		"run-c": {"automation", "", nil, factReasonNoCost, float64(0), 0},
	}
	for ref, w := range want {
		dto, ok := listed[ref]
		if !ok {
			t.Fatalf("%s missing from the list: %v", ref, listed)
		}
		actsFor, _ := dto["acts_for"].(map[string]any)
		cost, _ := dto["cost"].(map[string]any)
		approvals, _ := dto["approvals"].(map[string]any)
		proof, _ := dto["proof"].(map[string]any)
		wantUser, wantName := any(nil), any(nil)
		if w.user != "" {
			wantUser, wantName = w.user, "name of "+w.user
		}
		if actsFor["kind"] != w.kind || actsFor["user_id"] != wantUser || actsFor["name"] != wantName {
			t.Errorf("%s acts_for = %v, want %s %q", ref, actsFor, w.kind, w.user)
		}
		wantReason := any(nil)
		if w.costReason != "" {
			wantReason = w.costReason
		}
		if cost["micro_usd"] != w.cost || cost["reason"] != wantReason {
			t.Errorf("%s cost = %v, want %v %q", ref, cost, w.cost, w.costReason)
		}
		if approvals["approved"] != w.approved {
			t.Errorf("%s approvals = %v, want %v approved", ref, approvals, w.approved)
		}
		if proof["ledger_entries"] != w.entries {
			t.Errorf("%s proof = %v, want %v ledger entries", ref, proof, w.entries)
		}
	}
	detail := h.do(http.MethodGet, "/v1/m/sessions/runs/run-a", admin, tenantHdr(tenant))
	if cost, _ := detail.body["cost"].(map[string]any); detail.code != http.StatusOK || cost["micro_usd"] != float64(1_420_000) {
		t.Fatalf("detail of run-a = %d cost %v, want the priced 1420000", detail.code, detail.body["cost"])
	}
}
