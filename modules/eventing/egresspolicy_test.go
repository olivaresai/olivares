// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/core/egress"
	"github.com/olivaresai/olivares/core/model"
)

// fixedPolicy is an operator policy that answers the same thing for every tenant.
type fixedPolicy struct {
	pol egress.Policy
	err error
}

func (f fixedPolicy) EgressPolicy(context.Context, model.TenantID) (egress.Policy, error) {
	return f.pol, f.err
}

// loopbackResolver answers every name with 127.0.0.1, so a test can exercise a
// destination policy against a real httptest server without touching DNS.
type loopbackResolver struct{}

func (loopbackResolver) LookupIP(context.Context, string) ([]net.IP, error) {
	return []net.IP{net.ParseIP("127.0.0.1")}, nil
}

// settle drives the dispatcher until every delivery row has a committed outcome.
// Anchoring on the ROW rather than on a sleep is what keeps the test from passing
// because the assertion ran before the worker did.
func (h *harness) settle(tenant model.TenantID) {
	h.t.Helper()
	waitFor(h.t, "delivery outcome to commit", func() bool {
		h.dispatch(tenant)
		rows := h.deliveryRows(tenant)
		if len(rows) == 0 {
			return false
		}
		for _, r := range rows {
			if s := r.String(colDelStatus); s == statusDelivering || s == statusQueued {
				return false
			}
		}
		return true
	})
}

func allowHost(host string) egress.Policy {
	p := egress.Policy{InForce: true, Allow: []egress.Rule{{Host: host}}, Ref: "test"}
	if err := p.Validate(); err != nil {
		panic(err)
	}
	return p
}

// TestPolicyRefusesADestinationAuthoredBeforeIt is the property an authoring-time
// check cannot provide, and the reason the send path is the authoritative seam.
//
// The subscription is created while nothing constrains it — which is how every
// subscription on an existing estate got there — and the operator writes the policy
// afterwards. A control applied only at authoring would grandfather this row
// forever, so the destination the operator has just forbidden would keep receiving
// the tenant's governance events.
func TestPolicyRefusesADestinationAuthoredBeforeIt(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.test", "editor")
	h.createSubscription(editor, tenant, map[string]any{
		"name": "siem", "endpoint": srv.URL, "event_types": []string{"finding.reported"},
		"role": "viewer",
	})

	// The operator now forbids that destination. Nothing about the stored row changes.
	h.mod.egress = fixedPolicy{pol: allowHost("soc.example.com")}
	h.mod.resolver = loopbackResolver{}

	h.publishFinding(tenant, "scanner", "drift", "t")
	h.settle(tenant)

	if got := hits.Load(); got != 0 {
		t.Fatalf("a destination the policy forbids received %d deliveries", got)
	}
	rows := h.deliveryRows(tenant)
	if len(rows) != 1 {
		t.Fatalf("want exactly one delivery row, got %d", len(rows))
	}
	if got := rows[0].String(colDelStatus); got != statusDead {
		t.Errorf("status = %q, want %q: a refusal will not become a permit by waiting", got, statusDead)
	}
	if got := rows[0].String(colDelLastStatus); !strings.HasPrefix(got, "egress_") {
		t.Errorf("outcome = %q, want an egress denial token", got)
	}
}

// TestPolicyPermitsAnAllowlistedDestination is the other half: the control must not
// be a blanket refusal that happens to pass the test above.
func TestPolicyPermitsAnAllowlistedDestination(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.test", "editor")

	host, _, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	h.mod.egress = fixedPolicy{pol: allowHost(host)}
	h.mod.resolver = loopbackResolver{}

	h.createSubscription(editor, tenant, map[string]any{
		"name": "siem", "endpoint": srv.URL, "event_types": []string{"finding.reported"},
		"role": "viewer",
	})
	h.publishFinding(tenant, "scanner", "drift", "t")
	h.settle(tenant)

	if got := hits.Load(); got == 0 {
		t.Fatal("an allow-listed destination received nothing")
	}
}

// TestUnreadablePolicyDeniesRatherThanPermits. "The plane could not decide" must
// never read as "the plane decided yes" — the posture inferenceproxy and
// orchestration already take, and the only one that is safe for a control whose
// whole job is to constrain.
func TestUnreadablePolicyDeniesRatherThanPermits(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.test", "editor")
	h.createSubscription(editor, tenant, map[string]any{
		"name": "siem", "endpoint": srv.URL, "event_types": []string{"finding.reported"},
		"role": "viewer",
	})

	h.mod.egress = fixedPolicy{err: context.DeadlineExceeded}
	h.mod.resolver = loopbackResolver{}

	h.publishFinding(tenant, "scanner", "drift", "t")
	// Wait on the EFFECT — an attempt that recorded its outcome — rather than driving
	// a fixed number of passes. There is no terminal state to settle on here, and that
	// is the second half of the property: an unreadable store is an OUTAGE, not a
	// refusal, so the delivery is requeued rather than dead-lettered. Counting passes
	// asserted before the capture had even reached the worker, and passed only by
	// timing.
	waitFor(t, "the attempt to record its outcome", func() bool {
		h.dispatch(tenant)
		rows := h.deliveryRows(tenant)
		return len(rows) == 1 && rows[0].String(colDelLastStatus) != ""
	})

	if got := hits.Load(); got != 0 {
		t.Fatalf("an unreadable policy permitted %d deliveries", got)
	}
	rows := h.deliveryRows(tenant)
	if got := rows[0].String(colDelStatus); got == statusDead {
		t.Error("an unreadable policy dead-lettered the delivery: an outage is not a refusal")
	}
	if got := rows[0].String(colDelLastStatus); got != "egress_policy_unavailable" {
		t.Errorf("outcome = %q, want egress_policy_unavailable — the operator must be able to "+
			"tell a store outage from a destination the policy refuses", got)
	}
	// Unit G: and it is not paid for out of the retry ladder. Requeueing was already
	// right, but it happened AFTER the claim had incremented attempts, so a long enough
	// outage still walked the ladder to its end and dead-lettered the evidence. The
	// refusal now parks pre-attempt, on the kill switch's terms.
	if got := rows[0].Int(colDelAttempts); got != 0 {
		t.Errorf("attempts = %d, want 0: an outage of the control plane's own state must not "+
			"consume the retry ladder, or a long enough one destroys the evidence it was carrying", got)
	}
}

// TestAuthoringRefusesADestinationThePolicyForbids: the courtesy check. An author
// should learn at the moment they write the destination, not from a dead-letter.
func TestAuthoringRefusesADestinationThePolicyForbids(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.test", "editor")

	h.mod.egress = fixedPolicy{pol: allowHost("soc.example.com")}
	h.mod.resolver = loopbackResolver{}

	r := h.do("POST", "/v1/m/eventing/subscriptions", editor, map[string]any{
		"name": "siem", "endpoint": "https://attacker.example.com/x",
		"event_types": []string{"finding.reported"}, "role": "viewer",
	}, nil)
	if r.code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400: %s", r.code, r.raw)
	}
	// The message names the destination the caller just supplied and NOTHING about
	// the policy. A holder of eventing:subscription:write must not be able to
	// enumerate an operator's allow-list by watching which destinations are refused.
	if strings.Contains(r.raw, "soc.example.com") {
		t.Errorf("the refusal leaked the allow-list: %s", r.raw)
	}
}

// TestNoPolicyKeepsEveryExistingDestinationWorking. The upgrade-safety property: an
// estate whose operator has configured nothing must behave exactly as before, or
// shipping this breaks every subscription in the field.
func TestNoPolicyKeepsEveryExistingDestinationWorking(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := newHarness(t) // no WithEgressPolicy
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.test", "editor")
	h.createSubscription(editor, tenant, map[string]any{
		"name": "siem", "endpoint": srv.URL, "event_types": []string{"finding.reported"},
		"role": "viewer",
	})
	h.publishFinding(tenant, "scanner", "drift", "t")
	h.settle(tenant)

	if got := hits.Load(); got == 0 {
		t.Fatal("an unconfigured estate stopped delivering")
	}
}

// TestPinGovernsTheDialForBothAddressAndNameDestinations.
//
// The NAME case is the one that matters and it is the one an IP-literal fixture
// hides. An http.Transport hands its dialer "hostname:port" — it does not resolve
// the name, net.Dialer does, inside the dial — so a pin that merely VERIFIED its
// argument would see a name, not an address, and refuse every destination addressed
// by name. That is all of them. An earlier revision of this code did exactly that
// and this test passed anyway, because httptest hands back a 127.0.0.1 URL.
func TestPinGovernsTheDialForBothAddressAndNameDestinations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}

	m := &Module{allowLoopback: true}
	client := m.guardedClient()

	get := func(rawURL string, pin ...net.IP) error {
		ctx := egress.WithPin(context.Background(), pin)
		req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if rerr != nil {
			t.Fatal(rerr)
		}
		resp, derr := client.Do(req)
		if derr == nil {
			_ = resp.Body.Close()
		}
		return derr
	}

	// A destination addressed by NAME whose resolved address is pinned must connect,
	// and it must connect WITHOUT the dialer resolving the name a second time.
	if err := get("http://localhost:"+port+"/", net.ParseIP("127.0.0.1")); err != nil {
		t.Fatalf("a name destination whose address is pinned was refused: %v", err)
	}
	// The same name with a pin that does not cover it must NOT connect — otherwise
	// the test above would pass for a dialer that ignores the pin entirely.
	if err := get("http://localhost:"+port+"/", net.ParseIP("93.184.216.34")); err == nil {
		t.Fatal("a name destination connected despite a pin that did not cover its address")
	}
	// An address destination is verified rather than substituted.
	if err := get(srv.URL, net.ParseIP("127.0.0.1")); err != nil {
		t.Fatalf("an address destination that IS pinned was refused: %v", err)
	}
	if err := get(srv.URL, net.ParseIP("93.184.216.34")); err == nil {
		t.Fatal("the dialer connected to an address the authorization did not cover")
	}
}

// TestAnUnconfiguredEstateHasNoOpinionAboutHostSyntax is the regression that would
// have shipped otherwise, and the fixture is the whole point: a hostname with an
// UNDERSCORE, which is ordinary in internal naming and which IDNA2008's strict
// profile refuses.
//
// The destination used to be canonicalized BEFORE the policy was consulted, so on an
// estate with no policy at all such a subscription started failing terminally — with
// a message naming a policy that did not exist. An absent policy must change nothing
// whatsoever; that is the entire promise of the ABSENT arm of the tri-state, and the
// earlier test could not see the break because its httptest URL was "127.0.0.1".
func TestAnUnconfiguredEstateHasNoOpinionAboutHostSyntax(t *testing.T) {
	h := newHarness(t) // no policy wired: the default estate
	for _, raw := range []string{
		"https://my_host.example.com/hooks", // underscore: refused by IDNA2008
		"https://xn--bad-punycode-.example.com/hooks",
		"https://trailing-.example.com/hooks",
	} {
		d, err := h.mod.authorizeDestination(context.Background(), egressRequest{
			Tenant: model.NewTenantID(), Purpose: EgressCreate, URL: raw,
		})
		if !d.Permitted {
			t.Errorf("an estate with NO policy refused %q (code %q, err %v)", raw, d.Code, err)
		}
		if d.Code != egress.CodeNoPolicy {
			t.Errorf("%q: code = %q, want %q", raw, d.Code, egress.CodeNoPolicy)
		}
	}
}

// TestARefusalIsNotAMembershipOracle. Distinguishing "the host is allowed but not on
// that port" from "the host is not allowed" tells a caller which hosts are on the
// operator's allow-list: probe a known host on a port you expect to be closed, and a
// port-specific refusal confirms membership. The tenant-facing message is therefore
// one message; the precise code still reaches the delivery ledger, which an operator
// reads and a tenant does not.
func TestARefusalIsNotAMembershipOracle(t *testing.T) {
	p := egress.Policy{InForce: true, Allow: []egress.Rule{
		{Host: "secret-soc.example.com", Ports: []egress.PortRange{{Low: 443, High: 443}}},
	}, Ref: "test"}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	ips := []net.IP{net.ParseIP("93.184.216.34")}

	onList := egress.Evaluate(p, egress.Destination{Host: "secret-soc.example.com", Port: 8443}, ips)
	offList := egress.Evaluate(p, egress.Destination{Host: "guess.example.com", Port: 8443}, ips)
	if onList.Permitted || offList.Permitted {
		t.Fatal("both probes should be refused")
	}
	// The CODES differ — that is what the ledger needs.
	if onList.Code == offList.Code {
		t.Errorf("the ledger lost the distinction: both %q", onList.Code)
	}
	// The MESSAGES must not, or the 400 answers "is this host on the list?".
	onMsg := egressAuthoringError("https://secret-soc.example.com:8443/x", onList.Code)
	offMsg := egressAuthoringError("https://guess.example.com:8443/x", offList.Code)
	strip := func(s, host string) string { return strings.ReplaceAll(s, host, "HOST") }
	if strip(onMsg, "https://secret-soc.example.com:8443/x") != strip(offMsg, "https://guess.example.com:8443/x") {
		t.Errorf("a refusal distinguishes an allow-listed host from an unknown one:\n on:  %q\n off: %q",
			onMsg, offMsg)
	}
}

// TestSendRefusesACleartextURLEvenWithNoPolicy. Authoring requires https, and
// authoring is NOT authoritative for it: a stored row may predate the rule, and the
// URL a SinkRenderer returns is not the stored endpoint at all — the interface
// permits any URL and says so. A body, an HMAC signature and any bearer credential
// are about to travel, so the transport rule is checked on the URL that will actually
// be dialed, and independently of whether a destination policy exists.
func TestSendRefusesACleartextURLEvenWithNoPolicy(t *testing.T) {
	h := newHarness(t) // no policy: the rule must hold anyway

	// Exercise send() itself, not the validator it calls. An earlier version of this
	// test asserted on validateEndpointURL directly and would have passed even if
	// send() never consulted it — the same "tested the helper, not the behavior"
	// defect this campaign has already paid for twice.
	var reached atomic.Int64
	h.mod.doer = doerFunc(func(*http.Request) (*http.Response, error) {
		reached.Add(1)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	for _, raw := range []string{
		"http://soc.example.com:8080/hook", // explicit port: the case that slipped past
		"http://soc.example.com/hook",
	} {
		status, outcome := h.mod.send(context.Background(), model.NewTenantID(),
			attempt{endpoint: raw, eventID: "e1", deliveryID: model.NewID()}, "sec", "")
		if status != statusDead || outcome != outcomeBadEndpoint {
			t.Errorf("%q: send returned (%q,%q), want a terminal endpoint_invalid — a signed "+
				"body and any bearer credential would otherwise travel in cleartext",
				raw, status, outcome)
		}
	}
	if n := reached.Load(); n != 0 {
		t.Fatalf("%d cleartext request(s) reached the outbound client", n)
	}

	// And the development posture still works, or the check is a blanket refusal.
	if msg := validateEndpointURL("http://127.0.0.1:9000/hook", true); msg != "" {
		t.Errorf("loopback http must stay available under the development switch: %s", msg)
	}
}

// doerFunc adapts a function to the module's outbound-client seam.
type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

// TestTheEgressPolicySurfaceAnswersWithoutDisclosing covers the two endpoints that
// make the control usable: an operator can tell whether their file was read at all,
// and an author can ask about a destination before creating anything.
//
// The constraint that shapes both is that neither may become an ENUMERATION tool. The
// status answers "is one in force", never how many rules or which; the dry-run answers
// only about the destination the caller already supplied, with the same collapsed
// message the authoring path returns — a dry-run that were a better oracle than
// attempting the create would simply be the faster way to map an operator's list.
func TestTheEgressPolicySurfaceAnswersWithoutDisclosing(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@acme.test", "viewer")

	// With nothing configured, the surface says so — which is the answer an operator
	// needs when a destination they expected to be refused went through.
	r := h.do("GET", "/v1/m/eventing/egress-policy", viewer, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("status = %d: %s", r.code, r.raw)
	}
	if !strings.Contains(r.raw, `"in_force":false`) {
		t.Errorf("an unconfigured estate did not report itself as unconstrained: %s", r.raw)
	}

	// A PORT-restricted rule, because that is what creates the oracle risk: probing an
	// allow-listed host on a port it does not permit must be indistinguishable from
	// probing a host that is not on the list at all.
	portScoped := egress.Policy{InForce: true, Ref: "test", Allow: []egress.Rule{
		{Host: "soc.example.com", Ports: []egress.PortRange{{Low: 443, High: 443}}},
	}}
	if err := portScoped.Validate(); err != nil {
		t.Fatal(err)
	}
	h.mod.egress = fixedPolicy{pol: portScoped}
	h.mod.resolver = loopbackResolver{}

	r = h.do("GET", "/v1/m/eventing/egress-policy", viewer, nil, nil)
	if !strings.Contains(r.raw, `"in_force":true`) {
		t.Errorf("a policy in force was not reported: %s", r.raw)
	}
	if strings.Contains(r.raw, "soc.example.com") {
		t.Errorf("the status endpoint disclosed a rule: %s", r.raw)
	}

	// The dry-run permits what the policy permits...
	r = h.do("POST", "/v1/m/eventing/egress-policy/check", viewer,
		map[string]any{"endpoint": "https://soc.example.com/hooks"}, nil)
	if r.code != http.StatusOK || !strings.Contains(r.raw, `"permitted":true`) {
		t.Fatalf("an allow-listed destination was not permitted: %d %s", r.code, r.raw)
	}
	// ...refuses what it refuses...
	r = h.do("POST", "/v1/m/eventing/egress-policy/check", viewer,
		map[string]any{"endpoint": "https://attacker.example.com/x"}, nil)
	if !strings.Contains(r.raw, `"permitted":false`) {
		t.Fatalf("an unlisted destination was permitted: %s", r.raw)
	}
	if strings.Contains(r.raw, "soc.example.com") {
		t.Errorf("the dry-run disclosed the allow-list: %s", r.raw)
	}
	// ...and is not a better oracle than the create it stands in for: probing an
	// allow-listed host on a refused port must not be distinguishable from probing a
	// host that is not listed at all.
	onList := h.do("POST", "/v1/m/eventing/egress-policy/check", viewer,
		map[string]any{"endpoint": "https://soc.example.com:8443/x"}, nil).raw
	offList := h.do("POST", "/v1/m/eventing/egress-policy/check", viewer,
		map[string]any{"endpoint": "https://guess.example.com:8443/x"}, nil).raw
	strip := func(s, host string) string { return strings.ReplaceAll(s, host, "HOST") }
	if strip(onList, "soc.example.com") != strip(offList, "guess.example.com") {
		t.Errorf("the dry-run distinguishes an allow-listed host from an unknown one:\n on:  %s\n off: %s",
			onList, offList)
	}

	// A URL the SENDER would refuse must not dry-run as permitted, or the check is
	// answering a different question than the one that matters.
	r = h.do("POST", "/v1/m/eventing/egress-policy/check", viewer,
		map[string]any{"endpoint": "http://soc.example.com:8080/x"}, nil)
	if !strings.Contains(r.raw, `"permitted":false`) {
		t.Errorf("a cleartext URL dry-ran as permitted: %s", r.raw)
	}
}
