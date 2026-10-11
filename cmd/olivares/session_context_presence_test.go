// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/knowledge"
	"github.com/olivaresai/olivares/modules/sessions"
)

type launchContextPresence struct {
	configured                bool
	presenceErr               error
	presenceCalls, applyCalls int
	pol                       knowledge.EffectivePolicy
}

func (p *launchContextPresence) HasContextPolicies(context.Context, model.TenantID) (bool, error) {
	p.presenceCalls++
	return p.configured, p.presenceErr
}

func (p *launchContextPresence) Apply(context.Context, model.TenantID, knowledge.ContextPolicyQuery) (knowledge.EffectivePolicy, error) {
	p.applyCalls++
	return p.pol, nil
}

func TestSessionLaunchGateAppliesContextOnlyWhenTenantHasPolicy(t *testing.T) {
	for _, configured := range []bool{false, true} {
		name := "no-policy"
		if configured {
			name = "configured-policy"
		}
		t.Run(name, func(t *testing.T) {
			p := &launchContextPresence{configured: configured, pol: knowledge.EffectivePolicy{MaxContextTokens: 2048, Strategy: "summarize", WinningScope: "tenant:t1"}}
			gate := sessionLaunchGate{contextPolicy: p}
			dec, err := gate.Authorize(t.Context(), "t1", sessions.LaunchIntent{PermissionMode: "default"})
			if err != nil || !dec.Allowed {
				t.Fatalf("launch: %+v err=%v", dec, err)
			}
			wantCalls := 0
			if configured {
				wantCalls = 1
			}
			if p.applyCalls != wantCalls {
				t.Fatalf("context Apply called %d times, want %d for configured=%v", p.applyCalls, wantCalls, configured)
			}
			if p.presenceCalls != 1 {
				t.Fatalf("tenant presence checks=%d, want 1", p.presenceCalls)
			}
			if !configured {
				if len(dec.InjectEnv) != 0 || dec.ContextPolicySummary != "" {
					t.Fatalf("no policy injected context or retained a summary: %+v", dec)
				}
				return
			}
			if dec.ContextPolicySummary != "ctx max=2048 strategy=summarize scope=tenant:t1" {
				t.Fatalf("configured policy summary: %q", dec.ContextPolicySummary)
			}
			if len(dec.InjectEnv) != 2 || dec.InjectEnv[0].Name != envContextMaxTokens || dec.InjectEnv[0].Value != "2048" || dec.InjectEnv[1].Name != envContextStrategy || dec.InjectEnv[1].Value != "summarize" {
				t.Fatalf("configured policy injection: %+v", dec.InjectEnv)
			}
		})
	}
}

func TestSessionLaunchContextPresenceErrorsFollowAvailabilityPosture(t *testing.T) {
	for _, posture := range []availabilityPosture{availabilityFailOpen, availabilityFailClosed} {
		t.Run(posture.String(), func(t *testing.T) {
			p := &launchContextPresence{presenceErr: errors.New("context policy store unavailable")}
			gate := sessionLaunchGate{contextPolicy: p, contextPosture: posture}
			dec, err := gate.Authorize(t.Context(), "t1", sessions.LaunchIntent{PermissionMode: "default"})
			if err != nil {
				t.Fatal(err)
			}
			if dec.Allowed != (posture == availabilityFailOpen) || p.applyCalls != 0 || dec.ContextPolicySummary != "" || len(dec.InjectEnv) != 0 {
				t.Fatalf("presence failure changed launch posture or invoked Apply: %+v calls %d", dec, p.applyCalls)
			}
		})
	}
}
