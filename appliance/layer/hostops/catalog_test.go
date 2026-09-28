// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hostops_test

import (
	"github.com/olivaresai/olivares/appliance/layer/hostops"
	"strings"
	"testing"
)

func TestOperation_RefusesUndeclaredModuleAndVerbBeforeClaim(t *testing.T) {
	e, err := hostops.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := testCommand(strings.Repeat("d1", 16))
	c.Verb = "shell"
	if _, status, err := e.Submit(c, func() error { t.Fatal("undeclared effect ran"); return nil }); err == nil || status != 422 {
		t.Fatalf("status=%d err=%v", status, err)
	}
	d := hostops.StatusDescriptor()
	d.ID = "services.status"
	d.Module = "services"
	catalog, err := hostops.NewCatalog([]hostops.Descriptor{d})
	if err != nil {
		t.Fatal(err)
	}
	e, err = hostops.OpenWithCatalog(t.TempDir(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	c.Module = "services"
	c.Verb = "status"
	if _, _, err := e.Submit(c, nil); err != nil {
		t.Fatal(err)
	}
}
func TestOperation_EmptyPostconditionsDoNotProveSuccess(t *testing.T) {
	e, err := hostops.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := testCommand(strings.Repeat("d2", 16))
	if _, _, err := e.Submit(c, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Recover(c.OperationID, hostops.Observation{P1: "performed", Postconditions: []string{""}}); err == nil {
		t.Fatal("empty measurement accepted")
	}
	got, _, err := e.Get(c.OperationID)
	if err != nil || got.State != hostops.StateRunning {
		t.Fatalf("changed without measurement: %#v %v", got, err)
	}
}
