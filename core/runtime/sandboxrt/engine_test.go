// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandboxrt

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"
)

// fakeBackend is a deterministic Backend test double. It reports a fixed
// isolation identity, optionally fails preflight, and in Execute (a) resolves
// steps against mocks like the real guest harness would, and (b) for a probe,
// performs a REAL HTTP delivery to the target THROUGH the engine-owned proxy — so
// the engine→proxy→backend egress wiring is exercised end-to-end with real
// sockets, while the unrunnable runsc/firecracker spawn is left to production.
type fakeBackend struct {
	name         string
	isolated     bool
	preflightErr error
	destroyOK    bool // false ⇒ DestroyVerified=false (an unverified-ephemeral run)
	hadNIC       bool // true ⇒ the run was given a NIC (red-team posture)
	execErr      error
}

func (f *fakeBackend) Name() string                    { return f.name }
func (f *fakeBackend) Isolated() bool                  { return f.isolated }
func (f *fakeBackend) Preflight(context.Context) error { return f.preflightErr }

func (f *fakeBackend) Execute(ctx context.Context, job Job, _ Profile, proxyAddr string) (BackendResult, error) {
	if f.execErr != nil {
		return BackendResult{InstanceID: f.name + "-x"}, f.execErr
	}
	// Resolve steps against mocks (the deterministic, no-network path).
	resolve := map[string]string{}
	for _, m := range job.Mocks {
		resolve[m.Resource] = m.Response
	}
	var steps []StepOutput
	for _, s := range job.Steps {
		if r, ok := resolve[s.Input]; ok {
			steps = append(steps, StepOutput{Key: s.Key, Output: r, MockHit: true})
		} else {
			steps = append(steps, StepOutput{Key: s.Key, Output: "[[mock-miss:" + s.Input + "]]"})
		}
	}
	br := BackendResult{
		Steps: steps, InstanceID: f.name + "-1",
		// Mirror real backends: a NIC is attached only when egress is needed.
		HadNIC: f.hadNIC && !job.Egress.denyAll(), Destroyed: true, DestroyVerified: f.destroyOK,
	}
	// Red-team delivery: dial the target THROUGH the proxy (which the engine scoped
	// to the allowlist). A denied destination yields Reached=false.
	if job.Probe != nil && proxyAddr != "" {
		resp, reached := deliverThroughProxy(ctx, proxyAddr, job.Target, job.Probe.Payload)
		br.Response, br.Reached = resp, reached
	}
	return br, nil
}

// deliverThroughProxy issues a GET to target through the proxy and returns the
// body + whether it was reached (a 200 from the real target).
func deliverThroughProxy(ctx context.Context, proxyAddr, target, _ string) (string, bool) {
	pu, _ := url.Parse("http://" + proxyAddr)
	client := &http.Client{
		Timeout:   3 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(pu), DisableKeepAlives: true},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", false
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return string(b), true
}

// TestEngineSyntheticRunIsolatedAndAttested proves a synthetic (deny-all) run
// resolves steps, records the real backend attestation, denies all egress, and
// reports a verified-destroyed ephemeral instance.
func TestEngineSyntheticRunIsolatedAndAttested(t *testing.T) {
	eng := New(WithBackend(&fakeBackend{name: "gvisor", isolated: true, destroyOK: true}))
	if !eng.Available() {
		t.Fatal("engine should be available with a passing backend")
	}
	res, err := eng.Run(context.Background(), Job{
		Tenant: "t1", RunID: "scenario-1",
		Steps: []Step{{Key: "s1", Input: "db"}, {Key: "s2", Input: "absent"}},
		Mocks: []Mock{{Resource: "db", Response: "ROWS"}},
		// No Egress ⇒ deny all.
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Steps) != 2 || res.Steps[0].Output != "ROWS" || !res.Steps[0].MockHit {
		t.Fatalf("steps not resolved: %+v", res.Steps)
	}
	if res.Steps[1].MockHit {
		t.Fatalf("mock-miss step marked as hit: %+v", res.Steps[1])
	}
	a := res.Attestation
	if a.Backend != "gvisor" || !a.Isolated || !a.ReadonlyRoot || !a.CapsDropped || !a.NoNewPrivs || !a.NoNIC || !a.EgressDenyDefault {
		t.Fatalf("attestation not fully hardened: %+v", a)
	}
	if a.Seccomp != seccompBaseline {
		t.Fatalf("seccomp profile = %q, want %q", a.Seccomp, seccompBaseline)
	}
	if a.EgressAllowed != 0 {
		t.Fatalf("deny-all run has %d allow rules, want 0", a.EgressAllowed)
	}
	if !a.Destroyed || !a.DestroyVerified {
		t.Fatalf("ephemeral not verified destroyed: %+v", a)
	}
}

// TestEngineNoBackendFailsClosed proves that with NO available backend the engine
// fails closed (ErrNoIsolation) and reports an honest degraded primary — never a
// faked microVM.
func TestEngineNoBackendFailsClosed(t *testing.T) {
	eng := New(WithBackend(&fakeBackend{name: "gvisor", isolated: true, preflightErr: errors.New("no runsc")}))
	if eng.Available() {
		t.Fatal("engine must not be available when preflight fails")
	}
	name, isolated, ok := eng.Primary()
	if ok || isolated || name != "unavailable" {
		t.Fatalf("primary on no-backend = (%q,%v,%v), want (unavailable,false,false)", name, isolated, ok)
	}
	if _, err := eng.Run(context.Background(), Job{RunID: "x"}); !errors.Is(err, ErrNoIsolation) {
		t.Fatalf("run error = %v, want ErrNoIsolation", err)
	}
}

// TestEnginePolicySelectionOrderAndPreference proves backends are selected by
// policy order, an explicit preference is honored, and an unavailable preference
// fails closed.
func TestEnginePolicySelectionOrderAndPreference(t *testing.T) {
	gv := &fakeBackend{name: "gvisor", isolated: true, destroyOK: true}
	fc := &fakeBackend{name: "firecracker", isolated: true, destroyOK: true}
	eng := New(WithBackend(gv), WithBackend(fc))

	if name, _, _ := eng.Primary(); name != "gvisor" {
		t.Fatalf("primary = %q, want gvisor (first in policy order)", name)
	}
	// Prefer firecracker explicitly.
	res, err := eng.Run(context.Background(), Job{RunID: "p", Prefer: "firecracker"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Attestation.Backend != "firecracker" {
		t.Fatalf("preferred backend = %q, want firecracker", res.Attestation.Backend)
	}
	// Prefer an unavailable backend ⇒ fail closed.
	if _, err := eng.Run(context.Background(), Job{RunID: "p", Prefer: "nope"}); !errors.Is(err, ErrNoIsolation) {
		t.Fatalf("unavailable preference error = %v, want ErrNoIsolation", err)
	}
}

// TestEngineRedteamEgressScopedToTarget proves the red-team path: with the
// allowlist scoped to EXACTLY the target the probe reaches it through the gate and
// the response is captured; with a deny-all (or off-target) scope it is denied.
func TestEngineRedteamEgressScopedToTarget(t *testing.T) {
	_, host, port := startTarget(t, "TARGET-SAID-OK")
	eng := New(WithBackend(&fakeBackend{name: "firecracker", isolated: true, destroyOK: true}))
	target := "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/"

	// Scoped to the target ⇒ reached, response captured, egress logged as allowed.
	scoped, err := eng.Run(context.Background(), Job{
		Tenant: "t1", RunID: "rt-1", Target: target,
		Probe:  &Probe{ID: "inj-01", Surface: "input", Payload: "PWNED?"},
		Egress: EgressPolicy{Engagement: "t1/rt-1", Allow: []EgressRule{{Host: host, Ports: []int{port}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !scoped.Reached || scoped.Response != "TARGET-SAID-OK" {
		t.Fatalf("scoped probe did not reach target: reached=%v resp=%q", scoped.Reached, scoped.Response)
	}
	if scoped.Attestation.EgressAllowed != 1 {
		t.Fatalf("scoped run allow-rules = %d, want 1", scoped.Attestation.EgressAllowed)
	}

	// Deny-all (no allowlist) ⇒ the SAME target is unreachable (the gate denies it).
	denied, err := eng.Run(context.Background(), Job{
		Tenant: "t1", RunID: "rt-2", Target: target,
		Probe: &Probe{ID: "inj-01", Surface: "input", Payload: "PWNED?"},
		// No Egress ⇒ deny all.
	})
	if err != nil {
		t.Fatal(err)
	}
	if denied.Reached || denied.Response != "" {
		t.Fatalf("deny-all probe REACHED the target — egress gate failed: %+v", denied)
	}
	if denied.Attestation.EgressDenied == 0 {
		t.Fatalf("deny-all run recorded no denied egress attempt: %+v", denied.Attestation)
	}
}

// TestEngineAttestationNICReflectsRealPosture proves the attestation's NoNIC
// reflects the REAL per-run network posture (a red-team run that was given a NIC
// is reported NoNIC=false), not the static profile default.
func TestEngineAttestationNICReflectsRealPosture(t *testing.T) {
	_, host, port := startTarget(t, "ok")
	eng := New(WithBackend(&fakeBackend{name: "firecracker", isolated: true, destroyOK: true, hadNIC: true}))
	target := "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/"
	res, err := eng.Run(context.Background(), Job{
		RunID: "rt", Target: target, Probe: &Probe{ID: "p"},
		Egress: EgressPolicy{Allow: []EgressRule{{Host: host, Ports: []int{port}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Attestation.NoNIC {
		t.Fatal("red-team run with a NIC reported NoNIC=true (dishonest)")
	}
	// A synthetic run reports NoNIC=true (no interface).
	syn, _ := eng.Run(context.Background(), Job{RunID: "s"})
	if !syn.Attestation.NoNIC {
		t.Fatal("synthetic run reported NoNIC=false")
	}
}

// TestEngineRedteamRequiresTarget proves a probe job without a target fails closed.
func TestEngineRedteamRequiresTarget(t *testing.T) {
	eng := New(WithBackend(&fakeBackend{name: "gvisor", isolated: true, destroyOK: true}))
	if _, err := eng.Run(context.Background(), Job{RunID: "x", Probe: &Probe{ID: "p"}}); !errors.Is(err, ErrNoTarget) {
		t.Fatalf("missing-target error = %v, want ErrNoTarget", err)
	}
}

// TestEngineUnverifiedDestructionSurfaced proves an unverified destruction is
// reported honestly (DestroyVerified=false), not assumed away.
func TestEngineUnverifiedDestructionSurfaced(t *testing.T) {
	eng := New(WithBackend(&fakeBackend{name: "gvisor", isolated: true, destroyOK: false}))
	res, err := eng.Run(context.Background(), Job{RunID: "u"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Attestation.DestroyVerified {
		t.Fatal("DestroyVerified=true despite backend reporting unverified")
	}
}

// TestEngineExecFaultReturnsAttestation proves a backend execution fault still
// yields an honest attestation (the run was attempted under the real backend).
func TestEngineExecFaultReturnsAttestation(t *testing.T) {
	eng := New(WithBackend(&fakeBackend{name: "gvisor", isolated: true, execErr: errors.New("boom")}))
	res, err := eng.Run(context.Background(), Job{RunID: "f"})
	if err == nil {
		t.Fatal("expected execution fault error")
	}
	if res.Attestation.Backend != "gvisor" {
		t.Fatalf("fault attestation backend = %q, want gvisor", res.Attestation.Backend)
	}
}
