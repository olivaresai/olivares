// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package executor

import (
	"context"
	"strings"
	"testing"
)

func TestDockerSavedTenantOwnership(t *testing.T) {
	ctx := context.Background()
	daemon := newDockerFakeDaemon(t)
	backend := func(tenant string) *DockerBackend {
		b := NewDockerBackend(DockerConfig{Tenant: tenant})
		b.client, b.baseURL = daemon.srv.Client(), daemon.srv.URL
		return b
	}
	a, b := backend("tenant-a"), backend("tenant-b")
	desired := Desired{Tenant: "tenant-a", SubjectRef: "same-subject", Image: "agent:1"}
	cred := Credential{}
	pa, err := a.Plan(ctx, desired, cred)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(ctx, pa, cred); err != nil {
		t.Fatal(err)
	}
	desired.Tenant = "tenant-b"
	pb, err := b.Plan(ctx, desired, cred)
	if err != nil {
		t.Fatal(err)
	}
	if pb.Handle == pa.Handle || len(pb.Diff.Creates) != 1 {
		t.Fatalf("tenant collision: A=%q B=%q diff=%+v", pa.Handle, pb.Handle, pb.Diff)
	}
	destroy, err := b.DestroyPlan(ctx, desired, cred)
	if err != nil || !destroy.Diff.Empty() {
		t.Fatalf("B can retire A: %v %v", destroy, err)
	}
	if _, err := b.Apply(ctx, pb, cred); err != nil {
		t.Fatal(err)
	}
	rec := daemon.containers["/"+pb.Handle]
	if rec.labels[dockerTenantLabel] != "tenant-b" {
		t.Fatal("created workload lacks tenant ownership")
	}
	rec.labels[dockerTenantLabel] = "tenant-a"
	daemon.containers["/"+pb.Handle] = rec
	daemon.reqs = nil
	if _, err := b.Plan(ctx, desired, cred); err == nil {
		t.Fatal("plan adopted a foreign-owned workload")
	}
	if _, err := b.Observe(ctx, desired, cred); err == nil {
		t.Fatal("observe read a foreign-owned workload")
	}
	if _, err := b.DestroyPlan(ctx, desired, cred); err == nil {
		t.Fatal("retire adopted a foreign-owned workload")
	}
	if _, err := b.Apply(ctx, pb, cred); err == nil {
		t.Fatal("apply adopted a workload whose ownership changed after plan")
	}
	if _, err := b.Apply(ctx, pa, cred); err == nil {
		t.Fatal("apply accepted a foreign tenant plan")
	}
	desired.Tenant = "tenant-a"
	if _, err := b.Plan(ctx, desired, cred); err == nil {
		t.Fatal("backend accepted foreign desired tenant")
	}
	for _, call := range daemon.reqs {
		if !strings.HasPrefix(call, "GET ") {
			t.Fatalf("ownership refusal mutated daemon: %s", call)
		}
	}
	legacy := backend("")
	daemon.seed("legacy-agent", "agent:1", true)
	lp, err := legacy.Plan(ctx, Desired{SubjectRef: "legacy-agent", Image: "agent:1"}, cred)
	if err != nil || !lp.Diff.Empty() {
		t.Fatalf("legacy operator naming changed: %v %v", lp, err)
	}
}
