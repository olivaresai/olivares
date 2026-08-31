// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/egress"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Unit G — what the deployment's own history means for an absent policy.
//
// Every test here drives the PUBLIC path (authoring over HTTP, or a real dispatch
// pass) rather than calling the decision helper. That is not style: this campaign
// found three separate tests that exercised a helper and would have passed with the
// behavior they claimed to pin completely absent.

// stubRollout is a fixed durable disposition.
type stubRollout struct {
	st  store.RolloutState
	err error
}

func (s stubRollout) EgressRollout(context.Context) (store.RolloutState, error) {
	if s.err != nil {
		return store.RolloutState{}, s.err
	}
	return s.st, nil
}

func mode(m store.RolloutMode) stubRollout {
	return stubRollout{st: store.RolloutState{
		Key: EgressRolloutControlKey, ClassifiedMode: m, CurrentMode: m, Generation: 1,
	}}
}

// preGateSubscription writes a subscription STRAIGHT INTO THE STORE, with no
// authoring check, and returns its id.
//
// It is how an upgrade is modeled honestly. A deployment classified into
// compatibility mode is one whose subscriptions were authored BEFORE the gate existed,
// so a fixture that creates them through the API on a fresh store describes the
// opposite: the compatibility line gets drawn over an empty tenant, because that
// create is itself the first decision the new binary makes. Unit F paid for this lesson
// once already — a fixture that describes a world that does not exist pins nothing.
func (h *harness) preGateSubscription(tenant model.TenantID, name, endpoint string) model.ID {
	h.t.Helper()
	// Sealed the way the harness's sealer seals, so the dispatcher can unseal it and
	// actually sign a delivery. A placeholder here would make every send fail at the
	// unseal, and the test would then be measuring the sealer rather than the control.
	sealed, serr := fakeSealer{}.Seal(context.Background(), tenant, []byte("pre-gate-secret"))
	if serr != nil {
		h.t.Fatalf("seal: %v", serr)
	}
	// Unit H: "pre-gate" describes the DESTINATION, not the writer. The writer here is this
	// binary, it carries the egress gate, and on this fresh database the fence is armed by
	// classification — so it proves it, read before the transaction like every other writer.
	gen := fenceGenerationOf(h.t, h.st)
	var id model.ID
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		rec := model.Record{
			colSubName: name, colSubEnabled: true, colSubTypes: "finding.reported",
			colSubEndpoint: endpoint, colSubSecret: sealed, colSubSecretHint: "x",
			colSubRole: "viewer", colSubOwnerActor: "pre-gate", colSubOwnerActorK: "user",
			colSubAuthType: authTypeNone,
		}
		if err := StampWriterProof(context.Background(), sc, rec, gen); err != nil {
			return err
		}
		created, err := repo.Create(context.Background(), rec)
		if err != nil {
			return err
		}
		id = model.ID(created.String(model.ColID))
		return nil
	}); err != nil {
		h.t.Fatalf("write a pre-gate subscription: %v", err)
	}
	return id
}

// queueDelivery writes an event and a due delivery STRAIGHT INTO THE STORE, with no publish.
//
// It exists so a test can control exactly when a delivery is attempted. Publishing goes through the
// bus and nudges a worker, so the delivery can leave while the fixture is still being arranged —
// which is how an earlier version of the window test below measured nothing at all. A row written
// here produces no nudge, so it moves only when the test calls dispatch.
func (h *harness) queueDelivery(tenant model.TenantID, subID model.ID) {
	h.t.Helper()
	ctx := context.Background()
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		events, err := sc.Ext(eventKind)
		if err != nil {
			return err
		}
		ev, err := events.Create(ctx, model.Record{
			colEvSeq: int64(1), colEvEventID: model.NewID().String(),
			colEvType: "finding.reported", colEvSource: "scanner",
			colEvOccurredAt: h.clk.Now().String(), colEvPayload: `{"t":"x"}`,
		})
		if err != nil {
			return err
		}
		deliveries, err := sc.Ext(deliveryKind)
		if err != nil {
			return err
		}
		_, err = deliveries.Create(ctx, model.Record{
			colDelSubRef: subID.String(), colDelEventRef: ev.String(model.ColID),
			colDelEventID: ev.String(colEvEventID), colDelEventSeq: int64(1),
			colDelEventType: "finding.reported", colDelStatus: statusQueued,
			colDelOrigin: originLive, colDelAttempts: int64(0),
			colDelNextAt: h.clk.Now().String(),
		})
		return err
	}); err != nil {
		h.t.Fatalf("queue a delivery directly: %v", err)
	}
}

// TestFreshInstallWithNoPolicyRefusesAuthoring is the defect this unit exists to fix.
// Unit F's absent policy PERMITS, which is right for an upgrade and, on a deployment
// that never had a destination to protect, is allow-all with no expiry date.
func TestFreshInstallWithNoPolicyRefusesAuthoring(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := newHarness(t, WithEgressRollout(mode(store.RolloutEnforced)))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.test", "editor")

	got := h.do("POST", "/v1/m/eventing/subscriptions", editor, map[string]any{
		"name": "siem", "endpoint": srv.URL, "event_types": []string{"finding.reported"},
		"role": "viewer",
	}, nil)
	if got.code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: an enforced deployment with no authored policy has nothing that could permit a destination", got.code)
	}
	// The message must name the REMEDIATION OWNER, because that is the only part of the
	// refusal the caller can act on. It must still disclose nothing about any policy —
	// there is no policy.
	if body := strings.ToLower(got.raw); !strings.Contains(body, "platform operator") {
		t.Fatalf("the refusal does not tell the caller who can fix it: %s", got.raw)
	}
}

// TestPolicyOptionalWithNoPolicyPermits is the recorded-decision half. It must behave
// exactly as the pre-unit world did, or the mode is not the honest name for it.
func TestPolicyOptionalWithNoPolicyPermits(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := newHarness(t, WithEgressRollout(mode(store.RolloutPolicyOptional)))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.test", "editor")
	h.createSubscription(editor, tenant, map[string]any{
		"name": "siem", "endpoint": srv.URL, "event_types": []string{"finding.reported"},
		"role": "viewer",
	})
	h.publishFinding(tenant, "scanner", "drift", "t")
	h.settle(tenant)
	if hits.Load() == 0 {
		t.Fatal("a deployment that recorded the control as optional stopped delivering")
	}
}

// TestUpgradeKeepsADestinationItAlreadyHadWhenAPolicyArrives is the whole content of
// compatibility mode. An operator who authors a narrow policy must not silently break
// the destinations the estate was already using; they must be able to SEE them and
// then decide.
func TestUpgradeKeepsADestinationItAlreadyHadWhenAPolicyArrives(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := newHarness(t, WithEgressRollout(mode(store.RolloutLegacyCompat)))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	// Authored before the gate existed — which is what "this deployment predates the
	// control" means.
	h.preGateSubscription(tenant, "siem", srv.URL)

	// The operator now authors a policy that does NOT cover the stored destination.
	h.mod.egress = fixedPolicy{pol: allowHost("soc.example.com")}
	h.mod.resolver = loopbackResolver{}

	h.publishFinding(tenant, "scanner", "drift", "t")
	h.settle(tenant)

	if hits.Load() == 0 {
		t.Fatal("a destination this deployment already had stopped delivering the moment a policy was authored — which is the breaking change compatibility mode exists to prevent")
	}
	rows := h.deliveryRows(tenant)
	if len(rows) != 1 {
		t.Fatalf("want one delivery row, got %d", len(rows))
	}
	if got := rows[0].String(colDelStatus); got != statusDelivered {
		t.Fatalf("status = %q, want a delivery", got)
	}
	rep, err := h.mod.compat.report(context.Background(), tenant, allowHost("soc.example.com"))
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if !rep.Seeded {
		t.Fatal("the compatibility record was never drawn, so a refusal could not be told from a grandfathered destination")
	}
	if rep.Subscriptions != 1 {
		t.Fatalf("the record covers %d subscriptions, want the 1 that predated the gate", rep.Subscriptions)
	}
	if rep.StillNeeded != 1 {
		t.Fatalf("the report says %d destinations would break; want 1 — an operator who cannot count them cannot consent", rep.StillNeeded)
	}
	if len(rep.Authorities) != 1 || rep.Authorities[0].Covered {
		t.Fatalf("report authorities = %+v", rep.Authorities)
	}
}

// TestCompatibilityNeverGrandfathersANewDestination is the other half of the promise.
// Compatibility preserves what a deployment HAD; a create must never inherit it, or
// the mode is just allow-all with paperwork.
func TestCompatibilityNeverGrandfathersANewDestination(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer second.Close()

	h := newHarness(t, WithEgressRollout(mode(store.RolloutLegacyCompat)))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.test", "editor")
	h.preGateSubscription(tenant, "siem", first.URL)

	h.mod.egress = fixedPolicy{pol: allowHost("soc.example.com")}
	h.mod.resolver = loopbackResolver{}

	// A NEW subscription pointing at a destination the deployment did not have.
	got := h.do("POST", "/v1/m/eventing/subscriptions", editor, map[string]any{
		"name": "second", "endpoint": second.URL, "event_types": []string{"finding.reported"},
		"role": "viewer",
	}, nil)
	if got.code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: a create inherited a compatibility exception", got.code)
	}
	// And the FIRST one's own destination, re-offered as a create, is refused too: the
	// exception belongs to the subscription that had it, not to the authority.
	got = h.do("POST", "/v1/m/eventing/subscriptions", editor, map[string]any{
		"name": "copycat", "endpoint": first.URL, "event_types": []string{"finding.reported"},
		"role": "viewer",
	}, nil)
	if got.code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: a create borrowed another subscription's grandfathered destination", got.code)
	}
}

// TestCompatibilityKeepsAnEndpointStrictParsingRejects is the correction an
// adversarial review produced, and it is the case the target market actually has:
// unit F deliberately lets an unconfigured estate keep using host syntax IDNA2008
// refuses — an underscore in a label, which is ordinary in internal names. Recording
// only canonical authorities would have broken exactly those the moment a policy was
// authored, while reporting a count and calling it compatibility.
func TestCompatibilityKeepsAnEndpointStrictParsingRejects(t *testing.T) {
	h := newHarness(t, WithEgressRollout(mode(store.RolloutLegacyCompat)))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// Written straight into the store: the estate that has one of these predates every
	// gate, which is the premise.
	const raw = "https://siem_internal.corp:8443/collect"
	// Unit H: the DESTINATION predates every gate; the writer is this binary and proves it.
	gen := fenceGenerationOf(t, h.st)
	var subID model.ID
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		rec := model.Record{
			colSubName: "legacy", colSubEnabled: true, colSubTypes: "finding.reported",
			colSubEndpoint: raw, colSubSecret: "sealed:x", colSubSecretHint: "x",
			colSubRole: "viewer", colSubOwnerActor: "t", colSubOwnerActorK: "user",
			colSubAuthType: authTypeNone,
		}
		if err := StampWriterProof(context.Background(), sc, rec, gen); err != nil {
			return err
		}
		created, err := repo.Create(context.Background(), rec)
		if err != nil {
			return err
		}
		subID = model.ID(created.String(model.ColID))
		return nil
	}); err != nil {
		t.Fatalf("seed a pre-gate subscription: %v", err)
	}
	if _, err := egress.ParseDestination(raw); err == nil {
		t.Fatal("the fixture is wrong: the strict parser accepts this host, so it does not exercise the legacy grammar")
	}

	// A policy arrives. It cannot possibly name this host — no rule can express a name
	// the canonicalizer refuses — so without the legacy grammar this destination is
	// simply lost.
	h.mod.egress = fixedPolicy{pol: allowHost("soc.example.com")}
	dd := h.mod.decider()
	d, _ := dd.authorize(context.Background(), egressRequest{
		Tenant: tenant, Purpose: EgressSend, URL: raw, SubscriptionRef: subID,
	})
	if !d.Permitted {
		t.Fatalf("a pre-existing endpoint the strict parser refuses was denied (code %q); counting the breakage is not grandfathering it", d.Code)
	}
	if d.Code != egress.CodeLegacyException {
		t.Fatalf("code = %q, want %q so the delivery is countable on the retirement list", d.Code, egress.CodeLegacyException)
	}
	if len(d.Pin) != 0 {
		t.Fatal("a legacy-spelling permit must carry no pin: nothing was resolved, and unit F's absent-policy path never pinned it either")
	}
	// A CREATE with the same spelling is still refused.
	d, _ = dd.authorize(context.Background(), egressRequest{
		Tenant: tenant, Purpose: EgressCreate, URL: raw, SubscriptionRef: subID,
	})
	if d.Permitted {
		t.Fatal("a create used the legacy grammar; compatibility must never manufacture a new entitlement")
	}
	// And the report says WHICH grammar kept it, because an operator planning to enforce
	// needs to know this one can never be covered by a rule.
	rep, err := h.mod.compat.report(context.Background(), tenant, allowHost("soc.example.com"))
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if len(rep.Authorities) != 1 || rep.Authorities[0].Kind != string(authorityLegacyRawV1) {
		t.Fatalf("report authorities = %+v", rep.Authorities)
	}
	if rep.Authorities[0].Covered {
		t.Fatal("a legacy-spelling authority was reported as covered by a policy that cannot name it")
	}
}

// TestTheCompatibilityLineIsDrawnAtFirstDecisionNotAtFirstPolicy is the boundary
// correction. Drawing the line when a policy first NEEDS an exception would record
// every subscription created during the operator's delay as pre-existing, so the
// grandfathered set would grow for as long as nobody authored a policy.
func TestTheCompatibilityLineIsDrawnAtFirstDecisionNotAtFirstPolicy(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer first.Close()
	later := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer later.Close()

	h := newHarness(t, WithEgressRollout(mode(store.RolloutLegacyCompat)))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.test", "editor")

	// One subscription predates the gate. The new binary has decided nothing yet.
	h.preGateSubscription(tenant, "siem", first.URL)
	if _, err := h.mod.compat.report(context.Background(), tenant, egress.Policy{}); err != nil {
		t.Fatalf("report: %v", err)
	}
	// A create is the tenant's FIRST decision on this binary, so the line is drawn before
	// it is answered — and the create itself is therefore NOT on the list.
	h.createSubscription(editor, tenant, map[string]any{
		"name": "second", "endpoint": later.URL, "event_types": []string{"finding.reported"},
		"role": "viewer",
	})
	rep, err := h.mod.compat.report(context.Background(), tenant, egress.Policy{})
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if !rep.Seeded {
		t.Fatal("the line was not drawn at the first decision, so it can still move")
	}
	if rep.Subscriptions != 1 || len(rep.Authorities) != 1 {
		t.Fatalf("the grandfathered set covers %d subscriptions / %d authorities; want the 1 that predated the gate — deferring the line would let an operator's delay accumulate grandfathered destinations", rep.Subscriptions, len(rep.Authorities))
	}
	drawn := rep.SeedDigest

	// A third subscription, still with no policy authored. Permitted, because nothing
	// constrains it yet — and still not grandfathered.
	h.createSubscription(editor, tenant, map[string]any{
		"name": "third", "endpoint": later.URL, "event_types": []string{"finding.reported"},
		"role": "viewer",
	})
	rep2, err := h.mod.compat.report(context.Background(), tenant, egress.Policy{})
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if rep2.SeedDigest != drawn {
		t.Fatal("the compatibility set changed after the line was drawn")
	}
}

// TestAnUnreadableRolloutStateParksRatherThanBurningTheLadder is the fix for a defect
// that also affected merged unit F: a claim increments attempts BEFORE the destination
// is decided, a retryable refusal is requeued, and an exhausted ladder dead-letters —
// so a control plane that could not read its own state would spend the retry ladder on
// its own outage and destroy the evidence it was carrying.
func TestAnUnreadableRolloutStateParksRatherThanBurningTheLadder(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := newHarness(t, WithEgressRollout(mode(store.RolloutPolicyOptional)))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.test", "editor")
	h.createSubscription(editor, tenant, map[string]any{
		"name": "siem", "endpoint": srv.URL, "event_types": []string{"finding.reported"},
		"role": "viewer",
	})
	// The rollout store goes dark BEFORE anything is published, so no delivery can slip
	// out while the fixture is being set up — the capture path does not consult it, so
	// the delivery row is still written and is exactly what must survive the outage.
	h.mod.rollout = stubRollout{err: errors.New("store unavailable")}
	h.mod.rolloutState = rolloutCache{}
	h.publishFinding(tenant, "scanner", "drift", "t")
	waitFor(t, "the delivery row to be captured", func() bool {
		return len(h.deliveryRows(tenant)) == 1
	})

	// More passes than the retry ladder is long. If the outage were paid for out of the
	// ladder, this would dead-letter.
	for i := 0; i < 6; i++ {
		h.dispatch(tenant)
	}
	if hits.Load() != 0 {
		t.Fatal("a delivery went out while the plane could not establish whether the control was in force")
	}
	rows := h.deliveryRows(tenant)
	if len(rows) != 1 {
		t.Fatalf("want one delivery row, got %d", len(rows))
	}
	if got := rows[0].String(colDelStatus); got == statusDead {
		t.Fatal("the delivery was dead-lettered because the control plane could not read its own rollout state — evidence destroyed by an outage")
	}
	if got := rows[0].Int(colDelAttempts); got != 0 {
		t.Fatalf("attempts = %d, want 0: a park is pre-attempt and must not consume the ladder", got)
	}
	// The reason is on the ROW, not only in a log line. Unit F's per-delivery refusal
	// already gave an operator that, and a park that took it away would be a worse answer
	// to "why did this not go out" even though it costs less.
	if got := rows[0].String(colDelLastStatus); got != "egress_rollout_unavailable" {
		t.Fatalf("outcome = %q, want egress_rollout_unavailable so the operator can tell a plane outage from a refused destination", got)
	}

	// And it recovers on its own once the state is readable again. The clock has to move
	// because a park pushes the recheck out, exactly as the kill switch does.
	h.mod.rollout = mode(store.RolloutPolicyOptional)
	h.mod.rolloutState = rolloutCache{}
	h.clk.advance(disabledRecheck + time.Second)
	h.settle(tenant)
	if hits.Load() == 0 {
		t.Fatal("the parked delivery never resumed after the outage cleared")
	}
}

// TestAnUnwiredRolloutSeamBehavesAsBeforeTheUnit pins the compatibility contract for a
// custom embedder that has not adopted the seam. It is the upgrade-safe reading — and
// it is exactly why the first-party binary refuses to boot without it rather than
// relying on this.
func TestAnUnwiredRolloutSeamBehavesAsBeforeTheUnit(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := newHarness(t) // no WithEgressRollout
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.test", "editor")
	h.createSubscription(editor, tenant, map[string]any{
		"name": "siem", "endpoint": srv.URL, "event_types": []string{"finding.reported"},
		"role": "viewer",
	})
	h.publishFinding(tenant, "scanner", "drift", "t")
	h.settle(tenant)
	if hits.Load() == 0 {
		t.Fatal("an embedder that has not wired the rollout seam stopped delivering")
	}
}

// TestTheEgressStatusSurfaceReportsTheDispositionAndTheDiff. A control an operator
// cannot see is not one they can rely on, and "your destination was refused" without
// "and a platform operator owns the fix" is a support ticket.
func TestTheEgressStatusSurfaceReportsTheDispositionAndTheDiff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := newHarness(t, WithEgressRollout(mode(store.RolloutLegacyCompat)))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.test", "editor")
	adminTok := h.roleToken(admin, tenant, "a@acme.test", "admin")
	h.preGateSubscription(tenant, "siem", srv.URL)
	h.mod.egress = fixedPolicy{pol: allowHost("soc.example.com")}
	h.mod.resolver = loopbackResolver{}
	// One decision, so the line is drawn.
	h.publishFinding(tenant, "scanner", "drift", "t")
	h.settle(tenant)

	// A READ-tier caller gets the DISPOSITION — which is a fact about the deployment and tells
	// them who owns the remediation — and nothing that is a function of the operator's
	// allow-list. StillNeeded is computed by testing each grandfathered authority against the
	// policy, so for a tenant with one known destination it answers "is my destination on the
	// operator's list?": the membership oracle unit F collapsed every denial message to avoid,
	// reopened through a count. An earlier revision of this unit served it at read tier.
	got := h.do("GET", "/v1/m/eventing/egress-policy", editor, nil, nil)
	if got.code != http.StatusOK {
		t.Fatalf("status surface: %d", got.code)
	}
	body := got.raw
	for _, want := range []string{`"mode":"legacy_compat"`, `"classified_mode":"legacy_compat"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("status body is missing %s: %s", want, body)
		}
	}
	if strings.Contains(body, `"still_needed"`) {
		t.Fatalf("a read-tier caller was served a policy-derived compatibility count, which is a membership oracle: %s", body)
	}
	// An ADMIN caller gets it, because they may already read the itemized report.
	if got := h.do("GET", "/v1/m/eventing/egress-policy", adminTok, nil, nil); !strings.Contains(got.raw, `"still_needed":1`) {
		t.Fatalf("an admin caller was NOT served the compatibility summary: %s", got.raw)
	}
	// The itemized list NAMES HOSTS, so it is admin tier and a read-tier caller does not
	// get it.
	if got := h.do("GET", "/v1/m/eventing/egress-policy/compat", editor, nil, nil); got.code == http.StatusOK {
		t.Fatal("a read-tier caller was served the itemized compatibility list, which names an operator's collectors")
	}
	got = h.do("GET", "/v1/m/eventing/egress-policy/compat", adminTok, nil, nil)
	if got.code != http.StatusOK {
		t.Fatalf("admin compat report: %d %s", got.code, got.raw)
	}
	if !strings.Contains(got.raw, `"still_needed":1`) {
		t.Fatalf("the compat report does not count what enforcing would break: %s", got.raw)
	}
}

// flakyPolicy succeeds for the first n reads and fails afterwards, so a test can put an outage
// BETWEEN the pass-level readiness check and the send.
type flakyPolicy struct {
	pol      egress.Policy
	ok       *atomic.Int64
	failFrom int64
}

func (f flakyPolicy) EgressPolicy(context.Context, model.TenantID) (egress.Policy, error) {
	if f.ok.Add(1) > f.failFrom {
		return egress.Policy{}, errors.New("policy store unavailable")
	}
	return f.pol, nil
}

// TestAnOutageThatBeginsAfterTheReadinessCheckStillParks is the window the pass-level check does
// not cover, and it is the one an adversarial review reproduced against this unit: readiness reads
// the policy, `claim` increments attempts, and THEN the send reads it again. If that second read
// fails, the refusal was already correct — but it was charged to the retry ladder, so a long enough
// outage still walked the ladder to its end and dead-lettered the evidence.
//
// Every earlier test arranged the outage BEFORE the snapshot, which is exactly why none of them
// could catch this.
func TestAnOutageThatBeginsAfterTheReadinessCheckStillParks(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := newHarness(t, WithEgressRollout(mode(store.RolloutEnforced)))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	host, _, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	h.mod.resolver = loopbackResolver{}
	subID := h.preGateSubscription(tenant, "siem", srv.URL)

	// From here the FIRST read of the policy in each pass succeeds — the readiness check — and
	// every later one fails, which is the send. The delivery is written directly so nothing can
	// attempt it before the window is armed.
	var reads atomic.Int64
	h.mod.egress = flakyPolicy{pol: allowHost(host), ok: &reads, failFrom: 1}
	h.queueDelivery(tenant, subID)

	for i := 0; i < 6; i++ {
		reads.Store(0)
		h.clk.advance(2 * egressParkRecheck)
		h.dispatch(tenant)
	}
	if hits.Load() != 0 {
		t.Fatal("a delivery went out while the policy could not be read")
	}
	rows := h.deliveryRows(tenant)
	if len(rows) != 1 {
		t.Fatalf("want one delivery row, got %d", len(rows))
	}
	if got := rows[0].String(colDelStatus); got == statusDead {
		t.Fatal("the delivery was dead-lettered because the policy store became unreadable AFTER the pass snapshot — the ladder paid for the plane's outage")
	}
	if got := rows[0].Int(colDelAttempts); got != 0 {
		t.Fatalf("attempts = %d, want 0: a park restores the attempt the claim consumed, however late the outage began", got)
	}
	if got := rows[0].String(colDelLastStatus); got != "egress_policy_unavailable" {
		t.Fatalf("outcome = %q, want egress_policy_unavailable", got)
	}
}

// TestASeedMarkerWithoutItsExceptionsIsNotComplete. The marker was introduced because the absence
// of exception rows cannot prove seeding ran; reducing "complete" to "the marker exists" moved that
// same defect one level up. A restore that keeps the seed and loses the exceptions produces a report
// that looks complete and describes a set that has lost every member.
//
// The shape is reproduced through the store's own API rather than by deleting rows behind it, and
// that is not a shortcut — it is the only way available, because the exception table is append-only
// and the engine refuses a delete. What matters is the OBSERVABLE the restore produces: a marker
// claiming a set the surviving rows do not reproduce.
func TestASeedMarkerWithoutItsExceptionsIsNotComplete(t *testing.T) {
	h := newHarness(t, WithEgressRollout(mode(store.RolloutLegacyCompat)))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	ctx := context.Background()

	// A seed row that claims one recorded exception, with none present.
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(egressSeedKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(ctx, model.Record{
			colSeedBatch: "restored", colSeedSubs: int64(1), colSeedExcs: int64(1),
			colSeedUnparsed: int64(0), colSeedDigest: "d1e5f0a0",
		})
		return err
	}); err != nil {
		t.Fatalf("write the restored seed row: %v", err)
	}

	// ensureSeed must NOT accept it, because "the marker is there" is not "the marked set is
	// intact" — and this is the read every decision goes through.
	if err := h.mod.compat.ensureSeed(ctx, tenant); err == nil {
		t.Fatal("a seed row whose set does not match its own digest was accepted as a complete record")
	}
	rep, err := h.mod.compat.report(ctx, tenant, allowHost("soc.example.com"))
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if !rep.Seeded {
		t.Fatal("the marker exists, so the report must say the line was drawn")
	}
	if rep.Intact {
		t.Fatal("a record that does not reproduce its own seed reported itself intact: a coverage proof would pass over a set with lost members")
	}
	if rep.IntegrityNote == "" {
		t.Fatal("the report does not say WHAT disagrees, so an operator cannot act on it")
	}
}
