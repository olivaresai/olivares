// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package netguard

import "testing"

func TestNetprobe_RunsUnprivilegedAndProbesOnlyPlanTargets(t *testing.T) {
	p := ProbePlan{Interface: "ens4", Source: "192.0.2.20", Gateway: "192.0.2.1", DNS: []string{"192.0.2.53"}, Name: "product.example.test", Port: 443}
	if err := validateProbe(123, p); err != nil {
		t.Fatal(err)
	}
	if err := validateProbe(0, p); err == nil {
		t.Fatal("root probe accepted")
	}
	for _, change := range []func(*ProbePlan){func(p *ProbePlan) { p.Port = 22 }, func(p *ProbePlan) { p.Source = "127.0.0.1" }, func(p *ProbePlan) { p.DNS = []string{"https://foreign.example"} }, func(p *ProbePlan) { p.Interface = "../ens4" }, func(p *ProbePlan) { p.Name = "foreign.example/path" }} {
		bad := p
		change(&bad)
		if err := validateProbe(123, bad); err == nil {
			t.Fatal("unbounded probe accepted", bad)
		}
	}
}
