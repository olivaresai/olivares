// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func budgetDepartments(t *testing.T, st store.Store, tenant model.TenantID) map[string]model.Workspace {
	t.Helper()
	ctx := context.Background()
	rows := map[string]model.Workspace{}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		for _, node := range []struct{ slug, parent string }{
			{"eng", ""}, {"platform", "eng"}, {"sre", "platform"}, {"apps", "eng"}, {"sales", ""},
		} {
			ws, err := sc.Workspaces().Create(ctx, model.Workspace{
				Name: node.slug, Slug: node.slug, Status: model.StatusActive, ParentID: rows[node.parent].ID,
			})
			if err != nil {
				return err
			}
			rows[node.slug] = ws
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestDepartmentBudgetExhaustedParentRefusesDescendants(t *testing.T) {
	forEachAdmissionEngine(t, runDepartmentBudgetExhaustedParentRefusesDescendants)
}

func runDepartmentBudgetExhaustedParentRefusesDescendants(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	budgetDepartments(t, st, tenant)
	id := createBudget(t, st, tenant, "engineering", budgetSpec{
		Dimension: "workspace", Key: "eng", Period: "total", LimitMicroUSD: oneUSD,
		ReservedMicroUSD: oneUSD, Action: "block",
	})
	for _, ws := range []string{"eng", "platform", "sre", "apps", "sales", "external-provider-workspace"} {
		t.Run(ws, func(t *testing.T) {
			dims := SpendDims{WorkspaceRef: ws}
			wantAllowed := ws == "sales" || ws == "external-provider-workspace"
			check, err := m.CheckBudget(context.Background(), tenant, dims)
			if err != nil || check.Allowed != wantAllowed || (!wantAllowed && check.BudgetID != id.String()) {
				t.Errorf("CheckBudget = %+v, err=%v, want allowed=%v", check, err, wantAllowed)
			}
			reserve, err := m.ReserveBudget(context.Background(), tenant, dims, oneUSD)
			if err != nil || reserve.Allowed != wantAllowed || (!wantAllowed && reserve.BudgetID != id.String()) {
				t.Errorf("ReserveBudget = %+v, err=%v, want allowed=%v", reserve, err, wantAllowed)
			}
			admit, err := m.Reserve(context.Background(), tenant, AdmissionRequest{
				Scope: AdmissionScopeModelGateway, Dims: dims, EstimateMicroUSD: oneUSD, IdempotencyKey: ws,
			})
			if err != nil || admit.Allowed != wantAllowed || (!wantAllowed && admit.BudgetID != id.String()) {
				t.Errorf("Reserve = %+v, err=%v, want allowed=%v", admit, err, wantAllowed)
			}
		})
	}
}

func TestDepartmentBudgetCountsSubtreeSpendAndAlerts(t *testing.T) {
	forEachAdmissionEngine(t, runDepartmentBudgetCountsSubtreeSpendAndAlerts)
}

func runDepartmentBudgetCountsSubtreeSpendAndAlerts(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	budgetDepartments(t, st, tenant)
	m.clock = &fakeClock{t: baseTime}
	id := createBudget(t, st, tenant, "engineering", budgetSpec{
		Dimension: "workspace", Key: "eng", Period: "total", LimitMicroUSD: 1000, Thresholds: []float64{1}, Action: "block",
	})
	for i, sample := range []struct {
		ws     string
		amount int64
	}{
		{"sales", 9000}, {"eng", 100}, {"platform", 200}, {"sre", 700},
	} {
		c := mkCost("local", "test", "", 1, 1, sample.amount, baseTime.Add(time.Duration(i)*time.Second))
		c.WorkspaceRef = sample.ws
		m.ingest(t, tenant, c)
	}
	if got := len(listAlerts(t, st, tenant)); got != 1 {
		t.Errorf("alerts = %d, want one crossing from subtree spend", got)
	}
	for _, row := range alertRows(t, st, tenant) {
		if evidence := interpretAlertEvidence(row, tenant); evidence.State != evidenceValid {
			t.Errorf("subtree alert evidence is invalid: %+v", evidence)
		} else if !reflect.DeepEqual(evidence.Envelope.Context.ScopeValues, []string{"apps", "eng", "platform", "sre"}) {
			t.Errorf("captured scope = %v", evidence.Envelope.Context.ScopeValues)
		}
		for name, change := range map[string]func(*alertEvidenceEnvelope){
			"anchor absent":   func(e *alertEvidenceEnvelope) { e.Context.ScopeValues = []string{"apps", "sre"} },
			"duplicate":       func(e *alertEvidenceEnvelope) { e.Context.ScopeValues = []string{"eng", "eng", "sre"} },
			"other dimension": func(e *alertEvidenceEnvelope) { e.Policy.Dimension = "provider" },
		} {
			if bad := interpretAlertEvidence(resealed(t, row, change), tenant); bad.State != evidenceUnknownRead {
				t.Errorf("%s evidence accepted: %+v", name, bad)
			}
		}
	}
	check, err := m.CheckBudget(context.Background(), tenant, SpendDims{WorkspaceRef: "apps"})
	if err != nil || check.Allowed || check.SpendMicroUSD != 1000 {
		t.Errorf("sibling CheckBudget = %+v, err=%v, want blocked at 1000", check, err)
	}
	reserve, err := m.ReserveBudget(context.Background(), tenant, SpendDims{WorkspaceRef: "sre"}, 1)
	if err != nil || reserve.Allowed {
		t.Errorf("ReserveBudget = %+v, err=%v, want refused after recorded spend", reserve, err)
	}
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		p, err := sc.Policies().Get(context.Background(), id)
		if err != nil {
			return err
		}
		status, err := budgetStatus(context.Background(), sc, p, baseTime)
		if err != nil {
			return err
		}
		if status.SpendMicroUSD != 1000 || status.Samples != 3 || !status.Over || status.Truncated {
			t.Errorf("status = %+v, want exact subtree total 1000 from three samples", status)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDepartmentBudgetIDsAndFlatReferences(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	departments := budgetDepartments(t, st, tenant)
	id := createBudget(t, st, tenant, "engineering by id", budgetSpec{
		Dimension: "workspace", Key: departments["eng"].ID.String(), Period: "total", LimitMicroUSD: oneUSD,
		ReservedMicroUSD: oneUSD, Action: "block",
	})
	check, err := m.CheckBudget(context.Background(), tenant, SpendDims{WorkspaceRef: departments["sre"].ID.String()})
	if err != nil || check.Allowed || check.BudgetID != id.String() {
		t.Errorf("ID-scoped subtree = %+v, err=%v", check, err)
	}
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		for _, key := range []string{"sales", "external-provider-workspace", model.DefaultWorkspaceSlug} {
			spec := budgetSpec{Dimension: "workspace", Key: key}
			if err := spec.resolveWorkspace(context.Background(), sc); err != nil {
				return err
			}
			if !reflect.DeepEqual(spec.sampleFilters(), []model.Filter{eq(colWorkspaceRef, key)}) ||
				!spec.matches(attribution{WorkspaceRef: key}) || spec.matches(attribution{WorkspaceRef: "sre"}) {
				t.Errorf("flat reference %s changed its exact scope", key)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDepartmentBudgetAttemptUsesParent(t *testing.T) {
	forEachAdmissionEngine(t, runDepartmentBudgetAttemptUsesParent)
}

func runDepartmentBudgetAttemptUsesParent(t *testing.T, cfg store.Config) {
	_, st, tenant, _ := openFinCfg(t, cfg)
	budgetDepartments(t, st, tenant)
	id := createBudget(t, st, tenant, "engineering", budgetSpec{
		Dimension: "workspace", Key: "eng", Period: "total", LimitMicroUSD: oneUSD,
		ReservedMicroUSD: oneUSD, Action: "block", Currency: "USD", Thresholds: []float64{1},
	})
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		targets, err := planAttemptTargets(context.Background(), sc, AttemptBinding{
			Attribution: map[string]AttributionFact{"workspace": {State: factKnown, Values: []string{"sre"}}},
		}, model.NewTimestamp(baseTime))
		if err != nil {
			return err
		}
		if len(targets) != 1 || targets[0].snapshot.PolicyID != id {
			t.Fatalf("attempt targets = %+v, want parent budget %s", targets, id)
		}
		amount, err := targets[0].effectiveAmount(context.Background(), sc, model.NewTimestamp(baseTime))
		if err != nil {
			return err
		}
		if amount.Decimal() != "1000000" {
			t.Errorf("attempt effective amount = %s", amount.Decimal())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type departmentReadFaultData struct {
	api.ModuleData
	id model.ID
}

func (d departmentReadFaultData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.View(ctx, tenant, func(sc store.Scope) error {
		return fn(departmentReadFaultScope{Scope: sc, id: d.id})
	})
}

type departmentReadFaultScope struct {
	store.Scope
	id model.ID
}

func (s departmentReadFaultScope) Workspaces() store.WorkspaceRepo {
	return departmentReadFaultRepo{WorkspaceRepo: s.Scope.Workspaces(), id: s.id}
}

type departmentReadFaultRepo struct {
	store.WorkspaceRepo
	id model.ID
}

func (r departmentReadFaultRepo) Get(ctx context.Context, id model.ID) (model.Workspace, error) {
	if id == r.id {
		return model.Workspace{}, store.ErrStoreUnavailable
	}
	return r.WorkspaceRepo.Get(ctx, id)
}

func TestDepartmentBudgetLineageFailureRefusesAdmission(t *testing.T) {
	forEachAdmissionEngine(t, runDepartmentBudgetLineageFailureRefusesAdmission)
}

func runDepartmentBudgetLineageFailureRefusesAdmission(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	departments := budgetDepartments(t, st, tenant)
	createBudget(t, st, tenant, "engineering", budgetSpec{
		Dimension: "workspace", Key: "eng", Period: "total", LimitMicroUSD: oneUSD, Action: "block",
	})
	m.UseData(departmentReadFaultData{ModuleData: m.data, id: departments["platform"].ID})
	dims := SpendDims{WorkspaceRef: "sre"}
	check, err := m.CheckBudget(context.Background(), tenant, dims)
	if !errors.Is(err, store.ErrStoreUnavailable) || !check.Allowed {
		t.Errorf("CheckBudget = %+v, err=%v, want published fail-open result with read error", check, err)
	}
	admit, err := m.Reserve(context.Background(), tenant, AdmissionRequest{
		Scope: AdmissionScopeModelGateway, Dims: dims, EstimateMicroUSD: oneUSD, IdempotencyKey: "unread",
	})
	if err != nil || admit.Allowed || admit.Handle != "" || admit.Reason != ReasonStoreUnreachable {
		t.Errorf("Reserve = %+v, err=%v, want deny-closed with no hold", admit, err)
	}
}

func TestDepartmentBudgetHTTPSharedHoldAndMove(t *testing.T) {
	forEachAdmissionEngine(t, runDepartmentBudgetHTTPSharedHoldAndMove)
}

func runDepartmentBudgetHTTPSharedHoldAndMove(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	departments := budgetDepartments(t, st, tenant)
	createBudget(t, st, tenant, "engineering", budgetSpec{
		Dimension: "workspace", Key: "eng", Period: "total", LimitMicroUSD: oneUSD, Action: "block",
	})
	request := func(ws, key string) admissionAnswer {
		body := reserveBody(AdmissionRequest{
			Scope: AdmissionScopeModelGateway, Dims: SpendDims{WorkspaceRef: ws}, EstimateMicroUSD: oneUSD, IdempotencyKey: key,
		})
		body["dims"] = SpendDims{WorkspaceRef: ws}
		return serveAdmission(t, m, tenant, http.MethodPost, "/admission/reserve", body)
	}
	first := request("sre", "first")
	handle, _ := first.body["handle"].(string)
	if first.code != http.StatusOK || handle == "" {
		t.Fatalf("first = %d %s", first.code, first.raw)
	}
	second := request("apps", "second")
	if second.code != http.StatusPaymentRequired {
		t.Fatalf("sibling = %d %s, want 402", second.code, second.raw)
	}
	release := serveAdmission(t, m, tenant, http.MethodPost, "/admission/release", map[string]any{"handle": handle})
	if release.code != http.StatusOK {
		t.Fatalf("release = %d %s", release.code, release.raw)
	}
	if next := request("apps", "third"); next.code != http.StatusOK {
		t.Fatalf("after release = %d %s", next.code, next.raw)
	}
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, err := sc.Workspaces().SetParent(context.Background(), departments["platform"].ID, departments["sales"].ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if moved := request("sre", "moved"); moved.code != http.StatusOK {
		t.Fatalf("moved subtree = %d %s", moved.code, moved.raw)
	}
}

func TestDepartmentBudgetPreparedAttemptSurvivesMove(t *testing.T) {
	forEachAdmissionEngine(t, func(t *testing.T, cfg store.Config) {
		m, st, tenant, _ := openFinCfg(t, cfg)
		departments := budgetDepartments(t, st, tenant)
		createBudget(t, st, tenant, "engineering", budgetSpec{Dimension: "workspace", Key: "eng", Period: "total", LimitMicroUSD: oneUSD, Action: "block", Currency: "USD", Thresholds: []float64{1}})
		verifier := t0Wire(m, tenant)
		t0Active(t, m, st, tenant)
		req := t0Request(t, st, tenant)
		req.Binding.Attribution["workspace"] = AttributionFact{State: factKnown, Values: []string{"sre"}, Evidence: []EvidenceRef{labEvidence("department-attestation")}}
		verifier.approve(req)
		view := t0Admit(t, m, tenant, req)
		if len(view.Targets) != 1 || view.Targets[0].ScopeKey != "eng" {
			t.Fatalf("targets = %+v", view.Targets)
		}
		if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
			_, err := sc.Workspaces().SetParent(context.Background(), departments["platform"].ID, departments["sales"].ID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		replay, err := m.prepareAttempt(context.Background(), tenant, req)
		if err != nil || !replay.Replayed || replay.Attempt == nil {
			t.Fatalf("replay after move = %+v, err=%v", replay, err)
		}
		read, err := m.GetAttempt(context.Background(), tenant, req.AttemptRef)
		if err != nil || !reflect.DeepEqual(read.Targets, view.Targets) {
			t.Fatalf("read after move = %+v, err=%v", read, err)
		}
		// This still contains the anchor and attributed child and has a valid
		// canonical shape. Only the reservation commitment detects the changed set.
		mutateAttemptTargets(t, st, tenant, req.AttemptRef, func(ts []jsonTarget) {
			ts[0].WorkspaceRefs = []string{"eng", "platform", "sre"}
		})
		if _, err := m.GetAttempt(context.Background(), tenant, req.AttemptRef); attemptCode(err) != errCodeLedgerIndeterminate {
			t.Errorf("changed department scope was readable: %v", err)
		}
		if _, err := m.prepareAttempt(context.Background(), tenant, req); attemptCode(err) != errCodeLedgerIndeterminate {
			t.Errorf("changed department scope was replayable: %v", err)
		}
	})
}

func TestDepartmentBudgetMoveBeforeReservationWrite(t *testing.T) {
	forEachAdmissionEngine(t, func(t *testing.T, cfg store.Config) {
		for _, legacy := range []bool{false, true} {
			for _, moveIn := range []bool{false, true} {
				t.Run(fmt.Sprintf("legacy=%t/move-in=%t", legacy, moveIn), func(t *testing.T) {
					m, st, tenant, _ := openFinCfg(t, cfg)
					departments := budgetDepartments(t, st, tenant)
					id := createBudget(t, st, tenant, "engineering", budgetSpec{Dimension: "workspace", Key: "eng", Period: "total", LimitMicroUSD: oneUSD, ReservedMicroUSD: oneUSD, Action: "block"})
					move := func(parent string) {
						t.Helper()
						if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
							_, err := sc.Workspaces().SetParent(context.Background(), departments["platform"].ID, departments[parent].ID)
							return err
						}); err != nil {
							t.Fatal(err)
						}
					}
					if moveIn {
						move("sales")
					}
					nth := int64(2)
					if legacy {
						nth = 1
					}
					paused := &pausedReadData{ModuleData: m.data, nth: nth, reached: make(chan struct{}), resume: make(chan struct{})}
					m.UseData(paused)
					ctx, cancel := context.WithTimeout(pausedCtx(context.Background()), 15*time.Second)
					defer cancel()
					defer func() {
						select {
						case <-paused.resume:
						default:
							close(paused.resume)
						}
					}()
					type answer struct {
						allowed        bool
						budget, handle string
						err            error
					}
					done := make(chan answer, 1)
					go func() {
						dims := SpendDims{WorkspaceRef: "sre"}
						if legacy {
							r, err := m.ReserveBudget(ctx, tenant, dims, 1)
							done <- answer{r.Allowed, r.BudgetID, r.Handle, err}
							return
						}
						r, err := m.Reserve(ctx, tenant, AdmissionRequest{Scope: AdmissionScopeModelGateway, Dims: dims, EstimateMicroUSD: 1, IdempotencyKey: "move"})
						done <- answer{r.Allowed, r.BudgetID, r.Handle, err}
					}()
					awaitPausedRead(t, paused)
					parent := "sales"
					if moveIn {
						parent = "eng"
					}
					move(parent)
					close(paused.resume)
					var result answer
					select {
					case result = <-done:
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
					if result.err != nil || result.allowed == moveIn || result.handle != "" {
						t.Errorf("reservation = %+v, move-in=%t", result, moveIn)
					}
					if moveIn && result.budget != id.String() {
						t.Errorf("budget = %s, want %s", result.budget, id)
					}
				})
			}
		}
	})
}
