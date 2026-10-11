// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// The runner seam reports the actual capability, as procProcess does. Kernel
// enforcement and the ABI query are exercised by confine's isolated helpers.
type abiReportingRunner struct {
	fakeRunner
	state confine.State
}

func (r *abiReportingRunner) Launch(ctx context.Context, spec LaunchSpec) (Process, error) {
	p, err := r.fakeRunner.Launch(ctx, spec)
	if err != nil {
		return p, err
	}
	return abiReportingProcess{Process: p, state: r.state}, nil
}

type abiReportingProcess struct {
	Process
	state confine.State
}

func (p abiReportingProcess) Confinement() confine.State { return p.state }

func TestReadOnlyDefaultStatesTheTruncationLimitInRunAndAudit(t *testing.T) {
	for _, abi := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(abi), func(t *testing.T) {
			reason := ""
			if abi < 3 {
				reason = "this kernel cannot block truncation of existing files"
			}
			r := &abiReportingRunner{state: confine.State{Mode: confine.ModeLandlock, ABI: abi, Reason: reason}}
			m, st, tenant, _ := newRuntimeHarness(t, WithRunner(r), WithCredentialSource(staticCred()), WithConfinement([]string{t.TempDir()}, false))
			run, err := createProfiledTestRun(t, m, t.Context(), tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, PermissionMode: "plan", Actor: actorU, ActorKind: actorKindU})
			if err != nil {
				t.Fatal(err)
			}
			check := func(event string) {
				t.Helper()
				stored, err := m.getRun(t.Context(), tenant, run.RunRef)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(stored.Reason, "landlock") || (reason != "" && !strings.Contains(stored.Reason, reason)) {
					t.Fatalf("run hid confinement: %+v", stored)
				}
				var rows []model.Record
				if err := st.View(t.Context(), tenant, func(sc store.Scope) error {
					repo, err := sc.Ext(runEventKind)
					if err != nil {
						return err
					}
					rows, _, err = repo.List(t.Context(), model.Query{Filters: []model.Filter{{Column: colEvRunRef, Op: model.OpEq, Value: run.RunRef}, {Column: colEvEvent, Op: model.OpEq, Value: event}}})
					return err
				}); err != nil {
					t.Fatal(err)
				}
				if len(rows) != 1 {
					t.Fatalf("%s event count=%d", event, len(rows))
				}
				detail := rows[0].String(colEvDetail)
				if reason != "" && !strings.Contains(detail, reason) {
					t.Fatalf("event hid limitation: %q", detail)
				}
				_, meta := verifiedAuditAnchor(t, st, tenant, rows[0].Int(colEvAuditSeq))
				state, ok := meta["confinement"].(map[string]any)
				if !ok || state["mode"] != string(confine.ModeLandlock) || state["abi"] != float64(abi) || state["reason"] != reason {
					t.Fatalf("audit hid confinement: %v", meta)
				}
			}
			check("launched")
			if _, err := m.stopRun(t.Context(), tenant, run.RunRef, actorU, actorKindU); err != nil {
				t.Fatal(err)
			}
			if _, err := m.resumeRun(t.Context(), tenant, run.RunRef, actorU, actorKindU, ""); err != nil {
				t.Fatal(err)
			}
			check("resumed")
		})
	}
}

func TestReadOnlyTemplateReloadsTheStrictTruncationChoice(t *testing.T) {
	r := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(r), WithCredentialSource(staticCred()), WithConfinement([]string{t.TempDir()}, false))
	var strict tplBody
	if err := json.Unmarshal([]byte(`{"settings":{"permission_mode":"plan"},"policies":{"require_truncate_protection":true}}`), &strict); err != nil {
		t.Fatal(err)
	}
	id := seedTemplate(t, m, tenant, "Read only with truncation protection", strict)
	run, err := createProfiledTestRun(t, m, t.Context(), tenant, CreateRunParams{TemplateID: id, Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU})
	if err != nil {
		t.Fatal(err)
	}
	check := func(want bool) {
		t.Helper()
		policy := r.lastSpec().Confinement
		if policy == nil || len(policy.Sealed) != 1 {
			t.Fatalf("no sealed launch policy: %+v", policy)
		}
		if r.lastSpec().ConfinementRequireTruncateProtection != want {
			t.Fatalf("strict choice=%+v want=%v", policy, want)
		}
	}
	check(true)
	if _, err := m.stopRun(t.Context(), tenant, run.RunRef, actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	retermTemplate(t, m, tenant, id, tplBody{Settings: &tplSettings{PermissionMode: "plan"}})
	if _, err := m.resumeRun(t.Context(), tenant, run.RunRef, actorU, actorKindU, ""); err != nil {
		t.Fatal(err)
	}
	check(false)
}
