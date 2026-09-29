// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package portal

import (
	"net"
	"testing"
)

func TestLocalActivation_RequiresBothNameAndFixedUnixPath(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		want       bool
	}{
		{"local", "/run/olivares-portal-api/local.sock", true}, {"", "/run/olivares-portal-api/local.sock", false}, {"other", "/run/olivares-portal-api/local.sock", false}, {"local", "/tmp/portal.sock", false},
	} {
		if got := localActivation(tc.name, &net.UnixAddr{Name: tc.path, Net: "unix"}, true); got != tc.want {
			t.Errorf("%q %q: %v", tc.name, tc.path, got)
		}
	}
	if localActivation("local", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9443}, true) {
		t.Fatal("TCP accepted as local transport")
	}
}

func TestLocalActivation_TLSCustodyRemainsMandatory(t *testing.T) {
	if localActivation("local", &net.UnixAddr{Name: "/run/olivares-portal-api/local.sock", Net: "unix"}, false) {
		t.Fatal("local activation bypasses rejected TLS custody")
	}
}
