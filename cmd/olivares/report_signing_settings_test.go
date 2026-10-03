// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"

	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/reporting"
)

func TestReportingSigningSettingsSurviveActivationAndModuleChanges(t *testing.T) {
	ctx := context.Background()
	// Deployment settings use the core schema. No module runtime or edition
	// license source is needed to exercise this durable record.
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error {
		_, err := sys.EnsureSystemTenant(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p := newProductSettings(st, t.TempDir())
	actor := operator(t)
	settings, ok := any(p).(reporting.SigningSettingsStore)
	if !ok {
		t.Fatal("deployment settings have no reporting signing state")
	}
	state, err := settings.ReportingSigning(ctx)
	if err != nil || state != nil {
		t.Fatal("absent setting must leave legacy fallback available")
	}
	want := reporting.SigningState{Enabled: true, KeyID: "reporting-test", SecretRef: "reporting/signing/test"}
	if err := settings.SaveReportingSigning(ctx, actor, want); err != nil {
		t.Fatal(err)
	}
	if err := p.SaveActivation(ctx, actor, activationWith(reportingActive), nil); err != nil {
		t.Fatal(err)
	}
	if err := p.writeModules(ctx, actor, "test.module.selection", []string{"olivares.reporting"}); err != nil {
		t.Fatal(err)
	}
	reopened := newProductSettings(p.st, p.dataDir)
	settings = any(reopened).(reporting.SigningSettingsStore)
	got, err := settings.ReportingSigning(ctx)
	if err != nil || got == nil || *got != want {
		t.Fatal("signing setting was lost after activation/module changes")
	}
	want.Enabled = false
	if err := settings.SaveReportingSigning(ctx, actor, want); err != nil {
		t.Fatal(err)
	}
	activation, err := p.Activation(ctx)
	if err != nil || !sameActivation(activation, activationWith(reportingActive)) {
		t.Fatal("signing change altered activation")
	}
	doc, _, err := p.load(ctx)
	if err != nil || doc.Modules == nil || len(doc.Modules.Selected) != 1 || doc.Modules.Selected[0] != "olivares.reporting" {
		t.Fatal("signing change altered module selection")
	}
}
