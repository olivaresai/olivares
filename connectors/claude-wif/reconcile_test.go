// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudewif

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const testOrgAdminToken = "anthropic-org-admin-oauth-token-SECRET"

// wifDoer serves the WIF Admin API list endpoints from canned JSON keyed by path, with an
// optional per-path status override (to exercise the honest-unavailable path). Unmapped
// paths return an empty page so an unspecified list is simply empty, not a test failure.
type wifDoer struct {
	bodies map[string]string
	status map[string]int
	reqs   []*http.Request
}

func (d *wifDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	st := 200
	if d.status != nil {
		if s, ok := d.status[req.URL.Path]; ok {
			st = s
		}
	}
	body, ok := d.bodies[req.URL.Path]
	if !ok {
		body = `{"data":[],"next_page":null}`
	}
	return &http.Response{StatusCode: st, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}

// liveFixtures is a representative live WIF config covering every drift class against the
// declared baseline in declaredFixture. fdrl_drift carries a CEL condition we assert never
// leaks into a finding field.
func liveFixtures() map[string]string {
	return map[string]string{
		pathFederationIssuers: `{"data":[
			{"id":"fdis_spire","type":"federation_issuer","name":"spire","issuer_url":"https://oidc.spire.example","jwks":{"type":"discovery"}},
			{"id":"fdis_lonely","type":"federation_issuer","name":"lonely","issuer_url":"https://oidc.lonely.example","jwks":{"type":"explicit_url","ca_cert_pem":"-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----"}}
		],"next_page":null}`,
		pathServiceAccounts: `{"data":[
			{"id":"svac_ci","type":"service_account","name":"ci-deployer","organization_role":"developer"},
			{"id":"svac_admin","type":"service_account","name":"admin-sa","organization_role":"admin"}
		],"next_page":null}`,
		pathFederationRules: `{"data":[
			{"id":"fdrl_ok","type":"federation_rule","name":"ok","issuer_id":"fdis_spire","oauth_scope":"workspace:developer","token_lifetime_seconds":3600,
			 "match":{"subject_prefix":"spiffe://acme/ci/*"},"target":{"service_account_id":"svac_ci","type":"service_account"}},
			{"id":"fdrl_shadow","type":"federation_rule","name":"shadow","issuer_id":"fdis_spire","oauth_scope":"org:admin","token_lifetime_seconds":3600,
			 "match":{"subject_prefix":"","condition":"claims.team == 'sekret'"},"target":{"service_account_id":"svac_admin","type":"service_account"}},
			{"id":"fdrl_drift","type":"federation_rule","name":"drift","issuer_id":"fdis_spire","oauth_scope":"org:manage_tunnels","token_lifetime_seconds":86400,
			 "match":{"subject_prefix":"spiffe://acme/dep"},"target":{"service_account_id":"svac_ci","type":"service_account"}},
			{"id":"fdrl_broken","type":"federation_rule","name":"broken","issuer_id":"fdis_missing","oauth_scope":"workspace:developer","token_lifetime_seconds":3600,
			 "match":{"subject_prefix":"spiffe://acme/x"},"target":{"service_account_id":"svac_ci","type":"service_account"}}
		],"next_page":null}`,
	}
}

// declaredFixture is the operator-declared baseline. fdrl_ok matches live exactly (no
// drift), fdrl_drift diverges in scope+lifetime, fdrl_gone exists only in declaration.
const declaredFixture = `[
	{"rule_id":"fdrl_ok","service_account_id":"svac_ci","issuer_id":"fdis_spire","oauth_scope":"workspace:developer","token_lifetime_seconds":3600,"subject_prefix":"spiffe://acme/ci/*"},
	{"rule_id":"fdrl_drift","service_account_id":"svac_ci","issuer_id":"fdis_spire","oauth_scope":"workspace:developer","token_lifetime_seconds":3600,"subject_prefix":"spiffe://acme/dep"},
	{"rule_id":"fdrl_gone","service_account_id":"svac_gone","issuer_id":"fdis_gone","oauth_scope":"workspace:developer","token_lifetime_seconds":3600}
]`

func newReconciler(t *testing.T, doer *wifDoer, federation string) *Source {
	t.Helper()
	s := New()
	s.doer = doer
	s.now = fixedClock
	settings := map[string]string{
		"org_admin_oauth_token": testOrgAdminToken,
		"organization_id":       "11111111-1111-1111-1111-111111111111",
	}
	if federation != "" {
		settings["federation"] = federation
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// findingByTitleRef indexes findings by title+subject_ref (the same title can apply to
// more than one subject — e.g. two rules can both be undeclared).
func findingByTitleRef(fs []model.FindingReport) map[string]model.FindingReport {
	out := map[string]model.FindingReport{}
	for _, f := range fs {
		out[f.Title+"|"+f.SubjectRef] = f
	}
	return out
}

func TestReconcileFindings(t *testing.T) {
	s := newReconciler(t, &wifDoer{bodies: liveFixtures()}, declaredFixture)
	live, ok, err := s.fetchLiveSet(context.Background())
	if err != nil || !ok {
		t.Fatalf("fetchLiveSet: ok=%v err=%v", ok, err)
	}
	findings := s.reconcileFindings(live, s.clock().UTC())

	// Every finding must carry the shared drift Kind and a redacted (hashed) detail.
	for _, f := range findings {
		if f.Kind != identitysource.FindingFederationDrift {
			t.Errorf("finding %q kind = %q, want %q", f.Title, f.Kind, identitysource.FindingFederationDrift)
		}
		if f.DetailHash == "" || f.OccurredAt.IsZero() {
			t.Errorf("finding %q missing DetailHash/OccurredAt", f.Title)
		}
	}

	if len(findings) != 8 {
		t.Errorf("total findings = %d, want 8", len(findings))
	}

	by := findingByTitleRef(findings)
	want := []struct {
		title string
		sev   model.Severity
		kind  string
		ref   string
	}{
		{"Live WIF rule is not declared or governed", model.SeverityHigh, subjectFederation, "fdrl_shadow"},
		{"Live WIF rule is not declared or governed", model.SeverityHigh, subjectFederation, "fdrl_broken"},
		{"Live WIF rule has an over-broad subject match", model.SeverityMedium, subjectFederation, "fdrl_shadow"},
		{"Live WIF rule scope drifted from the declared scope", model.SeverityHigh, subjectFederation, "fdrl_drift"},
		{"Live WIF rule token lifetime drifted from the declared value", model.SeverityHigh, subjectFederation, "fdrl_drift"},
		{"Live WIF rule references a missing issuer or service account", model.SeverityMedium, subjectFederation, "fdrl_broken"},
		{"Declared WIF rule does not exist in the live organization", model.SeverityMedium, subjectFederation, "fdrl_gone"},
		{"Live WIF federation issuer is referenced by no rule", model.SeverityLow, subjectNHI, "fdis_lonely"},
	}
	for _, w := range want {
		f, ok := by[w.title+"|"+w.ref]
		if !ok {
			t.Errorf("missing finding %q on %q", w.title, w.ref)
			continue
		}
		if f.Severity != w.sev {
			t.Errorf("%q severity = %q, want %q", w.title, f.Severity, w.sev)
		}
		if f.SubjectKind != w.kind {
			t.Errorf("%q subject_kind = %q, want %q", w.title, f.SubjectKind, w.kind)
		}
		if f.SubjectRef != w.ref {
			t.Errorf("%q subject_ref = %q, want %q", w.title, f.SubjectRef, w.ref)
		}
	}

	// fdrl_broken is both undeclared AND orphan → two findings on the same subject.
	var brokenCount int
	for _, f := range findings {
		if f.SubjectRef == "fdrl_broken" {
			brokenCount++
		}
	}
	if brokenCount != 2 {
		t.Errorf("fdrl_broken findings = %d, want 2 (undeclared + orphan)", brokenCount)
	}

	// fdrl_ok matches live exactly → no finding for it.
	for _, f := range findings {
		if f.SubjectRef == "fdrl_ok" {
			t.Errorf("fdrl_ok should be clean, got finding %q", f.Title)
		}
	}

	// Minimal data: no finding field may leak the CEL value, the org-admin token, or a key.
	for _, f := range findings {
		for _, field := range []string{f.Title, f.SubjectRef, f.DetailHash} {
			for _, secret := range []string{"sekret", testOrgAdminToken, "claims.team", "BEGIN CERTIFICATE"} {
				if strings.Contains(field, secret) {
					t.Errorf("finding %q leaks %q in %q", f.Title, secret, field)
				}
			}
		}
	}
}

func TestReconcileScopeOrderIsNotDrift(t *testing.T) {
	// A multi-token scope written in a different order on each side is the SAME scope set
	// — it must NOT be reported as scope drift (regression guard for the asymmetric-norm bug).
	doer := &wifDoer{bodies: map[string]string{
		pathFederationIssuers: `{"data":[],"next_page":null}`,
		pathServiceAccounts:   `{"data":[{"id":"svac_x","type":"service_account","name":"x"}],"next_page":null}`,
		pathFederationRules: `{"data":[{"id":"fdrl_x","type":"federation_rule","issuer_id":"fdis_x","oauth_scope":"org:manage_tunnels workspace:developer","token_lifetime_seconds":3600,
			"match":{"subject_prefix":"spiffe://acme/x"},"target":{"service_account_id":"svac_x","type":"service_account"}}],"next_page":null}`,
	}}
	declared := `[{"rule_id":"fdrl_x","service_account_id":"svac_x","issuer_id":"fdis_x","oauth_scope":"workspace:developer org:manage_tunnels","token_lifetime_seconds":3600,"subject_prefix":"spiffe://acme/x"}]`
	s := newReconciler(t, doer, declared)
	live, _, err := s.fetchLiveSet(context.Background())
	if err != nil {
		t.Fatalf("fetchLiveSet: %v", err)
	}
	for _, f := range s.reconcileFindings(live, s.clock().UTC()) {
		if f.SubjectRef == "fdrl_x" && f.Title == "Live WIF rule scope drifted from the declared scope" {
			t.Errorf("identical scope set in different order reported as drift: %+v", f)
		}
	}
}

func TestReconcileExcludesArchived(t *testing.T) {
	// Archived (soft-deleted) live objects must be dropped: an archived rule must not fire
	// an "undeclared live rule" finding, and an archived issuer must not be an orphan.
	doer := &wifDoer{bodies: map[string]string{
		pathFederationIssuers: `{"data":[{"id":"fdis_gone","type":"federation_issuer","name":"gone","archived_at":"2026-01-01T00:00:00Z"}],"next_page":null}`,
		pathServiceAccounts:   `{"data":[{"id":"svac_gone","type":"service_account","name":"gone","archived_at":"2026-01-01T00:00:00Z"}],"next_page":null}`,
		pathFederationRules: `{"data":[{"id":"fdrl_gone","type":"federation_rule","issuer_id":"fdis_gone","oauth_scope":"org:admin","archived_at":"2026-01-01T00:00:00Z",
			"match":{"subject_prefix":""},"target":{"service_account_id":"svac_gone","type":"service_account"}}],"next_page":null}`,
	}}
	s := newReconciler(t, doer, "")
	live, _, err := s.fetchLiveSet(context.Background())
	if err != nil {
		t.Fatalf("fetchLiveSet: %v", err)
	}
	if len(live.rules) != 0 || len(live.issuers) != 0 || len(live.serviceAccounts) != 0 {
		t.Fatalf("archived objects should be filtered out, got %+v", live)
	}
	if findings := s.reconcileFindings(live, s.clock().UTC()); len(findings) != 0 {
		t.Errorf("archived-only live set should produce no findings, got %d", len(findings))
	}
	g, err := s.ReconciledWIFGraph(context.Background())
	if err != nil {
		t.Fatalf("ReconciledWIFGraph: %v", err)
	}
	if len(g.Rules) != 0 || len(g.Issuers) != 0 || len(g.ServiceAccounts) != 0 {
		t.Errorf("archived objects leaked into the reconciled graph: %+v", g)
	}
}

func TestReconciledWIFGraph(t *testing.T) {
	s := newReconciler(t, &wifDoer{bodies: liveFixtures()}, declaredFixture)
	g, err := s.ReconciledWIFGraph(context.Background())
	if err != nil {
		t.Fatalf("ReconciledWIFGraph: %v", err)
	}
	if g.Reconciliation == nil || !g.Reconciliation.Reconciled || g.Reconciliation.ObservedAt == "" {
		t.Fatalf("reconciliation = %+v, want reconciled with observed_at", g.Reconciliation)
	}

	ruleSrc := map[string]string{}
	for _, r := range g.Rules {
		ruleSrc[r.RuleID] = r.Source
	}
	for id, want := range map[string]string{
		"fdrl_ok":     sourceBoth,
		"fdrl_drift":  sourceBoth,
		"fdrl_shadow": sourceLive,
		"fdrl_broken": sourceLive,
		"fdrl_gone":   sourceDeclared,
	} {
		if ruleSrc[id] != want {
			t.Errorf("rule %s source = %q, want %q", id, ruleSrc[id], want)
		}
	}

	// The live "both" rule keeps the LIVE (actual) values, not the declared ones.
	for _, r := range g.Rules {
		if r.RuleID == "fdrl_drift" {
			if r.TokenLifetimeSeconds != 86400 || r.OAuthScope != "org:manage_tunnels" {
				t.Errorf("fdrl_drift should carry live values, got scope=%q lifetime=%d", r.OAuthScope, r.TokenLifetimeSeconds)
			}
		}
	}

	issSrc := map[string]string{}
	for _, i := range g.Issuers {
		issSrc[i.ID] = i.Source
	}
	if issSrc["fdis_spire"] != sourceBoth || issSrc["fdis_lonely"] != sourceLive || issSrc["fdis_gone"] != sourceDeclared {
		t.Errorf("issuer sources = %+v", issSrc)
	}

	saSrc := map[string]string{}
	saRole := map[string]string{}
	for _, sa := range g.ServiceAccounts {
		saSrc[sa.ID] = sa.Source
		saRole[sa.ID] = sa.OrganizationRole
	}
	if saSrc["svac_ci"] != sourceBoth || saSrc["svac_admin"] != sourceLive || saSrc["svac_gone"] != sourceDeclared {
		t.Errorf("service account sources = %+v", saSrc)
	}
	if saRole["svac_admin"] != "admin" {
		t.Errorf("svac_admin organization_role = %q, want admin", saRole["svac_admin"])
	}

	// Minimal data: the lonely issuer pins a CA cert; the graph carries the presence flag
	// only, never the PEM (there is no field for it, but assert the flag is set).
	for _, i := range g.Issuers {
		if i.ID == "fdis_lonely" && !i.CACertConfigured {
			t.Errorf("fdis_lonely should report ca_cert_configured=true")
		}
	}

	// Minimal data: the SERVED graph JSON must never carry the org:admin token or the CA PEM.
	blob, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("marshal graph: %v", err)
	}
	for _, secret := range []string{testOrgAdminToken, "BEGIN CERTIFICATE"} {
		if strings.Contains(string(blob), secret) {
			t.Errorf("served WIFGraph leaked %q", secret)
		}
	}
}

func TestReconciledWIFGraphOfflineNoToken(t *testing.T) {
	// No org:admin token → wifClient nil → identical to the declared graph, no reconciliation.
	s := New()
	s.doer = &wifDoer{bodies: liveFixtures()}
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"federation": declaredFixture}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	g, err := s.ReconciledWIFGraph(context.Background())
	if err != nil {
		t.Fatalf("ReconciledWIFGraph offline: %v", err)
	}
	if g.Reconciliation != nil {
		t.Errorf("offline reconciliation should be nil, got %+v", g.Reconciliation)
	}
	// Declared-only: the live-only fdrl_shadow must NOT appear.
	for _, r := range g.Rules {
		if r.RuleID == "fdrl_shadow" {
			t.Errorf("offline graph leaked a live-only rule")
		}
		if r.Source != "" {
			t.Errorf("offline rule %s should have no source marker, got %q", r.RuleID, r.Source)
		}
	}
}

func TestReconciledWIFGraphUnavailable(t *testing.T) {
	doer := &wifDoer{bodies: liveFixtures(), status: map[string]int{pathFederationIssuers: 500}}
	s := newReconciler(t, doer, declaredFixture)
	g, err := s.ReconciledWIFGraph(context.Background())
	if err == nil {
		t.Fatal("expected an error from the failed live list")
	}
	if g.Reconciliation == nil || g.Reconciliation.Reconciled || g.Reconciliation.Unavailable == "" {
		t.Fatalf("expected honest unavailable status, got %+v", g.Reconciliation)
	}
	// Honest fallback: the declared baseline still renders (fdrl_ok is declared).
	var sawDeclared bool
	for _, r := range g.Rules {
		if r.RuleID == "fdrl_ok" {
			sawDeclared = true
		}
		if r.Source != "" {
			t.Errorf("fallback graph should be declared-only (no source markers), got %q on %s", r.Source, r.RuleID)
		}
	}
	if !sawDeclared {
		t.Error("declared baseline missing from the unavailable fallback")
	}
	if strings.Contains(g.Reconciliation.Unavailable, testOrgAdminToken) {
		t.Error("unavailable reason leaked the org-admin token")
	}
}

func TestGatherEmitsDriftFindings(t *testing.T) {
	// adminKey empty so the API-key grant step early-returns; reconciliation still runs.
	doer := &wifDoer{bodies: liveFixtures()}
	s := newReconciler(t, doer, declaredFixture)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var drift int
	for _, f := range sink.findings() {
		if f.Kind == identitysource.FindingFederationDrift {
			drift++
		}
	}
	if drift != 8 {
		t.Errorf("drift findings emitted = %d, want 8", drift)
	}
}

func TestGatherReconciliationUnavailable(t *testing.T) {
	doer := &wifDoer{bodies: liveFixtures(), status: map[string]int{pathFederationRules: 503}}
	s := newReconciler(t, doer, declaredFixture)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather should degrade honestly, not fail: %v", err)
	}
	var sawUnavailable bool
	for _, f := range sink.findings() {
		if f.Kind == identitysource.FindingFederationDrift && f.Title == "Live WIF reconciliation unavailable" {
			sawUnavailable = true
			if f.Severity != model.SeverityMedium {
				t.Errorf("unavailable severity = %q, want medium", f.Severity)
			}
		}
	}
	if !sawUnavailable {
		t.Error("expected a reconciliation-unavailable finding when the live list fails")
	}
}

func TestListWIFPagination(t *testing.T) {
	// Two pages via the opaque next_page cursor (passed back as ?page=).
	doer := &pagerDoer{}
	client := modelprovider.NewClient("https://api.test", doer, modelprovider.AuthBearer, testOrgAdminToken, nil)
	sas, err := listWIF[liveServiceAccount](context.Background(), client, 20, pathServiceAccounts)
	if err != nil {
		t.Fatalf("listWIF: %v", err)
	}
	if len(sas) != 2 || sas[0].ID != "svac_1" || sas[1].ID != "svac_2" {
		t.Fatalf("paged service accounts = %+v", sas)
	}
	if doer.calls != 2 {
		t.Errorf("calls = %d, want 2 (one per page)", doer.calls)
	}
	if doer.lastPageParam != "cursor2" {
		t.Errorf("second request page param = %q, want cursor2", doer.lastPageParam)
	}
}

// pagerDoer returns a first page with a next_page cursor, then a final page.
type pagerDoer struct {
	calls         int
	lastPageParam string
}

func (d *pagerDoer) Do(req *http.Request) (*http.Response, error) {
	d.calls++
	page := req.URL.Query().Get("page")
	d.lastPageParam = page
	var body string
	if page == "" {
		body = `{"data":[{"id":"svac_1","type":"service_account","name":"a"}],"next_page":"cursor2"}`
	} else {
		body = `{"data":[{"id":"svac_2","type":"service_account","name":"b"}],"next_page":null}`
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}
