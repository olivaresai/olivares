// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/olivaresai/olivares/appliance/layer/portal"
)

// kernelHostName is where the kernel states the host name.
const kernelHostName = "/proc/sys/kernel/hostname"

// networkManager is NetworkManager's name and object on the system bus.
const (
	networkManager     = "org.freedesktop.NetworkManager"
	networkManagerPath = dbus.ObjectPath("/org/freedesktop/NetworkManager")
)

// busTimeout bounds each question to the system bus.
const busTimeout = 5 * time.Second

// liveFacts is the installed host's statement of the portal's origin and addresses.
//
// The origin is the host name the kernel states. The addresses follow the portal's published
// selection: loopback while the portal listens locally, and otherwise every address, link-local
// excepted, NetworkManager reports now on the selected management interfaces. The helper runs with
// no network of its own, so it asks NetworkManager over the system bus rather than the kernel.
type liveFacts struct{}

// Origin implements hostFacts.
func (liveFacts) Origin() (string, error) {
	data, err := os.ReadFile(kernelHostName)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// Addresses implements hostFacts.
func (liveFacts) Addresses() ([]netip.Addr, error) {
	selection, reason := portal.ReadSelection(portal.SelectionFile)
	if reason != "" {
		return nil, errors.New("the published selection cannot be read")
	}
	if selection.Listen != portal.ListenManagement {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.IPv6Loopback()}, nil
	}
	return managementAddresses(selection.ManagementInterfaces)
}

// managementAddresses asks NetworkManager for the current addresses of the named interfaces.
func managementAddresses(interfaces []string) ([]netip.Addr, error) {
	conn, err := dbus.SystemBusPrivate()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	ready := make(chan error, 1)
	go func() {
		if err := conn.Auth(nil); err != nil {
			ready <- err
			return
		}
		ready <- conn.Hello()
	}()
	select {
	case err := <-ready:
		if err != nil {
			return nil, err
		}
	case <-time.After(busTimeout):
		return nil, errors.New("the system bus did not answer")
	}
	ctx, cancel := context.WithTimeout(context.Background(), busTimeout)
	defer cancel()
	var devices []dbus.ObjectPath
	if err := conn.Object(networkManager, networkManagerPath).CallWithContext(ctx, networkManager+".GetDevices", 0).Store(&devices); err != nil {
		return nil, err
	}
	var addresses []netip.Addr
	for _, path := range devices {
		device := conn.Object(networkManager, path)
		name, err := device.GetProperty(networkManager + ".Device.Interface")
		if err != nil {
			return nil, err
		}
		if text, ok := name.Value().(string); !ok || !slices.Contains(interfaces, text) {
			continue
		}
		for _, family := range []struct{ config, iface string }{
			{networkManager + ".Device.Ip4Config", networkManager + ".IP4Config"},
			{networkManager + ".Device.Ip6Config", networkManager + ".IP6Config"},
		} {
			found, err := familyAddresses(conn, device, family.config, family.iface)
			if err != nil {
				return nil, err
			}
			addresses = append(addresses, found...)
		}
	}
	if len(addresses) == 0 {
		return nil, errors.New("no management interface has an address")
	}
	return addresses, nil
}

// familyAddresses reads one address family's AddressData of a device's current configuration.
func familyAddresses(conn *dbus.Conn, device dbus.BusObject, configProperty, configInterface string) ([]netip.Addr, error) {
	value, err := device.GetProperty(configProperty)
	if err != nil {
		return nil, err
	}
	config, ok := value.Value().(dbus.ObjectPath)
	if !ok {
		return nil, errors.New("a device states no configuration object")
	}
	if config == "/" {
		return nil, nil
	}
	data, err := conn.Object(networkManager, config).GetProperty(configInterface + ".AddressData")
	if err != nil {
		return nil, err
	}
	rows, ok := data.Value().([]map[string]dbus.Variant)
	if !ok {
		return nil, errors.New("a configuration states its addresses in another shape")
	}
	var addresses []netip.Addr
	for _, row := range rows {
		text, ok := row["address"].Value().(string)
		if !ok {
			return nil, errors.New("an address row states no address")
		}
		addr, err := netip.ParseAddr(text)
		if err != nil {
			return nil, err
		}
		if !addr.IsLinkLocalUnicast() {
			addresses = append(addresses, addr)
		}
	}
	return addresses, nil
}
