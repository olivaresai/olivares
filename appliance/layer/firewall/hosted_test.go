// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build appliance_host_hosted && linux

package firewall_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/firewall"
	"github.com/olivaresai/olivares/appliance/layer/firewall/policy"
	"github.com/olivaresai/olivares/appliance/layer/netguard"
)

// address is one address of `ip -j addr show`.
type address struct {
	Family        string `json:"family"`
	Local         string `json:"local"`
	Prefixlen     int    `json:"prefixlen"`
	Scope         string `json:"scope"`
	Dynamic       bool   `json:"dynamic"`
	ValidLifeTime int64  `json:"valid_life_time"`
}

// leases reads link's dynamic global addresses: the DHCPv4 lease, the DHCPv6 lease (a /128) and
// the SLAAC address (a /64).
func leases(t *testing.T, link string) map[string]address {
	t.Helper()
	out, err := exec.Command("/usr/sbin/ip", "-j", "addr", "show", "dev", link).Output()
	if err != nil {
		t.Fatalf("ip addr show dev %s: %v", link, err)
	}
	var links []struct {
		AddrInfo []address `json:"addr_info"`
	}
	if err := json.Unmarshal(out, &links); err != nil || len(links) != 1 {
		t.Fatalf("ip -j addr: %v", err)
	}
	got := map[string]address{}
	for _, a := range links[0].AddrInfo {
		if a.Scope != "global" || !a.Dynamic {
			continue
		}
		switch {
		case a.Family == "inet":
			got["dhcpv4"] = a
		case a.Family == "inet6" && a.Prefixlen == 128:
			got["dhcpv6"] = a
		case a.Family == "inet6" && a.Prefixlen == 64:
			got["slaac"] = a
		}
	}
	return got
}

// survive requires the three addresses to be present, to stay the same across one renewal
// interval and to be renewed within it: a valid lifetime refreshed by a DHCPv4 renewal, a
// DHCPv6 renewal and a router advertisement.
func survive(t *testing.T, phase, link string, wait time.Duration) {
	t.Helper()
	before := leases(t, link)
	for _, kind := range []string{"slaac", "dhcpv4", "dhcpv6"} {
		if _, ok := before[kind]; !ok {
			t.Fatalf("%s: %s has no %s address: %+v", phase, link, kind, before)
		}
	}
	start := time.Now()
	time.Sleep(wait)
	after := leases(t, link)
	elapsed := int64(time.Since(start).Seconds())
	for kind, b := range before {
		a, ok := after[kind]
		if !ok || a.Local != b.Local {
			t.Errorf("%s: the %s address %s did not survive: %+v", phase, kind, b.Local, after)
			continue
		}
		if a.ValidLifeTime <= b.ValidLifeTime-elapsed+1 {
			t.Errorf("%s: the %s lease was not renewed: valid %ds, then %ds after %ds", phase, kind, b.ValidLifeTime, a.ValidLifeTime, elapsed)
		}
	}
}

// TestHosted_LeasesAndSLAACSurviveLoadChangeAndRevert runs only in the disposable, privileged
// Debian 13 systemd container of the hosted job, as root, built with the appliance_host_hosted
// tag. The job gives NetworkManager a veth link whose peer serves DHCPv4, DHCPv6 and router
// advertisements with short lifetimes, and names the link and one renewal interval. The owner
// loads the real table with nft, changes it and reverts it; across each step the link keeps its
// SLAAC address and its DHCPv4 and DHCPv6 leases, and renews them.
func TestHosted_LeasesAndSLAACSurviveLoadChangeAndRevert(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Fatal("the hosted job runs this test as root in its systemd container")
	}
	link := os.Getenv("OLIVARES_FIREWALL_HOSTED_LINK")
	wait, err := time.ParseDuration(os.Getenv("OLIVARES_FIREWALL_HOSTED_RENEW"))
	if !policy.InterfaceName(link) || err != nil || wait <= 0 {
		t.Fatal("the hosted job names the NetworkManager link (OLIVARES_FIREWALL_HOSTED_LINK) and one renewal interval (OLIVARES_FIREWALL_HOSTED_RENEW)")
	}
	ctx := context.Background()
	clock, err := netguard.NewBootClock()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	o := &firewall.Owner{StateDir: filepath.Join(dir, "state"), RunDir: filepath.Join(dir, "run"), Kernel: firewall.NFT{}, Lock: &fakeLock{}, Clock: clock}
	t.Cleanup(func() { _ = exec.Command("/usr/sbin/nft", "delete", "table", "inet", "olivares").Run() })

	survive(t, "before the load", link, wait)
	initial := firewall.Initial(firewall.Selection{Enabled: true, Listen: policy.ListenManagement, ManagementInterfaces: []string{link}}, 22, []string{link})
	m, err := o.Install(ctx, initial)
	if err != nil || m.InputPolicy != "drop" || m.PolicyDigest != policy.Digest(initial) {
		t.Fatalf("the first load: %+v %v", m, err)
	}
	survive(t, "after the load", link, wait)

	candidate := firewall.Candidate(initial, firewall.Selection{Listen: policy.ListenLocal})
	id := operationID(1)
	if _, err := o.Apply(ctx, firewall.ApplyRequest{OperationID: id, Candidate: candidate, RevertAfter: firewall.MaxRevertAfter}); err != nil {
		t.Fatalf("the change: %v", err)
	}
	survive(t, "after the change", link, wait)

	if w, err := o.Revert(ctx, id); err != nil || w.State != firewall.WindowReverted {
		t.Fatalf("the revert: %+v %v", w, err)
	}
	survive(t, "after the revert", link, wait)

	table, err := firewall.NFT{}.Table(ctx)
	if err != nil || table.Comment != policy.TableComment+policy.Digest(initial) || table.InputPolicy != "drop" {
		t.Errorf("nft lists %+v (%v), want the confirmed policy", table, err)
	}
}
