// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// A work launch runs as the agent that owns the work item, so that agent's tool and profile
// assignments decide what it may run on, as a person's decide for POST /runs: for a profile the
// step names and for the default alike, deny-closed.

// agentAssignments answers as stored assignments do for agents: a "tool:<driver>" entry or a
// profile reference in to is assigned to the one agent named there and limited to it; anything
// else is open. It keeps the agent each question was asked for.
type agentAssignments struct {
	mu    sync.Mutex
	to    map[string]string
	asked []string
}

func (a *agentAssignments) MayUse(_ context.Context, _ model.TenantID, p auth.Principal, profileRef, driver string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asked = append(a.asked, p.AgentIdentity)
	for _, key := range []string{"tool:" + driver, profileRef} {
		if agent, ok := a.to[key]; ok && agent != p.AgentIdentity {
			return false, nil
		}
	}
	return true, nil
}

func (a *agentAssignments) Limited(_ context.Context, _ model.TenantID, _ auth.Principal, profileRef, driver string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, tool := a.to["tool:"+driver]
	_, profile := a.to[profileRef]
	return tool || profile, nil
}

func (a *agentAssignments) askedFor() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.asked...)
}

func profileCount(t *testing.T, m *Module, tenant model.TenantID) int {
	t.Helper()
	profiles, _, err := m.ListProfiles(context.Background(), tenant, "", model.Query{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	return len(profiles)
}

// askedOnlyFor fails unless the assignments were asked, and only for agent.
func askedOnlyFor(t *testing.T, access *agentAssignments, agent string) {
	t.Helper()
	asked := access.askedFor()
	if len(asked) == 0 {
		t.Fatal("no assignment was asked")
	}
	for _, a := range asked {
		if a != agent {
			t.Fatalf("assignment asked for %q, want only the owner agent %q (asked: %q)", a, agent, asked)
		}
	}
}

func TestLaunchForWorkWithoutAProviderProfileAsksTheOwnerAgentsAssignment(t *testing.T) {
	// A blank ref and a whitespace-only one both name no profile.
	for _, ref := range []string{"", "   "} {
		t.Run("tool assigned to another agent/ref="+ref, func(t *testing.T) {
			m, st, tenant, runner, _ := defaultProfileWorkHarness(t, true)
			itemID, _, agentRef := readyWorkLaunchItem(t, m, st, tenant)
			access := &agentAssignments{to: map[string]string{"tool:claude": "agent:someone-else"}}
			m.rt.ProfileAccess = access

			var control RuntimeControl = m
			_, err := control.LaunchForWork(context.Background(), tenant, workLaunchSpec(t, m, tenant, itemID, agentRef, ref))
			if !errors.Is(err, ErrProfileNotAssigned) {
				t.Fatalf("LaunchForWork with the tool assigned to another agent = %v, want ErrProfileNotAssigned", err)
			}
			if n := len(runner.specs); n != 0 {
				t.Fatalf("runner launches = %d, want none for a refused launch", n)
			}
			if n := profileCount(t, m, tenant); n != 0 {
				t.Fatalf("%d profiles after the refused launch, want none made", n)
			}
			askedOnlyFor(t, access, agentRef)
		})
		t.Run("tool assigned to the owner agent/ref="+ref, func(t *testing.T) {
			m, st, tenant, runner, _ := defaultProfileWorkHarness(t, true)
			itemID, _, agentRef := readyWorkLaunchItem(t, m, st, tenant)
			// Limited to the owner: asked for anyone else (no agent, the caller), it would refuse.
			access := &agentAssignments{to: map[string]string{"tool:claude": agentRef}}
			m.rt.ProfileAccess = access

			var control RuntimeControl = m
			managed, err := control.LaunchForWork(context.Background(), tenant, workLaunchSpec(t, m, tenant, itemID, agentRef, ref))
			if err != nil || managed.State != stateRunning {
				t.Fatalf("LaunchForWork with the tool assigned to the owner agent = %#v, %v; want it running", managed, err)
			}
			if n := len(runner.specs); n != 1 {
				t.Fatalf("runner launches = %d, want 1", n)
			}
			askedOnlyFor(t, access, agentRef)
		})
	}
}

// A profile the step names is asked too: the agent must be assigned it.
func TestLaunchForWorkWithANamedProfileAsksTheOwnerAgentsAssignment(t *testing.T) {
	m, st, tenant, runner, _ := defaultProfileWorkHarness(t, true)
	itemID, _, agentRef := readyWorkLaunchItem(t, m, st, tenant)
	named := ensureRuntimeTestProfileRef(t, m, tenant)
	access := &agentAssignments{to: map[string]string{named: "agent:someone-else"}}
	m.rt.ProfileAccess = access

	var control RuntimeControl = m
	_, err := control.LaunchForWork(context.Background(), tenant, workLaunchSpec(t, m, tenant, itemID, agentRef, named))
	if !errors.Is(err, ErrProfileNotAssigned) {
		t.Fatalf("LaunchForWork with a profile assigned to another agent = %v, want ErrProfileNotAssigned", err)
	}
	if n := len(runner.specs); n != 0 {
		t.Fatalf("runner launches = %d, want none for a refused launch", n)
	}
	askedOnlyFor(t, access, agentRef)

	// Assigned to the owner agent, the same named profile launches.
	access = &agentAssignments{to: map[string]string{named: agentRef}}
	m.rt.ProfileAccess = access
	managed, err := control.LaunchForWork(context.Background(), tenant, workLaunchSpec(t, m, tenant, itemID, agentRef, named))
	if err != nil || managed.State != stateRunning {
		t.Fatalf("LaunchForWork with a profile assigned to the owner agent = %#v, %v; want it running", managed, err)
	}
	askedOnlyFor(t, access, agentRef)
}

// The default would run on a key whose only profile is assigned to another agent: the key is
// not the owner agent's, so the launch is refused before the default is taken or a profile made.
func TestLaunchForWorkWithoutAProviderProfileRefusesAKeyThatIsNotTheAgents(t *testing.T) {
	m, st, tenant, runner, _ := defaultProfileWorkHarness(t, false) // not signed in: the rule picks the key
	itemID, _, agentRef := readyWorkLaunchItem(t, m, st, tenant)
	addRecord(t, m, tenant, ProviderKindAnthropic, "Finance key", "", "sk-ant-fixture-0123456789")
	onKey, err := m.ResolveProfile(context.Background(), tenant, providerDriverClaude)
	if err != nil || !onKey.Created || onKey.Provider == nil {
		t.Fatalf("profile on the key = %+v, %v; want one made on the key", onKey, err)
	}
	access := &agentAssignments{to: map[string]string{onKey.Profile.Ref: "agent:finance-bot"}}
	m.rt.ProfileAccess = access

	var control RuntimeControl = m
	_, err = control.LaunchForWork(context.Background(), tenant, workLaunchSpec(t, m, tenant, itemID, agentRef, ""))
	if !errors.Is(err, ErrProfileNotAssigned) {
		t.Fatalf("LaunchForWork on a key that is not the agent's = %v, want ErrProfileNotAssigned", err)
	}
	askedOnlyFor(t, access, agentRef)
	if n := len(runner.specs); n != 0 {
		t.Fatalf("runner launches = %d, want none for a refused launch", n)
	}
	if n := profileCount(t, m, tenant); n != 1 {
		t.Fatalf("%d profiles after the refused launch, want only the one on the key", n)
	}
}

// A replay is looked up before the default is resolved: the dispatch that already launched
// keeps its profile, so a default that can no longer be resolved does not fail the step while
// its session runs.
func TestLaunchForWorkReplayWithoutAProviderProfileKeepsTheFirstRunsProfile(t *testing.T) {
	m, st, tenant, runner, stub := defaultProfileWorkHarness(t, true)
	itemID, _, agentRef := readyWorkLaunchItem(t, m, st, tenant)
	spec := workLaunchSpec(t, m, tenant, itemID, agentRef, "")

	var control RuntimeControl = m
	first, err := control.LaunchForWork(context.Background(), tenant, spec)
	if err != nil || first.State != stateRunning {
		t.Fatalf("first launch = %#v, %v", first, err)
	}
	stub.mu.Lock()
	stub.installed["claude"] = false
	stub.mu.Unlock()
	if _, err := m.ResolveProfile(context.Background(), tenant, providerDriverClaude); err == nil {
		t.Fatal("the default still resolves; the test needs a default that cannot be resolved")
	}

	replayed, err := control.LaunchForWork(context.Background(), tenant, spec)
	if err != nil || !replayed.Replayed || replayed.RunRef != first.RunRef {
		t.Fatalf("replay after the default changed = %#v, %v; want the first run back", replayed, err)
	}
	if n := len(runner.specs); n != 1 {
		t.Fatalf("runner launches = %d, want 1", n)
	}
}

// The assignments were asked for the owner read before the launch; a reservation for another
// owner (the item was reassigned in between) is another launch and is refused.
func TestPrepareWorkLaunchRefusesAnOwnerOtherThanTheOneAsked(t *testing.T) {
	// Another agent, or none asked at all: either is not the owner the assignments were asked for.
	for _, asked := range []string{"agent:someone-else", ""} {
		t.Run("asked="+asked, func(t *testing.T) {
			m, st, tenant, runner, _ := defaultProfileWorkHarness(t, true)
			itemID, _, agentRef := readyWorkLaunchItem(t, m, st, tenant)
			spec := workLaunchSpec(t, m, tenant, itemID, agentRef, ensureRuntimeTestProfileRef(t, m, tenant))
			spec.askedAgent = asked

			_, _, _, err := m.prepareWorkLaunch(context.Background(), tenant, spec, &spec.Runtime)
			if we := asWorkError(err); we == nil || we.code != "dispatch_conflict" {
				t.Fatalf("reservation for an owner other than the one asked = %v, want dispatch_conflict", err)
			}
			if n := len(runner.specs); n != 0 {
				t.Fatalf("runner launches = %d, want none", n)
			}
		})
	}
}

// The key's only profile is assigned to another agent and disabled, so the rule would make a
// new profile on the key: that shares a key an assignment limits, which only a profile
// administrator may do. The owner agent is none, so nothing is made and nothing spawned.
func TestLaunchForWorkWithoutAProviderProfileDoesNotShareALimitedKey(t *testing.T) {
	m, st, tenant, runner, _ := defaultProfileWorkHarness(t, false) // not signed in: the rule picks the key
	itemID, _, agentRef := readyWorkLaunchItem(t, m, st, tenant)
	addRecord(t, m, tenant, ProviderKindAnthropic, "Finance key", "", "sk-ant-fixture-0123456789")
	onKey, err := m.ResolveProfile(context.Background(), tenant, providerDriverClaude)
	if err != nil || !onKey.Created || onKey.Provider == nil {
		t.Fatalf("profile on the key = %+v, %v; want one made on the key", onKey, err)
	}
	disabled := ProfileDisabled
	if _, err := m.PatchProfile(context.Background(), tenant, onKey.Profile.Ref, ProfilePatch{State: &disabled}); err != nil {
		t.Fatal(err)
	}
	m.rt.ProfileAccess = &agentAssignments{to: map[string]string{onKey.Profile.Ref: "agent:finance-bot"}}

	var control RuntimeControl = m
	_, err = control.LaunchForWork(context.Background(), tenant, workLaunchSpec(t, m, tenant, itemID, agentRef, ""))
	if !errors.Is(err, errKeyLimited) {
		t.Fatalf("LaunchForWork that would share a limited key = %v, want errKeyLimited", err)
	}
	if n := len(runner.specs); n != 0 {
		t.Fatalf("runner launches = %d, want none for a refused launch", n)
	}
	if n := profileCount(t, m, tenant); n != 1 {
		t.Fatalf("%d profiles after the refused launch, want only the disabled one", n)
	}
}

// An assignment that cannot be read refuses the launch (503), never runs it.
func TestLaunchForWorkWithAnUnreadableAssignmentSpawnsNothing(t *testing.T) {
	m, st, tenant, runner, _ := defaultProfileWorkHarness(t, true)
	itemID, _, agentRef := readyWorkLaunchItem(t, m, st, tenant)
	m.rt.ProfileAccess = &fakeProfileAccess{err: errors.New("assignment store unavailable")}

	var control RuntimeControl = m
	_, err := control.LaunchForWork(context.Background(), tenant, workLaunchSpec(t, m, tenant, itemID, agentRef, ""))
	if got := statusOf(err); got != http.StatusServiceUnavailable {
		t.Fatalf("LaunchForWork with an unreadable assignment = %v (status %d), want 503", err, got)
	}
	if n := len(runner.specs); n != 0 {
		t.Fatalf("runner launches = %d, want none", n)
	}
}

// A malformed launch is refused as before, ahead of any read.
func TestLaunchForWorkRefusesAMalformedLaunchBeforeReading(t *testing.T) {
	m, st, tenant, _, _ := defaultProfileWorkHarness(t, true)
	itemID, _, agentRef := readyWorkLaunchItem(t, m, st, tenant)
	spec := workLaunchSpec(t, m, tenant, itemID, agentRef, "")
	spec.WorkItemID = model.ID("")

	var control RuntimeControl = m
	_, err := control.LaunchForWork(context.Background(), tenant, spec)
	if we := asWorkError(err); we == nil || we.code != "invalid_command" {
		t.Fatalf("LaunchForWork with no work item = %v, want invalid_command", err)
	}
}
