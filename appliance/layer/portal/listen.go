// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package portal serves the Appliance Console: read-only status of the appliance over TLS
// on port 9443.
//
// The console binds no socket itself. It serves only sockets the service manager passes
// in, and Decide chooses which of them it may serve: loopback while any remote
// prerequisite is missing, never a wildcard address, and nothing at all without verified
// TLS material. The package performs no privileged act and imports nothing from the
// product.
package portal

import (
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// Listen scopes accepted in the answers document's portal.listen.
const (
	ListenLocal      = "local"
	ListenManagement = "management"
)

// Selection is the operator's answer about exposure, as the answers document states it.
type Selection struct {
	// Enabled is portal.enabled; nil when the document omits it.
	Enabled *bool
	// Listen is portal.listen: ListenLocal, ListenManagement or empty.
	Listen string
	// ManagementInterfaces is host.management_interfaces.
	ManagementInterfaces []string
}

// FirewallMeasurement is what a FirewallProbe observed.
type FirewallMeasurement struct {
	// Holds reports that the firewall prerequisite for remote exposure was measured
	// and holds.
	Holds bool
	// Policy summarizes the policy in force. Only loopback callers see it.
	Policy Fact
}

// condition is the firewall prerequisite as measured: held, not held, or unmeasured when
// no probe reported a policy. The public state word and the listen decision's reason are
// both taken from it, so they cannot disagree.
func (f FirewallMeasurement) condition() string {
	switch {
	case f.Holds:
		return "held"
	case f.Policy.value != "":
		return "not held"
	default:
		return "unmeasured"
	}
}

// state is the firewall's public state word.
func (f FirewallMeasurement) state() string {
	if c := f.condition(); c != "unmeasured" {
		return "prerequisite " + c
	}
	return "unmeasured"
}

// FirewallProbe measures the host firewall prerequisite for remote exposure.
type FirewallProbe interface {
	MeasureFirewall() FirewallMeasurement
}

// NoFirewallProbe is the default probe. It measures nothing, so remote exposure stays
// refused until a real probe is installed.
type NoFirewallProbe struct{}

// MeasureFirewall reports an unmeasured firewall.
func (NoFirewallProbe) MeasureFirewall() FirewallMeasurement { return FirewallMeasurement{} }

// Mode is the console's listen decision.
type Mode string

const (
	// Disabled serves nothing, not even loopback.
	Disabled Mode = "disabled"
	// LocalOnly serves loopback addresses only.
	LocalOnly Mode = "local-only"
	// Remote also serves sockets bound to the selected management interfaces.
	Remote Mode = "remote"
)

// Decision is the outcome of Decide.
type Decision struct {
	Mode Mode
	// Interfaces are the management interfaces a Remote decision exposes.
	Interfaces []string
	// Reasons name what keeps the remote listener off; empty for Remote.
	Reasons []string
}

// Decide chooses where the console may listen. It is a pure function of the operator's
// selection, the TLS custody verdict and the firewall measurement. Remote exposure needs
// all of: portal.enabled true, portal.listen management, at least one management
// interface, verified TLS custody and a firewall prerequisite that holds.
func Decide(s Selection, c Custody, f FirewallMeasurement) Decision {
	if s.Enabled != nil && !*s.Enabled {
		return Decision{Mode: Disabled, Reasons: []string{"portal.enabled is false"}}
	}
	if !c.Verified {
		reason := c.Reason
		if reason == "" {
			reason = "not checked"
		}
		return Decision{Mode: Disabled, Reasons: []string{"TLS custody is unverified: " + reason}}
	}
	var missing []string
	for _, p := range []struct {
		held   bool
		reason string
	}{
		{s.Enabled != nil && *s.Enabled, "portal.enabled is not true"},
		{s.Listen == ListenManagement, "portal.listen is not management"},
		{len(s.ManagementInterfaces) > 0, "no management interface is selected"},
		{f.Holds, "firewall prerequisite is " + f.condition()},
	} {
		if !p.held {
			missing = append(missing, p.reason)
		}
	}
	if len(missing) > 0 {
		return Decision{Mode: LocalOnly, Reasons: missing}
	}
	interfaces := slices.Clone(s.ManagementInterfaces)
	slices.Sort(interfaces)
	return Decision{Mode: Remote, Interfaces: interfaces}
}

// DeviceBound is implemented by the local address of a passed socket whose binding to a
// network interface was measured. BoundDevice names the interface the kernel restricts
// the socket to (SO_BINDTODEVICE), or is "" when the socket is bound to none.
type DeviceBound interface {
	BoundDevice() string
}

// Serves reports whether the console may serve a passed stream socket whose local
// address is addr. Refusal says why it may not.
func (d Decision) Serves(addr net.Addr) bool { return d.Refusal(addr) == "" }

// Refusal returns why the console may not serve a passed stream socket whose local
// address is addr, or "" when it may. Loopback is served unless the console is disabled.
// Any other address is served only when remote access is enabled and the kernel binds
// the socket to a selected management interface, which addr reports by implementing
// DeviceBound. The address alone proves no interface, because Linux accepts a packet for
// any of its addresses on any interface. A wildcard address is never served. A refusal
// names the interface, never key material.
func (d Decision) Refusal(addr net.Addr) string {
	if d.Mode != LocalOnly && d.Mode != Remote {
		return "the console is disabled"
	}
	if addr == nil || !strings.HasPrefix(addr.Network(), "tcp") {
		return "not a TCP socket"
	}
	local, err := netip.ParseAddrPort(addr.String())
	if err != nil {
		return "the socket address cannot be read"
	}
	ip := local.Addr().Unmap()
	switch {
	case ip.IsUnspecified():
		return "a wildcard address is never served"
	case ip.IsLoopback():
		return ""
	case d.Mode != Remote:
		return "remote access is off"
	}
	device := ""
	if b, ok := addr.(DeviceBound); ok {
		device = b.BoundDevice()
	}
	switch {
	case device == "":
		return "the socket is bound to no management interface"
	case !slices.Contains(d.Interfaces, device):
		return "the socket is bound to " + strconv.Quote(device) + ", which is not a selected management interface"
	}
	return ""
}
