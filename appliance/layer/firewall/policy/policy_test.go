// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package policy_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/base"
	"github.com/olivaresai/olivares/appliance/layer/firewall/policy"
)

// document is a valid policy: SSH on 22, the console selected on eth0 and eth1, DHCPv6 on eth0.
func document() policy.Document {
	return policy.Document{
		SchemaVersion:          policy.SchemaVersion,
		SSHPort:                22,
		Portal:                 policy.Portal{Enabled: true, Listen: policy.ListenManagement, ManagementInterfaces: []string{"eth1", "eth0"}},
		DHCPv6ClientInterfaces: []string{"eth0"},
		Apps:                   []policy.AppRow{},
	}
}

// render renders d and fails the test when it is refused.
func render(t *testing.T, d policy.Document) string {
	t.Helper()
	out, err := policy.Render(d)
	if err != nil {
		t.Fatalf("a valid policy was refused: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("the policy rendered no ruleset")
	}
	return string(out)
}

// chain returns the trimmed rule lines of one chain of the rendered table, hook line included.
func chain(t *testing.T, ruleset, name string) []string {
	t.Helper()
	var out []string
	inside := false
	for _, line := range strings.Split(ruleset, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "chain "+name+" {":
			inside = true
		case inside && line == "}":
			return out
		case inside && line != "":
			out = append(out, line)
		}
	}
	t.Fatalf("the ruleset has no chain %s:\n%s", name, ruleset)
	return nil
}

// row returns the measured row of port, or false.
func row(rows []policy.MeasuredRow, port string) (policy.MeasuredRow, bool) {
	for _, r := range rows {
		if r.Port == port {
			return r, true
		}
	}
	return policy.MeasuredRow{}, false
}

// rulesOnly drops the table's comment line, whose digest is hexadecimal and may hold any digits.
func rulesOnly(ruleset string) string {
	var kept []string
	for _, line := range strings.Split(ruleset, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "comment ") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// portRule reports whether line admits a port: the rules the connection-state rules must precede.
func portRule(line string) bool { return strings.Contains(line, " dport ") }

func TestPolicy_9443OnlyOnManagementInterfacesAndOnlyWhenEnabled(t *testing.T) {
	d := document()
	ruleset := render(t, d)
	var console []string
	for _, line := range chain(t, ruleset, "input") {
		if strings.Contains(line, "9443") {
			console = append(console, line)
		}
	}
	if want := []string{`iifname { "eth0", "eth1" } tcp dport 9443 accept`}; !slices.Equal(console, want) {
		t.Errorf("the console's rules are %q, want %q: 9443 only on the management interfaces", console, want)
	}
	if got := strings.Count(rulesOnly(ruleset), "9443"); got != 1 {
		t.Errorf("9443 appears %d times in the rules, want once:\n%s", got, ruleset)
	}
	r, ok := row(policy.Rows(d), "9443/tcp")
	if !ok || !slices.Equal(r.Interfaces, []string{"eth0", "eth1"}) {
		t.Errorf("the measured console row is %+v (present %v), want 9443/tcp on eth0, eth1", r, ok)
	}

	// Not enabled, or enabled on loopback only: no 9443 row on any interface.
	for name, portal := range map[string]policy.Portal{
		"disabled":        {Enabled: false, Listen: policy.ListenManagement, ManagementInterfaces: []string{"eth0"}},
		"local":           {Enabled: true, Listen: policy.ListenLocal, ManagementInterfaces: []string{"eth0"}},
		"local, no names": {Enabled: true, Listen: policy.ListenLocal},
	} {
		d := document()
		d.Portal = portal
		if got := render(t, d); strings.Contains(rulesOnly(got), "9443") {
			t.Errorf("%s: the ruleset admits 9443:\n%s", name, got)
		}
		if r, ok := row(policy.Rows(d), "9443/tcp"); ok {
			t.Errorf("%s: a measured 9443 row %+v", name, r)
		}
	}

	// Refused: management without an interface, an interface that is not a name, the wildcard as
	// a management interface, and 9443 reached through an application's row.
	for name, mutate := range map[string]func(*policy.Document){
		"management without an interface": func(d *policy.Document) { d.Portal.ManagementInterfaces = nil },
		"a quoted interface":              func(d *policy.Document) { d.Portal.ManagementInterfaces = []string{`eth0" accept`} },
		"every interface":                 func(d *policy.Document) { d.Portal.ManagementInterfaces = []string{"*"} },
		"a repeated interface":            func(d *policy.Document) { d.Portal.ManagementInterfaces = []string{"eth0", "eth0"} },
		"9443 as an application row": func(d *policy.Document) {
			d.Apps = []policy.AppRow{{App: "proxy", Port: "9443/tcp", Interfaces: []string{"*"}}}
		},
		"an unknown listen scope": func(d *policy.Document) { d.Portal.Listen = "any" },
	} {
		d := document()
		mutate(&d)
		if err := d.Validate(); err == nil {
			t.Errorf("%s: the policy was accepted", name)
		}
		if out, err := policy.Render(d); err == nil || len(out) != 0 {
			t.Errorf("%s: the policy rendered %d bytes, err %v", name, len(out), err)
		}
	}
}

func TestPolicy_ProductPortsComeFromProductPorts(t *testing.T) {
	d := document()
	ruleset := render(t, d)
	input := chain(t, ruleset, "input")
	rows := policy.Rows(d)
	if len(base.ProductPorts) == 0 {
		t.Fatal("the product declares no port")
	}
	for _, port := range base.ProductPorts {
		number, protocol, _ := strings.Cut(port, "/")
		if want := protocol + " dport " + number + " accept"; !slices.Contains(input, want) {
			t.Errorf("the ruleset does not admit the product port %s on every interface (%q):\n%s", port, want, ruleset)
		}
		r, ok := row(rows, port)
		if !ok || !slices.Equal(r.Interfaces, []string{"*"}) {
			t.Errorf("the measured row of %s is %+v (present %v), want every interface", port, r, ok)
		}
	}
	// The document carries no product port: a field for one is refused, never read.
	encoded, err := policy.Canonical(d)
	if err != nil {
		t.Fatal(err)
	}
	withPorts := strings.Replace(string(encoded), `"ssh_port":22`, `"product_ports":["9000/tcp"],"ssh_port":22`, 1)
	if withPorts == string(encoded) {
		t.Fatalf("the canonical document has no ssh_port member to place product_ports beside: %s", encoded)
	}
	if _, err := policy.Decode([]byte(withPorts)); err == nil {
		t.Error("a document that declares its own product ports was accepted")
	}
	if got, err := policy.Decode(encoded); err != nil || policy.Digest(got) != policy.Digest(d) {
		t.Errorf("the canonical document does not decode to the same policy: %v", err)
	}
	// Every port row is SSH, a product port, DHCPv6, the console or an application's.
	allowed := append([]string{"22/tcp", "546/udp", "9443/tcp"}, base.ProductPorts...)
	for _, r := range rows {
		if !slices.Contains(allowed, r.Port) {
			t.Errorf("a row %+v that no source declared", r)
		}
	}
}

func TestPolicy_KeepsIcmpv6NeighborAndRouterDiscovery(t *testing.T) {
	const discovery = "icmpv6 type { nd-neighbor-solicit, nd-neighbor-advert, nd-router-advert } accept"
	minimal := document()
	minimal.Portal = policy.Portal{Listen: policy.ListenLocal}
	minimal.DHCPv6ClientInterfaces = []string{}
	for name, d := range map[string]policy.Document{"full": document(), "minimal": minimal} {
		input := chain(t, render(t, d), "input")
		if !strings.HasSuffix(input[0], "policy drop;") {
			t.Errorf("%s: the input chain does not drop by default: %q", name, input[0])
		}
		at := slices.Index(input, discovery)
		if at < 0 {
			t.Errorf("%s: the input chain does not keep IPv6 neighbor and router discovery on every interface:\n%s", name, strings.Join(input, "\n"))
			continue
		}
		for i, line := range input[:at] {
			if portRule(line) {
				t.Errorf("%s: the port rule %q (line %d) precedes the discovery rule", name, line, i)
			}
		}
	}
}

func TestPolicy_KeepsDhcpv6ClientReplies(t *testing.T) {
	d := document()
	d.DHCPv6ClientInterfaces = []string{"eth2", "eth0"}
	input := chain(t, render(t, d), "input")
	const want = `iifname { "eth0", "eth2" } ip6 saddr fe80::/10 udp sport 547 udp dport 546 accept`
	if !slices.Contains(input, want) {
		t.Errorf("the input chain does not admit link-local DHCPv6 server replies on the client interfaces (%q):\n%s", want, strings.Join(input, "\n"))
	}
	for _, line := range input {
		if strings.Contains(line, "dport 546") && line != want {
			t.Errorf("another rule admits the DHCPv6 client port: %q", line)
		}
	}
	if r, ok := row(policy.Rows(d), "546/udp"); !ok || !slices.Equal(r.Interfaces, []string{"eth0", "eth2"}) {
		t.Errorf("the measured DHCPv6 row is %+v (present %v)", r, ok)
	}
	// No client interface, no reply row; never a wildcard.
	d.DHCPv6ClientInterfaces = []string{}
	if got := render(t, d); strings.Contains(got, "dport 546") {
		t.Errorf("a DHCPv6 row without a client interface:\n%s", got)
	}
	d.DHCPv6ClientInterfaces = []string{"*"}
	if d.Validate() == nil {
		t.Error("DHCPv6 replies on every interface were accepted")
	}
}

func TestPolicy_EstablishedAndRelatedAcceptedInvalidDropped(t *testing.T) {
	ruleset := render(t, document())
	for _, name := range []string{"input", "forward"} {
		rules := chain(t, ruleset, name)
		if len(rules) < 3 {
			t.Fatalf("chain %s has %d lines", name, len(rules))
		}
		if want := "type filter hook " + name + " priority filter; policy drop;"; rules[0] != want {
			t.Errorf("chain %s hook line %q, want %q", name, rules[0], want)
		}
		if rules[1] != "ct state established,related accept" || rules[2] != "ct state invalid drop" {
			t.Errorf("chain %s begins %q, %q: established and related accepted, then invalid dropped", name, rules[1], rules[2])
		}
		for _, line := range rules[3:] {
			if strings.HasPrefix(line, "ct state") {
				t.Errorf("chain %s has a later connection-state rule %q", name, line)
			}
		}
	}
	forward := chain(t, ruleset, "forward")
	for _, line := range forward[3:] {
		t.Errorf("the forward chain admits more than replies: %q", line)
	}
	if !strings.HasPrefix(ruleset, "table inet olivares\ndelete table inet olivares\ntable inet olivares {\n") {
		t.Errorf("the ruleset does not replace the one table in one transaction:\n%s", ruleset)
	}
	if !strings.Contains(ruleset, `comment "olivares-firewall-policy `+policy.Digest(document())+`"`) {
		t.Errorf("the table does not carry the policy digest:\n%s", ruleset)
	}
}
