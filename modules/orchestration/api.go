// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// APINamespace roots the module's routes at /v1/m/orchestration/.
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares the module's permissions so the built-in roles grant them by
// verb tier (viewer→read, editor→write, admin/owner→admin).
func (m *Module) Permissions() []auth.Permission {
	return []auth.Permission{
		permGraphRead, permScheduleRead, permScheduleWrite, permScheduleAdmin,
		permWorkflowRead, permWorkflowWrite, permWorkflowAdmin,
	}
}

// APIRoutes mounts the module's routes. The engine wraps each with authentication,
// tenant resolution and the declared permission check, and pins the data handle to
// the resolved tenant.
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	// The live communication/delegation graph (privileged, self-audited reads).
	reg.Handle("GET", "/graph", permGraphRead, m.route("handleGraph"))
	reg.Handle("GET", "/graph/neighbors", permGraphRead, m.route("handleNeighbors"))
	reg.Handle("GET", "/flows", permGraphRead, m.route("handleFlows"))
	reg.Handle("GET", "/timeline", permGraphRead, m.route("handleTimeline"))
	reg.Handle("GET", "/stream", permGraphRead, m.route("handleStream"))

	// Governed schedules. Declaring/retargeting desired state is write-tier; FIRING
	// is admin-tier AND gated by the HITL approval (deny-by-default).
	reg.Handle("GET", "/schedules", permScheduleRead, m.route("handleListSchedules"))
	reg.Handle("POST", "/schedules", permScheduleWrite, m.route("handleCreateSchedule"))
	reg.Handle("GET", "/schedules/{id}", permScheduleRead, m.route("handleGetSchedule"))
	reg.Handle("PATCH", "/schedules/{id}", permScheduleWrite, m.route("handlePatchSchedule"))
	reg.Handle("POST", "/schedules/{id}/fire", permScheduleAdmin, m.route("handleFire"))
	reg.Handle("GET", "/schedules/{id}/decisions", permScheduleRead, m.route("handleScheduleDecisions"))
	reg.Handle("GET", "/schedules/{id}/revisions", permScheduleRead, m.route("handleListRevisions"))
	reg.Handle("POST", "/schedules/{id}/restore", permScheduleWrite, m.route("handleRestoreSchedule"))

	// The append-only fire/miss governance-evidence ledger.
	reg.Handle("GET", "/decisions", permScheduleRead, m.route("handleDecisions"))

	// DAG workflows. Declaring/editing the graph is write-tier; the dry-run
	// is a no-effects read; RUNNING is admin-tier AND HITL-gated (two-phase).
	reg.Handle("GET", "/workflows", permWorkflowRead, m.route("handleListWorkflows"))
	reg.Handle("POST", "/workflows", permWorkflowWrite, m.route("handleCreateWorkflow"))
	reg.Handle("GET", "/workflows/{id}", permWorkflowRead, m.route("handleGetWorkflow"))
	reg.Handle("PATCH", "/workflows/{id}", permWorkflowWrite, m.route("handlePatchWorkflow"))
	reg.Handle("PUT", "/workflows/{id}/steps", permWorkflowWrite, m.route("handlePutWorkflowSteps"))
	reg.Handle("GET", "/workflows/{id}/revisions", permWorkflowRead, m.route("handleListWorkflowRevisions"))
	reg.Handle("POST", "/workflows/{id}/restore", permWorkflowWrite, m.route("handleRestoreWorkflow"))
	reg.Handle("POST", "/workflows/{id}/dry-run", permWorkflowRead, m.route("handleDryRunWorkflow"))
	reg.Handle("POST", "/workflows/{id}/run", permWorkflowAdmin, m.route("handleRunWorkflow"))
	reg.Handle("GET", "/workflows/{id}/runs", permWorkflowRead, m.route("handleListWorkflowRuns"))
	reg.Handle("GET", "/workflows/{id}/runs/{run}", permWorkflowRead, m.route("handleGetWorkflowRun"))
	// The owning continuation of a run paused for reauthentication: admin-tier,
	// like starting the run, and bound to the caller's own credential.
	reg.Handle("POST", "/workflows/{id}/runs/{run}/reauthorize", permWorkflowAdmin, m.route("handleReauthorizeWorkflowRun"))
}
