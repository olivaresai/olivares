// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestListRuns_StateAndAccountAreStoreFilters drives GET /runs over more rows than
// one page. The runs that answer the questions are the OLDEST: a dispatcher that
// counts running sessions per account asks exactly this, and a long-running session
// is always older than the newest page.
func TestListRuns_StateAndAccountAreStoreFilters(t *testing.T) {
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	const (
		accountA  = "ppf_01J8AAAAAAAAAAAAAAAAAAAAAA"
		accountB  = "ppf_01J8BBBBBBBBBBBBBBBBBBBBBB"
		quietRuns = 1100 // stored running, silent past the idle window, newer than the five: more than the store's largest page
		newer     = 596  // stopped runs created last
	)
	recent := model.NewTimestamp(m.now()).String()
	quiet := model.NewTimestamp(m.now().Add(-time.Hour)).String()
	waitingLaunch := model.NewID()
	seed := func(sc store.Scope, ref, state, profile, activity string, launch model.ID) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec := model.Record{
			colRunRef: ref, colTransport: "stream-json", colPermissionMode: "default",
			colIsolation: "native", colState: state, colLastEventSeq: int64(0),
			colRunProfileID: profile,
		}
		if activity != "" {
			rec[colLastActivityAt] = activity
		}
		if launch != "" {
			rec[colRuntimeLaunchID] = launch.String()
		}
		_, err = repo.Create(context.Background(), rec)
		return err
	}
	if err := m.Data.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		for _, r := range []struct {
			ref, state, profile, activity string
			launch                        model.ID
		}{
			{"run-old-a1", stateRunning, accountA, recent, ""},
			{"run-old-a2", stateRunning, accountA, recent, ""},
			{"run-old-b1", stateRunning, accountB, recent, ""},
			// stored running, silent past the idle window: displayed idle
			{"run-quiet-a", stateRunning, accountA, quiet, ""},
			// stored running, recent activity, its live handle waits for the next input: displayed idle
			{"run-waiting-a", stateRunning, accountA, recent, waitingLaunch},
		} {
			if err := seed(sc, r.ref, r.state, r.profile, r.activity, r.launch); err != nil {
				return err
			}
		}
		for i := 0; i < quietRuns; i++ {
			if err := seed(sc, fmt.Sprintf("run-idle-%03d", i), stateRunning, accountB, quiet, ""); err != nil {
				return err
			}
		}
		for i := 0; i < newer; i++ {
			profile := accountA
			if i%2 == 1 {
				profile = accountB
			}
			if err := seed(sc, fmt.Sprintf("run-new-%03d", i), stateStopped, profile, "", ""); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	m.rt.mu.Lock()
	m.rt.live[liveKey(tenant, "run-waiting-a")] = &liveRun{
		tenant: tenant, runRef: "run-waiting-a", launchID: waitingLaunch, turnInterrupted: true,
	}
	m.rt.mu.Unlock()
	t.Cleanup(func() { // this handle owns no process; module shutdown must not stop it
		m.rt.mu.Lock()
		delete(m.rt.live, liveKey(tenant, "run-waiting-a"))
		m.rt.mu.Unlock()
	})

	// page is one request: the refs in the order the engine answered, and has_more.
	page := func(query string) (got []string, hasMore bool, cursor string) {
		t.Helper()
		r := h.do("GET", "/v1/m/sessions/runs?"+query, admin, tenantHdr(tenant))
		if r.code != http.StatusOK {
			t.Fatalf("GET runs?%s = %d %s", query, r.code, r.raw)
		}
		items, ok := r.body["items"].([]any)
		if !ok {
			t.Fatalf("GET runs?%s: items is %T, want a list", query, r.body["items"])
		}
		for _, it := range items {
			row, _ := it.(map[string]any)
			ref, _ := row["run_ref"].(string)
			got = append(got, ref)
		}
		hasMore, _ = r.body["has_more"].(bool)
		cursor, _ = r.body["cursor"].(string)
		return got, hasMore, cursor
	}
	// refs reads the list the way a client does: one request, or the whole cursor
	// chain, sorted so a set can be compared.
	refs := func(query string, followCursor bool) (got []string, hasMore bool) {
		t.Helper()
		q := query
		for n := 0; n < 1000; n++ {
			items, more, cursor := page(q)
			got, hasMore = append(got, items...), more
			if !followCursor || !more || cursor == "" {
				break
			}
			q = query + "&cursor=" + url.QueryEscape(cursor)
		}
		sort.Strings(got)
		return got, hasMore
	}
	idleRefs := []string{"run-quiet-a", "run-waiting-a"}
	for i := 0; i < quietRuns; i++ {
		idleRefs = append(idleRefs, fmt.Sprintf("run-idle-%03d", i))
	}
	same := func(got []string, want ...string) bool {
		sort.Strings(want)
		return strings.Join(got, ",") == strings.Join(want, ",")
	}

	for _, mode := range []struct {
		name, suffix string
		cursor       bool
	}{{"recency page", "", false}, {"cursor mode", "&pagination=cursor", true}} {
		t.Run(mode.name, func(t *testing.T) {
			if got, _ := refs("state=running&limit=1000"+mode.suffix, mode.cursor); !same(got, "run-old-a1", "run-old-a2", "run-old-b1") {
				t.Errorf("state=running = %v, want the three displayed-running runs although %d newer rows exist", got, quietRuns+newer)
			}
			if got, _ := refs("state=running&provider_profile_ref="+accountA+mode.suffix, mode.cursor); !same(got, "run-old-a1", "run-old-a2") {
				t.Errorf("state=running for account A = %v, want [run-old-a1 run-old-a2]", got)
			}
			if got, _ := refs("state=running&provider_profile_ref=ppf_01J8UNKNOWN"+mode.suffix, mode.cursor); len(got) != 0 {
				t.Errorf("state=running for an unknown account = %v, want none", got)
			}
			if got, _ := refs("state=idle&provider_profile_ref="+accountA+"&limit=1000"+mode.suffix, mode.cursor); !same(got, "run-quiet-a", "run-waiting-a") {
				t.Errorf("state=idle for account A = %v, want [run-quiet-a run-waiting-a]", got)
			}
			if got, more := refs("state=stopped&limit=10"+mode.suffix, false); len(got) != 10 || !more {
				t.Errorf("state=stopped&limit=10 = %d rows has_more=%v, want a full page with more", len(got), more)
			}
			if got, _ := refs("state=stopped&provider_profile_ref="+accountB+"&limit=1000"+mode.suffix, false); len(got) != newer/2 {
				t.Errorf("stopped runs of account B = %d, want %d", len(got), newer/2)
			}
		})
	}

	// The recency page: newest first, cut at the limit, with has_more saying so. A
	// derived state is cut after it is derived, never before.
	if got, more, _ := page("state=running&limit=2"); strings.Join(got, ",") != "run-old-b1,run-old-a2" || !more {
		t.Errorf("state=running&limit=2 = %v has_more=%v, want the two newest [run-old-b1 run-old-a2] and more", got, more)
	}
	if got, more, _ := page("state=running&limit=3"); len(got) != 3 || more {
		t.Errorf("state=running&limit=3 = %v has_more=%v, want all three and no more", got, more)
	}
	if got, more, _ := page("state=idle&limit=1"); strings.Join(got, ",") != "run-idle-1099" || !more {
		t.Errorf("state=idle&limit=1 = %v has_more=%v, want the newest idle run [run-idle-1099] and more", got, more)
	}
	if got, more, _ := page("state=idle"); len(got) != 100 || !more {
		t.Errorf("state=idle with no limit = %d rows has_more=%v, want the default page of 100 and more", len(got), more)
	}
	// More idle runs than the store's largest page: the recency page is cut at the
	// limit the store would clamp to, and has_more says the walk saw more than that.
	if got, more, _ := page("state=idle&limit=2000"); len(got) != 1000 || !more {
		t.Errorf("state=idle&limit=2000 = %d rows has_more=%v, want the store's largest page of 1000 and more", len(got), more)
	}
	// An account nobody holds is a list, empty, not an error and not a null.
	if got, more, _ := page("provider_profile_ref=ppf_01J8UNKNOWN"); got != nil || more {
		t.Errorf("unknown account = %v has_more=%v, want an empty list", got, more)
	}

	// Cursor mode walks a derived state in short pages: each of the 1,102 idle runs
	// once, however many of the pages hold none.
	var walked []string
	query := "state=idle&limit=7&pagination=cursor"
	for n := 0; n < 1000; n++ {
		got, more, cursor := page(query)
		walked = append(walked, got...)
		if !more {
			break
		}
		if cursor == "" {
			t.Fatalf("has_more without a cursor after %d rows", len(walked))
		}
		query = "state=idle&limit=7&pagination=cursor&cursor=" + url.QueryEscape(cursor)
	}
	sort.Strings(walked)
	if !same(walked, idleRefs...) {
		t.Errorf("cursor walk of state=idle = %d rows, want each of the %d idle runs exactly once", len(walked), len(idleRefs))
	}
}
