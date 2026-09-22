// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package answers

import (
	"encoding/base64"
	"encoding/binary"
	"net"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

func validate(d *document) error {
	if d.SchemaVersion != "appliance-answers/v1" {
		return invalid("$.schema_version", "expected appliance-answers/v1")
	}
	switch d.Source {
	case "file", "nocloud", "guestinfo", "systemd-credential", "local-assistant":
	default:
		return invalid("$.source", "select exactly one supported provenance label")
	}
	if d.Host.Owner != "cloud-init" {
		return invalid("$.host.owner", "only cloud-init ownership is supported; local host adapter is unimplemented")
	}
	if !dnsName(d.Host.Hostname) || localhost(d.Host.Hostname) {
		return invalid("$.host.hostname", "expected a DNS hostname of at most 253 ASCII bytes")
	}
	d.Host.Hostname = strings.ToLower(d.Host.Hostname)
	if d.Host.Network.Mode != "dhcp" {
		return invalid("$.host.network.mode", "only dhcp prerequisites are supported")
	}
	if d.Host.Time.Timezone != "UTC" {
		return invalid("$.host.time.timezone", "only UTC prerequisites are supported")
	}
	if len(d.Host.Time.Servers) < 1 || len(d.Host.Time.Servers) > 8 {
		return invalid("$.host.time.servers", "expected 1 to 8 distinct time servers")
	}
	for i, value := range d.Host.Time.Servers {
		normal, ok := endpointHost(value)
		if !ok {
			return invalid("$.host.time.servers["+strconv.Itoa(i)+"]", "expected a DNS name or unicast IP")
		}
		d.Host.Time.Servers[i] = normal
	}
	if duplicate(d.Host.Time.Servers) {
		return invalid("$.host.time.servers", "duplicate time server")
	}
	if len(d.Host.SSHAuthorizedKeys) < 1 {
		return invalid("$.host.ssh_authorized_keys", "expected 1 to 16 SSH public keys")
	}
	for i, key := range d.Host.SSHAuthorizedKeys {
		if !sshPublicKey(key) {
			return invalid("$.host.ssh_authorized_keys["+strconv.Itoa(i)+"]", "expected an ssh-ed25519 public key without options or comment")
		}
	}
	if duplicate(d.Host.SSHAuthorizedKeys) {
		return invalid("$.host.ssh_authorized_keys", "duplicate SSH public key")
	}
	// Vocabulary follows cmd/olivares/cmd_config.go. Supporting postgres-prod also
	// requires protected DSN and signing-key references, which are not implemented here.
	if d.Product.StorageProfile != "single-node-prod" {
		return invalid("$.product.storage_profile", "only single-node-prod (SQLite) is supported")
	}
	console, ok := consoleURL(d.Product.PublicConsoleURL)
	if !ok {
		return invalid("$.product.public_console_url", "expected an HTTPS origin without credentials, query or fragment")
	}
	d.Product.PublicConsoleURL = console
	// These are the public update channels in core/release/manifest.go. This module
	// deliberately has no engine dependency or runtime configuration generator.
	switch d.Product.UpdateChannel {
	case "stable", "security", "lts":
	default:
		return invalid("$.product.update_channel", "expected stable, security or lts")
	}
	if d.Product.NodeRole != "control" {
		return invalid("$.product.node_role", "only control is supported; inference is not implemented")
	}
	return nil
}

// Set-valued prerequisites have no ordering significance. Sorting after normalization
// makes the plan stable; duplicate refusal avoids hiding contradictory repeated inputs.
func duplicate(values []string) bool {
	sort.Strings(values)
	for i := 1; i < len(values); i++ {
		if values[i] == values[i-1] {
			return true
		}
	}
	return false
}

func localhost(name string) bool {
	name = strings.ToLower(name)
	return name == "localhost" || strings.HasSuffix(name, ".localhost")
}

func dnsName(name string) bool {
	if len(name) < 1 || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-':
			default:
				return false
			}
		}
	}
	return true
}

func endpointHost(value string) (string, bool) {
	if address, err := netip.ParseAddr(value); err == nil {
		address = address.Unmap()
		return address.String(), address.Zone() == "" && address.IsGlobalUnicast() && !address.IsLoopback()
	}
	if !dnsName(value) || localhost(value) {
		return "", false
	}
	name := strings.ToLower(value)
	// Reject malformed numeric IP lookalikes instead of reinterpreting them as DNS.
	if strings.Trim(name, "0123456789.") == "" {
		return "", false
	}
	return name, true
}

func sshPublicKey(key string) bool {
	parts := strings.Split(key, " ")
	if len(parts) != 2 || parts[0] != "ssh-ed25519" {
		return false
	}
	if strings.ContainsAny(parts[1], "\t\r\n") {
		return false
	}
	blob, err := base64.StdEncoding.Strict().DecodeString(parts[1])
	if err != nil || len(blob) != 51 {
		return false
	}
	return binary.BigEndian.Uint32(blob[:4]) == 11 && string(blob[4:15]) == "ssh-ed25519" && binary.BigEndian.Uint32(blob[15:19]) == 32
}

func consoleURL(value string) (string, bool) {
	if strings.ContainsAny(value, " \t\r\n%?#") {
		return "", false
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || u.Host == "" || (u.Path != "" && u.Path != "/") {
		return "", false
	}
	hostname, ok := endpointHost(u.Hostname())
	if !ok {
		return "", false
	}
	port := u.Port()
	if port == "" && strings.HasSuffix(u.Host, ":") {
		return "", false
	}
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", false
		}
		if number == 443 {
			port = ""
		} else {
			port = strconv.Itoa(number)
		}
	}
	authority := hostname
	if port != "" {
		authority = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		authority = "[" + hostname + "]"
	}
	return "https://" + authority, true
}
