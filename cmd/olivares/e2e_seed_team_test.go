// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/seed"
)

// TestE2E_Seed_TeamSessionsReadAsAWorkingDay feeds the demo seed through the real
// bus and reads the same live list the Sessions view and the dashboard's Active
// agents card read (#2268). The card prints the Working section of that list, so
// what is proved here is the list: ten named sessions of three engines, in
// working, idle, flagged and ended states, none of them a one-event row, and none
// adding an access to the unexpected-access list.
func TestE2E_Seed_TeamSessionsReadAsAWorkingDay(t *testing.T) {
	h := newHarness(t)
	team := seed.TeamSessions()
	want := map[string]seed.TeamSession{}
	for _, s := range team {
		want[s.Ref] = s
	}

	var rows map[string]map[string]any
	h.eventually("every team session is named, counted and attributed to its engine", 30*time.Second, func() error {
		page := h.getJSON(h.adminToken, h.tenantA, "/v1/m/sessions/live?limit=200")
		rows = map[string]map[string]any{}
		for _, it := range items(page) {
			if ref, _ := it["session_ref"].(string); want[ref].Ref != "" {
				rows[ref] = it
			}
		}
		for _, s := range team {
			r, ok := rows[s.Ref]
			if !ok {
				return fmt.Errorf("%s not in the live list yet (%d of %d)", s.Ref, len(rows), len(team))
			}
			if got, _ := r["summary"].(string); got != s.Name() {
				return fmt.Errorf("%s summary = %q, want %q", s.Ref, got, s.Name())
			}
			if got, _ := r["engine"].(string); got != s.Engine {
				return fmt.Errorf("%s engine = %q, want %q", s.Ref, got, s.Engine)
			}
			if got, _ := r["project"].(string); got != s.Project {
				return fmt.Errorf("%s project = %q, want %q", s.Ref, got, s.Project)
			}
			if got, _ := r["event_count"].(float64); int(got) < s.Events {
				return fmt.Errorf("%s event_count = %v, want at least %d", s.Ref, got, s.Events)
			}
		}
		return nil
	})

	states, counts := map[string]int{}, map[float64]bool{}
	for _, s := range team {
		r := rows[s.Ref]
		st, _ := r["cc_state"].(string)
		states[st]++
		n, _ := r["event_count"].(float64)
		counts[n] = true
		if n < 5 {
			t.Errorf("%s has %v events: a one-event row is the bulk this seed must not add", s.Ref, n)
		}
	}
	// Working is the figure the dashboard card prints (the console files exactly the
	// live rows that say active under Working, and the seed has no runs): three
	// sessions, last seen 20 to 45 seconds before the boot clock. The others make
	// the list read as a team's day and not as one state.
	if states["active"] != 3 {
		t.Errorf("%d working sessions, want 3: %v", states["active"], states)
	}
	if states["idle"] != 2 || states["ended"] != 4 || states["silent_evasion"] != 1 {
		t.Errorf("states do not span a working day (want 2 idle, 4 ended, 1 flagged): %v", states)
	}
	if len(counts) < len(team)-2 {
		t.Errorf("event counts barely vary: %v", counts)
	}

	// The team's tool calls name no resource, so they add no access: only the two
	// sessions the estate already had to show access appear as an origin in drift.
	drift := h.getJSON(h.adminToken, h.tenantA, "/v1/m/accessmap/drift?limit=500")
	sawSecrets := false
	for _, e := range items2(drift, "unexpected_accesses") {
		edge, _ := e["edge"].(map[string]any)
		if edge["resource_ref"] == seed.ResSecrets {
			sawSecrets = true
		}
		ref, _ := edge["origin_ref"].(string)
		if _, isTeam := want[ref]; isTeam && ref != seed.SessionLive && ref != seed.SessionEvade {
			t.Errorf("team session %s is an origin of an unexpected access: %v", ref, edge)
		}
	}
	if !sawSecrets {
		t.Errorf("the drift read did not answer with the estate's known unexpected access (%s): the check above proves nothing", seed.ResSecrets)
	}

	// The inventory catalogs each tool by name and holds no blank resource: a team
	// tool edge with no kind would have created one.
	tools := map[string]bool{}
	for _, it := range items(h.getJSON(h.adminToken, h.tenantA, "/v1/m/inventory/entities?kind=tool&limit=200")) {
		name, _ := it["name"].(string)
		tools[name] = true
	}
	for _, tool := range []string{"Read", "Edit", "shell", "apply_patch"} {
		if !tools[tool] {
			t.Errorf("tool %q is not in the inventory catalog: %v", tool, tools)
		}
	}
	for _, it := range items(h.getJSON(h.adminToken, h.tenantA, "/v1/m/inventory/entities?kind=resource&limit=200")) {
		if name, _ := it["name"].(string); name == "" {
			t.Errorf("inventory holds a resource with no name: %v", it)
		}
	}
}
