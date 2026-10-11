// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestStartRecoversWaitingLaunchesAfterRestart(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprintf("unreadable_question=%t", corrupt), func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			gate := &controlledLaunchApproval{}
			runner := &fakeRunner{initSID: "recovered-session"}
			options := []Option{WithSessionWorkspaceRoot(root), WithRunner(runner), WithCredentialSource(staticCred()), WithLaunchGate(gate)}
			original := New(options...)
			h := newHarness(t, original)
			admin := h.adminLogin()
			tenants := []model.TenantID{h.createOrg(admin, "first"), h.createOrg(admin, "second")}
			issuer := auth.NewAuthenticator(h.st, nil)
			original.QueuedCredentialCapture = issuer.BindQueuedCredential
			refs := make(map[model.TenantID][]string)
			for _, tenant := range tenants {
				for range 2 {
					response := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{"provider_profile_ref": ensureRuntimeTestProfileRef(t, h.m, tenant), "name": "restart approval"}, tenantHdr(tenant))
					if response.code != http.StatusAccepted || response.body["state"] != stateWaitingApproval {
						t.Fatalf("queue launch = %d %s", response.code, response.raw)
					}
					refs[tenant] = append(refs[tenant], response.body["run_ref"].(string))
				}
			}
			if err := original.Stop(ctx); err != nil {
				t.Fatal(err)
			}
			if got := launchCount(runner); got != 0 {
				t.Fatalf("unapproved launches = %d, want 0", got)
			}

			var damagedRef string
			const unreadable = "private question: invalid JSON"
			if corrupt {
				// Damage the first row in recovery order, so a valid row must be
				// recovered after it in the same tenant, regardless of generated IDs.
				if err := h.st.Mutate(ctx, tenants[0], func(sc store.Scope) error {
					repo, err := sc.Ext(runKind)
					if err != nil {
						return err
					}
					rows, _, err := repo.List(ctx, model.Query{Limit: 200, Filters: []model.Filter{eq(colState, stateWaitingApproval)}})
					if err != nil {
						return err
					}
					damagedRef = rows[0].String(colRunRef)
					rows[0][colRunQueuedIntent] = unreadable
					_, err = repo.Update(ctx, rows[0])
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}

			var logs bytes.Buffer
			restarted := New(options...)
			restarted.UseExecutionEnvironmentRef(original.rt.environmentRef)
			restarted.log = slog.New(slog.NewTextHandler(&logs, nil))
			restarted.UseData(api.NewModuleData(h.st))
			bindStoreStanding(restarted, h.st)
			restarted.QueuedLaunchAuthorization = func(ctx context.Context, _ model.TenantID, credential auth.QueuedCredential, _ string, _ model.ID) (auth.Principal, error) {
				return issuer.RevalidateQueuedCredential(ctx, credential)
			}
			restarted.ApprovalRecoveryTenants = func(context.Context) ([]model.TenantID, error) { return tenants, nil }
			stopModuleAtCleanup(t, restarted)
			if err := restarted.Start(ctx); err != nil {
				t.Fatalf("restart = %v", err)
			}
			gate.approved.Store(true)
			for _, tenant := range tenants {
				for _, ref := range refs[tenant] {
					if ref == damagedRef {
						continue
					}
					waitFor(t, "recovered launch "+ref, func() bool {
						run, err := restarted.getRun(ctx, tenant, ref)
						return err == nil && run.State == stateRunning
					})
				}
			}
			want := 4
			if corrupt {
				want--
				row, err := restarted.loadRun(ctx, tenants[0], damagedRef)
				if err != nil || row.String(colState) != stateWaitingApproval || row.String(colRunQueuedIntent) != unreadable {
					t.Fatalf("unreadable launch was changed: row=%v, err=%v", row, err)
				}
			}
			if got := launchCount(runner); got != want {
				t.Fatalf("launches after restart = %d, want %d", got, want)
			}
			if err := restarted.Stop(ctx); err != nil {
				t.Fatal(err)
			}
			if corrupt && (!strings.Contains(logs.String(), damagedRef) || !strings.Contains(logs.String(), tenants[0].String()) || strings.Contains(logs.String(), unreadable)) {
				t.Fatalf("recovery warning must identify the row without exposing its question: %s", logs.String())
			}
		})
	}
}

// Start does not depend on the recovery of launches that wait for an approval:
// when the composition cannot list its tenants (PostgreSQL without the admin
// pool refuses a cross-tenant read), Start still completes and the active
// kill-switch sweep still stops a running session.
func TestStartRunsTheKillSwitchSweepWhenWaitingLaunchesCannotBeRecovered(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate := &flipStopGate{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{initSID: "sess-start"}), WithCredentialSource(staticCred()),
		WithStopGate(gate), WithKillSwitchSweep(20*time.Millisecond))
	m.ApprovalRecoveryTenants = func(context.Context) ([]model.TenantID, error) {
		return nil, fmt.Errorf("%w: no admin pool", store.ErrEnumerationNotAuthoritative)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start with an unreadable tenant list = %v, want nil", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })

	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		WorkspaceRef: registerTestWorkspace(t, m, tenant, t.TempDir()), Actor: "agent:a1", ActorKind: "agent",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if dto.State != stateRunning {
		t.Fatalf("session should be running, got %s", dto.State)
	}
	gate.flip()
	waitFor(t, "running session terminated by the kill-switch sweep", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.State == stateStopped
	})
}
