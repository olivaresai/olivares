// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package mcpgateway

import (
	"context"
	"errors"
	"testing"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
)

// At the memory bound a new client is served and the client idle longest is the one dropped,
// which then initializes again; the table never refuses and never grows past the bound.
func TestManagedMCPForwarderDropsTheLeastRecentlyUsedPrincipalAtTheLimit(t *testing.T) {
	f := newScaleForwarder(newScaleUpstream(t))
	for i := range managedSessionLimit {
		handshake(t, f, i)
	}
	use := func(i int) (mcpc.UpstreamResult, error) {
		return f.Forward(context.Background(), scalePrincipal("tools/list", i))
	}
	if res, err := use(0); err != nil || res.State != mcpc.DispatchCompleted { // client 1 is now the idlest
		t.Fatalf("client 0: state=%s err=%v", res.State, err)
	}
	handshake(t, f, managedSessionLimit)
	if len(f.sessions) != managedSessionLimit {
		t.Fatalf("%d sessions kept, want %d", len(f.sessions), managedSessionLimit)
	}
	if res, err := use(1); !errors.Is(err, errManagedInitializeRequired) || res.State != mcpc.DispatchNotSent {
		t.Fatalf("the idlest client was not dropped: state=%s err=%v", res.State, err)
	}
	for _, i := range []int{0, managedSessionLimit} {
		if res, err := use(i); err != nil || res.State != mcpc.DispatchCompleted {
			t.Fatalf("client %d lost its session: state=%s err=%v", i, res.State, err)
		}
	}
	// A client that still has a session and initializes again takes no other client's place.
	handshake(t, f, 5)
	if len(f.sessions) != managedSessionLimit {
		t.Fatalf("%d sessions kept after a re-initialize, want %d", len(f.sessions), managedSessionLimit)
	}
	for _, i := range []int{2, 5} {
		if res, err := use(i); err != nil || res.State != mcpc.DispatchCompleted {
			t.Fatalf("client %d after the re-initialize: state=%s err=%v", i, res.State, err)
		}
	}
	handshake(t, f, 1)
	if res, err := use(1); err != nil || res.State != mcpc.DispatchCompleted {
		t.Fatalf("client 1 after coming back: state=%s err=%v", res.State, err)
	}
}
