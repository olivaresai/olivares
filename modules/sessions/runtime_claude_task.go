// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package sessions

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"

	"github.com/olivaresai/olivares/core/model"
)

// claudeTaskID is the shape of the ids Claude Code gives its background tasks
// ("bxsdzgtr2", "ae045e70a45dc7509"): a short token, never a path or a command.
var claudeTaskID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// stopRunTask stops one background task of a live Claude Code session (a shell, a
// monitor or an agent the tool runs on its own) the way Claude Code itself does: the
// stop_task control request on the child's stream-json stdin, answered by a control
// response with the same request id. The session, its process and its turn keep
// running. A tool with no such request is told so; it never becomes a stop.
// Both ledger events keep the row's reason: stopping a task is not a lifecycle outcome.
func (m *Module) stopRunTask(ctx context.Context, tenant model.TenantID, runRef, taskID, actor, actorKind string) (runDTO, error) {
	if !claudeTaskID.MatchString(taskID) {
		return runDTO{}, badRequest("task must name one background task of the session")
	}
	release, err := m.rt.lockRunContext(ctx, liveKey(tenant, runRef))
	if err != nil {
		return runDTO{}, err
	}
	defer release()
	rec, err := m.loadRun(ctx, tenant, runRef)
	if err != nil {
		return runDTO{}, err
	}
	if err := m.refuseUnfencedActiveWork(ctx, tenant, rec); err != nil {
		return runDTO{}, err
	}
	if rec.String(colState) != stateRunning {
		return runDTO{}, conflictErr("session is not running (state=" + rec.String(colState) + ")")
	}
	lr, ok := m.rt.getLive(tenant, runRef)
	if !ok {
		return runDTO{}, conflictErr("session is not supervised by this runtime")
	}
	if !claudeFrameRun(lr, rec) {
		return runDTO{}, conflictErr("this session's tool cannot stop one background task; stop the session instead")
	}
	if _, err := m.transition(ctx, tenant, runRef, transitionInput{
		event: "task_stopping", actor: actor, actorKind: actorKind,
		detail: "background task " + taskID + " stop requested",
		lease:  lr.claim, guard: guardRuntimeLaunch(lr.launchID), keepReason: true,
	}); err != nil {
		return runDTO{}, err
	}
	if err := m.assertRunAuthority(ctx, lr); err != nil {
		return runDTO{}, err
	}
	id := "olv-stop-task-" + string(model.NewID())
	line, err := json.Marshal(map[string]any{
		"type":       "control_request",
		"request_id": id,
		"request":    map[string]any{"subtype": "stop_task", "task_id": taskID},
	})
	if err != nil {
		return runDTO{}, err
	}
	cursor := lr.ring.cursorNow()
	if err := lr.proc.Send(ctx, line); err != nil {
		m.warnf("background task stop not delivered", "run_ref", runRef)
		return runDTO{}, &runErr{http.StatusBadGateway, "the stop could not be written to the session; the task may still be running"}
	}
	if err := m.awaitClaudeControlResponse(ctx, lr.ring, cursor, id); err != nil {
		return runDTO{}, err
	}
	if _, err := m.transition(ctx, tenant, runRef, transitionInput{
		event: "task_stopped", actor: actor, actorKind: actorKind,
		detail: "background task " + taskID + " stopped; the session stays live",
		lease:  lr.claim, guard: guardRuntimeLaunch(lr.launchID), keepReason: true,
	}); err != nil {
		return runDTO{}, err
	}
	return m.getRun(ctx, tenant, runRef)
}
