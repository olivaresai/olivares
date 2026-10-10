// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise || !addon_ids

package orchestration

import (
	"context"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/sdk"
	"net/http"
)

// Module retains the schema and stored account obligations; it never schedules or delegates.
type Module struct{ data api.ModuleData }

func New() *Module { return &Module{} }
func (m *Module) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: Name, Version: "0.1.0", APIVersion: sdk.APIVersion, Type: sdk.TypeModule, Title: "Orchestration", Description: "Orchestration is a Business capability. Stored data remains exportable."}
}
func (m *Module) UseData(data api.ModuleData)                            { m.data = data }
func (m *Module) Init(context.Context, sdk.Host) error                   { return nil }
func (m *Module) Start(context.Context) error                            { return nil }
func (m *Module) Stop(context.Context) error                             { return nil }
func (m *Module) UseWorkflowCredentialBinder(WorkflowCredentialBinder)   {}
func (m *Module) RunCadenceScan(context.Context, api.ModuleContext)      {}
func (m *Module) AdvanceWorkflowRuns(context.Context, api.ModuleContext) {}
func (m *Module) route(string) api.ModuleHandler                         { return unavailable }
func unavailable(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	_, _ = w.Write([]byte(`{"error":"orchestration_unavailable","message":"Orchestration is a Business capability."}`))
}
