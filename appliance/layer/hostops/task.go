// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hostops

import (
	"errors"
	"os"
	"strings"
)

// Task groups the existing operation records. It creates no task ledger or
// effect, and an unreadable record refuses the projection instead of hiding it.
func (e *Engine) Task(id string) (Task, error) {
	if !matchToken(id, true) {
		return Task{}, refuse("task_id")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	release, err := e.hold()
	if err != nil {
		return Task{}, err
	}
	defer release()
	entries, err := os.ReadDir(e.dir)
	if err != nil {
		return Task{}, err
	}
	task := Task{TaskID: id, OperationIDs: []string{}}
	for _, entry := range entries {
		operationID, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || !isHex(operationID, 32) {
			continue
		}
		record, found, err := e.read(e.recordPath(operationID))
		if err != nil {
			return Task{}, err
		}
		if found && record.TaskID == id {
			task.OperationIDs = append(task.OperationIDs, operationID)
		}
	}
	if len(task.OperationIDs) == 0 {
		return Task{}, errors.New("no such task")
	}
	return task, nil
}
