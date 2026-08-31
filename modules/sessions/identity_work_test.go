// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestSessionWorkParticipantSeparatesCouldNotLookFromNotEligible pins the third
// answer at the one place that had collapsed it. An unwired plane returned a
// not-eligible participant, which checkParticipant turns into owner_ineligible —
// reporting "I could not look" as "you are refused", against the contract
// written three lines above it.
func TestSessionWorkParticipantSeparatesCouldNotLookFromNotEligible(t *testing.T) {
	t.Parallel()

	unwired := New()
	got, err := unwired.SessionWorkParticipant(
		context.Background(), "t", model.NewID(), sidPrefix+model.NewID().String())
	if err == nil {
		t.Fatalf("an unwired plane answered %#v with no error, want evidence_unavailable", got)
	}
	if we := asWorkError(err); we == nil || we.verdict != VerdictUnknown || we.code != "evidence_unavailable" {
		t.Fatalf("unwired plane = %v, want UNKNOWN/evidence_unavailable", err)
	}

	// NO-FIRE: a wired plane asked about a string that is not a canonical sid has
	// MEASURED that it names no session, and answers not-eligible without error.
	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	notASID, err := f.m.SessionWorkParticipant(context.Background(), f.tenant, f.workspace, "not-a-sid")
	if err != nil {
		t.Fatalf("non-canonical ref = %v, want a measured not-eligible", err)
	}
	if notASID.Active || notASID.WorkspaceEligible {
		t.Fatalf("non-canonical ref resolved to something: %#v", notASID)
	}
}

// TestSessionActsForAgentRefusesAnAmbiguousOperatedAlias pins the guarantee the
// helper used to CLAIM and the catalog does not make.
//
// The UNIQUE index on (tenant, provider, external_id) gives external_id -> sid,
// not sid + provider -> external_id, and BindAlias exists to attach additional
// provider ids to one session. Two operated aliases are therefore constructible,
// and an unordered "take the first" would answer from whichever row came back —
// possibly true for the agent of the run that was NOT the driver.
func TestSessionActsForAgentRefusesAnAmbiguousOperatedAlias(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	ctx := context.Background()
	agentRef := model.NewID().String()
	runA, runB := model.NewID().String(), model.NewID().String()

	sid, err := f.m.ResolveSession(ctx, f.tenant, SessionBinding{
		Provider: ProviderOperated, ExternalID: runA, Origin: OriginOperated, At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("resolve operated sid: %v", err)
	}
	seedRunRow(t, f, runA, agentRef)

	acts, err := f.m.SessionActsForAgent(ctx, f.tenant, sid, agentRef)
	if err != nil || !acts {
		t.Fatalf("single operated alias = %v, %v; want true", acts, err)
	}

	// A SECOND operated alias for the same session, through the exported API that
	// legitimately allows extra provider ids.
	if err := f.m.BindAlias(ctx, f.tenant, sid, SessionBinding{
		Provider: ProviderOperated, ExternalID: runB, At: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("bind a second operated alias: %v", err)
	}
	seedRunRow(t, f, runB, model.NewID().String())

	acts, err = f.m.SessionActsForAgent(ctx, f.tenant, sid, agentRef)
	if acts {
		t.Fatal("an ambiguous operated alias answered TRUE for one of two candidates")
	}
	if we := asWorkError(err); we == nil || we.verdict != VerdictUnknown {
		t.Fatalf("ambiguous operated alias = %v, want UNKNOWN evidence", err)
	}
}

func seedRunRow(t *testing.T, f workFixture, runRef, agentRef string) {
	t.Helper()
	if err := f.m.data.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{
			colRunRef: runRef, colTransport: "stream-json", colPermissionMode: "default",
			colIsolation: "native", colState: stateRunning, colLastEventSeq: int64(0),
			colRunAgentRef: agentRef,
		})
		return err
	}); err != nil {
		t.Fatalf("seed run %s: %v", runRef, err)
	}
}
