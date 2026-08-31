// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/license"
)

// refusingCredProvider stands in for the enterprise credential minter refusing for want of
// an entitlement. The real minter lives in the closed overlay; what this file pins is the
// TRANSLATION at the seam, which is the part that lives here.
type refusingCredProvider struct{ err error }

func (p refusingCredProvider) Credential(context.Context, string) (string, error) {
	return "", p.err
}

// The MCP leg must distinguish "your add-on is not entitled" from "the upstream is down".
// Before this, both left as an opaque credential error and the client was told the wrong
// thing about its own deployment.
//
// Two properties are asserted together because either alone is misleading: the refusal is
// recognizable (a caller can match it), and the dispatch state stays NOT_SENT — which is
// provably true, since we refuse before http.Client.Do, and which the dispatch
// contract depends on.
func TestMCPCredentialRefusalIsStableAndNotSent(t *testing.T) {
	f := &mcpUpstreamForwarder{
		url:      "https://upstream.invalid/mcp",
		credProv: refusingCredProvider{err: license.AddonRequired("credminter", "mcp.upstream.credential")},
	}

	res, err := f.Forward(context.Background(), mcpc.UpstreamRequest{Method: "tools/call"})
	if err == nil {
		t.Fatal("an entitlement refusal must fail the leg, not proceed unauthenticated")
	}
	if res.State != mcpc.DispatchNotSent {
		t.Fatalf("dispatch state = %v, want not_sent — we refuse before the request is transmitted", res.State)
	}
	if !errors.Is(err, errMCPAddonRequired) {
		t.Fatalf("the refusal is not recognizable as an add-on refusal: %v", err)
	}
	// And the underlying core sentinel survives, so a caller may match either level.
	if !errors.Is(err, license.ErrAddonRequiresLicense) {
		t.Fatalf("the core sentinel did not survive the wrap: %v", err)
	}
	for _, want := range []string{"credminter", "mcp.upstream.credential", "NOT sent"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not mention %q", err.Error(), want)
		}
	}
}

// The control: an ordinary credential failure must NOT be dressed up as an entitlement
// refusal. Conflating them in the other direction would be the same defect mirrored.
func TestMCPOrdinaryCredentialFailureIsNotAnAddonRefusal(t *testing.T) {
	f := &mcpUpstreamForwarder{
		url:      "https://upstream.invalid/mcp",
		credProv: refusingCredProvider{err: errors.New("token exchange timed out")},
	}
	res, err := f.Forward(context.Background(), mcpc.UpstreamRequest{Method: "tools/call"})
	if err == nil {
		t.Fatal("a credential failure must fail the leg (deny-closed)")
	}
	if res.State != mcpc.DispatchNotSent {
		t.Fatalf("dispatch state = %v, want not_sent", res.State)
	}
	if errors.Is(err, errMCPAddonRequired) || errors.Is(err, license.ErrAddonRequiresLicense) {
		t.Fatalf("an ordinary credential failure was reported as an entitlement refusal: %v", err)
	}
}
