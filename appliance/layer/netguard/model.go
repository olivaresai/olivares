// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package netguard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type State string

const (
	StatePending              State = "pending"
	StateAwaitingConfirmation State = "awaiting_confirmation"
	StateRestoring            State = "restoring"
	StateConfirmed            State = "confirmed"
	StateRolledBack           State = "rolled_back"
)

type ConfirmationClass string

const (
	NonManagement     ConfirmationClass = "non_management"
	StaticManagement  ConfirmationClass = "static_management"
	DynamicManagement ConfirmationClass = "dynamic_management"
)

// IPSettings is the complete public state of one address family. A nil address
// family in Change preserves that family; an empty slice clears that list.
type IPSettings struct {
	Method       string   `json:"method"`
	Addresses    []string `json:"addresses,omitempty"`
	Gateway      string   `json:"gateway"`
	Routes       []Route  `json:"routes,omitempty"`
	DNS          []string `json:"dns,omitempty"`
	Search       []string `json:"search,omitempty"`
	NeverDefault bool     `json:"never_default"`
}
type Route struct {
	Destination string `json:"destination"`
	NextHop     string `json:"next_hop"`
	Metric      uint32 `json:"metric"`
}

// Change contains public desired settings, never a secret, path to a file or unit claim.
type Change struct {
	ProfileUUID   string      `json:"profile_uuid"`
	OperationID   string      `json:"operation_id"`
	Interface     string      `json:"interface"`
	IPv4          *IPSettings `json:"ipv4,omitempty"`
	IPv6          *IPSettings `json:"ipv6,omitempty"`
	WindowSeconds uint32      `json:"window_seconds"`
}

func (c Change) Digest() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
func (c Change) Validate() error {
	if !uuidShape(c.ProfileUUID) || !validID(c.OperationID) || !interfaceName(c.Interface) || c.Interface == "lo" {
		return errors.New("network_input_refused")
	}
	if c.WindowSeconds < 60 || c.WindowSeconds > 600 || c.IPv4 == nil && c.IPv6 == nil {
		return errors.New("network_input_refused")
	}
	for _, f := range []struct {
		s  *IPSettings
		v4 bool
	}{{c.IPv4, true}, {c.IPv6, false}} {
		if f.s == nil {
			continue
		}
		if err := validateIP(*f.s, f.v4); err != nil {
			return err
		}
	}
	return nil
}
func validateIP(s IPSettings, v4 bool) error {
	switch s.Method {
	case "manual":
		if len(s.Addresses) == 0 {
			return errors.New("network_address_required")
		}
	case "auto", "disabled":
		if len(s.Addresses) > 0 || s.Gateway != "" {
			return errors.New("network_address_conflict")
		}
	default:
		return errors.New("network_method_refused")
	}
	if len(s.Addresses) > 16 || len(s.Routes) > 16 || len(s.DNS) > 16 || len(s.Search) > 16 {
		return errors.New("network_input_limit")
	}
	seen := map[netip.Addr]bool{}
	gatewayOnLink := s.Gateway == ""
	for _, value := range s.Addresses {
		p, err := netip.ParsePrefix(value)
		if err != nil || p.Bits() == 0 || p.Addr().Is4() != v4 || !unicast(p.Addr()) || seen[p.Addr()] {
			return errors.New("network_address_refused")
		}
		seen[p.Addr()] = true
		if s.Gateway != "" {
			g, err := netip.ParseAddr(s.Gateway)
			if err != nil || !unicast(g) || g.Is4() != v4 || g == p.Addr() {
				return errors.New("network_gateway_refused")
			}
			gatewayOnLink = gatewayOnLink || p.Contains(g)
		}
	}
	if !gatewayOnLink {
		return errors.New("network_gateway_refused")
	}
	for _, value := range s.DNS {
		a, err := netip.ParseAddr(value)
		if err != nil || !unicast(a) {
			return errors.New("network_dns_refused")
		}
	}
	for _, suffix := range s.Search {
		if !searchSuffix(suffix) {
			return errors.New("network_dns_refused")
		}
	}
	for _, r := range s.Routes {
		p, err := netip.ParsePrefix(r.Destination)
		if err != nil || p.Addr().Is4() != v4 {
			return errors.New("network_route_refused")
		}
		if r.NextHop != "" {
			a, err := netip.ParseAddr(r.NextHop)
			if err != nil || !unicast(a) || a.Is4() != v4 {
				return errors.New("network_route_refused")
			}
		}
	}
	return nil
}

// interfaceName is the answers schema's interface shape: 1-15 bytes of letters and digits,
// with '.', '-' or '_' allowed after the first byte.
func interfaceName(name string) bool {
	if len(name) < 1 || len(name) > 15 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case i > 0 && (c == '.' || c == '-' || c == '_'):
		default:
			return false
		}
	}
	return true
}

// searchSuffix is the answers schema's search suffix shape: a DNS name of 1-63 byte labels
// of letters, digits and inner hyphens, never an address, localhost or digits and dots only.
func searchSuffix(name string) bool {
	if len(name) < 1 || len(name) > 253 {
		return false
	}
	if _, err := netip.ParseAddr(name); err == nil {
		return false
	}
	lower := strings.ToLower(name)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") || strings.Trim(lower, "0123456789.") == "" {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func unicast(a netip.Addr) bool {
	return a.IsGlobalUnicast() && !a.IsLoopback() && !a.Is4In6() && a.Zone() == ""
}
func validID(s string) bool { return len(s) == 32 && strings.Trim(s, "0123456789abcdef") == "" }

// RuntimeFamily is one address family as the device carries it now: its addresses with
// prefix lengths and its routes, link-local entries excluded.
type RuntimeFamily struct {
	Addresses []string
	Routes    []Route
}

// RuntimeState is a device's current IPv4 and IPv6 facts. Profile and Applied are intent.
type RuntimeState struct{ IPv4, IPv6 RuntimeFamily }

// Reachability is a device-bound observation: Target answered a query sent through
// Interface from Source, in boot BootID at At. A probe makes it; NetworkManager's
// address and route facts never do.
type Reachability struct {
	Interface string        `json:"interface"`
	Source    string        `json:"source"`
	Target    string        `json:"target"`
	BootID    string        `json:"boot_id"`
	At        time.Duration `json:"observed_boottime_ns"`
}

func dynamicMethod(method string) bool { return method == "auto" || method == "dhcp" }

// usableRoute reports whether a family can reach beyond its address: every planned route,
// and the default route unless the plan says never-default. A never-default family
// without planned routes needs the connected route of one of its addresses.
func usableRoute(want IPSettings, got RuntimeFamily) bool {
	for _, r := range want.Routes {
		if !slices.ContainsFunc(got.Routes, func(actual Route) bool {
			return actual.Destination == r.Destination && actual.NextHop == r.NextHop && (r.Metric == 0 || r.Metric == actual.Metric)
		}) {
			return false
		}
	}
	if !want.NeverDefault {
		return slices.ContainsFunc(got.Routes, func(r Route) bool { return strings.HasSuffix(r.Destination, "/0") })
	}
	if len(want.Routes) > 0 {
		return true
	}
	return slices.ContainsFunc(got.Routes, func(r Route) bool {
		route, err := netip.ParsePrefix(r.Destination)
		if err != nil || route.Bits() == 0 || r.NextHop != "" {
			return false
		}
		return slices.ContainsFunc(got.Addresses, func(a string) bool {
			p, err := netip.ParsePrefix(a)
			return err == nil && route.Contains(p.Addr())
		})
	})
}

// dynamicUsable is the oracle of a DHCP or SLAAC family: an address, a usable route, and a
// device-bound reachability observation from one of those addresses, in this boot, made no
// earlier than since. An address alone proves nothing.
func dynamicUsable(iface string, want IPSettings, v4 bool, got RuntimeFamily, reach []Reachability, boot string, since time.Duration) bool {
	if len(got.Addresses) == 0 || !usableRoute(want, got) {
		return false
	}
	for _, r := range reach {
		source, err := netip.ParseAddr(r.Source)
		if err != nil || r.Interface != iface || r.BootID != boot || r.At < since || source.Is4() != v4 {
			continue
		}
		target, err := netip.ParseAddr(r.Target)
		if err != nil || target.Is4() != v4 {
			continue
		}
		if slices.ContainsFunc(got.Addresses, func(a string) bool {
			p, err := netip.ParsePrefix(a)
			return err == nil && p.Addr() == source
		}) {
			return true
		}
	}
	return false
}

// Profile contains only public settings and persistence facts. Versions are separate
// because Settings.Connection.VersionId and Device's applied version are not interchangeable.
type Profile struct {
	UUID           string     `json:"uuid"`
	Interface      string     `json:"interface"`
	Device         string     `json:"device"`
	Connection     string     `json:"connection"`
	Filename       string     `json:"filename"`
	Flags          uint32     `json:"flags"`
	ProfileVersion uint64     `json:"profile_version"`
	AppliedVersion uint64     `json:"applied_version"`
	IPv4           IPSettings `json:"ipv4"`
	IPv6           IPSettings `json:"ipv6"`
}

func (p Profile) Persistent() bool {
	return p.Flags&1 == 0 && filepath.Clean(p.Filename) == p.Filename && filepath.Dir(p.Filename) == "/etc/NetworkManager/system-connections" && strings.HasSuffix(p.Filename, ".nmconnection")
}
func (p Profile) With(c Change) Profile {
	if c.IPv4 != nil {
		p.IPv4 = *c.IPv4
	}
	if c.IPv6 != nil {
		p.IPv6 = *c.IPv6
	}
	return p
}
func (p Profile) Matches(q Profile) bool {
	return p.UUID == q.UUID && p.Interface == q.Interface && sameIP(p.IPv4, q.IPv4) && sameIP(p.IPv6, q.IPv6)
}
func sameIP(a, b IPSettings) bool {
	normalize := func(p IPSettings) IPSettings {
		if len(p.Addresses) == 0 {
			p.Addresses = nil
		}
		if len(p.Routes) == 0 {
			p.Routes = nil
		}
		if len(p.DNS) == 0 {
			p.DNS = nil
		}
		if len(p.Search) == 0 {
			p.Search = nil
		}
		return p
	}
	a = normalize(a)
	b = normalize(b)
	a.Addresses = slices.Clone(a.Addresses)
	b.Addresses = slices.Clone(b.Addresses)
	slices.Sort(a.Addresses)
	slices.Sort(b.Addresses)
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(aa) == string(bb)
}

const JournalSchema = "olivares-network-window/v1"

// Window is the sole recovery authority. Secret values and plaintext confirm tokens
// cannot be represented here. Calls are retained even when the transport disappeared.
type TargetBinding struct {
	BusID   string          `json:"bus_id"`
	Process ProcessIdentity `json:"process"`
}

type RestoreAttemptRecord struct {
	Attempt uint32        `json:"attempt"`
	Target  TargetBinding `json:"target"`
	Closed  bool          `json:"closed"`
}

type Window struct {
	FinalBootID                   string                 `json:"final_boot_id,omitempty"`
	ObservationBootID             string                 `json:"observation_boot_id,omitempty"`
	RestoreAttempts               []RestoreAttemptRecord `json:"restore_attempts,omitempty"`
	RecoveryTarget                TargetBinding          `json:"recovery_target"`
	HelperStarted                 bool                   `json:"helper_started"`
	Schema                        string                 `json:"schema"`
	OperationID                   string                 `json:"operation_id"`
	Digest                        string                 `json:"digest"`
	Generation                    string                 `json:"window_generation"`
	BootID                        string                 `json:"boot_id"`
	Attempt                       uint32                 `json:"recovery_attempt"`
	State                         State                  `json:"state"`
	Phase                         string                 `json:"phase"`
	Reason                        string                 `json:"reason,omitempty"`
	Class                         ConfirmationClass      `json:"confirmation_class"`
	ManagementSettingsFingerprint string                 `json:"management_settings_fingerprint"`
	ManagementFingerprint         string                 `json:"management_fingerprint"`
	Baseline                      Profile                `json:"baseline"`
	Candidate                     Profile                `json:"candidate"`
	Deadline                      time.Duration          `json:"deadline_boottime_ns"`
	TokenHash                     string                 `json:"token_hash"`
	TokenConsumed                 bool                   `json:"token_consumed"`
	Checkpoint                    string                 `json:"checkpoint,omitempty"`
	Calls                         []CallRecord           `json:"calls"`
	LastObservation               time.Duration          `json:"last_observation_boottime_ns"`
	AttemptStarted                time.Duration          `json:"attempt_boottime_ns"`
	FinalKnownAt                  time.Duration          `json:"final_known_boottime_ns,omitempty"`
	RollbackResults               map[string]uint32      `json:"rollback_results,omitempty"`
}

func (w Window) Terminal() bool { return w.State == StateConfirmed || w.State == StateRolledBack }
func (w Window) Pending() bool {
	for _, c := range w.Calls {
		if !c.Settled {
			return true
		}
	}
	return false
}

func cloneWindow(w Window) Window {
	w.Calls = slices.Clone(w.Calls)
	w.RestoreAttempts = slices.Clone(w.RestoreAttempts)
	cloneIP := func(p IPSettings) IPSettings {
		p.Addresses = slices.Clone(p.Addresses)
		p.Routes = slices.Clone(p.Routes)
		p.DNS = slices.Clone(p.DNS)
		p.Search = slices.Clone(p.Search)
		return p
	}
	w.Baseline.IPv4 = cloneIP(w.Baseline.IPv4)
	w.Baseline.IPv6 = cloneIP(w.Baseline.IPv6)
	w.Candidate.IPv4 = cloneIP(w.Candidate.IPv4)
	w.Candidate.IPv6 = cloneIP(w.Candidate.IPv6)
	if w.RollbackResults != nil {
		old := w.RollbackResults
		w.RollbackResults = make(map[string]uint32, len(old))
		for k, v := range old {
			w.RollbackResults[k] = v
		}
	}
	return w
}
