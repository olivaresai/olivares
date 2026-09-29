// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package base

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/answers"
)

func networkHost(t *testing.T) (CloudInitHost, Input) {
	t.Helper()
	root := t.TempDir()
	in := answersFixture(t, "olivares.example.test")
	for p, v := range map[string]string{"usr/bin/cloud-init": "installed", "run/cloud-init/status.json": cloudInitDone, "run/cloud-init/result.json": cloudInitResult, "proc/sys/kernel/hostname": in.Answers.Hostname, "root/.ssh/authorized_keys": strings.Join(in.Answers.SSHAuthorizedKeys, "\n"), "etc/NetworkManager/system-connections/cloud-init-ens4.nmconnection": "[connection]\nid=cloud-init ens4\ninterface-name=ens4\n", "var/lib/cloud/instance/network-config.json": `{"version":2,"ethernets":{"ens4":{"dhcp4":true}}}`} {
		place(t, root, p, v)
	}
	observed := []NetworkProfile{{Interface: "ens4", Managed: true, Filename: "/etc/NetworkManager/system-connections/cloud-init-ens4.nmconnection", IPv4Method: "auto", IPv6Method: "auto"}}
	return CloudInitHost{Host: Host{Root: root}, Network: func(context.Context) ([]NetworkProfile, error) { return observed, nil }}, in
}

func TestFirstBoot_StaticModeWithoutACloudInitNetworkSourceIsRefused(t *testing.T) {
	s, in := networkHost(t)
	in.Answers.Network = answers.Network{Mode: "static", Interfaces: []answers.NetworkInterface{{Name: "ens4", IPv4: &answers.AddressFamily{Addresses: []string{"192.0.2.10/24"}}}}}
	if err := os.Remove(filepath.Join(s.Host.Root, "var/lib/cloud/instance/network-config.json")); err != nil {
		t.Fatal(err)
	}
	for _, carrier := range []string{"file:answers.json", "systemd-credential:olivares.appliance.answers"} {
		in.Source = carrier
		_, err := s.Apply(context.Background(), in)
		if err == nil || !strings.Contains(err.Error(), "static_network_source_missing") {
			t.Fatalf("static answers without cloud-init source: %v", err)
		}
	}
}

func TestFirstBoot_RefusesReadyWhenTheEffectiveRendererIsNotNetworkManager(t *testing.T) {
	s, in := networkHost(t)
	if _, err := s.Apply(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	s.Network = func(context.Context) ([]NetworkProfile, error) {
		return []NetworkProfile{{Interface: "ens4", Managed: true, Filename: "/run/network/interfaces"}}, nil
	}
	if _, err := s.Apply(context.Background(), in); err == nil || !strings.Contains(err.Error(), "network_renderer_not_networkmanager") {
		t.Fatalf("wrong renderer accepted: %v", err)
	}
	s.Network = func(context.Context) ([]NetworkProfile, error) { return nil, errors.New("bus unavailable") }
	if _, err := s.Apply(context.Background(), in); err == nil {
		t.Fatal("unmeasured renderer accepted")
	}
}

func TestFirstBoot_RefusesReadyWithASecondNetworkOwner(t *testing.T) {
	for _, tc := range []struct{ p, body string }{
		{"etc/network/interfaces", "auto lo\niface lo inet loopback\niface ens4 inet dhcp\n"},
		{"etc/network/interfaces.d/ens4", "iface ens4 inet static\n"},
		{"etc/sysconfig/network-scripts/ifcfg-ens4", "DEVICE=ens4\n"},
		{"etc/systemd/network/10-ens4.network", "[Match]\nName=ens4\n"},
		{"etc/netplan/10-ens4.yaml", "network:\n  version: 2\n  renderer: networkd\n  ethernets:\n    ens4:\n      dhcp4: true\n"},
	} {
		t.Run(tc.p, func(t *testing.T) {
			s, in := networkHost(t)
			place(t, s.Host.Root, tc.p, tc.body)
			if _, err := s.Apply(context.Background(), in); err == nil || !strings.Contains(err.Error(), "second_network_owner") {
				t.Fatalf("second owner accepted: %v", err)
			}
		})
	}
}

func TestFirstBoot_DnsPrecedenceIsComparedInOrder(t *testing.T) {
	for _, tc := range []struct {
		name            string
		servers, search []string
		refused         bool
	}{
		{"planned order", []string{"192.0.2.53", "192.0.2.54", "2001:db8::53"}, []string{"a.example", "b.example"}, false},
		{"servers reordered", []string{"192.0.2.54", "192.0.2.53", "2001:db8::53"}, []string{"a.example", "b.example"}, true},
		{"search reordered", []string{"192.0.2.53", "192.0.2.54", "2001:db8::53"}, []string{"b.example", "a.example"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, in := networkHost(t)
			// NetworkManager keeps server precedence within each address family.
			in.Answers.Network = answers.Network{Mode: "static", Interfaces: []answers.NetworkInterface{{Name: "ens4", IPv4: &answers.AddressFamily{Addresses: []string{"192.0.2.10/24"}}}},
				DNS: &answers.DNS{Servers: []string{"192.0.2.53", "2001:db8::53", "192.0.2.54"}, Search: []string{"a.example", "b.example"}}}
			s.Network = func(context.Context) ([]NetworkProfile, error) {
				return []NetworkProfile{{Interface: "ens4", Managed: true, Filename: "/etc/NetworkManager/system-connections/cloud-init-ens4.nmconnection",
					IPv4Method: "manual", IPv4: []string{"192.0.2.10/24"}, IPv6Method: "auto", DNSServers: tc.servers, DNSSearch: tc.search}}, nil
			}
			_, err := s.Apply(context.Background(), in)
			refused := err != nil && strings.Contains(err.Error(), "static_network_dns_mismatch")
			if refused != tc.refused || !tc.refused && err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
		})
	}
}

func TestFirstBoot_VendorNetworkFilesRequireAnInactiveDisabledOwner(t *testing.T) {
	dormant := NetworkdUnit{LoadState: "loaded", ActiveState: "inactive", UnitFileState: "disabled"}
	for _, tc := range []struct {
		name    string
		state   NetworkdState
		err     error
		refusal string
	}{
		{"disabled", NetworkdState{Service: dormant, Socket: dormant}, nil, ""},
		{"not loaded disabled", NetworkdState{Service: NetworkdUnit{NotLoaded: true, UnitFileState: "disabled"}, Socket: dormant}, nil, ""},
		{"absent socket", NetworkdState{Service: dormant, Socket: NetworkdUnit{NotLoaded: true, FileAbsent: true}}, nil, ""},
		{"unknown reader", NetworkdState{}, nil, "network_owner_unmeasured"},
		{"bus error", NetworkdState{Service: dormant, Socket: dormant}, errors.New("bus unavailable"), "network_owner_unmeasured"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, in := networkHost(t)
			place(t, s.Host.Root, "usr/lib/systemd/network/80-container-host0.network", "[Match]\nName=host0\n[Network]\nDHCP=yes\n")
			observed := 0
			s.Networkd = func(context.Context) (NetworkdState, error) { observed++; return tc.state, tc.err }
			s.Host.Run = func(context.Context, string, ...string) ([]byte, error) {
				t.Fatal("observe must never execute a program")
				return nil, errors.New("forbidden")
			}
			_, err := s.Apply(context.Background(), in)
			if observed != 1 {
				t.Fatalf("networkd reads=%d, want1", observed)
			}
			if tc.refusal == "" && err != nil || tc.refusal != "" && (err == nil || !strings.Contains(err.Error(), tc.refusal)) {
				t.Fatalf("want %q, got %v", tc.refusal, err)
			}
		})
	}
	for _, dir := range []string{"etc", "run"} {
		t.Run(dir+" intent remains conflicting", func(t *testing.T) {
			s, in := networkHost(t)
			place(t, s.Host.Root, "usr/lib/systemd/network/80-container-host0.network", "[Match]\nName=host0\n")
			place(t, s.Host.Root, dir+"/systemd/network/10-admin.network", "[Match]\nName=ens4\n[Network]\nDHCP=yes\n")
			s.Networkd = func(context.Context) (NetworkdState, error) {
				t.Fatal("local intent must refuse before vendor exemption")
				return NetworkdState{}, nil
			}
			s.Host.Run = func(context.Context, string, ...string) ([]byte, error) {
				t.Fatal("observe ran a program")
				return nil, errors.New("forbidden")
			}
			if _, err := s.Apply(context.Background(), in); err == nil || !strings.Contains(err.Error(), "second_network_owner") {
				t.Fatal(err)
			}
		})
	}
}

func TestFirstBoot_DnsSearchIsReadInNetworkManagerOrder(t *testing.T) {
	// IPv4 suffixes come first, then IPv6 suffixes not already present, without sorting.
	got := appendSearch(appendSearch(nil, "b.example", "a.example"), "A.example", "c.example")
	if !slices.Equal(got, []string{"b.example", "a.example", "c.example"}) {
		t.Fatal(got)
	}
}

func TestFirstBoot_HandOverHostSettingsRunsAfterVerifyHostSettings(t *testing.T) {
	in := answersFixture(t, "olivares.example.test")
	h := newFakeHost()
	m := newMachine(t.TempDir(), h, &in, h.seams())
	rec, err := m.Run(context.Background())
	if err != nil || rec.State != Ready {
		t.Fatalf("run: %+v %v", rec, err)
	}
	verify := slices.Index(h.order, StageHostSettings)
	handoff := slices.Index(h.order, Stage("hand-over-host-settings"))
	product := slices.Index(h.order, StageProductConfig)
	if verify < 0 || handoff != verify+1 || product != handoff+1 || rec.HostSettingsOwner != "appliance" {
		t.Fatalf("handoff order/record: %+v %+v", h.order, rec)
	}
}

func TestHandOverHostSettings_RequiresACompletedVerifiedRecordAndHasNoOtherEffect(t *testing.T) {
	for _, verified := range []bool{false, true} {
		t.Run(map[bool]string{false: "unverified", true: "verified"}[verified], func(t *testing.T) {
			root := t.TempDir()
			store := Store{Dir: filepath.Join(root, "state")}
			if err := os.MkdirAll(store.Dir, 0700); err != nil {
				t.Fatal(err)
			}
			rec := Record{Schema: recordSchema, State: Pending}
			if verified {
				rec.Completed = []Completed{{Stage: StageHostSettings, Effect: "measured"}}
			}
			if err := store.Save(rec); err != nil {
				t.Fatal(err)
			}
			handoff := HostSettingsHandoff{Host: Host{Root: root}}
			got, err := HandOverHostSettings(context.Background(), store, handoff)
			data, readErr := os.ReadFile(filepath.Join(root, HostOwnerFile))
			if !verified {
				if err == nil || !os.IsNotExist(readErr) {
					t.Fatalf("unverified handoff had effect: %v %v", err, readErr)
				}
				return
			}
			if err != nil || string(data) != hostOwnerConfig || got.HostSettingsOwner != "appliance" || len(got.Completed) != 2 {
				t.Fatalf("handoff: %+v %v %q", got, err, data)
			}
			if _, err := HandOverHostSettings(context.Background(), store, handoff); err != nil {
				t.Fatal("idempotent handoff", err)
			}
		})
	}
	root := t.TempDir()
	_, err := HandOverHostSettings(context.Background(), Store{Dir: root}, HostSettingsHandoff{Host: Host{Root: root}})
	if err == nil {
		t.Fatal("missing record accepted")
	}
}

func TestReconcile_RefusesAfterHostOwnerHandoff(t *testing.T) {
	root := t.TempDir()
	store := Store{Dir: root}
	rec := Record{Schema: recordSchema, State: Pending, HostSettingsOwner: "appliance"}
	if err := store.Save(rec); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Machine{Store: store}).Reconcile(); err == nil || !strings.Contains(err.Error(), "host_settings_already_handed_over") {
		t.Fatalf("handoff reconciled: %v", err)
	}
}

func TestBasePackage_DependsOnNetworkManager(t *testing.T) {
	raw := readRepo(t, "packaging/nfpm/olivares-appliance-base.yaml")
	raw = raw[strings.Index(raw, "{"):]
	var m struct {
		Overrides map[string]struct {
			Depends []string `json:"depends"`
		} `json:"overrides"`
	}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	for family, wants := range map[string][]string{"rpm": {"NetworkManager", "polkit"}, "deb": {"network-manager", "polkitd"}} {
		for _, want := range wants {
			if !slices.Contains(m.Overrides[family].Depends, want) {
				t.Fatalf("%s lacks %s dependency", family, want)
			}
		}
	}
}
