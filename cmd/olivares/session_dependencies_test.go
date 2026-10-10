// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/sdk"
)

// This crosses the engine's real composition and the new sessions constructor,
// including optional modules/custody left OFF on a fresh installation.
func TestSessionsDependencySetFromRealBoot(t *testing.T) {
	t.Setenv(envCommunicationActivation, "off")
	eng := bootForComposition(t, t.TempDir())
	t.Cleanup(func() { _ = eng.sessionsMod.Stop(context.Background()); _ = eng.Close() })
	builder := eng.sessionsMod.Dependencies
	// The actual engine constructor, not just a fresh test constructor, must
	// require its own composition record before any lifecycle effect.
	mcp := builder.SessionMCP
	builder.SessionMCP = nil
	if err := eng.sessionsMod.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "SessionMCP") {
		builder.SessionMCP = mcp
		t.Fatalf("engine constructor did not require its dependency set: %v", err)
	}
	builder.SessionMCP = mcp
	if err := eng.sessionsMod.Start(context.Background()); err != nil {
		t.Fatalf("complete engine set: %v", err)
	}
	if eng.sessionsMod.Dependencies != builder {
		t.Fatal("Start replaced the constructor-owned dependency record")
	}
	required := []string{
		"Data",
		"Standing",
		"RecoveryData",
		"WorkspaceSnapshotAuthority",
		"WorkIdentity",
		"WorkContent",
		"WorkEventSink",
		"WorkAuthorizer",
		"OrchestrationScopes",
		"WorkOutboxAuthority",
		"ProtocolLocalResourceResolver",
		"ProviderSources",
		"ToolLogin",
		"ProfileLogin",
		"CommunicationAuthority",
		"ManagedStopAuthority",
		"SessionMCP",
		"LaunchGate",
		"StopGate",
		"Recorder",
		"CostSink",
		"ListPricer",
		"WorkSessionCreds",
		"CommunicationSessionCreds",
		"RecoveryWorkSessionCreds",
		"RecoveryCommunicationSessionCreds",
		"SessionAccessCheck",
		"QueuedCredentialCapture",
		"ProviderApprovalPolicy",
		"ProviderApprovalPrincipal",
		"QueuedLaunchAuthorization",
		"ApprovalGate",
	}
	t.Run("empty communication authority", func(t *testing.T) {
		d := *eng.sessionsMod.Dependencies
		d.CommunicationAuthority = &sessions.CommunicationRequestAuthority{}
		m := sessions.NewWithDependencies(&d)
		readiness, _ := m.EvaluateCommunicationReadiness(context.Background())
		if readiness.Components.PermissionsReady {
			t.Error("empty communication authority certified permissions readiness")
		}
		if err := m.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "CommunicationAuthority") {
			_ = m.Stop(context.Background())
			t.Errorf("empty communication authority startup: %v", err)
		}
	})
	t.Run("typed-nil required port", func(t *testing.T) {
		d := *eng.sessionsMod.Dependencies
		var sink *workEventSink
		d.WorkEventSink = sink
		m := sessions.NewWithDependencies(&d)
		if err := m.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "WorkEventSink") {
			_ = m.Stop(context.Background())
			t.Fatalf("typed nil WorkEventSink: %v", err)
		}
	})
	for _, missing := range []string{"resolver", "authorizer", "elector"} {
		t.Run("ManagedStopAuthority."+missing, func(t *testing.T) {
			d := *eng.sessionsMod.Dependencies
			switch missing {
			case "resolver":
				d.ManagedStopAuthority = sessions.NewManagedStopAuthority(nil, eng.authz, eng.store.Leader())
			case "authorizer":
				d.ManagedStopAuthority = sessions.NewManagedStopAuthority(eng.authr, nil, eng.store.Leader())
			case "elector":
				d.ManagedStopAuthority = sessions.NewManagedStopAuthority(eng.authr, eng.authz, nil)
			}
			m := sessions.NewWithDependencies(&d)
			if err := m.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "ManagedStopAuthority."+missing) {
				_ = m.Stop(context.Background())
				t.Fatalf("incomplete managed Stop %s: %v", missing, err)
			}
		})
	}
	t.Run("optional ports remain unavailable", func(t *testing.T) {
		d := *eng.sessionsMod.Dependencies
		optional := []string{
			"ProtocolBindingReconciler", "CommunicationSealer", "CommunicationDirectoryResolver",
			"CommunicationAudienceAttestor", "CommunicationGrantClosure", "CommunicationReadAuthorizer",
			"CommunicationOperationAuthorizer", "CommunicationGuardData", "CommunicationStoreReadiness",
			"CommunicationPumpReadiness", "CursorKeyring", "Runner", "GitRead", "Creds", "Classifier",
			"ProgramResolver", "ProviderVault", "ProviderProbe", "HostTools", "Tracer", "ApprovalRecoveryTenants",
		}
		for _, name := range optional {
			reflect.ValueOf(&d).Elem().FieldByName(name).SetZero()
		}
		m := sessions.NewWithDependencies(&d)
		// Tracer is a typed nil pointer when boxed by validation/reporting;
		// assigning it during assembly must not make it a required port.
		m.Tracer = nil
		// An empty exported key bundle remains an unavailable optional port.
		m.CursorKeyring = &sessions.CursorTokenKeys{}
		var output bytes.Buffer
		rt := runtime.New(runtime.Options{Logger: slog.New(slog.NewTextHandler(&output, nil))})
		if err := rt.AddModule(m, sdk.Config{}); err != nil {
			t.Fatal(err)
		}
		if err := rt.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = rt.Stop(context.Background()) })
		for _, component := range rt.Status() {
			if component.Name == sessions.Name && component.Status != "running" {
				t.Fatalf("optional omission startup: %+v", component)
			}
		}
		if m.ProviderVaultWired() || m.ProviderProbeWired() || m.CommunicationCursorTokenKeyringBound() {
			t.Fatal("absent optional capability became available")
		}
		if err := rt.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		log := output.String()
		if !strings.Contains(log, "sessions: optional dependencies unavailable") {
			t.Fatalf("missing optional report: %s", log)
		}
		for _, name := range optional {
			if !strings.Contains(log, name) {
				t.Errorf("optional %s not named: %s", name, log)
			}
		}
	})
	for _, name := range required {
		t.Run(name, func(t *testing.T) {
			d := *eng.sessionsMod.Dependencies
			field := reflect.ValueOf(&d).Elem().FieldByName(name)
			field.SetZero()
			m := sessions.NewWithDependencies(&d)
			err := m.Start(context.Background())
			if err == nil {
				_ = m.Stop(context.Background())
				t.Fatalf("missing %s started", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Fatalf("missing %s not named: %v", name, err)
			}
		})
	}
}

// Runtime.Init subscribes modules before Start. The constructor-owned ports,
// including the engine/MCP cycle, must be complete before that publication.
func TestSessionsDependencySetAvailableBeforeRuntimeStart(t *testing.T) {
	t.Setenv(envCommunicationActivation, "off")
	prepareCompositionTestBoot(t)
	eng, err := boot(context.Background(), bootConfig{DataDir: t.TempDir(), Engine: "sqlite", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	for _, component := range eng.rt.Status() {
		if component.Name == sessions.Name {
			if component.Status != "running" {
				t.Fatalf("sessions startup: %+v", component)
			}
			return
		}
	}
	t.Fatal("sessions missing from runtime status")
}
