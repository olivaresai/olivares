// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package base

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// ReadNetworkManager reads active keyfile profile facts through a private D-Bus
// connection. It invokes no program and sends no NetworkManager mutation.
func ReadNetworkManager(ctx context.Context) ([]NetworkProfile, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := dbus.SystemBusPrivate(dbus.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.Auth(nil); err != nil {
		return nil, err
	}
	if err := conn.Hello(); err != nil {
		return nil, err
	}
	var owner string
	if err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.GetNameOwner", dbus.FlagNoAutoStart, "org.freedesktop.NetworkManager").Store(&owner); err != nil {
		return nil, err
	}
	call := func(path dbus.ObjectPath, method string, args ...any) *dbus.Call {
		return conn.Object(owner, path).CallWithContext(ctx, method, dbus.FlagNoAutoStart, args...)
	}
	get := func(path dbus.ObjectPath, iface, name string) (dbus.Variant, error) {
		var v dbus.Variant
		err := call(path, "org.freedesktop.DBus.Properties.Get", iface, name).Store(&v)
		return v, err
	}
	const device = "org.freedesktop.NetworkManager.Device"
	const connection = "org.freedesktop.NetworkManager.Settings.Connection"
	var devices []dbus.ObjectPath
	if err := call("/org/freedesktop/NetworkManager", "org.freedesktop.NetworkManager.GetDevices").Store(&devices); err != nil {
		return nil, err
	}
	var result []NetworkProfile
	for _, path := range devices {
		name, err := get(path, device, "Interface")
		if err != nil {
			return nil, err
		}
		iface, ok := name.Value().(string)
		if !ok {
			return nil, errors.New("invalid interface type")
		}
		if iface == "lo" {
			continue
		}
		managed, err := get(path, device, "Managed")
		if err != nil {
			return nil, err
		}
		isManaged, ok := managed.Value().(bool)
		if !ok {
			return nil, errors.New("invalid managed type")
		}
		active, err := get(path, device, "ActiveConnection")
		if err != nil {
			return nil, err
		}
		activePath, ok := active.Value().(dbus.ObjectPath)
		if !ok || activePath == "/" {
			continue
		}
		cv, err := get(activePath, "org.freedesktop.NetworkManager.Connection.Active", "Connection")
		if err != nil {
			return nil, err
		}
		cp, ok := cv.Value().(dbus.ObjectPath)
		if !ok || cp == "/" {
			return nil, errors.New("active profile unavailable")
		}
		filename, err := get(cp, connection, "Filename")
		if err != nil {
			return nil, err
		}
		file, ok := filename.Value().(string)
		if !ok {
			return nil, errors.New("invalid filename type")
		}
		var settings map[string]map[string]dbus.Variant
		if err := call(cp, connection+".GetSettings").Store(&settings); err != nil {
			return nil, err
		}
		var applied map[string]map[string]dbus.Variant
		var version uint64
		if err := call(path, device+".GetAppliedConnection", uint32(0)).Store(&applied, &version); err != nil {
			return nil, err
		}
		if version == 0 {
			return nil, errors.New("applied version unavailable")
		}
		p := NetworkProfile{Interface: iface, Managed: isManaged, Filename: file}
		p.IPv4Method, p.IPv4, p.Gateway4 = networkFamily(settings["ipv4"])
		p.IPv6Method, p.IPv6, p.Gateway6 = networkFamily(settings["ipv6"])
		for _, family := range []string{"ipv4", "ipv6"} {
			pm, pa, pg := networkFamily(settings[family])
			am, aa, ag := networkFamily(applied[family])
			if pm != am || !sameStrings(pa, aa) || pg != ag {
				return nil, errors.New("stored and applied profile differ")
			}
			if servers, ok := applied[family]["dns-data"].Value().([]string); ok {
				p.DNSServers = append(p.DNSServers, servers...)
			}
			if search, ok := applied[family]["dns-search"].Value().([]string); ok {
				p.DNSSearch = appendSearch(p.DNSSearch, search...)
			}
		}
		result = append(result, p)
	}
	return result, nil
}

// appendSearch keeps NetworkManager's search order: the IPv4 suffixes, then IPv6 suffixes
// not already present. The first occurrence of a suffix keeps its precedence.
func appendSearch(list []string, values ...string) []string {
	for _, v := range values {
		if !slices.ContainsFunc(list, func(s string) bool { return strings.EqualFold(s, v) }) {
			list = append(list, v)
		}
	}
	return list
}

func networkFamily(settings map[string]dbus.Variant) (string, []string, string) {
	method, _ := settings["method"].Value().(string)
	gateway, _ := settings["gateway"].Value().(string)
	var addresses []string
	if data, ok := settings["address-data"].Value().([]map[string]dbus.Variant); ok {
		for _, entry := range data {
			address, ok := entry["address"].Value().(string)
			prefix, pok := entry["prefix"].Value().(uint32)
			if !ok || !pok {
				continue
			}
			if a, err := netip.ParseAddr(address); err == nil {
				addresses = append(addresses, a.String()+"/"+strconv.Itoa(int(prefix)))
			}
		}
	}
	return method, addresses, gateway
}
