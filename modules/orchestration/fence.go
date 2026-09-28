// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"net/http"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// Orchestration's fenced writers store references that bind an account to a
// duty or let work act for it in the tenant: a workflow step naming a work
// participant, a run's initiator and its frozen steps, a schedule's owner. Each
// reads the named accounts' standing through the request's standing port and
// pins them with the tenant's directory fact before it writes (auth.FencedWrite).
// The accounts are read from the row about to be written, through the same
// declarations the write seam and the retirement step read.

// countedColumn is one counted column of a table and its declaration.
type countedColumn struct {
	name string
	decl *model.ColumnDecl
}

// The counted columns of each table the fenced writers and the retirement step
// read.
var (
	workflowCountedColumns = []countedColumn{{colWfSteps, workflowStepsDecl}}
	runCountedColumns      = []countedColumn{
		{colWrActor, pdeclRunActor}, {colWrUserIdentity, pdeclRunUserIdentity}, {colWrSteps, runStepsDecl},
	}
	scheduleCountedColumns = []countedColumn{{colOwnerActor, pdeclScheduleOwnerActor}, {colOwnerUserRef, pdeclScheduleOwnerUser}}
)

// countedSubjects returns the accounts rec names through the counted view of
// each column's declaration; the fence skips those that name no account.
func countedSubjects(rec model.Record, cols []countedColumn) ([]model.ID, error) {
	var out []model.ID
	for _, c := range cols {
		ids, err := c.decl.CountedUserIDs(rec, c.name)
		if err != nil {
			return nil, err
		}
		out = append(out, ids...)
	}
	return out, nil
}

// stepSubjects returns the accounts a workflow's step graph names.
func stepSubjects(steps []stepDTO) ([]model.ID, error) {
	return countedSubjects(model.Record{colWfSteps: encodeSteps(steps)}, workflowCountedColumns)
}

// writeFenceRefusal answers a fence refusal with 409 and its stable code, and
// reports whether err was one.
func writeFenceRefusal(w http.ResponseWriter, err error) bool {
	code, ok := auth.FenceRefusalCode(err)
	if !ok {
		return false
	}
	writeJSON(w, http.StatusConflict, errorBodyCode(code, err.Error()))
	return true
}
