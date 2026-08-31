// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// workflow_dryrun.go — the no-effects execution plan (the notify
// routes/evaluate posture): what WOULD run, in which order, behind which
// gates. It writes nothing, dispatches nothing and opens no approval; it also
// re-checks reference staleness (a schedule retired after the graph was saved)
// so the plan a human reviews is current, not archeological.

// planStepDTO is one step of the dry-run plan, in topological order.
type planStepDTO struct {
	Order     int      `json:"order"`
	Ref       string   `json:"ref"`
	Kind      string   `json:"kind"`
	Action    string   `json:"action"`
	Requires  []string `json:"requires"`
	DependsOn []string `json:"depends_on"`
	Warning   string   `json:"warning,omitempty"`
}

// dryRunResponse is the whole plan: the hash phase 1 would bind an approval
// to, the run-level gates, and the per-step plan.
type dryRunResponse struct {
	PlanHash string        `json:"plan_hash"`
	Enabled  bool          `json:"enabled"`
	Requires []string      `json:"requires"`
	Steps    []planStepDTO `json:"steps"`
}

// Gate vocabulary for the plan's requires lists (stable strings the editor
// renders; additions are additive).
const (
	reqHITLApproval   = "hitl_approval"
	reqKillSwitch     = "kill_switch_clear"
	reqBudgetHeadroom = "budget_headroom"
	reqDispatcher     = "dispatcher_wired"
	reqNotifyActuator = "notify_actuator_wired"
)

// handleDryRunWorkflow computes the execution plan for a workflow as declared,
// with ZERO effects (read-tier).
func (m *Module) handleDryRunWorkflow(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("workflow id required"))
		return
	}
	var out dryRunResponse
	var staleGraph *graphError
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		rec, steps, ok, err := getWorkflow(r.Context(), sc, id)
		if err != nil || !ok {
			return err
		}
		found = true
		targets, terr := m.resolveTargets(r.Context(), sc, mc.Tenant, steps)
		if terr != nil {
			return terr
		}
		out.PlanHash = planHashOfWorkflow(rec.String(model.ColID), rec.String(colWfName), steps, targets)
		out.Enabled = rec.Bool(colWfEnabled)
		out.Requires = []string{reqHITLApproval, reqKillSwitch}
		ordered, ge := validateGraph(steps, m.maxWorkflowSteps)
		if ge != nil {
			// A stored graph is validated at write time, so failing here means the
			// caps were tightened underneath it. That is an operator-actionable
			// state, not an engine fault: say which step and why, rather than
			// answering 500 and leaving the graph un-previewable but still
			// runnable. (Running it re-validates too, and fails the same way.)
			staleGraph = ge
			return nil
		}
		out.Steps = make([]planStepDTO, 0, len(ordered))
		for i, s := range ordered {
			p := planStepDTO{Order: i + 1, Ref: s.Ref, Kind: s.Kind, DependsOn: s.DependsOn, Requires: []string{}}
			switch s.Kind {
			case stepScheduleFire:
				var cfg scheduleFireConfig
				_ = json.Unmarshal(s.Config, &cfg)
				p.Requires = []string{reqKillSwitch, reqBudgetHeadroom, reqDispatcher}
				p.Action = "dispatch schedule " + cfg.ScheduleID
				repo, err := sc.Ext(scheduleKind)
				if err != nil {
					return err
				}
				srec, err := repo.Get(r.Context(), model.ID(cfg.ScheduleID))
				switch {
				case err != nil && isNotFound(err):
					p.Warning = "schedule no longer exists; this step will fail"
				case err != nil:
					return err
				case srec.String(colDesiredStat) == "retired":
					p.Warning = "schedule is retired; this step will fail"
				default:
					p.Action = "dispatch schedule " + srec.String(colSchedName) + " (" + srec.String(colSubjectKind) + " " + srec.String(colSubjectRef) + ")"
				}
			case stepEventingEmit:
				var cfg eventingEmitConfig
				_ = json.Unmarshal(s.Config, &cfg)
				p.Action = "publish workflow.signal with label " + cfg.Label
			case stepNotifyTest:
				var cfg notifyTestConfig
				_ = json.Unmarshal(s.Config, &cfg)
				p.Requires = []string{reqNotifyActuator}
				// Show the route's NAME when the seam can resolve it: a human
				// approving this plan cannot judge an opaque id.
				if name, ok, lerr := m.notifyTest.LookupRoute(r.Context(), mc.Tenant, cfg.RouteID); lerr == nil && ok {
					p.Action = "send a test notification through route " + name
				} else {
					p.Action = "send a test notification through route " + cfg.RouteID
					if lerr == nil && !ok {
						p.Warning = "alert route no longer exists; this step will fail"
					}
				}
			case stepWait:
				var cfg waitConfig
				_ = json.Unmarshal(s.Config, &cfg)
				p.Action = fmt.Sprintf("pause the run %ds", cfg.Seconds)
			case stepApprovalGate:
				var cfg approvalGateConfig
				_ = json.Unmarshal(s.Config, &cfg)
				p.Requires = []string{reqHITLApproval}
				p.Action = "open a HITL approval and pause until resolved"
				if cfg.Reason != "" {
					p.Action += ": " + cfg.Reason
				}
			}
			out.Steps = append(out.Steps, p)
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	if staleGraph != nil {
		writeGraphError(w, staleGraph)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
