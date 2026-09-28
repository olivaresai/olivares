// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hostops_test

import (
	"github.com/olivaresai/olivares/appliance/layer/hostops"
	"reflect"
	"strings"
	"testing"
)

func TestTask_GroupsTheExistingOperationReadModel(t *testing.T) {
	dir := t.TempDir()
	e, err := hostops.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{strings.Repeat("e1", 16), strings.Repeat("e2", 16)}
	for i, id := range ids {
		c := testCommand(id)
		c.TaskID = "task.confirmed"
		c.Target = []string{"network", "packages"}[i]
		if _, _, err := e.Submit(c, nil); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := hostops.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	task, err := reopened.Task("task.confirmed")
	if err != nil || task.TaskID != "task.confirmed" || !reflect.DeepEqual(task.OperationIDs, ids) {
		t.Fatalf("task=%#v err=%v", task, err)
	}
	if _, err := reopened.Task("../other"); err == nil {
		t.Fatal("caller path accepted")
	}
}
