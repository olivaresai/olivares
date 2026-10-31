// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestNativeHandshakeDoesNotHoldFirstOrResumedInput(t *testing.T) {
	for _, driver := range []string{providerDriverCodex, providerDriverOpenCode} {
		for _, resume := range []bool{false, true} {
			name := driver + "/fresh"
			if resume {
				name = driver + "/resumed"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				var m *Module
				var tenant model.TenantID
				var run runDTO
				var err error
				if driver == providerDriverCodex {
					mod, _, tid, prof := codexHarness(t, AuthSourceAccountHome)
					m, tenant = mod, tid
					setCodexFixture(t, prof, codexFixture{ThreadID: "thread-queue-resume", Account: "apikey"})
					run, err = codexLaunch(t, m, tenant, prof)
				} else {
					mod, _, tid, prof := openCodeHarness(t, AuthSourceAccountHome)
					m, tenant = mod, tid
					setOpenCodeFixture(t, prof, openCodeFixture{SessionID: "session-queue-resume", PromptMode: "never"})
					run, err = openCodeLaunch(t, m, tenant, prof)
				}
				if err != nil {
					t.Fatal(err)
				}
				ref := run.RunRef
				t.Cleanup(func() { _, _ = m.stopRun(ctx, tenant, ref, "user:u1", model.ActorUser) })
				if resume {
					if _, err := m.stopRun(ctx, tenant, ref, "user:u1", model.ActorUser); err != nil {
						t.Fatal(err)
					}
					if _, err := m.resumeRun(ctx, tenant, ref, "user:u1", model.ActorUser, ""); err != nil {
						t.Fatal(err)
					}
				}
				rec, err := m.loadRun(ctx, tenant, ref)
				if err != nil {
					t.Fatal(err)
				}
				live, ok := m.rt.getLive(tenant, ref)
				if !ok || live.session.ActiveTurn() != "" {
					t.Fatal("fixture must have no active turn after handshake")
				}
				if hold := m.queueHold(rec); hold != "" {
					t.Fatalf("handshake holds the first message: %q, want no hold", hold)
				}
				if err := m.sendTextInput(ctx, tenant, ref, "continue after handshake"); err != nil {
					t.Fatal(err)
				}
				if hold := m.queueHold(rec); hold != queueHoldTurn {
					t.Fatalf("accepted active input hold = %q, want turn", hold)
				}
			})
		}
	}
}
