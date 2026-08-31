// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"
)

func TestStaticCredentialProvider(t *testing.T) {
	p := &staticCredentialProvider{authHeader: "Bearer static-secret"}
	hdr, err := p.Credential(context.Background(), "https://upstream.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if hdr != "Bearer static-secret" {
		t.Fatalf("expected static header, got %q", hdr)
	}
}

func TestStaticCredentialProviderNeverReturnsInboundBearer(t *testing.T) {
	inbound := "Bearer eyJhbGciOiJSUzI1NiJ9.inbound-token"
	p := &staticCredentialProvider{authHeader: "Bearer separate-upstream-cred"}
	hdr, _ := p.Credential(context.Background(), "https://target.example.com")
	if hdr == inbound {
		t.Fatal("credential provider must NEVER return the inbound bearer")
	}
}
