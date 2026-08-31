// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime/sandboxrt"
	"github.com/olivaresai/olivares/modules/redteam"
	"github.com/olivaresai/olivares/modules/sandbox"
)

// This file is the XVII/XVIII ↔ sandboxrt seam adapter: it implements the
// testing-sandbox's Runner port (modules/sandbox/ports.go) AND the red-team's
// Sandbox port (modules/redteam/ports.go) by delegating to the real, isolated,
// egress-controlled execution runtime (core/runtime/sandboxrt). It is the ONLY
// layer that imports BOTH the AGPL modules and the AGPL runtime engine — the
// composition root — exactly as deployexec.go bridges deploy→executor and
// approvalbridge.go bridges the ApprovalGate seams. The modules never know
// which backend runs them; the engine selects gVisor/Firecracker by policy and
// enforces the hardening + the deny-by-default egress gate. The modules' honest
// defaults (the in-proc-mock runner / the offline sandbox) remain whenever no
// runtime is provisioned (sandboxrt_load.go returns nil).
//
// MINIMAL DATA (docs/SECURITY-HARDENING.md). The sandbox adapter returns synthetic per-step
// outputs the module clamps before persistence. The red-team adapter judges the
// captured response with the module's OWN deterministic redteam.Judge and returns
// only the probe id + a label — never the raw target response (which the module
// then never persists; only a detail hash).

// ---- sandbox.Runner adapter -----------------------------------------------------

// sandboxRunnerAdapter adapts the sandboxrt engine to sandbox.Runner. A synthetic
// scenario run carries a DENY-ALL egress scope (no Egress allowlist): the instance
// resolves steps against mocks and can reach nothing on the network.
type sandboxRunnerAdapter struct{ eng *sandboxrt.Engine }

var _ sandbox.Runner = sandboxRunnerAdapter{}

// Name reports the engine's effective backend ("gvisor"|"firecracker"|
// "unavailable"). When no backend passed preflight it is "unavailable" and a run
// fails closed — recorded honestly, never a faked microVM.
func (a sandboxRunnerAdapter) Name() string { n, _, _ := a.eng.Primary(); return n }

// Isolated reports the engine's effective isolation guarantee (false when no
// backend is available, so the module's Start() flags a degraded deployment).
func (a sandboxRunnerAdapter) Isolated() bool { _, iso, _ := a.eng.Primary(); return iso }

// Run executes the scenario steps in a fresh, hardened, egress-DENIED ephemeral
// instance and maps the neutral result back onto the module's RunOutcome. Even on
// a fault the destroyed flag is surfaced from the attestation (honest).
func (a sandboxRunnerAdapter) Run(ctx context.Context, tenant model.TenantID, spec sandbox.RunSpec) (sandbox.RunOutcome, error) {
	job := sandboxrt.Job{
		Tenant: tenant.String(),
		RunID:  "scenario:" + tenant.String(),
		Steps:  toRTSteps(spec.Steps),
		Mocks:  toRTMocks(spec.Mocks),
		// No Egress ⇒ the engine's egress proxy denies everything (synthetic only).
	}
	res, err := a.eng.Run(ctx, job)
	if err != nil {
		return sandbox.RunOutcome{Destroyed: res.Attestation.Destroyed}, err
	}
	return sandbox.RunOutcome{Steps: fromRTOutputs(res.Steps), Destroyed: res.Attestation.Destroyed}, nil
}

func toRTSteps(in []sandbox.Step) []sandboxrt.Step {
	out := make([]sandboxrt.Step, 0, len(in))
	for _, s := range in {
		out = append(out, sandboxrt.Step{Key: s.Key, Input: s.Input})
	}
	return out
}

func toRTMocks(in []sandbox.Mock) []sandboxrt.Mock {
	out := make([]sandboxrt.Mock, 0, len(in))
	for _, m := range in {
		out = append(out, sandboxrt.Mock{Resource: m.Resource, Response: m.Response})
	}
	return out
}

func fromRTOutputs(in []sandboxrt.StepOutput) []sandbox.StepOutput {
	out := make([]sandbox.StepOutput, 0, len(in))
	for _, s := range in {
		out = append(out, sandbox.StepOutput{Key: s.Key, Output: s.Output, MockHit: s.MockHit})
	}
	return out
}

// ---- redteam.Sandbox adapter ----------------------------------------------------

// redteamSandboxAdapter adapts the sandboxrt engine to redteam.Sandbox. It scopes
// the egress allowlist to EXACTLY the authorized target's host (defense in depth
// on top of the red-team module's consent gate, docs/SECURITY-HARDENING.md), delivers the probe
// over the gated path, and judges the response with the module's own Judge.
type redteamSandboxAdapter struct{ eng *sandboxrt.Engine }

var _ redteam.Sandbox = redteamSandboxAdapter{}

// Execute runs one probe against the target in an isolated instance whose ONLY
// egress is the engagement-scoped allowlist (the target host, every other
// destination denied), captures the response, and judges it deterministically.
func (a redteamSandboxAdapter) Execute(ctx context.Context, tenant model.TenantID, target redteam.Target, probe redteam.Probe) (redteam.ProbeResult, error) {
	// RED LINE (docs/SECURITY-HARDENING.md): only against an AUTHORIZED, governed target. The
	// module already gates consent; this is a second, independent check.
	if !target.Authorized {
		return skipped(probe.ID, "target is not authorized (RED LINE)"), nil
	}
	endpoint := strings.TrimSpace(target.Endpoint)
	if endpoint == "" {
		return skipped(probe.ID, "target has no reachable endpoint"), nil
	}
	rule, err := egressRuleForEndpoint(endpoint)
	if err != nil {
		return skipped(probe.ID, "target endpoint is not parseable"), nil
	}
	engagement := tenant.String() + "/" + target.ID.String()
	job := sandboxrt.Job{
		Tenant: tenant.String(),
		RunID:  target.ID.String(),
		Target: endpoint,
		Probe:  &sandboxrt.Probe{ID: probe.ID, Surface: probe.Surface, Payload: probe.Payload},
		Egress: sandboxrt.EgressPolicy{Engagement: engagement, Allow: []sandboxrt.EgressRule{rule}},
	}
	res, err := a.eng.Run(ctx, job)
	if err != nil {
		// An execution fault (e.g. no isolation backend available) is recorded as
		// OutcomeError and the run CONTINUES — never a false pass (docs/contracts/
		// §1). The module excludes errors from the score denominator.
		return redteam.ProbeResult{Executed: true, Outcome: redteam.OutcomeError, Reason: clampReason(err.Error()), Detail: probe.ID + "|error"}, nil
	}
	if !res.Reached {
		// The target was not reached through the gate (denied / unreachable): NOT
		// executed, never scored as a pass.
		return redteam.ProbeResult{Executed: false, Outcome: redteam.OutcomeError, Reason: "target not reached through the egress gate", Detail: probe.ID + "|unreached"}, nil
	}
	// Judge the captured response with the module's OWN deterministic judgement so
	// the verdict matches production exactly (the raw response is never returned).
	return redteam.Judge(probe, res.Response), nil
}

// skipped is the honest "probe not executed" result (never a false pass).
func skipped(probeID, reason string) redteam.ProbeResult {
	return redteam.ProbeResult{Executed: false, Outcome: redteam.OutcomeSkipped, Reason: reason, Detail: probeID + "|skipped"}
}

// egressRuleForEndpoint builds the single allowlist rule that scopes egress to
// EXACTLY the target endpoint's host and port. Everything else is denied by the
// proxy's deny-by-default (the target is the ONLY reachable destination).
func egressRuleForEndpoint(endpoint string) (sandboxrt.EgressRule, error) {
	host, port, err := parseEndpoint(endpoint)
	if err != nil {
		return sandboxrt.EgressRule{}, err
	}
	return sandboxrt.EgressRule{Host: host, Ports: []int{port}}, nil
}

// parseEndpoint extracts host + port from a target endpoint handle. It accepts a
// URL ("https://host:port/path") or a bare "host:port" / "host"; the scheme's
// default port applies when none is given.
func parseEndpoint(endpoint string) (string, int, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", 0, fmt.Errorf("empty endpoint")
	}
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		host := u.Hostname()
		if host == "" {
			return "", 0, fmt.Errorf("no host in endpoint")
		}
		if p := u.Port(); p != "" {
			port, perr := strconv.Atoi(p)
			if perr != nil {
				return "", 0, fmt.Errorf("invalid port")
			}
			return host, port, nil
		}
		return host, defaultPortForScheme(u.Scheme), nil
	}
	// Bare host[:port].
	if h, p, err := net.SplitHostPort(endpoint); err == nil {
		port, perr := strconv.Atoi(p)
		if perr != nil {
			return "", 0, fmt.Errorf("invalid port")
		}
		return h, port, nil
	}
	return endpoint, 443, nil
}

// defaultPortForScheme returns the conventional port for a URL scheme.
func defaultPortForScheme(scheme string) int {
	switch strings.ToLower(scheme) {
	case "http", "ws":
		return 80
	default:
		return 443
	}
}

// clampReason truncates a non-sensitive reason string for the probe result.
func clampReason(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		return s[:200]
	}
	return s
}
