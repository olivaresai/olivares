// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package tui

import (
	"context"
	"github.com/olivaresai/olivares/appliance/layer/netguard"
	"github.com/olivaresai/olivares/appliance/layer/portal/auth"
	"strings"
	"testing"
)

type networkFixture struct{ calls []netguard.EdgeRequest }

func (f *networkFixture) Call(_ context.Context, r netguard.EdgeRequest) (netguard.EdgeResponse, error) {
	f.calls = append(f.calls, r)
	if r.Action == "status" {
		return netguard.EdgeResponse{Code: "ok", OpenWindows: []netguard.Status{{OperationID: "0123456789abcdef0123456789abcdef", State: netguard.StateAwaitingConfirmation}}}, nil
	}
	return netguard.EdgeResponse{Code: "ok", Status: &netguard.Status{OperationID: r.OperationID, State: netguard.StateRolledBack}}, nil
}
func TestConsole_NetworkVerbRefusesBeforeQualifiedSignIn(t *testing.T) {
	f := &networkFixture{}
	c := NewConsole(auth.State{}).WithNetwork(f)
	got := c.Network("restore")
	if len(f.calls) != 0 || !strings.Contains(got, "qualified sign-in") {
		t.Fatal(got, f.calls)
	}
}
func TestConsole_AuthenticatedNetworkVerbRestoresTheLastConfirmedProfiles(t *testing.T) {
	f := &networkFixture{}
	c := signedIn(NewConsole(auth.State{}).WithNetwork(f))
	got := c.Network("restore")
	if len(f.calls) != 2 || f.calls[1].Action != "revert" || f.calls[1].OperationID != "0123456789abcdef0123456789abcdef" || !strings.Contains(got, "rolled_back") {
		t.Fatal(got, f.calls)
	}
}
