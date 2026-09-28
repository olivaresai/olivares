// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package runtime_test

import (
	"context"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	"github.com/olivaresai/olivares/sdk/model"
	"testing"
)

// This fixture is the public storage port; production qualification uses the
// real inventory/ModuleData path in modules/inventory/coverage_test.go.
type coverageRecorder struct{}

func (coverageRecorder) BeginRun(context.Context, event.InventoryRun) error { return nil }
func (coverageRecorder) StartCollection(context.Context, event.InventoryRun, model.InventoryCollectionStart) error {
	return nil
}
func (coverageRecorder) AdmitMember(context.Context, event.InventoryRun, string, event.InventoryMember) error {
	return nil
}
func (coverageRecorder) FinishRun(context.Context, event.InventoryRun, event.InventoryFinish) error {
	return nil
}

type coverageModule struct{ *fakeModule }

func (*coverageModule) InventoryCoverageRecorder() event.InventoryRecorder { return coverageRecorder{} }
func TestInventoryCoverageDuplicateRecorder(t *testing.T) {
	rt := runtime.New(runtime.Options{Logger: quiet()})
	if err := rt.AddModule(&coverageModule{&fakeModule{name: "inventory-one"}}, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddModule(&coverageModule{&fakeModule{name: "inventory-two"}}, sdk.Config{}); err == nil {
		t.Fatal("second collection recorder accepted")
	}
	// A rejected provider must not reserve its component name.
	if err := rt.AddModule(&fakeModule{name: "inventory-two"}, sdk.Config{}); err != nil {
		t.Fatal("rejected recorder reserved name", err)
	}
}
