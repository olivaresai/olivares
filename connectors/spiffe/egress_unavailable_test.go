//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package spiffe

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
)

type communityAssertionSource struct {
	svid  *jwtsvid.SVID
	calls int
}

func (f *communityAssertionSource) FetchJWTSVID(context.Context, string, ...string) (*jwtsvid.SVID, error) {
	f.calls++
	return f.svid, nil
}

func TestCommunityAnthropicAssertionUnavailable(t *testing.T) {
	// The shared workload fixture mints a valid, signed JWT-SVID with the
	// provider's actual audience, so refusal cannot be credited to bad input.
	f := &communityAssertionSource{svid: mintAnthropicSVID(t, "spiffe://corp.example/workload/edition-probe", []string{AnthropicAudience})}
	token, err := FetchAnthropicAssertion(context.Background(), f, func(string) (string, bool) { return "", false })
	if err == nil || !strings.Contains(err.Error(), "Business") {
		t.Error("assertion fetch must return an explicit Business-unavailable error")
	}
	if token != "" {
		t.Error("unavailable assertion fetch returned credential data")
	}
	if f.calls != 0 {
		t.Errorf("Community fetched %d JWT-SVIDs, want none", f.calls)
	}
	claims, err := InspectAnthropicAssertion(f.svid.Marshal())
	if err == nil || !strings.Contains(err.Error(), "Business") || !reflect.DeepEqual(claims, SVIDClaims{}) {
		t.Error("unavailable inspection must return Business error and zero claims")
	}
	lookups := 0
	err = AssertNoStaticKeyShadowing(func(string) (string, bool) { lookups++; return "", false })
	if err == nil || !strings.Contains(err.Error(), "Business") || lookups != 0 {
		t.Error("unavailable federation guard must refuse before accessing environment credentials")
	}
}
