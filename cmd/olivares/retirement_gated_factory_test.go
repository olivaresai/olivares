// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/compliance"
	"github.com/olivaresai/olivares/modules/knowledge"
)

type legacyRetirementFactory struct{ calls int }

func (*legacyRetirementFactory) APINamespace() string           { return "retirement-factory-fixture" }
func (*legacyRetirementFactory) APIRoutes(api.RouteRegistrar)   {}
func (*legacyRetirementFactory) Permissions() []auth.Permission { return nil }
func (m *legacyRetirementFactory) RetirementStep() auth.RetirementStep {
	m.calls++
	return authPartitionStep{}
}
func (*legacyRetirementFactory) RetirementCovers() []string { return nil }

type gatedRetirementFactory struct {
	legacyRetirementFactory
	gate knowledge.HoldGate
}

func (m *gatedRetirementFactory) RetirementStepWithHoldGate(g knowledge.HoldGate) auth.RetirementStep {
	m.gate = g
	return authPartitionStep{}
}

func TestRetirementFactoryReceivesDormantComplianceHoldGate(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		user := e.onboard(e.tT, "gated-factory@consent.test", "viewer")
		id := e.seedFenced(e.tT, "compliance.legal_hold", model.Record{
			"matter_ref": "matter-factory", "scope_kind": "subject", "subject_kind": "user",
			"subject_ref": user.String(), "reason": "PRIVATE FACTORY HOLD CONTENT", "status": "active", "created_by": "test",
		}, user)
		for _, selected := range [][]string{nil, {"compliance"}} {
			name := "dormant"
			if len(selected) > 0 {
				name = "active"
			}
			t.Run(name, func(t *testing.T) {
				built := compliance.New()
				factory := &gatedRetirementFactory{}
				set := moduleSet{compliance: built, all: []api.Module{built, factory}}
				profile, err := resolveModuleProfile(selected)
				if err != nil {
					t.Fatal(err)
				}
				if (set.running(profile).compliance == nil) != (name == "dormant") {
					t.Fatal("fixture has the wrong compliance lifecycle")
				}
				declared := declaredModules(set, nil)
				if len(declared) != 2 || factory.gate == nil || factory.calls != 0 {
					t.Fatalf("gated factory bypassed: declared=%d gate=%v generic calls=%d", len(declared), factory.gate, factory.calls)
				}
				// Boot registers the factory before binding data to every built module.
				built.UseData(api.NewModuleData(e.eng.store))
				held, holds, err := factory.gate.Check(context.Background(), e.tT, "user", user.String(), "transcripts")
				if err != nil || !held || len(holds) != 1 || holds[0].ID != id.String() || holds[0].MatterRef != "matter-factory" {
					t.Fatalf("factory's %s hold gate=%v %+v %v", name, held, holds, err)
				}
				adapter, ok := factory.gate.(complianceHoldGate)
				if !ok || adapter.m != set.compliance {
					t.Fatal("factory received a gate from a different compliance instance")
				}
			})
		}
		t.Run("legacy factory", func(t *testing.T) {
			legacy := &legacyRetirementFactory{}
			declared := declaredModules(moduleSet{all: []api.Module{legacy}}, nil)
			if len(declared) != 1 || legacy.calls != 1 {
				t.Fatalf("legacy factory changed: declared=%d calls=%d", len(declared), legacy.calls)
			}
		})
		t.Run("unavailable compliance refuses", func(t *testing.T) {
			factory := &gatedRetirementFactory{}
			declaredModules(moduleSet{all: []api.Module{factory}}, nil)
			if factory.gate == nil {
				t.Fatal("gated factory was not called")
			}
			if held, _, err := factory.gate.Check(context.Background(), e.tT, "user", user.String(), "transcripts"); err == nil {
				t.Fatalf("missing compliance admitted erasure: held=%v", held)
			}
		})
	})
}
