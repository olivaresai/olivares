// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux && (amd64 || arm64)

package netguard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

func (t *NMTransport) Observe(ctx context.Context, iface string) (Observation, error) {
	if t.successor != nil {
		return t.successor.Observe(ctx, iface)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := t.verifyTarget(ctx); err != nil {
		return Observation{}, err
	}
	device, err := t.device(ctx, iface)
	if err != nil {
		return Observation{}, err
	}
	observed, _, err := t.profile(ctx, device, true)
	if err != nil {
		return Observation{}, err
	}
	if len(t.management) == 0 {
		return Observation{}, errors.New("network_management_selection_unavailable")
	}
	management := make(map[string]any, len(t.management))
	settingsOnly := make(map[string]any, len(t.management))
	for _, name := range t.management {
		path, err := t.device(ctx, name)
		if err != nil {
			return Observation{}, err
		}
		o, _, err := t.profile(ctx, path, false)
		if err != nil {
			return Observation{}, err
		}
		if !o.Managed || !o.RuntimeUsable {
			return Observation{}, errors.New("network_management_runtime_unavailable")
		}
		settingsOnly[name] = struct{ IPv4, IPv6 IPSettings }{o.Applied.IPv4, o.Applied.IPv6}
		runtime, err := t.runtimeFacts(ctx, path)
		if err != nil {
			return Observation{}, err
		}
		management[name] = struct {
			IPv4, IPv6 IPSettings
			Runtime    any
		}{o.Applied.IPv4, o.Applied.IPv6, runtime}
		if name == iface {
			observed.ManagementDevice = true
		}
	}
	dns, err := t.get(ctx, "/org/freedesktop/NetworkManager/DnsManager", nmName+".DnsManager", "Configuration")
	if err != nil {
		return Observation{}, err
	}
	var entries []map[string]dbus.Variant
	if err := dbus.Store([]any{dns.Value()}, &entries); err != nil {
		return Observation{}, errors.New("network_dns_measurement_unavailable")
	}
	var selected []map[string]any
	for _, entry := range entries {
		name, _ := entry["interface"].Value().(string)
		if name == "" || slices.Contains(t.management, name) {
			plain := map[string]any{}
			for key, value := range entry {
				plain[key] = value.Value()
			}
			selected = append(selected, plain)
		}
	}
	management["dns"] = selected
	b, err := json.Marshal(management)
	if err != nil {
		return Observation{}, err
	}
	sum := sha256.Sum256(b)
	observed.ManagementFingerprint = hex.EncodeToString(sum[:])
	settingsBytes, err := json.Marshal(settingsOnly)
	if err != nil {
		return Observation{}, err
	}
	settingsHash := sha256.Sum256(settingsBytes)
	observed.ManagementSettingsFingerprint = hex.EncodeToString(settingsHash[:])
	observed.ManagementUnchanged = true
	return observed, nil
}
func (t *NMTransport) device(ctx context.Context, iface string) (dbus.ObjectPath, error) {
	body, err := t.read(ctx, t.process.identity.Unique, nmPath, nmName, "GetDeviceByIpIface", iface)
	if err != nil {
		return "", err
	}
	var path dbus.ObjectPath
	if err := storeReply(body, &path); err != nil || path == "/" || !path.IsValid() {
		return "", errors.New("network_device_unavailable")
	}
	return path, nil
}
func (t *NMTransport) profile(ctx context.Context, device dbus.ObjectPath, secrets bool) (Observation, settingsMap, error) {
	var o Observation
	refuse := func(reason string) (Observation, settingsMap, error) { return o, nil, errors.New(reason) }
	managed, err := t.get(ctx, device, deviceInterface, "Managed")
	if err != nil {
		return o, nil, err
	}
	o.Managed, _ = managed.Value().(bool)
	if !o.Managed {
		return refuse("network_device_unmanaged")
	}
	iv, err := t.get(ctx, device, deviceInterface, "Interface")
	if err != nil {
		return o, nil, err
	}
	iface, ok := iv.Value().(string)
	if !ok {
		return refuse("network_interface_unavailable")
	}
	active, err := t.get(ctx, device, deviceInterface, "ActiveConnection")
	if err != nil {
		return o, nil, err
	}
	activePath, ok := active.Value().(dbus.ObjectPath)
	if !ok || activePath == "/" {
		o.RequiresActivation = true
		return o, nil, nil
	}
	cv, err := t.get(ctx, activePath, nmName+".Connection.Active", "Connection")
	if err != nil {
		return o, nil, err
	}
	cp, ok := cv.Value().(dbus.ObjectPath)
	if !ok || cp == "/" {
		return refuse("network_profile_unavailable")
	}
	version, err := t.get(ctx, cp, ProfileInterface, "VersionId")
	if err != nil {
		return refuse("profile_version_unavailable")
	}
	before, err := ProfileVersion(version.Value())
	if err != nil {
		return o, nil, err
	}
	body, err := t.read(ctx, t.process.identity.Unique, cp, ProfileInterface, "GetSettings")
	if err != nil {
		return o, nil, err
	}
	var settings settingsMap
	if err := storeReply(body, &settings); err != nil {
		return o, nil, err
	}
	kind, _ := settings["connection"]["type"].Value().(string)
	// This plane changes IP on Ethernet profiles. Other activation mechanisms have
	// secret and reactivation contracts of their own and cannot be guessed here.
	if kind != "802-3-ethernet" {
		return refuse("activation_not_fenced")
	}
	allowed := map[string]bool{"connection": true, "802-3-ethernet": true, "802-1x": true, "ipv4": true, "ipv6": true, "proxy": true, "ethtool": true, "tc": true, "sriov": true, "dcb": true, "hostname": true, "match": true, "user": true}
	for name := range settings {
		if !allowed[name] {
			return refuse("network_secrets_unavailable")
		}
	}
	o.SecretsComplete = settings["802-1x"] == nil
	if secrets && settings["802-1x"] != nil {
		body, err = t.read(ctx, t.process.identity.Unique, cp, ProfileInterface, "GetSecrets", "802-1x")
		if err != nil {
			return refuse("network_secrets_unavailable")
		}
		var secret settingsMap
		if err := storeReply(body, &secret); err != nil || len(secret["802-1x"]) == 0 {
			return refuse("network_secrets_unavailable")
		}
		for group, values := range secret {
			if settings[group] == nil {
				settings[group] = map[string]dbus.Variant{}
			}
			for key, value := range values {
				settings[group][key] = value
			}
		}
		o.SecretsComplete = true
		for key, value := range settings["802-1x"] {
			if base, ok := strings.CutSuffix(key, "-flags"); ok {
				flags, ok := value.Value().(uint32)
				if !ok {
					return refuse("network_secrets_unavailable")
				}
				if flags&4 == 0 {
					if _, found := settings["802-1x"][base]; !found {
						o.SecretsComplete = false
					}
				}
			}
		}
	}
	uuid, _ := settings["connection"]["uuid"].Value().(string)
	if len(uuid) != 36 {
		return refuse("network_profile_identity_unavailable")
	}
	p := Profile{UUID: uuid, Interface: iface, Device: string(device), Connection: string(cp), ProfileVersion: before}
	filename, err := t.get(ctx, cp, ProfileInterface, "Filename")
	if err != nil {
		return o, nil, err
	}
	p.Filename, ok = filename.Value().(string)
	if !ok {
		return refuse("network_profile_storage_unavailable")
	}
	flags, err := t.get(ctx, cp, ProfileInterface, "Flags")
	if err != nil {
		return o, nil, err
	}
	p.Flags, ok = flags.Value().(uint32)
	if !ok {
		return refuse("network_profile_storage_unavailable")
	}
	p.IPv4, err = readIP(settings["ipv4"])
	if err != nil {
		return o, nil, err
	}
	p.IPv6, err = readIP(settings["ipv6"])
	if err != nil {
		return o, nil, err
	}
	body, err = t.read(ctx, t.process.identity.Unique, device, deviceInterface, "GetAppliedConnection", uint32(0))
	if err != nil {
		return o, nil, err
	}
	var applied settingsMap
	var appliedVersion uint64
	if err := storeReply(body, &applied, &appliedVersion); err != nil || appliedVersion == 0 {
		return refuse("applied_version_unavailable")
	}
	p.AppliedVersion = appliedVersion
	o.Profile = p
	o.Applied = p
	o.Applied.IPv4, err = readIP(applied["ipv4"])
	if err != nil {
		return o, nil, err
	}
	o.Applied.IPv6, err = readIP(applied["ipv6"])
	if err != nil {
		return o, nil, err
	}
	runtime, err := t.runtimeFacts(ctx, device)
	if err != nil {
		return o, nil, err
	}
	o.RuntimeUsable = runtimeMatches(p.IPv4, runtime.IPv4) && runtimeMatches(p.IPv6, runtime.IPv6)
	o.Runtime = RuntimeState{IPv4: runtime.IPv4, IPv6: runtime.IPv6}
	afterValue, err := t.get(ctx, cp, ProfileInterface, "VersionId")
	if err != nil {
		return refuse("profile_version_unavailable")
	}
	after, err := ProfileVersion(afterValue.Value())
	if err != nil {
		return o, nil, err
	}
	if err := VersionBracket(before, after); err != nil {
		return o, nil, err
	}
	return o, settings, nil
}

type runtimeIP = RuntimeFamily
type runtimeNetwork struct{ IPv4, IPv6 runtimeIP }

func (t *NMTransport) runtimeFacts(ctx context.Context, device dbus.ObjectPath) (runtimeNetwork, error) {
	var result runtimeNetwork
	for _, family := range []struct {
		property, iface string
		out             *runtimeIP
	}{{"Ip4Config", nmName + ".IP4Config", &result.IPv4}, {"Ip6Config", nmName + ".IP6Config", &result.IPv6}} {
		v, err := t.get(ctx, device, deviceInterface, family.property)
		if err != nil {
			return result, err
		}
		path, ok := v.Value().(dbus.ObjectPath)
		if !ok {
			return result, errors.New("network_runtime_unavailable")
		}
		if path == "/" {
			continue
		}
		addresses, err := t.get(ctx, path, family.iface, "AddressData")
		if err != nil {
			return result, err
		}
		family.out.Addresses, err = addressData(addresses)
		if err != nil {
			return result, err
		}
		routes, err := t.get(ctx, path, family.iface, "RouteData")
		if err != nil {
			return result, err
		}
		family.out.Routes, err = routeData(routes)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}
func runtimeMatches(want IPSettings, got runtimeIP) bool {
	switch want.Method {
	case "disabled", "ignore":
		return true
	case "auto", "dhcp":
		// Reachability is the probe's observation; the engine adds it (dynamicUsable).
		return len(got.Addresses) > 0 && usableRoute(want, got)
	case "manual":
		if !stringSetEqual(want.Addresses, got.Addresses) {
			return false
		}
		for _, r := range want.Routes {
			if !slices.ContainsFunc(got.Routes, func(actual Route) bool {
				return actual.Destination == r.Destination && actual.NextHop == r.NextHop && (r.Metric == 0 || r.Metric == actual.Metric)
			}) {
				return false
			}
		}
		if want.Gateway != "" && !slices.ContainsFunc(got.Routes, func(r Route) bool { return strings.HasSuffix(r.Destination, "/0") && r.NextHop == want.Gateway }) {
			return false
		}
		for _, r := range got.Routes {
			if slices.ContainsFunc(want.Routes, func(w Route) bool { return w.Destination == r.Destination && w.NextHop == r.NextHop }) {
				continue
			}
			if r.NextHop == want.Gateway && want.Gateway != "" && strings.HasSuffix(r.Destination, "/0") {
				continue
			}
			connected := false
			for _, a := range want.Addresses {
				p, err := netip.ParsePrefix(a)
				if err == nil && r.Destination == p.Masked().String() && r.NextHop == "" {
					connected = true
				}
			}
			if !connected {
				return false
			}
		}
		return true
	}
	return false
}
func stringSetEqual(a, b []string) bool {
	a = slices.Clone(a)
	b = slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}
func readIP(values map[string]dbus.Variant) (IPSettings, error) {
	var p IPSettings
	p.Method, _ = values["method"].Value().(string)
	p.Gateway, _ = values["gateway"].Value().(string)
	p.NeverDefault, _ = values["never-default"].Value().(bool)
	var err error
	if v, ok := values["address-data"]; ok {
		p.Addresses, err = addressData(v)
		if err != nil {
			return p, err
		}
	}
	if v, ok := values["route-data"]; ok {
		p.Routes, err = routeData(v)
		if err != nil {
			return p, err
		}
	}
	if v, ok := values["dns-data"]; ok {
		if err := dbus.Store([]any{v.Value()}, &p.DNS); err != nil {
			return p, errors.New("network_dns_unavailable")
		}
	} else if v, ok := values["dns"]; ok {
		switch addresses := v.Value().(type) {
		case []uint32:
			for _, a := range addresses {
				raw := [4]byte{byte(a), byte(a >> 8), byte(a >> 16), byte(a >> 24)}
				p.DNS = append(p.DNS, netip.AddrFrom4(raw).String())
			}
		case [][]byte:
			for _, a := range addresses {
				ip, ok := netip.AddrFromSlice(a)
				if !ok {
					return p, errors.New("network_dns_unavailable")
				}
				p.DNS = append(p.DNS, ip.String())
			}
		default:
			return p, errors.New("network_dns_unavailable")
		}
	}
	if v, ok := values["dns-search"]; ok {
		if err := dbus.Store([]any{v.Value()}, &p.Search); err != nil {
			return p, errors.New("network_dns_unavailable")
		}
	}
	return p, nil
}
func addressData(v dbus.Variant) ([]string, error) {
	var entries []map[string]dbus.Variant
	if err := dbus.Store([]any{v.Value()}, &entries); err != nil {
		return nil, errors.New("network_address_data_invalid")
	}
	var result []string
	for _, entry := range entries {
		address, ok := entry["address"].Value().(string)
		prefix, pok := entry["prefix"].Value().(uint32)
		if !ok || !pok {
			return nil, errors.New("network_address_data_invalid")
		}
		ip, err := netip.ParseAddr(address)
		if err != nil || prefix > uint32(ip.BitLen()) {
			return nil, errors.New("network_address_data_invalid")
		}
		if ip.IsLinkLocalUnicast() {
			continue
		}
		result = append(result, ip.String()+"/"+strconv.Itoa(int(prefix)))
	}
	return result, nil
}
func routeData(v dbus.Variant) ([]Route, error) {
	var entries []map[string]dbus.Variant
	if err := dbus.Store([]any{v.Value()}, &entries); err != nil {
		return nil, errors.New("network_route_data_invalid")
	}
	var result []Route
	for _, entry := range entries {
		dest, ok := entry["dest"].Value().(string)
		prefix, pok := entry["prefix"].Value().(uint32)
		if !ok || !pok {
			return nil, errors.New("network_route_data_invalid")
		}
		ip, err := netip.ParseAddr(dest)
		if err != nil || prefix > uint32(ip.BitLen()) {
			return nil, errors.New("network_route_data_invalid")
		}
		if ip.IsLinkLocalUnicast() {
			continue
		}
		hop, _ := entry["next-hop"].Value().(string)
		if hop == "0.0.0.0" || hop == "::" {
			hop = ""
		}
		metric, _ := entry["metric"].Value().(uint32)
		result = append(result, Route{Destination: ip.String() + "/" + strconv.Itoa(int(prefix)), NextHop: hop, Metric: metric})
	}
	return result, nil
}
func replaceIP(settings settingsMap, family string, p IPSettings) {
	values := settings[family]
	if values == nil {
		values = map[string]dbus.Variant{}
		settings[family] = values
	}
	for _, key := range []string{"addresses", "routes", "dns", "address-data", "route-data", "gateway"} {
		delete(values, key)
	}
	values["method"] = dbus.MakeVariant(p.Method)
	values["never-default"] = dbus.MakeVariant(p.NeverDefault)
	addresses := make([]map[string]dbus.Variant, 0, len(p.Addresses))
	for _, a := range p.Addresses {
		prefix, err := netip.ParsePrefix(a)
		if err != nil {
			continue
		}
		addresses = append(addresses, map[string]dbus.Variant{"address": dbus.MakeVariant(prefix.Addr().String()), "prefix": dbus.MakeVariant(uint32(prefix.Bits()))})
	}
	values["address-data"] = dbus.MakeVariant(addresses)
	routes := make([]map[string]dbus.Variant, 0, len(p.Routes))
	for _, r := range p.Routes {
		prefix, err := netip.ParsePrefix(r.Destination)
		if err != nil {
			continue
		}
		entry := map[string]dbus.Variant{"dest": dbus.MakeVariant(prefix.Addr().String()), "prefix": dbus.MakeVariant(uint32(prefix.Bits()))}
		if r.NextHop != "" {
			entry["next-hop"] = dbus.MakeVariant(r.NextHop)
		}
		if r.Metric != 0 {
			entry["metric"] = dbus.MakeVariant(r.Metric)
		}
		routes = append(routes, entry)
	}
	values["route-data"] = dbus.MakeVariant(routes)
	if p.Gateway != "" {
		values["gateway"] = dbus.MakeVariant(p.Gateway)
	}
	dns := append([]string{}, p.DNS...)
	search := append([]string{}, p.Search...)
	values["dns-data"] = dbus.MakeVariant(dns)
	values["dns-search"] = dbus.MakeVariant(search)
}

func (t *NMTransport) storedProfile(ctx context.Context, cp dbus.ObjectPath, iface, device string) (Profile, error) {
	version, err := t.get(ctx, cp, ProfileInterface, "VersionId")
	if err != nil {
		return Profile{}, err
	}
	before, err := ProfileVersion(version.Value())
	if err != nil {
		return Profile{}, err
	}
	body, err := t.read(ctx, t.process.identity.Unique, cp, ProfileInterface, "GetSettings")
	if err != nil {
		return Profile{}, err
	}
	var settings settingsMap
	if err := storeReply(body, &settings); err != nil {
		return Profile{}, err
	}
	uuid, _ := settings["connection"]["uuid"].Value().(string)
	p := Profile{UUID: uuid, Interface: iface, Device: device, Connection: string(cp), ProfileVersion: before}
	p.IPv4, err = readIP(settings["ipv4"])
	if err != nil {
		return p, err
	}
	p.IPv6, err = readIP(settings["ipv6"])
	if err != nil {
		return p, err
	}
	name, err := t.get(ctx, cp, ProfileInterface, "Filename")
	if err != nil {
		return p, err
	}
	p.Filename, _ = name.Value().(string)
	flags, err := t.get(ctx, cp, ProfileInterface, "Flags")
	if err != nil {
		return p, err
	}
	var ok bool
	p.Flags, ok = flags.Value().(uint32)
	if !ok || !uuidShape(p.UUID) {
		return p, errors.New("network_profile_storage_unavailable")
	}
	afterValue, err := t.get(ctx, cp, ProfileInterface, "VersionId")
	if err != nil {
		return p, err
	}
	after, err := ProfileVersion(afterValue.Value())
	if err != nil {
		return p, err
	}
	return p, VersionBracket(before, after)
}
