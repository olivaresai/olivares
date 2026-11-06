// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// TestSessionUsageReadsTheEnginesAggregate: `session usage --by tool --since 7d -o json`
// asks the engine's GET /usage for exactly that and prints the answer untouched, so a sum
// nobody reported stays null. The table says "-" for it, never 0.
func TestSessionUsageReadsTheEnginesAggregate(t *testing.T) {
	var mu sync.Mutex
	var queries []url.Values
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case sessionUsagePath:
			mu.Lock()
			queries = append(queries, r.URL.Query())
			mu.Unlock()
			_, _ = w.Write([]byte(`{"group_by":"` + r.URL.Query().Get("group_by") + `","items":[` +
				`{"key":"ppf_A","runs":3,"input_tokens":1500,"output_tokens":300,"cost_micro_usd":5000,"runs_without_tokens":0,"runs_without_cost":1},` +
				`{"key":"ppf_B","runs":1,"input_tokens":null,"output_tokens":null,"cost_micro_usd":null,"runs_without_tokens":1,"runs_without_cost":1},` +
				`{"runs":2,"input_tokens":null,"output_tokens":null,"cost_micro_usd":null,"runs_without_tokens":2,"runs_without_cost":2}]}`))
		case providerAccountsPath:
			_, _ = w.Write([]byte(`{"items":[{"account_ref":"ppf_A","name":"work-2"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(engine.Close)
	lastQuery := func() url.Values {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if len(queries) == 0 {
			t.Fatal("no request reached GET /usage")
		}
		return queries[len(queries)-1]
	}

	before := time.Now()
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "usage", "--by", "tool", "--since", "7d", "-o", "json"}, sessionCreds(engine.URL)...)...)
	if err != nil {
		t.Fatal(err)
	}
	q := lastQuery()
	since, perr := time.Parse(time.RFC3339, q.Get("since"))
	if q.Get("group_by") != "tool" || perr != nil || since.Before(before.Add(-7*24*time.Hour-time.Minute)) || since.After(time.Now().Add(-7*24*time.Hour+time.Minute)) {
		t.Errorf("query = %v, want group_by=tool and since a week ago", q)
	}
	for _, want := range []string{`"cost_micro_usd": 5000`, `"cost_micro_usd": null`, `"runs_without_cost": 1`} {
		if !strings.Contains(out, want) {
			t.Errorf("-o json lacks %s:\n%s", want, out)
		}
	}

	out, _, err = execSessionCLI(t, nil, append([]string{"session", "usage", "--by", "account", "--daily"}, sessionCreds(engine.URL)...)...)
	if err != nil {
		t.Fatal(err)
	}
	if q := lastQuery(); q.Get("group_by") != "account" || q.Get("bucket") != "day" {
		t.Errorf("query = %v, want group_by=account and bucket=day", q)
	}
	for _, want := range []string{"work-2", "ppf_B", "(none)", "$0.0050"} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "$0.0000") {
		t.Errorf("a cost nobody reported was printed as zero:\n%s", out)
	}
}

// TestSessionUsageRefusesWhatTheEngineWouldNotAnswer: a dimension or a time that is not
// one is a usage error before any request leaves.
func TestSessionUsageRefusesWhatTheEngineWouldNotAnswer(t *testing.T) {
	var hits int
	engine := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	t.Cleanup(engine.Close)
	for _, args := range [][]string{{"--by", "colour"}, {"--since", "yesterday-ish"}, {"--until", "soon"}} {
		_, _, err := execSessionCLI(t, nil, append(append([]string{"session", "usage"}, args...), sessionCreds(engine.URL)...)...)
		if exitcode.From(err) != exitcode.Usage {
			t.Errorf("usage %v = %v (exit %d), want a usage error", args, err, exitcode.From(err))
		}
	}
	if hits != 0 {
		t.Errorf("%d refused requests still reached the engine", hits)
	}
}
