// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import "testing"

func TestMCPGatewayProbeReasonIsClosed(t *testing.T) {
	for _, c := range []struct {
		probe MCPGatewayProbe
		valid bool
	}{
		{MCPGatewayProbe{State: "unreachable", Reason: "dns"}, true},
		{MCPGatewayProbe{State: "unreachable", Reason: "http_status", HTTPStatus: 404}, true},
		{MCPGatewayProbe{State: "unreachable", Reason: "http_status"}, false},
		{MCPGatewayProbe{State: "unreachable", Reason: "dns", HTTPStatus: 404}, false},
		{MCPGatewayProbe{State: "unreachable", Reason: "http_status", HTTPStatus: 200}, false},
		{MCPGatewayProbe{State: "unreachable", Reason: "the server said no"}, false},
		{MCPGatewayProbe{State: "refused", Reason: "dns"}, false},
	} {
		if got := validMCPGatewayProbe(c.probe, false); got != c.valid {
			t.Errorf("%+v: valid %v, want %v", c.probe, got, c.valid)
		}
	}
}
