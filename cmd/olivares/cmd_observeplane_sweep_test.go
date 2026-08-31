// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

// The observe-and-report lane, driven END TO END: all 73 leaf verbs, both
// directions, against the route census the modules actually mount.
//
// WHY THIS EXISTS NEXT TO cmd_observeplane_test.go. That file samples — one read
// per family for the credential and routing properties, ten representatives for
// the refusal map — and pins the surface with a per-family verb COUNT
// (TestLaneVerbCountMatchesTheRouteCensus). A count is a weak referent, and this
// branch has already paid for that once: C08-02 lot 1 landed 3.043 lines of
// command tree whose constructors were never added to the root, and the half of
// its parity suite that asserted "no route is reachable without a credential"
// stayed green throughout, because nothing was reachable at all. A count also
// cannot see two verbs addressing one route while a third route has none.
//
// So this file asserts the three things a count cannot:
//
//   PERMIT — every leaf produces EXACTLY ONE request, and the (method, path) on
//   the wire matches a route some module mounts. A path typo is otherwise
//   invisible: a stub that answers 200 to everything makes a misaddressed
//   command look perfectly healthy.
//
//   BIJECTION — the 73 leaves cover the 73 routes exactly once each. Both
//   failure directions are named: a route with no command, and a route with two.
//
//   DENY — the SAME 73 leaves, with an empty --token, reach the wire ZERO times.
//   Ten representatives say nothing about the other 63.
//
// WHAT IT CANNOT SEE, stated rather than left for a reader to assume: the engine
// table below is a FROZEN census, hand-carried from reg.Handle/HandleEntity in
// the module sources. It catches a command that stops matching the census; it
// does NOT notice a route added to a module and to neither table. That gap is
// the reason the count assertions in the sibling file stay where they are.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// verEngineRoutes is the census: every route the ten lot-4 namespaces mount,
// read off reg.Handle/HandleEntity in the module sources.
var verEngineRoutes = []string{
	"GET /v1/m/reporting/reports",
	"GET /v1/m/reporting/reports/{type}",
	"GET /v1/m/reporting/enterprise/posture",
	"GET /v1/m/reporting/enterprise/risk",
	"GET /v1/m/reporting/enterprise/bundle",
	"GET /v1/m/reporting/schedules",
	"POST /v1/m/reporting/schedules",
	"DELETE /v1/m/reporting/schedules/{id}",
	"GET /v1/m/reporting/schedules/{id}/runs",
	"GET /v1/m/reporting/schedules/{id}/runs/{rid}",
	"GET /v1/m/reporting/branding",
	"PUT /v1/m/reporting/branding",
	"GET /v1/m/reporting/templates/{type}",
	"PUT /v1/m/reporting/templates/{type}",
	"DELETE /v1/m/reporting/templates/{type}",

	"GET /v1/m/notify/routes",
	"POST /v1/m/notify/routes",
	"GET /v1/m/notify/routes/{id}",
	"PUT /v1/m/notify/routes/{id}",
	"DELETE /v1/m/notify/routes/{id}",
	"POST /v1/m/notify/routes/{id}/test",
	"GET /v1/m/notify/routes/{id}/revisions",
	"POST /v1/m/notify/routes/{id}/restore",
	"POST /v1/m/notify/routes/evaluate",
	"GET /v1/m/notify/match-types",
	"GET /v1/m/notify/destinations",
	"GET /v1/m/notify/deliveries",
	"GET /v1/m/notify/outbox",
	"POST /v1/m/notify/outbox/{id}/redeliver",

	"GET /v1/m/health/status",
	"GET /v1/m/health/stream",
	"GET /v1/m/health/sla",
	"GET /v1/m/health/dependencies",
	"GET /v1/m/health/events",
	"GET /v1/m/health/incidents",
	"GET /v1/m/health/incidents/{id}",
	"GET /v1/m/health/checks",
	"POST /v1/m/health/checks",
	"GET /v1/m/health/checks/{id}",
	"PUT /v1/m/health/checks/{id}",
	"DELETE /v1/m/health/checks/{id}",
	"POST /v1/m/health/checks/{id}/report",
	"POST /v1/m/health/incidents/{id}/resolve",

	"GET /v1/m/accessmap/graph",
	"GET /v1/m/accessmap/neighbors",
	"GET /v1/m/accessmap/drift",
	"GET /v1/m/accessmap/attack-paths/reachability",
	"GET /v1/m/accessmap/attack-paths/escalation",
	"GET /v1/m/accessmap/attack-paths/exfil",
	"GET /v1/m/accessmap/attack-paths/summary",

	"GET /v1/m/observability/ingestion-health",
	"GET /v1/m/observability/traces",
	"GET /v1/m/observability/traces/{id}",
	"GET /v1/m/observability/traces/{id}/export",
	"GET /v1/m/observability/attestation",

	"GET /v1/m/consoleviews/views",
	"GET /v1/m/consoleviews/views/{id}",
	"POST /v1/m/consoleviews/views",
	"PUT /v1/m/consoleviews/views/{id}",
	"DELETE /v1/m/consoleviews/views/{id}",

	"GET /v1/m/adoption/summary",
	"GET /v1/m/adoption/trend",
	"GET /v1/m/adoption/teams",
	"GET /v1/m/adoption/discrepancy",
	"GET /v1/m/adoption/developers",

	"GET /v1/m/identity/wif",
	"GET /v1/m/identity/sso",
	"GET /v1/m/identity/external-keys",
	"GET /v1/m/identity/residency",

	"GET /v1/m/inventory/summary",
	"GET /v1/m/inventory/entities",
	"GET /v1/m/inventory/entities/{kind}/{id}",

	"GET /v1/m/posture/export",
}

// verLeaf is one leaf verb and the engine route it must address.
type verLeaf struct {
	args  []string
	route string
}

func verLeaves(tmp string) []verLeaf {
	tpl := filepath.Join(tmp, "tpl.html")
	return []verLeaf{
		// ---- reporting (15) ----
		{[]string{"reporting", "branding", "get"}, "GET /v1/m/reporting/branding"},
		{[]string{"reporting", "branding", "set", "--company-name", "Acme"}, "PUT /v1/m/reporting/branding"},
		{[]string{"reporting", "enterprise", "bundle"}, "GET /v1/m/reporting/enterprise/bundle"},
		{[]string{"reporting", "enterprise", "posture"}, "GET /v1/m/reporting/enterprise/posture"},
		{[]string{"reporting", "enterprise", "risk"}, "GET /v1/m/reporting/enterprise/risk"},
		{[]string{"reporting", "reports", "get", "audit-summary", "--out", "-"}, "GET /v1/m/reporting/reports/{type}"},
		{[]string{"reporting", "reports", "ls"}, "GET /v1/m/reporting/reports"},
		{[]string{"reporting", "schedules", "create", "--report-type", "audit-summary", "--cron", "0 0 * * *"}, "POST /v1/m/reporting/schedules"},
		{[]string{"reporting", "schedules", "ls"}, "GET /v1/m/reporting/schedules"},
		{[]string{"reporting", "schedules", "rm", "sch-1", "--yes"}, "DELETE /v1/m/reporting/schedules/{id}"},
		{[]string{"reporting", "schedules", "run", "sch-1", "run-1", "--out", "-"}, "GET /v1/m/reporting/schedules/{id}/runs/{rid}"},
		{[]string{"reporting", "schedules", "runs", "sch-1"}, "GET /v1/m/reporting/schedules/{id}/runs"},
		{[]string{"reporting", "templates", "get", "audit-summary", "--out", "-"}, "GET /v1/m/reporting/templates/{type}"},
		{[]string{"reporting", "templates", "rm", "audit-summary", "--yes"}, "DELETE /v1/m/reporting/templates/{type}"},
		{[]string{"reporting", "templates", "set", "audit-summary", tpl}, "PUT /v1/m/reporting/templates/{type}"},

		// ---- notify (14) ----
		{[]string{"notify", "deliveries"}, "GET /v1/m/notify/deliveries"},
		{[]string{"notify", "destinations"}, "GET /v1/m/notify/destinations"},
		{[]string{"notify", "evaluate", "--event-type", "finding.created"}, "POST /v1/m/notify/routes/evaluate"},
		{[]string{"notify", "match-types"}, "GET /v1/m/notify/match-types"},
		{[]string{"notify", "outbox", "ls"}, "GET /v1/m/notify/outbox"},
		{[]string{"notify", "outbox", "redeliver", "ob-1"}, "POST /v1/m/notify/outbox/{id}/redeliver"},
		{[]string{"notify", "routes", "create", "--name", "n", "--destination", "d"}, "POST /v1/m/notify/routes"},
		{[]string{"notify", "routes", "get", "rt-1"}, "GET /v1/m/notify/routes/{id}"},
		{[]string{"notify", "routes", "ls"}, "GET /v1/m/notify/routes"},
		{[]string{"notify", "routes", "restore", "rt-1", "--revision-id", "rev-1"}, "POST /v1/m/notify/routes/{id}/restore"},
		{[]string{"notify", "routes", "revisions", "rt-1"}, "GET /v1/m/notify/routes/{id}/revisions"},
		{[]string{"notify", "routes", "rm", "rt-1", "--yes"}, "DELETE /v1/m/notify/routes/{id}"},
		{[]string{"notify", "routes", "test", "rt-1"}, "POST /v1/m/notify/routes/{id}/test"},
		{[]string{"notify", "routes", "update", "rt-1", "--name", "n", "--destination", "d"}, "PUT /v1/m/notify/routes/{id}"},

		// ---- health (14) ----
		{[]string{"health", "checks", "create", "--subject-kind", "agent", "--subject-ref", "ag-7", "--interval", "60"}, "POST /v1/m/health/checks"},
		{[]string{"health", "checks", "get", "chk-1"}, "GET /v1/m/health/checks/{id}"},
		{[]string{"health", "checks", "ls"}, "GET /v1/m/health/checks"},
		{[]string{"health", "checks", "report", "chk-1", "--state", "healthy"}, "POST /v1/m/health/checks/{id}/report"},
		{[]string{"health", "checks", "rm", "chk-1", "--yes"}, "DELETE /v1/m/health/checks/{id}"},
		{[]string{"health", "checks", "update", "chk-1", "--name", "n"}, "PUT /v1/m/health/checks/{id}"},
		{[]string{"health", "dependencies"}, "GET /v1/m/health/dependencies"},
		{[]string{"health", "events"}, "GET /v1/m/health/events"},
		{[]string{"health", "incidents", "get", "inc-1"}, "GET /v1/m/health/incidents/{id}"},
		{[]string{"health", "incidents", "ls"}, "GET /v1/m/health/incidents"},
		{[]string{"health", "incidents", "resolve", "inc-1"}, "POST /v1/m/health/incidents/{id}/resolve"},
		{[]string{"health", "sla", "--subject-kind", "agent", "--subject-ref", "ag-7"}, "GET /v1/m/health/sla"},
		{[]string{"health", "status"}, "GET /v1/m/health/status"},
		{[]string{"health", "watch"}, "GET /v1/m/health/stream"},

		// ---- accessmap (7) ----
		{[]string{"accessmap", "attack-paths", "escalation", "--agent-id", "ag-7"}, "GET /v1/m/accessmap/attack-paths/escalation"},
		{[]string{"accessmap", "attack-paths", "exfil", "--resource-id", "r-1"}, "GET /v1/m/accessmap/attack-paths/exfil"},
		{[]string{"accessmap", "attack-paths", "reachability", "--agent-id", "ag-7"}, "GET /v1/m/accessmap/attack-paths/reachability"},
		{[]string{"accessmap", "attack-paths", "summary"}, "GET /v1/m/accessmap/attack-paths/summary"},
		{[]string{"accessmap", "drift"}, "GET /v1/m/accessmap/drift"},
		{[]string{"accessmap", "graph"}, "GET /v1/m/accessmap/graph"},
		{[]string{"accessmap", "neighbors", "--id", "ag-7"}, "GET /v1/m/accessmap/neighbors"},

		// ---- observability (5) ----
		{[]string{"observability", "attestation"}, "GET /v1/m/observability/attestation"},
		{[]string{"observability", "ingestion-health"}, "GET /v1/m/observability/ingestion-health"},
		{[]string{"observability", "traces", "export", "tr-1"}, "GET /v1/m/observability/traces/{id}/export"},
		{[]string{"observability", "traces", "get", "tr-1"}, "GET /v1/m/observability/traces/{id}"},
		{[]string{"observability", "traces", "ls"}, "GET /v1/m/observability/traces"},

		// ---- consoleviews (5) ----
		{[]string{"consoleviews", "create", "--feature-id", "findings", "--name", "n", "--params", `{"a":1}`}, "POST /v1/m/consoleviews/views"},
		{[]string{"consoleviews", "get", "sv-1"}, "GET /v1/m/consoleviews/views/{id}"},
		{[]string{"consoleviews", "ls"}, "GET /v1/m/consoleviews/views"},
		{[]string{"consoleviews", "rm", "sv-1", "--yes"}, "DELETE /v1/m/consoleviews/views/{id}"},
		{[]string{"consoleviews", "update", "sv-1", "--name", "n", "--params", `{"a":1}`}, "PUT /v1/m/consoleviews/views/{id}"},

		// ---- adoption (5) ----
		{[]string{"adoption", "developers"}, "GET /v1/m/adoption/developers"},
		{[]string{"adoption", "discrepancy"}, "GET /v1/m/adoption/discrepancy"},
		{[]string{"adoption", "summary"}, "GET /v1/m/adoption/summary"},
		{[]string{"adoption", "teams"}, "GET /v1/m/adoption/teams"},
		{[]string{"adoption", "trend"}, "GET /v1/m/adoption/trend"},

		// ---- identity (4) ----
		{[]string{"identity", "external-keys"}, "GET /v1/m/identity/external-keys"},
		{[]string{"identity", "residency"}, "GET /v1/m/identity/residency"},
		{[]string{"identity", "sso"}, "GET /v1/m/identity/sso"},
		{[]string{"identity", "wif"}, "GET /v1/m/identity/wif"},

		// ---- inventory (3) ----
		{[]string{"inventory", "entities", "get", "agent", "ent-1"}, "GET /v1/m/inventory/entities/{kind}/{id}"},
		{[]string{"inventory", "entities", "ls"}, "GET /v1/m/inventory/entities"},
		{[]string{"inventory", "summary"}, "GET /v1/m/inventory/summary"},

		// ---- posture (1) ----
		{[]string{"posture", "export"}, "GET /v1/m/posture/export"},
	}
}

type verSpy struct {
	mu   sync.Mutex
	reqs []string
	srv  *httptest.Server
}

func newVerSpy(t *testing.T) *verSpy {
	s := &verSpy{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.reqs = append(s.reqs, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"x","items":[],"nodes":[],"edges":[],"teams":[],` +
			`"destinations":[],"has_data":true,"tenant":"t","deleted":true,"state":"resolved"}`))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *verSpy) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.reqs...)
}

// verNormalise turns a concrete request line back into its route template using
// the engine census. It returns "" when NO mounted route matches, which is the
// failure this instrument exists to find.
func verNormalise(line string) string {
	for _, r := range verEngineRoutes {
		rm, rp, _ := strings.Cut(r, " ")
		gm, gp, _ := strings.Cut(line, " ")
		if rm != gm {
			continue
		}
		pat := regexp.QuoteMeta(rp)
		pat = regexp.MustCompile(`\\\{[a-z]+\\\}`).ReplaceAllString(pat, `[^/]+`)
		if regexp.MustCompile("^" + pat + "$").MatchString(gp) {
			return r
		}
	}
	return ""
}

// TestEveryLaneVerbLandsOnARealEngineRouteExactlyOnce is the PERMIT sweep plus
// the bijection — the measurement a per-family verb count cannot make.
func TestEveryLaneVerbLandsOnARealEngineRouteExactlyOnce(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "tpl.html"), []byte("<html>t</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	leaves := verLeaves(tmp)
	if len(leaves) != 73 {
		t.Fatalf("the driving table has %d leaves, not the 73 the census counted", len(leaves))
	}
	if len(verEngineRoutes) != 73 {
		t.Fatalf("the engine table has %d routes, not 73", len(verEngineRoutes))
	}
	covered := map[string][]string{}
	for _, lf := range leaves {
		name := strings.Join(lf.args, " ")
		t.Run(name, func(t *testing.T) {
			spy := newVerSpy(t)
			args := append(append([]string{}, lf.args...),
				"--server", spy.srv.URL, "--token", "tok", "--tenant", "tenant-a")
			_, _, err := execRoot(t, args...)
			got := spy.all()
			if len(got) != 1 {
				t.Fatalf("%d request(s) reached the wire, want exactly 1 (err=%v)", len(got), err)
			}
			route := verNormalise(got[0])
			if route == "" {
				t.Fatalf("addressed %q — NO SUCH ROUTE is mounted by any module", got[0])
			}
			if route != lf.route {
				t.Fatalf("addressed %q (route %s), want %s", got[0], route, lf.route)
			}
			covered[route] = append(covered[route], name)
		})
	}
	for _, r := range verEngineRoutes {
		switch n := len(covered[r]); {
		case n == 0:
			t.Errorf("ENGINE ROUTE WITH NO COMMAND: %s", r)
		case n > 1:
			t.Errorf("route %s is addressed by %d verbs: %v", r, n, covered[r])
		}
	}
}

// TestNoLaneVerbReachesTheWireWithoutACredential is the DENY sweep over the SAME
// 73 verbs the permit sweep just proved reach the wire. That pairing is what
// makes zero-requests evidence: on its own it is equally satisfied by a command
// that is simply broken.
func TestNoLaneVerbReachesTheWireWithoutACredential(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "tpl.html"), []byte("<html>t</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, lf := range verLeaves(tmp) {
		name := strings.Join(lf.args, " ")
		t.Run(name, func(t *testing.T) {
			spy := newVerSpy(t)
			args := append(append([]string{}, lf.args...),
				"--server", spy.srv.URL, "--token", "", "--tenant", "tenant-a")
			_, _, err := execRoot(t, args...)
			if err == nil {
				t.Fatal("a verb with no credential must not succeed")
			}
			if n := len(spy.all()); n != 0 {
				t.Fatalf("%d request(s) reached the server with no credential: %v", n, spy.all())
			}
		})
	}
}
