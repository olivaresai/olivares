// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/store"
)

// A launch that names a managed credential asks tenant administration, once, over the real
// create handler: a caller who may not is refused before the broker is asked, and one who
// may gets the helpers.
func TestCreateAsksTenantAdministrationForCredentials(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		allow    bool
		status   int
		brokered int
	}{
		"a member":               {allow: false, status: http.StatusForbidden, brokered: 0},
		"a tenant administrator": {allow: true, status: http.StatusCreated, brokered: 1},
	} {
		t.Run(name, func(t *testing.T) {
			m, tenant, _, az := callerQuestionsHarness(t, map[auth.Permission]bool{permTenantAdmin: tc.allow})
			broker := newFakeCredentialBroker(t)
			m.CredentialBroker = broker
			requireStatus(t, postRun(t, m, tenant, map[string]any{"permission_mode": "default", "credentials": []string{"prod-db"}}), tc.status)
			if got := az.count(permTenantAdmin); got != 1 {
				t.Fatalf("tenant administration asked %d times, want once", got)
			}
			if got := len(broker.requests()); got != tc.brokered {
				t.Fatalf("the broker was asked %d times, want %d", got, tc.brokered)
			}
		})
	}
}

// A run row whose credentials cannot be read never resumes without them: the session asked
// for helpers, so it starts with them or not at all. A value that is there but names
// nothing is as damaged as one that is not JSON.
func TestCredentials_AResumeWithAnUnreadableStoredValueStartsNothing(t *testing.T) {
	t.Parallel()
	for name, stored := range map[string]string{"not JSON": "{not json", "an empty list": "[]", "null": "null"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			m, tenant, fr, broker := credentialHarness(t)
			dto, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m.stopRun(ctx, tenant, dto.RunRef, actorU, actorKindU); err != nil {
				t.Fatal(err)
			}
			broker.awaitRelease(t)
			if err := m.Data.Mutate(ctx, tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(runKind)
				if err != nil {
					return err
				}
				rec, err := findRunRec(ctx, repo, dto.RunRef)
				if err != nil {
					return err
				}
				rec[colRunCredentials] = stored
				_, err = repo.Update(ctx, rec)
				return err
			}); err != nil {
				t.Fatal(err)
			}

			launches := launchCount(fr)
			if _, err := m.resumeRunAs(ctx, tenant, dto.RunRef, actorU, actorKindU, "", true); err == nil {
				t.Fatalf("a run whose stored credentials are %s resumed", name)
			}
			if got := len(broker.requests()); got != 1 {
				t.Fatalf("the broker was asked %d times, want only the first launch's", got)
			}
			if launchCount(fr) != launches {
				t.Fatal("a resume without its credentials started a process")
			}
		})
	}
}

// The credential's promise is that the child cannot reach the engine's memory or the other
// runs' helpers, and only confinement keeps that: a launch that names a credential must not
// start on a host that cannot confine it, and one that names none is left as it was.
func TestCredentials_ASessionHoldingACredentialRequiresConfinement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, _ := credentialHarness(t)

	with := credentialParams(true)
	if _, err := createProfiledTestRun(t, m, ctx, tenant, with); err != nil {
		t.Fatal(err)
	}
	if !fr.lastSpec().ConfinementRequired {
		t.Fatal("a session given a credential may start where the host cannot confine it")
	}

	fr.initSID = "sess-without-credential" // separate runs under one profile need distinct provider session IDs
	without := credentialParams(true)
	without.Credentials = nil
	if _, err := createProfiledTestRun(t, m, ctx, tenant, without); err != nil {
		t.Fatal(err)
	}
	if fr.lastSpec().ConfinementRequired {
		t.Fatal("a session with no credential now requires confinement")
	}
}
